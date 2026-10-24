package deploy

// 成绩投放验收（design.md §5.2 全部跳过分支 + §7-M2-4）：五分支逐条断言
// 计数与人话原因——① 目标无 Score 表 / ② BeatmapHash 为空 / ③ 目标缺
// 对应难度 Hash（惰性索引）/ ④ 规则集不可解析（fork 专有）/ ⑤ 挂接成功
//（BeatmapInfo 关系链到目标难度 + BeatmapHash 列 + Files=[.osr] +
// RealmFile 行）；同键 (BeatmapHash,Hash) 已存在 → 就地更新白名单列
//（OnlineID 列不触碰，S8）+ 重续幂等 + 隐难度不入索引。
// librealm 缺失时显式 SKIP（§8.1 补充门：不得静默绿）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// sha256Of 测试内哈希 helper（夹具字节 → hex）。
func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// makeScore 构造一条自洽 hub.Score（回放字节入池，Files=[.osr]）。
func makeScore(id, beatmapHash, ruleset string, replay []byte, files map[string][]byte) hub.Score {
	replayHash := sha256Of(replay)
	files[replayHash] = replay
	pp := 266.5
	legacy := int64(951753)
	return hub.Score{
		ID:                    id,
		BeatmapHash:           beatmapHash,
		RulesetShortName:      ruleset,
		ClientVersion:         "lazer 2026.1001.0",
		Hash:                  replayHash,
		TotalScore:            1234567,
		TotalScoreWithoutMods: 1180000,
		TotalScoreVersion:     30000019,
		LegacyTotalScore:      &legacy,
		MaxCombo:              728,
		Accuracy:              0.9876,
		Date:                  t0.Add(5 * time.Hour),
		PP:                    &pp,
		OnlineID:              55001,
		LegacyOnlineID:        -1,
		User:                  hub.RealmUser{OnlineID: 1, Username: "score-player", CountryString: "CN"},
		ModsJson:              `[{"acronym":"HD","settings":{}}]`,
		StatisticsJson:        `{"great":900,"ok":20,"miss":0}`,
		MaximumStatisticsJson: `{"great":1000}`,
		Pauses:                []int32{2500, 60000},
		RankInt:               1, // SS
		Combo:                 728,
		Files:                 []hub.NamedFile{{Hash: replayHash, Filename: "replay.osr"}},
	}
}

// scorePlan 手工构造成绩计划（BuildPlan 的 score 面由后续块放开；写入面
// 以手工计划直验）。
func scorePlan(t *testing.T, forkID string, refs forks.LibraryRef, scores []hub.Score) Plan {
	t.Helper()
	items := make([]PlanItem, 0, len(scores))
	for _, sc := range scores {
		items = append(items, PlanItem{
			Kind:       hub.KindScore,
			ContentKey: hub.ScoreContentKey(sc.BeatmapHash, sc.Hash),
			Action:     ActionCreate,
			Summary:    "成绩 " + hashPrefix(sc.Hash) + "…",
		})
	}
	plan, err := NewPlan(forkID, items)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	plan.Target = refs
	return plan
}

// readScoreRow 读回目标库成绩行（组合键定位 + 白名单全列 + 结构挂接）。
func readScoreRow(t *testing.T, sr *setReader, sc hub.Score) (hub.Score, bool) {
	t.Helper()
	ck := sr.class("Score")
	obj, err := sr.r.FindFirstByString(ck, sr.prop("Score", "Hash"), sc.Hash)
	if err != nil {
		t.Fatalf("查成绩 %s: %v", hashPrefix(sc.Hash), err)
	}
	if obj == nil {
		return hub.Score{}, false
	}
	defer sr.r.ReleaseObject(obj)

	got := hub.Score{
		ID:                           sr.guidOf(obj, "Score", "ID"),
		BeatmapHash:                  sr.str(obj, "Score", "BeatmapHash"),
		Hash:                         sc.Hash,
		ClientVersion:                sr.str(obj, "Score", "ClientVersion"),
		TotalScore:                   sr.int64Of(obj, "Score", "TotalScore"),
		TotalScoreWithoutMods:        sr.int64Of(obj, "Score", "TotalScoreWithoutMods"),
		TotalScoreVersion:            int(sr.int64Of(obj, "Score", "TotalScoreVersion")),
		MaxCombo:                     int(sr.int64Of(obj, "Score", "MaxCombo")),
		OnlineID:                     sr.int64Of(obj, "Score", "OnlineID"),
		LegacyOnlineID:               sr.int64Of(obj, "Score", "LegacyOnlineID"),
		ModsJson:                     sr.str(obj, "Score", "Mods"),
		StatisticsJson:               sr.str(obj, "Score", "Statistics"),
		MaximumStatisticsJson:        sr.str(obj, "Score", "MaximumStatistics"),
		RankInt:                      int(sr.int64Of(obj, "Score", "Rank")),
		Combo:                        int(sr.int64Of(obj, "Score", "Combo")),
		Accuracy:                     sr.float64Of(obj, "Score", "Accuracy"),
		Date:                         derefTime(sr.tsPtrOf(obj, "Score", "Date")),
		BackgroundReprocessingFailed: sr.boolOf(obj, "Score", "BackgroundReprocessingFailed"),
		IsLegacyScore:                sr.boolOf(obj, "Score", "IsLegacyScore"),
		DeletePending:                sr.boolOf(obj, "Score", "DeletePending"),
	}
	if key, err := sr.r.FindProperty(ck, "LegacyTotalScore"); err == nil {
		if null, err := sr.r.IsNull(obj, key); err == nil && !null {
			if v, err := sr.r.GetInt64(obj, key); err == nil {
				got.LegacyTotalScore = &v
			}
		}
	}
	if key, err := sr.r.FindProperty(ck, "PP"); err == nil {
		if null, err := sr.r.IsNull(obj, key); err == nil && !null {
			if v, err := sr.r.GetFloat64(obj, key); err == nil {
				got.PP = &v
			}
		}
	}
	// 结构挂接面：BeatmapInfo → 难度 Hash；Ruleset → ShortName；User embedded。
	bm := sr.linked(obj, "Score", "BeatmapInfo")
	got.BeatmapHash = sr.str(bm, "Beatmap", "Hash") // 挂接目标难度的现场 Hash
	sr.r.ReleaseObject(bm)
	rs := sr.linked(obj, "Score", "Ruleset")
	got.RulesetShortName = sr.str(rs, "Ruleset", "ShortName")
	sr.r.ReleaseObject(rs)
	user := sr.linked(obj, "Score", "User")
	got.User = hub.RealmUser{
		OnlineID:      int(sr.int64Of(user, "RealmUser", "OnlineID")),
		Username:      sr.str(user, "RealmUser", "Username"),
		CountryString: sr.str(user, "RealmUser", "CountryString"),
	}
	sr.r.ReleaseObject(user)
	// Pauses 列表。
	pausesLst, err := sr.r.GetList(obj, sr.prop("Score", "Pauses"))
	if err != nil {
		t.Fatalf("取 Pauses: %v", err)
	}
	n, _ := pausesLst.Size()
	for i := uint64(0); i < n; i++ {
		v, err := pausesLst.GetInt64(i)
		if err != nil {
			t.Fatalf("读 Pauses[%d]: %v", i, err)
		}
		got.Pauses = append(got.Pauses, int32(v))
	}
	pausesLst.Close()
	// Files usage（.osr）。
	filesLst, err := sr.r.GetList(obj, sr.prop("Score", "Files"))
	if err != nil {
		t.Fatalf("取 Files: %v", err)
	}
	n, _ = filesLst.Size()
	for i := uint64(0); i < n; i++ {
		usage, err := filesLst.GetLinkedObject(i)
		if err != nil || usage == nil {
			t.Fatalf("读 Files[%d]: %v", i, err)
		}
		nf := hub.NamedFile{Filename: sr.str(usage, "RealmNamedFileUsage", "Filename")}
		file := sr.linked(usage, "RealmNamedFileUsage", "File")
		nf.Hash = sr.str(file, "File", "Hash")
		sr.r.ReleaseObject(file)
		sr.r.ReleaseObject(usage)
		got.Files = append(got.Files, nf)
	}
	filesLst.Close()
	return got, true
}

// assertScoreDeepEqual 挂接成功分支的逐字段断言（wantOnlineID 独立传入：
// S8 异域新行读回值 ≠ canonical 值）。
func assertScoreDeepEqual(t *testing.T, got, want hub.Score, wantOnlineID int64) {
	t.Helper()
	if got.ID != want.ID && !guidEqual(got.ID, want.ID) {
		t.Errorf("成绩 Guid = %s，期望 %s", got.ID, want.ID)
	}
	if got.BeatmapHash != want.BeatmapHash {
		t.Errorf("成绩 BeatmapHash = %s，期望 %s（挂接目标难度的 Hash）", got.BeatmapHash, want.BeatmapHash)
	}
	if got.RulesetShortName != want.RulesetShortName {
		t.Errorf("成绩 Ruleset = %q，期望 %q", got.RulesetShortName, want.RulesetShortName)
	}
	for _, c := range []struct{ name, a, b string }{
		{"ClientVersion", got.ClientVersion, want.ClientVersion},
		{"Mods", got.ModsJson, want.ModsJson},
		{"Statistics", got.StatisticsJson, want.StatisticsJson},
		{"MaximumStatistics", got.MaximumStatisticsJson, want.MaximumStatisticsJson},
	} {
		if c.a != c.b {
			t.Errorf("成绩 %s = %q，期望 %q", c.name, c.a, c.b)
		}
	}
	for _, c := range []struct {
		name string
		a, b int64
	}{
		{"TotalScore", got.TotalScore, want.TotalScore},
		{"TotalScoreWithoutMods", got.TotalScoreWithoutMods, want.TotalScoreWithoutMods},
		{"TotalScoreVersion", int64(got.TotalScoreVersion), int64(want.TotalScoreVersion)},
		{"MaxCombo", int64(got.MaxCombo), int64(want.MaxCombo)},
		{"Rank", int64(got.RankInt), int64(want.RankInt)},
		{"Combo", int64(got.Combo), int64(want.Combo)},
		{"OnlineID", got.OnlineID, wantOnlineID},
		{"LegacyOnlineID", got.LegacyOnlineID, want.LegacyOnlineID},
	} {
		if c.a != c.b {
			t.Errorf("成绩 %s = %d，期望 %d", c.name, c.a, c.b)
		}
	}
	if got.Accuracy != want.Accuracy {
		t.Errorf("成绩 Accuracy = %v，期望 %v", got.Accuracy, want.Accuracy)
	}
	if !got.Date.Equal(want.Date) {
		t.Errorf("成绩 Date = %v，期望 %v", got.Date, want.Date)
	}
	if got.DeletePending || got.BackgroundReprocessingFailed || got.IsLegacyScore != want.IsLegacyScore {
		t.Errorf("成绩 bool 列 = %v/%v/%v，期望 false/false/%v",
			got.DeletePending, got.BackgroundReprocessingFailed, got.IsLegacyScore, want.IsLegacyScore)
	}
	if (got.LegacyTotalScore == nil) != (want.LegacyTotalScore == nil) ||
		(got.LegacyTotalScore != nil && *got.LegacyTotalScore != *want.LegacyTotalScore) {
		t.Errorf("成绩 LegacyTotalScore = %v，期望 %v", got.LegacyTotalScore, want.LegacyTotalScore)
	}
	if (got.PP == nil) != (want.PP == nil) || (got.PP != nil && *got.PP != *want.PP) {
		t.Errorf("成绩 PP = %v，期望 %v", got.PP, want.PP)
	}
	if len(got.Pauses) != len(want.Pauses) {
		t.Errorf("成绩 Pauses = %v，期望 %v", got.Pauses, want.Pauses)
	} else {
		for i := range got.Pauses {
			if got.Pauses[i] != want.Pauses[i] {
				t.Errorf("成绩 Pauses[%d] = %d，期望 %d", i, got.Pauses[i], want.Pauses[i])
			}
		}
	}
	if !namedFilesEqual(got.Files, want.Files) {
		t.Errorf("成绩 Files = %v，期望 %v", got.Files, want.Files)
	}
	if got.User.Username != want.User.Username || got.User.CountryString != want.User.CountryString {
		t.Errorf("成绩 User = %+v，期望 %+v", got.User, want.User)
	}
}

// skipCountOf 报告中含指定原因片段的计数总和。
func skipCountOf(report Report, substr string) int {
	n := 0
	for _, st := range report.Skipped {
		if strings.Contains(st.Reason, substr) {
			n += st.Count
		}
	}
	return n
}

// TestDeployScoreFiveBranches §5.2 五分支：① 无 Score 表（独立库）；
// ② 空 BeatmapHash；③ 目标缺难度 Hash；④ 规则集不可解析（bms fork 专有）；
// ⑤ 挂接成功（含刚同步进来难度的惰性索引挂接 + 隐难度不入索引）。
func TestDeployScoreFiveBranches(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	// 目标库：lazer 镜像 + 一个谱面集（难度 = 挂接锚点）+ 一条隐难度
	//（不入索引，§5.2-3「目标全部非隐藏难度」）。
	setFiles := map[string][]byte{}
	set, err := fixture.MaterializeSet(9101, "Score Artist", "Score Title", "mapperS",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("score anchor osu bytes")}}, nil, setFiles)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	hiddenSet, err := fixture.MaterializeSet(9102, "Score Artist", "Hidden Title", "mapperS",
		[]fixture.DiffMaterial{{Name: "HiddenDiff", OsuBytes: []byte("hidden diff osu bytes")}}, nil, setFiles)
	if err != nil {
		t.Fatalf("MaterializeSet(hidden): %v", err)
	}
	hiddenSet.Beatmaps[0].Hidden = true

	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(),
		Seed:    fixture.Seed{BeatmapSets: []hub.BeatmapSet{set, hiddenSet}, Files: setFiles},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}

	// hub：五条成绩（挂接成功 / 空 Hash / 缺难度 / bms 规则集 / 隐难度锚）。
	files := map[string][]byte{}
	okScore := makeScore("c1111111-1111-4111-8111-1111111111c1", set.Beatmaps[0].Hash, "osu",
		[]byte("ok replay bytes"), files)
	emptyAnchor := makeScore("c2222222-2222-4222-8222-2222222222c2", "", "osu",
		[]byte("empty anchor replay"), files)
	missingBM := makeScore("c3333333-3333-4333-8333-3333333333c3",
		"00000000000000000000000000000000000000000000000000000000c0ffee", "osu",
		[]byte("missing bm replay"), files)
	bmsScore := makeScore("c4444444-4444-4444-8444-4444444444c4", set.Beatmaps[0].Hash, "bms",
		[]byte("bms replay bytes"), files)
	hiddenAnchor := makeScore("c5555555-5555-4555-8555-5555555555c5", hiddenSet.Beatmaps[0].Hash, "osu",
		[]byte("hidden anchor replay"), files)
	scores := []hub.Score{okScore, emptyAnchor, missingBM, bmsScore, hiddenAnchor}

	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkDelta, CollectedAt: t0, Scores: scores})

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files) // .osr 链接先行的仓源
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})

	plan := scorePlan(t, refB.ForkID, refB, scores)
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	// ⑤ 挂接成功恰一条落地。
	if len(report.Applied) != 1 || report.Applied[0].Action != ActionCreate {
		t.Fatalf("Applied = %+v（期望仅挂接成功分支 1 条 create）", report.Applied)
	}
	// ②③④ 逐条计数与人话原因（① 在独立库用例）。
	if n := skipCountOf(report, "BeatmapHash 为空"); n != 1 {
		t.Errorf("空 BeatmapHash 跳过计数 = %d（期望 1），Skipped=%+v", n, report.Skipped)
	}
	if n := skipCountOf(report, "目标库缺少对应谱面 Hash"); n != 2 {
		// 缺难度分支：missingBM（真缺）+ hiddenAnchor（隐难度不入索引）各 1。
		t.Errorf("缺难度 Hash 跳过计数 = %d（期望 2：真缺 + 隐难度），Skipped=%+v", n, report.Skipped)
	}
	if n := skipCountOf(report, "规则集"); n != 1 {
		t.Errorf("规则集不可解析跳过计数 = %d（期望 1），Skipped=%+v", n, report.Skipped)
	}
	// 分组 = 4：空锚 1 + bms 1 + 缺难度 2（人话原因含各自目标 hash 前缀，
	// 用户可定位——按行分组是 SkipCollector 语义，非按模板归并）。
	if len(report.Skipped) != 4 {
		t.Errorf("Skipped 分组 = %d（期望 4），got %+v", len(report.Skipped), report.Skipped)
	}

	// ⑤ 逐字段读回：BeatmapInfo 链到目标难度 + BeatmapHash 列 + Files=[.osr]
	// + RealmFile 行同事务。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := readScoreRow(t, sr, okScore)
		if !ok {
			t.Fatal("挂接成功分支读回缺成绩行")
		}
		assertScoreDeepEqual(t, got, okScore, okScore.OnlineID) // delta→lazer 同域 "ppy"，S8 携带
	})
	// .osr 链接先行：目标 files/ 有回放实体且字节一致。
	filesDir := filestore.ResolveFilesDir(refB.RealmPath)
	got, err := os.ReadFile(filepath.Join(filesDir, okScore.Hash[:1], okScore.Hash[:2], okScore.Hash))
	if err != nil {
		t.Fatalf("目标 files/ 缺回放 %s: %v", hashPrefix(okScore.Hash), err)
	}
	if string(got) != "ok replay bytes" {
		t.Errorf("目标 files/ 回放字节不符")
	}
	// 隐难度不落地、空锚/bms 均零落地（Score 行数恰 1）。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if n := classRowCount(t, sr, "Score"); n != 1 {
			t.Errorf("Score 行数 = %d（期望 1——跳过分支零落地，S7）", n)
		}
	})

	// 重续幂等：同计划重跑 → 已存在 → 跳过，零 applied。
	report2, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %+v（期望 0）", report2.Applied)
	}
	if n := skipCountOf(report2, "重续判定"); n != 1 {
		t.Errorf("重跑重续跳过计数 = %d（期望 1），Skipped=%+v", n, report2.Skipped)
	}

	// ① 目标无 Score 表：独立库（lazer 镜像删 Score 类）整面跳过。
	schema, err := fixture.SchemaFor(fixture.ForkLazer, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	var keptClasses []realmc.ClassDef
	for _, c := range schema.Classes {
		if c.Name == "Score" {
			continue
		}
		keptClasses = append(keptClasses, c)
	}
	schema.Classes = keptClasses
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("建无 Score 表库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭无 Score 表库: %v", err)
	}
	refNoScore := forks.LibraryRef{
		ForkID: fixture.ForkLazer, DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionLazer, VersionSource: forks.VersionFromOpen,
	}
	report3, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx,
		scorePlan(t, refNoScore.ForkID, refNoScore, scores), nil)
	if err != nil {
		t.Fatalf("Deploy(无 Score 表): %v", err)
	}
	if len(report3.Applied) != 0 {
		t.Errorf("无 Score 表 Applied = %+v（期望 0）", report3.Applied)
	}
	if n := skipCountOf(report3, "目标库没有 Score 表"); n != len(scores) {
		t.Errorf("无 Score 表跳过计数 = %d（期望 %d——整面跳过），Skipped=%+v",
			n, len(scores), report3.Skipped)
	}
}

// TestDeployScoreUpdateSameKeyInPlace 同键 (BeatmapHash, Hash) 已存在 →
// 就地更新白名单列：现场预插同键行（OnlineID=现场值）→ canonical 覆盖
// （TotalScore 等 + 挂接补链）→ create 改判 update；异域 canonical
// （origin_fork=g0v0）OnlineID/LegacyOnlineID/User.OnlineID 列不触碰（S8）。
func TestDeployScoreUpdateSameKeyInPlace(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	setFiles := map[string][]byte{}
	set, err := fixture.MaterializeSet(9201, "Upd Artist", "Upd Title", "mapperU",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("update anchor osu bytes")}}, nil, setFiles)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(),
		Seed:    fixture.Seed{BeatmapSets: []hub.BeatmapSet{set}, Files: setFiles},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}

	files := map[string][]byte{}
	sc := makeScore("d1111111-1111-4111-8111-1111111111d1", set.Beatmaps[0].Hash, "osu",
		[]byte("inplace replay bytes"), files)
	h := openMemHub(t)
	// canonical 来源 g0v0（ServerRealm "g0v0.top" → 与目标 delta 异域，S8）。
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkG0v0, CollectedAt: t0, Scores: []hub.Score{sc}})
	plan := scorePlan(t, refB.ForkID, refB, []hub.Score{sc})

	// 现场预插同键行（模拟目标端已有同键本地成绩；OnlineID=现场独有值，
	// 关系留空——历史解绑态，§5.2 就地更新应补挂）。
	insertBareScore(t, lib, refB.RealmPath, sc, 777001)

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(report.Applied) != 1 || report.Applied[0].Action != ActionUpdate {
		t.Fatalf("Applied = %+v（期望 1 条 update——同键改判）", report.Applied)
	}
	if len(report.Repointed) != 1 || !strings.Contains(report.Repointed[0].Reason, "新建 → 就地更新") {
		t.Errorf("Repointed = %+v（期望 1 条新建→就地更新）", report.Repointed)
	}

	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if n := classRowCount(t, sr, "Score"); n != 1 {
			t.Errorf("Score 行数 = %d（期望 1——就地更新不建第二行）", n)
		}
		got, ok := readScoreRow(t, sr, sc)
		if !ok {
			t.Fatal("就地更新后读回缺成绩")
		}
		// 白名单列覆盖 canonical（抽查 TotalScore 与挂接补链）。
		if got.TotalScore != sc.TotalScore {
			t.Errorf("TotalScore = %d，期望 canonical %d", got.TotalScore, sc.TotalScore)
		}
		if got.BeatmapHash != sc.BeatmapHash {
			t.Errorf("BeatmapInfo 关系未补挂（读回挂接 Hash = %s，期望 %s）", got.BeatmapHash, sc.BeatmapHash)
		}
		// S8：异域更新不触碰 OnlineID 三列（现场值 777001 保留）。
		if got.OnlineID != 777001 {
			t.Errorf("OnlineID = %d，期望保持现场值 777001（S8 异域更新不触碰）", got.OnlineID)
		}
		if got.User.OnlineID != 321 {
			t.Errorf("User.OnlineID = %d，期望保持现场值 321（S8）", got.User.OnlineID)
		}
	})
}

// insertBareScore 向目标库现场插入一行同键最小形态成绩（Hash + BeatmapHash
// + OnlineID/User.OnlineID 现场独有值 + TotalScore 异于 canonical），模拟
// 计划构建后目标已有的同键本地行（§3.3 执行期现场复核夹具）。
func insertBareScore(t *testing.T, lib *realmc.Library, realmPath string, sc hub.Score, onlineID int64) {
	t.Helper()
	r, err := lib.OpenDynamicReadWrite(realmPath)
	if err != nil {
		t.Fatalf("写会话打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginWrite(); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	ck, err := r.FindClass("Score")
	if err != nil {
		t.Fatalf("FindClass(Score): %v", err)
	}
	obj, err := r.CreateObject(ck)
	if err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("建行: %v", err)
	}
	defer r.ReleaseObject(obj)
	keys := map[string]int64{}
	for _, name := range []string{"Hash", "BeatmapHash", "OnlineID", "TotalScore", "User"} {
		key, err := r.FindProperty(ck, name)
		if err != nil {
			r.Rollback() //nolint:errcheck
			t.Fatalf("FindProperty(%s): %v", name, err)
		}
		keys[name] = key
	}
	for _, w := range []func() error{
		func() error { return r.SetString(obj, keys["Hash"], sc.Hash) },
		func() error { return r.SetString(obj, keys["BeatmapHash"], sc.BeatmapHash) },
		func() error { return r.SetInt64(obj, keys["OnlineID"], onlineID) },
		func() error { return r.SetInt64(obj, keys["TotalScore"], 1) }, // 异于 canonical
	} {
		if err := w(); err != nil {
			r.Rollback() //nolint:errcheck
			t.Fatalf("现场注入写入: %v", err)
		}
	}
	user, err := r.SetEmbedded(obj, keys["User"])
	if err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("建 User embedded: %v", err)
	}
	userCK, err := r.FindClass("RealmUser")
	if err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("FindClass(RealmUser): %v", err)
	}
	uid, err := r.FindProperty(userCK, "OnlineID")
	if err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("FindProperty(User.OnlineID): %v", err)
	}
	if err := r.SetInt64(user, uid, 321); err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("写 User.OnlineID: %v", err)
	}
	r.ReleaseObject(user)
	if err := r.Commit(); err != nil {
		t.Fatalf("提交注入: %v", err)
	}
}

// TestDeployScoreLazyIndexAfterSetBatch 惰性索引：同一次投放中谱面集批先
// 于成绩批——本次刚同步进来的难度也能挂上（§5.2-3 ??= 语义）。
func TestDeployScoreLazyIndexAfterSetBatch(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(9301, "Lazy Artist", "Lazy Title", "mapperL",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("lazy set osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	set.ID = "93111111-1111-4311-8311-111111111131"
	set.Beatmaps[0].ID = "93222222-2222-4322-8322-222222222232"
	sc := makeScore("e1111111-1111-4111-8111-1111111111e1", set.Beatmaps[0].Hash, "osu",
		[]byte("lazy index replay"), files)

	// 目标 = delta 空库：谱面集与成绩同计划投放（谱面批在前）。
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	// canonical 同源 delta（同域 "ppy"，OnlineID 断言不受 S8 干扰）。
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkDelta, CollectedAt: t0,
		BeatmapSets: []hub.BeatmapSet{set}, Scores: []hub.Score{sc}})
	plan, err := NewPlan(refB.ForkID, []PlanItem{
		{Kind: hub.KindBeatmapSet, ContentKey: set.Hash, Action: ActionCreate, Summary: "谱面集"},
		{Kind: hub.KindScore, ContentKey: hub.ScoreContentKey(sc.BeatmapHash, sc.Hash), Action: ActionCreate, Summary: "成绩"},
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	plan.Target = refB

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("Applied = %+v（期望谱面集 + 成绩各 1）", report.Applied)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("Skipped = %+v（期望空——成绩应挂上刚同步的难度）", report.Skipped)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := readScoreRow(t, sr, sc)
		if !ok {
			t.Fatal("读回缺成绩（惰性索引应挂上刚同步的难度）")
		}
		assertScoreDeepEqual(t, got, sc, sc.OnlineID)
	})
}

// TestSplitScoreContentKey 组合键拆解的边界（解析侧契约：空 BeatmapHash
// 是 §5.2-2 跳过分支的合法输入，replayHash 段必须非空）。
func TestSplitScoreContentKey(t *testing.T) {
	bh := strings.Repeat("ab", 32)
	rh := strings.Repeat("cd", 32)
	gotBh, gotRh, err := splitScoreContentKey(hub.ScoreContentKey(bh, rh))
	if err != nil || gotBh != bh || gotRh != rh {
		t.Fatalf("拆解 = (%s, %s, %v)，期望 (%s, %s)", gotBh, gotRh, err, bh, rh)
	}
	if gotBh, gotRh, err := splitScoreContentKey(hub.ScoreContentKey("", rh)); err != nil || gotBh != "" || gotRh != rh {
		t.Errorf("空 BeatmapHash 拆解 = (%q, %q, %v)，应合法（跳过分支输入）", gotBh, gotRh, err)
	}
	for _, bad := range []string{"", "abc|", "no-separator"} {
		if _, _, err := splitScoreContentKey(bad); err == nil {
			t.Errorf("splitScoreContentKey(%q) 应报错", bad)
		}
	}
}
