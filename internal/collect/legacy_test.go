package collect

// stable 端（StorageLegacy）收集测试：全程不依赖 realm-core——临时
// stable 目录（Songs/ 集目录 + collection.db，无 osu!.db——覆盖「投放后
// 未开游戏」路径）→ CollectWith → 断言 hub 实体/presence、集哈希跨端
// 算法（SHA-256 拼接，stable-legacy §4）、收仓回链。真机样本用
// ModeSnapshotOnly（绝不碰用户真实文件）。

import (
	"context"
	"crypto/md5" // mimosa-ignore -- osu! 谱面格式规定 MD5 内容哈希，非安全用途，已裁定误报
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/legacydb"
)

// newLegacyHarness 是 stable 收集专用 harness（与 newHarness 的差异：
// 无 realmc 依赖、无结构档案仓）。
func newLegacyHarness(t *testing.T) (*Runner, *hub.Store, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := hub.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatalf("hub.Open: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	store, err := filestore.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	return NewRunner(Deps{
		Hub:   h,
		Files: filestore.NewEngine(store),
		Guard: guard.NewNoScanGuard(), // hermetic：不吃宿主机进程现场
	}), h, dir
}

// buildStableDir 构造临时 stable 目录：一个集目录（两个难度 + 音频）、
// 一条收藏；无 osu!.db（目录扫描自足路径）。
func buildStableDir(t *testing.T) (root, setDir string, osu1, osu2 []byte) {
	t.Helper()
	root = t.TempDir()
	setDir = filepath.Join(root, "Songs", "1002716 Digital Sexy - Til Death (Nightcore Mix)")
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatal(err)
	}
	osu1 = []byte("osu file format v14\r\n\r\n[Metadata]\r\nTitle:Til Death (Nightcore Mix)\r\n" +
		"Artist:Digital Sexy\r\nCreator:Sotarks\r\nVersion:Expert\r\n" +
		"BeatmapID:2608394\r\nBeatmapSetID:1002716\r\n")
	osu2 = []byte("osu file format v14\r\n\r\n[Metadata]\r\nTitle:Til Death (Nightcore Mix)\r\n" +
		"Artist:Digital Sexy\r\nCreator:Sotarks\r\nVersion:Normal\r\n" +
		"BeatmapID:2608395\r\nBeatmapSetID:1002716\r\n")
	if err := os.WriteFile(filepath.Join(setDir, "a (Sotarks) [Expert].osu"), osu1, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "a (Sotarks) [Normal].osu"), osu2, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "audio.mp3"), []byte("mp3-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(osu1) // mimosa-ignore -- osu! 谱面格式规定 MD5，非安全用途，已裁定误报
	col := legacydb.CollectionDB{
		Version:     20260924,
		Collections: []legacydb.Collection{{Name: "favourites", BeatmapMD5s: []string{hex.EncodeToString(sum[:])}}},
	}
	if err := os.WriteFile(filepath.Join(root, "collection.db"), legacydb.WriteCollectionDB(col), 0o644); err != nil {
		t.Fatal(err)
	}
	// 皮肤目录（skin.ini 目录特征）。
	skinDir := filepath.Join(root, "Skins", "test-skin")
	if err := os.MkdirAll(skinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skinDir, "skin.ini"),
		[]byte("[General]\r\nName:test-skin\r\nCreator:tester\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skinDir, "cursor.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, setDir, osu1, osu2
}

func TestLegacyCollectFullChain(t *testing.T) {
	root, setDir, osu1, osu2 := buildStableDir(t)
	runner, h, _ := newLegacyHarness(t)

	report, err := runner.CollectWith(context.Background(),
		forks.LibraryRef{ForkID: "stable", DataDir: root}, Options{GameIni: true}, nil)
	if err != nil {
		t.Fatalf("CollectWith: %v", err)
	}
	if report.Entities[hub.KindBeatmapSet] != 1 || report.Entities[hub.KindCollection] != 1 || report.Entities[hub.KindSkin] != 1 {
		t.Fatalf("实体计数 = %+v", report.Entities)
	}
	if report.FilesAdopted != 5 {
		t.Fatalf("收仓文件数 = %d，期望 5（集 3 + 皮肤 2）", report.FilesAdopted)
	}

	// 集哈希 = .osu 内容按文件名序拼接的 SHA-256（跨端同键，§4）。
	hasher := sha256.New()
	hasher.Write(osu1) // "[Expert].osu" < "[Normal].osu"
	hasher.Write(osu2)
	wantSet := hex.EncodeToString(hasher.Sum(nil))
	var payload string
	if err := h.DB().QueryRow(
		`SELECT payload FROM beatmap_set WHERE content_hash=?`, wantSet).Scan(&payload); err != nil {
		t.Fatalf("集哈希跨端算法未命中（beatmap_set 行缺失）: %v", err)
	}
	if got := extractJSONRaw(payload, "OnlineID"); got != "1002716" {
		t.Errorf("集 OnlineID = %q，期望 1002716（目录名前缀）", got)
	}
	if got := extractJSONString(payload, "AuthorUsername"); got != "Sotarks" {
		t.Errorf("AuthorUsername = %q，期望 Sotarks（.osu 元数据）", got)
	}

	// presence：集 + 收藏各一行；难度挂父集无独立 presence。
	for _, tc := range []struct {
		kind string
		want int
	}{
		{"beatmap_set", 1}, {"collection", 1},
	} {
		var n int
		if err := h.DB().QueryRow(
			`SELECT count(*) FROM presence WHERE fork_id='stable' AND kind=?`, tc.kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != tc.want {
			t.Errorf("presence %s = %d，期望 %d", tc.kind, n, tc.want)
		}
	}

	// 难度元数据来自 .osu 解析（db 缺席路径）：MD5、难度名、OnlineID。
	rows, err := h.DB().Query(`SELECT payload FROM beatmap WHERE set_hash=?`, wantSet)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotVersions []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		if b, derr := hex.DecodeString(extractJSONString(p, "MD5Hash")); derr != nil || len(b) != 16 {
			t.Errorf("难度 MD5Hash 形态异常: %s", p)
		}
		gotVersions = append(gotVersions, extractJSONString(p, "DifficultyName"))
	}
	if len(gotVersions) != 2 {
		t.Fatalf("难度行 = %d，期望 2", len(gotVersions))
	}
	if gotVersions[0] != "Expert" || gotVersions[1] != "Normal" {
		t.Errorf("难度名 = %v", gotVersions)
	}

	// 收仓回链：Songs/ 与 Skins/ 下文件全部变链接且指向仓内源。
	for _, name := range []string{"a (Sotarks) [Expert].osu", "a (Sotarks) [Normal].osu", "audio.mp3"} {
		path := filepath.Join(setDir, name)
		info, lerr := os.Lstat(path)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s 未回链（仍是普通文件）", name)
		}
	}
	for _, name := range []string{"skin.ini", "cursor.png"} {
		path := filepath.Join(root, "Skins", "test-skin", name)
		info, lerr := os.Lstat(path)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("皮肤文件 %s 未回链（仍是普通文件）", name)
		}
	}
	// 皮肤投影：Name=目录名、Creator 取 skin.ini、Files 含两文件。
	var skinPayload string
	if err := h.DB().QueryRow(
		`SELECT payload FROM skin LIMIT 1`).Scan(&skinPayload); err != nil {
		t.Fatal(err)
	}
	if got := extractJSONString(skinPayload, "Name"); got != "test-skin" {
		t.Errorf("皮肤 Name = %q", got)
	}
	if got := extractJSONString(skinPayload, "Creator"); got != "tester" {
		t.Errorf("皮肤 Creator = %q", got)
	}

	// 幂等：重跑不报错、不重复。
	if _, err := runner.CollectWith(context.Background(),
		forks.LibraryRef{ForkID: "stable", DataDir: root}, Options{}, nil); err != nil {
		t.Fatalf("幂等重跑: %v", err)
	}
}

func TestLegacyCollectSnapshotOnlyKeepsFiles(t *testing.T) {
	root, setDir, _, _ := buildStableDir(t)
	runner, h, _ := newLegacyHarness(t)

	report, err := runner.CollectWith(context.Background(),
		forks.LibraryRef{ForkID: "stable", DataDir: root},
		Options{Mode: ModeSnapshotOnly}, nil)
	if err != nil {
		t.Fatalf("CollectWith: %v", err)
	}
	if report.FilesAdopted != 0 {
		t.Fatalf("SnapshotOnly 收仓文件数 = %d，期望 0", report.FilesAdopted)
	}
	// 仅目标端语义：快照进 presence、文件原样（普通文件）。
	var n int
	if err := h.DB().QueryRow(
		`SELECT count(*) FROM presence WHERE fork_id='stable' AND kind='beatmap_set'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("presence beatmap_set = %d，期望 1", n)
	}
	entries, rerr := os.ReadDir(setDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			t.Errorf("SnapshotOnly 不应回链: %s", e.Name())
		}
	}
}

// TestLegacyCollectRealSample 对真机 stable 目录全量投影（SnapshotOnly——
// 绝不触碰用户真实文件；样本缺失时跳过）。
func TestLegacyCollectRealSample(t *testing.T) {
	root := os.Getenv("LZSYNC_STABLE_ROOT")
	if root == "" {
		root = "/home/brokenshine/Games/osu!"
	}
	if _, err := os.Stat(filepath.Join(root, "osu!.db")); err != nil {
		t.Skipf("真机样本不可用: %v", err)
	}
	runner, h, _ := newLegacyHarness(t)
	if _, err := runner.CollectWith(context.Background(),
		forks.LibraryRef{ForkID: "stable", DataDir: root},
		Options{Mode: ModeSnapshotOnly}, nil); err != nil {
		t.Fatalf("CollectWith: %v", err)
	}
	// SnapshotOnly 不写实体（EntitiesWritten 恒 0），以 presence 计数断言。
	var n int
	if err := h.DB().QueryRow(
		`SELECT count(*) FROM presence WHERE fork_id='stable' AND kind='beatmap_set'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 60 {
		t.Fatalf("真机集数 = %d（本机样本 66，容错下界 60）", n)
	}
	t.Logf("sets=%d", n)
}

// extractJSONString 是测试用最小 JSON 字符串字段抽取（payload 断言辅助；
// 字段值均为无转义 ASCII hex/词，简单扫描足够）。数字字段（OnlineID 等）
// 用 extractJSONRaw 取到值边界（, 或 }）。
func extractJSONString(payload, key string) string {
	needle := `"` + key + `":"`
	for i := 0; i+len(needle) <= len(payload); i++ {
		if payload[i:i+len(needle)] == needle {
			rest := payload[i+len(needle):]
			for j := 0; j < len(rest); j++ {
				if rest[j] == '"' {
					return rest[:j]
				}
			}
		}
	}
	return ""
}

func extractJSONRaw(payload, key string) string {
	needle := `"` + key + `":`
	for i := 0; i+len(needle) <= len(payload); i++ {
		if payload[i:i+len(needle)] == needle {
			rest := payload[i+len(needle):]
			for j := 0; j < len(rest); j++ {
				if rest[j] == ',' || rest[j] == '}' {
					return rest[:j]
				}
			}
		}
	}
	return ""
}
