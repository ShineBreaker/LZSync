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
	for _, r := range rows {
		w := want[string(r.Category)]
		w.count++
		if r.DefaultChecked {
			w.checked++
		}
		want[string(r.Category)] = w
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
	// 全部行都勾上（含恢复组 = 用户显式拍板）。
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
	var actions, skips int
	for _, it := range plan.Items {
		if it.Action == deploy.ActionSkip {
			skips++
			if it.SkipReason == "" {
				t.Fatalf("skip 项必须携带原因（不静默吞掉，§1.5-4）")
			}
		} else {
			actions++
		}
	}
	if actions == 0 {
		t.Fatalf("计划应含收藏夹实际动作项")
	}
	if skips == 0 {
		t.Fatalf("谱面集勾选应计为 skip + M2 原因")
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
	if got["Y"] != CategoryConflict || !checked["Y"] {
		t.Fatalf("Y 应为默认勾选的冲突（hub 覆盖，§5.4）: %+v", rows)
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
