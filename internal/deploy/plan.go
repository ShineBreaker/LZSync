// Package deploy 是投放方向编排（hub → fork，design.md §3.1 的 publish
// 模块、§3.2/§3.3）：计划 → 守卫 → 备份 → 建链接先行 → 批事务写 realm 行
// → DriftGuard → 结果报告。批 = 原子单位（S1 回滚承诺按批兑现）+ 进度
// 单位 + 取消单位。
package deploy

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("deploy: 契约桩未实现")

// Action 计划项动作。
type Action string

const (
	ActionCreate         Action = "create"          // 目标缺行 → 以 origin_guid 新建（§4.2）
	ActionUpdate         Action = "update"          // 内容键命中 → 就地更新白名单列（保目标 Guid）
	ActionVersionReplace Action = "version-replace" // 版本更新 → 整集原子替换三步（§5.4）
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
	// Restore 标记「恢复（已删除）」组的显式勾选项（目标 tombstone 命中，
	// HB2/§6.2）：默认不进计划；用户勾选投放成功后同时清除该 tombstone
	//（§5.4 删除不复活——确认动作即清除）。
	Restore bool
	// OldContentKey 版本替换专用（Action == ActionVersionReplace 时必填）：
	// 目标现场旧版本集的内容键——由对账按谱面身份键/版本链定位
	//（beatmap_identity / superseded_by 链，§4.4），执行期在现场按此键
	// 重寻址旧行（§3.3 现场复核）。ContentKey 则是 hub 链尾新版本键
	//（写入面 canonical payload 的读取键）。
	OldContentKey string
}

// Plan 投放计划：预览页勾选结果的快照（计划项按内容键幂等，§3.3——重跑
// 已提交项自然判为「已存在 → 跳过」，重续安全）。Target 是计划的目标库
// 解析结果（投放执行需要 RealmPath 落盘位置；骨架期的字符串 forkID 不足以
// 定位库文件）。
type Plan struct {
	TargetFork string
	Target     forks.LibraryRef
	Items      []PlanItem
}

// NewPlan 构造并校验计划：targetFork 非空；skip 项必须带原因；同一
// (Kind, ContentKey) 不得重复；Restore 项必须为 create 动作（恢复语义 =
// 重建整行）；version-replace 项必须携带旧版本键且与新键不同。NeedsConfirm
// 项必须已经用户拍板方可进入（由调用方 appsvc 保证）。
func NewPlan(targetFork string, items []PlanItem) (Plan, error) {
	if targetFork == "" {
		return Plan{}, errors.New("deploy: 计划缺少目标 fork")
	}
	seen := make(map[hub.EntityKind]map[string]bool, len(items))
	for _, it := range items {
		if it.ContentKey == "" {
			return Plan{}, fmt.Errorf("deploy: 计划项 %s 缺内容键", it.Kind)
		}
		if seen[it.Kind] == nil {
			seen[it.Kind] = map[string]bool{}
		}
		if seen[it.Kind][it.ContentKey] {
			return Plan{}, fmt.Errorf("deploy: 计划项重复 (%s, %s)", it.Kind, it.ContentKey)
		}
		seen[it.Kind][it.ContentKey] = true
		if it.Action == ActionSkip && it.SkipReason == "" {
			return Plan{}, fmt.Errorf("deploy: skip 项 (%s, %s) 必须携带原因", it.Kind, it.ContentKey)
		}
		if it.Restore && it.Action != ActionCreate {
			return Plan{}, fmt.Errorf("deploy: 恢复项 (%s, %s) 必须是 create 动作，got %s",
				it.Kind, it.ContentKey, it.Action)
		}
		if it.Action == ActionVersionReplace && (it.OldContentKey == "" || it.OldContentKey == it.ContentKey) {
			return Plan{}, fmt.Errorf("deploy: version-replace 项 (%s, %s) 缺旧版本键或新旧键相同",
				it.Kind, it.ContentKey)
		}
	}
	return Plan{TargetFork: targetFork, Items: items}, nil
}

// BuildOptions BuildPlan 的勾选输入（§6.2 默认勾选规则的用户侧自由度）。
type BuildOptions struct {
	// RestoreKeys 用户在「恢复（已删除）」组显式勾选的内容键：命中
	// tombstone 的 create 项默认不进计划（§5.4 删除不复活），出现在本名单
	// 才进入，且投放成功后同时清除对应 tombstone。
	RestoreKeys []string
	// ConfirmVersionReplaceKeys 元数据弱键「疑似」版本更新经用户拍板
	// 「替换」的 hub 链尾键（ReconcileItem.CurrentKey）：弱键项默认不进
	// 计划（NeedsConfirm 语义——§5.4 三选一：替换/并存/忽略），出现在本
	// 名单才以 version-replace 进入（「并存」走普通 create 链路，不经本名单）。
	ConfirmVersionReplaceKeys []string
}

// BuildPlan 从 hub 对账结果构建默认投放计划（§3.2 计划 / §6.2 默认勾选）：
// hub diff（ReconcileAgainst，当前版本差集）− 目标 tombstone 命中（恢复组
// 默认排除，仅 RestoreKeys 显式勾选者进入）− 需确认项。conflict → update
// （默认 hub 覆盖目标，§5.4 方向模型）。
//
// 施工面：收藏夹（M1，§5.1 最安全首投放路径）+ 谱面集（M2 create/update +
// 版本更新整集原子替换，§5.4 三步）。其他实体类型返回错误，由后续块放开。
func BuildPlan(ctx context.Context, h *hub.Store, target forks.LibraryRef, opts BuildOptions) (Plan, error) {
	if target.ForkID == "" {
		return Plan{}, errors.New("deploy: BuildPlan 缺目标 fork 身份")
	}
	if target.RealmPath == "" {
		return Plan{}, errors.New("deploy: BuildPlan 缺目标库路径（LibraryRef 未解析）")
	}
	items, err := h.ReconcileAgainst(ctx, target.ForkID, []hub.EntityKind{hub.KindCollection, hub.KindBeatmapSet})
	if err != nil {
		return Plan{}, err
	}
	restore := make(map[string]bool, len(opts.RestoreKeys))
	for _, k := range opts.RestoreKeys {
		restore[k] = true
	}
	confirmVersion := make(map[string]bool, len(opts.ConfirmVersionReplaceKeys))
	for _, k := range opts.ConfirmVersionReplaceKeys {
		confirmVersion[k] = true
	}
	var planItems []PlanItem
	for _, ri := range items {
		switch ri.Kind {
		case hub.KindCollection:
			switch ri.Category {
			case hub.ReconcileCreate:
				if ri.Restorable && !restore[ri.ContentKey] {
					continue // 恢复组默认排除（HB2：需用户显式勾选）
				}
				planItems = append(planItems, PlanItem{
					Kind:       ri.Kind,
					ContentKey: ri.ContentKey,
					Action:     ActionCreate,
					Summary:    collectionSummary(ri.ContentKey),
					Restore:    ri.Restorable,
				})
			case hub.ReconcileConflict:
				// 默认方向 = hub 覆盖目标（就地更新白名单列，保目标 Guid，§5.4）。
				planItems = append(planItems, PlanItem{
					Kind:       ri.Kind,
					ContentKey: ri.ContentKey,
					Action:     ActionUpdate,
					Summary:    collectionSummary(ri.ContentKey),
				})
			case hub.ReconcileVersionUpdate:
				// 收藏夹无版本链 / 谱面身份键，ReconcileAgainst 对 collection 不产
				// 此分类；防御性拒绝而非静默吞（§1.5-4）。
				return Plan{}, fmt.Errorf("deploy: 收藏夹对账出现 version-update 分类（键 %s），属工具缺陷请报告", ri.ContentKey)
			default:
				return Plan{}, fmt.Errorf("deploy: 未知对账分类 %q（键 %s）", ri.Category, ri.ContentKey)
			}
		case hub.KindBeatmapSet:
			switch ri.Category {
			case hub.ReconcileCreate:
				if ri.Restorable && !restore[ri.ContentKey] {
					continue // 恢复组默认排除（HB2：需用户显式勾选）
				}
				planItems = append(planItems, PlanItem{
					Kind:       ri.Kind,
					ContentKey: ri.ContentKey,
					Action:     ActionCreate,
					Summary:    beatmapSetSummary(ri.ContentKey),
					Restore:    ri.Restorable,
				})
			case hub.ReconcileConflict:
				planItems = append(planItems, PlanItem{
					Kind:       ri.Kind,
					ContentKey: ri.ContentKey,
					Action:     ActionUpdate,
					Summary:    beatmapSetSummary(ri.ContentKey),
				})
			case hub.ReconcileVersionUpdate:
				// 版本更新 = 整集原子替换（§5.4 三步）。OnlineID 强键默认
				// 执行；元数据弱键「疑似」默认不进计划——配对建议需用户
				// 三选一拍板（替换/并存/忽略，§5.4），显式确认（以 hub
				// 链尾键为锚）后按替换执行。
				switch ri.Evidence {
				case hub.EvidenceStrong:
				case hub.EvidenceWeak:
					if !confirmVersion[ri.CurrentKey] {
						continue
					}
				default:
					return Plan{}, fmt.Errorf("deploy: 未知证据强度 %q（键 %s），属工具缺陷请报告", ri.Evidence, ri.ContentKey)
				}
				planItems = append(planItems, PlanItem{
					Kind:          ri.Kind,
					ContentKey:    ri.CurrentKey,
					OldContentKey: ri.ContentKey,
					Action:        ActionVersionReplace,
					Summary:       versionReplaceSummary(ri.ContentKey, ri.CurrentKey),
				})
			default:
				return Plan{}, fmt.Errorf("deploy: 未知对账分类 %q（键 %s）", ri.Category, ri.ContentKey)
			}
		default:
			return Plan{}, fmt.Errorf("deploy: BuildPlan 不支持实体类型 %q（后续块扩面）", ri.Kind)
		}
	}
	plan, err := NewPlan(target.ForkID, planItems)
	if err != nil {
		return Plan{}, err
	}
	plan.Target = target
	return plan, nil
}

func collectionSummary(name string) string {
	return "收藏夹 " + strconv.Quote(name)
}

// beatmapSetSummary 谱面集摘要：对账面只携带内容键（hash），摘要取其
// 前缀（预览页的人类可读锚点；曲名等由 payload 查询面补充，§6.2）。
func beatmapSetSummary(contentHash string) string {
	return "谱面集 " + hashPrefix(contentHash) + "…"
}

// versionReplaceSummary 版本替换摘要：新旧内容键前缀对照（预览页锚点）。
func versionReplaceSummary(oldHash, newHash string) string {
	return "谱面集版本替换 " + hashPrefix(oldHash) + "… → " + hashPrefix(newHash) + "…"
}

func hashPrefix(contentHash string) string {
	prefix := contentHash
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return prefix
}
