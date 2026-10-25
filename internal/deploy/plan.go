// Package deploy 是投放方向编排（hub → fork，design.md §3.1 的 publish
// 模块、§3.2/§3.3）：计划 → 守卫 → 备份 → 建链接先行 → 批事务写 realm 行
// → DriftGuard → 结果报告。批 = 原子单位（S1 回滚承诺按批兑现）+ 进度
// 单位 + 取消单位。
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"lzsync/internal/filestore"
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
	ActionDelete         Action = "delete"          // 按内容键定位并删行（键位集合判定的副键移除等，§0.3）
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
	// Warning 非空 = 告警标记（动作照常执行，§5.4 R6：delta gimmick 谱面
	// 流出告警）。预演、执行报告与 UI 一致呈现；默认动作不变。
	Warning string
	// PayloadJSON 非空 = 裁决层给定的变体载荷（投影 DTO JSON，§0.3 三
	// 方判定决定值可以不是 canonical 首见值）：执行期以它替代 hub
	// canonical 行。空 = 按 ContentKey 读 canonical（旧行为）。
	PayloadJSON string
	// SourceFork 非空 = 决定值的来源端（§0.3 胜者端）：执行期 S8/D20
	// 等源端判定优先于 canonical origin_fork。空 = 用 origin_fork。
	SourceFork string
}

// Plan 投放计划：预览页勾选结果的快照（计划项按内容键幂等，§3.3——重跑
// 已提交项自然判为「已存在 → 跳过」，重续安全）。Target 是计划的目标库
// 解析结果（投放执行需要 RealmPath 落盘位置；骨架期的字符串 forkID 不足以
// 定位库文件）。
type Plan struct {
	TargetFork string
	Target     forks.LibraryRef
	Items      []PlanItem
	// Intersection 全局交集面（§0-B3/G6）：本计划写入的数据必须满足的全部
	// 目标端（含 TargetFork 自身，含一轮同步中因运行中被跳过的端）。执行期
	// 对 Intersection 中每端做静态兼容性判定——任一端拒绝，所有端都不写。
	// 空 = 仅 TargetFork（BuildPlan/手工计划的旧行为）。
	Intersection []string
}

// IntersectionRejectPrefix 全局交集排除的 skip 原因前缀（§0-B3/G6）：
// 目标端自身的拒绝保持原有原因文本；只有「因其他 peer 端不支持」被拒的
// 项以本前缀开头（appsvc 据此把该项计入「不支持」待处理计数）。
// 完整原因形态：「全局交集排除：<fork> 不支持 — <原规则原因>」。
const IntersectionRejectPrefix = "全局交集排除："

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
		if it.Action == ActionDelete && (it.OldContentKey != "" || it.Restore) {
			return Plan{}, fmt.Errorf("deploy: delete 项 (%s, %s) 不得携带旧版本键/恢复标记",
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
	// Files 内容寻址仓（gimmick 头扫描的只读依赖，R6）：计划含谱面集
	// 动作项时必填——缺仓 = 告警面缺失，BuildPlan 如实报错而非静默
	// 跳过检测（§1.5-4 不静默吞）。
	Files *filestore.Store
}

// BuildPlan 从 hub 对账结果构建默认投放计划（§3.2 计划 / §6.2 默认勾选）：
// hub diff（ReconcileAgainst，当前版本差集）− 目标 tombstone 命中（恢复组
// 默认排除，仅 RestoreKeys 显式勾选者进入）− 需确认项。conflict → skip
//（§0-B4 判不准不动：各端取值不同、无三方基线裁决（G7 待办），默认保留
// 差异不落地——用户在数据页显式勾选的冲突行仍走 appsvc planItemFrom 的
// update 手动处置路径）。
//
// 施工面：收藏夹（M1，§5.1 最安全首投放路径）+ 谱面集（M2 create/update +
// 版本更新整集原子替换，§5.4 三步）+ 皮肤（M2，§5.1）。其他实体类型返回
// 错误，由后续块放开。
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
	// 皮肤对账：hub.ReconcileAgainst 尚未放开 skin kind（块 1/2 未提供），
	// 此处以 hub.DB() 只读直查承载（loadBeatmapSet 的同构过渡先例，hub
	// 增补领域方法后应切换）；结果在主循环之后并入计划。
	skins, err := reconcileSkins(ctx, h, target.ForkID)
	if err != nil {
		return Plan{}, err
	}
	// M3 设置类三类对账（同上过渡承载）：key_binding / ruleset_setting /
	// mod_preset 全部行 vs 目标 presence。
	settings, err := ReconcileSettings(ctx, h, target.ForkID)
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
				// §0-B4 判不准不动：冲突项默认不落地（无三方基线裁决，G7
				// 待办）；用户在数据页显式勾选的冲突行走 appsvc 手动处置。
				planItems = append(planItems, PlanItem{
					Kind:       ri.Kind,
					ContentKey: ri.ContentKey,
					Action:     ActionSkip,
					Summary:    collectionSummary(ri.ContentKey),
					SkipReason: skipReasonConflict,
				})
			case hub.ReconcileVersionUpdate:
				// 收藏夹无版本链 / 谱面身份键，ReconcileAgainst 对 collection 不产
				// 此分类；防御性拒绝而非静默吞（§1.5-4）。
				return Plan{}, fmt.Errorf("deploy: 收藏夹对账出现 version-update 分类（键 %s），属工具缺陷请报告", ri.ContentKey)
			default:
				return Plan{}, fmt.Errorf("deploy: 未知对账分类 %q（键 %s）", ri.Category, ri.ContentKey)
			}
		case hub.KindBeatmapSet:
			// §5.4-D5 预演：外部托管集 Hash（路径编码）不进默认计划——
			// 与执行期 writer 防线共用 externalSetHash（§3.3 两处一致）。
			if externalSetHash(ri.ContentKey) {
				planItems = append(planItems, PlanItem{
					Kind:       ri.Kind,
					ContentKey: ri.ContentKey,
					Action:     ActionSkip,
					Summary:    beatmapSetSummary(ri.ContentKey),
					SkipReason: "外部托管集（Hash 为路径编码而非内容哈希，files/ 无字节），不投放（D5）",
				})
				continue
			}
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
					Action:     ActionSkip,
					Summary:    beatmapSetSummary(ri.ContentKey),
					SkipReason: skipReasonConflict,
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
	// 皮肤 → 计划项（create/conflict 两分类；皮肤无版本链与谱面身份键，
	// restore 语义与收藏夹一致——tombstone 命中默认排除，§6.2）。
	for _, sr := range skins {
		// §5.4-D21 预演：fork 专有皮肤指纹命中 → skip 项（与执行期
		// skinWriter 防线共用 forkOwnSkinHit，§3.3 两处一致）。
		if forkOwnSkinHit(forks.All(), sr.guid, sr.name, sr.creator) {
			planItems = append(planItems, PlanItem{
				Kind:       hub.KindSkin,
				ContentKey: sr.contentKey,
				Action:     ActionSkip,
				Summary:    skinSummary(sr.name),
				SkipReason: "皮肤命中 fork 专有皮肤指纹（OwnSkins），不跨端投放（D21）",
			})
			continue
		}
		switch sr.category {
		case hub.ReconcileCreate:
			if sr.restorable && !restore[sr.contentKey] {
				continue // 恢复组默认排除（HB2：需用户显式勾选）
			}
			planItems = append(planItems, PlanItem{
				Kind:       hub.KindSkin,
				ContentKey: sr.contentKey,
				Action:     ActionCreate,
				Summary:    skinSummary(sr.name),
				Restore:    sr.restorable,
			})
		case hub.ReconcileConflict:
			planItems = append(planItems, PlanItem{
				Kind:       hub.KindSkin,
				ContentKey: sr.contentKey,
				Action:     ActionSkip,
				Summary:    skinSummary(sr.name),
				SkipReason: skipReasonConflict,
			})
		default:
			return Plan{}, fmt.Errorf("deploy: 皮肤对账出现 %q 分类（键 %s），属工具缺陷请报告", sr.category, sr.contentKey)
		}
	}
	// M3 设置类三类 → 计划项（create/conflict 两分类；无版本链，restore
	// 语义与皮肤一致）。D20/D7/规则集可解析性不预演——执行期现场复核是
	// 唯一防线（域数据待激活态 + 目标库现场才可判定的规则集行）。
	for _, sr := range settings {
		switch sr.Category {
		case hub.ReconcileCreate:
			if sr.Restorable && !restore[sr.ContentKey] {
				continue // 恢复组默认排除（HB2：需用户显式勾选）
			}
			planItems = append(planItems, PlanItem{
				Kind:       sr.Kind,
				ContentKey: sr.ContentKey,
				Action:     ActionCreate,
				Summary:    sr.Summary,
				Restore:    sr.Restorable,
			})
		case hub.ReconcileConflict:
			planItems = append(planItems, PlanItem{
				Kind:       sr.Kind,
				ContentKey: sr.ContentKey,
				Action:     ActionSkip,
				Summary:    sr.Summary,
				SkipReason: skipReasonConflict,
			})
		default:
			return Plan{}, fmt.Errorf("deploy: %s 对账出现 %q 分类（键 %s），属工具缺陷请报告", sr.Kind, sr.Category, sr.ContentKey)
		}
	}
	// §5.4 R6 预演：delta gimmick 谱面流出告警——计划含谱面集动作项时
	// 扫描仓内 .osu section 头并标记 Warning（skip 项不投放不检测）；
	// 仓未注入 = 告警面缺失，如实报错（不静默吞，§1.5-4）。
	if planHasSetAction(planItems) {
		if opts.Files == nil {
			return Plan{}, errors.New("deploy: BuildOptions.Files 未配置（谱面集计划需要仓做 gimmick 头扫描，R6）")
		}
		if err := markGimmickWarnings(ctx, h, opts.Files, planItems); err != nil {
			return Plan{}, err
		}
	}
	plan, err := NewPlan(target.ForkID, planItems)
	if err != nil {
		return Plan{}, err
	}
	plan.Target = target
	return plan, nil
}

// skipReasonConflict 冲突项的默认跳过原因（§0-B4 判不准不动：各端取值
// 不同、无三方基线裁决时保留差异）。
const skipReasonConflict = "冲突：各端取值不同，保留差异（§0 B4）"

// intersectionPeers 计划的全局交集端清单（§0-B3/G6）：Intersection 为空
// 时仅 TargetFork（旧行为）。
func intersectionPeers(plan Plan) []string {
	if len(plan.Intersection) == 0 {
		return []string{plan.TargetFork}
	}
	return plan.Intersection
}

// planHasSetAction 计划是否含谱面集动作项（gimmick 检测的触发条件）。
func planHasSetAction(items []PlanItem) bool {
	for _, it := range items {
		if it.Kind == hub.KindBeatmapSet && it.Action != ActionSkip {
			return true
		}
	}
	return false
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

// skinReconcile 皮肤对账行（deploy 内过渡承载：hub.ReconcileAgainst 未
// 放开 skin kind，hub.ReconcileItem 亦无摘要字段——皮肤摘要取 Name 而非
// 内容键前缀，故自带 name；hub 增补领域方法后应切换）。
type skinReconcile struct {
	contentKey string
	name       string
	creator    string
	guid       string // 行 Guid 的 hex（OwnSkins 指纹判定输入，D21）
	category   hub.ReconcileCategory
	restorable bool
}

// reconcileSkins 皮肤对账（hub 真相 → 目标 fork，纯 SQL 只读）：skin 表
// 全部行（皮肤无版本链）vs 目标 presence 差集 → create；命中 tombstone 的
// 归恢复组（默认排除，§5.4 删除不复活）；presence 有该键则比冲突字段
// Name/Creator（§4.2：Skin 比 Name/Creator，内容键 Hash 本身不比），有
// 差异 → conflict（hub 覆盖目标）。
func reconcileSkins(ctx context.Context, h *hub.Store, forkID string) ([]skinReconcile, error) {
	rows, err := h.DB().QueryContext(ctx,
		`SELECT content_hash, payload FROM skin ORDER BY content_hash`)
	if err != nil {
		return nil, fmt.Errorf("deploy: 读皮肤 canonical: %w", err)
	}
	type canon struct {
		key, name, creator, guid, payload string
	}
	var canonRows []canon
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, fmt.Errorf("deploy: 读皮肤 canonical 行: %w", err)
		}
		var sk hub.Skin
		if err := json.Unmarshal([]byte(v), &sk); err != nil {
			rows.Close()
			return nil, fmt.Errorf("deploy: 解码皮肤 %q payload: %w", k, err)
		}
		canonRows = append(canonRows, canon{k, sk.Name, sk.Creator, sk.ID, v})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("deploy: 遍历皮肤 canonical: %w", err)
	}
	rows.Close()

	presRows, err := h.DB().QueryContext(ctx,
		`SELECT content_key, local_values FROM presence WHERE fork_id=? AND kind=?`, forkID, hub.KindSkin)
	if err != nil {
		return nil, fmt.Errorf("deploy: 读皮肤 presence: %w", err)
	}
	pres := map[string]string{}
	for presRows.Next() {
		var k, v string
		if err := presRows.Scan(&k, &v); err != nil {
			presRows.Close()
			return nil, fmt.Errorf("deploy: 读皮肤 presence 行: %w", err)
		}
		pres[k] = v
	}
	if err := presRows.Err(); err != nil {
		presRows.Close()
		return nil, fmt.Errorf("deploy: 遍历皮肤 presence: %w", err)
	}
	presRows.Close()

	tombs, err := h.Tombstones(ctx, forkID)
	if err != nil {
		return nil, err
	}
	tombKeys := tombs[hub.KindSkin]

	var out []skinReconcile
	for _, c := range canonRows {
		local, inPres := pres[c.key]
		if !inPres {
			it := skinReconcile{contentKey: c.key, name: c.name, creator: c.creator, guid: c.guid, category: hub.ReconcileCreate}
			if _, dead := tombKeys[c.key]; dead {
				it.restorable = true
			}
			out = append(out, it)
			continue
		}
		var hubSk, forkSk hub.Skin
		if err := json.Unmarshal([]byte(c.payload), &hubSk); err != nil {
			return nil, fmt.Errorf("deploy: 解码皮肤 %q canonical: %w", c.key, err)
		}
		if err := json.Unmarshal([]byte(local), &forkSk); err != nil {
			return nil, fmt.Errorf("deploy: 解码皮肤 %q presence: %w", c.key, err)
		}
		// 冲突判据 §4.2：Name/Creator；一致则无动作不产出。
		if hubSk.Name != forkSk.Name || hubSk.Creator != forkSk.Creator {
			out = append(out, skinReconcile{contentKey: c.key, name: hubSk.Name, category: hub.ReconcileConflict})
		}
	}
	return out, nil
}

func skinSummary(name string) string {
	return "皮肤 " + strconv.Quote(name)
}

// SettingsReconcile M3 设置类对账行（deploy 内过渡承载：与 skinReconcile
// 同构；三类共用的形态）。appsvc 预演/计划面经 ReconcileSettings 消费——
// 预览页三类行与全量 BuildPlan 共源（§6.2）。 
type SettingsReconcile struct {
	Kind       hub.EntityKind
	ContentKey string
	Summary    string
	Category   hub.ReconcileCategory
	Restorable bool
	DeletedAt  int64 // Restorable 时的删除时间（Unix 毫秒）
}

// reconcileSettings M3 设置类三类对账（hub 真相 → 目标 fork，纯 SQL 只读，
// reconcileSkins 的同款过渡承载）：三类全部行（无版本链）vs 目标 presence
// 差集 → create（命中 tombstone 归恢复组）；presence 有键则比冲突字段
// （§4.2：键位比 KeyCombination、设置比 Value、预设比 Description/ModsJson
// ——内容键段本身不比），有差异 → conflict。
// 导出供 appsvc 预演/计划面消费（deploy 全量 BuildPlan 同源）。
func ReconcileSettings(ctx context.Context, h *hub.Store, forkID string) ([]SettingsReconcile, error) {
	presOf := func(kind hub.EntityKind) (map[string]string, error) {
		rows, err := h.DB().QueryContext(ctx,
			`SELECT content_key, local_values FROM presence WHERE fork_id=? AND kind=?`, forkID, kind)
		if err != nil {
			return nil, fmt.Errorf("deploy: 读 presence(%s): %w", kind, err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return nil, fmt.Errorf("deploy: 读 presence(%s) 行: %w", kind, err)
			}
			out[k] = v
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("deploy: 遍历 presence(%s): %w", kind, err)
		}
		return out, nil
	}
	tombs, err := h.Tombstones(ctx, forkID)
	if err != nil {
		return nil, err
	}

	var out []SettingsReconcile
	// 每类：键提取（canonical 行 → 编码内容键 + 摘要）+ 冲突判据。
	type spec struct {
		kind     hub.EntityKind
		query    string                                       // 取全部 canonical 行（content_key 列 + payload 列）
		keyOf    func(payload string) (string, string, error) // 编码键 + 摘要
		conflict func(hubJSON, forkJSON string) (bool, error)
	}
	specs := []spec{
		{hub.KindKeyBinding,
			`SELECT payload FROM key_binding`,
			func(p string) (string, string, error) {
				var kb hub.KeyBinding
				if err := json.Unmarshal([]byte(p), &kb); err != nil {
					return "", "", err
				}
				return hub.KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination),
					keyBindingSummary(kb), nil
			},
			func(a, b string) (bool, error) {
				var x, y hub.KeyBinding
				if err := decodeSettingPair(a, b, &x, &y); err != nil {
					return false, err
				}
				// KeyCombination 入内容键后同键必等值，此比对退化为防御
				//（键位行的全部非 Guid 列都在身份里，update 语义已空）。
				return x.KeyCombination != y.KeyCombination, nil
			}},
		{hub.KindRulesetSetting,
			`SELECT payload FROM ruleset_setting`,
			func(p string) (string, string, error) {
				var rs hub.RulesetSetting
				if err := json.Unmarshal([]byte(p), &rs); err != nil {
					return "", "", err
				}
				return hub.RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key),
					rulesetSettingSummary(rs), nil
			},
			func(a, b string) (bool, error) {
				var x, y hub.RulesetSetting
				if err := decodeSettingPair(a, b, &x, &y); err != nil {
					return false, err
				}
				return x.Value != y.Value, nil
			}},
		{hub.KindModPreset,
			`SELECT payload FROM mod_preset`,
			func(p string) (string, string, error) {
				var mp hub.ModPreset
				if err := json.Unmarshal([]byte(p), &mp); err != nil {
					return "", "", err
				}
				return hub.ModPresetContentKey(mp.RulesetShortName, mp.Name),
					modPresetSummary(mp), nil
			},
			func(a, b string) (bool, error) {
				var x, y hub.ModPreset
				if err := decodeSettingPair(a, b, &x, &y); err != nil {
					return false, err
				}
				return x.Description != y.Description || x.ModsJson != y.ModsJson, nil
			}},
	}
	for _, sp := range specs {
		pres, err := presOf(sp.kind)
		if err != nil {
			return nil, err
		}
		tombKeys := tombs[sp.kind]
		rows, err := h.DB().QueryContext(ctx, sp.query)
		if err != nil {
			return nil, fmt.Errorf("deploy: 读 %s canonical: %w", sp.kind, err)
		}
		type canon struct{ key, payload, summary string }
		var canonRows []canon
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, fmt.Errorf("deploy: 读 %s canonical 行: %w", sp.kind, err)
			}
			key, summary, kErr := sp.keyOf(v)
			if kErr != nil {
				rows.Close()
				return nil, fmt.Errorf("deploy: 解码 %s payload: %w", sp.kind, kErr)
			}
			canonRows = append(canonRows, canon{key, v, summary})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("deploy: 遍历 %s canonical: %w", sp.kind, err)
		}
		rows.Close()

		for _, c := range canonRows {
			local, inPres := pres[c.key]
			if !inPres {
				it := SettingsReconcile{Kind: sp.kind, ContentKey: c.key, Summary: c.summary, Category: hub.ReconcileCreate}
				if deadAt, dead := tombKeys[c.key]; dead {
					it.Restorable = true
					it.DeletedAt = deadAt
				}
				out = append(out, it)
				continue
			}
			conflict, err := sp.conflict(c.payload, local)
			if err != nil {
				return nil, err
			}
			if conflict {
				out = append(out, SettingsReconcile{Kind: sp.kind, ContentKey: c.key, Summary: c.summary, Category: hub.ReconcileConflict})
			}
		}
	}
	return out, nil
}

// decodeSettingPair 解码 canonical payload 与 presence local_values（同构
// DTO JSON；hub.decodeBoth 的 deploy 侧同款）。
func decodeSettingPair(canonJSON, localJSON string, canon, fork any) error {
	if err := json.Unmarshal([]byte(canonJSON), canon); err != nil {
		return fmt.Errorf("解码 canonical: %w", err)
	}
	if err := json.Unmarshal([]byte(localJSON), fork); err != nil {
		return fmt.Errorf("解码 presence: %w", err)
	}
	return nil
}

// keyBindingSummary 键位摘要（预览页人类可读锚点）。
func keyBindingSummary(kb hub.KeyBinding) string {
	scope := "全局"
	if kb.RulesetName != nil && *kb.RulesetName != "" {
		scope = *kb.RulesetName
		if kb.Variant != nil {
			scope += fmt.Sprintf(" %dK", *kb.Variant)
		}
	}
	return "键位 " + scope + " 动作 " + strconv.Itoa(kb.ActionInt) + " = " + kb.KeyCombination
}

// rulesetSettingSummary 规则集设置摘要。
func rulesetSettingSummary(rs hub.RulesetSetting) string {
	return "规则集设置 " + rs.RulesetName + " · " + rs.Key + " = " + rs.Value
}

// modPresetSummary 模组预设摘要。
func modPresetSummary(mp hub.ModPreset) string {
	return "模组预设 " + strconv.Quote(mp.Name) + "（" + mp.RulesetShortName + "）"
}
