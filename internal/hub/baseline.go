package hub

// baseline 表读写面（schema v4，design.md §0.3 基线三方判定）：
// b(f, I) = 端 f 上同步身份 I 在上一轮同步结束时的取值指纹。行只增不
// 删——「无行 = 无同步史」与「已知缺席」由 BaselineAbsent 哨兵区分；
// 身份从所有端消失后行自然无人再读（审计面保留）。
//
// 判定单位 (fork_id, kind, identity, member) 的取值约定见 ddlV4 注释；
// kind 对 game.ini 用伪 kind BaselineKindGameIni（非实体类，不占
// EntityKind 枚举）。

import (
	"context"
	"fmt"
	"strings"
)

// BaselineKindGameIni 是 baseline 表中 game.ini 白名单键的伪 kind
//（判定粒度 = 键名，value = 键值原文，§0.3）。
const BaselineKindGameIni = "game_ini"

// BaselineAbsent 是「已知缺席」的取值哨兵（value 列；与「无行 = 无
// 同步史」区分——无行端在 §0.3 判为「无基线」、不算变更来源；哨兵行
// 端取值变化即算 Δ）。真实值（哈希/键值/JSON）不可能以 \x00 开头。
const BaselineAbsent = "\x00"

// BaselineRow 是 baseline 表的一行（判定单位 = 四元组主键）。
type BaselineRow struct {
	ForkID   string
	Kind     string // EntityKind 或 BaselineKindGameIni
	Identity string // 同步身份（§0.3：谱面身份键/内容键/三段键/ini 键名）
	Member   string // 判定子粒度：收藏成员 md5；其他 ''
	Value    string // 同步值指纹；BaselineAbsent = 已知缺席
	SyncedAt int64  // Unix 毫秒
}

// Baselines 读全量基线行（一轮判定的输入面——行数 = 端×身份量级，
// 一次性全读比拼凑子查询简单且总量可控）。
func (s *Store) Baselines(ctx context.Context) ([]BaselineRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT fork_id, kind, identity, member, value, synced_at FROM baseline`)
	if err != nil {
		return nil, fmt.Errorf("hub: 读 baseline: %w", err)
	}
	defer rows.Close()
	var out []BaselineRow
	for rows.Next() {
		var r BaselineRow
		if err := rows.Scan(&r.ForkID, &r.Kind, &r.Identity, &r.Member, &r.Value, &r.SyncedAt); err != nil {
			return nil, fmt.Errorf("hub: 扫 baseline 行: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: 遍历 baseline: %w", err)
	}
	return out, nil
}

// AdvanceBaselines 推进基线（§0.3「一轮结束时 b(f,I) := 该端最终值」）：
// UPSERT 覆盖同单位旧值；调用方已剔除冲突/保护/跳过端的单位（冲突身份
// 基线不动）。单事务批写。
func (s *Store) AdvanceBaselines(ctx context.Context, rows []BaselineRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hub: 开基线事务: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck
		}
	}()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO baseline(fork_id, kind, identity, member, value, synced_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(fork_id, kind, identity, member) DO UPDATE SET
  value=excluded.value, synced_at=excluded.synced_at`)
	if err != nil {
		return fmt.Errorf("hub: 备基线写句: %w", err)
	}
	defer stmt.Close()
	var miss []string
	for i := range rows {
		r := &rows[i]
		if r.ForkID == "" || r.Kind == "" || r.Identity == "" {
			miss = append(miss, fmt.Sprintf("(%q,%q,%q)", r.ForkID, r.Kind, r.Identity))
			continue
		}
		if _, err := stmt.ExecContext(ctx, r.ForkID, r.Kind, r.Identity, r.Member, r.Value, r.SyncedAt); err != nil {
			return fmt.Errorf("hub: 写 baseline (%s,%s,%s): %w", r.ForkID, r.Kind, r.Identity, err)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("hub: baseline 行缺主键段: %s", strings.Join(miss, ","))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hub: 提交基线事务: %w", err)
	}
	committed = true
	return nil
}
