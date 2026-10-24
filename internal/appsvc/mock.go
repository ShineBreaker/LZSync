package appsvc

// Mock 后端（M-4：ezrealmsync-design 的 --ui-test 同款结构）：四服务全
// 内存态，不触碰任何文件系统与 native 库，供无 native 库环境开发与冒烟。
// 数据刻意覆盖 UI 的全部呈现分支：四端卡片（含 Ez 版本解码与「未知
// （运行中）」降级）、五类差异徽标（含恢复组与弱键疑似）、守卫拦截
//（g0v0 模拟运行中）、批进度与取消。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

// mockSleep 步进间隔：足够肉眼观察进度条，又不拖慢冒烟。
const mockSleep = 40 * time.Millisecond

type mockBackend struct {
	lib   *mockLibrary
	cmp   *mockCompare
	sync  *mockSync
	mode  string
	reasn string
}

func newMockBackend(mode, reason string, prog ProgressSink) *Backend {
	if prog == nil {
		prog = func(ProgressEvent) {}
	}
	cmp := &mockCompare{createdAt: time.Now()}
	sync := &mockSync{prog: prog}
	b := &mockBackend{
		lib:  &mockLibrary{},
		cmp:  cmp,
		sync: sync,
		mode: mode, reasn: reason,
	}
	sync.cmp = cmp
	return &Backend{
		Library:  b.lib,
		Compare:  cmp,
		Sync:     sync,
		Settings: b,
		Mode:     mode,
		Reason:   reason,
	}
}

// --- 库管理 ---

type mockLibrary struct {
	mu   sync.Mutex
	refs map[string]forks.LibraryRef // 注册态（可被 Remove）
}

// mockRefs 固定四端样本：Ez 演示版本编码解码，g0v0 演示「运行中 →
// 版本未知」降级链与守卫拦截。
var mockRefs = []forks.LibraryRef{
	{ForkID: "lazer", DataDir: "/mock/.local/share/osu", RealmPath: "/mock/.local/share/osu/client.realm",
		DiskVersion: 52, VersionSource: forks.VersionFromOpen, UpstreamVersion: 52},
	{ForkID: "delta", DataDir: "/mock/.local/share/deltalazer", RealmPath: "/mock/.local/share/deltalazer/client.realm",
		DiskVersion: 51, VersionSource: forks.VersionFromSnapshot, UpstreamVersion: 51},
	{ForkID: "ez2lazer", DataDir: "/mock/.local/share/osu-Ez2Lazer", RealmPath: "/mock/.local/share/osu-Ez2Lazer/client_52011.realm",
		DiskVersion: 52011, VersionSource: forks.VersionFromFilename, UpstreamVersion: 52, ForkRev: 11},
	{ForkID: "g0v0", DataDir: "/mock/.local/share/g0v0", RealmPath: "/mock/.local/share/g0v0/client.realm",
		DiskVersion: 0, VersionSource: forks.VersionUnknownRunning},
}

func (m *mockLibrary) reg() map[string]forks.LibraryRef {
	if m.refs == nil {
		m.refs = map[string]forks.LibraryRef{}
		for _, r := range mockRefs {
			m.refs[r.ForkID] = r
		}
	}
	return m.refs
}

func (m *mockLibrary) Scan() ([]forks.LibraryRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]forks.LibraryRef, 0, len(mockRefs))
	for _, r := range mockRefs {
		m.reg()[r.ForkID] = r
		out = append(out, r)
	}
	return out, nil
}

func (m *mockLibrary) Register(path string) (forks.LibraryRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range mockRefs {
		if r.RealmPath == path {
			m.reg()[r.ForkID] = r
			return r, nil
		}
	}
	// 未命中样本：演示识别失败分支（ForkID 空串 + 原始版本，§6.1 指认）。
	return forks.LibraryRef{
		DataDir: "/mock/.local/share/unknown-fork", RealmPath: path,
		DiskVersion: 9999, VersionSource: forks.VersionFromOpen, UpstreamVersion: 9999,
	}, nil
}

func (m *mockLibrary) AssignFork(path, forkID string) (forks.LibraryRef, error) {
	p, ok := forks.Profile(forkID)
	if !ok {
		return forks.LibraryRef{}, fmt.Errorf("appsvc: 未知 fork %q（可选：[lazer delta ez2lazer g0v0]）", forkID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ref := forks.LibraryRef{
		ForkID: forkID, DataDir: "/mock/.local/share/" + p.DataDirName, RealmPath: path,
		DiskVersion: 9999, VersionSource: forks.VersionFromOpen,
	}
	ref.UpstreamVersion, ref.ForkRev = p.DecodeVersion(ref.DiskVersion)
	m.reg()[forkID] = ref
	return ref, nil
}

func (m *mockLibrary) List() []LibraryCard {
	m.mu.Lock()
	refs := make([]forks.LibraryRef, 0, len(m.reg()))
	for _, r := range m.reg() {
		refs = append(refs, r)
	}
	m.mu.Unlock()
	cards := make([]LibraryCard, 0, len(refs))
	for i, ref := range refs {
		card := LibraryCard{
			ForkID: ref.ForkID, DataDir: ref.DataDir, RealmPath: ref.RealmPath,
			DiskVersion: ref.DiskVersion, VersionSource: ref.VersionSource,
			UpstreamVersion: ref.UpstreamVersion, ForkRev: ref.ForkRev,
			VersionLabel: versionLabel(ref), Registered: true,
			FilesDir: ref.DataDir + "/files", RunningNames: []string{},
		}
		card.RealmSize = int64(1_048_576 + i*262_144)
		card.FilesSize = int64(3_221_225_472 + int64(i)*512_000_000)
		card.FilesCount = 4200 + i*1337
		if ref.ForkID == "g0v0" {
			card.ProcessRunning = true // 演示运行状态灯与警示条
			card.RunningNames = []string{"g0v0!"}
		}
		cards = append(cards, card)
	}
	return cards
}

func (m *mockLibrary) Remove(forkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reg()[forkID]; !ok {
		return fmt.Errorf("appsvc: 未注册的 fork %q", forkID)
	}
	delete(m.reg(), forkID)
	return nil
}

// --- 对比预览 ---

type mockCompare struct {
	mu        sync.Mutex
	plan      deploy.Plan
	createdAt time.Time
}

// mockPreviewRows hub → fork 投放预演样本（对 target=delta）：覆盖五类
// 徽标与默认勾选规则。
func mockPreviewRows() []DiffRow {
	return []DiffRow{
		{Kind: hub.KindCollection, ContentKey: "新谱面收藏", Category: CategoryCreate,
			Summary: "收藏夹「新谱面收藏」", DefaultChecked: true},
		{Kind: hub.KindCollection, ContentKey: "常用练习", Category: CategoryConflict,
			Summary: "收藏夹「常用练习」", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "BeatmapMD5Hashes",
				HubValue: "md5-a,md5-b,md5-c", ForkValue: "md5-a,md5-b"}}},
		{Kind: hub.KindCollection, ContentKey: "旧收藏", Category: CategoryRestore,
			Summary: "收藏夹「旧收藏」", DefaultChecked: false,
			DeletedAt: time.Now().Add(-72 * time.Hour).UnixMilli()},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-new-set-0001", Category: CategoryCreate,
			Summary: "Cranky - Terpsichore（Reol）", DefaultChecked: true},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-old-ver-0002", Category: CategoryVersionUpdate,
			Summary: "ginkiha - EOS（kamome sano）", IdentityKey: "online:1656406",
			Evidence: EvidenceStrong, DefaultChecked: true},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-weak-0003", Category: CategoryVersionUpdate,
			Summary: "Unknown Artist - Demo（mapper）", IdentityKey: "meta:UA|Demo|mapper",
			Evidence: EvidenceWeak, DefaultChecked: false,
			SkipReason: "元数据弱键「疑似」版本更新，需用户拍板（§5.4 三选一）"},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-conflict-0004", Category: CategoryConflict,
			Summary: "IA - Suna no Wakusei（nhato）", DefaultChecked: true,
			ConflictFields: []FieldDiff{
				{Field: "StatusInt", HubValue: "1", ForkValue: "0"},
				{Field: "DateAdded", HubValue: "1769000000000", ForkValue: "1768000000000"},
			}},
	}
}

// mockForkDiffRows fork↔fork 样本（lazer → delta）。
func mockForkDiffRows() []DiffRow {
	return []DiffRow{
		{Kind: hub.KindCollection, ContentKey: "新谱面收藏", Category: CategoryCreate,
			Summary: "收藏夹「新谱面收藏」", DefaultChecked: true},
		{Kind: hub.KindCollection, ContentKey: "delta 独有收藏", Category: CategoryTargetOnly,
			Summary: "收藏夹「delta 独有收藏」", DefaultChecked: false},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-old-ver-0002", Category: CategoryVersionUpdate,
			Summary: "ginkiha - EOS（kamome sano）", IdentityKey: "online:1656406",
			Evidence: EvidenceStrong, DefaultChecked: true},
		{Kind: hub.KindCollection, ContentKey: "常用练习", Category: CategoryConflict,
			Summary: "收藏夹「常用练习」", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "BeatmapMD5Hashes",
				HubValue: "md5-a,md5-b,md5-c", ForkValue: "md5-a,md5-b"}}},
	}
}

func (m *mockCompare) PreviewDeploy(target string) ([]DiffRow, error) {
	if target == "" {
		return nil, errors.New("appsvc: 请选择目标 fork")
	}
	return mockPreviewRows(), nil
}

func (m *mockCompare) CompareForks(source, target string) ([]DiffRow, error) {
	if source == "" || target == "" {
		return nil, errors.New("appsvc: 请选择对比双方 fork")
	}
	if source == target {
		return nil, errors.New("appsvc: 对比双方不能是同一个 fork")
	}
	return mockForkDiffRows(), nil
}

func (m *mockCompare) DeletedEntries(target string) ([]DiffRow, error) {
	if target == "" {
		return nil, errors.New("appsvc: 请选择目标 fork")
	}
	return []DiffRow{
		{Kind: hub.KindCollection, ContentKey: "旧收藏", Category: CategoryRestore,
			Summary: "收藏夹「旧收藏」", DefaultChecked: false,
			DeletedAt: time.Now().Add(-72 * time.Hour).UnixMilli()},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-deleted-0009", Category: CategoryRestore,
			Summary: "DECO*27 - Ghost Rule（Marathon）", DefaultChecked: false,
			DeletedAt: time.Now().Add(-240 * time.Hour).UnixMilli()},
	}, nil
}

// BuildPlan 勾选 → 计划（内存持久；M1 动作面与真实后端同构：仅收藏夹
// 产生 create/update，其余计 skip + 原因；走 deploy.NewPlan 校验）。
func (m *mockCompare) BuildPlan(target string, sel []Selection) (deploy.Plan, error) {
	if target == "" {
		return deploy.Plan{}, errors.New("appsvc: 生成计划缺少目标 fork")
	}
	if len(sel) == 0 {
		return deploy.Plan{}, errors.New("appsvc: 未勾选任何条目")
	}
	var targetRef forks.LibraryRef
	for _, r := range mockRefs {
		if r.ForkID == target {
			targetRef = r
		}
	}
	if targetRef.ForkID == "" {
		return deploy.Plan{}, fmt.Errorf("appsvc: 目标 fork %q 未注册", target)
	}
	rows := mockPreviewRows()
	byKey := map[string]DiffRow{}
	for _, r := range rows {
		byKey[string(r.Kind)+"\x00"+r.ContentKey] = r
	}
	var raw []deploy.PlanItem
	seen := map[string]bool{}
	for _, sl := range sel {
		key := string(sl.Kind) + "\x00" + sl.ContentKey
		if seen[key] {
			continue
		}
		seen[key] = true
		row, ok := byKey[key]
		if !ok {
			raw = append(raw, deploy.PlanItem{
				Kind: sl.Kind, ContentKey: sl.ContentKey, Action: deploy.ActionSkip,
				Summary:    string(sl.Kind) + " " + shortKey(sl.ContentKey),
				SkipReason: "预演数据中不存在该键（快照可能已过期，请刷新预览）",
			})
			continue
		}
		item := deploy.PlanItem{Kind: row.Kind, ContentKey: row.ContentKey, Summary: row.Summary}
		if row.Kind != hub.KindCollection {
			item.Action, item.SkipReason = deploy.ActionSkip, "谱面集/难度投放属 M2 写路径（§5.1 分阶段），M1 仅收藏夹"
		} else {
			switch row.Category {
			case CategoryCreate, CategoryRestore: // 勾选恢复组 = 用户显式拍板投放并清 tombstone（§5.4）
				item.Action = deploy.ActionCreate
				item.Restore = row.Category == CategoryRestore
			case CategoryConflict:
				item.Action = deploy.ActionUpdate
			default:
				item.Action, item.SkipReason = deploy.ActionSkip, "收藏夹无版本更新语义（内容键即 Name）"
			}
		}
		raw = append(raw, item)
	}
	plan, err := deploy.NewPlan(target, raw)
	if err != nil {
		return deploy.Plan{}, err
	}
	plan.Target = targetRef
	m.mu.Lock()
	m.plan, m.createdAt = plan, time.Now()
	m.mu.Unlock()
	return plan, nil
}

func (r DiffRow) summary() string {
	if r.Summary != "" {
		return r.Summary
	}
	return string(r.Kind) + " " + shortKey(r.ContentKey)
}

func (m *mockCompare) CurrentPlan() (deploy.Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plan.TargetFork == "" {
		return deploy.Plan{}, ErrNoPlan
	}
	return m.plan, nil
}

// --- 同步执行 ---

type mockSync struct {
	cmp    *mockCompare
	prog   ProgressSink
	mu     sync.Mutex
	cancel context.CancelFunc
	status SyncStatus
}

func (m *mockSync) emit(ev ProgressEvent) {
	if m.prog != nil {
		m.prog(ev)
	}
}

// Collect 模拟收集两阶段进度（投影 → 收仓），g0v0 演示守卫拦截。
func (m *mockSync) Collect(forkID string) (collect.Report, error) {
	if err := m.begin("collect", forkID); err != nil {
		return collect.Report{}, err
	}
	defer m.finish()
	if forkID == "g0v0" {
		return collect.Report{}, fmt.Errorf("%w（运行中：[g0v0!]）", guard.ErrGameRunning)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.setCancel(cancel)
	defer cancel()
	steps := []struct {
		stage   collect.Stage
		label   string
		total   int
	}{
		{collect.StageProject, "只读投影", 12},
		{collect.StageApply, "入 hub", 12},
		{collect.StageAdopt, "收仓+回链", 34},
	}
	for _, st := range steps {
		for i := 1; i <= st.total; i++ {
			if ctx.Err() != nil {
				return collect.Report{}, ctx.Err()
			}
			m.emit(ProgressEvent{Service: "collect", Stage: string(st.stage), Current: i, Total: st.total,
				Message: fmt.Sprintf("%s：%d/%d", st.label, i, st.total), At: time.Now()})
			time.Sleep(mockSleep)
		}
	}
	return collect.Report{
		Entities:        map[hub.EntityKind]int{hub.KindCollection: 28, hub.KindBeatmapSet: 1314, hub.KindBeatmap: 5210},
		Skips:           []hub.SkipStat{{Reason: "Protected 系统谱面集", Count: 4}, {Reason: "Ez 外部托管谱面集", Count: 2}},
		FilesAdopted:    34,
		FilesSkipped:    0,
		AdoptModeCounts: map[filestore.LinkMode]int{filestore.LinkSymlink: 34},
	}, nil
}

// DeployCurrentPlan 模拟投放：守卫 → 备份（一次性）→ 4 批 ×（建链接 →
// 事务写入 → DriftGuard）；取消点在批间（§3.3）。
func (m *mockSync) DeployCurrentPlan() (deploy.Report, error) {
	plan, err := m.cmp.CurrentPlan()
	if err != nil {
		return deploy.Report{}, err
	}
	if err := m.begin("deploy", plan.TargetFork); err != nil {
		return deploy.Report{}, err
	}
	defer m.finish()
	if plan.TargetFork == "g0v0" {
		return deploy.Report{}, fmt.Errorf("%w（运行中：[g0v0!]）", guard.ErrGameRunning)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.setCancel(cancel)
	defer cancel()

	backup := fmt.Sprintf("/mock/data/backups/client.realm.%s", time.Now().UTC().Format("20060102-150405"))
	m.emit(ProgressEvent{Service: "deploy", Stage: string(deploy.StageGuard), At: time.Now(), Message: "守卫复查通过"})
	time.Sleep(mockSleep)
	m.emit(ProgressEvent{Service: "deploy", Stage: string(deploy.StageBackup), At: time.Now(), Message: "写前自动备份：" + backup})
	time.Sleep(mockSleep)

	const batches = 4
	applied := []deploy.ItemResult{}
	notRun := []deploy.PlanItem{}
	cancelled := false
	for b := 1; b <= batches; b++ {
		if ctx.Err() != nil {
			cancelled = true
			for _, it := range remaining(plan, applied) {
				notRun = append(notRun, it)
			}
			break
		}
		for _, st := range []deploy.Stage{deploy.StageLink, deploy.StageWrite, deploy.StageVerify} {
			for i := 1; i <= 3; i++ {
				m.emit(ProgressEvent{Service: "deploy", Stage: string(st), Current: i, Total: 3,
					Message: fmt.Sprintf("批 %d/%d · %s：%d/3", b, batches, stageLabel(st), i), At: time.Now()})
				time.Sleep(mockSleep)
			}
		}
		for _, it := range plan.Items {
			if it.Action == deploy.ActionCreate || it.Action == deploy.ActionUpdate {
				applied = append(applied, deploy.ItemResult{Kind: it.Kind, ContentKey: it.ContentKey, Action: it.Action})
			}
		}
	}
	if cancelled {
		return deploy.Report{Applied: applied, Skipped: mockSkipStats(plan), NotRun: notRun, BackupPath: backup}, context.Canceled
	}
	// 演示一次执行期改判（§3.3：现场复核与计划不符）。
	report := deploy.Report{Applied: applied, Skipped: mockSkipStats(plan), BackupPath: backup}
	if len(applied) > 0 {
		first := applied[0]
		report.Repointed = []deploy.RepointedItem{{
			Item: deploy.PlanItem{Kind: first.Kind, ContentKey: first.ContentKey, Action: first.Action},
			Reason: "目标已出现同键行：新建 → 就地更新",
		}}
	}
	return report, nil
}

// mockSkipStats 计划内 skip 项按原因分组（SkipCollector 口径，§3.3）。
func mockSkipStats(plan deploy.Plan) []hub.SkipStat {
	counts := map[string]int{}
	for _, it := range plan.Items {
		if it.Action == deploy.ActionSkip {
			counts[it.SkipReason]++
		}
	}
	reasons := make([]string, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	out := make([]hub.SkipStat, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, hub.SkipStat{Reason: r, Count: counts[r]})
	}
	return out
}

// remaining 取计划中未被 applied 覆盖的项（未执行批清单，§6.3）。
func remaining(plan deploy.Plan, applied []deploy.ItemResult) []deploy.PlanItem {
	done := map[string]bool{}
	for _, a := range applied {
		done[string(a.Kind)+"\x00"+a.ContentKey] = true
	}
	var out []deploy.PlanItem
	for _, it := range plan.Items {
		if !done[string(it.Kind)+"\x00"+it.ContentKey] {
			out = append(out, it)
		}
	}
	return out
}

func (m *mockSync) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *mockSync) Running() SyncStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockSync) RestoreBackup(forkID, backupPath string) error {
	if forkID == "" || backupPath == "" {
		return errors.New("appsvc: 还原需要 fork 与备份路径")
	}
	if forkID == "g0v0" {
		return fmt.Errorf("%w（运行中：[g0v0!]）", guard.ErrGameRunning)
	}
	return nil
}

func (m *mockSync) begin(kind, forkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Busy {
		return ErrBusy
	}
	m.status = SyncStatus{Busy: true, Kind: kind, ForkID: forkID}
	return nil
}

func (m *mockSync) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	m.status = SyncStatus{}
}

func (m *mockSync) setCancel(cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = cancel
}

// --- 设置（mockSettings 即 mockBackend 的 SettingsService 实现） ---

func (b *mockBackend) Get() (SettingsInfo, error) {
	return SettingsInfo{
		Mode:       b.mode,
		Reason:     b.reasn,
		DataDir:    "/mock/data",
		HubDBPath:  "/mock/data/hub.db",
		BackupsDir: "/mock/data/backups",
		BackupsCount: 3,
		GuardNames: []string{"Ez2Lazer", "g0v0!", "osu", "osu!"},
		LinkSymlink:  true,
		LinkHardlink: true,
		LinkNote:     "Mock 探测结果（真实后端按 §5.3 现场探测）",
		NativeLibOK:  false,
		NativeLibErr: "Mock 后端不加载 native 库（M-4 冒烟模式）",
	}, nil
}
