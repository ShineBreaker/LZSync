package itests

// 版本链断言（design §7-M1-6 HB1 / §4.4 版本链）：同 OnlineID 的 v1 收集
// 进 hub，再造 v2 收集 → v1.superseded_by 指向 v2（链尾唯一）；对第三
// fork 的投放差集只含 v2（历史版本永不进入差集）；被版本链取代的消失键
// 不生成 tombstone（HB2 第二道闸）。
//
// 「同 fork 谱面更新后再次收集」用同 ForkID 的第二座库模拟（fixture 无法
// 原地改行——收集语义按 LibraryRef 执行，presence 按 fork 整段重写，与
// 真实「谱面更新后的下一次收集」等价）。

import (
	"context"
	"testing"

	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

func TestVersionChainAndThirdForkDiff(t *testing.T) {
	mustLib(t)
	e := newEnv(t)

	// v1/v2：同 OnlineID=2002、难度 .osu 内容不同 → Hash 全链不同
	//（fixture.VersionChangeSeeds 即此夹具）。
	seedV1, seedV2, err := fixture.VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	v1Hash := seedV1.BeatmapSets[0].Hash
	v2Hash := seedV2.BeatmapSets[0].Hash

	// ① 收集 v1（lazer 座）。
	refV1 := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, seedV1)
	e.collectInto(refV1)
	if got := e.supersededOf(v1Hash); got != "" {
		t.Fatalf("首收 v1 后 superseded_by = %q，期望空（尚无新版本）", got)
	}

	// ② 再收集 v2（同 fork 新座：谱面更新后的下一次收集）。
	refV2 := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, seedV2)
	e.collectInto(refV2)

	// v1.superseded_by = v2；v2 是链尾（HB1：链尾即当前版本）。
	if got := e.supersededOf(v1Hash); got != v2Hash {
		t.Errorf("v1.superseded_by = %q，期望 %q", got, v2Hash)
	}
	if got := e.supersededOf(v2Hash); got != "" {
		t.Errorf("v2.superseded_by = %q，期望空（链尾）", got)
	}
	current, err := e.hub.CurrentBeatmapSets(context.Background())
	if err != nil {
		t.Fatalf("CurrentBeatmapSets: %v", err)
	}
	if len(current) != 1 || current[0].Set.Hash != v2Hash {
		t.Errorf("当前版本集合 = %d 项（%v），期望恰 [v2]", len(current), keysOf(current))
	}

	// 被版本链取代的消失不生成 tombstone（HB2：v1 从 lazer presence 消失，
	// 但其链尾 v2 仍在本次快照 → 不算删除）。
	if got := e.tombstoneCount(fixture.ForkLazer, hub.KindBeatmapSet, v1Hash); got != 0 {
		t.Errorf("被版本链取代的 v1 生成 tombstone = %d，期望 0", got)
	}

	// ③ 第三 fork（g0v0 空谱面库）收集后，投放差集只含 v2。
	refC := buildLib(t, fixture.ForkG0v0, fixture.VersionG0v0, fixture.Seed{})
	e.collectInto(refC)
	items := e.reconcile(fixture.ForkG0v0, hub.KindBeatmapSet)
	if len(items) != 1 {
		t.Fatalf("对第三 fork 的差集 = %d 项（%v），期望仅 v2", len(items), items)
	}
	it := items[0]
	if it.ContentKey != v2Hash || it.Category != hub.ReconcileCreate {
		t.Errorf("差集项 = {key=%s cat=%s}，期望 {v2, create}", it.ContentKey, it.Category)
	}
	// 历史版本 v1 不以任何分类出现（含恢复组——它不是删除）。
	for _, x := range items {
		if x.ContentKey == v1Hash {
			t.Errorf("历史版本 v1 进入差集： %+v", x)
		}
	}
}

func keysOf(entries []hub.BeatmapSetEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Set.Hash)
	}
	return out
}
