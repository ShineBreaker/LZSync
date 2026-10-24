// Package appsvc 是 mygo Bind 的 UI 服务层（design.md §3.1、§1.4、§6）：
// 五服务（库管理 / 对比预览 / 同步执行 / 容器导出 / 设置）+ 进度事件 +
// 真实/Mock 后端切换（M-4）。方法签名保持可 JSON 序列化（mygo typed
// bindings 边界）；长任务进度经构造时注入的 ProgressSink 推送（由 main
// 装配桥接 mygo.NewEvent，M-2），mygo 的方法调用本身跑在独立 goroutine 上，
// UI 线程不被阻塞。
//
// 分层纪律：appsvc 是薄服务层——库身份走 forks、对账查询走 hub/diff、
// 收集与投放编排走 collect/deploy Runner；编排层契约桩尚未施工时，
// 相应服务如实上抛其 ErrNotImplemented，UI 由 Mock 后端承载开发与冒烟。
package appsvc

import (
	"errors"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/diff"
	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

// ErrBusy 已有长任务在执行（同一时刻只允许一个收集/投放任务）。
var ErrBusy = errors.New("appsvc: 已有同步任务在执行，请等待完成或取消")

// ErrNoPlan 尚未生成投放计划（对比页「生成投放计划」是执行的前置）。
var ErrNoPlan = errors.New("appsvc: 尚无投放计划，请先在对比预览页勾选并生成计划")

// ProgressEvent 长任务进度事件（M-2：同步执行不阻塞 UI 线程）。
type ProgressEvent struct {
	Service string    // "collect" | "deploy"
	Stage   string    // collect/deploy 的 Stage 字符串值
	Current int
	Total   int
	Message string // 人类可读补充（批号、对象计数等）
	At      time.Time
}

// ProgressSink 进度推送端（main 装配时桥接 mygo 事件流）。
type ProgressSink func(ProgressEvent)

// Category 对账分类的 UI 徽标口径（§6.2 四分类 + 恢复组）。
type Category string

const (
	CategoryCreate        Category = "create"         // 新增（源独有）
	CategoryTargetOnly    Category = "target-only"    // 目标独有（fork↔fork 模式）
	CategoryConflict      Category = "conflict"       // 冲突（内容键相等、非键字段不同）
	CategoryVersionUpdate Category = "version-update" // 版本更新（谱面身份键相等）
	CategoryRestore       Category = "restore"        // 恢复（已删除，目标 tombstone 命中，HB2）
)

// 证据强度（§4.2 第二层；影响默认勾选）。
const (
	EvidenceStrong = "strong" // OnlineID 强键 → 默认按版本更新执行
	EvidenceWeak   = "weak"   // 元数据弱键「疑似」→ 需用户确认，默认不勾选
)

// LibraryCard 库管理页卡片数据（§6.1）：身份、版本来源标注、files 体积、
// 进程运行状态（状态灯数据）。
type LibraryCard struct {
	ForkID          string             // 命中的 Profile ID；未识别为 ""
	DataDir         string             // fork 数据目录（files/ 的父目录）
	RealmPath       string
	DiskVersion     uint64             // 原始磁盘版本（未识别/运行中可为 0）
	VersionSource   forks.VersionSource // 版本值来源标注
	UpstreamVersion int                // 解码后的官方基线版本
	ForkRev         int                // fork 修订号（非 Ez 恒 0）
	VersionLabel    string             // 预组装显示（如 "52011 = 官方 52 + Ez 11"）
	RealmSize       int64              // 库文件字节数
	FilesDir        string             // §5.3 三级回退解析结果
	FilesSize       int64              // files/ 总字节（实体与链接一并计入）
	FilesCount      int
	ProcessRunning  bool     // 该 fork 的进程名命中守卫名单（§6.1 运行状态）
	RunningNames    []string // 命中的进程名
	Registered      bool     // 已注册（Discover 发现或手动注册）
}

// FieldDiff 冲突字段级对比（canonical vs 目标 fork presence）。
type FieldDiff struct {
	Field     string
	HubValue  string
	ForkValue string
}

// DiffRow 对比预览页单行（§6.2 差异列表的行模型）。Kind 是数据类型 Tab
// 的数据面维度（谱面集/难度/收藏/皮肤/成绩），前端按其分组渲染。
type DiffRow struct {
	Kind           hub.EntityKind
	ContentKey     string
	Category       Category
	Summary        string      // 人类可读摘要（曲名/收藏名/皮肤名/日期/分数）
	ConflictFields []FieldDiff // Category == CategoryConflict 时填充
	IdentityKey    string      // 版本更新时的谱面身份键（online:/meta: 前缀）
	Evidence       string      // 版本更新时的证据强度（EvidenceStrong/Weak）；弱键 = 需用户确认项
	CurrentKey     string      // 版本更新时的 hub 链尾内容键（勾选 → version-replace 的写入键）
	SkipReason     string      // 跳过原因预演（adapter 规则在预览期即跑）
	DeletedAt      int64       // 恢复组的删除时间（Unix 毫秒）
	DefaultChecked bool        // §6.2 默认勾选规则预计算
}

// Selection 预览页勾选结果的一项（类型 + 内容键）。
type Selection struct {
	Kind       hub.EntityKind
	ContentKey string
}

// SyncStatus 长任务运行状态（执行页轮询）。
type SyncStatus struct {
	Busy   bool
	Kind   string // "collect" | "deploy" | ""
	ForkID string
	Stage  string
}

// SettingsInfo 设置页数据（§6.4 M1 骨架：备份目录、守卫名单、链接策略显示）。
type SettingsInfo struct {
	Mode         string // "real" | "mock" | "mock-fallback"
	Reason       string // 降级/强制原因（空 = 正常真实后端）
	DataDir      string // hub 数据目录（§3.4）
	HubDBPath    string
	BackupsDir   string
	BackupsCount int    // 备份目录内条目数
	GuardNames   []string // 内置守卫名单（ForkProfile.ProcessNames 汇总）
	LinkSymlink  bool   // 链接能力探测（§5.3 分级）
	LinkHardlink bool
	LinkNote     string // 探测说明（如「不可用时自动降级复制」）
	NativeLibOK  bool   // librealm 加载状态（P-2 诊断）
	NativeLibErr string
}

// LibraryService 库管理（§6.1）。
type LibraryService interface {
	// Scan 扫描数据根并按 ForkProfile 识别四端候选（目录、库文件）。
	Scan() ([]forks.LibraryRef, error)
	// Register 手动注册自定义路径（storage.ini 场景）。识别失败不报错
	//（mygo 错误通道会丢失返回值载荷）：返回的 ref.ForkID 为空串并携带
	// 原始 DiskVersion，由 UI 呈现并要求用户指认（forks.ErrUnidentified
	// 语义）；路径不可访问等硬错误才走 error。
	Register(path string) (forks.LibraryRef, error)
	// AssignFork 用户指认 fork 身份（识别失败绝不猜，§6.1 核心交互）。
	AssignFork(path, forkID string) (forks.LibraryRef, error)
	// List 已注册库卡片列表（状态灯数据）。
	List() []LibraryCard
	// Remove 解除管理（不碰磁盘）。
	Remove(forkID string) error
}

// CompareService 对比预览（§6.2）。全部查询走 hub（diff 层），页面不触碰
// 任何 fork 库；BuildPlan 把勾选结果组装为投放计划并持久（M1 落 hub 数据
// 目录 plan.json，执行页跨页/重启可取）。
type CompareService interface {
	// PreviewDeploy hub 真相 → 单 fork 投放预演（四分类 + 跳过原因预演）。
	PreviewDeploy(target string) ([]DiffRow, error)
	// CompareForks fork↔fork 快照对比（两份 presence 差集）。
	CompareForks(source, target string) ([]DiffRow, error)
	// DeletedEntries 目标 fork 的「恢复（已删除）」组（tombstone 命中，
	// 默认不勾选，展示删除时间）。
	DeletedEntries(target string) ([]DiffRow, error)
	// BuildPlan 把勾选结果组装为投放计划（覆盖全部启用 kind：收藏夹/
	// 谱面集/皮肤/成绩）。NeedsConfirm 项（元数据弱键疑似）必须已经用户
	// 拍板——勾选即视为拍板（NeedsConfirm 置位留痕）；「恢复（已删除）」
	// 组同理，勾选即显式拍板投放并清 tombstone。
	BuildPlan(target string, sel []Selection) (deploy.Plan, error)
	// CurrentPlan 当前持久化的投放计划（无计划返回 ErrNoPlan）。
	CurrentPlan() (deploy.Plan, error)
}

// SyncService 同步执行（§6.3）：收集（可对多个 fork 顺序执行）与投放，
// 进度经 ProgressSink 实时推送；取消点在批间（已提交批保留，§3.3）。
type SyncService interface {
	// Collect 对单个 fork 执行收集；游戏运行中返回 guard.ErrGameRunning。
	Collect(forkID string) (collect.Report, error)
	// DeployCurrentPlan 执行当前计划（守卫 → 备份 → 批事务，§3.3）。
	DeployCurrentPlan() (deploy.Report, error)
	// Cancel 取消当前长任务（批事务进行中不可打断，批间生效）。
	Cancel()
	// Running 当前任务状态。
	Running() SyncStatus
	// RestoreBackup 从备份还原目标库（还原前自动再备份，§6.3 恢复入口）。
	RestoreBackup(forkID, backupPath string) error
}

// ExportResult 容器导出结果（§7-M3-5 路线④）。拉起失败不吞路径：容器
// 文件已生成，人话提示进 Note，用户总能手动导入。
type ExportResult struct {
	Paths []string // 生成的容器绝对路径（临时目录）
	Note  string   // 拉起结果人话提示；空 = 已全部交给系统默认程序
}

// ExportService 容器降级路径（§7-M3-5 路线④：不碰 realm，容器导入）。
type ExportService interface {
	// ExportContainers 按 kind 从 hub 真相读取 payload，与仓内实体一起生成
	// .osz/.osk/.osr 到临时目录，并以系统默认打开方式把容器交给目标 fork
	// 客户端（lazer 启动参数导入，go-realm-options §5-1）。仅谱面集/皮肤/
	// 成绩有容器形态。
	ExportContainers(forkID string, kind hub.EntityKind, contentKeys []string) (ExportResult, error)
}

// SettingsService 设置（§6.4 M1 骨架：只读显示）。
type SettingsService interface {
	Get() (SettingsInfo, error)
}

// Backend 聚合服务（main 装配后逐字段 BindAs 暴露给前端）。
type Backend struct {
	Library  LibraryService
	Compare  CompareService
	Sync     SyncService
	Export   ExportService
	Settings SettingsService
	Mode     string // SettingsInfo.Mode 的来源
	Reason   string
}

// Deps 真实后端的协作依赖（main 按 §3.4 hub 目录布局装配）。
type Deps struct {
	Hub       *hub.Store
	Files     *filestore.Engine
	Snapshots *forks.SnapshotStore
	Backups   *guard.Backups
	Guard     *guard.Guard
	DataRoot  string // fork 数据根（P-3 平台规则由装配方解析后传入）
	DataDir   string // LZSync 自有数据目录（plan.json 持久落点）
}

// NewRealBackend 构造真实后端。native 库加载失败时返回错误，由 main 进入
// P-2 降级（应用可启动、切换 Mock 后端并显示诊断，不闪退）。
func NewRealBackend(deps Deps, prog ProgressSink) (*Backend, error) {
	return newRealBackend(deps, prog)
}

// NewMockBackend 构造四服务全 Mock 的后端（M-4：UI 在没有 native 库的
// 环境开发与冒烟）。数据由内存态承载，不触碰任何文件系统与 native 库；
// mode/reason 由装配方传入以在设置页呈现降级原因，进度经 prog 推送。
func NewMockBackend(mode, reason string, prog ProgressSink) *Backend {
	return newMockBackend(mode, reason, prog)
}

// ensureRows/ensureStrs 保证切片与映射的非 nil 形态（JSON 序列化边界：
// nil 切片/映射会渲染成 null，前端类型是数组/对象）。
func ensureStrs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func ensureRows(r []DiffRow) []DiffRow {
	if r == nil {
		return []DiffRow{}
	}
	return r
}

// diffEntryToRow 把 diff 层条目映射为 UI 行（CompareForks 委托路径）。
func diffEntryToRow(e diff.Entry) DiffRow {
	cat := CategoryCreate
	switch e.Category {
	case diff.CategoryTargetOnly:
		cat = CategoryTargetOnly
	case diff.CategoryConflict:
		cat = CategoryConflict
	case diff.CategoryVersionUpdate:
		cat = CategoryVersionUpdate
	}
	fields := make([]FieldDiff, 0, len(e.ConflictFields))
	for _, f := range e.ConflictFields {
		fields = append(fields, FieldDiff{Field: f.Field, HubValue: f.HubValue, ForkValue: f.ForkValue})
	}
	// 默认勾选（§6.2）：新增/冲突默认全选；目标独有无动作、弱键需拍板。
	checked := cat == CategoryCreate || cat == CategoryConflict ||
		(cat == CategoryVersionUpdate && e.Evidence == EvidenceStrong)
	return DiffRow{
		Kind:           e.Kind,
		ContentKey:     e.ContentKey,
		Category:       cat,
		Summary:        e.Summary,
		ConflictFields: fields,
		IdentityKey:    e.IdentityKey,
		Evidence:       string(e.Evidence),
		SkipReason:     "",
		DefaultChecked: checked,
	}
}
