package itests

// M2 全链集成验收（design.md §7-M2 / §8.2-5 全链往返）：两个构造库
// （源含皮肤/成绩/新旧版本谱面，目标含旧版本与 B 独有集）→ 真实收集 →
// hub 对账（版本更新强键 + 源独有 create）→ 计划（版本替换 + 新建 + 手工
// 成绩项——BuildPlan 的 score 面尚未放开）→ 投放 → ① 目标现场轻量核对
// （旧集软删、新集在、成绩挂接非悬空）② 结构档案写前后零漂移 ③ 重新收集
// 目标（重新投影）后 hub canonical 与投放前逐键逐字段相等（payload 字节级
// ——DTO Marshal 字段序确定，相等 ⇔ 逐字段相等）④ 版本更新取代的旧版本
// 消失不产生 tombstone（§4.4 排除规则）。链路全部走真实编排层。

import (
	"context"
	"sort"
	"testing"

	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// canonSnap hub canonical 的 payload 快照（beatmap_set 链尾 / skin / score
// 三面，内容键 → payload 原文）——投放前后各取一份做逐字段相等断言。
type canonSnap struct {
	sets   map[string]string
	skins  map[string]string
	scores map[string]string
}

func (e *env) canonSnapshot(t *testing.T) canonSnap {
	t.Helper()
	snap := canonSnap{
		sets:   map[string]string{},
		skins:  map[string]string{},
		scores: map[string]string{},
	}
	rows, err := e.hub.DB().Query(
		`SELECT content_hash, payload FROM beatmap_set WHERE superseded_by IS NULL`)
	if err != nil {
		t.Fatalf("查链尾谱面集 canonical: %v", err)
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			t.Fatalf("读链尾行: %v", err)
		}
		snap.sets[k] = v
	}
	rows.Close()
	rows, err = e.hub.DB().Query(`SELECT content_hash, payload FROM skin`)
	if err != nil {
		t.Fatalf("查皮肤 canonical: %v", err)
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			t.Fatalf("读皮肤行: %v", err)
		}
		snap.skins[k] = v
	}
	rows.Close()
	rows, err = e.hub.DB().Query(`SELECT beatmap_hash, content_hash, payload FROM score`)
	if err != nil {
		t.Fatalf("查成绩 canonical: %v", err)
	}
	for rows.Next() {
		var bh, h, v string
		if err := rows.Scan(&bh, &h, &v); err != nil {
			rows.Close()
			t.Fatalf("读成绩行: %v", err)
		}
		snap.scores[hub.ScoreContentKey(bh, h)] = v
	}
	rows.Close()
	return snap
}

// assertEqual 逐键比对两份快照；差异报出具体键与 payload 前缀（逐字段
// 相等的失败定位面）。
func (s canonSnap) assertEqual(t *testing.T, other canonSnap, stage string) {
	t.Helper()
	cmp := func(name string, a, b map[string]string) {
		keys := make([]string, 0, len(a)+len(b))
		for k := range a {
			keys = append(keys, k)
		}
		for k := range b {
			if _, ok := a[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			av, aok := a[k]
			bv, bok := b[k]
			switch {
			case !aok:
				t.Errorf("%s：%s 快照缺键 %s", stage, name, k)
			case !bok:
				t.Errorf("%s：%s 重新投影后缺键 %s", stage, name, k)
			case av != bv:
				t.Errorf("%s：%s 键 %s 逐字段不等\n投放前 %s\n重新投影 %s",
					stage, name, k, av, bv)
			}
		}
	}
	cmp("beatmap_set", s.sets, other.sets)
	cmp("skin", s.skins, other.skins)
	cmp("score", s.scores, other.scores)
}

// TestFullChainAllKindsBothLibraries 收集（两库）→ 对账 → 投放 → 重新投影
// 逐字段相等 + 结构档案零漂移（§7-M2 完成标准 6 的全链面）。
func TestFullChainAllKindsBothLibraries(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	// ---- 夹具 ----
	// 源座 A（lazer）：两个普通集 + v2 新版本（强键 OnlineID 2002）+ 用户
	// 皮肤 + 两条成绩（分别挂 v2 难度与普通集难度）。
	filesA := map[string][]byte{}
	plain1, err := fixture.MaterializeSet(8001, "Chain Artist", "Chain Plain 1", "mapperC",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("fullchain plain1 osu bytes")}},
		[]byte("fullchain plain1 audio"), filesA)
	if err != nil {
		t.Fatalf("MaterializeSet(plain1): %v", err)
	}
	plain2, err := fixture.MaterializeSet(8002, "Chain Artist", "Chain Plain 2", "mapperC",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("fullchain plain2 osu bytes")}}, nil, filesA)
	if err != nil {
		t.Fatalf("MaterializeSet(plain2): %v", err)
	}
	_, seedV2, err := fixture.VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	v2 := seedV2.BeatmapSets[0]
	for h, b := range seedV2.Files {
		filesA[h] = b
	}
	skinSeed, err := fixture.MaterializeSkin("链路用户皮肤", "skinny", []fixture.SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:链路用户皮肤\n")},
		{Filename: "bg.png", Bytes: []byte("skin bg png bytes")},
	}, filesA)
	if err != nil {
		t.Fatalf("MaterializeSkin: %v", err)
	}
	scoreV2, err := fixture.MaterializeScore(v2.Beatmaps[0], "lazer 2026.1001.0",
		[]byte("fullchain score on v2 replay"), filesA)
	if err != nil {
		t.Fatalf("MaterializeScore(v2): %v", err)
	}
	scorePlain, err := fixture.MaterializeScore(plain1.Beatmaps[0], "lazer 2026.1001.0",
		[]byte("fullchain score on plain replay"), filesA)
	if err != nil {
		t.Fatalf("MaterializeScore(plain): %v", err)
	}
	refA := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{plain1, plain2, v2},
		Skins:       []fixture.SeedSkin{skinSeed},
		Scores:      []fixture.SeedScore{scoreV2, scorePlain},
		Files:       filesA,
	})

	// 目标座 B（delta）：v1 旧版本（与 v2 同 OnlineID 强键）+ B 独有集。
	seedV1, _, err := fixture.VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	v1 := seedV1.BeatmapSets[0]
	filesB := map[string][]byte{}
	for h, b := range seedV1.Files {
		filesB[h] = b
	}
	bOnly, err := fixture.MaterializeSet(8101, "Chain Artist", "B Exclusive", "mapperD",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("fullchain b-only osu bytes")}}, nil, filesB)
	if err != nil {
		t.Fatalf("MaterializeSet(bOnly): %v", err)
	}
	refB := buildLib(t, fixture.ForkDelta, fixture.VersionDelta, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{v1, bOnly},
		Files:       filesB,
	})

	// ---- 收集：B 先（v1 presence 建立版本链基准），A 后（v2 强键配对）----
	e.collectInto(refB)
	repA := e.collectInto(refA)
	for _, kind := range []hub.EntityKind{hub.KindBeatmapSet, hub.KindSkin, hub.KindScore} {
		if got := repA.Entities[kind]; got == 0 {
			t.Fatalf("A 收集 %s 实体数 = 0（夹具构造异常）", kind)
		}
	}
	if got := e.supersededOf(v1.Hash); got != v2.Hash {
		t.Fatalf("版本链 v1.superseded_by = %q，期望 %q（HB1）", got, v2.Hash)
	}

	// ---- 对账：恰 3 项谱面集（v2 强键 version-update + 2 普通集 create）----
	items := e.reconcile(fixture.ForkDelta, hub.KindBeatmapSet)
	if len(items) != 3 {
		t.Fatalf("对账 = %d 项（%+v），期望 3（version-update + 2 create）", len(items), items)
	}
	gotV2, gotCreate := false, 0
	for _, ri := range items {
		switch {
		case ri.Category == hub.ReconcileVersionUpdate:
			if ri.ContentKey != v1.Hash || ri.CurrentKey != v2.Hash || ri.Evidence != hub.EvidenceStrong {
				t.Errorf("version-update 项 = %+v，期望 strong %s→%s", ri, v1.Hash, v2.Hash)
			}
			gotV2 = true
		case ri.Category == hub.ReconcileCreate:
			gotCreate++
		default:
			t.Errorf("对账出现意外分类 %+v", ri)
		}
	}
	if !gotV2 || gotCreate != 2 {
		t.Fatalf("对账构成：version-update=%v create=%d（期望 true/2）", gotV2, gotCreate)
	}

	// ---- 计划：BuildPlan（版本替换 + 谱面集/皮肤 create）+ 手工补成绩项 ----
	plan, err := deploy.BuildPlan(ctx, e.hub, refB, deploy.BuildOptions{Files: e.engine.Store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var setCreates, skinCreates, versionReplaces int
	for _, it := range plan.Items {
		switch {
		case it.Kind == hub.KindBeatmapSet && it.Action == deploy.ActionVersionReplace:
			versionReplaces++
		case it.Kind == hub.KindBeatmapSet && it.Action == deploy.ActionCreate:
			setCreates++
		case it.Kind == hub.KindSkin && it.Action == deploy.ActionCreate:
			skinCreates++
		default:
			t.Errorf("计划项意外：%+v", it)
		}
	}
	if versionReplaces != 1 || setCreates != 2 || skinCreates != 1 {
		t.Fatalf("计划构成 = version-replace %d / set create %d / skin create %d（期望 1/2/1）: %+v",
			versionReplaces, setCreates, skinCreates, plan.Items)
	}
	// BuildPlan 的 score 面未放开（后续块），成绩项以 hub canonical 键手工
	// 补入（scorePlan 先例，deploy_score_test.go）。
	for _, sc := range []fixture.SeedScore{scoreV2, scorePlain} {
		plan.Items = append(plan.Items, deploy.PlanItem{
			Kind:       hub.KindScore,
			ContentKey: hub.ScoreContentKey(sc.BeatmapHash, sc.Hash),
			Action:     deploy.ActionCreate,
			Summary:    "成绩 " + sc.Hash[:12] + "…",
		})
	}

	// ---- 投放（A 收集时已收仓，CopyMissing 的源齐备）----
	snapBefore := e.canonSnapshot(t)
	schemaBefore := schemaSnapshotOf(t, lib, refB.RealmPath)
	runner := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	var appliedVR, appliedSet, appliedSkin, appliedScore int
	for _, ap := range report.Applied {
		switch {
		case ap.Kind == hub.KindBeatmapSet && ap.Action == deploy.ActionVersionReplace:
			appliedVR++
		case ap.Kind == hub.KindBeatmapSet:
			appliedSet++
		case ap.Kind == hub.KindSkin:
			appliedSkin++
		case ap.Kind == hub.KindScore:
			appliedScore++
		}
	}
	if appliedVR != 1 || appliedSet != 2 || appliedSkin != 1 || appliedScore != 2 {
		t.Fatalf("Applied 构成 = vr %d / set %d / skin %d / score %d（期望 1/2/1/2）: %+v",
			appliedVR, appliedSet, appliedSkin, appliedScore, report.Applied)
	}
	if len(report.FailedBatches) != 0 || len(report.NotRun) != 0 {
		t.Fatalf("失败/未执行段非空: %+v / %+v", report.FailedBatches, report.NotRun)
	}

	// ---- 目标现场轻量核对：旧集软删、新集在、成绩挂接非悬空（S7）----
	withRO(t, lib, refB.RealmPath, func(r *realmc.Realm) {
		if !setDeletePending(t, r, v1.Hash) {
			t.Error("全链投放后旧版本集未软删（§5.4 三步之②）")
		}
		if setDeletePending(t, r, v2.Hash) {
			t.Error("新版本集被误软删")
		}
		for _, sc := range []fixture.SeedScore{scoreV2, scorePlain} {
			scoreCK, err := r.FindClass("Score")
			if err != nil {
				t.Fatalf("FindClass(Score): %v", err)
			}
			hashKey, err := r.FindProperty(scoreCK, "Hash")
			if err != nil {
				t.Fatalf("FindProperty(Score.Hash): %v", err)
			}
			obj, err := r.FindFirstByString(scoreCK, hashKey, sc.Hash)
			if err != nil || obj == nil {
				t.Fatalf("读回成绩 %s: %v", sc.Hash, err)
			}
			linkKey, err := r.FindProperty(scoreCK, "BeatmapInfo")
			if err != nil {
				t.Fatalf("FindProperty(BeatmapInfo): %v", err)
			}
			null, err := r.IsNull(obj, linkKey)
			if err != nil {
				t.Fatalf("判空 BeatmapInfo: %v", err)
			}
			if null {
				t.Errorf("成绩 %s… 的 BeatmapInfo 关系为 null（S7 悬空，应挂接目标难度）", sc.Hash[:12])
			}
		}
	})

	// ---- 结构档案写前后零漂移（DriftGuard 之外的独立第二校验，§8.2-7）----
	if !schemaBefore.Equal(schemaSnapshotOf(t, lib, refB.RealmPath)) {
		t.Error("投放前后目标结构档案漂移")
	}

	// ---- 重新投影：再收集 B → canonical 逐键逐字段相等 ----
	e.collectInto(refB)
	e.canonSnapshot(t).assertEqual(t, snapBefore, "全链往返")

	// 版本更新取代的旧版本消失不产生 tombstone（§4.4 排除规则的全链验证）。
	if got := e.tombstoneCount(fixture.ForkDelta, hub.KindBeatmapSet, v1.Hash); got != 0 {
		t.Errorf("被版本链取代的 v1 产生 tombstone %d 行（期望 0，§4.4 排除规则）", got)
	}
}
