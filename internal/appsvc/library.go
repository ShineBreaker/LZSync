package appsvc

// 资料库页行集（DESIGN.md §4）：库内全量条目 ∪ 各端预演/删除组键，
// 逐端覆盖态按「首条命中规则」派生。buildLibrary 为纯函数——real 与
// mock 两侧喂同一实现，单测直测规则面。
//
// 覆盖态判定（逐端逐键，首个命中规则胜出；row = 该端预演中同键的行）：
//
//	a. 行冲突 → conflict（原因 = 行 SkipReason）
//	b. 行版本更新且弱键 → confirm（疑似配对待用户确认）
//	c. 行版本更新强键且该端 Target → incoming
//	d. 行新增且该端 Target 且无跳过原因 → incoming
//	e. 该键在该端删除组 / 行属恢复组 → deleted
//	f. presence 命中 → has
//	g. none；原因取行 SkipReason，无行且非 Target → 「不写入」
//
// 行态 = 全端覆盖态的最坏序：conflict > confirm > incoming > ok。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"lzsync/internal/hub"
)

// 覆盖态枚举（ForkCoverage.State 取值域）。
const (
	covHas      = "has"
	covIncoming = "incoming"
	covConflict = "conflict"
	covConfirm  = "confirm"
	covDeleted  = "deleted"
	covNone     = "none"
)

// 行级态枚举（LibraryItem.State 取值域，§4.2 左缘色条）。
const (
	rowStateOK       = "ok"
	rowStateIncoming = "incoming"
	rowStateConflict = "conflict"
	rowStateConfirm  = "confirm"
)

// canonItem canonical 面一行（buildLibrary 的库内条目输入）。
type canonItem struct {
	ContentKey  string
	IdentityKey string // 仅谱面集有值
	Summary     string
}

// forkLibInput 单端输入面：Preview/Deleted 已按 kind 过滤；Presence 为
// 该 kind 的 presence 键集；Target = 该端的写入角色。
type forkLibInput struct {
	ForkID   string
	Target   bool
	Preview  []DiffRow
	Deleted  []DiffRow
	Presence map[string]bool
}

// buildLibrary 资料库行集推导（纯函数）。行集 = canonical 条目 ∪ 任一端
// 预演/删除组出现的同 kind 键；每行逐端一条覆盖态（按输入端序，调用方
// 已按 ForkID 排序）。
func buildLibrary(kind hub.EntityKind, canon []canonItem, in []forkLibInput) []LibraryItem {
	byKey := map[string]*LibraryItem{}
	itemFor := func(key string) *LibraryItem {
		it, ok := byKey[key]
		if !ok {
			it = &LibraryItem{Kind: kind, ContentKey: key}
			byKey[key] = it
		}
		return it
	}
	for _, c := range canon {
		it := itemFor(c.ContentKey)
		it.Summary = c.Summary
		it.IdentityKey = c.IdentityKey
	}
	for _, f := range in {
		for _, r := range f.Preview {
			it := itemFor(r.ContentKey)
			if it.Summary == "" {
				it.Summary = r.Summary
			}
			if it.IdentityKey == "" {
				it.IdentityKey = r.IdentityKey
			}
		}
		for _, r := range f.Deleted {
			it := itemFor(r.ContentKey)
			if it.Summary == "" {
				it.Summary = r.Summary
			}
			if it.IdentityKey == "" {
				it.IdentityKey = r.IdentityKey
			}
		}
	}

	// 逐端覆盖态。删除标记独立于覆盖态累计：键在某端删除组/恢复组行
	// 即为「有端删过」（即使该端覆盖态按规则序落 conflict 等更前规则）。
	for _, f := range in {
		previewByKey := map[string]*DiffRow{}
		for i := range f.Preview {
			r := &f.Preview[i]
			if _, ok := previewByKey[r.ContentKey]; !ok {
				previewByKey[r.ContentKey] = r
			}
		}
		deletedKeys := map[string]bool{}
		for i := range f.Deleted {
			r := &f.Deleted[i]
			deletedKeys[r.ContentKey] = true
			it := itemFor(r.ContentKey)
			it.Deleted = true
			if r.DeletedAt > it.DeletedAt {
				it.DeletedAt = r.DeletedAt
			}
		}
		for key, it := range byKey {
			row := previewByKey[key]
			state := ""
			reason := ""
			switch {
			case row != nil && row.Category == CategoryConflict:
				state, reason = covConflict, row.SkipReason
			case row != nil && row.Category == CategoryVersionUpdate && row.Evidence == EvidenceWeak:
				state, reason = covConfirm, row.SkipReason
			case row != nil && row.Category == CategoryVersionUpdate && f.Target:
				state = covIncoming
			case row != nil && row.Category == CategoryCreate && f.Target && row.SkipReason == "":
				state = covIncoming
			case deletedKeys[key] || (row != nil && row.Category == CategoryRestore):
				state = covDeleted
				it.Deleted = true
			case f.Presence[key]:
				state = covHas
			default:
				state = covNone
				if row != nil {
					reason = row.SkipReason
				}
				if reason == "" && !f.Target {
					reason = "不写入"
				}
			}
			it.Forks = append(it.Forks, ForkCoverage{ForkID: f.ForkID, State: state, Reason: reason})
		}
	}

	// 行级 worst-state。
	rank := map[string]int{covConflict: 3, covConfirm: 2, covIncoming: 1}
	for _, it := range byKey {
		worst := 0
		for _, fc := range it.Forks {
			if r := rank[fc.State]; r > worst {
				worst = r
			}
		}
		switch worst {
		case 3:
			it.State = rowStateConflict
		case 2:
			it.State = rowStateConfirm
		case 1:
			it.State = rowStateIncoming
		default:
			it.State = rowStateOK
		}
	}

	out := make([]LibraryItem, 0, len(byKey))
	for _, it := range byKey {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Summary), strings.ToLower(out[j].Summary)
		if a != b {
			return a < b
		}
		return out[i].ContentKey < out[j].ContentKey
	})
	return out
}

// --- 真实实现 ---

// libraryKinds 资料库页服务的四数据类。
var libraryKinds = map[hub.EntityKind]bool{
	hub.KindBeatmapSet: true, hub.KindCollection: true,
	hub.KindSkin: true, hub.KindScore: true,
}

// HubCounts 库内各类条目数（CompareService 实现）。
func (s *realCompare) HubCounts() (map[string]int, error) {
	ctx := context.Background()
	out := map[string]int{
		string(hub.KindBeatmapSet): 0, string(hub.KindCollection): 0,
		string(hub.KindSkin): 0, string(hub.KindScore): 0,
	}
	var sets, cols, skins, scores int
	// collection 的内容键即 Name（§4.2 第一层，PK 去重即 distinct 名）；
	// beatmap_set 只计链尾当前版本（superseded_by IS NULL，HB1）。
	err := s.deps.Hub.DB().QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM beatmap_set WHERE superseded_by IS NULL),
  (SELECT COUNT(*) FROM collection),
  (SELECT COUNT(*) FROM skin),
  (SELECT COUNT(*) FROM score)`).Scan(&sets, &cols, &skins, &scores)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读库内计数: %w", err)
	}
	out[string(hub.KindBeatmapSet)] = sets
	out[string(hub.KindCollection)] = cols
	out[string(hub.KindSkin)] = skins
	out[string(hub.KindScore)] = scores
	return out, nil
}

// canonItems 库内某类的 canonical 条目（谱面集只取当前版本，身份键走
// identity_version 锚、载荷现算兜底——与 buildUnits 同规则）。
func (s *realCompare) canonItems(ctx context.Context, kind hub.EntityKind, sum *summarizer) ([]canonItem, error) {
	var out []canonItem
	switch kind {
	case hub.KindBeatmapSet:
		anchor, err := s.setIdentityAnchor(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := s.deps.Hub.DB().QueryContext(ctx,
			`SELECT content_hash, payload FROM beatmap_set WHERE superseded_by IS NULL`)
		if err != nil {
			return nil, fmt.Errorf("appsvc: 读 canonical 谱面集: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var k, p string
			if err := rows.Scan(&k, &p); err != nil {
				return nil, fmt.Errorf("appsvc: 扫谱面集: %w", err)
			}
			ik := anchor[k]
			if ik == "" {
				var bs hub.BeatmapSet
				if json.Unmarshal([]byte(p), &bs) == nil {
					ik, _ = hub.BeatmapSetIdentityKey(&bs)
				}
			}
			out = append(out, canonItem{ContentKey: k, IdentityKey: ik, Summary: sum.summary(kind, k)})
		}
		return out, rows.Err()
	case hub.KindCollection:
		rows, err := s.deps.Hub.DB().QueryContext(ctx, `SELECT content_key FROM collection`)
		if err != nil {
			return nil, fmt.Errorf("appsvc: 读 canonical 收藏: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, fmt.Errorf("appsvc: 扫收藏: %w", err)
			}
			out = append(out, canonItem{ContentKey: k, Summary: sum.summary(kind, k)})
		}
		return out, rows.Err()
	case hub.KindSkin:
		rows, err := s.deps.Hub.DB().QueryContext(ctx, `SELECT content_hash FROM skin`)
		if err != nil {
			return nil, fmt.Errorf("appsvc: 读 canonical 皮肤: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, fmt.Errorf("appsvc: 扫皮肤: %w", err)
			}
			out = append(out, canonItem{ContentKey: k, Summary: sum.summary(kind, k)})
		}
		return out, rows.Err()
	case hub.KindScore:
		rows, err := s.deps.Hub.DB().QueryContext(ctx,
			`SELECT beatmap_hash || '|' || content_hash FROM score`)
		if err != nil {
			return nil, fmt.Errorf("appsvc: 读 canonical 成绩: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, fmt.Errorf("appsvc: 扫成绩: %w", err)
			}
			out = append(out, canonItem{ContentKey: k, Summary: sum.summary(kind, k)})
		}
		return out, rows.Err()
	}
	return nil, fmt.Errorf("appsvc: 资料库页不支持实体类型 %q", kind)
}

// Library 资料库页行集（CompareService 实现）：逐注册端跑预演+删除组+
// presence，喂 buildLibrary。判定叠加（G12 行态）与 PreviewDeploy 同规则。
func (s *realCompare) Library(kind hub.EntityKind) ([]LibraryItem, error) {
	if !libraryKinds[kind] {
		return nil, fmt.Errorf("appsvc: 资料库页不支持实体类型 %q", kind)
	}
	ctx := context.Background()
	sum := newSummarizer(s.deps.Hub)
	canon, err := s.canonItems(ctx, kind, sum)
	if err != nil {
		return nil, err
	}
	decs, ckIx := s.decisionOverlay(ctx)

	var ins []forkLibInput
	for _, ref := range s.reg.all() { // all() 已按 ForkID 排序
		role, _ := s.cfg.role(ref.ForkID)
		items, err := s.allReconcileItems(ctx, ref.ForkID)
		if err != nil {
			return nil, err
		}
		var preview []DiffRow
		for _, it := range items {
			if it.Kind != kind {
				continue
			}
			row := reconcileToRow(it, sum)
			if decs != nil && row.Category != CategoryRestore {
				overlayRowDecision(&row, decs, ckIx, ref.ForkID)
			}
			preview = append(preview, row)
		}
		tombs, err := s.deps.Hub.Tombstones(ctx, ref.ForkID)
		if err != nil {
			return nil, err
		}
		var deleted []DiffRow
		for key, at := range tombs[kind] {
			deleted = append(deleted, DiffRow{
				Kind: kind, ContentKey: key, Category: CategoryRestore,
				Summary: sum.summary(kind, key), DeletedAt: at,
			})
		}
		pres, err := s.deps.Hub.DB().QueryContext(ctx,
			`SELECT content_key FROM presence WHERE fork_id=? AND kind=?`, ref.ForkID, kind)
		if err != nil {
			return nil, fmt.Errorf("appsvc: 读 %s presence: %w", ref.ForkID, err)
		}
		presence := map[string]bool{}
		for pres.Next() {
			var k string
			if err := pres.Scan(&k); err != nil {
				pres.Close()
				return nil, fmt.Errorf("appsvc: 扫 %s presence: %w", ref.ForkID, err)
			}
			presence[k] = true
		}
		if err := pres.Err(); err != nil {
			pres.Close()
			return nil, err
		}
		pres.Close()
		ins = append(ins, forkLibInput{
			ForkID: ref.ForkID, Target: role.Target,
			Preview: preview, Deleted: deleted, Presence: presence,
		})
	}
	return buildLibrary(kind, canon, ins), nil
}
