package hub

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// rowQuerier 抽象 *sql.DB 与 *sql.Tx 的共同查询面（链尾解析在事务内外的
// 只读路径复用）。
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// 证据强度标签（§4.2 第二层；reconcile 查询原样透出给 diff 层）。
const (
	EvidenceStrong = "strong" // OnlineID 强键：默认按版本更新执行
	EvidenceWeak   = "weak"   // 元数据弱键「疑似」：需用户确认组，默认不勾选
)

// identityKey 计算谱面集身份键（§4.2 第二层）：
//   - OnlineID > 0 → "online:<id>"（强键，同服务器命名空间内稳定）；
//   - 否则 → "meta:<artist>|<title>|<author>"（弱键，需用户确认）。
//
// 弱键分隔符 "|" 与字段值碰撞时可能误并——v1 接受该极小概率（谱面身份仅
// 用于配对建议，弱键本就需用户拍板），待真实数据暴露再归一化。
func identityKey(bs *BeatmapSet) (key string, strong bool) {
	if bs.OnlineID > 0 {
		return "online:" + strconv.FormatInt(bs.OnlineID, 10), true
	}
	return "meta:" + bs.Artist + "|" + bs.Title + "|" + bs.AuthorUsername, false
}

// resolveChainTail 返回 content_hash 所在版本链的链尾（superseded_by 为
// NULL 的行，HB1：链尾即该谱面的当前版本）。行不存在、指向行缺失或成环
// 一律按当前行停走（防御：建链只指向已落库行，正常不会出现后两者）。
func resolveChainTail(ctx context.Context, q rowQuerier, hash string) (string, error) {
	seen := map[string]bool{hash: true}
	cur := hash
	for {
		var next sql.NullString
		err := q.QueryRowContext(ctx,
			`SELECT superseded_by FROM beatmap_set WHERE content_hash=?`, cur).Scan(&next)
		if err != nil {
			if err == sql.ErrNoRows {
				return cur, nil
			}
			return "", fmt.Errorf("hub: 解析版本链 %q: %w", cur, err)
		}
		if !next.Valid || next.String == "" || seen[next.String] {
			return cur, nil
		}
		seen[next.String] = true
		cur = next.String
	}
}

// maintainIdentity 在收集事务内维护身份键缓存与版本链（§4.4 对账缓存 +
// HB1）：记录本次 (identity_key, content_hash) 配对；强键自动把同身份键
// 的既有链尾指向本次版本，弱键仅在用户已确认配对（user_confirmed=1）时
// 建链——弱键默认「疑似」，链一建旧版即退出当前版本集合，会剥夺用户
// 在预览页三选一（替换/并存/忽略）的机会（§5.4）。
// 返回本次新维护的 superseded_by 指针数。
func maintainIdentity(ctx context.Context, tx *sql.Tx, bs *BeatmapSet, now int64) (int64, error) {
	ikey, strong := identityKey(bs)
	label := EvidenceWeak
	if strong {
		label = EvidenceStrong
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO beatmap_identity(identity_key, evidence, user_confirmed, first_seen)
VALUES(?,?,0,?)`, ikey, label, now); err != nil {
		return 0, fmt.Errorf("hub: 写身份键 %q: %w", ikey, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO identity_version(identity_key, content_hash)
VALUES(?,?)`, ikey, bs.Hash); err != nil {
		return 0, fmt.Errorf("hub: 写身份配对 (%q,%q): %w", ikey, bs.Hash, err)
	}
	if !strong {
		var confirmed int
		if err := tx.QueryRowContext(ctx,
			`SELECT user_confirmed FROM beatmap_identity WHERE identity_key=?`, ikey).Scan(&confirmed); err != nil {
			return 0, fmt.Errorf("hub: 读身份键确认态 %q: %w", ikey, err)
		}
		if confirmed != 1 {
			return 0, nil // 弱键未拍板：不建链
		}
	}
	// 本次版本成为同身份键下全部既有链尾的新后继（收集序定链方向，链尾
	// 语义只认指针，§4.4——不用 DateAdded 也不用收集序做「最新」判定）。
	res, err := tx.ExecContext(ctx, `
UPDATE beatmap_set SET superseded_by=?
WHERE superseded_by IS NULL
  AND content_hash IN (
      SELECT content_hash FROM identity_version
      WHERE identity_key=? AND content_hash<>?)`, bs.Hash, ikey, bs.Hash)
	if err != nil {
		return 0, fmt.Errorf("hub: 建版本链 %q→%q: %w", ikey, bs.Hash, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("hub: 计版本链 %q: %w", ikey, err)
	}
	return n, nil
}
