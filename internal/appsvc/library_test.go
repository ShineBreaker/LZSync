package appsvc

// buildLibrary 纯函数单测（DESIGN.md §4.2）：覆盖态规则 a–g 逐条、
// 行级 worst-state 排序（conflict > confirm > incoming > ok）、Deleted/
// DeletedAt 累计与 Summary 排序。real/mock 共用同一推导——这里直测规则面。

import (
	"testing"

	"lzsync/internal/hub"
)

func libItem(items []LibraryItem, key string) *LibraryItem {
	for i := range items {
		if items[i].ContentKey == key {
			return &items[i]
		}
	}
	return nil
}

func covOf(it *LibraryItem, fork string) ForkCoverage {
	for _, fc := range it.Forks {
		if fc.ForkID == fork {
			return fc
		}
	}
	return ForkCoverage{}
}

func TestBuildLibraryCoverageRules(t *testing.T) {
	canon := []canonItem{
		{ContentKey: "c1", Summary: "收藏夹「c1」"},
		{ContentKey: "c2", Summary: "收藏夹「c2」"},
	}
	fA := forkLibInput{
		ForkID: "A", Target: true,
		Preview: []DiffRow{
			{Kind: hub.KindCollection, ContentKey: "k-conf", Category: CategoryConflict, Summary: "cf", SkipReason: "冲突原因"},
			{Kind: hub.KindCollection, ContentKey: "k-weak", Category: CategoryVersionUpdate, Evidence: EvidenceWeak, Summary: "wk"},
			{Kind: hub.KindCollection, ContentKey: "k-ver", Category: CategoryVersionUpdate, Evidence: EvidenceStrong, Summary: "ver"},
			{Kind: hub.KindCollection, ContentKey: "k-new", Category: CategoryCreate, Summary: "new"},
			{Kind: hub.KindCollection, ContentKey: "k-uns", Category: CategoryCreate, Summary: "uns", SkipReason: "不支持类型"},
			{Kind: hub.KindCollection, ContentKey: "k-restore", Category: CategoryRestore, Summary: "rs"},
			{Kind: hub.KindCollection, ContentKey: "k-conf-del", Category: CategoryConflict, Summary: "cd"},
		},
		Deleted: []DiffRow{
			{Kind: hub.KindCollection, ContentKey: "k-del", DeletedAt: 100},
			{Kind: hub.KindCollection, ContentKey: "k-conf-del", DeletedAt: 50},
		},
		Presence: map[string]bool{"c1": true, "k-has": true, "k-ver": true},
	}
	fB := forkLibInput{
		ForkID: "B", Target: false,
		Preview: []DiffRow{
			{Kind: hub.KindCollection, ContentKey: "k-conf", Category: CategoryConflict},
			{Kind: hub.KindCollection, ContentKey: "k-weak", Category: CategoryVersionUpdate, Evidence: EvidenceWeak},
			{Kind: hub.KindCollection, ContentKey: "k-ver", Category: CategoryVersionUpdate, Evidence: EvidenceStrong},
			{Kind: hub.KindCollection, ContentKey: "k-new", Category: CategoryCreate},
		},
		Deleted:  []DiffRow{{Kind: hub.KindCollection, ContentKey: "k-del", DeletedAt: 200}},
		Presence: map[string]bool{"c1": true, "k-ver": true},
	}
	items := buildLibrary(hub.KindCollection, canon, []forkLibInput{fA, fB})
	byKey := map[string]*LibraryItem{}
	for i := range items {
		byKey[items[i].ContentKey] = &items[i]
	}

	assertCov := func(key, fork, wantState, wantReason string) {
		t.Helper()
		it := byKey[key]
		if it == nil {
			t.Fatalf("行 %q 不在行集", key)
		}
		got := covOf(it, fork)
		if got.State != wantState || got.Reason != wantReason {
			t.Errorf("%s/%s 覆盖 = (%q,%q)，期望 (%q,%q)", key, fork, got.State, got.Reason, wantState, wantReason)
		}
	}

	// 规则 a：冲突行 → conflict（含原因），与 Target 无关。
	assertCov("k-conf", "A", covConflict, "冲突原因")
	assertCov("k-conf", "B", covConflict, "")
	// 规则 b：弱键版本更新 → confirm（两端都 confirm，与 Target 无关）。
	assertCov("k-weak", "A", covConfirm, "")
	assertCov("k-weak", "B", covConfirm, "")
	// 规则 c：强键版本更新 + Target → incoming；非 Target 回退后续规则
	//（presence 命中 → has）。
	assertCov("k-ver", "A", covIncoming, "")
	assertCov("k-ver", "B", covHas, "")
	// 规则 d：新增 + Target + 无跳过原因 → incoming；非 Target 无原因 →
	// none + 「不写入」。
	assertCov("k-new", "A", covIncoming, "")
	assertCov("k-new", "B", covNone, "不写入")
	// 规则 d 失败 → g：新增行带跳过原因 → none + 原因；B 无该行 →
	// none + 「不写入」。
	assertCov("k-uns", "A", covNone, "不支持类型")
	assertCov("k-uns", "B", covNone, "不写入")
	// 规则 e：删除组命中 → deleted；预演恢复组行同样 → deleted。
	assertCov("k-del", "A", covDeleted, "")
	assertCov("k-del", "B", covDeleted, "")
	assertCov("k-restore", "A", covDeleted, "")
	// 规则 a 先于 e：冲突行 + 删除组命中 → 仍 conflict；但键在删除组
	// → 行 Deleted 标记独立成立。
	assertCov("k-conf-del", "A", covConflict, "")
	if it := byKey["k-conf-del"]; !it.Deleted {
		t.Error("k-conf-del 应标记 Deleted（A 删除组命中）")
	}
	// 规则 f：无预演行、presence 命中 → has。
	assertCov("c1", "A", covHas, "")
	assertCov("c1", "B", covHas, "")
	// 规则 g：无行无 presence → none；非 Target 附「不写入」，Target 空。
	assertCov("c2", "A", covNone, "")
	assertCov("c2", "B", covNone, "不写入")
	assertCov("k-restore", "B", covNone, "不写入")

	// 行级 worst-state：conflict > confirm > incoming > ok。
	assertRow := func(key, want string) {
		t.Helper()
		if it := byKey[key]; it == nil || it.State != want {
			t.Errorf("行 %q State = %+v，期望 %q", key, byKey[key], want)
		}
	}
	assertRow("k-conf", rowStateConflict)
	assertRow("k-conf-del", rowStateConflict)
	assertRow("k-weak", rowStateConfirm)
	assertRow("k-ver", rowStateIncoming)
	assertRow("k-new", rowStateIncoming)
	assertRow("c1", rowStateOK)
	assertRow("k-uns", rowStateOK)
	assertRow("k-del", rowStateOK)

	// Deleted/DeletedAt：行级删除时间 = 各端删除时间最大者（100 vs 200）。
	if it := byKey["k-del"]; !it.Deleted || it.DeletedAt != 200 {
		t.Errorf("k-del 删除面 = %+v，期望 Deleted + DeletedAt 200", it)
	}
	if it := byKey["k-restore"]; !it.Deleted || it.DeletedAt != 0 {
		t.Errorf("k-restore 删除面 = %+v，期望 Deleted + DeletedAt 0", it)
	}
}

func TestBuildLibrarySortAndRowSet(t *testing.T) {
	// 行集 = canonical ∪ 各端预演/删除键；排序 = Summary 小写 → ContentKey。
	canon := []canonItem{
		{ContentKey: "k2", Summary: "Beta"},
		{ContentKey: "k1", Summary: "alpha"},
	}
	f := forkLibInput{
		ForkID: "F", Target: true,
		Preview: []DiffRow{
			{Kind: hub.KindSkin, ContentKey: "k3", Category: CategoryCreate, Summary: "zeta"},
			{Kind: hub.KindSkin, ContentKey: "k4", Category: CategoryCreate, Summary: "Alpha"},
		},
		Deleted: []DiffRow{{Kind: hub.KindSkin, ContentKey: "k5", Summary: "omega", DeletedAt: 1}},
	}
	items := buildLibrary(hub.KindSkin, canon, []forkLibInput{f})
	if len(items) != 5 {
		t.Fatalf("行数 = %d，期望 5（2 canonical + 2 预演 + 1 删除组）", len(items))
	}
	got := []string{items[0].ContentKey, items[1].ContentKey, items[2].ContentKey, items[3].ContentKey, items[4].ContentKey}
	want := []string{"k1", "k4", "k2", "k5", "k3"} // alpha < Alpha < Beta < omega < zeta
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] || got[4] != want[4] {
		t.Fatalf("排序 = %v，期望 %v", got, want)
	}
}

func TestBuildLibraryEmptyForks(t *testing.T) {
	// 无注册端：canonical 行仍在（Forks 空，State ok）。
	items := buildLibrary(hub.KindCollection, []canonItem{{ContentKey: "c", Summary: "收藏夹「c」"}}, nil)
	if len(items) != 1 || items[0].State != rowStateOK || len(items[0].Forks) != 0 {
		t.Fatalf("无端库行 = %+v", items)
	}
}
