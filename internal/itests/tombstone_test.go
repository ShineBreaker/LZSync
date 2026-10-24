package itests

// tombstone 断言（design §7-M1-6 HB2 / §4.4 删除标记）：游戏侧删除（软删
// 行的收集投影视为消失）→ 再收集 → 生成 tombstone；投放差集命中 tombstone
// 归「恢复（已删除）」组（Restorable 标记——M1 的 BuildPlan 只覆盖收藏夹，
// beatmap_set 面以对账标记断言，收藏夹面的默认排除/勾选恢复在
// roundtrip_test 的全链覆盖）；同键重现自动清除。被版本链取代的消失不生成
// tombstone 在 versionchain_test 覆盖。

import (
	"testing"

	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

func TestTombstoneLifecycle(t *testing.T) {
	lib := mustLib(t)
	e := newEnv(t)

	// keep + gone 两集（不同 OnlineID → 不同身份键，互不并链）。
	files := map[string][]byte{}
	keep := mustSet(t, 3003, "Keep Artist", "Keep Title", "keep osu bytes", files)
	gone := mustSet(t, 3004, "Gone Artist", "Gone Title", "gone osu bytes", files)
	ref := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{keep, gone}, Files: files,
	})

	// ① 收集：两键 presence。
	e.collectInto(ref)
	if got := e.tombstoneCount(fixture.ForkLazer, hub.KindBeatmapSet, gone.Hash); got != 0 {
		t.Fatalf("布置失败：初收后已存在 tombstone = %d", got)
	}

	// ② 游戏侧删除 gone（软删；S4 语义），再收集 → 投影跳过 + tombstone 生成。
	setBeatmapSetDeleted(t, lib, ref.RealmPath, gone.Hash, true)
	rep := e.collectInto(ref)
	if !skipCount(rep.Skips, "软删谱面集（DeletePending）", 1) {
		t.Errorf("再收集 Skips = %+v，期望含「软删谱面集（DeletePending）×1」", rep.Skips)
	}
	if got := e.tombstoneCount(fixture.ForkLazer, hub.KindBeatmapSet, gone.Hash); got != 1 {
		t.Fatalf("删除后再收集 tombstone = %d，期望 1", got)
	}

	// ③ 投放差集命中 tombstone → 「恢复（已删除）」组（Restorable + 删除时间；
	// 默认不进计划、需用户显式勾选——勾选链路见 roundtrip_test）。
	items := e.reconcile(fixture.ForkLazer, hub.KindBeatmapSet)
	restores := map[string]hub.ReconcileItem{}
	for _, it := range items {
		if it.Restorable {
			restores[it.ContentKey] = it
		}
	}
	if len(restores) != 1 {
		t.Fatalf("恢复组 = %d 项（%v），期望恰 [gone]", len(restores), items)
	}
	ri, ok := restores[gone.Hash]
	if !ok {
		t.Fatalf("恢复组键 = %v，期望含 gone=%s", restores, gone.Hash)
	}
	if ri.Category != hub.ReconcileCreate || ri.DeletedAt <= 0 {
		t.Errorf("恢复项 = {cat=%s deletedAt=%d}，期望 {create, >0}", ri.Category, ri.DeletedAt)
	}
	// keep 未删除：不在恢复组（一致项不产出动作）。
	if _, ok := restores[keep.Hash]; ok {
		t.Error("keep 未删除却进入恢复组")
	}

	// ④ 同键重现自动清除（HB2 第三道闸）：软删撤销 → 再收集 → tombstone 清除。
	setBeatmapSetDeleted(t, lib, ref.RealmPath, gone.Hash, false)
	e.collectInto(ref)
	if got := e.tombstoneCount(fixture.ForkLazer, hub.KindBeatmapSet, gone.Hash); got != 0 {
		t.Errorf("同键重现后 tombstone = %d，期望自动清除为 0", got)
	}
}

// skipCount 断言 Skips 含指定原因与计数。
func skipCount(skips []hub.SkipStat, reason string, want int) bool {
	for _, s := range skips {
		if s.Reason == reason {
			return s.Count == want
		}
	}
	return false
}
