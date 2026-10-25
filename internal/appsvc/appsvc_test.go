package appsvc

// 双后端行为测试：Mock 覆盖 UI 呈现分支（五类徽标、默认勾选、守卫拦截、
// 计划持久），真实后端覆盖已实现的数据面（forks 扫描注册、hub 对账 →
// 行映射、计划落盘恢复）；编排层（collect/deploy/diff）契约桩未施工时
// 断言其错误如实穿透（层实现落地后零改动接通）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/diff"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

func collectEvents() (*[]ProgressEvent, ProgressSink) {
	events := &[]ProgressEvent{}
	return events, func(ev ProgressEvent) { *events = append(*events, ev) }
}

// --- Mock 后端 ---

func TestMockLibraryScanListRemove(t *testing.T) {
	b := NewMockBackend("mock", "测试", nil)
	refs, err := b.Library.Scan()
	if err != nil || len(refs) != 4 {
		t.Fatalf("Scan = (%d, %v)，期望 4 个四端样本", len(refs), err)
	}
	cards := b.Library.List()
	if len(cards) != 4 {
		t.Fatalf("List 卡片数 = %d，期望 4", len(cards))
	}
	var ez, gov LibraryCard
	for _, c := range cards {
		switch c.ForkID {
		case "ez2lazer":
			ez = c
		case "g0v0":
			gov = c
		}
	}
	if ez.ForkID == "" || gov.ForkID == "" {
		t.Fatalf("List 缺少 ez2lazer/g0v0 卡片: %+v", cards)
	}
	if ez.VersionLabel != "52011 = 官方 52 + ez2lazer 11" {
		t.Fatalf("Ez 版本解码标注 = %q", ez.VersionLabel)
	}
	if !gov.ProcessRunning {
		t.Fatalf("g0v0 应演示运行中状态灯")
	}
	if gov.VersionSource != forks.VersionUnknownRunning {
		t.Fatalf("g0v0 版本来源 = %q，期望 unknown-running", gov.VersionSource)
	}
	if err := b.Library.Remove("lazer"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := b.Library.Remove("lazer"); err == nil {
		t.Fatalf("重复 Remove 应报错")
	}
	if got := len(b.Library.List()); got != 3 {
		t.Fatalf("Remove 后卡片数 = %d，期望 3", got)
	}
}

func TestMockRegisterUnidentifiedAndAssign(t *testing.T) {
	b := NewMockBackend("mock", "", nil)
	ref, err := b.Library.Register("/mock/unknown/client.realm")
	if err != nil {
		t.Fatalf("识别失败不走 error（载荷走值通道）: %v", err)
	}
	if ref.ForkID != "" || ref.DiskVersion == 0 {
		t.Fatalf("未识别 ref 应带空 ForkID 与原始版本: %+v", ref)
	}
	ref, err = b.Library.AssignFork("/mock/unknown/client.realm", "lazer")
	if err != nil || ref.ForkID != "lazer" {
		t.Fatalf("AssignFork = (%+v, %v)", ref, err)
	}
	if _, err := b.Library.AssignFork("/x", "nope"); err == nil {
		t.Fatalf("未知 fork 指认应报错")
	}
}

func TestMockCompareCategoriesAndDefaults(t *testing.T) {
	b := NewMockBackend("mock", "", nil)
	rows, err := b.Compare.PreviewDeploy("delta")
	if err != nil {
		t.Fatalf("PreviewDeploy: %v", err)
	}
	want := map[string]struct{ count, checked int }{}
	kinds := map[hub.EntityKind]int{}
	for _, r := range rows {
		w := want[string(r.Category)]
		w.count++
		if r.DefaultChecked {
			w.checked++
		}
		want[string(r.Category)] = w
		kinds[r.Kind]++
	}
	for _, cat := range []string{"create", "conflict", "version-update", "restore"} {
		if _, ok := want[cat]; !ok {
			t.Fatalf("预演缺少分类 %q: %+v", cat, rows)
		}
	}
	if want["restore"].checked != 0 {
		t.Fatalf("恢复组默认不应勾选（§6.2），实际勾选 %d", want["restore"].checked)
	}
	vu := want["version-update"]
	if vu.checked != 1 { // 强键勾选、弱键不勾
		t.Fatalf("版本更新默认勾选 = %d，期望 1（强键勾、弱键不勾）", vu.checked)
	}
	var weak int
	for _, r := range rows {
		if r.Evidence == EvidenceWeak && r.DefaultChecked {
			weak++
		}
	}
	if weak != 0 {
		t.Fatalf("弱键「疑似」默认不应勾选（§5.4）")
	}
	// 八类 kind 数据面：谱面集/收藏/皮肤/成绩 + M3 设置类三类各有行；
	// 难度不产独立对账行（生命周期挂集整集管理，§4.2），Mock 不伪造
	// 该数据面。
	for _, k := range []hub.EntityKind{hub.KindBeatmapSet, hub.KindCollection, hub.KindSkin, hub.KindScore,
		hub.KindKeyBinding, hub.KindRulesetSetting, hub.KindModPreset} {
		if kinds[k] == 0 {
			t.Fatalf("预演缺少 %s 类型行: %v", k, kinds)
		}
	}
	if kinds[hub.KindBeatmap] != 0 {
		t.Fatalf("难度不应有独立对账行（§4.2）: %v", kinds)
	}
	for _, r := range rows {
		if r.Category == CategoryVersionUpdate && r.CurrentKey == "" {
			t.Fatalf("版本更新行应携带 hub 链尾键（勾选 → version-replace 的写入键）: %+v", r)
		}
	}
	deleted, err := b.Compare.DeletedEntries("delta")
	if err != nil || len(deleted) == 0 {
		t.Fatalf("DeletedEntries = (%d, %v)", len(deleted), err)
	}
	if deleted[0].DeletedAt == 0 {
		t.Fatalf("恢复组应展示删除时间")
	}
	if _, err := b.Compare.CompareForks("lazer", "delta"); err != nil {
		t.Fatalf("CompareForks: %v", err)
	}
	if _, err := b.Compare.CompareForks("lazer", "lazer"); err == nil {
		t.Fatalf("同 fork 对比应报错")
	}
}

func TestMockBuildPlanAndPersistence(t *testing.T) {
	b := NewMockBackend("mock", "", nil)
	rows, err := b.Compare.PreviewDeploy("delta")
	if err != nil {
		t.Fatalf("PreviewDeploy: %v", err)
	}
	if _, err := b.Compare.CurrentPlan(); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("无计划时应返回 ErrNoPlan，实际 %v", err)
	}
	// 全部行都勾上（含恢复组 = 用户显式拍板；含弱键疑似 = 三选一拍板「替换」）。
	sel := make([]Selection, 0, len(rows))
	for _, r := range rows {
		sel = append(sel, Selection{Kind: r.Kind, ContentKey: r.ContentKey})
	}
	plan, err := b.Compare.BuildPlan("delta", sel)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.TargetFork != "delta" {
		t.Fatalf("计划目标 = %q", plan.TargetFork)
	}
	var actions, skips, replaces, confirms, restores int
	kindsActed := map[hub.EntityKind]bool{}
	for _, it := range plan.Items {
		if it.Action == deploy.ActionSkip {
			skips++
			if it.SkipReason == "" {
				t.Fatalf("skip 项必须携带原因（不静默吞掉，§1.5-4）")
			}
			continue
		}
		actions++
		kindsActed[it.Kind] = true
		switch {
		case it.Action == deploy.ActionVersionReplace:
			replaces++
			if it.OldContentKey == "" || it.OldContentKey == it.ContentKey {
				t.Fatalf("version-replace 项必须携带差异旧版本键: %+v", it)
			}
			if it.NeedsConfirm {
				confirms++
			}
		case it.Restore:
			restores++
		}
	}
	if actions == 0 {
		t.Fatalf("计划应含实际动作项")
	}
	if skips != 0 {
		t.Fatalf("M2 三类放行后不应再有 M1 收缩 skip，实际 %d 项", skips)
	}
	if replaces != 2 || confirms != 1 {
		t.Fatalf("版本替换 = %d（期望 2：强键+弱键拍板）、其中弱键留痕 = %d（期望 1）", replaces, confirms)
	}
	if restores == 0 {
		t.Fatalf("勾选恢复组应产生 Restore 标记的 create 项")
	}
	for _, k := range []hub.EntityKind{hub.KindCollection, hub.KindBeatmapSet, hub.KindSkin, hub.KindScore} {
		if !kindsActed[k] {
			t.Fatalf("计划动作面应覆盖 %s: %v", k, kindsActed)
		}
	}
	got, err := b.Compare.CurrentPlan()
	if err != nil || got.TargetFork != "delta" {
		t.Fatalf("CurrentPlan = (%+v, %v)", got, err)
	}
	if _, err := b.Compare.BuildPlan("delta", nil); err == nil {
		t.Fatalf("空勾选应报错")
	}
}

func TestMockSyncCollectDeployProgressAndGuard(t *testing.T) {
	events, prog := collectEvents()
	b := NewMockBackend("mock", "", prog)
	if _, err := b.Sync.Collect("g0v0"); !errors.Is(err, guard.ErrGameRunning) {
		t.Fatalf("g0v0 运行中收集应被守卫拦截，实际 %v", err)
	}
	rep, err := b.Sync.Collect("lazer")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if rep.Entities[hub.KindCollection] == 0 || len(*events) == 0 {
		t.Fatalf("收集报告/进度事件为空: %+v %d", rep, len(*events))
	}
	st := b.Sync.Running()
	if st.Busy {
		t.Fatalf("完成后 Running 应复位")
	}

	rows, _ := b.Compare.PreviewDeploy("delta")
	sel := []Selection{}
	for _, r := range rows {
		if r.DefaultChecked {
			sel = append(sel, Selection{Kind: r.Kind, ContentKey: r.ContentKey})
		}
	}
	if _, err := b.Compare.BuildPlan("delta", sel); err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	*events = nil
	drep, err := b.Sync.DeployCurrentPlan()
	if err != nil {
		t.Fatalf("DeployCurrentPlan: %v", err)
	}
	if drep.BackupPath == "" || len(drep.Applied) == 0 {
		t.Fatalf("投放报告缺备份路径/已应用项: %+v", drep)
	}
	if len(*events) == 0 {
		t.Fatalf("投放应推送进度事件（M-2）")
	}
	// 无计划时的部署前置错误。
	b2 := NewMockBackend("mock", "", nil)
	if _, err := b2.Sync.DeployCurrentPlan(); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("无计划部署应返回 ErrNoPlan，实际 %v", err)
	}
}

// --- 真实后端 ---

// newRealForTest 装配真实后端（临时数据目录 + 内存面依赖）；native 库
// 不可用时显式 SKIP（不静默绿，§8.1）。
func newRealForTest(t *testing.T, dataRoot string) (*Backend, *hub.Store, string) {
	t.Helper()
	dir := t.TempDir()
	b, h := newRealInDir(t, dataRoot, dir)
	return b, h, dir
}

func newRealInDir(t *testing.T, dataRoot, dir string) (*Backend, *hub.Store) {
	t.Helper()
	if _, err := realmc.Load(); err != nil {
		t.Skipf("native 库不可用，真实后端测试跳过: %v", err)
	}
	h, err := hub.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatalf("打开 hub: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	deps := Deps{
		Hub:       h,
		Backups:   &guard.Backups{Dir: filepath.Join(dir, "backups")},
		Guard:     guard.NewGuard(nil),
		DataRoot:  dataRoot,
		DataDir:   dir,
	}
	b, err := NewRealBackend(deps, nil)
	if err != nil {
		t.Fatalf("NewRealBackend: %v", err)
	}
	return b, h
}

func TestRealLibraryScanAndRegister(t *testing.T) {
	b, _, _ := newRealForTest(t, t.TempDir())
	refs, err := b.Library.Scan()
	if err != nil || len(refs) != 0 {
		t.Fatalf("空数据根扫描 = (%d, %v)，期望 0", len(refs), err)
	}
	if _, err := b.Library.Register(filepath.Join(t.TempDir(), "client.realm")); err == nil {
		t.Fatalf("不存在的库文件注册应报错")
	}
	if err := b.Library.Remove("lazer"); err == nil {
		t.Fatalf("移除未注册 fork 应报错")
	}
	cards := b.Library.List()
	if len(cards) != 0 {
		t.Fatalf("空注册表卡片 = %d，期望 0", len(cards))
	}
}

func TestRealComparePreviewFromHub(t *testing.T) {
	b, h, _ := newRealForTest(t, t.TempDir())
	ctx := context.Background()
	t0 := time.UnixMilli(1770000000000)
	// 收集两个 fork：源 fork A 有收藏 X（新增）、Y（冲突）；目标 fork B
	// 有旧值 Y 与已删除的 Z（tombstone 命中恢复组）。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []hub.Collection{
			{ID: "g1", Name: "X", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
			{ID: "g2", Name: "Y", BeatmapMD5Hashes: []string{"m1", "m2"}, LastModified: t0},
			{ID: "g3", Name: "Z", BeatmapMD5Hashes: []string{"m9"}, LastModified: t0},
		},
	}); err != nil {
		t.Fatalf("收集 A: %v", err)
	}
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "B", CollectedAt: t0,
		Collections: []hub.Collection{
			{ID: "g2", Name: "Y", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
			{ID: "g3", Name: "Z", BeatmapMD5Hashes: []string{"m9"}, LastModified: t0},
		},
	}); err != nil {
		t.Fatalf("收集 B: %v", err)
	}
	// B 侧删除 Z 后再收集 → Z 命中 tombstone。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "B", CollectedAt: t0.Add(time.Hour),
		Collections: []hub.Collection{
			{ID: "g2", Name: "Y", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
		},
	}); err != nil {
		t.Fatalf("再收集 B: %v", err)
	}
	// 再收集 A 恢复 canonical（HB3：canonical 值 = 最近一次收集确立，
	// B 的收集曾把 Y 整行覆盖为其本地值）→ Y 呈冲突。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "A", CollectedAt: t0.Add(2 * time.Hour),
		Collections: []hub.Collection{
			{ID: "g1", Name: "X", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
			{ID: "g2", Name: "Y", BeatmapMD5Hashes: []string{"m1", "m2"}, LastModified: t0},
			{ID: "g3", Name: "Z", BeatmapMD5Hashes: []string{"m9"}, LastModified: t0},
		},
	}); err != nil {
		t.Fatalf("再收集 A: %v", err)
	}

	rows, err := b.Compare.PreviewDeploy("B")
	if err != nil {
		t.Fatalf("PreviewDeploy: %v", err)
	}
	got := map[string]Category{}
	checked := map[string]bool{}
	for _, r := range rows {
		got[r.ContentKey] = r.Category
		checked[r.ContentKey] = r.DefaultChecked
	}
	if got["X"] != CategoryCreate || !checked["X"] {
		t.Fatalf("X 应为默认勾选的新增: %+v", rows)
	}
	if got["Y"] != CategoryConflict || checked["Y"] {
		t.Fatalf("Y 应为默认不勾选的冲突（§0-B4 判不准不动）: %+v", rows)
	}
	if got["Z"] != CategoryRestore || checked["Z"] {
		t.Fatalf("Z 应为默认不勾选的恢复组: %+v", rows)
	}
	var zRow *DiffRow
	for i := range rows {
		if rows[i].ContentKey == "Z" {
			zRow = &rows[i]
		}
	}
	if zRow == nil || zRow.DeletedAt == 0 {
		t.Fatalf("恢复组应展示删除时间（tombstone.deleted_at）")
	}
	deleted, err := b.Compare.DeletedEntries("B")
	if err != nil || len(deleted) != 1 || deleted[0].ContentKey != "Z" {
		t.Fatalf("DeletedEntries = (%+v, %v)", deleted, err)
	}
}

func TestRealBuildPlanPersistenceAndSkipReasons(t *testing.T) {
	dir := t.TempDir()
	dataRoot := t.TempDir()
	b, h := newRealInDir(t, dataRoot, dir)
	ctx := context.Background()
	t0 := time.UnixMilli(1770000000000)
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []hub.Collection{{ID: "g1", Name: "X", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0}},
	}); err != nil {
		t.Fatalf("收集 A: %v", err)
	}
	// 注册目标 fork（BuildPlan 需要 Target 解析结果落 RealmPath）。
	if _, err := b.Library.AssignFork("/tmp/lzsync-test-target/client.realm", "delta"); err != nil {
		t.Fatalf("AssignFork: %v", err)
	}
	if _, err := b.Compare.BuildPlan("g0v0", []Selection{{Kind: hub.KindCollection, ContentKey: "X"}}); err == nil {
		t.Fatalf("未注册目标应报错")
	}
	sel := []Selection{{Kind: hub.KindCollection, ContentKey: "X"}, {Kind: hub.KindCollection, ContentKey: "不存在"}}
	plan, err := b.Compare.BuildPlan("delta", sel)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("计划项 = %d，期望 2（含过期键 skip）", len(plan.Items))
	}
	if plan.Items[0].Action != deploy.ActionCreate {
		t.Fatalf("收藏夹新增应映射为 create: %+v", plan.Items[0])
	}
	if plan.Items[1].Action != deploy.ActionSkip || plan.Items[1].SkipReason == "" {
		t.Fatalf("过期键应计 skip + 原因: %+v", plan.Items[1])
	}
	if plan.Target.ForkID != "delta" || plan.Target.RealmPath == "" {
		t.Fatalf("计划应携带目标解析结果: %+v", plan.Target)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.json")); err != nil {
		t.Fatalf("计划应持久化到 plan.json: %v", err)
	}
	// 同一数据目录重建后端验证计划恢复（勾选计划持久）。
	b2, _ := newRealInDir(t, dataRoot, dir)
	got, err := b2.Compare.CurrentPlan()
	if err != nil || got.TargetFork != "delta" || len(got.Items) != 2 {
		t.Fatalf("重建后计划应恢复: (%+v, %v)", got, err)
	}
}


func TestRealSyncPreconditions(t *testing.T) {
	b, h, _ := newRealForTest(t, t.TempDir())
	ctx := context.Background()
	t0 := time.UnixMilli(1770000000000)
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []hub.Collection{{ID: "g1", Name: "X", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0}},
	}); err != nil {
		t.Fatalf("收集 A: %v", err)
	}
	// 无计划：部署在服务层即被拦下（不触碰编排层）。
	if _, err := b.Sync.DeployCurrentPlan(); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("无计划部署应返回 ErrNoPlan，实际 %v", err)
	}
	// 未注册 fork：收集在服务层即被拦下。
	if _, err := b.Sync.Collect("nope"); err == nil || errors.Is(err, collect.ErrNotImplemented) {
		t.Fatalf("未注册 fork 应在服务层报错，实际 %v", err)
	}
	// 注册 fork 后收集：应穿透到 collect 编排层（而非 appsvc 前置错误）。
	if _, err := b.Library.AssignFork("/tmp/lzsync-test-not-exist/client.realm", "lazer"); err != nil {
		t.Fatalf("AssignFork: %v", err)
	}
	if _, err := b.Sync.Collect("lazer"); err == nil {
		t.Fatalf("对不存在的库收集应报错（由编排层判定）")
	}
	// 注册目标后构建计划（BuildPlan 需要 Target 解析结果）。
	if _, err := b.Library.AssignFork("/tmp/lzsync-test-target/client.realm", "delta"); err != nil {
		t.Fatalf("AssignFork(delta): %v", err)
	}
	if _, err := b.Compare.BuildPlan("delta", []Selection{{Kind: hub.KindCollection, ContentKey: "X"}}); err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	st := b.Sync.Running()
	if st.Busy {
		t.Fatalf("无任务时 Running 应空闲")
	}
	b.Sync.Cancel() // 无任务时取消是 no-op，不 panic
}

func TestRealSettings(t *testing.T) {
	b, _, dir := newRealForTest(t, t.TempDir())
	info, err := b.Settings.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Mode != "real" || !info.NativeLibOK {
		t.Fatalf("真实后端设置应呈 real 模式与 native 可用: %+v", info)
	}
	if info.DataDir != dir || info.HubDBPath != filepath.Join(dir, "hub.db") {
		t.Fatalf("数据目录标注不符: %+v", info)
	}
	if len(info.GuardNames) == 0 {
		t.Fatalf("守卫名单应包含内置进程名")
	}
}

func TestDiffEntryToRowMapping(t *testing.T) {
	in := diff.Entry{
		Kind: hub.KindBeatmapSet, ContentKey: "h", Category: diff.CategoryVersionUpdate,
		IdentityKey: "online:1", Evidence: diff.EvidenceStrong,
	}
	row := diffEntryToRow(in)
	if row.Category != CategoryVersionUpdate || !row.DefaultChecked {
		t.Fatalf("强键版本更新默认勾选: %+v", row)
	}
	in.Evidence = diff.EvidenceWeak
	if row := diffEntryToRow(in); row.DefaultChecked {
		t.Fatalf("弱键默认不勾选: %+v", row)
	}
	in.Category = diff.CategoryTargetOnly
	if row := diffEntryToRow(in); row.Category != CategoryTargetOnly || row.DefaultChecked {
		t.Fatalf("目标独有无动作不勾选: %+v", row)
	}
}

// TestRealComparePreviewM2Kinds M2 三类（skin/score）接入预演：create/
// conflict/restore 三分类、tombstone 恢复组默认不勾选、摘要携带皮肤名与
// 成绩要素（§6.2 类型 Tab 数据面）。快照 fork 用真实 fork ID（hub 的
// fork_id 命名空间与注册表一致，BuildPlan 目标须命中 presence）。
func TestRealComparePreviewM2Kinds(t *testing.T) {
	b, h, _ := newRealForTest(t, t.TempDir())
	ctx := context.Background()
	t0 := time.UnixMilli(1770000000000)
	// delta（源）：皮肤 S1（新增）、S2（冲突源值）、S3（目标将删除→恢复组）；
	// 成绩 SC1（新增）、SC2（冲突源值）、SC3（恢复组）。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "delta", CollectedAt: t0,
		Skins: []hub.Skin{
			{ID: "gs1", Hash: "sk1", Name: "S1 皮肤", Creator: "作者甲"},
			{ID: "gs2", Hash: "sk2", Name: "S2 皮肤", Creator: "作者乙"},
			{ID: "gs3", Hash: "sk3", Name: "S3 皮肤", Creator: "作者丙"},
		},
		Scores: []hub.Score{
			{ID: "sc1", BeatmapHash: "bh1", Hash: "sc1", RulesetShortName: "osu",
				TotalScore: 123456, RankInt: 3, Date: t0},
			{ID: "sc2", BeatmapHash: "bh2", Hash: "sc2", RulesetShortName: "taiko",
				TotalScore: 999, RankInt: 2, Date: t0},
			{ID: "sc3", BeatmapHash: "bh3", Hash: "sc3", RulesetShortName: "fruits",
				TotalScore: 42, RankInt: 1, Date: t0},
		},
	}); err != nil {
		t.Fatalf("收集 delta: %v", err)
	}
	// lazer（目标）：持有 S2/SC2 的旧值（→冲突）与 S3/SC3（→随后删除进恢复组）。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "lazer", CollectedAt: t0.Add(time.Hour),
		Skins: []hub.Skin{
			{ID: "gs2", Hash: "sk2", Name: "S2 皮肤（改）", Creator: "作者乙"},
			{ID: "gs3", Hash: "sk3", Name: "S3 皮肤", Creator: "作者丙"},
		},
		Scores: []hub.Score{
			{ID: "sc2", BeatmapHash: "bh2", Hash: "sc2", RulesetShortName: "taiko",
				TotalScore: 500, RankInt: 2, Date: t0},
			{ID: "sc3", BeatmapHash: "bh3", Hash: "sc3", RulesetShortName: "fruits",
				TotalScore: 42, RankInt: 1, Date: t0},
		},
	}); err != nil {
		t.Fatalf("收集 lazer: %v", err)
	}
	// lazer 删除 S3/SC3 后再收集 → tombstone；delta 再收集恢复 canonical（HB3）。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "lazer", CollectedAt: t0.Add(2 * time.Hour),
		Skins: []hub.Skin{{ID: "gs2", Hash: "sk2", Name: "S2 皮肤（改）", Creator: "作者乙"}},
		Scores: []hub.Score{{ID: "sc2", BeatmapHash: "bh2", Hash: "sc2", RulesetShortName: "taiko",
			TotalScore: 500, RankInt: 2, Date: t0}},
	}); err != nil {
		t.Fatalf("再收集 lazer: %v", err)
	}
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "delta", CollectedAt: t0.Add(3 * time.Hour),
		Skins: []hub.Skin{
			{ID: "gs1", Hash: "sk1", Name: "S1 皮肤", Creator: "作者甲"},
			{ID: "gs2", Hash: "sk2", Name: "S2 皮肤", Creator: "作者乙"},
			{ID: "gs3", Hash: "sk3", Name: "S3 皮肤", Creator: "作者丙"},
		},
		Scores: []hub.Score{
			{ID: "sc1", BeatmapHash: "bh1", Hash: "sc1", RulesetShortName: "osu",
				TotalScore: 123456, RankInt: 3, Date: t0},
			{ID: "sc2", BeatmapHash: "bh2", Hash: "sc2", RulesetShortName: "taiko",
				TotalScore: 999, RankInt: 2, Date: t0},
			{ID: "sc3", BeatmapHash: "bh3", Hash: "sc3", RulesetShortName: "fruits",
				TotalScore: 42, RankInt: 1, Date: t0},
		},
	}); err != nil {
		t.Fatalf("再收集 delta: %v", err)
	}

	rows, err := b.Compare.PreviewDeploy("lazer")
	if err != nil {
		t.Fatalf("PreviewDeploy: %v", err)
	}
	type want struct {
		cat     Category
		checked bool
	}
	skinWant := map[string]want{
		"sk1": {CategoryCreate, true},
		"sk2": {CategoryConflict, false}, // §0-B4 判不准不动：冲突默认不勾
		"sk3": {CategoryRestore, false},
	}
	scoreWant := map[string]want{
		"bh1|sc1": {CategoryCreate, true},
		"bh2|sc2": {CategoryConflict, false},
		"bh3|sc3": {CategoryRestore, false},
	}
	skinGot, scoreGot := map[string]want{}, map[string]want{}
	for _, r := range rows {
		w := want{r.Category, r.DefaultChecked}
		switch r.Kind {
		case hub.KindSkin:
			skinGot[r.ContentKey] = w
		case hub.KindScore:
			scoreGot[r.ContentKey] = w
		}
	}
	if len(skinGot) != len(skinWant) || len(scoreGot) != len(scoreWant) {
		t.Fatalf("skin/score 预演行 = skin%v score%v，期望各 3 行", skinGot, scoreGot)
	}
	for key, w := range skinWant {
		if skinGot[key] != w {
			t.Fatalf("皮肤 %s = %+v，期望 %+v", key, skinGot[key], w)
		}
	}
	for key, w := range scoreWant {
		if scoreGot[key] != w {
			t.Fatalf("成绩 %s = %+v，期望 %+v", key, scoreGot[key], w)
		}
	}
	for _, r := range rows {
		if r.Kind == hub.KindSkin && r.ContentKey == "sk1" {
			if r.Summary != "皮肤「S1 皮肤」（作者甲）" {
				t.Fatalf("皮肤摘要 = %q", r.Summary)
			}
		}
		if r.Kind == hub.KindScore && r.ContentKey == "bh1|sc1" {
			if r.Summary != "成绩 osu · 123456 分 · 2026-02-02" {
				t.Fatalf("成绩摘要 = %q", r.Summary)
			}
		}
		if r.Category == CategoryRestore && r.DeletedAt == 0 {
			t.Fatalf("恢复组应携带删除时间: %+v", r)
		}
	}
	// 冲突字段级对比：皮肤 Name/Creator、成绩 TotalScore（§4.2 清单）。
	conflictFields := map[string][]string{}
	for _, r := range rows {
		if r.Category != CategoryConflict {
			continue
		}
		for _, f := range r.ConflictFields {
			conflictFields[string(r.Kind)+"."+f.Field] = append(conflictFields[string(r.Kind)+"."+f.Field], f.HubValue)
		}
	}
	if len(conflictFields["skin.Name"]) == 0 || len(conflictFields["score.TotalScore"]) == 0 {
		t.Fatalf("冲突字段应含皮肤 Name 与成绩 TotalScore: %v", conflictFields)
	}

	// DeletedEntries 覆盖全部启用 kind（皮肤/成绩恢复组在列）。
	deleted, err := b.Compare.DeletedEntries("lazer")
	if err != nil {
		t.Fatalf("DeletedEntries: %v", err)
	}
	kinds := map[hub.EntityKind]int{}
	for _, d := range deleted {
		kinds[d.Kind]++
	}
	if kinds[hub.KindSkin] != 1 || kinds[hub.KindScore] != 1 {
		t.Fatalf("恢复组应含 skin/score 各 1 项: %v", kinds)
	}

	// BuildPlan 放开 M2 三类：皮肤/成绩的 create 与 conflict 产生实际动作，
	// 恢复组勾选 = 拍板（Restore 标记）。
	if _, err := b.Library.AssignFork("/tmp/lzsync-test-target/client.realm", "lazer"); err != nil {
		t.Fatalf("AssignFork: %v", err)
	}
	sel := []Selection{
		{Kind: hub.KindSkin, ContentKey: "sk1"},
		{Kind: hub.KindSkin, ContentKey: "sk2"},
		{Kind: hub.KindSkin, ContentKey: "sk3"},
		{Kind: hub.KindScore, ContentKey: "bh1|sc1"},
		{Kind: hub.KindScore, ContentKey: "bh2|sc2"},
	}
	plan, err := b.Compare.BuildPlan("lazer", sel)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	got := map[string]deploy.PlanItem{}
	for _, it := range plan.Items {
		got[string(it.Kind)+"\x00"+it.ContentKey] = it
	}
	if it := got["skin\x00sk1"]; it.Action != deploy.ActionCreate || it.Restore {
		t.Fatalf("皮肤新增应为 create: %+v", it)
	}
	if it := got["skin\x00sk2"]; it.Action != deploy.ActionUpdate {
		t.Fatalf("皮肤冲突应为 update（hub 覆盖）: %+v", it)
	}
	if it := got["skin\x00sk3"]; it.Action != deploy.ActionCreate || !it.Restore {
		t.Fatalf("皮肤恢复组勾选应为 create+Restore: %+v", it)
	}
	if it := got["score\x00bh1|sc1"]; it.Action != deploy.ActionCreate {
		t.Fatalf("成绩新增应为 create: %+v", it)
	}
	if it := got["score\x00bh2|sc2"]; it.Action != deploy.ActionUpdate {
		t.Fatalf("成绩冲突应为 update: %+v", it)
	}
}

// TestPlanItemFromVersionUpdate 版本更新的计划项映射：ContentKey 落 hub
// 链尾键、旧版本键落 OldContentKey；弱键勾选留 NeedsConfirm 痕（§5.4）。
func TestPlanItemFromVersionUpdate(t *testing.T) {
	sum := &summarizer{setTitles: map[string]string{
		"new-hash": "Artist - Title（Author）",
	}}
	strong := planItemFrom(hub.ReconcileItem{
		Kind: hub.KindBeatmapSet, ContentKey: "old-hash", CurrentKey: "new-hash",
		Category: hub.ReconcileVersionUpdate, IdentityKey: "online:1",
		Evidence: EvidenceStrong,
	}, sum)
	if strong.Action != deploy.ActionVersionReplace ||
		strong.ContentKey != "new-hash" || strong.OldContentKey != "old-hash" || strong.NeedsConfirm {
		t.Fatalf("强键版本替换项不符: %+v", strong)
	}
	weak := planItemFrom(hub.ReconcileItem{
		Kind: hub.KindBeatmapSet, ContentKey: "old-hash", CurrentKey: "new-hash",
		Category: hub.ReconcileVersionUpdate, IdentityKey: "meta:A|T|U",
		Evidence: EvidenceWeak,
	}, sum)
	if !weak.NeedsConfirm || weak.Action != deploy.ActionVersionReplace {
		t.Fatalf("弱键勾选（拍板）应留 NeedsConfirm 并按替换执行: %+v", weak)
	}
	if weak.Summary != "Artist - Title（Author）" {
		t.Fatalf("版本替换摘要应取链尾曲名: %q", weak.Summary)
	}
}


// TestRealComparePreviewSettingsKinds M3 设置类三类的预演面（「不可见即
// 不可勾选投放」的缺口闭合验收，§6.2 八类 Tab）：三类各一形态——键位
// 新增（create 默认勾选）、规则集设置冲突（conflict 默认勾选 hub 覆盖）、
// ModPreset 恢复组（restore 默认不勾，HB2）；BuildPlan 三类勾选产生实际
// 动作（M3 写路径接通）。
func TestRealComparePreviewSettingsKinds(t *testing.T) {
	b, h, _ := newRealForTest(t, t.TempDir())
	ctx := context.Background()
	t0 := time.UnixMilli(1770000000000)
	// delta（源）：键位 KB1、规则集设置 RS1（冲突源值）、ModPreset MP1。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "delta", CollectedAt: t0,
		KeyBindings: []hub.KeyBinding{
			{ID: "gkb1", ActionInt: 7, KeyCombination: "Z"},
		},
		RulesetSettings: []hub.RulesetSetting{
			{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"bottomleft"`},
		},
		ModPresets: []hub.ModPreset{
			{ID: "gmp1", RulesetShortName: "osu", Name: "Score Preset",
				ModsJson: `[{"acronym":"HD","settings":{}}]`},
		},
	}); err != nil {
		t.Fatalf("收集 delta: %v", err)
	}
	// lazer：RS1 不同值（冲突面）；MP1 先持有、随后删除 → tombstone（恢复组）。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "lazer", CollectedAt: t0.Add(time.Hour),
		RulesetSettings: []hub.RulesetSetting{
			{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"topright"`},
		},
		ModPresets: []hub.ModPreset{
			{ID: "gmp1", RulesetShortName: "osu", Name: "Score Preset",
				ModsJson: `[{"acronym":"HD","settings":{}}]`},
		},
	}); err != nil {
		t.Fatalf("收集 lazer: %v", err)
	}
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "lazer", CollectedAt: t0.Add(2 * time.Hour),
		RulesetSettings: []hub.RulesetSetting{
			{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"topright"`},
		},
	}); err != nil {
		t.Fatalf("再收集 lazer（删除 MP1）: %v", err)
	}
	// delta 再收集恢复 canonical（HB3）。
	if _, err := h.ApplySnapshot(ctx, hub.Snapshot{
		ForkID: "delta", CollectedAt: t0.Add(3 * time.Hour),
		KeyBindings: []hub.KeyBinding{
			{ID: "gkb1", ActionInt: 7, KeyCombination: "Z"},
		},
		RulesetSettings: []hub.RulesetSetting{
			{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"bottomleft"`},
		},
		ModPresets: []hub.ModPreset{
			{ID: "gmp1", RulesetShortName: "osu", Name: "Score Preset",
				ModsJson: `[{"acronym":"HD","settings":{}}]`},
		},
	}); err != nil {
		t.Fatalf("再收集 delta: %v", err)
	}

	rows, err := b.Compare.PreviewDeploy("lazer")
	if err != nil {
		t.Fatalf("PreviewDeploy: %v", err)
	}
	got := map[hub.EntityKind]DiffRow{}
	for _, r := range rows {
		switch r.Kind {
		case hub.KindKeyBinding, hub.KindRulesetSetting, hub.KindModPreset:
			got[r.Kind] = r
		}
	}
	if len(got) != 3 {
		t.Fatalf("设置类预演行 = %d 类（%+v），期望三类各一行", len(got), got)
	}
	if r := got[hub.KindKeyBinding]; r.Category != CategoryCreate || !r.DefaultChecked {
		t.Errorf("键位行 = %s/%v，期望 create 默认勾选", r.Category, r.DefaultChecked)
	}
	if r := got[hub.KindRulesetSetting]; r.Category != CategoryConflict || r.DefaultChecked {
		t.Errorf("规则集设置行 = %s/%v，期望 conflict 默认不勾（§0-B4 判不准不动）", r.Category, r.DefaultChecked)
	}
	if r := got[hub.KindModPreset]; r.Category != CategoryRestore || r.DefaultChecked || r.DeletedAt == 0 {
		t.Errorf("ModPreset 行 = %s/%v/%d，期望 restore 默认不勾且带删除时间（HB2）", r.Category, r.DefaultChecked, r.DeletedAt)
	}
	// 键位行摘要来自 deploy 键级摘要（reconcileToRow 的 it.Summary 优先面）。
	if r := got[hub.KindKeyBinding]; r.Summary == "" {
		t.Error("键位行摘要为空（deploy.ReconcileSettings 自带键级摘要）")
	}

	// BuildPlan：三类勾选 → create/update/restore 实际动作（M3 写路径接通）。
	if _, err := b.Library.AssignFork("/tmp/lzsync-test-target/client.realm", "lazer"); err != nil {
		t.Fatalf("AssignFork: %v", err)
	}
	sel := []Selection{
		{Kind: hub.KindKeyBinding, ContentKey: got[hub.KindKeyBinding].ContentKey},
		{Kind: hub.KindRulesetSetting, ContentKey: got[hub.KindRulesetSetting].ContentKey},
		{Kind: hub.KindModPreset, ContentKey: got[hub.KindModPreset].ContentKey},
	}
	plan, err := b.Compare.BuildPlan("lazer", sel)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	actions := map[hub.EntityKind]deploy.Action{}
	for _, it := range plan.Items {
		actions[it.Kind] = it.Action
	}
	if actions[hub.KindKeyBinding] != deploy.ActionCreate {
		t.Errorf("键位计划动作 = %s，期望 create", actions[hub.KindKeyBinding])
	}
	if actions[hub.KindRulesetSetting] != deploy.ActionUpdate {
		t.Errorf("规则集设置计划动作 = %s，期望 update（冲突 hub 覆盖）", actions[hub.KindRulesetSetting])
	}
	if actions[hub.KindModPreset] != deploy.ActionCreate {
		t.Errorf("ModPreset 计划动作 = %s，期望 create（恢复项勾选拍板）", actions[hub.KindModPreset])
	}
}
