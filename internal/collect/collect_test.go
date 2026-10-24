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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// guidNorm 归一化 Guid 比较（播种夹具带连字符、投影读回 32 位 hex）。
func guidNorm(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", ""))
}

// skipCount 从报告取某原因的跳过计数（不存在返回 -1，便于区分「零次」与
// 「未出现」）。
func skipCount(report Report, reason string) int {
	for _, s := range report.Skips {
		if s.Reason == reason {
			return s.Count
		}
	}
	return -1
}

// TestSkinIsOwnFingerprint fork 专有皮肤指纹规则单测（Name+Creator 双等值；
// 注册表数据属 forks 包，越界不补——规则语义在此独立验证）。
func TestSkinIsOwnFingerprint(t *testing.T) {
	own := []forks.SkinFingerprint{{Name: "Ez Default", Creator: "Ez2Lazer"}}
	for _, tc := range []struct {
		name, creator string
		want          bool
	}{
		{"Ez Default", "Ez2Lazer", true},
		{"Ez Default", "someone", false},  // 仅 Name 命中不算
		{"other", "Ez2Lazer", false},      // 仅 Creator 命中不算
		{"My Skin", "maker", false},
	} {
		if got := skinIsOwn(own, tc.name, tc.creator); got != tc.want {
			t.Errorf("skinIsOwn(%q,%q) = %v，期望 %v", tc.name, tc.creator, got, tc.want)
		}
	}
	if skinIsOwn(nil, "Ez Default", "Ez2Lazer") {
		t.Error("空指纹清单不应命中")
	}
}

// TestCollectM2EntitiesFullColumns M2 收集验收（design §7-M2 / §8.2-5）：
// 全列谱面/皮肤/成绩入 hub 逐字段断言；跳过规则逐条断言计数与原因；
// collect_log 计数含新实体；幂等重跑一致。
func TestCollectM2EntitiesFullColumns(t *testing.T) {
	files := map[string][]byte{}
	fullSet, err := fixture.MaterializeSet(7001, "M2 Artist", "M2 Title", "m2mapper",
		[]fixture.DiffMaterial{
			{Name: "Easy", OsuBytes: []byte("m2 easy osu bytes")},
			{Name: "Tough", OsuBytes: []byte("m2 tough osu bytes")},
		}, []byte("m2-audio-bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	fullSet.ID = "77777777-7777-7777-7777-777777777777"
	fullSet.Beatmaps[0].ID = "88888888-8888-8888-8888-888888888888"
	fullSet.Beatmaps[1].ID = "99999999-9999-9999-9999-999999999999"
	fullSet.Beatmaps[1].Hidden = true // Hidden 难度跳过夹具

	// 谱面集跳过夹具：软删集（难度随集消失不重复计数）与 Protected 系统集。
	softDelSet := hub.BeatmapSet{
		ID: "aaaaaaa1-0000-0000-0000-000000000001", Hash: sha256Fixture(t, "m2-softdel-set-concat"),
		OnlineID: 7002, DeletePending: true,
	}
	protectedSet := hub.BeatmapSet{
		ID: "aaaaaaa2-0000-0000-0000-000000000002", Hash: sha256Fixture(t, "m2-protected-set-concat"),
		OnlineID: 7003, Protected: true,
	}

	// 成绩：全列正常行 + 软删行 + 空 BeatmapHash 行。
	score, err := fixture.MaterializeScore(fullSet.Beatmaps[0], "lazer 2026.1002.0", []byte("m2 replay bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeScore: %v", err)
	}
	score.Guid = "bbbbbbbb-0000-0000-0000-000000000001"
	score.TotalScore = 1234567
	score.TotalScoreWithoutMods = 1200000
	score.MaxCombo = 999
	score.RankInt = 1 // SS
	score.Combo = 42
	score.ModsJson = `[{"acronym":"HD","settings":{}}]`
	score.StatisticsJson = `{"great":1000,"miss":0}`
	score.MaximumStatisticsJson = `{"great":1000}`
	softDelScore := fixture.SeedScore{
		Guid: "bbbbbbbb-0000-0000-0000-000000000002", BeatmapHash: fullSet.Beatmaps[0].Hash,
		Hash: sha256Fixture(t, "m2-softdel-score-replay"), DeletePending: true,
	}
	noAnchorScore := fixture.SeedScore{
		Guid: "bbbbbbbb-0000-0000-0000-000000000003",
		Hash: sha256Fixture(t, "m2-noanchor-score-replay"),
	}

	// 皮肤：全列用户行 + 软删行 + 空 Hash 行（内置六皮肤由 fixture 恒播）。
	skin, err := fixture.MaterializeSkin("M2 Skin", "skinner", []fixture.SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:M2 Skin\n")},
		{Filename: "layout.json", Bytes: []byte(`{"version":2}`)},
		{Filename: "hit.png", Bytes: []byte("m2-hit-png-bytes")},
	}, files)
	if err != nil {
		t.Fatalf("MaterializeSkin: %v", err)
	}
	skin.Guid = "cccccccc-0000-0000-0000-000000000001"
	softDelSkin := fixture.SeedSkin{
		Name: "Dead Skin", Creator: "dead", Hash: sha256Fixture(t, "m2-dead-skin-concat"),
		DeletePending: true,
	}
	noHashSkin := fixture.SeedSkin{Name: "NoHash Skin", Creator: "nohash"}

	seed := fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{fullSet, softDelSet, protectedSet},
		Scores:      []fixture.SeedScore{score, softDelScore, noAnchorScore},
		Skins:       []fixture.SeedSkin{skin, softDelSkin, noHashSkin},
		Files:       files,
	}
	dataDir := t.TempDir()
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: dataDir, Seed: seed,
	})
	if err != nil {
		t.Fatalf("fixture.Build: %v", err)
	}

	h := newHarness(t, nil)
	report, err := h.runner.Collect(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// 实体入仓计数（跳过行不入仓）。
	for kind, want := range map[hub.EntityKind]int{
		hub.KindCollection: 0, hub.KindBeatmapSet: 1, hub.KindBeatmap: 1,
		hub.KindSkin: 1, hub.KindScore: 1,
	} {
		if got := report.Entities[kind]; got != want {
			t.Errorf("Entities[%s] = %d，期望 %d", kind, got, want)
		}
	}

	// 跳过规则逐条：计数与原因。
	for reason, want := range map[string]int{
		skipSoftDeleted:     1, // 软删集（难度随集消失）
		skipProtectedSet:    1,
		skipHiddenDiff:      1,
		skipSkinSoftDel:     1,
		skipProtectedSkin:   6, // 内置六皮肤（fixture 恒播）
		skipSkinNoHash:      1,
		skipScoreSoftDel:    1,
		skipScoreNoBeatmap:  1,
	} {
		if got := skipCount(report, reason); got != want {
			t.Errorf("跳过[%s] = %d，期望 %d（Skips=%+v）", reason, got, want, report.Skips)
		}
	}
	if skipCount(report, skipOwnSkin) != -1 {
		t.Errorf("lazer 注册表未登记 OwnSkins，不应出现 fork 专有皮肤跳过（Skips=%+v）", report.Skips)
	}

	// collect_log：entity_counts 含五类实体、skip_reasons 可解析。
	var countsJSON, skipsJSON string
	if err := h.hub.DB().QueryRow(
		`SELECT entity_counts, skip_reasons FROM collect_log ORDER BY finished_at DESC LIMIT 1`,
	).Scan(&countsJSON, &skipsJSON); err != nil {
		t.Fatalf("读 collect_log: %v", err)
	}
	var counts map[hub.EntityKind]int
	if err := json.Unmarshal([]byte(countsJSON), &counts); err != nil {
		t.Fatalf("解码 entity_counts: %v", err)
	}
	if counts[hub.KindSkin] != 1 || counts[hub.KindScore] != 1 || counts[hub.KindBeatmap] != 1 {
		t.Errorf("collect_log entity_counts = %v，期望含 skin=1/score=1/beatmap=1", counts)
	}
	var logSkips []hub.SkipStat
	if err := json.Unmarshal([]byte(skipsJSON), &logSkips); err != nil {
		t.Fatalf("解码 skip_reasons: %v", err)
	}
	if len(logSkips) != len(report.Skips) {
		t.Errorf("collect_log skip_reasons %d 条，报告 %d 条，应一致", len(logSkips), len(report.Skips))
	}

	// 皮肤逐字段断言（hub canonical ← 投影）。
	wantSkin := hub.Skin{
		ID: guidNorm(skin.Guid), Hash: skin.Hash, Name: skin.Name, Creator: skin.Creator,
		InstantiationInfo: skin.InstantiationInfo, Files: skin.Files,
	}
	gotSkin, originSkin, ok, err := h.hub.LoadSkin(context.Background(), skin.Hash)
	if err != nil || !ok {
		t.Fatalf("LoadSkin(%s) = ok=%v err=%v", skin.Hash, ok, err)
	}
	if !reflect.DeepEqual(gotSkin, wantSkin) {
		t.Errorf("hub 皮肤 payload = %+v，期望 %+v", gotSkin, wantSkin)
	}
	if originSkin.Fork != "lazer" || originSkin.GUID != guidNorm(skin.Guid) {
		t.Errorf("皮肤 origin = %+v，期望 {lazer, 源 Guid}（HB3）", originSkin)
	}

	// 成绩逐字段断言。
	wantScore := hub.Score{
		ID:                   guidNorm(score.Guid),
		BeatmapHash:          score.BeatmapHash,
		Hash:                 score.Hash,
		RulesetShortName:     score.RulesetShortName,
		ClientVersion:        score.ClientVersion,
		TotalScore:           score.TotalScore,
		TotalScoreWithoutMods: score.TotalScoreWithoutMods,
		TotalScoreVersion:    int(score.TotalScoreVersion),
		Accuracy:             score.Accuracy,
		Date:                 score.Date,
		PP:                   score.PP,
		OnlineID:             score.OnlineID,
		LegacyOnlineID:       score.LegacyOnlineID,
		User:                 score.User,
		ModsJson:             score.ModsJson,
		StatisticsJson:       score.StatisticsJson,
		MaximumStatisticsJson: score.MaximumStatisticsJson,
		Pauses:               score.Pauses,
		RankInt:              int(score.RankInt),
		Combo:                int(score.Combo),
		MaxCombo:             int(score.MaxCombo),
		Files:                []hub.NamedFile{{Hash: score.Hash, Filename: "replay.osr"}},
	}
	gotScore, originScore, ok, err := h.hub.LoadScore(context.Background(), score.BeatmapHash, score.Hash)
	if err != nil || !ok {
		t.Fatalf("LoadScore = ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(gotScore, wantScore) {
		t.Errorf("hub 成绩 payload = %+v，期望 %+v", gotScore, wantScore)
	}
	if originScore.Fork != "lazer" || originScore.GUID != guidNorm(score.Guid) {
		t.Errorf("成绩 origin = %+v，期望 {lazer, 源 Guid}（HB3）", originScore)
	}

	// 谱面集逐字段断言（Hidden 难度被剔出 Beatmaps；Files 保留全部集内文件）。
	wantSet := fullSet
	wantSet.ID = guidNorm(fullSet.ID)
	wantSet.Beatmaps = []hub.Beatmap{fullSet.Beatmaps[0]}
	wantSet.Beatmaps[0].ID = guidNorm(fullSet.Beatmaps[0].ID)
	sets, err := h.hub.CurrentBeatmapSets(context.Background())
	if err != nil {
		t.Fatalf("CurrentBeatmapSets: %v", err)
	}
	if len(sets) != 1 {
		t.Fatalf("当前版本谱面集 = %d，期望 1", len(sets))
	}
	if !reflect.DeepEqual(sets[0].Set, wantSet) {
		t.Errorf("hub 谱面集 payload = %+v，期望 %+v", sets[0].Set, wantSet)
	}
	if sets[0].Set.Artist != "M2 Artist" || sets[0].Set.Title != "M2 Title" || sets[0].Set.AuthorUsername != "m2mapper" {
		t.Errorf("谱面身份键字段 = %q/%q/%q，期望首难度 Metadata 承载（§4.2）",
			sets[0].Set.Artist, sets[0].Set.Title, sets[0].Set.AuthorUsername)
	}

	// 难度行逐字段断言（beatmap 表 canonical，非集内嵌视图）。
	var bmPayload string
	if err := h.hub.DB().QueryRow(
		`SELECT payload FROM beatmap WHERE content_hash=?`, fullSet.Beatmaps[0].Hash,
	).Scan(&bmPayload); err != nil {
		t.Fatalf("读 beatmap payload: %v", err)
	}
	var gotBM hub.Beatmap
	if err := json.Unmarshal([]byte(bmPayload), &gotBM); err != nil {
		t.Fatalf("解码 beatmap payload: %v", err)
	}
	wantBM := fullSet.Beatmaps[0]
	wantBM.ID = guidNorm(wantBM.ID)
	if !reflect.DeepEqual(gotBM, wantBM) {
		t.Errorf("hub 难度 payload = %+v，期望 %+v", gotBM, wantBM)
	}
	// Hidden 难度与跳过集的难度不得入仓。
	for _, bad := range []string{fullSet.Beatmaps[1].Hash} {
		var n int
		if err := h.hub.DB().QueryRow(
			`SELECT COUNT(*) FROM beatmap WHERE content_hash=?`, bad).Scan(&n); err != nil {
			t.Fatalf("查隐藏难度行: %v", err)
		}
		if n != 0 {
			t.Errorf("Hidden 难度 %s 入仓 %d 行，期望 0", bad, n)
		}
	}

	// presence：五类实体快照完整。
	for kind, want := range map[hub.EntityKind]int{
		hub.KindCollection: 0, hub.KindBeatmapSet: 1, hub.KindSkin: 1, hub.KindScore: 1,
	} {
		if got := h.presenceCount("lazer", kind); got != want {
			t.Errorf("presence(%s) = %d，期望 %d", kind, got, want)
		}
	}

	// 幂等重跑：实体/presence/payload 一致，无 tombstone（HB2 前置）。
	report2, err := h.runner.Collect(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("二次 Collect: %v", err)
	}
	if got := report2.Entities[hub.KindSkin]; got != 1 {
		t.Errorf("二次收集 skin = %d，期望 1", got)
	}
	if got := report2.Entities[hub.KindScore]; got != 1 {
		t.Errorf("二次收集 score = %d，期望 1", got)
	}
	skin2, _, ok, err := h.hub.LoadSkin(context.Background(), skin.Hash)
	if err != nil || !ok || !reflect.DeepEqual(skin2, wantSkin) {
		t.Errorf("二次收集后皮肤 payload 不一致: ok=%v err=%v", ok, err)
	}
	var tombs int
	if err := h.hub.DB().QueryRow(`SELECT count(*) FROM tombstone`).Scan(&tombs); err != nil {
		t.Fatalf("tombstone 计数: %v", err)
	}
	if tombs != 0 {
		t.Errorf("无删除场景 tombstone = %d，期望 0", tombs)
	}
}
