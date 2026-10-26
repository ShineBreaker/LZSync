package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReconcileCategory 是 hub→fork 对账分类（§4.2 四分类在投放预演方向的
// 落位）。target-only 属 fork↔fork 对比（diff 层两份 presence 差集），
// 不在此列：hub 真相包含曾收集的全部内容键（hub 不删实体，§4.4）。
type ReconcileCategory string

const (
	ReconcileCreate        ReconcileCategory = "create"         // 源独有：hub 当前版本、目标 presence 缺该键
	ReconcileConflict      ReconcileCategory = "conflict"       // 内容键相等、非键字段不同（§4.2）
	ReconcileVersionUpdate ReconcileCategory = "version-update" // 内容键不等、谱面身份键相等（HB1 链或弱键疑似）
)

// ConflictField 冲突字段级对比（canonical vs 目标 fork presence）。
// 冲突字段只取内容键之外的字段——内容键匹配时必相等，比它是死代码（§4.2）。
type ConflictField struct {
	Field     string
	HubValue  string
	ForkValue string
}

// ReconcileItem 单条对账结果行（hub 内纯 SQL 查询产物，diff 层据此组装
// Entry 与人类可读 Summary，§3.2 对账）。
type ReconcileItem struct {
	Kind           EntityKind
	ContentKey     string // 目标侧键（VersionUpdate 时 = 目标 presence 中的旧版本键）
	Summary        string // 行摘要：hub 公共对账面不填（appsvc summarizer 生成），设置类过渡直查面自带键级摘要时填
	Category       ReconcileCategory
	ConflictFields []ConflictField // Conflict 时填充
	IdentityKey    string          // VersionUpdate 时填充（online:/meta: 前缀）
	Evidence       string          // VersionUpdate 时填充（EvidenceStrong/EvidenceWeak）
	CurrentKey     string          // VersionUpdate 时：hub 当前版本内容键（投放侧的链尾）
	Restorable     bool            // Create ∧ 命中目标 tombstone：「恢复（已删除）」组——默认不进计划、需用户显式勾选（§6.2）
	DeletedAt      int64           // Restorable 时的删除时间（Unix 毫秒）
}

// ReconcileAgainst 计算 hub 真相 → 单 fork 的对账差异（§4.4 对账语义，
// 只读）：当前版本集合（superseded_by IS NULL，HB1——历史版本永不进入
// 差集）vs 目标 presence；create 项再减去 tombstone 命中（归入恢复组，
// 默认排除出计划，§5.4 删除不复活）。
func (s *Store) ReconcileAgainst(ctx context.Context, forkID string, kinds []EntityKind) ([]ReconcileItem, error) {
	if forkID == "" {
		return nil, fmt.Errorf("hub: 对账缺少 fork_id")
	}
	var out []ReconcileItem
	for _, kind := range kinds {
		var items []ReconcileItem
		var err error
		switch kind {
		case KindCollection:
			items, err = s.reconcileCollections(ctx, forkID)
		case KindBeatmapSet:
			items, err = s.reconcileBeatmapSets(ctx, forkID)
		default:
			return nil, fmt.Errorf("hub: 对账不支持实体类型 %q（由后续 schema 迁移启用）", kind)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ContentKey < out[j].ContentKey
	})
	return out, nil
}

// Tombstones 返回目标 fork 各类型的全部 tombstone（恢复组的完整数据面，
// diff 层 TombstoneHits 的数据源；含已不在 hub 当前版本集合中的键）。
func (s *Store) Tombstones(ctx context.Context, forkID string) (map[EntityKind]map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, content_key, deleted_at FROM tombstone WHERE fork_id=? ORDER BY kind, content_key`, forkID)
	if err != nil {
		return nil, fmt.Errorf("hub: 读 tombstone: %w", err)
	}
	defer rows.Close()
	out := map[EntityKind]map[string]int64{}
	for rows.Next() {
		var (
			kind      EntityKind
			key       string
			deletedAt int64
		)
		if err := rows.Scan(&kind, &key, &deletedAt); err != nil {
			return nil, fmt.Errorf("hub: 读 tombstone 行: %w", err)
		}
		if out[kind] == nil {
			out[kind] = map[string]int64{}
		}
		out[kind][key] = deletedAt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: 遍历 tombstone: %w", err)
	}
	return out, nil
}

// presenceOf 读目标 fork 某类型的 presence（内容键 → fork 侧投影 JSON）。
func (s *Store) presenceOf(ctx context.Context, forkID string, kind EntityKind) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_key, local_values FROM presence WHERE fork_id=? AND kind=?`, forkID, kind)
	if err != nil {
		return nil, fmt.Errorf("hub: 读 presence(%s): %w", kind, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("hub: 读 presence(%s) 行: %w", kind, err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: 遍历 presence(%s): %w", kind, err)
	}
	return out, nil
}

func (s *Store) reconcileCollections(ctx context.Context, forkID string) ([]ReconcileItem, error) {
	// canonical 值（收藏夹无版本链，全部行即当前版本集合）。
	hubRows, err := s.db.QueryContext(ctx,
		`SELECT content_key, payload FROM collection ORDER BY content_key`)
	if err != nil {
		return nil, fmt.Errorf("hub: 读收藏 canonical: %w", err)
	}
	type pair struct {
		key     string
		payload string
	}
	var canon []pair
	for hubRows.Next() {
		var k, v string
		if err := hubRows.Scan(&k, &v); err != nil {
			hubRows.Close()
			return nil, fmt.Errorf("hub: 读收藏 canonical 行: %w", err)
		}
		canon = append(canon, pair{k, v})
	}
	if err := hubRows.Err(); err != nil {
		hubRows.Close()
		return nil, fmt.Errorf("hub: 遍历收藏 canonical: %w", err)
	}
	hubRows.Close()

	pres, err := s.presenceOf(ctx, forkID, KindCollection)
	if err != nil {
		return nil, err
	}
	tombs, err := s.Tombstones(ctx, forkID)
	if err != nil {
		return nil, err
	}
	tombKeys := tombs[KindCollection]

	var out []ReconcileItem
	for _, c := range canon {
		local, inPres := pres[c.key]
		if !inPres {
			it := ReconcileItem{Kind: KindCollection, ContentKey: c.key, Category: ReconcileCreate}
			if at, dead := tombKeys[c.key]; dead {
				it.Restorable, it.DeletedAt = true, at
			}
			out = append(out, it)
			continue
		}
		var hubC, forkC Collection
		if err := decodeBoth(c.payload, local, &hubC, &forkC); err != nil {
			return nil, fmt.Errorf("hub: 收藏 %q: %w", c.key, err)
		}
		if cf := collectionConflicts(hubC, forkC); len(cf) > 0 {
			out = append(out, ReconcileItem{
				Kind: KindCollection, ContentKey: c.key,
				Category: ReconcileConflict, ConflictFields: cf,
			})
		}
		// 字段一致：无动作，不产出。
	}
	return out, nil
}

func (s *Store) reconcileBeatmapSets(ctx context.Context, forkID string) ([]ReconcileItem, error) {
	// hub 当前版本集合 = 版本链尾（HB1）。
	hubRows, err := s.db.QueryContext(ctx, `
SELECT content_hash, payload FROM beatmap_set WHERE superseded_by IS NULL ORDER BY content_hash`)
	if err != nil {
		return nil, fmt.Errorf("hub: 读谱面集 canonical: %w", err)
	}
	current := map[string]string{}
	var currentKeys []string
	for hubRows.Next() {
		var k, v string
		if err := hubRows.Scan(&k, &v); err != nil {
			hubRows.Close()
			return nil, fmt.Errorf("hub: 读谱面集 canonical 行: %w", err)
		}
		current[k] = v
		currentKeys = append(currentKeys, k)
	}
	if err := hubRows.Err(); err != nil {
		hubRows.Close()
		return nil, fmt.Errorf("hub: 遍历谱面集 canonical: %w", err)
	}
	hubRows.Close()

	pres, err := s.presenceOf(ctx, forkID, KindBeatmapSet)
	if err != nil {
		return nil, err
	}
	tombs, err := s.Tombstones(ctx, forkID)
	if err != nil {
		return nil, err
	}
	tombKeys := tombs[KindBeatmapSet]

	// 身份索引：内容哈希 → 身份键 + 证据强度。异域副本会让同一哈希同时
	// 挂强键（online:）与弱键（meta:）——与判定层同序「强键优先 + 序
	// 最小」取首个命中，保证 pairing/锚定跨端一致（§0.3 身份锚）。
	idRows, err := s.db.QueryContext(ctx, `
SELECT iv.content_hash, iv.identity_key, bi.evidence
FROM identity_version iv JOIN beatmap_identity bi ON bi.identity_key = iv.identity_key
ORDER BY CASE WHEN iv.identity_key LIKE 'online:%' THEN 0 ELSE 1 END, iv.identity_key`)
	if err != nil {
		return nil, fmt.Errorf("hub: 读身份索引: %w", err)
	}
	type ident struct {
		key      string
		evidence string
	}
	hashIdent := map[string]ident{}
	for idRows.Next() {
		var h, k, ev string
		if err := idRows.Scan(&h, &k, &ev); err != nil {
			idRows.Close()
			return nil, fmt.Errorf("hub: 读身份索引行: %w", err)
		}
		if _, ok := hashIdent[h]; !ok {
			hashIdent[h] = ident{k, ev}
		}
	}
	if err := idRows.Err(); err != nil {
		idRows.Close()
		return nil, fmt.Errorf("hub: 遍历身份索引: %w", err)
	}
	idRows.Close()

	paired := map[string]bool{} // 已被 version-update 消费的 hub 当前版本键
	var out []ReconcileItem

	// 目标 presence 键（确定性顺序）。
	presKeys := make([]string, 0, len(pres))
	for k := range pres {
		presKeys = append(presKeys, k)
	}
	sort.Strings(presKeys)

	for _, k := range presKeys {
		matched := false
		tail, err := resolveChainTail(ctx, s.db, k)
		if err != nil {
			return nil, err
		}
		if tail != k {
			// 目标持有历史版本：hub 当前 = 链尾（强键语义的版本更新）。
			it := ReconcileItem{
				Kind: KindBeatmapSet, ContentKey: k, Category: ReconcileVersionUpdate,
				CurrentKey: tail, Evidence: EvidenceStrong,
			}
			if id, ok := hashIdent[tail]; ok {
				it.IdentityKey, it.Evidence = id.key, id.evidence
			}
			out = append(out, it)
			paired[tail] = true
			matched = true
		} else if id, ok := hashIdent[k]; ok && id.evidence == EvidenceWeak {
			// 弱键疑似配对：hub 有同身份键的另一链尾且目标没有它 → 归入
			// 版本更新（需用户确认组，默认不勾选，§4.2/§6.2）。
			for _, h := range currentKeys {
				if h == k || paired[h] {
					continue
				}
				if _, inPres := pres[h]; inPres {
					continue
				}
				if hi, ok2 := hashIdent[h]; ok2 && hi.key == id.key {
					out = append(out, ReconcileItem{
						Kind: KindBeatmapSet, ContentKey: k, Category: ReconcileVersionUpdate,
						CurrentKey: h, IdentityKey: id.key, Evidence: EvidenceWeak,
					})
					paired[h] = true
					matched = true
					break
				}
			}
		}
		if matched {
			continue
		}
		// 目标持有的是链尾：一致或冲突（内容键相等、非键字段不同）。
		if payload, ok := current[k]; ok {
			var hubSet, forkSet BeatmapSet
			if err := decodeBoth(payload, pres[k], &hubSet, &forkSet); err != nil {
				return nil, fmt.Errorf("hub: 谱面集 %q: %w", k, err)
			}
			if cf := beatmapSetConflicts(hubSet, forkSet); len(cf) > 0 {
				out = append(out, ReconcileItem{
					Kind: KindBeatmapSet, ContentKey: k,
					Category: ReconcileConflict, ConflictFields: cf,
				})
			}
		}
		// presence 键不在实体表：理论不可能（presence 由收集写入、hub 不删
		// 实体）；防御性跳过，不产出任何动作。
	}

	// 源独有：hub 当前版本 − 目标 presence − 已配对；命中 tombstone 的归
	// 恢复组（默认排除，§5.4 删除不复活）。
	for _, h := range currentKeys {
		if _, inPres := pres[h]; inPres || paired[h] {
			continue
		}
		it := ReconcileItem{Kind: KindBeatmapSet, ContentKey: h, Category: ReconcileCreate}
		if at, dead := tombKeys[h]; dead {
			it.Restorable, it.DeletedAt = true, at
		}
		out = append(out, it)
	}
	return out, nil
}

// decodeBoth 解码 canonical payload 与 presence local_values（同构 DTO JSON）。
func decodeBoth(canonJSON, localJSON string, canon, fork any) error {
	if err := json.Unmarshal([]byte(canonJSON), canon); err != nil {
		return fmt.Errorf("解码 canonical: %w", err)
	}
	if err := json.Unmarshal([]byte(localJSON), fork); err != nil {
		return fmt.Errorf("解码 presence: %w", err)
	}
	return nil
}

// md5Fingerprint 收藏夹排序指纹（冲突判据，§4.2 第一层）。
func md5Fingerprint(hashes []string) string {
	sorted := append([]string(nil), hashes...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// collectionConflicts 收藏夹冲突字段（§4.2：BeatmapMD5Hashes 排序指纹作
// 冲突判据；内容键 Name 本身不比）。
func collectionConflicts(hubC, forkC Collection) []ConflictField {
	hf, ff := md5Fingerprint(hubC.BeatmapMD5Hashes), md5Fingerprint(forkC.BeatmapMD5Hashes)
	if hf == ff {
		return nil
	}
	return []ConflictField{{Field: "BeatmapMD5Hashes", HubValue: hf, ForkValue: ff}}
}

// beatmapSetConflicts 谱面集冲突字段（内容键 Hash 本身不比）。
// §0.3-G10/B7：DateAdded/StatusInt/Protected 是端本地字段，OnlineID 是
// S8 服务器域作用域值——天然按端发散，不参与冲突判定（参与即永久冲突）。
// 比对面 = 就地更新实际会写的列：Title（内容元数据）+ DateSubmitted/
// DateRanked（上游谱面状态，可同步面）。
func beatmapSetConflicts(hubSet, forkSet BeatmapSet) []ConflictField {
	var out []ConflictField
	add := func(field string, a, b string) {
		if a != b {
			out = append(out, ConflictField{Field: field, HubValue: a, ForkValue: b})
		}
	}
	ts := func(p *time.Time) string {
		if p == nil {
			return "<null>"
		}
		return p.UTC().Format(time.RFC3339Nano)
	}
	add("Title", hubSet.Title, forkSet.Title)
	add("DateSubmitted", ts(hubSet.DateSubmitted), ts(forkSet.DateSubmitted))
	add("DateRanked", ts(hubSet.DateRanked), ts(forkSet.DateRanked))
	return out
}
