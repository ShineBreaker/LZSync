// Package deploy 是投放方向编排（hub → fork，design.md §3.1 的 publish
// 模块、§3.2/§3.3）：计划 → 守卫 → 备份 → 建链接先行 → 批事务写 realm 行
// → DriftGuard → 结果报告。批 = 原子单位（S1 回滚承诺按批兑现）+ 进度
// 单位 + 取消单位。
package deploy

import (
	"errors"

	"lzsync/internal/hub"
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("deploy: 契约桩未实现")

// Action 计划项动作。
type Action string

const (
	ActionCreate         Action = "create"          // 目标缺行 → 以 origin_guid 新建（§4.2）
	ActionUpdate         Action = "update"          // 内容键命中 → 就地更新白名单列（保目标 Guid）
	ActionVersionReplace Action = "version-replace" // 版本更新 → 整集原子替换（M2，§5.4）
	ActionSkip           Action = "skip"            // 跳过（必须携带原因）
)

// PlanItem 单个计划项 = 动作 + 跳过原因预演（§6.2：adapter 规则在预览期即跑）。
type PlanItem struct {
	Kind         hub.EntityKind
	ContentKey   string
	Action       Action
	Summary      string // 人类可读摘要（收藏名/曲名/难度名/分数）
	SkipReason   string // Action == ActionSkip 时必填
	NeedsConfirm bool   // 元数据弱键「疑似」等需用户拍板项（默认不勾选，§6.2）
}

// Plan 投放计划：预览页勾选结果的快照（计划项按内容键幂等，§3.3——重跑
// 已提交项自然判为「已存在 → 跳过」，重续安全）。
type Plan struct {
	TargetFork string
	Items      []PlanItem
}

// NewPlan 构造并校验计划：targetFork 非空；skip 项必须带原因；同一
// (Kind, ContentKey) 不得重复。NeedsConfirm 项必须已经用户拍板方可进入
// （由调用方 appsvc 保证）。
func NewPlan(targetFork string, items []PlanItem) (Plan, error) {
	return Plan{}, ErrNotImplemented
}
