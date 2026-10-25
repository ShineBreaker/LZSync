package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Snapshot 是一次收集投影的完整入仓载荷（§3.2 收集步骤②）。
// 「投影完整成功」是生成 tombstone 的前置条件：部分失败/中断的收集不得
// 构造本结构落库（防误标删除，HB2）。
//
// Kinds 圈定本次快照覆盖的实体类（nil = 全部七类）：非 nil 时未包含的
// kind 完全不受触碰——不插实体行、不重写 presence、不产生/清除
// tombstone（§0 板块开关：关掉一个板块不得把该 kind 的既有 presence
// 误标成「消失」）。注意 beatmap 无独立 presence（难度生命周期挂父集）；
// presence 侧按七个持有 kind 判定。
//
// PresenceOnly = true 时跳过实体插入与身份/版本链维护（效果 1/4），只写
// presence/tombstone/collect_log——「仅目标」端收集（§0 角色模型）：
// 目标端的取值留在 presence.local_values，canonical 实体由来源端确立。
type Snapshot struct {
	ForkID      string
	CollectedAt time.Time
	Collections []Collection
	BeatmapSets []BeatmapSet
	Skins       []Skin  // v2：M2 皮肤实体（Hash 为空的行由投影端跳过，§4.2）
	Scores      []Score // v2：M2 成绩实体（组合内容键见 ScoreContentKey）
	// v3：M3 设置类实体（§5.1-M3）。键位/规则集设置全收（hub 保留原文，
	// 目标端兼容过滤在投放侧做，D7/D20 同款哲学）；ModPreset 软删行由
	// 投影端跳过（S4）。
	KeyBindings     []KeyBinding
	RulesetSettings []RulesetSetting
	ModPresets      []ModPreset
	Kinds           map[EntityKind]bool // nil = 全部（见上）
	PresenceOnly    bool                // 只写 presence/tombstone，不插实体
	Skips           []SkipStat          // 投影期跳过（进 collect_log）
}

// ScoreContentKey 把成绩组合内容键 (BeatmapHash, Hash)（§4.2 第一层）
// 编码为 presence/tombstone 的单列内容键。两段均为 SHA-256 hex（字符集
// [0-9a-f]、定长 64，不含分隔符 "|"），编码无歧义；这是组合键的规范
// 序列化约定，解析侧按首个 "|" 拆分。
func ScoreContentKey(beatmapHash, hash string) string {
	return beatmapHash + "|" + hash
}

// keyBindingVariantNull 是 key_binding 表 Variant 列的归一化哨兵
// （realm 侧 int? 的 null = 无变体；真实域是 mania 键数等非负值，-1 不碰撞）。
const keyBindingVariantNull = -1

// encodeContentKey 把组合内容键段编码为单列内容键（presence / tombstone /
// 投放计划项的统一键形）。JSON 数组编码：段序固定、null 保真、任意字符串
// 无歧义——ScoreContentKey 的 "|" 拼接依赖两段均为定长 hex 的前提，设置类
// 实体的键段是任意字符串（用户命名的 ModPreset/设置 Key），拼接会被
// 分隔符碰撞破坏，故取 JSON。
func encodeContentKey(parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		// 数组装 string/int/指针，Marshal 不可能失败；保守返回空串由
		// 调用方的唯一性前提兜底（空串键不与任何合法编码冲突）。
		return ""
	}
	return string(b)
}

// KeyBindingContentKey 键位组合内容键 (RulesetName, Variant, ActionInt,
// KeyCombination)（§4.2 第一层）的单列编码；nil 段保真为 null（全局键位/
// 无变体）。KeyCombination 段是 2026-10-25 真实库实测修订：lazer 一个
// action 允许多行多键（默认键位大量一动作双键；真库探针 495 行塌缩成
// 311 个三段键、183 组同键不同键组合、0 组全同重复），三段键会把副键位
// 行误判为重复而吞掉。
func KeyBindingContentKey(rulesetName *string, variant *int, actionInt int, keyCombination string) string {
	return encodeContentKey(rulesetName, variant, actionInt, keyCombination)
}

// RulesetSettingContentKey 规则集设置组合内容键 (RulesetName, Variant,
// Key)（§4.2 第一层）的单列编码。
func RulesetSettingContentKey(rulesetName string, variant int, key string) string {
	return encodeContentKey(rulesetName, variant, key)
}

// ModPresetContentKey 模组预设业务键 (RulesetShortName, Name)（§4.2：
// 无内容哈希取业务键）的单列编码。
func ModPresetContentKey(rulesetShortName, name string) string {
	return encodeContentKey(rulesetShortName, name)
}

// KeyBindingIdentityKey 键位三段同步身份键 (RulesetName, Variant, ActionInt)
// 的单列编码（§0.3 判定粒度——与内容键同编码域、少 KeyCombination 段；
// 判定层以它聚合一动作的全部键位组合行）。
func KeyBindingIdentityKey(rulesetName *string, variant *int, actionInt int) string {
	return encodeContentKey(rulesetName, variant, actionInt)
}

// DecodeKeyBindingContentKey 是 KeyBindingContentKey 的导出解析面（判定
// 层由四段内容键还原身份段）。
func DecodeKeyBindingContentKey(key string) (rulesetName *string, variant *int, actionInt int, keyCombination string, err error) {
	return decodeKeyBindingContentKey(key)
}

// decodeKeyBindingContentKey 是 KeyBindingContentKey 的解析侧（Load 按单列
// 键取行时拆回复合主键参数；nil 段还原 null 语义）。
func decodeKeyBindingContentKey(key string) (rulesetName *string, variant *int, actionInt int, keyCombination string, err error) {
	var parts [4]json.RawMessage
	if dErr := json.Unmarshal([]byte(key), &parts); dErr != nil {
		return nil, nil, 0, "", fmt.Errorf("hub: 非法的键位内容键 %q: %w", key, dErr)
	}
	if dErr := json.Unmarshal(parts[0], &rulesetName); dErr != nil {
		return nil, nil, 0, "", fmt.Errorf("hub: 键位内容键 %q 的 RulesetName 段非法: %w", key, dErr)
	}
	if dErr := json.Unmarshal(parts[1], &variant); dErr != nil {
		return nil, nil, 0, "", fmt.Errorf("hub: 键位内容键 %q 的 Variant 段非法: %w", key, dErr)
	}
	if dErr := json.Unmarshal(parts[2], &actionInt); dErr != nil {
		return nil, nil, 0, "", fmt.Errorf("hub: 键位内容键 %q 的 ActionInt 段非法: %w", key, dErr)
	}
	if dErr := json.Unmarshal(parts[3], &keyCombination); dErr != nil {
		return nil, nil, 0, "", fmt.Errorf("hub: 键位内容键 %q 的 KeyCombination 段非法: %w", key, dErr)
	}
	return rulesetName, variant, actionInt, keyCombination, nil
}

// ApplyResult 是 ApplySnapshot 的执行摘要。
type ApplyResult struct {
	EntitiesWritten     map[EntityKind]int
	TombstonesAdded     int
	TombstonesCleared   int
	VersionLinksCreated int // 新维护的 superseded_by 指针数（HB1）
}

// ApplySnapshot 以单次 SQLite 事务应用一次收集快照（§3.3 收集方向原子性），
// 五个效果要么完整生效要么整体不生效：
//  1. 实体 insert-if-absent：canonical payload 与 origin_guid/origin_fork
//     首见即定（§0-B2；HB3 的整行覆盖已废止——后到端的取值留在该端
//     presence.local_values，由对账面判定）；
//  2. presence 按 fork 整段重写（快照语义，幂等；Snapshot.Kinds 非 nil 时
//     只重写圈定 kind）；
//  3. 与上次快照差集生成 tombstone——被版本链取代的消失键除外（HB2）；
//  4. 经 beatmap_identity 配对维护版本链 superseded_by（HB1，先于差集
//     判定执行，使「被取代的消失」可识别）；
//  5. 写 collect_log 审计行。
func (s *Store) ApplySnapshot(ctx context.Context, snap Snapshot) (ApplyResult, error) {
	res := ApplyResult{EntitiesWritten: map[EntityKind]int{}}
	if snap.ForkID == "" {
		return res, fmt.Errorf("hub: 快照缺少 fork_id")
	}
	now := snap.CollectedAt.UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("hub: 开收集事务: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // 回滚失败只能泄漏至连接关闭
		}
	}()

	// included 圈定实体写入与 presence 重写的 kind 面（Kinds 非 nil 时）。
	included := func(k EntityKind) bool {
		return snap.Kinds == nil || snap.Kinds[k]
	}

	if !snap.PresenceOnly {
		// 效果 1 + 4：实体 insert-if-absent 与身份/版本链维护（HB1、§0-B2）。
		// EntitiesWritten 只计实际新插入的行（ON CONFLICT DO NOTHING 的
		// RowsAffected）——首见即定后重复收集自然计 0。
		if included(KindCollection) {
			for i := range snap.Collections {
				ins, err := insertCollection(ctx, tx, &snap.Collections[i], snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if ins {
					res.EntitiesWritten[KindCollection]++
				}
			}
		}
		if included(KindBeatmapSet) {
			for i := range snap.BeatmapSets {
				bs := &snap.BeatmapSets[i]
				ins, err := insertBeatmapSet(ctx, tx, bs, snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if included(KindBeatmap) {
					for j := range bs.Beatmaps {
						bmIns, err := insertBeatmap(ctx, tx, &bs.Beatmaps[j], bs.Hash, now)
						if err != nil {
							return res, err
						}
						if bmIns {
							res.EntitiesWritten[KindBeatmap]++
						}
					}
				}
				if ins {
					res.EntitiesWritten[KindBeatmapSet]++
				}
				links, err := maintainIdentity(ctx, tx, bs, now)
				if err != nil {
					return res, err
				}
				res.VersionLinksCreated += int(links)
			}
		}
		if included(KindSkin) {
			for i := range snap.Skins {
				ins, err := insertSkin(ctx, tx, &snap.Skins[i], snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if ins {
					res.EntitiesWritten[KindSkin]++
				}
			}
		}
		if included(KindScore) {
			for i := range snap.Scores {
				ins, err := insertScore(ctx, tx, &snap.Scores[i], snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if ins {
					res.EntitiesWritten[KindScore]++
				}
			}
		}
		if included(KindKeyBinding) {
			for i := range snap.KeyBindings {
				ins, err := insertKeyBinding(ctx, tx, &snap.KeyBindings[i], snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if ins {
					res.EntitiesWritten[KindKeyBinding]++
				}
			}
		}
		if included(KindRulesetSetting) {
			for i := range snap.RulesetSettings {
				ins, err := insertRulesetSetting(ctx, tx, &snap.RulesetSettings[i], snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if ins {
					res.EntitiesWritten[KindRulesetSetting]++
				}
			}
		}
		if included(KindModPreset) {
			for i := range snap.ModPresets {
				ins, err := insertModPreset(ctx, tx, &snap.ModPresets[i], snap.ForkID, now)
				if err != nil {
					return res, err
				}
				if ins {
					res.EntitiesWritten[KindModPreset]++
				}
			}
		}
	}

	// 效果 2 + 3：presence 整段重写 + 差集 tombstone（HB2；Kinds 圈定）。
	added, cleared, err := rewritePresence(ctx, tx, &snap, now)
	if err != nil {
		return res, err
	}
	res.TombstonesAdded, res.TombstonesCleared = added, cleared

	// 效果 5：collect_log 审计行。
	countsJSON, err := json.Marshal(res.EntitiesWritten)
	if err != nil {
		return res, fmt.Errorf("hub: 序列化 entity_counts: %w", err)
	}
	skipsJSON, err := json.Marshal(snap.Skips)
	if err != nil {
		return res, fmt.Errorf("hub: 序列化 skip_reasons: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO collect_log(fork_id, finished_at, entity_counts, skip_reasons, tombstone_added)
VALUES(?,?,?,?,?)`, snap.ForkID, now, string(countsJSON), string(skipsJSON), res.TombstonesAdded); err != nil {
		return res, fmt.Errorf("hub: 写 collect_log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("hub: 提交收集事务: %w", err)
	}
	committed = true
	return res, nil
}

// insertCollection 收藏夹实体行（内容键 = Name）。§0-B2：payload 与
// origin_guid/origin_fork 首见即定，同键已存在即不动（HB3 整行覆盖已
// 废止——后到端的取值留在该端 presence.local_values，由对账面判定）。
// 返回值 = 是否实际插入。
func insertCollection(ctx context.Context, tx *sql.Tx, c *Collection, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化收藏 %q: %w", c.Name, err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO collection(content_key, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(content_key) DO NOTHING`,
		c.Name, string(payload), c.ID, fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入收藏 %q: %w", c.Name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计收藏 %q 插入: %w", c.Name, err)
	}
	return n > 0, nil
}

// insertBeatmapSet 谱面集实体行（内容键 = Hash）。同 §0-B2 语义；
// superseded_by 属版本链状态，插入恒 NULL（历史行永久保留）。
func insertBeatmapSet(ctx context.Context, tx *sql.Tx, bs *BeatmapSet, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(bs)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化谱面集 %q: %w", bs.Hash, err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO beatmap_set(content_hash, payload, origin_guid, origin_fork, superseded_by, first_seen, updated_at)
VALUES(?,?,?,?,NULL,?,?)
ON CONFLICT(content_hash) DO NOTHING`,
		bs.Hash, string(payload), bs.ID, fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入谱面集 %q: %w", bs.Hash, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计谱面集 %q 插入: %w", bs.Hash, err)
	}
	return n > 0, nil
}

// insertBeatmap 难度行（内容键 = Hash，随父集维护 set_hash）。同 §0-B2
// 语义；难度不做 presence / 版本链：整集按一个版本处理（§4.2 第二层末段）。
func insertBeatmap(ctx context.Context, tx *sql.Tx, bm *Beatmap, setHash string, now int64) (bool, error) {
	payload, err := json.Marshal(bm)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化难度 %q: %w", bm.Hash, err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO beatmap(content_hash, set_hash, payload, first_seen, updated_at)
VALUES(?,?,?,?,?)
ON CONFLICT(content_hash) DO NOTHING`,
		bm.Hash, setHash, string(payload), now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入难度 %q: %w", bm.Hash, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计难度 %q 插入: %w", bm.Hash, err)
	}
	return n > 0, nil
}

// insertSkin 皮肤实体行（内容键 = Hash，v2）。同 §0-B2 语义。
func insertSkin(ctx context.Context, tx *sql.Tx, sk *Skin, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(sk)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化皮肤 %q: %w", sk.Hash, err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO skin(content_hash, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(content_hash) DO NOTHING`,
		sk.Hash, string(payload), sk.ID, fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入皮肤 %q: %w", sk.Hash, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计皮肤 %q 插入: %w", sk.Hash, err)
	}
	return n > 0, nil
}

// insertScore 成绩实体行（组合内容键 = (BeatmapHash, Hash)，v2）。同
// §0-B2 语义；皮肤/成绩无版本链语义（superseded_by 是 beatmap_set 专属，
// HB1）。
func insertScore(ctx context.Context, tx *sql.Tx, sc *Score, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(sc)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化成绩 %q: %w", ScoreContentKey(sc.BeatmapHash, sc.Hash), err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO score(beatmap_hash, content_hash, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(beatmap_hash, content_hash) DO NOTHING`,
		sc.BeatmapHash, sc.Hash, string(payload), sc.ID, fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入成绩 %q: %w", ScoreContentKey(sc.BeatmapHash, sc.Hash), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计成绩 %q 插入: %w", ScoreContentKey(sc.BeatmapHash, sc.Hash), err)
	}
	return n > 0, nil
}

// insertKeyBinding 键位实体行（v3，组合内容键 = (RulesetName, Variant,
// ActionInt, KeyCombination)；NULL 语义段归一化哨兵见 ddlV3 注释）。
// 同 §0-B2 语义。
func insertKeyBinding(ctx context.Context, tx *sql.Tx, kb *KeyBinding, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(kb)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化键位 %q: %w", KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination), err)
	}
	rulesetName, variant := keyBindingKeyCols(kb.RulesetName, kb.Variant)
	res, err := tx.ExecContext(ctx, `
INSERT INTO key_binding(ruleset_name, variant, action_int, key_combination, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(ruleset_name, variant, action_int, key_combination) DO NOTHING`,
		rulesetName, variant, kb.ActionInt, kb.KeyCombination, string(payload), kb.ID, fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入键位 %q: %w", KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计键位 %q 插入: %w", KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination), err)
	}
	return n > 0, nil
}

// keyBindingKeyCols 键位可空键段的表列归一化（nil → ”/-1 哨兵）。
func keyBindingKeyCols(rulesetName *string, variant *int) (string, int64) {
	name := ""
	if rulesetName != nil {
		name = *rulesetName
	}
	v := int64(keyBindingVariantNull)
	if variant != nil {
		v = int64(*variant)
	}
	return name, v
}

// insertRulesetSetting 规则集设置实体行（v3，组合内容键 = (RulesetName,
// Variant, Key)；realm 侧无 Guid 列，origin_guid 不设）。同 §0-B2 语义。
func insertRulesetSetting(ctx context.Context, tx *sql.Tx, rs *RulesetSetting, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(rs)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化规则集设置 %q: %w", RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key), err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO ruleset_setting(ruleset_name, variant, key, payload, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(ruleset_name, variant, key) DO NOTHING`,
		rs.RulesetName, rs.Variant, rs.Key, string(payload), fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入规则集设置 %q: %w", RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计规则集设置 %q 插入: %w", RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key), err)
	}
	return n > 0, nil
}

// insertModPreset 模组预设实体行（v3，业务键 = (RulesetShortName, Name)，
// §4.2 无内容哈希取业务键）。同 §0-B2 语义。
func insertModPreset(ctx context.Context, tx *sql.Tx, mp *ModPreset, fork string, now int64) (bool, error) {
	payload, err := json.Marshal(mp)
	if err != nil {
		return false, fmt.Errorf("hub: 序列化模组预设 %q: %w", ModPresetContentKey(mp.RulesetShortName, mp.Name), err)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO mod_preset(ruleset_short_name, name, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(ruleset_short_name, name) DO NOTHING`,
		mp.RulesetShortName, mp.Name, string(payload), mp.ID, fork, now, now)
	if err != nil {
		return false, fmt.Errorf("hub: 插入模组预设 %q: %w", ModPresetContentKey(mp.RulesetShortName, mp.Name), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub: 计模组预设 %q 插入: %w", ModPresetContentKey(mp.RulesetShortName, mp.Name), err)
	}
	return n > 0, nil
}

// rewritePresence 按 fork 整段重写各实体类 presence（快照语义，幂等），
// 并就上次快照差集生成 tombstone（HB2）。防误标三道闸：
//   - 「投影完整成功才落库」由 Snapshot 构造方保证（见 Snapshot 注释）；
//   - 消失键已被版本链取代（链尾仍在本次快照中）→ 不算删除（仅
//     beatmap_set 有版本链，skin/score 按普通差集）；
//   - 本次出现的键命中既有 tombstone → 即刻清除（同键重现）。
//
// 必须在版本链维护之后调用（链状态参与差集判定）。
func rewritePresence(ctx context.Context, tx *sql.Tx, snap *Snapshot, now int64) (added, cleared int, err error) {
	type kindPayload struct {
		kind  EntityKind
		keys  []string
		local map[string]string // 内容键 → fork 侧投影 JSON（local_values）
	}
	newPlan := func(kind EntityKind, n int) kindPayload {
		return kindPayload{kind: kind, keys: make([]string, 0, n), local: make(map[string]string, n)}
	}
	plans := []kindPayload{
		newPlan(KindCollection, len(snap.Collections)),
		newPlan(KindBeatmapSet, len(snap.BeatmapSets)),
		newPlan(KindSkin, len(snap.Skins)),
		newPlan(KindScore, len(snap.Scores)),
		newPlan(KindKeyBinding, len(snap.KeyBindings)),
		newPlan(KindRulesetSetting, len(snap.RulesetSettings)),
		newPlan(KindModPreset, len(snap.ModPresets)),
	}
	appendPlan := func(p *kindPayload, key string, v any) error {
		j, mErr := json.Marshal(v)
		if mErr != nil {
			return fmt.Errorf("hub: 序列化 %s %q presence: %w", p.kind, key, mErr)
		}
		p.keys = append(p.keys, key)
		p.local[key] = string(j)
		return nil
	}
	for i := range snap.Collections {
		c := &snap.Collections[i]
		if aErr := appendPlan(&plans[0], c.Name, c); aErr != nil {
			return 0, 0, aErr
		}
	}
	for i := range snap.BeatmapSets {
		bs := &snap.BeatmapSets[i]
		if aErr := appendPlan(&plans[1], bs.Hash, bs); aErr != nil {
			return 0, 0, aErr
		}
	}
	for i := range snap.Skins {
		sk := &snap.Skins[i]
		if aErr := appendPlan(&plans[2], sk.Hash, sk); aErr != nil {
			return 0, 0, aErr
		}
	}
	for i := range snap.Scores {
		sc := &snap.Scores[i]
		if aErr := appendPlan(&plans[3], ScoreContentKey(sc.BeatmapHash, sc.Hash), sc); aErr != nil {
			return 0, 0, aErr
		}
	}
	for i := range snap.KeyBindings {
		kb := &snap.KeyBindings[i]
		if aErr := appendPlan(&plans[4], KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination), kb); aErr != nil {
			return 0, 0, aErr
		}
	}
	for i := range snap.RulesetSettings {
		rs := &snap.RulesetSettings[i]
		if aErr := appendPlan(&plans[5], RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key), rs); aErr != nil {
			return 0, 0, aErr
		}
	}
	for i := range snap.ModPresets {
		mp := &snap.ModPresets[i]
		if aErr := appendPlan(&plans[6], ModPresetContentKey(mp.RulesetShortName, mp.Name), mp); aErr != nil {
			return 0, 0, aErr
		}
	}

	for _, p := range plans {
		if snap.Kinds != nil && !snap.Kinds[p.kind] {
			continue // §0 板块开关：未圈定 kind 的 presence/tombstone 原封不动
		}
		prev := map[string]bool{}
		rows, qErr := tx.QueryContext(ctx,
			`SELECT content_key FROM presence WHERE fork_id=? AND kind=?`, snap.ForkID, p.kind)
		if qErr != nil {
			return 0, 0, fmt.Errorf("hub: 读上次 presence(%s): %w", p.kind, qErr)
		}
		for rows.Next() {
			var k string
			if sErr := rows.Scan(&k); sErr != nil {
				rows.Close()
				return 0, 0, fmt.Errorf("hub: 读上次 presence 行(%s): %w", p.kind, sErr)
			}
			prev[k] = true
		}
		if rErr := rows.Err(); rErr != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("hub: 遍历上次 presence(%s): %w", p.kind, rErr)
		}
		rows.Close()

		next := make(map[string]bool, len(p.keys))
		for _, k := range p.keys {
			next[k] = true
		}

		// 差集 → tombstone（确定性顺序便于断言）。
		var gone []string
		for k := range prev {
			if !next[k] {
				gone = append(gone, k)
			}
		}
		sort.Strings(gone)
		for _, k := range gone {
			if p.kind == KindBeatmapSet {
				tail, tErr := resolveChainTail(ctx, tx, k)
				if tErr != nil {
					return 0, 0, tErr
				}
				if tail != k && next[tail] {
					continue // 版本更新取代的消失不算删除（HB2 第二道闸）
				}
			}
			if _, eErr := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO tombstone(fork_id, kind, content_key, deleted_at)
VALUES(?,?,?,?)`, snap.ForkID, p.kind, k, now); eErr != nil {
				return 0, 0, fmt.Errorf("hub: 写 tombstone %q(%s): %w", k, p.kind, eErr)
			}
			added++
		}

		// 整段重写 presence。
		if _, dErr := tx.ExecContext(ctx,
			`DELETE FROM presence WHERE fork_id=? AND kind=?`, snap.ForkID, p.kind); dErr != nil {
			return 0, 0, fmt.Errorf("hub: 清 presence(%s): %w", p.kind, dErr)
		}
		ins, pErr := tx.PrepareContext(ctx, `
INSERT INTO presence(fork_id, kind, content_key, local_values, collected_at)
VALUES(?,?,?,?,?)`)
		if pErr != nil {
			return 0, 0, fmt.Errorf("hub: 预写 presence(%s): %w", p.kind, pErr)
		}
		for _, k := range p.keys {
			if _, eErr := ins.ExecContext(ctx, snap.ForkID, p.kind, k, p.local[k], now); eErr != nil {
				ins.Close()
				return 0, 0, fmt.Errorf("hub: 写 presence %q(%s): %w", k, p.kind, eErr)
			}
		}
		ins.Close()

		// 同键重现 → 清除 tombstone（HB2 第三道闸）。
		del, pErr := tx.PrepareContext(ctx, `
DELETE FROM tombstone WHERE fork_id=? AND kind=? AND content_key=?`)
		if pErr != nil {
			return 0, 0, fmt.Errorf("hub: 预清 tombstone(%s): %w", p.kind, pErr)
		}
		for _, k := range p.keys {
			r, eErr := del.ExecContext(ctx, snap.ForkID, p.kind, k)
			if eErr != nil {
				del.Close()
				return 0, 0, fmt.Errorf("hub: 清 tombstone %q(%s): %w", k, p.kind, eErr)
			}
			if n, aErr := r.RowsAffected(); aErr == nil {
				cleared += int(n)
			}
		}
		del.Close()
	}
	return added, cleared, nil
}
