package deploy

// §5.4 目标端兼容过滤的投放验收（块 2/7）：mods acronym 白名单（宁拒
// 勿杀，D7）、Statistics/MaximumStatistics 未知 HitResult 键过滤后写与
// 解析失败整条跳过（D7）、市场规则集行成绩的执行期兜底（D8）、外部
// 托管集的预演 + 防线（D5）、S8 跨服务器 OnlineID 双向（g0v0↔官方：
// 异域新行 -1 / 更新行不触碰）。预演（adaptScoreForTarget 纯函数/
// BuildPlan skip 项）与执行期现场复核两处判定一致（§3.3）。
// librealm 缺失时显式 SKIP（§8.1 补充门：不得静默绿）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

// TestDeployScoreTargetCompatFilters D7 成绩内容兼容 + D8 市场规则集兜底：
// ① 未知 HitResult 键过滤后写（已知键保留、被滤键计数）；② 非官方 mod
// 整条跳过；③ Statistics 解析失败整条跳过；④ 指向市场规则集的 hub 已收
// 行经「规则集不可解析」防线跳过。
func TestDeployScoreTargetCompatFilters(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	setFiles := map[string][]byte{}
	set, err := fixture.MaterializeSet(9201, "Compat Artist", "Compat Title", "mapperC",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("compat anchor osu bytes")}}, nil, setFiles)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(),
		Seed:    fixture.Seed{BeatmapSets: []hub.BeatmapSet{set}, Files: setFiles},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}

	files := map[string][]byte{}
	// ① 未知键成绩：Statistics 混 1 个未知键、MaximumStatistics 混 1 个，
	// mods 全官方（HD）→ 整条落地、键过滤后写（great/legacy_combo_increase
	// 是官方 HitResult 键，保留）。
	statsUnknown := makeScore("d1111111-1111-4111-8111-1111111111d1", set.Beatmaps[0].Hash, "osu",
		[]byte("stats unknown replay"), files)
	statsUnknown.StatisticsJson = `{"great":900,"ez_unknown_result":42,"legacy_combo_increase":7}`
	statsUnknown.MaximumStatisticsJson = `{"great":1000,"bms_flash":5}`
	// ② 非官方 mod：EZX 不在官方名录 → 整条跳过。
	modEZX := makeScore("d2222222-2222-4222-8222-2222222222d2", set.Beatmaps[0].Hash, "osu",
		[]byte("ezx mod replay"), files)
	modEZX.ModsJson = `[{"acronym":"EZX","settings":{}}]`
	// ③ Statistics 解析失败 → 整条跳过。
	badStats := makeScore("d3333333-3333-4333-8333-3333333333d3", set.Beatmaps[0].Hash, "osu",
		[]byte("bad stats replay"), files)
	badStats.StatisticsJson = `{"great":900,` // 非法 JSON
	// ④ 市场规则集成绩（D8 的 hub 已收行兜底）：收集侧 D8 已滤新行，
	// M2 旧数据/手工构造的行在投放期被「规则集不可解析」防线拦下。
	market := makeScore("d4444444-4444-4444-8444-4444444444d4", set.Beatmaps[0].Hash, "g0v0-custom",
		[]byte("market ruleset replay"), files)

	scores := []hub.Score{statsUnknown, modEZX, badStats, market}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkEz2Lazer, CollectedAt: t0, Scores: scores})

	// 预演一致性（§3.3）：纯函数给出的拒绝原因与转换结果即执行期写面
	// 的输入——预演与执行期共用同一判定。
	_, droppedPreview, rejectPreview := adaptScoreForTarget(statsUnknown, fixture.ForkLazer)
	if droppedPreview != 2 || rejectPreview != "" {
		t.Errorf("预演 statsUnknown = (dropped=%d, reject=%q)，期望 (2, \"\")", droppedPreview, rejectPreview)
	}
	_, _, rejectMod := adaptScoreForTarget(modEZX, fixture.ForkLazer)
	if !strings.Contains(rejectMod, "EZX") {
		t.Errorf("预演 modEZX 拒绝原因 = %q，期望含 EZX", rejectMod)
	}

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).
		Deploy(ctx, scorePlan(t, refB.ForkID, refB, scores), nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	// ① 唯一落地。
	if len(report.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望仅未知键成绩落地）", report.Applied)
	}
	// ②③④ 整条跳过逐条计数与人话原因；被滤键 3 个单独计数。
	if n := skipCountOf(report, "不认识的 mod"); n != 1 {
		t.Errorf("非官方 mod 跳过计数 = %d（期望 1），Skipped=%+v", n, report.Skipped)
	}
	if n := skipCountOf(report, "无法解析"); n != 1 {
		t.Errorf("Statistics 解析失败跳过计数 = %d（期望 1），Skipped=%+v", n, report.Skipped)
	}
	if n := skipCountOf(report, "规则集"); n != 1 {
		t.Errorf("市场规则集跳过计数 = %d（期望 1，D8 兜底），Skipped=%+v", n, report.Skipped)
	}
	if n := skipCountOf(report, "不认识的 HitResult 键"); n != 2 {
		t.Errorf("未知键过滤计数 = %d（期望 2），Skipped=%+v", n, report.Skipped)
	}
	// ① 读回：已知键保留（键排序重序列化），未知键消失。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := readScoreRow(t, sr, statsUnknown)
		if !ok {
			t.Fatal("未知键成绩未落地")
		}
		if got.StatisticsJson != `{"great":900,"legacy_combo_increase":7}` {
			t.Errorf("Statistics = %q，期望过滤后 {great, legacy_combo_increase}", got.StatisticsJson)
		}
		if got.MaximumStatisticsJson != `{"great":1000}` {
			t.Errorf("MaximumStatistics = %q，期望过滤后 {great}", got.MaximumStatisticsJson)
		}
		if got.ModsJson != statsUnknown.ModsJson {
			t.Errorf("ModsJson = %q，期望原样（全官方 mod 不过滤）", got.ModsJson)
		}
		if n := classRowCount(t, sr, "Score"); n != 1 {
			t.Errorf("Score 行数 = %d（期望 1——②③④零落地）", n)
		}
	})
}

// TestDeployExternalSetGuard D5 外部托管集的投放防线：hub 已收行（M2 旧
// 数据/手工构造）在预演（BuildPlan skip 项）与执行期（writer 防线）两处
// 一致拒绝，目标库零落地。
func TestDeployExternalSetGuard(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	extSet := hub.BeatmapSet{
		ID: "e1111111-1111-4111-8111-1111111111e1",
		// 路径编码 Hash（非内容哈希）：判定只认前缀，直接构造。
		Hash:     "ext:set:L2Jtcy9saWI=",
		OnlineID: 5006,
	}
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkEz2Lazer, CollectedAt: t0,
		BeatmapSets: []hub.BeatmapSet{extSet}})

	// 预演：BuildPlan 产 skip 项（人话原因含 D5 锚点）。
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != ActionSkip ||
		!strings.Contains(plan.Items[0].SkipReason, "外部托管集") {
		t.Fatalf("计划项 = %+v，期望唯一 skip 项（外部托管集，D5）", plan.Items)
	}
	// 执行期复核：skip 项如实计数；再以手工 create 项直验 writer 防线
	//（绕过预演——现场复核与预演判定共用 externalSetHash）。
	report, err := NewRunner(Deps{Hub: h}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if n := skipCountOf(report, "外部托管集"); n != 1 {
		t.Errorf("外部托管集跳过计数 = %d（期望 1），Skipped=%+v", n, report.Skipped)
	}

	manual, err := NewPlan(refB.ForkID, []PlanItem{{
		Kind: hub.KindBeatmapSet, ContentKey: extSet.Hash, Action: ActionCreate,
	}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	manual.Target = refB
	// 两次 Deploy 同一 realmPath，毫秒级时间戳撞名（S2 绝不覆盖）。
	time.Sleep(2 * time.Millisecond)
	report2, err := NewRunner(Deps{Hub: h}).Deploy(ctx, manual, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if n := skipCountOf(report2, "外部托管集"); n != 1 {
		t.Errorf("writer 防线跳过计数 = %d（期望 1），Skipped=%+v", n, report2.Skipped)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if n := classRowCount(t, sr, "BeatmapSet"); n != 0 {
			t.Errorf("BeatmapSet 行数 = %d（期望 0——外部托管集零落地）", n)
		}
	})
}

// TestDeployS8CrossRealmG0v0Bidirectional S8 跨服务器 OnlineID 双向
//（g0v0 ServerRealm=g0v0.top vs 官方系 ppy，§4.4）：
//   - 官方 → g0v0 异域：新行 OnlineID 置 -1（不对拷官方服务器标识）；
//   - g0v0 目标已有同键本地行（OnlineID=99001 服务器回填）：异域更新
//     不触碰该列（保目标本地标识），其余白名单列照常 canonical 覆盖；
//   - g0v0 → 官方异域：新行同样置 -1。
func TestDeployS8CrossRealmG0v0Bidirectional(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(9301, "S8 Artist", "S8 Title", "mapperS8",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("s8 anchor osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	localReplay := []byte("g0v0 local replay bytes")
	localHash := sha256Of(localReplay)
	files[localHash] = localReplay

	// g0v0 目标库：挂接锚点集 + 一条本地同键成绩（OnlineID=99001 为
	// g0v0 服务器本地标识，D10/R1——投放更新不得触碰）。
	localScore := fixture.SeedScore{
		Guid:             "f2222222-2222-4222-8222-2222222222f2",
		BeatmapHash:      set.Beatmaps[0].Hash,
		Hash:             localHash,
		RulesetShortName: "osu",
		OnlineID:         99001,
		TotalScore:       111111, // 与 canonical 不同 → 触发 update 覆盖
		ReplayHashes:     []string{localHash},
	}
	g0v0Ref, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkG0v0, SchemaVersion: fixture.VersionG0v0,
		DataDir: t.TempDir(),
		Seed: fixture.Seed{
			BeatmapSets: []hub.BeatmapSet{set},
			Scores:      []fixture.SeedScore{localScore},
			Files:       files,
		},
	})
	if err != nil {
		t.Fatalf("fixture.Build(g0v0): %v", err)
	}
	// 官方目标库：同一锚点集（新行场景的独立库）。
	lazerRef, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(),
		Seed:    fixture.Seed{BeatmapSets: []hub.BeatmapSet{set}, Files: files},
	})
	if err != nil {
		t.Fatalf("fixture.Build(lazer): %v", err)
	}

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}

	// 场景 A+B：官方 → g0v0（异域）。canonical 行与本地行同键
	//（BeatmapHash+回放 Hash），TotalScore 不同 → 现场改判 update。
	crossScore := makeScore("f1111111-1111-4111-8111-1111111111f1",
		set.Beatmaps[0].Hash, "osu", localReplay, files)
	crossScore.OnlineID = 12345 // 官方服务器标识，不得带入 g0v0
	// 场景 C 的回放入池后统一补仓（fillStore 先于任一 Deploy 即可）。
	fromG0v0 := makeScore("f3333333-3333-4333-8333-3333333333f3",
		set.Beatmaps[0].Hash, "osu", []byte("g0v0 source replay"), files)
	fromG0v0.OnlineID = 99001 // g0v0 服务器本地标识
	fillStore(t, store, files)
	hA := openMemHub(t)
	applySnap(t, hA, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0,
		Scores: []hub.Score{crossScore}})
	reportA, err := NewRunner(Deps{Hub: hA, Files: filestore.NewEngine(store)}).
		Deploy(ctx, scorePlan(t, g0v0Ref.ForkID, g0v0Ref, []hub.Score{crossScore}), nil)
	if err != nil {
		t.Fatalf("Deploy(→g0v0): %v（report=%+v）", err, reportA)
	}
	if len(reportA.Applied) != 1 || reportA.Applied[0].Action != ActionUpdate {
		t.Fatalf("Applied = %+v（期望 1 条 update——同键行现场改判）", reportA.Applied)
	}
	if len(reportA.Repointed) != 1 || !strings.Contains(reportA.Repointed[0].Reason, "就地更新") {
		t.Errorf("Repointed = %+v（期望「新建 → 就地更新」改判记录）", reportA.Repointed)
	}
	withSetReader(t, lib, g0v0Ref.RealmPath, func(sr *setReader) {
		got, ok := readScoreRow(t, sr, crossScore)
		if !ok {
			t.Fatal("g0v0 目标缺成绩行")
		}
		if got.OnlineID != 99001 {
			t.Errorf("异域更新 OnlineID = %d，期望保留目标本地值 99001（S8 不触碰）", got.OnlineID)
		}
		if got.TotalScore != crossScore.TotalScore {
			t.Errorf("异域更新 TotalScore = %d，期望 canonical %d（白名单列照常覆盖）",
				got.TotalScore, crossScore.TotalScore)
		}
	})

	// 场景 C：g0v0 → 官方（异域）：新行 OnlineID 置 -1。
	hC := openMemHub(t)
	applySnap(t, hC, hub.Snapshot{ForkID: fixture.ForkG0v0, CollectedAt: t0,
		Scores: []hub.Score{fromG0v0}})
	reportC, err := NewRunner(Deps{Hub: hC, Files: filestore.NewEngine(store)}).
		Deploy(ctx, scorePlan(t, lazerRef.ForkID, lazerRef, []hub.Score{fromG0v0}), nil)
	if err != nil {
		t.Fatalf("Deploy(→lazer): %v（report=%+v）", err, reportC)
	}
	if len(reportC.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望 1 条 create）", reportC.Applied)
	}
	withSetReader(t, lib, lazerRef.RealmPath, func(sr *setReader) {
		got, ok := readScoreRow(t, sr, fromG0v0)
		if !ok {
			t.Fatal("lazer 目标缺成绩行")
		}
		if got.OnlineID != -1 {
			t.Errorf("异域新行 OnlineID = %d，期望 -1（S8 跨服不对拷）", got.OnlineID)
		}
	})
}

// TestAdaptModPresetModsForTarget ModPreset 的 D7 mod 白名单纯函数分支：
// 未注册目标 fork / 空 ModsJson / 名录外 / 解析失败。
func TestAdaptModPresetModsForTarget(t *testing.T) {
	mp := hub.ModPreset{Name: "P", RulesetShortName: "osu"}
	if r := adaptModPresetModsForTarget(mp, "lazer"); r != "" {
		t.Errorf("空 ModsJson 应放行，got %q", r)
	}
	if r := adaptModPresetModsForTarget(mp, "unknown-fork"); r == "" {
		t.Errorf("未注册目标 fork 应保守拒绝")
	}
	mp.ModsJson = `[{"acronym":"HD"},{"acronym":"CL"}]`
	if r := adaptModPresetModsForTarget(mp, "lazer"); r != "" {
		t.Errorf("名录内 mods 应放行，got %q", r)
	}
	mp.ModsJson = `[{"acronym":"HD"},{"acronym":"EZX"}]`
	if r := adaptModPresetModsForTarget(mp, "lazer"); r == "" || !strings.Contains(r, "EZX") {
		t.Errorf("名录外 mod 应整条拒绝（got %q）", r)
	}
	mp.ModsJson = `not-json`
	if r := adaptModPresetModsForTarget(mp, "lazer"); r == "" {
		t.Errorf("ModsJson 解析失败应整条拒绝")
	}
}

// --- 全局交集（§0-B3/G6） ---

// TestAdaptScoreForIntersection 成绩交集适配：目标端自身拒绝不加前缀；
// 因其他 peer（未注册端 = 保守拒绝）被拒 → 原因带 IntersectionRejectPrefix
// 且点名该端；键过滤按交集逐端累积（四端名录同构时与单端一致）。
func TestAdaptScoreForIntersection(t *testing.T) {
	sc := hub.Score{Hash: "sc-x", ModsJson: `[{"acronym":"HD"}]`,
		StatisticsJson: `{"great":1,"nonstandard_hit":2}`}
	out, dropped, rej := adaptScoreForIntersection(sc, "lazer", []string{"lazer", "delta"})
	if rej != "" {
		t.Fatalf("交集内成绩不应被拒: %q", rej)
	}
	if dropped != 1 {
		t.Errorf("键过滤计数 = %d，期望 1（nonstandard_hit 被滤）", dropped)
	}
	if !strings.Contains(out.StatisticsJson, "great") || strings.Contains(out.StatisticsJson, "nonstandard_hit") {
		t.Errorf("交集过滤后 StatisticsJson = %s，期望只含 great", out.StatisticsJson)
	}
	// 目标端自身拒绝：不带前缀（mod 名录外）。
	scBad := hub.Score{Hash: "sc-y", ModsJson: `[{"acronym":"EZX"}]`}
	_, _, rej = adaptScoreForIntersection(scBad, "lazer", []string{"lazer", "ghost"})
	if strings.HasPrefix(rej, IntersectionRejectPrefix) || !strings.Contains(rej, "EZX") {
		t.Errorf("目标端自身拒绝应不加前缀: %q", rej)
	}
	// peer 未注册 → 保守拒绝 + 前缀 + 点名。
	_, _, rej = adaptScoreForIntersection(sc, "lazer", []string{"lazer", "ghost"})
	if !strings.HasPrefix(rej, IntersectionRejectPrefix) || !strings.Contains(rej, "ghost") {
		t.Errorf("peer 未注册拒绝 = %q，期望 %q…ghost…", rej, IntersectionRejectPrefix)
	}
}

// TestAdaptModPresetForIntersection 模组预设交集：peer 不认识/未注册 →
// 前缀拒绝；目标端名录外 mod → 不加前缀。
func TestAdaptModPresetForIntersection(t *testing.T) {
	mp := hub.ModPreset{Name: "P", RulesetShortName: "osu", ModsJson: `[{"acronym":"HD"}]`}
	if r := adaptModPresetForIntersection(mp, "lazer", []string{"lazer", "delta", "g0v0"}); r != "" {
		t.Fatalf("名录内 mod 交集内应放行: %q", r)
	}
	if r := adaptModPresetForIntersection(mp, "lazer", []string{"lazer", "ghost"}); !strings.HasPrefix(r, IntersectionRejectPrefix) {
		t.Errorf("peer 未注册拒绝 = %q，期望带交集前缀", r)
	}
	mp.ModsJson = `[{"acronym":"EZX"}]`
	if r := adaptModPresetForIntersection(mp, "lazer", []string{"lazer", "ghost"}); strings.HasPrefix(r, IntersectionRejectPrefix) {
		t.Errorf("目标端自身名录外拒绝不应加前缀: %q", r)
	}
}

// TestKeyBindingActionAllowedIntersection 键位交集：ActionInt 超出任一
// 目标端枚举域即拒。delta 的 GlobalAction 值域上界 99（D20 实测）——
// ActionInt=100 的全局键位过 lazer 目标端、被 delta peer 拒。
func TestKeyBindingActionAllowedIntersection(t *testing.T) {
	kb := hub.KeyBinding{ActionInt: 100, KeyCombination: "Ctrl+K"} // 全局键位
	if ok, r := keyBindingActionAllowedIntersection("lazer", "lazer", []string{"lazer"}, kb); !ok {
		t.Fatalf("仅 lazer 交集应放行 ActionInt=100: %q", r)
	}
	ok, reason := keyBindingActionAllowedIntersection("lazer", "lazer", []string{"lazer", "delta"}, kb)
	if ok {
		t.Fatal("ActionInt=100 越出 delta 枚举域（GlobalMax=99），应被交集拒绝")
	}
	if !strings.HasPrefix(reason, IntersectionRejectPrefix) || !strings.Contains(reason, "delta") {
		t.Errorf("交集拒绝原因 = %q，期望前缀 + 点名 delta", reason)
	}
	// 目标端自身域内但源端域外（ActionInt=200 超 lazer GlobalMax=121）→
	// 不加前缀（源端/目标端判定走原原因文本）。
	kb2 := hub.KeyBinding{ActionInt: 200, KeyCombination: "Ctrl+K"}
	if ok, r := keyBindingActionAllowedIntersection("lazer", "lazer", []string{"lazer", "delta"}, kb2); ok {
		t.Fatal("ActionInt=200 应被源端枚举域拒绝")
	} else if strings.HasPrefix(r, IntersectionRejectPrefix) {
		t.Errorf("源端自身域越界拒绝不应加前缀: %q", r)
	}
}
