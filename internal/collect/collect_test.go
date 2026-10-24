package collect

// 集成测试（design §8.2-5 全链往返的收集半边 + M1 验收 5）：internal/fixture
// 造官方 lazer 库 → Runner.Collect → 临时 hub.db 断言 presence / 版本链
//（HB1）/ 收仓产物（HB4）与幂等；守卫拦截（S5）；Ez 外部托管跳过。
// librealm 缺失时显式 SKIP（§8.1「不得静默绿」）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// harness 聚合一次收集集成测试的全部依赖（临时 hub.db + 仓 + 守卫）。
type harness struct {
	t      *testing.T
	hubDir string
	hub    *hub.Store
	engine *filestore.Engine
	runner *Runner
}

func newHarness(t *testing.T, processNames []string) *harness {
	t.Helper()
	if _, err := realmc.Load(); err != nil {
		t.Skipf("realm-core 库不可用，跳过收集集成测试（§8.1 显式 SKIP）: %v", err)
	}
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
	snaps, err := forks.OpenSnapshotStore(filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatalf("forks.OpenSnapshotStore: %v", err)
	}
	engine := filestore.NewEngine(store)
	return &harness{
		t:      t,
		hubDir: dir,
		hub:    h,
		engine: engine,
		runner: NewRunner(Deps{Hub: h, Files: engine, Snapshots: snaps, Guard: guard.NewGuard(processNames)}),
	}
}

// presenceCount 直查 hub presence 行数（快照断言的最小口径）。
func (h *harness) presenceCount(forkID string, kind hub.EntityKind) int {
	h.t.Helper()
	var n int
	if err := h.hub.DB().QueryRow(
		`SELECT count(*) FROM presence WHERE fork_id=? AND kind=?`, forkID, kind).Scan(&n); err != nil {
		h.t.Fatalf("presence 计数: %v", err)
	}
	return n
}

// supersededOf 读谱面集行的版本链指针（NULL 返回空串）。
func (h *harness) supersededOf(hash string) string {
	h.t.Helper()
	var next sql.NullString
	if err := h.hub.DB().QueryRow(
		`SELECT superseded_by FROM beatmap_set WHERE content_hash=?`, hash).Scan(&next); err != nil {
		h.t.Fatalf("读 %s 的 superseded_by: %v", hash, err)
	}
	return next.String
}

// officialSeed 构造官方 lazer 种子：2 收藏夹 + 2 个同 OnlineID 不同 Hash
// 的谱面集（版本链夹具，HB1）+ 3 个实体文件（含 1 个跨集共享哈希）。
func officialSeed(hashes map[string]string) fixture.Seed {
	return fixture.Seed{
		Collections: []hub.Collection{
			{Name: "favorites"},
			{Name: "tech maps"},
		},
		BeatmapSets: []hub.BeatmapSet{
			{
				Hash:     hashes["set1"],
				OnlineID: 5,
				StatusInt: 4,
				Files:    []hub.NamedFile{{Hash: hashes["audio1"], Filename: "audio.mp3"}},
			},
			{
				Hash:     hashes["set2"],
				OnlineID: 5, // 同谱面新版本（§4.2 强键配对夹具）
				StatusInt: 4,
				Files:    []hub.NamedFile{
					{Hash: hashes["audio1"], Filename: "audio.mp3"}, // 跨集共享 blob
					{Hash: hashes["audio2"], Filename: "audio2.mp3"},
				},
			},
		},
		Files: map[string][]byte{
			hashes["audio1"]: []byte("shared-audio-bytes"),
			hashes["audio2"]: []byte("second-audio-bytes"),
		},
	}
}

func sha256Fixture(t *testing.T, content string) string {
	t.Helper()
	// fixture 要求 Files 键 = SHA-256(值)（§8.2-3 内容寻址自洽）；同款算法现场算。
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestCollectOfficialLibraryIntoHub(t *testing.T) {
	hashes := map[string]string{
		"set1":   sha256Fixture(t, "beatmaps-set-v1-concat"),
		"set2":   sha256Fixture(t, "beatmaps-set-v2-concat"),
		"audio1": sha256Fixture(t, "shared-audio-bytes"),
		"audio2": sha256Fixture(t, "second-audio-bytes"),
	}
	dataDir := t.TempDir() // fixture 要求 DataDir 已存在（realm 建伴生 .lock 不建父目录）
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID:        fixture.ForkLazer,
		SchemaVersion: fixture.VersionLazer,
		DataDir:       dataDir,
		Seed:          officialSeed(hashes),
	})
	if err != nil {
		t.Fatalf("fixture.Build: %v", err)
	}

	h := newHarness(t, nil) // 空名单守卫恒放行
	report, err := h.runner.Collect(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// 实体入仓计数。
	if got := report.Entities[hub.KindCollection]; got != 2 {
		t.Errorf("收藏入仓 = %d，期望 2", got)
	}
	if got := report.Entities[hub.KindBeatmapSet]; got != 2 {
		t.Errorf("谱面集入仓 = %d，期望 2", got)
	}

	// presence：fork × 内容键快照完整落库。
	if got := h.presenceCount("lazer", hub.KindCollection); got != 2 {
		t.Errorf("presence(collection) = %d，期望 2", got)
	}
	if got := h.presenceCount("lazer", hub.KindBeatmapSet); got != 2 {
		t.Errorf("presence(beatmap_set) = %d，期望 2", got)
	}

	// canonical：收藏按内容键可读回。
	if _, origin, ok, err := h.hub.LoadCollection(context.Background(), "favorites"); err != nil || !ok {
		t.Errorf("LoadCollection(favorites) = ok=%v err=%v，期望命中", ok, err)
	} else if origin.Fork != "lazer" {
		t.Errorf("收藏 origin_fork = %q，期望 lazer（HB3）", origin.Fork)
	}

	// 版本链（HB1）：同 OnlineID 两版本 → 恰一向另一的链指针，链尾唯一。
	l1, l2 := h.supersededOf(hashes["set1"]), h.supersededOf(hashes["set2"])
	switch {
	case l1 == hashes["set2"] && l2 == "":
	case l2 == hashes["set1"] && l1 == "":
	default:
		t.Fatalf("版本链失配：set1→%q set2→%q，期望恰一向另一", l1, l2)
	}
	current, err := h.hub.CurrentBeatmapSets(context.Background())
	if err != nil {
		t.Fatalf("CurrentBeatmapSets: %v", err)
	}
	if len(current) != 1 {
		t.Errorf("当前版本集合 = %d 个，期望 1（HB1：只认链尾）", len(current))
	}

	// 收仓产物（HB4）：files/ 下全部实体 → 仓内副本 + 原位链接。
	if got := report.FilesAdopted; got != 2 {
		t.Errorf("收仓成功数 = %d，期望 2", got)
	}
	for _, hash := range []string{hashes["audio1"], hashes["audio2"]} {
		gameFile := filepath.Join(dataDir, "files", hash[:1], hash[:2], hash)
		info, err := os.Lstat(gameFile)
		if err != nil {
			t.Fatalf("Lstat 游戏侧文件 %s: %v", gameFile, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("游戏侧 %s 收仓后应呈符号链接形态，实际 mode=%v", hash, info.Mode())
			continue
		}
		target, err := os.Readlink(gameFile)
		if err != nil {
			t.Fatalf("Readlink %s: %v", gameFile, err)
		}
		if want := h.engine.Store.Path(hash); target != want {
			t.Errorf("链接目标 = %q，期望仓内路径 %q", target, want)
		}
		if err := h.engine.Store.Verify(hash); err != nil {
			t.Errorf("仓副本校验 %s: %v", hash, err)
		}
	}
	if got := report.AdoptModeCounts[filestore.LinkSymlink]; got != 2 {
		t.Errorf("符号链接形态计数 = %d，期望 2（Linux 无特权）", got)
	}

	// 结构档案顺手采集（同版本只留首份，§4.3）。
	if _, err := os.Stat(filepath.Join(h.hubDir, "snapshots", "lazer-52.json")); err != nil {
		t.Errorf("结构档案未落盘: %v", err)
	}

	// 幂等重跑：快照重写一致、链接文件不再被收仓（§3.3）。
	report2, err := h.runner.Collect(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("二次 Collect: %v", err)
	}
	if got := h.presenceCount("lazer", hub.KindBeatmapSet); got != 2 {
		t.Errorf("二次收集后 presence(beatmap_set) = %d，期望仍 2", got)
	}
	if got := report2.FilesAdopted; got != 0 {
		t.Errorf("二次收仓成功数 = %d，期望 0（链接跳过）", got)
	}
	if got := report2.FilesSkipped; got != 2 {
		t.Errorf("二次收仓跳过数 = %d，期望 2（既有链接）", got)
	}
	// 无消失键 → 无 tombstone（HB2 前置）。
	var tombs int
	if err := h.hub.DB().QueryRow(`SELECT count(*) FROM tombstone`).Scan(&tombs); err != nil {
		t.Fatalf("tombstone 计数: %v", err)
	}
	if tombs != 0 {
		t.Errorf("无删除场景 tombstone = %d，期望 0", tombs)
	}
}

func TestCollectBlockedByGuard(t *testing.T) {
	// 名单内进程 = 测试进程自身（S5 最小可复现占用夹具）。
	h := newHarness(t, []string{filepath.Base(os.Args[0])})
	ref := forks.LibraryRef{ForkID: "lazer", RealmPath: "whatever.realm", DataDir: t.TempDir()}
	_, err := h.runner.Collect(context.Background(), ref, nil)
	if !errors.Is(err, guard.ErrGameRunning) {
		t.Fatalf("Collect = %v，期望 guard.ErrGameRunning", err)
	}
	// 守卫在投影前拦截：hub 无任何该 fork 快照（HB2：部分收集不落库）。
	if got := h.presenceCount("lazer", hub.KindCollection); got != 0 {
		t.Errorf("拦截后 presence = %d，期望 0", got)
	}
}

func TestCollectSkipsEzExternalSets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "extroot")
	hashes := map[string]string{
		"set1":  sha256Fixture(t, "ez-local-set-concat"),
		"file1": sha256Fixture(t, "ez-local-bytes"),
	}
	seed := fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{
			{Hash: hashes["set1"], OnlineID: 7, StatusInt: 1,
				Files: []hub.NamedFile{{Hash: hashes["file1"], Filename: "a.mp3"}}},
		},
		Files: map[string][]byte{hashes["file1"]: []byte("ez-local-bytes")},
		// 外部托管集（fork-ez2lazer §5.1：ext:set: 编码 Hash，投放/收集双向排除）。
		ExternalSets: []fixture.ExternalSetSpec{{OnlineID: 9, ContentRoot: root}},
	}
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID:        fixture.ForkEz2Lazer,
		SchemaVersion: fixture.VersionEzDisk,
		DataDir:       t.TempDir(),
		Seed:          seed,
	})
	if err != nil {
		t.Fatalf("fixture.Build: %v", err)
	}

	h := newHarness(t, nil)
	report, err := h.runner.Collect(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	found := false
	for _, s := range report.Skips {
		if s.Reason == skipExternalSet {
			if s.Count != 1 {
				t.Errorf("外部托管跳过计数 = %d，期望 1", s.Count)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("Skips = %+v，期望含「Ez 外部托管谱面集」", report.Skips)
	}
	if got := report.Entities[hub.KindBeatmapSet]; got != 1 {
		t.Errorf("谱面集入仓 = %d，期望 1（外部集被跳过）", got)
	}
	if got := h.presenceCount("ez2lazer", hub.KindBeatmapSet); got != 1 {
		t.Errorf("presence(beatmap_set) = %d，期望 1", got)
	}
}
