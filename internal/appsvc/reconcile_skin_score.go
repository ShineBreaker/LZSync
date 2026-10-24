package appsvc

// skin/score 的 hub→fork 对账过渡直查：hub.ReconcileAgainst 尚未放开
// skin/score kind（internal/hub/reconcile.go 的 switch 只认 collection/
// beatmap_set），diff 层亦为契约桩——本文件以 hub.DB() 只读直查承载
// 预演数据面（同 deploy.reconcileSkins 的过渡先例，hub 增补领域方法后
// 应切换，届时本文件删除）。
//
// 对账语义与 §4.4/§4.2 一致：canonical 全部行（skin/score 无版本链）
// vs 目标 presence 差集 → create；命中 tombstone 归恢复组（默认排除，
// §5.4 删除不复活）；presence 有该键则比冲突字段（Skin 比 Name/Creator、
// Score 比 Date/TotalScore/RankInt——内容键本身不比，§4.2），有差异 →
// conflict（默认 hub 覆盖，§5.4）。

import (
	"context"
	"encoding/json"
	"fmt"

	"lzsync/internal/hub"
)

// presenceOf 读目标 fork 某类型的 presence（内容键 → fork 侧投影 JSON）。
// hub.presenceOf 未导出，此为只读直查（与 deploy.reconcileSkins 同款）。
func presenceOf(ctx context.Context, h *hub.Store, forkID string, kind hub.EntityKind) (map[string]string, error) {
	rows, err := h.DB().QueryContext(ctx,
		`SELECT content_key, local_values FROM presence WHERE fork_id=? AND kind=?`, forkID, kind)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读 presence(%s): %w", kind, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("appsvc: 读 presence(%s) 行: %w", kind, err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("appsvc: 遍历 presence(%s): %w", kind, err)
	}
	return out, nil
}

// reconcileSkins 皮肤对账（skin 表全部行 vs 目标 presence，§4.4）。
func (s *realCompare) reconcileSkins(ctx context.Context, forkID string) ([]hub.ReconcileItem, error) {
	rows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT content_hash, payload FROM skin ORDER BY content_hash`)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读皮肤 canonical: %w", err)
	}
	var canonRows []keyedPayload
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, fmt.Errorf("appsvc: 读皮肤 canonical 行: %w", err)
		}
		canonRows = append(canonRows, keyedPayload{k, v})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("appsvc: 遍历皮肤 canonical: %w", err)
	}
	rows.Close()

	return finishSimpleReconcile(ctx, s, forkID, hub.KindSkin, canonRows,
		func(canonJSON, localJSON string) ([]hub.ConflictField, error) {
			var hubSk, forkSk hub.Skin
			if err := decodeReconcilePayloads(canonJSON, localJSON, &hubSk, &forkSk); err != nil {
				return nil, err
			}
			var out []hub.ConflictField
			if hubSk.Name != forkSk.Name {
				out = append(out, hub.ConflictField{Field: "Name", HubValue: hubSk.Name, ForkValue: forkSk.Name})
			}
			if hubSk.Creator != forkSk.Creator {
				out = append(out, hub.ConflictField{Field: "Creator", HubValue: hubSk.Creator, ForkValue: forkSk.Creator})
			}
			return out, nil
		})
}

// reconcileScores 成绩对账（score 表全部行按组合内容键 vs 目标 presence）。
func (s *realCompare) reconcileScores(ctx context.Context, forkID string) ([]hub.ReconcileItem, error) {
	rows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT beatmap_hash, content_hash, payload FROM score ORDER BY beatmap_hash, content_hash`)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读成绩 canonical: %w", err)
	}
	var canonRows []keyedPayload
	for rows.Next() {
		var beatmapHash, contentHash, payload string
		if err := rows.Scan(&beatmapHash, &contentHash, &payload); err != nil {
			rows.Close()
			return nil, fmt.Errorf("appsvc: 读成绩 canonical 行: %w", err)
		}
		canonRows = append(canonRows, keyedPayload{hub.ScoreContentKey(beatmapHash, contentHash), payload})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("appsvc: 遍历成绩 canonical: %w", err)
	}
	rows.Close()

	return finishSimpleReconcile(ctx, s, forkID, hub.KindScore, canonRows,
		func(canonJSON, localJSON string) ([]hub.ConflictField, error) {
			var hubSc, forkSc hub.Score
			if err := decodeReconcilePayloads(canonJSON, localJSON, &hubSc, &forkSc); err != nil {
				return nil, err
			}
			var out []hub.ConflictField
			add := func(field string, a, b any) {
				ha, fa := fmt.Sprint(a), fmt.Sprint(b)
				if ha != fa {
					out = append(out, hub.ConflictField{Field: field, HubValue: ha, ForkValue: fa})
				}
			}
			add("Date", hubSc.Date.Format("2006-01-02 15:04:05"), forkSc.Date.Format("2006-01-02 15:04:05"))
			add("TotalScore", hubSc.TotalScore, forkSc.TotalScore)
			add("RankInt", hubSc.RankInt, forkSc.RankInt)
			return out, nil
		})
}

// keyedPayload canonical 行的最小形态：内容键 + payload JSON。
type keyedPayload struct {
	key, payload string
}

// finishSimpleReconcile 无版本链实体（skin/score）的对账收尾：canonical vs
// presence 差集 → create（tombstone 命中归恢复组）；命中 presence 则比冲突
// 字段，一致则无动作不产出。
func finishSimpleReconcile(
	ctx context.Context,
	s *realCompare,
	forkID string,
	kind hub.EntityKind,
	canonRows []keyedPayload,
	compare func(canonJSON, localJSON string) ([]hub.ConflictField, error),
) ([]hub.ReconcileItem, error) {
	pres, err := presenceOf(ctx, s.deps.Hub, forkID, kind)
	if err != nil {
		return nil, err
	}
	tombs, err := s.deps.Hub.Tombstones(ctx, forkID)
	if err != nil {
		return nil, err
	}
	tombKeys := tombs[kind]

	var out []hub.ReconcileItem
	for _, c := range canonRows {
		local, inPres := pres[c.key]
		if !inPres {
			it := hub.ReconcileItem{Kind: kind, ContentKey: c.key, Category: hub.ReconcileCreate}
			if at, dead := tombKeys[c.key]; dead {
				it.Restorable, it.DeletedAt = true, at
			}
			out = append(out, it)
			continue
		}
		cf, err := compare(c.payload, local)
		if err != nil {
			return nil, fmt.Errorf("appsvc: %s %q: %w", kind, c.key, err)
		}
		if len(cf) > 0 {
			out = append(out, hub.ReconcileItem{
				Kind: kind, ContentKey: c.key,
				Category: hub.ReconcileConflict, ConflictFields: cf,
			})
		}
	}
	return out, nil
}

// decodeReconcilePayloads 解码 canonical payload 与 presence local_values。
func decodeReconcilePayloads(canonJSON, localJSON string, canon, fork any) error {
	if err := json.Unmarshal([]byte(canonJSON), canon); err != nil {
		return fmt.Errorf("解码 canonical: %w", err)
	}
	if err := json.Unmarshal([]byte(localJSON), fork); err != nil {
		return fmt.Errorf("解码 presence: %w", err)
	}
	return nil
}
