// Package appsvc 是 mygo Bind 的 UI 服务层（design.md §3.1、§1.4）：四服务
// + 进度事件 + 真实/Mock 后端切换（M-4）。方法签名保持可 JSON 序列化
// （mygo typed bindings 边界）；进度经构造时注入的 ProgressSink 推送
// （由 main 装配桥接 mygo.NewEvent，M-2）。设置页服务（SettingsService，
// §6.4）不在 M1 四服务之列，待 M3 设置落地时增补。
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

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("appsvc: 契约桩未实现")

// ProgressEvent 长任务进度事件（M-2：同步执行不阻塞 UI 线程）。
type ProgressEvent struct {
	Service string    // "collect" | "deploy"
	Stage   string
	Current int
	Total   int
	Message string
	At      time.Time
}

// ProgressSink 进度推送端（main 装配时桥接 mygo 事件流）。
type ProgressSink func(ProgressEvent)

// Selection 预览页勾选结果的一项（kind + 内容键）。
type Selection struct {
	Kind       hub.EntityKind
	ContentKey string
}

// LibraryService 库管理（§6.1）。
type LibraryService interface {
	// Scan 扫描数据根并按 ForkProfile 识别四端候选（目录、库文件、大小）。
	Scan() ([]forks.LibraryRef, error)
	// Register 手动注册自定义路径（storage.ini 场景）；识别失败返回
	// forks.ErrUnidentified，由 UI 呈现原始版本并要求用户指认 fork。
	Register(path string) (forks.LibraryRef, error)
	// List 已注册库列表（含身份、版本来源标注、进程状态等卡片数据）。
	List() []forks.LibraryRef
	// Remove 解除管理（不碰磁盘）。
	Remove(forkID string) error
}

// CollectService 收集（§6.3 收集阶段；可对多个 fork 顺序执行）。
type CollectService interface {
	// Collect 对单个 fork 执行收集；游戏运行中返回 guard.ErrGameRunning。
	Collect(forkID string) (collect.Report, error)
}

// CompareService 对比预览（§6.2）。全部查询走 hub，页面不触碰任何 fork 库。
type CompareService interface {
	// CompareForks fork↔fork 快照对比（四分类 + 冲突摘要）。
	CompareForks(source, target string) ([]diff.Entry, error)
	// PreviewDeploy hub 真相 → 单 fork 投放预演（四分类 + 跳过原因预演）。
	PreviewDeploy(target string) ([]diff.Entry, error)
	// DeletedEntries 目标 fork 的「恢复（已删除）」组（tombstone 命中，
	// 默认不勾选，展示删除时间）。
	DeletedEntries(target string) ([]diff.Entry, error)
	// BuildPlan 把勾选结果组装为投放计划（adapter 过滤在此层生效；
	// NeedsConfirm 项必须已经用户拍板）。
	BuildPlan(target string, sel []Selection) (deploy.Plan, error)
}

// DeployService 投放（§6.3 投放执行）。
type DeployService interface {
	// Deploy 执行计划；进度经构造时注入的 ProgressSink 实时推送，
	// 取消点在批间（已提交批保留）。
	Deploy(plan deploy.Plan) (deploy.Report, error)
}

// Backend 聚合四服务（mygo Bind 的目标对象形态）。
type Backend interface {
	LibraryService
	CollectService
	CompareService
	DeployService
}

// Deps 真实后端的协作依赖。
type Deps struct {
	Hub       *hub.Store
	Files     *filestore.Store
	Snapshots *forks.SnapshotStore
	Backups   *guard.Backups
	DataRoot  string // fork 数据根（P-3 平台规则由装配方解析后传入）
}

// NewRealBackend 构造真实后端。native 库加载失败时返回错误，由 main 进入
// P-2 降级模式（应用可启动、只显示诊断与只读探测不可用，不闪退）。
func NewRealBackend(deps Deps, prog ProgressSink) (Backend, error) {
	return nil, ErrNotImplemented
}
