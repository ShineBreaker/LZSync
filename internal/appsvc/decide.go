package appsvc

// §0.3 基线三方判定引擎（G7/G8 主体）：
//
// 输入面 = 各端 presence 投影（当前值 v(f,I) 的来源，含未参与本轮收集
// 端的最后已知值）+ baseline 表（b(f,I)，上轮同步结束时的取值指纹）+
// game.ini 快照仓（逐键）+ beatmap_identity 证据强度 + 各端 tombstone。
//
// 判定模型（逐同步身份，design.md §0.3）：
//  1. 有任一端持基线 → 候选 = 变更来源 Δ = {v(f) : f∈Source 且 f 有基线
//     且 v(f)≠b(f) 且 v(f) 非缺席}；无基线 → 候选 = 全部来源端持有值。
//  2. 候选多值 → 冲突（各端不动）；唯一 → 决定值 v*。
//  3. 目标端写入前提：v(t)=b(t)（未自行改动）；目标端自行改动（含删除）
//     且 ≠v* → 保护跳过并计冲突；无基线的不同取值同样计冲突——新端的
//     异值不得压过既有取值。
//  4. 缺席不是变更来源（B6）：删除不传播、也不复活；目标端在决定值上的
//     tombstone 命中 → 恢复组（需用户显式处置）。
//
// 判定粒度按 §0.3 表：收藏夹逐成员（member=md5，值 "1"/"0"）、键位按
// 三段身份（值 = 该动作组合集）、game.ini 逐白名单键、其余按内容键/
// 谱面身份键整体。
//
// 引擎纯函数化：buildUnits 提取取值面 → decideAll 逐单位判定 →
// assemblePlans 装配逐端计划；产物 outcome 同时是基线推进面
// （advanceBaselines 在一轮结束后按最终快照重算取值再写基线）。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"lzsync/internal/deploy"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/inisettings"
)

// unitKey 判定单位 = (kind, identity, member)；member 仅收藏逐成员
// 判定使用（其他 kind 恒 ""）。kind 对 game.ini 是伪 kind
// hub.BaselineKindGameIni。
type unitKey struct {
	kind     string
	identity string
	member   string
}

// unitVal 一端一单位的当前值：fp = 同步值指纹（BaselineAbsent = 已知
// 缺席）；payload = 该端该单位的投影载荷（local_values JSON / ini 键值
// 原文），决定值写入面消费。
type unitVal struct {
	fp      string
	payload string
}

// unitRow 一个判定单位的全部输入：各端当前值 + 各端基线值（仅有行的端）。
type unitRow struct {
	cur  map[string]unitVal // fork → 当前值（无条目 = 无 presence/快照行 = 缺席）
	base map[string]string  // fork → 基线值（无条目 = 无同步史）
}

// unitDecision 判定结果。
type unitDecision struct {
	decided   bool            // 唯一候选 → 有决定值
	fp        string          // 决定值指纹
	winner    string          // 决定值来源端（fp 的首个候选来源）
	conflict  bool            // 候选多值 → 全局冲突（任何端不动）
	writable  map[string]bool // 目标端可写 v*（未改动/缺席/已收敛）
	protected map[string]bool // 目标端自行改动且 ≠v* → 保护跳过
	divergent map[string]bool // 无基线不同取值（新端异值）→ 冲突
	forced    bool            // 用户拍板构造（勾选处置以某端为准）：弱键/tombstone 门降级为 NeedsConfirm/Restore 项而非搁置
}

// decideOutcome 引擎产物。
type decideOutcome struct {
	plans   map[string][]deploy.PlanItem          // 目标端 → 计划项
	ini     map[string]map[string]deploy.IniValue // 目标端 → ini 键 → 决定值
	pending PendingSummary                        // 待处理计数（全局去重）
	units   map[unitKey]*unitRow                  // 评估过的全部单位（基线推进面）
	decs    map[unitKey]*unitDecision             // 单位 → 判定
	in      *decideInput                          // 引擎输入面（勾选处置的胜者装配复用）
}

// decideInput 引擎输入（run.go 装载）。
type decideInput struct {
	presence  map[string]map[hub.EntityKind]map[string]string // fork → kind → 内容键 → local_values
	baseline  map[unitKey]map[string]string                   // 单位 → fork → 基线值
	ini       map[string]map[string]string                    // fork → 键 → 值（仅有快照端）
	sources   []string                                        // 全部 Source 端（含本轮跳过——最后已知值）
	targets   []string                                        // 全部 Target 端（含本轮跳过）
	live      map[string]bool                                 // 本轮未跳过的目标端（出计划者）
	kinds     map[hub.EntityKind]bool                         // 开启板块实体类
	gameIni   bool                                            // ini 板块开关
	tombs     map[string]map[hub.EntityKind]map[string]int64  // 目标端 → kind → 键 → deletedAt
	evidence  map[string]bool                                 // beatmap identity_key → 强键已确认可用
	hashIdent map[string]string                               // 内容哈希 → 注册身份键（identity_version 表锚）
	canon     *canonicalIndex                                 // canonical 载荷索引（create 的载荷来源）
	sum       *summarizer
}

// canonicalIndex canonical 载荷索引（判定单位 → 写面载荷/身份键）。
// 决定值必须落库可查：谱面集 create 按内容键、收藏 create 以 canonical
// 行为底（成员集替换为决定集）、成绩载荷供名录交集预检。
type canonicalIndex struct {
	collections map[string]string // name → canonical payload JSON
	sets        map[string]bool   // 内容键 → canonical 行存在
	skins       map[string]string // 内容键 → canonical payload JSON（G11：交集预检要 Protected/指纹段）
	scores      map[string]string // 组合内容键 → canonical payload JSON
	kbs         map[string]bool   // 四段内容键 → canonical 行存在
	settings    map[string]bool
	presets     map[string]bool
}

// absentEq 各类「缺席等价值」判定：实体类 = BaselineAbsent；收藏成员
// = BaselineAbsent 或 "0"（持有收藏但缺成员——新成员补缺不是冲突）。
func absentEq(kind string, v string) bool {
	if v == hub.BaselineAbsent {
		return true
	}
	return kind == string(hub.KindCollection) && v == "0"
}

// decideUnit 单单位三方判定（§0.3）。sources/targets 按 ForkID 排序传入
// （胜者端取候选中序最前者——同值多端时确定性）。
func decideUnit(key unitKey, row *unitRow, sources, targets []string) *unitDecision {
	d := &unitDecision{
		writable:  map[string]bool{},
		protected: map[string]bool{},
		divergent: map[string]bool{},
	}
	cur := func(f string) string {
		if v, ok := row.cur[f]; ok {
			return v.fp
		}
		return hub.BaselineAbsent
	}
	// 缺席等价归一：收藏成员的基线 "0"（持夹缺成员）与无行（缺席）是同一
	// 语义态——按 absentEq 折叠后再比，否则「收藏在而成员缺席」被误判成
	// 端自行改动。
	eq := func(a, b string) bool {
		return a == b || (absentEq(key.kind, a) && absentEq(key.kind, b))
	}
	// 候选集：fp → 首个持有来源端。
	cands := map[string]string{}
	if len(row.base) > 0 {
		for _, f := range sources { // 变更来源 Δ
			b, hasB := row.base[f]
			v := cur(f)
			if hasB && !eq(v, b) && !absentEq(key.kind, v) {
				if _, ok := cands[v]; !ok {
					cands[v] = f
				}
			}
		}
	} else {
		for _, f := range sources { // 无同步史：全部来源持有值为候选
			v := cur(f)
			if !absentEq(key.kind, v) {
				if _, ok := cands[v]; !ok {
					cands[v] = f
				}
			}
		}
	}
	switch len(cands) {
	case 0:
		return d // 无候选 → 保留差异（各端不动）
	case 1:
		for fp, w := range cands {
			d.decided, d.fp, d.winner = true, fp, w
		}
	default:
		d.conflict = true
		return d
	}
	// 参与端评估（规则 3/4/5——规则 5「无基线的不同取值」覆盖全部参与端，
	// 源端的新异值同样计冲突；writable 只被目标端消费，源端多标记无害）。
	seen := map[string]bool{}
	for _, t := range append(append([]string{}, targets...), sources...) {
		if seen[t] {
			continue
		}
		seen[t] = true
		b, hasB := row.base[t]
		v := cur(t)
		switch {
		case hasB && eq(v, b): // 未改动（缺席等价归一后同值）
			if v != d.fp {
				d.writable[t] = true
			}
		case hasB: // 自行改动（含删除：v 为缺席值仍与基线不同）
			if v == d.fp {
				// 已收敛到决定值——无写无冲突
			} else {
				d.protected[t] = true
			}
		case absentEq(key.kind, v): // 无基线且缺席 → 可补缺
			d.writable[t] = true
		case v == d.fp: // 无基线但值恰是决定值 → 已一致
		default: // 无基线的不同取值 → 冲突（规则 5）
			d.divergent[t] = true
		}
	}
	return d
}

// buildUnits 提取判定面：presence/ini → 单位取值表。只纳入开启板块与
// Source||Target 端的行；其余端 presence 行不参与判定（B9）。
// 第二返回是「内容键 → 判定单位」索引（kind\x00ck → 单位列表）：数据
// 浏览页把对账行态叠加到三方判定结果时用（G12）——谱面集单位按身份键、
// 键位按三段身份、收藏按哨兵+逐成员，行级内容键不直接等于单位身份。
func buildUnits(in *decideInput) (map[unitKey]*unitRow, map[string][]unitKey) {
	units := map[unitKey]*unitRow{}
	ckIx := map[string][]unitKey{}
	ix := func(kind hub.EntityKind, ck string, uk unitKey) {
		k := string(kind) + "\x00" + ck
		ckIx[k] = append(ckIx[k], uk)
	}
	rowOf := func(k unitKey) *unitRow {
		r, ok := units[k]
		if !ok {
			r = &unitRow{cur: map[string]unitVal{}, base: map[string]string{}}
			units[k] = r
		}
		return r
	}
	participating := map[string]bool{}
	for _, f := range in.sources {
		participating[f] = true
	}
	for _, f := range in.targets {
		participating[f] = true
	}

	for _, fork := range sortedKeys(in.presence) {
		if !participating[fork] {
			continue
		}
		for kind, rows := range in.presence[fork] {
			if !in.kinds[kind] {
				continue
			}
			switch kind {
			case hub.KindCollection:
				for ck, lv := range rows {
					var c hub.Collection
					if json.Unmarshal([]byte(lv), &c) != nil {
						continue // 单行坏载荷不挡路（投影层已计跳过）
					}
					name := c.Name
					if name == "" {
						name = ck
					}
					// 身份级哨兵单位（每条收藏行都产）：持有态判定与
					// 「目标端是否持有该收藏」的事实面——空收藏靠它才能
					// 进判定；有成员的收藏也靠它区分「未持有」与「空集」。
					sentinel := unitKey{string(kind), name, ""}
					r := rowOf(sentinel)
					r.cur[fork] = unitVal{fp: "held", payload: lv}
					ix(kind, ck, sentinel)
					for _, m := range c.BeatmapMD5Hashes {
						mu := unitKey{string(kind), name, m}
						rm := rowOf(mu)
						rm.cur[fork] = unitVal{fp: "1", payload: lv}
						ix(kind, ck, mu)
					}
				}
			case hub.KindBeatmapSet:
				for ck, lv := range rows {
					var bs hub.BeatmapSet
					if json.Unmarshal([]byte(lv), &bs) != nil {
						continue
					}
					// 身份锚 = hub 注册表（identity_version），载荷现算只做
					// 兜底：S8 异域写入把 OnlineID 置 -1 → 同一物理集在不同
					// 服务器域会算出不同身份键（online:<id> vs meta:<…>），
					// 用载荷现算单位身份会让三端永不收敛、互相重灌全量。
					ik := in.hashIdent[ck]
					if ik == "" {
						ik, _ = hub.BeatmapSetIdentityKey(&bs)
					}
					uk := unitKey{string(kind), ik, ""}
					r := rowOf(uk)
					hashes := decodeSetFP(r.cur[fork].fp)
					hashes = append(hashes, ck)
					sort.Strings(hashes)
					r.cur[fork] = unitVal{fp: encodeSetFP(hashes)}
					ix(kind, ck, uk)
				}
			case hub.KindRulesetSetting:
				for ck, lv := range rows {
					var rs hub.RulesetSetting
					if json.Unmarshal([]byte(lv), &rs) != nil {
						continue
					}
					uk := unitKey{string(kind), ck, ""}
					r := rowOf(uk)
					r.cur[fork] = unitVal{fp: rs.Value, payload: lv}
					ix(kind, ck, uk)
				}
			case hub.KindModPreset:
				for ck, lv := range rows {
					var mp hub.ModPreset
					if json.Unmarshal([]byte(lv), &mp) != nil {
						continue
					}
					uk := unitKey{string(kind), ck, ""}
					r := rowOf(uk)
					r.cur[fork] = unitVal{fp: mp.Description + "\x00" + mp.ModsJson, payload: lv}
					ix(kind, ck, uk)
				}
			case hub.KindKeyBinding:
				for ck, lv := range rows {
					var kb hub.KeyBinding
					if json.Unmarshal([]byte(lv), &kb) != nil {
						continue
					}
					ik := hub.KeyBindingIdentityKey(kb.RulesetName, kb.Variant, kb.ActionInt)
					uk := unitKey{string(kind), ik, ""}
					ix(kind, ck, uk)
					r := rowOf(uk)
					combos := decodeSetFP(r.cur[fork].fp)
					found := false
					for _, c := range combos {
						if c == kb.KeyCombination {
							found = true
							break
						}
					}
					if !found {
						combos = append(combos, kb.KeyCombination)
						sort.Strings(combos)
					}
					// payload 保留首行：裁决载荷以它为模板改组合+新 Guid。
					lv0 := r.cur[fork].payload
					if lv0 == "" {
						lv0 = lv
					}
					r.cur[fork] = unitVal{fp: encodeSetFP(combos), payload: lv0}
				}
			case hub.KindSkin, hub.KindScore:
				for ck := range rows {
					uk := unitKey{string(kind), ck, ""}
					r := rowOf(uk)
					r.cur[fork] = unitVal{fp: "held"}
					ix(kind, ck, uk)
				}
			}
		}
	}

	// game.ini 逐键（仅开启板块 + 有快照端）。
	if in.gameIni {
		for _, fork := range sortedKeys(in.ini) {
			if !participating[fork] {
				continue
			}
			for k, v := range in.ini[fork] {
				r := rowOf(unitKey{hub.BaselineKindGameIni, k, ""})
				r.cur[fork] = unitVal{fp: v, payload: v}
			}
		}
	}

	// 基线并入：身份从全部端消失后仍有基线行 → 单位照常评估（决定缺席）。
	for uk, forks_ := range in.baseline {
		if uk.kind == hub.BaselineKindGameIni && !in.gameIni {
			continue
		}
		if uk.kind != hub.BaselineKindGameIni && !in.kinds[hub.EntityKind(uk.kind)] {
			continue
		}
		r := rowOf(uk)
		for f, v := range forks_ {
			if participating[f] {
				r.base[f] = v
			}
		}
	}
	return units, ckIx
}

// decideAll 逐单位判定。
func decideAll(units map[unitKey]*unitRow, sources, targets []string) map[unitKey]*unitDecision {
	out := map[unitKey]*unitDecision{}
	for uk, row := range units {
		out[uk] = decideUnit(uk, row, sources, targets)
	}
	return out
}

// encodeSetFP/decodeSetFP 集合值指纹编码（键位组合集/谱面版本集/收藏
// 成员集：JSON 数组，元素排序后在调用侧完成）。
func encodeSetFP(set []string) string {
	b, _ := json.Marshal(set)
	return string(b)
}

func decodeSetFP(fp string) []string {
	if fp == "" || fp == hub.BaselineAbsent {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(fp), &out) != nil {
		return nil
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- 装配：判定 → 逐端计划项 / ini 决定值 / 待处理计数 ----

// assemblePlans 遍历判定结果，为每个 live 目标端产出计划项。写入形态：
//   - collection：逐成员判定合并出目标端应有成员集 → 整集 create/update
//     一项（PayloadJSON 携带决定成员集；LastModified 不写——B7）；
//   - beatmap_set：决定值必须是单版本集（多版本候选不可投放 → 冲突）；
//     目标端持不同版本 → version-replace（弱键未确认 → NeedsConfirm 不
//     入计划）；目标端缺席 → create（tombstone 命中 → Restore 不入计划）；
//   - ruleset_setting/mod_preset：PayloadJSON = 胜者端投影载荷（create
//     与 update 同形）；规则集/Mod 名录全局交集预检不过 → Unsupported；
//   - key_binding：决定组合集对目标做差集——缺的逐组合 create（载荷
//     模板取胜者端行、换 Guid）、多的逐组合 delete；
//   - skin/score：内容键即值，只补缺（score 先过名录全局交集预检）；
//   - game_ini：逐键决定值入 ini 决定图（目标端可写才进）。
//
// 全部路径：decided=false（无候选）与 conflict/protected/divergent 一律
// 不出计划项——判不准不动，保留差异。
func assemblePlans(in *decideInput, units map[unitKey]*unitRow,
	decs map[unitKey]*unitDecision) *decideOutcome {
	out := &decideOutcome{
		plans:   map[string][]deploy.PlanItem{},
		ini:     map[string]map[string]deploy.IniValue{},
		units:   units,
		decs:    decs,
		pending: PendingSummary{ByKind: map[string]int{}},
	}
	emitted := map[string]map[string]bool{} // target → kind\x00key 去重（谱面集同 hash 可挂多身份键）
	addItem := func(target string, it deploy.PlanItem) {
		k := string(it.Kind) + "\x00" + it.ContentKey
		m, ok := emitted[target]
		if !ok {
			m = map[string]bool{}
			emitted[target] = m
		}
		if m[k] {
			return
		}
		m[k] = true
		out.plans[target] = append(out.plans[target], it)
	}
	// 收藏按 (目标端, 身份) 聚合哨兵+成员级判定 → 单条集级计划项。
	// 成员级「应有集」= 每个成员单位独立的判定落点：已判定且本端可写 →
	// 决定值；其余（冲突/保护/无候选）→ 保留目标端当前成员态（B12）。
	// 哨兵单位判定「持有」本身：决定 held 且目标端未持有 → 建收藏
	// （成员集 = 各成员判定落点）。
	type colAcc struct {
		final   map[string]bool // 应有成员集
		heldNow bool            // 目标端当前持有该收藏（哨兵或成员行）
		changed bool
	}
	colAccs := map[string]map[string]*colAcc{} // target → identity → acc
	accOf := func(t, identity string) *colAcc {
		m, ok := colAccs[t]
		if !ok {
			m = map[string]*colAcc{}
			colAccs[t] = m
		}
		a, ok := m[identity]
		if !ok {
			a = &colAcc{final: map[string]bool{}}
			m[identity] = a
		}
		return a
	}
	for uk, row := range units {
		if uk.kind != string(hub.KindCollection) {
			continue
		}
		dec := decs[uk]
		for _, t := range in.targets {
			a := accOf(t, uk.identity)
			if uk.member == "" {
				// 哨兵：持有态。决定 held 且本端未持有 → 变化（建收藏）。
				_, held := row.cur[t]
				a.heldNow = a.heldNow || held
				if dec.decided && dec.writable[t] && dec.fp == "held" && !held {
					a.changed = true
				}
				continue
			}
			curHas := false
			if v, ok := row.cur[t]; ok && v.fp == "1" {
				curHas = true
				a.heldNow = true
			}
			want := curHas // 默认保留目标端现状
			if dec.decided && dec.writable[t] && dec.fp == "1" {
				want = true
			}
			// 拍板成员（forced）：胜者端不持有 → 决定值「无」显式移除
			// （正常轮无「决定缺席」——删除不传播，保留差异）。
			if dec.decided && dec.writable[t] && dec.forced {
				want = dec.fp == "1"
			}
			a.final[uk.member] = want
			if want != curHas {
				a.changed = true
			}
		}
	}

	for uk, dec := range decs {
		row := units[uk]
		if dec.conflict || len(dec.protected) > 0 || len(dec.divergent) > 0 {
			out.pending.Conflicts++
			out.pending.ByKind[uk.kind]++
		}
		if !dec.decided {
			continue // 无候选 → 保留差异
		}
		switch uk.kind {
		case string(hub.KindCollection):
			continue // 成员级判定已在上方聚合
		case string(hub.KindBeatmapSet):
			hashes := decodeSetFP(dec.fp)
			if len(hashes) != 1 {
				// 多版本持有端当候选 → 无法决定单版本投放 → 冲突。
				out.pending.Conflicts++
				out.pending.ByKind[uk.kind]++
				dec.conflict = true
				continue
			}
			want := hashes[0]
			for _, t := range in.targets {
				if !dec.writable[t] {
					continue
				}
				cur := decodeSetFP(row.cur[t].fp)
				if len(cur) == 1 && cur[0] == want {
					continue
				}
				if len(cur) > 1 {
					// 目标端自身持多版本 → 判不准，保护计冲突。
					out.pending.Conflicts++
					out.pending.ByKind[uk.kind]++
					dec.protected[t] = true
					continue
				}
				if len(cur) == 0 {
					// 补缺：身份级 tombstone（任一版本被删过）→ 恢复组；
					// 拍板项（forced）显式恢复——Restore 置位交投放清标记。
					if tombstoneHit(in.tombs[t], hub.KindBeatmapSet, want) ||
						identityTombstoned(in.tombs[t], hub.KindBeatmapSet, row) {
						if !dec.forced {
							out.pending.Restore++
							continue
						}
						if !in.live[t] {
							continue
						}
						if !in.canon.sets[want] {
							continue // canonical 无该版本行（不该发生，宁缺）
						}
						addItem(t, deploy.PlanItem{
							Kind: hub.KindBeatmapSet, ContentKey: want,
							Action: deploy.ActionCreate, SourceFork: dec.winner,
							Restore: true,
							Summary: in.sum.summary(hub.KindBeatmapSet, want),
						})
						continue
					}
					if !in.live[t] {
						continue
					}
					if !in.canon.sets[want] {
						continue // canonical 无该版本行（不该发生，宁缺）
					}
					addItem(t, deploy.PlanItem{
						Kind: hub.KindBeatmapSet, ContentKey: want,
						Action: deploy.ActionCreate, SourceFork: dec.winner,
						Summary: in.sum.summary(hub.KindBeatmapSet, want),
					})
					continue
				}
				// 版本替换：弱键身份未确认 → 需确认（§5.4 三选一前置）；
				// 拍板项勾选即确认——NeedsConfirm 置位留痕照常入计划。
				if !in.evidence[uk.identity] && !dec.forced {
					out.pending.NeedsConfirm++
					continue
				}
				if !in.live[t] {
					continue
				}
				if !in.canon.sets[want] {
					continue
				}
				addItem(t, deploy.PlanItem{
					Kind: hub.KindBeatmapSet, ContentKey: want,
					OldContentKey: cur[0], Action: deploy.ActionVersionReplace,
					SourceFork:   dec.winner,
					NeedsConfirm: !in.evidence[uk.identity],
					Summary:      in.sum.summary(hub.KindBeatmapSet, want),
				})
			}
		case string(hub.KindRulesetSetting):
			// 规则集可解析性全局交集（任一目标端不收 → 所有端不写）。
			var rs hub.RulesetSetting
			if err := json.Unmarshal([]byte(row.cur[dec.winner].payload), &rs); err != nil {
				out.pending.Unsupported++
				continue
			}
			if r := deploy.RulesetIntersectionReject(rs.RulesetName, in.targets); r != "" {
				out.pending.Unsupported++
				continue
			}
			for _, t := range in.targets {
				if !dec.writable[t] || !in.live[t] {
					continue
				}
				// 拍板：值已等于决定值的端（胜者自身/已收敛）不出空转项。
				if dec.forced {
					if v, ok := row.cur[t]; ok && v.fp == dec.fp {
						continue
					}
				}
				if _, held := row.cur[t]; held {
					addItem(t, deploy.PlanItem{
						Kind: hub.KindRulesetSetting, ContentKey: uk.identity,
						Action: deploy.ActionUpdate, PayloadJSON: row.cur[dec.winner].payload,
						SourceFork: dec.winner,
						Summary:    in.sum.summary(hub.KindRulesetSetting, uk.identity),
					})
				} else {
					if tombstoneHit(in.tombs[t], hub.KindRulesetSetting, uk.identity) && !dec.forced {
						out.pending.Restore++
						continue
					}
					addItem(t, deploy.PlanItem{
						Kind: hub.KindRulesetSetting, ContentKey: uk.identity,
						Action: deploy.ActionCreate, PayloadJSON: row.cur[dec.winner].payload,
						SourceFork: dec.winner,
						Restore:    tombstoneHit(in.tombs[t], hub.KindRulesetSetting, uk.identity),
						Summary:    in.sum.summary(hub.KindRulesetSetting, uk.identity),
					})
				}
			}
		case string(hub.KindModPreset):
			var mp hub.ModPreset
			if err := json.Unmarshal([]byte(row.cur[dec.winner].payload), &mp); err != nil {
				out.pending.Unsupported++
				continue
			}
			if r := deploy.ModPresetIntersectionReject(mp, in.targets); r != "" {
				out.pending.Unsupported++
				continue
			}
			if r := deploy.RulesetIntersectionReject(mp.RulesetShortName, in.targets); r != "" {
				out.pending.Unsupported++
				continue
			}
			for _, t := range in.targets {
				if !dec.writable[t] || !in.live[t] {
					continue
				}
				if dec.forced {
					if v, ok := row.cur[t]; ok && v.fp == dec.fp {
						continue
					}
				}
				if _, held := row.cur[t]; held {
					addItem(t, deploy.PlanItem{
						Kind: hub.KindModPreset, ContentKey: uk.identity,
						Action: deploy.ActionUpdate, PayloadJSON: row.cur[dec.winner].payload,
						SourceFork: dec.winner,
						Summary:    in.sum.summary(hub.KindModPreset, uk.identity),
					})
				} else {
					if tombstoneHit(in.tombs[t], hub.KindModPreset, uk.identity) && !dec.forced {
						out.pending.Restore++
						continue
					}
					addItem(t, deploy.PlanItem{
						Kind: hub.KindModPreset, ContentKey: uk.identity,
						Action: deploy.ActionCreate, PayloadJSON: row.cur[dec.winner].payload,
						SourceFork: dec.winner,
						Restore:    tombstoneHit(in.tombs[t], hub.KindModPreset, uk.identity),
						Summary:    in.sum.summary(hub.KindModPreset, uk.identity),
					})
				}
			}
		case string(hub.KindKeyBinding):
			wantCombos := decodeSetFP(dec.fp)
			var tmpl hub.KeyBinding
			if err := json.Unmarshal([]byte(row.cur[dec.winner].payload), &tmpl); err != nil {
				out.pending.Unsupported++
				continue
			}
			if r := deploy.KeyBindingIntersectionReject(dec.winner, in.targets, tmpl); r != "" {
				out.pending.Unsupported++
				continue
			}
			if tmpl.RulesetName != nil {
				if r := deploy.RulesetIntersectionReject(*tmpl.RulesetName, in.targets); r != "" {
					out.pending.Unsupported++
					continue
				}
			}
			for _, t := range in.targets {
				if !dec.writable[t] || !in.live[t] {
					continue
				}
				have := map[string]bool{}
				for _, c := range decodeSetFP(row.cur[t].fp) {
					have[c] = true
				}
				for _, c := range wantCombos {
					if have[c] {
						continue
					}
					kb := tmpl
					kb.ID = deploy.NewGuidHex()
					kb.KeyCombination = c
					payload, _ := json.Marshal(kb)
					addItem(t, deploy.PlanItem{
						Kind: hub.KindKeyBinding,
						ContentKey: hub.KeyBindingContentKey(
							kb.RulesetName, kb.Variant, kb.ActionInt, c),
						Action: deploy.ActionCreate, PayloadJSON: string(payload),
						SourceFork: dec.winner,
						Summary:    in.sum.summary(hub.KindKeyBinding, uk.identity),
					})
				}
				for c := range have {
					inWant := false
					for _, wc := range wantCombos {
						if wc == c {
							inWant = true
							break
						}
					}
					if inWant {
						continue
					}
					ck := hub.KeyBindingContentKey(tmpl.RulesetName, tmpl.Variant, tmpl.ActionInt, c)
					if tombstoneHit(in.tombs[t], hub.KindKeyBinding, ck) {
						out.pending.Restore++
						continue // 键位行删除不复活——tombstone 命中需用户处置
					}
					addItem(t, deploy.PlanItem{
						Kind: hub.KindKeyBinding, ContentKey: ck,
						Action:     deploy.ActionDelete,
						SourceFork: dec.winner,
						Summary:    in.sum.summary(hub.KindKeyBinding, uk.identity) + "（移除多余键位）",
					})
				}
			}
		case string(hub.KindSkin), string(hub.KindScore):
			// 内容键即值的两类先跑名录/指纹全局交集预检（单位级一次判定
			// 全 peers——G11 收全的出口闸：Protected/专有皮肤与专有规则
			// 集成绩在 hub 留存但永不导出）。
			if uk.kind == string(hub.KindSkin) {
				payload, ok := in.canon.skins[uk.identity]
				if !ok {
					continue
				}
				var sk hub.Skin
				if err := json.Unmarshal([]byte(payload), &sk); err != nil {
					out.pending.Unsupported++
					continue
				}
				if r := deploy.SkinIntersectionReject(sk, in.targets); r != "" {
					out.pending.Unsupported++
					continue
				}
			}
			if uk.kind == string(hub.KindScore) {
				payload, ok := in.canon.scores[uk.identity]
				if !ok {
					continue
				}
				var sc hub.Score
				if err := json.Unmarshal([]byte(payload), &sc); err != nil {
					out.pending.Unsupported++
					continue
				}
				if r := deploy.ScoreIntersectionReject(sc, in.targets); r != "" {
					out.pending.Unsupported++
					continue
				}
			}
			for _, t := range in.targets {
				if !dec.writable[t] {
					continue
				}
				if _, held := row.cur[t]; held {
					// 内容键即值：正常轮持有 = 一致。拍板轮（forced）按
					// 字段级覆盖——目标端字段可能与胜者端不同（Date/
					// TotalScore 等），以胜者端投影载荷就地更新。
					if dec.forced && t != dec.winner && in.live[t] {
						payload := in.presence[dec.winner][hub.EntityKind(uk.kind)][uk.identity]
						if payload == "" {
							continue
						}
						addItem(t, deploy.PlanItem{
							Kind: hub.EntityKind(uk.kind), ContentKey: uk.identity,
							Action: deploy.ActionUpdate, PayloadJSON: payload,
							SourceFork: dec.winner,
							Summary:    in.sum.summary(hub.EntityKind(uk.kind), uk.identity),
						})
					}
					continue
				}
				dead := tombstoneHit(in.tombs[t], hub.EntityKind(uk.kind), uk.identity)
				if dead && !dec.forced {
					out.pending.Restore++
					continue
				}
				if !in.live[t] {
					continue
				}
				if uk.kind == string(hub.KindSkin) {
					addItem(t, deploy.PlanItem{
						Kind: hub.KindSkin, ContentKey: uk.identity,
						Action: deploy.ActionCreate, SourceFork: dec.winner,
						Restore: dead,
						Summary: in.sum.summary(hub.KindSkin, uk.identity),
					})
				} else {
					addItem(t, deploy.PlanItem{
						Kind: hub.KindScore, ContentKey: uk.identity,
						Action: deploy.ActionCreate, SourceFork: dec.winner,
						Restore: dead,
						Summary: in.sum.summary(hub.KindScore, uk.identity),
					})
				}
			}
		case hub.BaselineKindGameIni:
			for _, t := range in.targets {
				if !dec.writable[t] || !in.live[t] {
					continue
				}
				m, ok := out.ini[t]
				if !ok {
					m = map[string]deploy.IniValue{}
					out.ini[t] = m
				}
				m[uk.identity] = deploy.IniValue{Value: dec.fp, SourceFork: dec.winner}
			}
		}
	}

	// 收藏聚合项落计划：持有态或应有成员集与现状不同才出项。forced 取
	// 哨兵判定（拍板轮对该身份的全部单位都置位）。
	colForced := map[string]bool{}
	for uk, dec := range decs {
		if uk.kind == string(hub.KindCollection) && uk.member == "" && dec != nil && dec.forced {
			colForced[uk.identity] = true
		}
	}
	for t, perIdentity := range colAccs {
		if !in.live[t] {
			continue
		}
		for identity, a := range perIdentity {
			if !a.changed {
				continue
			}
			final := sortedSet(a.final)
			payload, ok := collectionPayload(in.canon, identity, final)
			if !ok {
				continue // canonical 无收藏行 → 无法造载荷（宁缺）
			}
			action := deploy.ActionUpdate
			dead := false
			if !a.heldNow {
				action = deploy.ActionCreate
				dead = tombstoneHit(in.tombs[t], hub.KindCollection, identity)
				if dead && !colForced[identity] {
					out.pending.Restore++
					continue
				}
			}
			addItem(t, deploy.PlanItem{
				Kind: hub.KindCollection, ContentKey: identity,
				Action: action, PayloadJSON: payload,
				Restore: dead,
				Summary: in.sum.summary(hub.KindCollection, identity),
			})
		}
	}
	return out
}

// tombstoneHit 目标端 (kind,key) tombstone 命中判定。
func tombstoneHit(tombs map[hub.EntityKind]map[string]int64, kind hub.EntityKind, key string) bool {
	if tombs == nil {
		return false
	}
	_, ok := tombs[kind][key]
	return ok
}

// identityTombstoned 谱面身份任一已知版本的 tombstone 命中（目标端删过
// 该谱面的任意版本 → 写新版本同样是「复活已删内容」，归恢复组）。
func identityTombstoned(tombs map[hub.EntityKind]map[string]int64, kind hub.EntityKind, row *unitRow) bool {
	if tombs == nil {
		return false
	}
	m, ok := tombs[kind]
	if !ok {
		return false
	}
	for _, uv := range row.cur {
		for _, h := range decodeSetFP(uv.fp) {
			if _, dead := m[h]; dead {
				return true
			}
		}
	}
	return false
}

// collectionPayload 决定成员集的收藏载荷（canonical 行为底——ID 与
// LastModified 沿用首见值，新行建立可写；成员集替换为决定集）。
func collectionPayload(canon *canonicalIndex, name string, members []string) (string, bool) {
	base, ok := canon.collections[name]
	if !ok {
		return "", false
	}
	var c hub.Collection
	if err := json.Unmarshal([]byte(base), &c); err != nil {
		return "", false
	}
	c.BeatmapMD5Hashes = members
	b, err := json.Marshal(c)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ---- 输入装载与基线推进 ----

// loadDecideInput 装载判定输入面（一轮同步一次）。
//   - presence：全行读入后按 Source||Target 端过滤（参与端外的不入判定）；
//   - baseline：baseline 表全行（同样按单位 kind 面与参与端过滤）；
//   - ini：逐端读快照仓——仅本轮成功跑过收集的端参与（跳过端的 ini
//     状态陈旧，不作 v(f) 输入；但它们的 fork 角色仍在 targets/sources）；
//   - tombs：逐目标端 tombstone；
//   - evidence：beatmap_identity 的强键/已确认面；
//   - canon：canonical 载荷索引（create/交集预检的载荷来源）。
func loadDecideInput(ctx context.Context, deps *Deps, sources, targets []string,
	live map[string]bool, kinds map[hub.EntityKind]bool, wantGameIni bool,
	freshIni map[string]bool) (*decideInput, error) {
	in := &decideInput{
		presence:  map[string]map[hub.EntityKind]map[string]string{},
		baseline:  map[unitKey]map[string]string{},
		ini:       map[string]map[string]string{},
		sources:   append([]string(nil), sources...),
		targets:   append([]string(nil), targets...),
		live:      live,
		kinds:     kinds,
		gameIni:   wantGameIni,
		tombs:     map[string]map[hub.EntityKind]map[string]int64{},
		evidence:  map[string]bool{},
		hashIdent: map[string]string{},
		canon: &canonicalIndex{
			collections: map[string]string{},
			sets:        map[string]bool{},
			skins:       map[string]string{},
			scores:      map[string]string{},
			kbs:         map[string]bool{},
			settings:    map[string]bool{},
			presets:     map[string]bool{},
		},
		sum: newSummarizer(deps.Hub),
	}
	sort.Strings(in.sources)
	sort.Strings(in.targets)

	// presence 全量（只读；参与端过滤在 buildUnits）。
	rows, err := deps.Hub.DB().QueryContext(ctx,
		`SELECT fork_id, kind, content_key, local_values FROM presence`)
	if err != nil {
		return nil, fmt.Errorf("读 presence: %w", err)
	}
	for rows.Next() {
		var fid, kind, ck, lv string
		if err := rows.Scan(&fid, &kind, &ck, &lv); err != nil {
			rows.Close()
			return nil, fmt.Errorf("扫 presence 行: %w", err)
		}
		k := hub.EntityKind(kind)
		m, ok := in.presence[fid]
		if !ok {
			m = map[hub.EntityKind]map[string]string{}
			in.presence[fid] = m
		}
		if m[k] == nil {
			m[k] = map[string]string{}
		}
		m[k][ck] = lv
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历 presence: %w", err)
	}
	rows.Close()

	// baseline 全量。
	base, err := deps.Hub.Baselines(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range base {
		uk := unitKey{r.Kind, r.Identity, r.Member}
		m, ok := in.baseline[uk]
		if !ok {
			m = map[string]string{}
			in.baseline[uk] = m
		}
		m[r.ForkID] = r.Value
	}

	// ini 快照（仅本轮收集过的端——freshIni 由调用方按收集结果给出）。
	if wantGameIni && deps.GameIni != nil {
		for _, fid := range sortedKeys(freshIni) {
			if !freshIni[fid] {
				continue
			}
			vals, _, ok, err := deps.GameIni.Load(fid)
			if err != nil {
				return nil, fmt.Errorf("读 %s 的 ini 快照: %w", fid, err)
			}
			if ok {
				in.ini[fid] = vals
			}
		}
	}

	// tombstone 逐目标端。
	for _, t := range in.targets {
		tm, err := deps.Hub.Tombstones(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("读 %s tombstone: %w", t, err)
		}
		in.tombs[t] = tm
	}

	// 谱面身份锚：内容哈希 → 注册身份键（identity_version）。异域副本
	// 会让同一哈希同时挂强键（online:）与弱键（meta:）——锚必须优先
	// 强键：选弱键会让该端后续新版本注册到 online: 后单位分裂（同一
	// 物理集进两个单位 → 双向 create 重灌）。ORDER BY 强键优先 + 序
	// 最小，哈希首个命中行即锚（确定性）。
	idAnchor, err := deps.Hub.DB().QueryContext(ctx, `
SELECT content_hash, identity_key FROM identity_version
ORDER BY CASE WHEN identity_key LIKE 'online:%' THEN 0 ELSE 1 END, identity_key`)
	if err != nil {
		return nil, fmt.Errorf("读身份锚: %w", err)
	}
	for idAnchor.Next() {
		var h, ik string
		if err := idAnchor.Scan(&h, &ik); err != nil {
			idAnchor.Close()
			return nil, fmt.Errorf("扫身份锚: %w", err)
		}
		if _, ok := in.hashIdent[h]; !ok {
			in.hashIdent[h] = ik
		}
	}
	if err := idAnchor.Err(); err != nil {
		idAnchor.Close()
		return nil, fmt.Errorf("遍历身份锚: %w", err)
	}
	idAnchor.Close()

	// beatmap 身份证据：强键或用户已确认 → 自动路径可用；弱键未确认 →
	// 需确认（§5.4 三选一前置，版本替换不自动）。
	idRows, err := deps.Hub.DB().QueryContext(ctx,
		`SELECT identity_key, evidence, user_confirmed FROM beatmap_identity`)
	if err != nil {
		return nil, fmt.Errorf("读 beatmap_identity: %w", err)
	}
	for idRows.Next() {
		var ik, ev string
		var confirmed int
		if err := idRows.Scan(&ik, &ev, &confirmed); err != nil {
			idRows.Close()
			return nil, fmt.Errorf("扫 beatmap_identity: %w", err)
		}
		in.evidence[ik] = ev == string(hub.EvidenceStrong) || confirmed != 0
	}
	if err := idRows.Err(); err != nil {
		idRows.Close()
		return nil, fmt.Errorf("遍历 beatmap_identity: %w", err)
	}
	idRows.Close()

	// canonical 索引。
	load := func(q string, fn func(key, payload string)) error {
		rs, err := deps.Hub.DB().QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var k, p string
			if err := rs.Scan(&k, &p); err != nil {
				return err
			}
			fn(k, p)
		}
		return rs.Err()
	}
	if err := load(`SELECT content_key, payload FROM collection`, func(k, p string) {
		in.canon.collections[k] = p
	}); err != nil {
		return nil, fmt.Errorf("读 canonical 收藏: %w", err)
	}
	if err := load(`SELECT content_hash, payload FROM beatmap_set`, func(k, p string) {
		in.canon.sets[k] = true
	}); err != nil {
		return nil, fmt.Errorf("读 canonical 谱面集: %w", err)
	}
	if err := load(`SELECT content_hash, payload FROM skin`, func(k, p string) {
		in.canon.skins[k] = p
	}); err != nil {
		return nil, fmt.Errorf("读 canonical 皮肤: %w", err)
	}
	if err := load(`SELECT beatmap_hash || '|' || content_hash, payload FROM score`, func(k, p string) {
		in.canon.scores[k] = p
	}); err != nil {
		return nil, fmt.Errorf("读 canonical 成绩: %w", err)
	}
	// 设置类三表：决定值载荷来自胜者端 presence（不走 canonical），此处
	// 仅需存在性以外的字段无需求——kbs/settings/presets 索引留空即可，
	// 装配面不消费它们。
	return in, nil
}

// runDecisions 引擎入口：装载 → 提值 → 判定 → 装配。包级函数以便
// Compare 面（冲突枚举）复用——只依赖 Deps。
func runDecisions(ctx context.Context, deps *Deps, sources, targets []string,
	live map[string]bool, kinds map[hub.EntityKind]bool, wantGameIni bool,
	freshIni map[string]bool) (*decideOutcome, error) {
	in, err := loadDecideInput(ctx, deps, sources, targets, live, kinds, wantGameIni, freshIni)
	if err != nil {
		return nil, err
	}
	units, _ := buildUnits(in)
	decs := decideAll(units, in.sources, in.targets)
	out := assemblePlans(in, units, decs)
	out.in = in
	return out, nil
}

// baselineRows 一轮结束后应推进的基线行（§0.3）：对评估过的每个单位、
// 每个本轮有新鲜快照的端，写 b(f,单位) := 该端最终值。冻结规则：
//   - 全局冲突单位（候选多值）任何端都不推进；
//   - 端级 protected/divergent 只冻结该端该行；
//   - 无新鲜快照的端（跳过/收集失败/投放或重收集失败）整体不推进。
//
// freshFor 按 kind 给新鲜端集（game_ini 用 ini 快照面）。resolveOnly =
// 勾选处置轮（用户拍板即解析，解冻——只看端的新鲜度）。forced = 拍板轮
// 的胜者判定：可写端基线直接记决定值（cur 是写前快照，拍板后该端持
// 有的一定是决定值——不记决定值会留假「已改动」态）。
func baselineRows(units map[unitKey]*unitRow, decs map[unitKey]*unitDecision,
	forced map[unitKey]*unitDecision,
	freshFor func(kind string) map[string]bool, resolveOnly bool, at int64) []hub.BaselineRow {
	var out []hub.BaselineRow
	for uk, row := range units {
		dec := decs[uk]
		if !resolveOnly && dec != nil && dec.conflict {
			continue // 全局冲突单位基线不动
		}
		fresh := freshFor(uk.kind)
		for _, f := range sortedKeys(fresh) {
			if !fresh[f] {
				continue
			}
			if !resolveOnly && dec != nil && (dec.protected[f] || dec.divergent[f]) {
				continue // 该端此单位基线不动（冲突冻结/异值保留）
			}
			v := hub.BaselineAbsent
			if uv, ok := row.cur[f]; ok {
				v = uv.fp
			}
			if fd := forced[uk]; fd != nil && fd.decided && fd.writable[f] {
				v = fd.fp // 拍板：已写端基线记决定值
			}
			out = append(out, hub.BaselineRow{
				ForkID: f, Kind: uk.kind, Identity: uk.identity,
				Member: uk.member, Value: v, SyncedAt: at,
			})
		}
	}
	return out
}

// selUnits 勾选处置覆盖的判定单位（PrepareSelected：用户显式处置的单位
// 在一轮结束推进基线——「选定变体推向全部目标端后推进」语义）。
//   - collection：勾选集名 → 该身份全部成员单位 + 身份哨兵；
//   - beatmap_set：勾选内容键 → 该版本在 identity_version 中的全部身份键；
//   - key_binding：四段内容键 → 三段身份；
//   - 其余：内容键即身份。
func (s *realSync) selUnits(ctx context.Context, sel []Selection,
	units map[unitKey]*unitRow) (map[unitKey]bool, error) {
	out := map[unitKey]bool{}
	for _, sl := range sel {
		uks, err := s.unitsForSelection(ctx, sl, units)
		if err != nil {
			return nil, err
		}
		for uk := range uks {
			out[uk] = true
		}
	}
	return out, nil
}

// unitsForSelection 单条勾选 → 覆盖的判定单位（处置装配与基线推进共用）。
// collection 勾名 = 哨兵+全成员单位；beatmap_set 勾内容键 = 注册的全部
// 身份键；key_binding 四段键 → 三段身份；其余键即身份。
func (s *realSync) unitsForSelection(ctx context.Context, sl Selection,
	units map[unitKey]*unitRow) (map[unitKey]bool, error) {
	out := map[unitKey]bool{}
	switch sl.Kind {
	case hub.KindCollection:
		for uk := range units {
			if uk.kind == string(hub.KindCollection) && uk.identity == sl.ContentKey {
				out[uk] = true
			}
		}
	case hub.KindBeatmapSet:
		rows, err := s.deps.Hub.DB().QueryContext(ctx,
			`SELECT DISTINCT identity_key FROM identity_version WHERE content_hash=?`, sl.ContentKey)
		if err != nil {
			return nil, fmt.Errorf("读谱面集 %s 的身份键: %w", sl.ContentKey, err)
		}
		for rows.Next() {
			var ik string
			if err := rows.Scan(&ik); err != nil {
				rows.Close()
				return nil, fmt.Errorf("扫身份键: %w", err)
			}
			out[unitKey{string(hub.KindBeatmapSet), ik, ""}] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("遍历身份键: %w", err)
		}
		rows.Close()
	case hub.KindKeyBinding:
		rn, v, ai, _, err := hub.DecodeKeyBindingContentKey(sl.ContentKey)
		if err != nil {
			return nil, fmt.Errorf("键位内容键解码 %q: %w", sl.ContentKey, err)
		}
		out[unitKey{string(hub.KindKeyBinding), hub.KeyBindingIdentityKey(rn, v, ai), ""}] = true
	default:
		out[unitKey{string(sl.Kind), sl.ContentKey, ""}] = true
	}
	return out, nil
}

// refreshIniSnapshot ini 写入后重读目标端快照（基线推进的「最终值」输入；
// 也保持快照仓与落盘值一致）。
func (s *realSync) refreshIniSnapshot(ref forks.LibraryRef) error {
	prof, ok := forks.Profile(ref.ForkID)
	if !ok {
		return fmt.Errorf("fork %q 未注册（ini 快照刷新跳过）", ref.ForkID)
	}
	vals, exists, err := inisettings.CollectFile(
		filepath.Join(ref.DataDir, inisettings.GameIniName), prof)
	if err != nil {
		return err
	}
	if !exists {
		return nil // 文件仍不存在（不该发生——SyncFile 刚写过）
	}
	return s.deps.GameIni.Save(ref.ForkID, vals, time.Now())
}
