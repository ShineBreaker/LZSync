// Package collect 是收集方向编排（fork → hub，design.md §3.2/§3.3）：
// 只读投影 → hub 单事务入仓（hub.ApplySnapshot）→ 收仓 + 回链。
// 幂等可重跑；游戏运行时整段被守卫拦截。
package collect

import (
	"context"
	"errors"

	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("collect: 契约桩未实现")

// Deps 是 Runner 的协作依赖。
type Deps struct {
	Hub       *hub.Store
	Files     *filestore.Engine
	Snapshots *forks.SnapshotStore
	Guard     *guard.Guard
}

// Stage 收集进度阶段（§3.2 收集三步）。
type Stage string

const (
	StageProject Stage = "project" // 只读投影（对象计数）
	StageApply   Stage = "apply"   // 投影入 hub（实体/快照/tombstone）
	StageAdopt   Stage = "adopt"   // 收仓 + 回链（文件计数）
)

// ProgressFunc 进度回调（appsvc 层桥接为前端事件，M-2）。
type ProgressFunc func(stage Stage, current, total int)

// Report 收集执行报告。
type Report struct {
	Entities        map[hub.EntityKind]int    // 各类型入仓行数
	Skips           []hub.SkipStat            // 投影跳过（原因 + 计数）
	FilesAdopted    int                       // 收仓成功文件数
	FilesSkipped    int                       // 收仓跳过数（能力探测整体跳过等）
	AdoptModeCounts map[filestore.LinkMode]int // 收仓后各链接形态计数（降级统计）
}

// Runner 执行对单个 fork 的完整收集。
type Runner struct {
	deps Deps
}

// NewRunner 创建收集编排器。
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Collect 对 target 库执行收集：
//  1. 守卫拦截（游戏运行中直接报 guard.ErrGameRunning；呈现缓存快照的
//     回退策略属 appsvc，不在本层）；
//  2. 动态只读投影（跳过规则 §4.1；跳过项计数入 Report.Skips——不静默吞）；
//  3. hub.ApplySnapshot 单事务入仓（部分失败不产生 tombstone，HB2）；
//  4. 顺手采集结构档案（同版本只留首份，§4.3）；
//  5. 收仓 + 回链（HB4：能力探测 + 首文件试探失败 → 该 fork 整体跳过
//     收仓、文件留原地 + 计数——纯增益操作，跳过只损失磁盘节约）。
//
// 幂等：重跑 = 重写快照 + 补收文件，无部分完成风险（§3.3）。
func (r *Runner) Collect(ctx context.Context, target forks.LibraryRef, prog ProgressFunc) (Report, error) {
	return Report{}, ErrNotImplemented
}
