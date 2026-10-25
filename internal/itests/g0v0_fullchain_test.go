package itests

// g0v0 夹具全链验收（design.md §7-M3-2 / §5.4 D8+S8）：
// ① 市场规则集过滤：g0v0 座（G0v0DiffSeed：OnlineID≥10 市场规则集行 +
// 指向它的成绩）真实收集 → 指向市场行的成绩整条丢弃 + 计数告知，hub 零
// 污染；市场行与成绩行留现场（丢弃 = 不入 hub，不是删除游戏侧）。
// ② OnlineID 命名空间双向（S8，origin_fork → ServerRealm 判定）：
// g0v0→官方：异域新行 OnlineID 全链置 -1（集行/难度行/Author.OnlineID/
// 成绩 OnlineID/User.OnlineID）；官方→g0v0：现场同键行就地更新时
// OnlineID/LegacyOnlineID/User.OnlineID 列不触碰（现场值保留，白名单
// 其余列被 canonical 覆盖证明覆盖写入确实发生）。librealm 缺失时显式
// SKIP（§8.1）。

import (
	"context"
	"testing"

	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

func TestG0v0CompatFullChain(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)
	runner := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	})

	// ---- ① g0v0 座 A 收集：市场规则集行过滤（D8）----
	seedA, err := fixture.G0v0DiffSeed()
	if err != nil {
		t.Fatalf("G0v0DiffSeed: %v", err)
	}
	seedA.Scores[1].OnlineID = 99001 // g0v0 域成绩 OnlineID（异域新行 -1 断言的非平凡基准）
	marketScore, stdSetA, stdScoreA := seedA.Scores[0], seedA.BeatmapSets[0], seedA.Scores[1]
	refA := buildLib(t, fixture.ForkG0v0, fixture.VersionG0v0, seedA)

	repA := e.collectInto(refA)
	if got := collectSkip(repA, "规则集市场行成绩"); got != 1 {
		t.Errorf("市场规则集成绩跳过 = %d，期望 1（Skips=%+v）", got, repA.Skips)
	}
	assertHubCount(t, e, `SELECT COUNT(*) FROM score`, 1, "score（仅标准成绩，市场行被 D8 丢弃）")
	assertHubCount(t, e, `SELECT COUNT(*) FROM score WHERE content_hash=?`, 0,
		"市场成绩行（D8 收集侧应拦）", marketScore.Hash)
	// 市场行与成绩留现场：丢弃语义 = 不入 hub，收集是只读投影，绝不删游戏侧。
	withRO(t, lib, refA.RealmPath, func(r *realmc.Realm) {
		rsObj := findRowByString(t, r, "Ruleset", "ShortName", "g0v0-custom")
		defer r.ReleaseObject(rsObj)
		assertRealmInt64(t, r, rsObj, "Ruleset", "OnlineID", 10, "市场规则集行（OnlineID≥10）留现场")
		mktObj := findRowByString(t, r, "Score", "Hash", marketScore.Hash)
		defer r.ReleaseObject(mktObj)
		assertRealmInt64(t, r, mktObj, "Score", "OnlineID", 99001, "市场成绩行留现场（收集不删）")
	})

	// ---- ②a g0v0 → 官方（lazer 空座 B）：异域新行 OnlineID 置 -1 ----
	refB := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{})
	planB, err := deploy.BuildPlan(ctx, e.hub, refB, deploy.BuildOptions{Files: e.engine.Store})
	if err != nil {
		t.Fatalf("BuildPlan(B): %v", err)
	}
	var setCreates, colCreates int
	for _, it := range planB.Items {
		switch {
		case it.Kind == hub.KindBeatmapSet && it.Action == deploy.ActionCreate:
			setCreates++
		case it.Kind == hub.KindCollection && it.Action == deploy.ActionCreate:
			colCreates++
		default:
			t.Fatalf("计划 B 意外项 %+v", it)
		}
	}
	if setCreates != 1 || colCreates != 1 {
		t.Fatalf("计划 B = 集 create %d / 收藏 create %d（期望 1/1）: %+v", setCreates, colCreates, planB.Items)
	}
	planB.Items = append(planB.Items, deploy.PlanItem{
		Kind: hub.KindScore, ContentKey: hub.ScoreContentKey(stdScoreA.BeatmapHash, stdScoreA.Hash),
		Action: deploy.ActionCreate, Summary: "成绩 " + stdScoreA.Hash[:12] + "…",
	})
	reportB, err := runner.Deploy(ctx, planB, nil)
	if err != nil {
		t.Fatalf("Deploy(B): %v（report=%+v）", err, reportB)
	}
	if len(reportB.FailedBatches) != 0 || len(reportB.NotRun) != 0 {
		t.Fatalf("B 投放失败/未执行段非空: %+v / %+v", reportB.FailedBatches, reportB.NotRun)
	}
	withRO(t, lib, refB.RealmPath, func(r *realmc.Realm) {
		setObj := findRowByString(t, r, "BeatmapSet", "Hash", stdSetA.Hash)
		defer r.ReleaseObject(setObj)
		assertRealmInt64(t, r, setObj, "BeatmapSet", "OnlineID", -1,
			"异域新行集 OnlineID 置 -1（canonical=1001，g0v0.top→ppy）")
		for _, bm := range stdSetA.Beatmaps {
			bmObj := findRowByString(t, r, "Beatmap", "Hash", bm.Hash)
			assertRealmInt64(t, r, bmObj, "Beatmap", "OnlineID", -1, "异域新行难度 OnlineID 置 -1")
			r.ReleaseObject(bmObj)
		}
		mdObj := findRowByString(t, r, "BeatmapMetadata", "Title", stdSetA.Title)
		defer r.ReleaseObject(mdObj)
		authorKey := propKey(t, r, "BeatmapMetadata", "Author")
		author, err := r.GetLinkedObject(mdObj, authorKey)
		if err != nil || author == nil {
			t.Fatalf("读 Author embedded: %v", err)
		}
		defer r.ReleaseObject(author)
		assertRealmInt64(t, r, author, "RealmUser", "OnlineID", -1, "异域新行 Author.OnlineID 置 -1")

		scObj := findRowByString(t, r, "Score", "Hash", stdScoreA.Hash)
		defer r.ReleaseObject(scObj)
		assertRealmInt64(t, r, scObj, "Score", "OnlineID", -1,
			"异域新行成绩 OnlineID 置 -1（canonical=99001）")
		userKey := propKey(t, r, "Score", "User")
		user, err := r.GetLinkedObject(scObj, userKey)
		if err != nil || user == nil {
			t.Fatalf("读成绩 User embedded: %v", err)
		}
		defer r.ReleaseObject(user)
		assertRealmInt64(t, r, user, "RealmUser", "OnlineID", -1, "异域新行 User.OnlineID 置 -1")
	})

	// ---- ②b 官方 → g0v0（座 C 源 / 座 D 目标）：异域更新不触碰 OnlineID ----
	// C 收集刷新 canonical 为 lazer 域值（最近一次收集为准，§4.4）。
	seedC, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed(C): %v", err)
	}
	seedC.Scores[0].OnlineID = 12345 // ppy 域值（异域更新断言的 canonical 侧）
	refC := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, seedC)
	e.collectInto(refC)

	// D 座：同键现场行带 g0v0 域独有 OnlineID（播种直设——副本赋值不回写）。
	seedD, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed(D): %v", err)
	}
	seedD.BeatmapSets[0].OnlineID = 777001
	seedD.BeatmapSets[0].StatusInt = 0 // 现场差异：matchesCanonical 不一致 → 覆盖写入确实发生
	seedD.Scores[0].OnlineID = 888001
	seedD.Scores[0].LegacyOnlineID = 888002
	seedD.Scores[0].User.OnlineID = 888003
	seedD.Scores[0].TotalScore = 1 // 现场差异：同上
	seedD.Collections = nil        // 收敛夹具面：本段只断言集/成绩的 S8 列
	refD := buildLib(t, fixture.ForkG0v0, fixture.VersionG0v0, seedD)
	dScore := seedD.Scores[0]

	// 手工计划驱动（不走 BuildPlan：A 座收集已建立 g0v0 的 presence，同键
	// 命中使 BuildPlan 判无差异——presence 是 fork 级而非库级）。本段断言
	// 的是执行期写面（S8 列不触碰），create 项在执行期被现场同键行改判
	// update（§3.3 现场复核，fullchain_test 手工补项先例）。
	planD := deploy.Plan{
		TargetFork: fixture.ForkG0v0, Target: refD,
		Items: []deploy.PlanItem{
			{
				Kind: hub.KindBeatmapSet, ContentKey: seedD.BeatmapSets[0].Hash,
				Action: deploy.ActionCreate, Summary: "谱面集 " + seedD.BeatmapSets[0].Hash[:12] + "…",
			},
			{
				Kind: hub.KindScore, ContentKey: hub.ScoreContentKey(dScore.BeatmapHash, dScore.Hash),
				Action: deploy.ActionCreate, Summary: "成绩 " + dScore.Hash[:12] + "…",
			},
		},
	}
	reportD, err := runner.Deploy(ctx, planD, nil)
	if err != nil {
		t.Fatalf("Deploy(D): %v（report=%+v）", err, reportD)
	}
	if len(reportD.FailedBatches) != 0 || len(reportD.NotRun) != 0 {
		t.Fatalf("D 投放失败/未执行段非空: %+v / %+v", reportD.FailedBatches, reportD.NotRun)
	}
	// 覆盖写入经执行期改判（create → update）发生：恰集与成绩两条。
	if len(reportD.Repointed) != 2 {
		t.Fatalf("Repointed = %+v，期望 2 条（集/成绩现场同键 → 改判 update）", reportD.Repointed)
	}
	var dSetUpdates, dScoreUpdates int
	for _, ap := range reportD.Applied {
		switch {
		case ap.Kind == hub.KindBeatmapSet && ap.Action == deploy.ActionUpdate:
			dSetUpdates++
		case ap.Kind == hub.KindScore && ap.Action == deploy.ActionUpdate:
			dScoreUpdates++
		default:
			t.Errorf("D 投放意外 applied %+v", ap)
		}
	}
	if dSetUpdates != 1 || dScoreUpdates != 1 {
		t.Fatalf("D Applied 构成 = 集 update %d / 成绩 update %d（期望 1/1）: %+v",
			dSetUpdates, dScoreUpdates, reportD.Applied)
	}

	withRO(t, lib, refD.RealmPath, func(r *realmc.Realm) {
		// 集行：Status 被 canonical 覆盖回 1（覆盖证据），OnlineID 保持现场值。
		setObj := findRowByString(t, r, "BeatmapSet", "Hash", seedD.BeatmapSets[0].Hash)
		defer r.ReleaseObject(setObj)
		assertRealmInt64(t, r, setObj, "BeatmapSet", "Status", 1, "集行白名单覆盖")
		assertRealmInt64(t, r, setObj, "BeatmapSet", "OnlineID", 777001,
			"异域更新不触碰集 OnlineID（canonical=1001，现场值保留）")

		// 成绩：TotalScore 被 canonical 覆盖（覆盖证据），S8 三列保持现场值。
		scObj := findRowByString(t, r, "Score", "Hash", dScore.Hash)
		defer r.ReleaseObject(scObj)
		assertRealmInt64(t, r, scObj, "Score", "TotalScore", 9876543, "成绩白名单覆盖")
		assertRealmInt64(t, r, scObj, "Score", "OnlineID", 888001, "异域更新不触碰 OnlineID")
		assertRealmInt64(t, r, scObj, "Score", "LegacyOnlineID", 888002, "异域更新不触碰 LegacyOnlineID")
		userKey := propKey(t, r, "Score", "User")
		user, err := r.GetLinkedObject(scObj, userKey)
		if err != nil || user == nil {
			t.Fatalf("读成绩 User embedded: %v", err)
		}
		defer r.ReleaseObject(user)
		assertRealmInt64(t, r, user, "RealmUser", "OnlineID", 888003, "异域更新不触碰 User.OnlineID")
	})
}
