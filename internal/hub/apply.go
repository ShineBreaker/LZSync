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
type Snapshot struct {
	ForkID      string
	CollectedAt time.Time
	Collections []Collection
	BeatmapSets []BeatmapSet
	Skips       []SkipStat // 投影期跳过（进 collect_log）
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
//  1. 实体 upsert：整行覆盖并随行更新 origin_fork（HB3；origin_guid 仅
//     首次落库固定——「首次收集时的源 fork Guid」，投放新建行时沿用）；
//  2. presence 按 fork 整段重写（快照语义，幂等）；
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

	// 效果 1 + 4：实体 upsert 与身份/版本链维护（HB1/HB3）。
	for i := range snap.Collections {
		if err := upsertCollection(ctx, tx, &snap.Collections[i], snap.ForkID, now); err != nil {
			return res, err
		}
		res.EntitiesWritten[KindCollection]++
	}
	for i := range snap.BeatmapSets {
		bs := &snap.BeatmapSets[i]
		if err := upsertBeatmapSet(ctx, tx, bs, snap.ForkID, now); err != nil {
			return res, err
		}
		for j := range bs.Beatmaps {
			if err := upsertBeatmap(ctx, tx, &bs.Beatmaps[j], bs.Hash, now); err != nil {
				return res, err
			}
			res.EntitiesWritten[KindBeatmap]++
		}
		res.EntitiesWritten[KindBeatmapSet]++
		links, err := maintainIdentity(ctx, tx, bs, now)
		if err != nil {
			return res, err
		}
		res.VersionLinksCreated += int(links)
	}

	// 效果 2 + 3：presence 整段重写 + 差集 tombstone（HB2）。
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

// upsertCollection 收藏夹实体行（内容键 = Name）。HB3：payload 与
// origin_fork 整行覆盖；origin_guid 仅首次插入落库（冲突更新不含该列）。
func upsertCollection(ctx context.Context, tx *sql.Tx, c *Collection, fork string, now int64) error {
	payload, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("hub: 序列化收藏 %q: %w", c.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO collection(content_key, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(content_key) DO UPDATE SET
  payload=excluded.payload, origin_fork=excluded.origin_fork, updated_at=excluded.updated_at`,
		c.Name, string(payload), c.ID, fork, now, now); err != nil {
		return fmt.Errorf("hub: upsert 收藏 %q: %w", c.Name, err)
	}
	return nil
}

// upsertBeatmapSet 谱面集实体行（内容键 = Hash）。同 HB3 语义；
// superseded_by 属版本链状态，upsert 不得触碰（历史行永久保留）。
func upsertBeatmapSet(ctx context.Context, tx *sql.Tx, bs *BeatmapSet, fork string, now int64) error {
	payload, err := json.Marshal(bs)
	if err != nil {
		return fmt.Errorf("hub: 序列化谱面集 %q: %w", bs.Hash, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO beatmap_set(content_hash, payload, origin_guid, origin_fork, superseded_by, first_seen, updated_at)
VALUES(?,?,?,?,NULL,?,?)
ON CONFLICT(content_hash) DO UPDATE SET
  payload=excluded.payload, origin_fork=excluded.origin_fork, updated_at=excluded.updated_at`,
		bs.Hash, string(payload), bs.ID, fork, now, now); err != nil {
		return fmt.Errorf("hub: upsert 谱面集 %q: %w", bs.Hash, err)
	}
	return nil
}

// upsertBeatmap 难度行（内容键 = Hash，随父集维护 set_hash）。难度不做
// presence / 版本链：整集按一个版本处理（§4.2 第二层末段）。
func upsertBeatmap(ctx context.Context, tx *sql.Tx, bm *Beatmap, setHash string, now int64) error {
	payload, err := json.Marshal(bm)
	if err != nil {
		return fmt.Errorf("hub: 序列化难度 %q: %w", bm.Hash, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO beatmap(content_hash, set_hash, payload, first_seen, updated_at)
VALUES(?,?,?,?,?)
ON CONFLICT(content_hash) DO UPDATE SET
  set_hash=excluded.set_hash, payload=excluded.payload, updated_at=excluded.updated_at`,
		bm.Hash, setHash, string(payload), now, now); err != nil {
		return fmt.Errorf("hub: upsert 难度 %q: %w", bm.Hash, err)
	}
	return nil
}

// rewritePresence 按 fork 整段重写两类 presence（快照语义，幂等），并就
// 上次快照差集生成 tombstone（HB2）。防误标三道闸：
//   - 「投影完整成功才落库」由 Snapshot 构造方保证（见 Snapshot 注释）；
//   - 消失键已被版本链取代（链尾仍在本次快照中）→ 不算删除；
//   - 本次出现的键命中既有 tombstone → 即刻清除（同键重现）。
//
// 必须在版本链维护之后调用（链状态参与差集判定）。
func rewritePresence(ctx context.Context, tx *sql.Tx, snap *Snapshot, now int64) (added, cleared int, err error) {
	type kindPayload struct {
		kind  EntityKind
		keys  []string
		local map[string]string // 内容键 → fork 侧投影 JSON（local_values）
	}
	plans := []kindPayload{
		{kind: KindCollection, local: map[string]string{}},
		{kind: KindBeatmapSet, local: map[string]string{}},
	}
	for i := range snap.Collections {
		c := &snap.Collections[i]
		j, mErr := json.Marshal(c)
		if mErr != nil {
			return 0, 0, fmt.Errorf("hub: 序列化收藏 %q presence: %w", c.Name, mErr)
		}
		plans[0].keys = append(plans[0].keys, c.Name)
		plans[0].local[c.Name] = string(j)
	}
	for i := range snap.BeatmapSets {
		bs := &snap.BeatmapSets[i]
		j, mErr := json.Marshal(bs)
		if mErr != nil {
			return 0, 0, fmt.Errorf("hub: 序列化谱面集 %q presence: %w", bs.Hash, mErr)
		}
		plans[1].keys = append(plans[1].keys, bs.Hash)
		plans[1].local[bs.Hash] = string(j)
	}

	for _, p := range plans {
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
