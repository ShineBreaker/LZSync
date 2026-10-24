package itests

// delta gimmick 谱面流出告警全链验收（design §5.4 R6 / §7-M3-4）：delta
// 源库（DeltaGimmickSeed——标准集 + 含两类 gimmick section 头的谱面集）
// 真实收集入 hub 与内容寻址仓 → BuildPlan 预演标记 → Deploy 执行报告
// 计数——预演与执行一致呈现，默认动作不变（gimmick 集照常同步），正常
// 谱面零误报。librealm 缺失时显式 SKIP（§8.1）。

import (
	"context"
	"testing"

	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

func TestDeltaGimmickWarningFullChain(t *testing.T) {
	mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	// 源：delta 座收 DeltaGimmickSeed（真实 collect：谱面集/收藏夹入 hub，
	// .osu 字节收仓+回链，HB4 四步序列）。
	seed, err := fixture.DeltaGimmickSeed()
	if err != nil {
		t.Fatalf("DeltaGimmickSeed: %v", err)
	}
	refDelta := buildLib(t, fixture.ForkDelta, fixture.VersionDelta, seed)
	e.collectInto(refDelta)

	// 目标：lazer 空座。
	refLazer := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{})

	// 预演：gimmick 集 Warning 标记，标准集与收藏夹零误报，动作不变。
	plan, err := deploy.BuildPlan(ctx, e.hub, refLazer, deploy.BuildOptions{Files: e.engine.Store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	gimmickSet, stdSet := seed.BeatmapSets[1], seed.BeatmapSets[0]
	for _, it := range plan.Items {
		switch {
		case it.Kind == hub.KindBeatmapSet && it.ContentKey == gimmickSet.Hash:
			if it.Warning == "" || it.Action != deploy.ActionCreate {
				t.Errorf("gimmick 集计划项 = %+v（期望 create + Warning——告警不改变默认动作）", it)
			}
		case it.Kind == hub.KindBeatmapSet && it.ContentKey == stdSet.Hash:
			if it.Warning != "" {
				t.Errorf("标准谱面集误报告警: %+v", it)
			}
		case it.Kind == hub.KindCollection:
			if it.Warning != "" {
				t.Errorf("收藏夹误报告警: %+v", it)
			}
		}
	}

	// 执行：报告计数与预演一致（恰 1 条 gimmick 集），动作照常提交。
	runner := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	appliedSets := 0
	for _, ap := range report.Applied {
		if ap.Kind == hub.KindBeatmapSet {
			appliedSets++
		}
	}
	if appliedSets != 2 {
		t.Errorf("applied 谱面集 = %d（期望 2——告警不阻断同步）: %+v", appliedSets, report.Applied)
	}
	if len(report.GimmickWarnings) != 1 ||
		report.GimmickWarnings[0].Kind != hub.KindBeatmapSet ||
		report.GimmickWarnings[0].ContentKey != gimmickSet.Hash {
		t.Errorf("GimmickWarnings = %+v（期望恰 1 条 gimmick 集）", report.GimmickWarnings)
	}
	// 仓内副本仍在（投放建链不改写仓内容，读侧哈希复核通过）。
	if ok, err := e.engine.Store.Has(gimmickSet.Beatmaps[0].Hash); err != nil || !ok {
		t.Errorf("仓内 gimmick .osu 副本 = (%v, %v)，期望在仓且校验通过", ok, err)
	}
}
