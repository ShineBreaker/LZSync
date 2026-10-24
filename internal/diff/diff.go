// Package diff 是 hub 内对账查询（design.md §3.1/§4.4）：全部对比都是对
// hub.db 的 SQL 查询，绝不触碰任何 fork 库（§6.2）。产出四分类 + 冲突摘要
// + tombstone 恢复组；跨版本对账的谱面身份键配对缓存（beatmap_identity）
// 由 hub 维护，本包只消费其查询结果。
package diff

import (
	"context"
	"errors"

	"lzsync/internal/hub"
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("diff: 契约桩未实现")

// Category 对账四分类（§4.2：在 EZRealmSync 三分类上加第四类）。
type Category string

const (
	CategorySourceOnly    Category = "source-only"    // 源独有
	CategoryTargetOnly    Category = "target-only"    // 目标独有
	CategoryConflict      Category = "conflict"       // 内容键相等、非键字段不同
	CategoryVersionUpdate Category = "version-update" // 内容键不等、谱面身份键相等
)

// FieldDiff 冲突字段级对比。冲突字段只取内容键之外的字段（内容键本身
// 键匹配时必相等，比它是死代码，§4.2）。
type FieldDiff struct {
	Field     string
	HubValue  string // canonical 值（最近一次收集确立，origin_fork 指向它）
	ForkValue string // 目标 fork presence 值
}

// Evidence 谱面身份键证据强度（§4.2 第二层）。
type Evidence string

const (
	EvidenceStrong Evidence = "strong" // OnlineID 强键（同服务器）→ 默认按版本更新执行
	EvidenceWeak   Evidence = "weak"   // 元数据弱键「疑似」→ 需用户确认组，默认不勾选
)

// Entry 单条对账结果。
type Entry struct {
	Kind           hub.EntityKind
	ContentKey     string
	Category       Category
	Summary        string      // 人类可读摘要（曲名/难度名/日期/分数）
	ConflictFields []FieldDiff // Category == CategoryConflict 时填充
	IdentityKey    string      // 版本更新时的谱面身份键（online:/meta: 前缀）
	Evidence       Evidence    // 版本更新时的证据强度
}

// CompareForks fork↔fork 快照对比（§4.4）：两份 presence 的差集，谱面集按
// 身份键归并、取各自 presence 中实际存在的版本；字段取实体表规范值。
func CompareForks(ctx context.Context, h *hub.Store, source, target string, kinds []hub.EntityKind) ([]Entry, error) {
	return nil, ErrNotImplemented
}

// DiffHubAgainst hub 真相 → 单 fork 的投放预演差异（§4.4 对账语义）：
// 当前版本集合（superseded_by IS NULL）vs 目标 presence 差集；历史版本
// （superseded 行）已排除、永不进入计划。目标 presence 中的版本 ≠ hub
// 当前版本 → 版本更新分类。
func DiffHubAgainst(ctx context.Context, h *hub.Store, targetFork string, kinds []hub.EntityKind) ([]Entry, error) {
	return nil, ErrNotImplemented
}

// TombstoneHits 目标 fork 的 tombstone 命中项（HB2「恢复（已删除）」组，
// §6.2）：不进默认计划、默认不勾选、展示删除时间；用户显式勾选投放时
// 同时清除该 tombstone（§5.4 删除不复活）。
func TombstoneHits(ctx context.Context, h *hub.Store, targetFork string, kinds []hub.EntityKind) ([]Entry, error) {
	return nil, ErrNotImplemented
}
