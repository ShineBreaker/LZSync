package deploy

import (
	"context"

	"lzsync/internal/filestore"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

// Deps 是 Runner 的协作依赖。
type Deps struct {
	Hub     *hub.Store
	Files   *filestore.Engine // M1 收藏夹无 files 依赖，可为 nil；M2 起必填
	Backups *guard.Backups
	Guard   *guard.Guard
}

// Stage 投放进度阶段（§3.3 批内顺序 + 两个一次性阶段）。
type Stage string

const (
	StageGuard  Stage = "guard"  // 守卫复查（投放启动时一次通过）
	StageBackup Stage = "backup" // 自动备份（首批前一次性生成，全计划共用，S2）
	StageLink   Stage = "link"   // 批内：建链接先行（文件计数，S6）
	StageWrite  Stage = "write"  // 批内：单事务写 realm 行（对象计数，File 表行与模型行同事务）
	StageVerify Stage = "verify" // 批内：DriftGuard 写前/写后结构快照比对
)

// ProgressFunc 批级进度回调；批事务进行中不回调（批内不可打断，§3.3）。
type ProgressFunc func(stage Stage, batchIndex, batchTotal, current, total int)

// ItemResult 单个已提交项结果。
type ItemResult struct {
	Kind       hub.EntityKind
	ContentKey string
	Action     Action
}

// BatchFailure 失败批：整批回滚 + 原因（§3.3 部分失败语义）。
type BatchFailure struct {
	BatchIndex int
	Reason     string
	Items      []PlanItem
}

// RepointedItem 执行期改判：现场复核与计划不符的项（§3.3——预览与执行
// 之间目标库可能漂移，绝不按计划快照盲目写入）。
type RepointedItem struct {
	Item   PlanItem
	Reason string // 如「目标已出现同键行：新建 → 就地更新」
}

// Report 投放执行报告（三段式 + 改判 + 备份，§3.3/§6.3）。
type Report struct {
	Applied       []ItemResult    // 已提交批的 applied 计数明细
	FailedBatches []BatchFailure  // 失败批（整批回滚 + 原因）
	NotRun        []PlanItem      // 未执行批清单（「从失败处重续」入口的输入）
	Repointed     []RepointedItem // 执行期改判
	BackupPath    string          // 本次共用的写前备份路径
}

// Runner 执行投放。
type Runner struct {
	deps Deps
}

// NewRunner 创建投放编排器。
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Deploy 执行计划（§3.3 执行模型）：
// 守卫复查 → 首批前一次性备份 → 按对象类型切批（批 = 事务；批大小默认值
// 见 §3.3）→ 每批「建链接先行（S6）→ 单事务写 realm 行 → DriftGuard（任何
// 结构漂移整批回滚并上报工具缺陷）」→ 每计划项以目标库现场状态重跑身份
// 匹配与判定（改判入报告）。
// 批间是取消点（ctx 取消 → 停止后续批，已提交批保留）；批失败 → 整批回滚
// 并停止，报告部分完成（合法终态，不强制整体撤销）。
// M1 范围：收藏夹单批（无 files 阶段）。
func (r *Runner) Deploy(ctx context.Context, plan Plan, prog ProgressFunc) (Report, error) {
	return Report{}, ErrNotImplemented
}
