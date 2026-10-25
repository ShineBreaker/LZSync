package itests

// 四分类对比断言（design §7-M1-6 / §4.2）：构造源独有 / 目标独有 / 冲突 /
// 版本更新夹具，断言 hub 对账分类正确——hub→fork 投放预演方向（M1 的产品
// 面 = hub.ReconcileAgainst，appsvc 真实后端同源）产出 create / conflict /
// version-update（元数据弱键入「需用户确认」组：Evidence=weak）；目标独有
// 属 fork↔fork 对比（两份 presence 差集），diff.CompareForks 契约桩尚未
// 施工（上报 notes），此处以 hub 内 SQL 差集断言其数据面完整可查。
//
// 夹具要点：fixture 镜像不承载 Beatmaps 列表列 → 投影的 Metadata 身份字段
// 恒空 → 全部 OnlineID=0 的集会并进同一弱键 "meta:|"。为避免跨素材误配对，
// 非弱键素材各给唯一正 OnlineID（强键互异）；弱键素材（W1/W2）保持 0。

import (
	"testing"
	"time"

	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

func TestReconcileFourCategories(t *testing.T) {
	mustLib(t)
	e := newEnv(t)

	filesA := map[string][]byte{}
	cxOSU := "conflict-shared-osu-bytes" // 两端同字节 → 同内容键
	SA := mustSet(t, 101, "SA Artist", "SA Title", "sa osu bytes", filesA)
	CXA := mustSet(t, 102, "CX Artist", "Conflict From A", cxOSU, filesA)
	V1 := mustSet(t, 77, "V Artist", "V Title", "version one osu", filesA)
	W1 := mustSet(t, 0, "W Artist", "W Title", "weak one osu", filesA) // 弱键素材
	W2 := mustSet(t, 0, "W Artist", "W Title", "weak two osu", filesA) // 同元数据、异内容 → 弱键配对
	// 冲突字段用 DateSubmitted（G10/B7 后 StatusInt 是端本地字段不参与
	// 冲突；投影 Title 在 fixture 形态下恒空不可构造 → 用就地更新写入面
	// 的非本地字段差异构造同键冲突）。
	CXA.StatusInt = 4 // 端本地字段差异：按 G10 不参与冲突判定

	filesG := map[string][]byte{}
	V2 := mustSet(t, 77, "V Artist", "V Title", "version two osu", filesG) // 同强键新版本

	filesC := map[string][]byte{}
	TO := mustSet(t, 103, "TO Artist", "TO Title", "target only osu", filesC)
	CXC := mustSet(t, 102, "CX Artist", "Conflict From C", cxOSU, filesC) // 同键
	cxSubmitted := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	CXC.DateSubmitted = &cxSubmitted // 同键异 DateSubmitted → 冲突（非本地字段）
	V1C := mustSet(t, 77, "V Artist", "V Title", "version one osu", filesC)
	W1C := mustSet(t, 0, "W Artist", "W Title", "weak one osu", filesC)

	// 收集顺序（§0-B2 首见即定：先收集者成为 canonical）：源 A 先收
	//（canonical=A 值）→ 目标 C 后收（其 StatusInt 只留在自己的
	// presence → 与 canonical 成冲突）→ G（delta，v2 建链）。
	refA := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{SA, CXA, V1, W1, W2}, Files: filesA,
	})
	e.collectInto(refA)
	refC := buildLib(t, fixture.ForkG0v0, fixture.VersionG0v0, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{TO, CXC, V1C, W1C}, Files: filesC,
	})
	e.collectInto(refC)
	refG := buildLib(t, fixture.ForkDelta, fixture.VersionDelta, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{V2}, Files: filesG,
	})
	e.collectInto(refG)

	// ---- hub→g0v0 投放预演差异（§4.4 对账语义）----
	items := e.reconcile(fixture.ForkG0v0, hub.KindBeatmapSet)
	byKey := map[string][]hub.ReconcileItem{}
	for _, it := range items {
		byKey[it.ContentKey] = append(byKey[it.ContentKey], it)
	}

	// 源独有：SA → create。
	got := byKey[SA.Hash]
	if len(got) != 1 || got[0].Category != hub.ReconcileCreate || got[0].Restorable {
		t.Errorf("源独有 %s = %+v，期望单条 create 非 Restorable", shortHash(SA.Hash), got)
	}

	// 冲突：CX 内容键相等、DateSubmitted 不同 → conflict 且字段级 diff 含
	// DateSubmitted（§4.2：冲突字段只取内容键之外的字段；G10 后端本地
	// 字段 StatusInt 不入冲突面）。
	got = byKey[CXA.Hash]
	if len(got) != 1 || got[0].Category != hub.ReconcileConflict {
		t.Fatalf("冲突 %s = %+v，期望单条 conflict", shortHash(CXA.Hash), got)
	}
	var sawDateSubmitted bool
	for _, cf := range got[0].ConflictFields {
		if cf.Field == "DateSubmitted" {
			sawDateSubmitted = true
		}
		if cf.Field == "StatusInt" {
			t.Errorf("端本地字段 StatusInt 不应进冲突字段: %+v", cf)
		}
	}
	if !sawDateSubmitted {
		t.Errorf("冲突字段 = %+v，期望含 DateSubmitted", got[0].ConflictFields)
	}

	// 版本更新（强键）：g0v0 持有旧版本 v1，hub 链尾 v2 → version-update
	// (strong)；v2 不再单独产出 create（已配对消费——目标将以整集替换拿到 v2）。
	got = byKey[V1.Hash]
	if len(got) != 1 || got[0].Category != hub.ReconcileVersionUpdate {
		t.Fatalf("强键版本更新 %s = %+v，期望单条 version-update", shortHash(V1.Hash), got)
	}
	if got[0].CurrentKey != V2.Hash || got[0].Evidence != hub.EvidenceStrong {
		t.Errorf("强键版本更新项 = {cur=%s ev=%s}，期望 {v2, strong}",
			shortHash(got[0].CurrentKey), got[0].Evidence)
	}
	if _, ok := byKey[V2.Hash]; ok {
		t.Errorf("已配对消费的 v2 不应再单独产出动作: %+v", byKey[V2.Hash])
	}

	// 版本更新（弱键「需确认」组）：g0v0 持有 W1，hub 另有同元数据链尾 W2 →
	// version-update(weak)——默认不勾选、需用户拍板（§4.2/§6.2）。
	got = byKey[W1.Hash]
	if len(got) != 1 || got[0].Category != hub.ReconcileVersionUpdate {
		t.Fatalf("弱键版本更新 %s = %+v，期望单条 version-update", shortHash(W1.Hash), got)
	}
	if got[0].CurrentKey != W2.Hash || got[0].Evidence != hub.EvidenceWeak {
		t.Errorf("弱键版本更新项 = {cur=%s ev=%s}，期望 {W2, weak（需用户确认组）}",
			shortHash(got[0].CurrentKey), got[0].Evidence)
	}

	// 消费侧一致性：W2 被弱键配对消费，不再单独产出 create；TO 为目标侧
	// 独有内容（canonical 来自 g0v0 收集，一致）→ hub→fork 方向不产出
	// target-only（hub 不删实体，§4.4——目标独有只在 fork↔fork 面）。
	if _, ok := byKey[W2.Hash]; ok {
		t.Errorf("W2 应被弱键配对消费，不应单独产出: %+v", byKey[W2.Hash])
	}
	if _, ok := byKey[TO.Hash]; ok {
		t.Errorf("目标独有 TO 在 hub→fork 方向不应产出动作: %+v", byKey[TO.Hash])
	}
	if len(items) != 4 { // SA create、CX conflict、v1 vu-strong、W1 vu-weak
		t.Errorf("差集项数 = %d（%v），期望 4", len(items), items)
	}

	// ---- fork↔fork 目标独有（两份 presence 差集的 SQL 面，§4.4）----
	// g0v0 有而 lazer 无：恰 {TO}。
	if got := e.presenceDiff(fixture.ForkLazer, fixture.ForkG0v0, hub.KindBeatmapSet); len(got) != 1 || got[0] != TO.Hash {
		t.Errorf("目标独有（g0v0−lazer）= %v，期望 [%s]", got, shortHash(TO.Hash))
	}
	// 对偶面（源独有快照差集）：lazer 有而 g0v0 无 ⊇ {SA, W2}。
	gotDiff := e.presenceDiff(fixture.ForkG0v0, fixture.ForkLazer, hub.KindBeatmapSet)
	for _, want := range []string{SA.Hash, W2.Hash} {
		found := false
		for _, k := range gotDiff {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("源独有快照差集（lazer−g0v0）缺 %s: %v", shortHash(want), gotDiff)
		}
	}
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8] + "…"
	}
	return h
}
