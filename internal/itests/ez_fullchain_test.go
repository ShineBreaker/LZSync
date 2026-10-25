package itests

// Ez 夹具全链验收（design.md §7-M3-1 / §5.4 目标端兼容过滤 / §8.2-4）：
// Ez 座（EzDiffSeed：bms 集+成绩、判定档成绩、外部托管 set、7 张技能表
// 演示行、Ez 专有皮肤）真实收集 → 连带行逐条丢弃 + 计数告知 + hub 无
// 污染行；随后 lazer 源收集 → BuildPlan → 投放回 Ez 座 → 现场读回断言：
// ① 覆盖写入（集 conflict update + 成绩改判 update）时 10 个 Ez 扩展列
// 原值保留——集行 ExternalContentRoot/HostingKind、难度行 XxyStarRating/
// PerformancePoints/HasVideo/HasStoryboard、成绩行 JudgeMode/HealthMode、
// Ruleset 行 LastAppliedXxySrVersion/LastAppliedManiaSkillVersion（写面
// 白名单不含这些列，覆盖只动白名单列）；② StatisticsJson/Maximum
// StatisticsJson 未知键过滤后写入（目标库值断言 + 键级计数告知，D7）；
// ③ 技能表/外部托管集/bms 规则集行留现场（收集面排除或连带丢弃、投放
// 不触碰——「丢弃」指不跨端，绝不删游戏侧数据）。librealm 缺失时显式
// SKIP（§8.1）。

import (
	"context"
	"path/filepath"
	"testing"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// Ez 10 个扩展列（forks.Profile.ExtColumns 全量对照面）的现场预置值——
// 取确定性非零值，零值无法区分「保留」与「没写」。
const (
	ezPresetExternalRoot = "/preset/ez-fullchain/root"
	ezPresetHostingKind  = int64(2) // 非 1（1 = External，避免与外部托管集判定混淆）
	ezPresetXxySR        = 5.25
	ezPresetPerfPoints   = 333.75
	ezPresetXxySrVersion = int64(7)
	ezPresetManiaVersion = int64(9)
)

// presetEzColumns 向 Ez 座现场预置 10 个扩展列的非零值（fixture 播种面
// 不携带这些列，覆盖写入断言的「原值」基准）。
func presetEzColumns(t *testing.T, lib *realmc.Library, realmPath string, set hub.BeatmapSet) {
	t.Helper()
	mutate(t, lib, realmPath, func(r *realmc.Realm) error {
		setObj, err := findRowSoft(r, "BeatmapSet", "Hash", set.Hash)
		if err != nil || setObj == nil {
			t.Fatalf("预置：找不到集行 %s: %v", set.Hash, err)
		}
		defer r.ReleaseObject(setObj)
		if err := r.SetString(setObj, propKey(t, r, "BeatmapSet", "ExternalContentRoot"), ezPresetExternalRoot); err != nil {
			return err
		}
		if err := r.SetInt64(setObj, propKey(t, r, "BeatmapSet", "HostingKind"), ezPresetHostingKind); err != nil {
			return err
		}

		for _, bm := range set.Beatmaps {
			bmObj, err := findRowSoft(r, "Beatmap", "Hash", bm.Hash)
			if err != nil || bmObj == nil {
				t.Fatalf("预置：找不到难度行 %s: %v", bm.Hash, err)
			}
			for _, w := range []func() error{
				func() error {
					return r.SetFloat64(bmObj, propKey(t, r, "Beatmap", "XxyStarRating"), ezPresetXxySR)
				},
				func() error {
					return r.SetFloat64(bmObj, propKey(t, r, "Beatmap", "PerformancePoints"), ezPresetPerfPoints)
				},
				func() error {
					return r.SetBool(bmObj, propKey(t, r, "Beatmap", "HasVideo"), true)
				},
				func() error {
					return r.SetBool(bmObj, propKey(t, r, "Beatmap", "HasStoryboard"), true)
				},
			} {
				if err := w(); err != nil {
					r.ReleaseObject(bmObj)
					return err
				}
			}
			r.ReleaseObject(bmObj)
		}

		rsObj, err := findRowSoft(r, "Ruleset", "ShortName", "osu")
		if err != nil || rsObj == nil {
			t.Fatalf("预置：找不到 osu 规则集行: %v", err)
		}
		defer r.ReleaseObject(rsObj)
		for _, w := range []struct {
			name string
			val  int64
		}{{"LastAppliedXxySrVersion", ezPresetXxySrVersion}, {"LastAppliedManiaSkillVersion", ezPresetManiaVersion}} {
			if err := r.SetInt64(rsObj, propKey(t, r, "Ruleset", w.name), w.val); err != nil {
				return err
			}
		}
		return nil
	})
}

func TestEzCompatFullChain(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	// ---- Ez 座：差异夹具 + 覆盖写入的现场预置 ----
	externalRoot := t.TempDir()
	seed, err := fixture.EzDiffSeed(externalRoot)
	if err != nil {
		t.Fatalf("EzDiffSeed: %v", err)
	}
	stdSet := seed.BeatmapSets[0] // 1001 标准集（与 lazer 源同内容键）
	bmsSet := seed.BeatmapSets[1] // 5005 bms 集（难度/成绩连带丢弃夹具）
	// 标准成绩预置判定档列（收集侧丢弃夹具 + 投放侧保留断言面）+ 现场差异
	// 值（触发 writeScore 真覆盖，非重续幂等跳过）；集行 Status 现场差异 →
	// 对账 conflict → update（StatusInt 在冲突判据内）。注意直接改 seed
	// 元素（副本赋值不回写播种）。
	seed.Scores[1].JudgeMode, seed.Scores[1].HealthMode = 31, 34
	seed.Scores[1].TotalScore = 1
	seed.BeatmapSets[0].StatusInt = 0
	stdScore := seed.Scores[1]
	refEz := buildLib(t, fixture.ForkEz2Lazer, fixture.VersionEzDisk, seed)
	presetEzColumns(t, lib, refEz.RealmPath, stdSet)

	// ---- 收集 Ez：连带行逐条丢弃 + 计数告知（§5.4：这些 Ez 侧产物在收集
	// 面即拦，不入 hub 污染其他端）----
	repEz := e.collectInto(refEz)
	for reason, want := range map[string]int{
		"Ez 外部托管谱面集":                    1, // D5：ext:set: Hash + HostingKind=1
		"fork 专有规则集难度（OwnRulesets）": 1, // D6：bms 集唯一难度
		"fork 专有规则集成绩（OwnRulesets）": 1, // D6：指向 bms 的成绩
		"fork 专有判定档成绩":                 2, // R7：judge 夹具行 + 预置判定档的标准成绩
		"fork 专有皮肤":                     1, // D21：Ez 专有皮肤指纹（Guid 通道）
	} {
		if got := collectSkip(repEz, reason); got != want {
			t.Errorf("收集 Ez 跳过 %q = %d，期望 %d（Skips=%+v）", reason, got, want, repEz.Skips)
		}
	}
	extHash := fixture.EncodeExtSetHash(externalRoot)
	assertHubKeys(t, e, `SELECT content_hash FROM beatmap_set WHERE superseded_by IS NULL`,
		map[string]bool{stdSet.Hash: true, bmsSet.Hash: true}, "beatmap_set 链尾")
	assertHubCount(t, e, `SELECT COUNT(*) FROM beatmap_set WHERE content_hash=?`, 0,
		"外部托管集行（D5 收集侧应拦）", extHash)
	assertHubCount(t, e, `SELECT COUNT(*) FROM score`, 0, "score（三条成绩全部被规则丢弃）")
	assertHubCount(t, e, `SELECT COUNT(*) FROM skin`, 0, "skin（Ez 专有皮肤被指纹丢弃）")

	// ---- lazer 源：标准种子 + 未知 StatisticsJson 键的合法成绩 ----
	lazerSeed, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	files := lazerSeed.Files
	statsScore, err := fixture.MaterializeScore(lazerSeed.BeatmapSets[0].Beatmaps[0],
		"lazer 2026.1001.0", []byte("ez stats unknown replay bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeScore(stats): %v", err)
	}
	statsScore.StatisticsJson = `{"great":900,"ez_unknown_result":42,"legacy_combo_increase":7}`
	statsScore.MaximumStatisticsJson = `{"great":1000,"ez_max_unknown":1}`
	lazerSeed.Scores = append(lazerSeed.Scores, statsScore)
	lazerSeed.Files = files
	refLazer := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, lazerSeed)
	e.collectInto(refLazer)

	// ---- 计划（§0-B2/B4：canonical 首见即定（Ez 先收确立）+ 冲突默认不
	// 落地——集 update 覆盖路径由显式构造的动作项承载（数据页勾选冲突行的
	// 手动处置）；canonical 换用 lazer 侧取值经第二个 hub 重演）+ 成绩手工
	// 补项（BuildPlan 的 score 面未放开，fullchain_test 先例）。----
	// Ez 侧 stdSet presence 与其自身 canonical 一致 → 主 hub 上无计划项。
	plan, err := deploy.BuildPlan(ctx, e.hub, refEz, deploy.BuildOptions{Files: e.engine.Store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, it := range plan.Items {
		if it.Kind == hub.KindBeatmapSet && it.ContentKey == stdSet.Hash && it.Action != deploy.ActionSkip {
			t.Fatalf("stdSet 计划动作 = %s（§0-B4 冲突应 skip），plan=%+v", it.Action, plan.Items)
		}
	}
	h2, err := hub.Open(filepath.Join(t.TempDir(), "hub2.db"))
	if err != nil {
		t.Fatalf("hub.Open(h2): %v", err)
	}
	defer h2.Close()
	if _, err := collect.NewRunner(collect.Deps{
		Hub: h2, Files: e.engine, Snapshots: e.snaps, Guard: guard.NewGuard(nil),
	}).Collect(ctx, refLazer, nil); err != nil {
		t.Fatalf("h2 收集 lazer: %v", err)
	}
	plan2, err := deploy.NewPlan(refEz.ForkID, []deploy.PlanItem{
		{
			Kind: hub.KindBeatmapSet, ContentKey: stdSet.Hash,
			Action: deploy.ActionUpdate, Summary: "谱面集 " + stdSet.Hash[:12] + "…",
		},
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	plan2.Target = refEz
	for _, sc := range []fixture.SeedScore{lazerSeed.Scores[0], statsScore} {
		plan2.Items = append(plan2.Items, deploy.PlanItem{
			Kind: hub.KindScore, ContentKey: hub.ScoreContentKey(sc.BeatmapHash, sc.Hash),
			Action: deploy.ActionCreate, Summary: "成绩 " + sc.Hash[:12] + "…",
		})
	}

	runner := deploy.NewRunner(deploy.Deps{
		Hub: h2, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	})
	report, err := runner.Deploy(ctx, plan2, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.FailedBatches) != 0 || len(report.NotRun) != 0 {
		t.Fatalf("失败/未执行段非空: %+v / %+v", report.FailedBatches, report.NotRun)
	}
	// 覆盖写入确实发生：集 update + 标准成绩现场同键改判 update + 未知键成绩 create。
	var setUpdates, scoreUpdates, scoreCreates int
	for _, ap := range report.Applied {
		switch {
		case ap.Kind == hub.KindBeatmapSet && ap.Action == deploy.ActionUpdate:
			setUpdates++
		case ap.Kind == hub.KindScore && ap.Action == deploy.ActionUpdate:
			scoreUpdates++
		case ap.Kind == hub.KindScore && ap.Action == deploy.ActionCreate:
			scoreCreates++
		}
	}
	if setUpdates != 1 || scoreUpdates != 1 || scoreCreates != 1 {
		t.Fatalf("Applied 构成 = 集 update %d / 成绩 update %d / 成绩 create %d（期望 1/1/1）: %+v",
			setUpdates, scoreUpdates, scoreCreates, report.Applied)
	}
	if len(report.Repointed) != 1 {
		t.Fatalf("Repointed = %+v，期望 1 条（标准成绩现场同键 → 改判 update）", report.Repointed)
	}
	// D7 键级过滤计数：两个未知键各计 1（整条仍落地）。
	if got := deploySkip(report, "成绩 Statistics 含目标端不认识的 HitResult 键"); got != 2 {
		t.Errorf("Statistics 未知键过滤计数 = %d，期望 2（Skipped=%+v）", got, report.Skipped)
	}

	// ---- 现场读回：覆盖写入后 10 扩展列原值保留 + 白名单确实被覆盖 ----
	withRO(t, lib, refEz.RealmPath, func(r *realmc.Realm) {
		// 集行：Status 被 canonical 覆盖回 1，两个 Ez 扩展列保持预置值。
		setObj := findRowByString(t, r, "BeatmapSet", "Hash", stdSet.Hash)
		defer r.ReleaseObject(setObj)
		assertRealmInt64(t, r, setObj, "BeatmapSet", "Status", 1, "集行白名单覆盖")
		assertRealmStr(t, r, setObj, "BeatmapSet", "ExternalContentRoot", ezPresetExternalRoot, "集行 Ez 扩展列保留")
		assertRealmInt64(t, r, setObj, "BeatmapSet", "HostingKind", ezPresetHostingKind, "集行 Ez 扩展列保留")

		// 难度行：四个 Ez 扩展列保持预置值（就地更新不动难度行）。
		for _, bm := range stdSet.Beatmaps {
			bmObj := findRowByString(t, r, "Beatmap", "Hash", bm.Hash)
			assertRealmFloat64(t, r, bmObj, "Beatmap", "XxyStarRating", ezPresetXxySR, "难度 Ez 扩展列保留")
			assertRealmFloat64(t, r, bmObj, "Beatmap", "PerformancePoints", ezPresetPerfPoints, "难度 Ez 扩展列保留")
			assertRealmBool(t, r, bmObj, "Beatmap", "HasVideo", true, "难度 Ez 扩展列保留")
			assertRealmBool(t, r, bmObj, "Beatmap", "HasStoryboard", true, "难度 Ez 扩展列保留")
			r.ReleaseObject(bmObj)
		}

		// Ruleset 行：两个 Ez 扩展列保持预置值（难度解析复用现有行，不写）。
		rsObj := findRowByString(t, r, "Ruleset", "ShortName", "osu")
		defer r.ReleaseObject(rsObj)
		assertRealmInt64(t, r, rsObj, "Ruleset", "LastAppliedXxySrVersion", ezPresetXxySrVersion, "规则集 Ez 扩展列保留")
		assertRealmInt64(t, r, rsObj, "Ruleset", "LastAppliedManiaSkillVersion", ezPresetManiaVersion, "规则集 Ez 扩展列保留")

		// 标准成绩：TotalScore 被 canonical 覆盖，判定档两列保持预置值。
		stdObj := findRowByString(t, r, "Score", "Hash", stdScore.Hash)
		defer r.ReleaseObject(stdObj)
		assertRealmInt64(t, r, stdObj, "Score", "TotalScore", 9876543, "成绩白名单覆盖")
		assertRealmInt64(t, r, stdObj, "Score", "JudgeMode", 31, "成绩 Ez 扩展列保留")
		assertRealmInt64(t, r, stdObj, "Score", "HealthMode", 34, "成绩 Ez 扩展列保留")

		// 未知键成绩：过滤后写入的目标库值（重序列化确定性输出，键排序）。
		stObj := findRowByString(t, r, "Score", "Hash", statsScore.Hash)
		defer r.ReleaseObject(stObj)
		assertRealmStr(t, r, stObj, "Score", "Statistics", `{"great":900,"legacy_combo_increase":7}`, "StatisticsJson 过滤后写入")
		assertRealmStr(t, r, stObj, "Score", "MaximumStatistics", `{"great":1000}`, "MaximumStatisticsJson 过滤后写入")

		// 技能表/外部托管集/bms 规则集行留现场（丢弃 = 不跨端，不动游戏侧）。
		for _, tbl := range []string{
			"EzBeatmapSkillValue", "EzPlayerSkillValue", "EzDanEstimate",
			"EzPlayerSkillHistoryPoint", "EzBeatmapChartSkillInfo",
			"EzPlayerDanSkillsetValue", "EzBeatmapChartDan",
		} {
			if got := rowCount(t, r, tbl); got != 1 {
				t.Errorf("技能表 %s 行数 = %d，期望 1（投放不触碰技能表）", tbl, got)
			}
		}
		extObj, extErr := findRowSoft(r, "BeatmapSet", "Hash", extHash)
		if extErr != nil || extObj == nil {
			t.Errorf("外部托管集行被触碰（err=%v）——丢弃语义是「不入 hub」，不是删除游戏侧", extErr)
		} else {
			r.ReleaseObject(extObj)
		}
		bmsObj, bmsErr := findRowSoft(r, "Ruleset", "ShortName", "bms")
		if bmsErr != nil || bmsObj == nil {
			t.Errorf("bms 规则集行被触碰（err=%v）", bmsErr)
		} else {
			r.ReleaseObject(bmsObj)
		}
	})
}
