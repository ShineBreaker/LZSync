package deploy

// stable 端（StorageLegacy）投放测试：不依赖 realm-core——仓内字节 +
// hub 行 → Deploy → 断言 Songs/ 链接目录、collection.db 原子重写与
// 现场复核三路跳过（同内容/不同版本/仓缺字节）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/legacydb"
)

// legacyDeps 聚合 stable 投放测试依赖。
type legacyDeps struct {
	hub    *hub.Store
	engine *filestore.Engine
	runner *Runner
}

func newLegacyDeployDeps(t *testing.T) *legacyDeps {
	t.Helper()
	h := openMemHub(t)
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	engine := filestore.NewEngine(store)
	if _, err := engine.Probe(); err != nil {
		t.Fatalf("链接能力探测: %v", err)
	}
	return &legacyDeps{
		hub:    h,
		engine: engine,
		runner: NewRunner(Deps{Hub: h, Files: engine, Guard: guard.NewGuard(nil)}),
	}
}

// seedFile 把字节写入临时源文件后收仓，返回 (hash, NamedFile)。
func seedFile(t *testing.T, d *legacyDeps, name string, content []byte) (string, hub.NamedFile) {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	if err := d.engine.Store.Put(src); err != nil {
		t.Fatalf("收仓 %s: %v", name, err)
	}
	return hash, hub.NamedFile{Hash: hash, Filename: name}
}

// seedSet 构造并入库一个双难度谱面集（字节进仓），返回集 DTO。
func seedSet(t *testing.T, d *legacyDeps) hub.BeatmapSet {
	t.Helper()
	osu1 := []byte("osu file format v14\r\n\r\n[Metadata]\r\nTitle:Til Death\r\nArtist:Digital Sexy\r\nVersion:Expert\r\n")
	osu2 := []byte("osu file format v14\r\n\r\n[Metadata]\r\nTitle:Til Death\r\nArtist:Digital Sexy\r\nVersion:Normal\r\n")
	audio := []byte("mp3-bytes")
	h1, f1 := seedFile(t, d, "a (Sotarks) [Expert].osu", osu1)
	h2, f2 := seedFile(t, d, "a (Sotarks) [Normal].osu", osu2)
	_, f3 := seedFile(t, d, "audio.mp3", audio)
	setHasher := sha256.New()
	setHasher.Write(osu1)
	setHasher.Write(osu2)
	bs := hub.BeatmapSet{
		Hash:           hex.EncodeToString(setHasher.Sum(nil)),
		OnlineID:       1002716,
		Artist:         "Digital Sexy",
		Title:          "Til Death (Nightcore Mix)",
		AuthorUsername: "Sotarks",
		Beatmaps: []hub.Beatmap{
			{Hash: h1, DifficultyName: "Expert", RulesetShortName: "osu"},
			{Hash: h2, DifficultyName: "Normal", RulesetShortName: "osu"},
		},
		Files: []hub.NamedFile{f1, f2, f3},
	}
	snap := hub.Snapshot{ForkID: "lazer", CollectedAt: t0, BeatmapSets: []hub.BeatmapSet{bs}}
	if _, err := d.hub.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatalf("入库: %v", err)
	}
	return bs
}

func TestLegacyDeploySetAndCollection(t *testing.T) {
	d := newLegacyDeployDeps(t)
	bs := seedSet(t, d)
	stableDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stableDir, "Songs"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		TargetFork: "stable",
		Target:     forks.LibraryRef{ForkID: "stable", DataDir: stableDir},
		Items: []PlanItem{
			{Kind: hub.KindBeatmapSet, ContentKey: bs.Hash, Action: ActionCreate},
			{Kind: hub.KindCollection, ContentKey: "favourites", Action: ActionCreate,
				PayloadJSON: `{"Name":"favourites","BeatmapMD5Hashes":["aa","bb"]}`},
		},
	}
	report, err := d.runner.Deploy(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("applied = %+v（期望集+收藏两项）", report.Applied)
	}

	// 集目录落地：目录名 <setID> <artist> - <title>；三文件全部为链接。
	setDir := filepath.Join(stableDir, "Songs", "1002716 Digital Sexy - Til Death (Nightcore Mix)")
	for _, name := range []string{"a (Sotarks) [Expert].osu", "a (Sotarks) [Normal].osu", "audio.mp3"} {
		info, serr := os.Lstat(filepath.Join(setDir, name))
		if serr != nil {
			t.Fatalf("文件未落地 %s: %v", name, serr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s 不是链接", name)
		}
	}

	// collection.db 重写：可解析、成员为决定值。
	cdata, err := os.ReadFile(filepath.Join(stableDir, "collection.db"))
	if err != nil {
		t.Fatalf("collection.db: %v", err)
	}
	cdb, err := legacydb.ParseCollectionDB(cdata)
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if len(cdb.Collections) != 1 || cdb.Collections[0].Name != "favourites" {
		t.Fatalf("collections = %+v", cdb.Collections)
	}
	if len(cdb.Collections[0].BeatmapMD5s) != 2 || cdb.Collections[0].BeatmapMD5s[0] != "aa" {
		t.Fatalf("成员 = %v", cdb.Collections[0].BeatmapMD5s)
	}

	// 同内容重投：现场复核跳过，不重复落地。
	report2, err := d.runner.Deploy(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("重投: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Fatalf("重投 applied = %+v（期望全跳过）", report2.Applied)
	}
	if len(report2.Skipped) == 0 {
		t.Fatal("重投应有跳过计数")
	}
}

func TestLegacyDeploySetSkips(t *testing.T) {
	d := newLegacyDeployDeps(t)
	bs := seedSet(t, d)

	t.Run("仓缺字节", func(t *testing.T) {
		stableDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(stableDir, "Songs"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Files 引用仓内不存在的哈希（来源收集不完整形态）。
		missing := bs
		missing.Hash = strings.Repeat("f", 64)
		for i := range missing.Files {
			missing.Files[i].Hash = strings.Repeat("e", 64)
		}
		plan := Plan{TargetFork: "stable",
			Target: forks.LibraryRef{ForkID: "stable", DataDir: stableDir},
			Items: []PlanItem{{Kind: hub.KindBeatmapSet, ContentKey: missing.Hash, Action: ActionCreate,
				PayloadJSON: mustJSON(t, missing)}}}
		report, err := d.runner.Deploy(context.Background(), plan, nil)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if len(report.Applied) != 0 || len(report.Skipped) == 0 {
			t.Fatalf("applied=%+v skipped=%+v（期望跳过）", report.Applied, report.Skipped)
		}
	})

	t.Run("目标已有不同版本", func(t *testing.T) {
		stableDir := t.TempDir()
		setDir := filepath.Join(stableDir, "Songs", "1002716 Digital Sexy - Til Death (Nightcore Mix)")
		if err := os.MkdirAll(setDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// 既有目录只放一个不同的 .osu（难度集合不同 → 版本分歧）。
		if err := os.WriteFile(filepath.Join(setDir, "x [Other].osu"), []byte("different"), 0o644); err != nil {
			t.Fatal(err)
		}
		plan := Plan{TargetFork: "stable",
			Target: forks.LibraryRef{ForkID: "stable", DataDir: stableDir},
			Items:  []PlanItem{{Kind: hub.KindBeatmapSet, ContentKey: bs.Hash, Action: ActionCreate}}}
		report, err := d.runner.Deploy(context.Background(), plan, nil)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if len(report.Applied) != 0 {
			t.Fatalf("版本分歧应跳过，applied = %+v", report.Applied)
		}
	})
}

func TestLegacyCollectionMergeKeepsOthers(t *testing.T) {
	d := newLegacyDeployDeps(t)
	stableDir := t.TempDir()
	// 既有 collection.db 含另一条收藏（保留验证）+ 同名旧成员（替换验证）。
	old := legacydb.CollectionDB{
		Version:     20260924,
		Collections: []legacydb.Collection{
			{Name: "keep-me", BeatmapMD5s: []string{"11"}},
			{Name: "favourites", BeatmapMD5s: []string{"old1", "old2"}},
		},
	}
	if err := os.WriteFile(filepath.Join(stableDir, "collection.db"),
		legacydb.WriteCollectionDB(old), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := Plan{TargetFork: "stable",
		Target: forks.LibraryRef{ForkID: "stable", DataDir: stableDir},
		Items: []PlanItem{{Kind: hub.KindCollection, ContentKey: "favourites", Action: ActionUpdate,
			PayloadJSON: `{"Name":"favourites","BeatmapMD5Hashes":["new1"]}`}}}
	if _, err := d.runner.Deploy(context.Background(), plan, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	cdata, err := os.ReadFile(filepath.Join(stableDir, "collection.db"))
	if err != nil {
		t.Fatal(err)
	}
	cdb, err := legacydb.ParseCollectionDB(cdata)
	if err != nil {
		t.Fatal(err)
	}
	if len(cdb.Collections) != 2 {
		t.Fatalf("收藏数 = %d（应保留既有 2 条）", len(cdb.Collections))
	}
	byName := map[string][]string{}
	for _, c := range cdb.Collections {
		byName[c.Name] = c.BeatmapMD5s
	}
	if len(byName["keep-me"]) != 1 || byName["keep-me"][0] != "11" {
		t.Errorf("无关收藏被改动: %v", byName["keep-me"])
	}
	if len(byName["favourites"]) != 1 || byName["favourites"][0] != "new1" {
		t.Errorf("目标收藏未替换为决定值: %v", byName["favourites"])
	}
	// 备份生成（S2）。
	matches, _ := filepath.Glob(filepath.Join(stableDir, "collection.db.*"))
	if len(matches) == 0 {
		t.Error("写前备份未生成（S2）")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
