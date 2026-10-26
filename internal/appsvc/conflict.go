package appsvc

// 冲突单位的逐端取值面（UnitConflict）：数据页冲突明细二级面板的数
// 据源——谁持有什么版本/值、与中枢值差在哪。行键按身份锚归并到判
// 定单位（谱面集用 identity_version 锚，收藏用 Name，键位三段身份，
// 其余内容键即身份）。
//
// 与判定层的关系：只读 presence + canonical 投影，不跑收集——面板展
// 示的是最近一轮同步后的快照态。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"lzsync/internal/hub"
)

// UnitConflict 单冲突单位的取值面（CompareService 实现）。
func (s *realCompare) UnitConflict(kind hub.EntityKind, contentKey string) (ConflictDetail, error) {
	ctx := context.Background()
	d := ConflictDetail{Kind: string(kind), ContentKey: contentKey}
	sum := newSummarizer(s.deps.Hub)
	parts := s.participants()

	switch kind {
	case hub.KindBeatmapSet:
		return s.unitConflictSet(ctx, contentKey, parts, sum, d)
	case hub.KindCollection:
		return s.unitConflictCollection(ctx, contentKey, parts, d)
	case hub.KindSkin, hub.KindScore,
		hub.KindRulesetSetting, hub.KindModPreset:
		return s.unitConflictScalar(ctx, kind, contentKey, parts, sum, d)
	case hub.KindKeyBinding:
		return s.unitConflictKeyBinding(ctx, contentKey, parts, d)
	default:
		return d, fmt.Errorf("appsvc: 冲突详情不支持实体类型 %q", kind)
	}
}

// participants 参与端（Source||Target，与判定面一致）。
func (s *realCompare) participants() []string {
	var out []string
	for _, ref := range s.reg.all() {
		role, ok := s.cfg.role(ref.ForkID)
		if !ok || (!role.Source && !role.Target) {
			continue
		}
		out = append(out, ref.ForkID)
	}
	sort.Strings(out)
	return out
}

// setIdentityAnchor 谱面集内容哈希 → 注册身份键（与判定层同序：强键
// 优先、序最小——异域双注册的确定性锚）。
func (s *realCompare) setIdentityAnchor(ctx context.Context) (map[string]string, error) {
	rows, err := s.deps.Hub.DB().QueryContext(ctx, `
SELECT content_hash, identity_key FROM identity_version
ORDER BY CASE WHEN identity_key LIKE 'online:%' THEN 0 ELSE 1 END, identity_key`)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读身份锚: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, ik string
		if err := rows.Scan(&h, &ik); err != nil {
			return nil, fmt.Errorf("appsvc: 扫身份锚: %w", err)
		}
		if _, ok := out[h]; !ok {
			out[h] = ik
		}
	}
	return out, rows.Err()
}

// unitConflictSet 谱面集冲突面：逐端该身份下持有的版本（hash → 标题），
// 中枢 = 传入键的版本链尾。
func (s *realCompare) unitConflictSet(ctx context.Context, contentKey string,
	parts []string, sum *summarizer, d ConflictDetail) (ConflictDetail, error) {
	anchor, err := s.setIdentityAnchor(ctx)
	if err != nil {
		return d, err
	}
	identity := anchor[contentKey]
	if identity == "" {
		// 未注册键兜底：载荷现算身份（与 buildUnits 同兜底规则）。
		var bs hub.BeatmapSet
		if lv, err := s.presenceValue(ctx, hub.KindBeatmapSet, contentKey); err == nil && lv != "" {
			if json.Unmarshal([]byte(lv), &bs) == nil {
				identity, _ = hub.BeatmapSetIdentityKey(&bs)
			}
		}
		if identity == "" {
			return d, fmt.Errorf("appsvc: 谱面集 %q 无身份注册，无法归并判定单位", contentKey)
		}
	}

	// 中枢值 = 传入键所在版本链的链尾（canonical 当前版本）。
	tail, err := s.chainTail(ctx, contentKey)
	if err != nil {
		return d, err
	}
	d.Canonical = versionSummary(ctx, s.deps.Hub, sum, tail)

	// 逐端该身份下持有的版本集。
	pres, err := s.presenceOfKind(ctx, hub.KindBeatmapSet, parts)
	if err != nil {
		return d, err
	}
	for _, f := range parts {
		v := ConflictVariant{ForkID: f}
		var held []string
		for _, key := range pres[f] {
			if anchor[key] == identity {
				held = append(held, key)
			}
		}
		sort.Strings(held)
		if len(held) > 0 {
			v.Has = true
			v.Key = held[0]
			var vs []string
			for _, h := range held {
				vs = append(vs, versionSummary(ctx, s.deps.Hub, sum, h))
			}
			v.Value = strings.Join(vs, " ；")
		} else {
			v.Value = "缺席"
		}
		d.Variants = append(d.Variants, v)
	}
	return d, nil
}

// versionSummary 谱面集版本的人话摘要（canonical 标题 + hash 短码；
// canonical 缺行时退化 hash 短码——异域版本未在 canonical 表属正常）。
func versionSummary(ctx context.Context, h *hub.Store, sum *summarizer, hash string) string {
	return sum.summary(hub.KindBeatmapSet, hash) + " @" + shortHash(hash)
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// chainTail 谱面集内容键的版本链尾（hub.resolveChainTail 的本地等价——
// 行缺失/成环按当前行停走）。
func (s *realCompare) chainTail(ctx context.Context, hash string) (string, error) {
	seen := map[string]bool{hash: true}
	cur := hash
	for {
		var next *string
		err := s.deps.Hub.DB().QueryRowContext(ctx,
			`SELECT superseded_by FROM beatmap_set WHERE content_hash=?`, cur).Scan(&next)
		if err != nil {
			return cur, nil // 无行按当前键停走（presence 侧键不在 canonical 属正常）
		}
		if next == nil || *next == "" || seen[*next] {
			return cur, nil
		}
		seen[*next] = true
		cur = *next
	}
}

// presenceValue 某内容键在任意端的 presence 载荷（身份兜底推算用）。
func (s *realCompare) presenceValue(ctx context.Context, kind hub.EntityKind, key string) (string, error) {
	var lv string
	err := s.deps.Hub.DB().QueryRowContext(ctx,
		`SELECT local_values FROM presence WHERE kind=? AND content_key=? LIMIT 1`, kind, key).Scan(&lv)
	if err != nil {
		return "", nil
	}
	return lv, nil
}

// presenceOfKind 某类的 presence 键集（fork → content_key 列表，参与端限定）。
func (s *realCompare) presenceOfKind(ctx context.Context, kind hub.EntityKind,
	parts []string) (map[string][]string, error) {
	out := map[string][]string{}
	rows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT fork_id, content_key FROM presence WHERE kind=?`, kind)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读 presence(%s): %w", kind, err)
	}
	defer rows.Close()
	part := map[string]bool{}
	for _, f := range parts {
		part[f] = true
	}
	for rows.Next() {
		var f, k string
		if err := rows.Scan(&f, &k); err != nil {
			return nil, fmt.Errorf("appsvc: 扫 presence(%s): %w", kind, err)
		}
		if part[f] {
			out[f] = append(out[f], k)
		}
	}
	return out, rows.Err()
}

// presenceValues 某类的 presence 载荷（fork → content_key → local_values）。
func (s *realCompare) presenceValues(ctx context.Context, kind hub.EntityKind,
	parts []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	rows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT fork_id, content_key, local_values FROM presence WHERE kind=?`, kind)
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读 presence(%s): %w", kind, err)
	}
	defer rows.Close()
	part := map[string]bool{}
	for _, f := range parts {
		part[f] = true
	}
	for rows.Next() {
		var f, k, lv string
		if err := rows.Scan(&f, &k, &lv); err != nil {
			return nil, fmt.Errorf("appsvc: 扫 presence(%s): %w", kind, err)
		}
		if !part[f] {
			continue
		}
		m, ok := out[f]
		if !ok {
			m = map[string]string{}
			out[f] = m
		}
		m[k] = lv
	}
	return out, rows.Err()
}

// unitConflictCollection 收藏夹冲突面：中枢/逐端成员数与增减明细。
func (s *realCompare) unitConflictCollection(ctx context.Context, contentKey string,
	parts []string, d ConflictDetail) (ConflictDetail, error) {
	var canonJSON string
	err := s.deps.Hub.DB().QueryRowContext(ctx,
		`SELECT payload FROM collection WHERE content_key=?`, contentKey).Scan(&canonJSON)
	var canon hub.Collection
	if err == nil {
		if err := json.Unmarshal([]byte(canonJSON), &canon); err != nil {
			return d, fmt.Errorf("appsvc: 解码收藏 canonical: %w", err)
		}
	}
	canonMembers := map[string]bool{}
	for _, m := range canon.BeatmapMD5Hashes {
		canonMembers[m] = true
	}
	d.Canonical = fmt.Sprintf("%d 项成员", len(canon.BeatmapMD5Hashes))

	pres, err := s.presenceValues(ctx, hub.KindCollection, parts)
	if err != nil {
		return d, err
	}
	for _, f := range parts {
		v := ConflictVariant{ForkID: f}
		lv, ok := pres[f][contentKey]
		if !ok {
			v.Value = "缺席"
			d.Variants = append(d.Variants, v)
			continue
		}
		var c hub.Collection
		if err := json.Unmarshal([]byte(lv), &c); err != nil {
			return d, fmt.Errorf("appsvc: 解码 %s 收藏 %q: %w", f, contentKey, err)
		}
		v.Has, v.Key, v.Value = true, contentKey, fmt.Sprintf("%d 项成员", len(c.BeatmapMD5Hashes))
		forkMembers := map[string]bool{}
		for _, m := range c.BeatmapMD5Hashes {
			forkMembers[m] = true
		}
		onlyHub, onlyFork := 0, 0
		for m := range canonMembers {
			if !forkMembers[m] {
				onlyHub++
			}
		}
		for m := range forkMembers {
			if !canonMembers[m] {
				onlyFork++
			}
		}
		if onlyHub+onlyFork > 0 {
			v.Fields = append(v.Fields, FieldDiff{
				Field:     "成员差异",
				HubValue:  fmt.Sprintf("仅中枢 %d 项", onlyHub),
				ForkValue: fmt.Sprintf("仅该端 %d 项", onlyFork),
			})
		}
		d.Variants = append(d.Variants, v)
	}
	return d, nil
}

// unitConflictScalar 标量类冲突面（skin/score/ruleset_setting/mod_preset）：
// canonical 载荷 vs 逐端投影，逐字段差异。
func (s *realCompare) unitConflictScalar(ctx context.Context, kind hub.EntityKind,
	contentKey string, parts []string, sum *summarizer, d ConflictDetail) (ConflictDetail, error) {
	canonJSON, err := s.canonicalPayload(ctx, kind, contentKey)
	if err != nil {
		return d, err
	}
	if canonJSON == "" {
		return d, fmt.Errorf("appsvc: %s %q 无 canonical 行", kind, contentKey)
	}
	d.Canonical = scalarSummary(kind, canonJSON, sum, contentKey)

	pres, err := s.presenceValues(ctx, kind, parts)
	if err != nil {
		return d, err
	}
	for _, f := range parts {
		v := ConflictVariant{ForkID: f}
		lv, ok := pres[f][contentKey]
		if !ok {
			v.Value = "缺席"
			d.Variants = append(d.Variants, v)
			continue
		}
		fields, err := scalarFields(kind, canonJSON, lv)
		if err != nil {
			return d, fmt.Errorf("appsvc: %s %q @%s: %w", kind, contentKey, f, err)
		}
		v.Has, v.Key = true, contentKey
		v.Value = scalarSummary(kind, lv, sum, contentKey)
		v.Fields = fields
		d.Variants = append(d.Variants, v)
	}
	return d, nil
}

// canonicalPayload 各类 canonical 载荷查询（内容键→payload）。
func (s *realCompare) canonicalPayload(ctx context.Context, kind hub.EntityKind, key string) (string, error) {
	var q string
	switch kind {
	case hub.KindCollection:
		q = `SELECT payload FROM collection WHERE content_key=?`
	case hub.KindSkin:
		q = `SELECT payload FROM skin WHERE content_hash=?`
	case hub.KindScore:
		q = `SELECT payload FROM score WHERE beatmap_hash || '|' || content_hash=?`
	case hub.KindRulesetSetting:
		q = `SELECT payload FROM ruleset_setting WHERE ruleset_name || '|' || variant || '|' || key=?`
	case hub.KindModPreset:
		q = `SELECT payload FROM mod_preset WHERE ruleset_short_name || '|' || name=?`
	case hub.KindKeyBinding:
		q = `SELECT payload FROM key_binding` // 调用侧按键位身份过滤
	default:
		return "", fmt.Errorf("appsvc: 无 canonical 查询的实体类型 %q", kind)
	}
	var p string
	err := s.deps.Hub.DB().QueryRowContext(ctx, q, key).Scan(&p)
	if err != nil {
		return "", nil // 无 canonical 行（异域内容等）——上层决定话术
	}
	return p, nil
}

// scalarSummary 标量类载荷 → 人话摘要（面板「值」列）。
func scalarSummary(kind hub.EntityKind, payload string, sum *summarizer, key string) string {
	switch kind {
	case hub.KindSkin:
		var sk hub.Skin
		if json.Unmarshal([]byte(payload), &sk) == nil {
			return fmt.Sprintf("%s（%s）", sk.Name, sk.Creator)
		}
	case hub.KindScore:
		var sc hub.Score
		if json.Unmarshal([]byte(payload), &sc) == nil {
			return fmt.Sprintf("%d 分 · %s", sc.TotalScore, sc.Date.Format("2006-01-02"))
		}
	case hub.KindRulesetSetting:
		var rs hub.RulesetSetting
		if json.Unmarshal([]byte(payload), &rs) == nil {
			return rs.Value
		}
	case hub.KindModPreset:
		var mp hub.ModPreset
		if json.Unmarshal([]byte(payload), &mp) == nil {
			if mp.Description != "" {
				return mp.Description
			}
			return mp.ModsJson
		}
	}
	return sum.summary(kind, key)
}

// scalarFields 标量类 canonical vs presence 的字段级差异（面板明细列）。
func scalarFields(kind hub.EntityKind, canonJSON, localJSON string) ([]FieldDiff, error) {
	add := func(out *[]FieldDiff, field, a, b string) {
		if a != b {
			*out = append(*out, FieldDiff{Field: field, HubValue: a, ForkValue: b})
		}
	}
	var out []FieldDiff
	switch kind {
	case hub.KindSkin:
		var a, b hub.Skin
		if err := decodeReconcilePayloads(canonJSON, localJSON, &a, &b); err != nil {
			return nil, err
		}
		add(&out, "Name", a.Name, b.Name)
		add(&out, "Creator", a.Creator, b.Creator)
	case hub.KindScore:
		var a, b hub.Score
		if err := decodeReconcilePayloads(canonJSON, localJSON, &a, &b); err != nil {
			return nil, err
		}
		add(&out, "Date", a.Date.Format("2006-01-02 15:04:05"), b.Date.Format("2006-01-02 15:04:05"))
		add(&out, "TotalScore", fmt.Sprint(a.TotalScore), fmt.Sprint(b.TotalScore))
		add(&out, "RankInt", fmt.Sprint(a.RankInt), fmt.Sprint(b.RankInt))
	case hub.KindRulesetSetting:
		var a, b hub.RulesetSetting
		if err := decodeReconcilePayloads(canonJSON, localJSON, &a, &b); err != nil {
			return nil, err
		}
		add(&out, "Value", a.Value, b.Value)
	case hub.KindModPreset:
		var a, b hub.ModPreset
		if err := decodeReconcilePayloads(canonJSON, localJSON, &a, &b); err != nil {
			return nil, err
		}
		add(&out, "Description", a.Description, b.Description)
		add(&out, "ModsJson", a.ModsJson, b.ModsJson)
	}
	return out, nil
}

// unitConflictKeyBinding 键位冲突面：三段身份下逐端按键组合集差异。
func (s *realCompare) unitConflictKeyBinding(ctx context.Context, contentKey string,
	parts []string, d ConflictDetail) (ConflictDetail, error) {
	rn, v, ai, _, err := hub.DecodeKeyBindingContentKey(contentKey)
	if err != nil {
		return d, fmt.Errorf("appsvc: 键位内容键解码 %q: %w", contentKey, err)
	}
	identity := hub.KeyBindingIdentityKey(rn, v, ai)

	// 中枢组合集：canonical key_binding 全行按身份过滤。
	rows, err := s.deps.Hub.DB().QueryContext(ctx, `SELECT payload FROM key_binding`)
	if err != nil {
		return d, fmt.Errorf("appsvc: 读键位 canonical: %w", err)
	}
	canonCombos := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return d, fmt.Errorf("appsvc: 扫键位 canonical: %w", err)
		}
		var kb hub.KeyBinding
		if json.Unmarshal([]byte(p), &kb) != nil {
			continue
		}
		if hub.KeyBindingIdentityKey(kb.RulesetName, kb.Variant, kb.ActionInt) == identity {
			canonCombos[kb.KeyCombination] = true
		}
	}
	rows.Close()
	d.Canonical = comboSummary(canonCombos)

	pres, err := s.presenceValues(ctx, hub.KindKeyBinding, parts)
	if err != nil {
		return d, err
	}
	for _, f := range parts {
		v := ConflictVariant{ForkID: f}
		forkCombos := map[string]bool{}
		for _, lv := range pres[f] {
			var kb hub.KeyBinding
			if json.Unmarshal([]byte(lv), &kb) != nil {
				continue
			}
			if hub.KeyBindingIdentityKey(kb.RulesetName, kb.Variant, kb.ActionInt) == identity {
				forkCombos[kb.KeyCombination] = true
			}
		}
		if len(forkCombos) == 0 {
			v.Value = "缺席"
			d.Variants = append(d.Variants, v)
			continue
		}
		v.Has, v.Key, v.Value = true, contentKey, comboSummary(forkCombos)
		v.Fields = []FieldDiff{{
			Field:     "KeyCombination",
			HubValue:  comboSummary(canonCombos),
			ForkValue: comboSummary(forkCombos),
		}}
		d.Variants = append(d.Variants, v)
	}
	return d, nil
}

func comboSummary(set map[string]bool) string {
	if len(set) == 0 {
		return "（空）"
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
