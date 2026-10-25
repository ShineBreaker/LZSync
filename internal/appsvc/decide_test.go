package appsvc

// §0.3 三方判定引擎单测（纯函数面：decideUnit/buildUnits/assemblePlans
// 不触 realm——输入面全用内存 decideInput 构造）。覆盖：一端改动传播、
// 两端各改冲突、保护/异值、缺席非候选、收藏逐成员、键位集合删建、
// ini 逐键、谱面版本替换与弱键确认、tombstone 恢复组。

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"lzsync/internal/deploy"
	"lzsync/internal/hub"
)

// decideFixture 构造一份空输入面 + 内存 hub（summarizer 需要 store）。
func decideFixture(t *testing.T) *decideInput {
	t.Helper()
	h, err := hub.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("hub.Open: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return &decideInput{
		presence: map[string]map[hub.EntityKind]map[string]string{},
		baseline: map[unitKey]map[string]string{},
		ini:      map[string]map[string]string{},
		live:     map[string]bool{},
		kinds:    map[hub.EntityKind]bool{},
		tombs:    map[string]map[hub.EntityKind]map[string]int64{},
		evidence: map[string]bool{},
		canon: &canonicalIndex{
			collections: map[string]string{},
			sets:        map[string]bool{},
			skins:       map[string]string{},
			scores:      map[string]string{},
		},
		sum: newSummarizer(h),
	}
}

// unit 手工装一个判定单位（cur/base 逐端给值）。
func unit(kind, identity, member string, cur map[string]unitVal, base map[string]string) (unitKey, *unitRow) {
	return unitKey{kind, identity, member}, &unitRow{cur: cur, base: base}
}

func TestDecideUnitSingleForkChangePropagates(t *testing.T) {
	// 基线：A/B 都持 v0；A 改成 v1 → v*=v1，B 可写，A 已收敛。
	_, row := unit("ruleset_setting", "k1", "",
		map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v0"}},
		map[string]string{"A": "v0", "B": "v0"})
	d := decideUnit(unitKey{"ruleset_setting", "k1", ""}, row, []string{"A", "B"}, []string{"A", "B"})
	if !d.decided || d.fp != "v1" || d.winner != "A" {
		t.Fatalf("判定 = %+v，期望决定值 v1 来源 A", d)
	}
	if !d.writable["B"] {
		t.Error("B 应可写（未改动）")
	}
	if d.conflict || len(d.protected) != 0 || len(d.divergent) != 0 {
		t.Errorf("不应有冲突/保护/异值: %+v", d)
	}
}

func TestDecideUnitTwoForksChangedConflict(t *testing.T) {
	// 基线 v0；A→v1、B→v2 → 候选多值冲突，两端都不动。
	_, row := unit("ruleset_setting", "k1", "",
		map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v2"}},
		map[string]string{"A": "v0", "B": "v0"})
	d := decideUnit(unitKey{"ruleset_setting", "k1", ""}, row, []string{"A", "B"}, []string{"A", "B"})
	if !d.conflict || d.decided {
		t.Fatalf("应判冲突: %+v", d)
	}
}

func TestDecideUnitProtectedForkKeepsOwnChange(t *testing.T) {
	// 基线 v0；来源 A→v1，目标 B 自行改成 v2 → B 保护跳过 + 计冲突。
	_, row := unit("ruleset_setting", "k1", "",
		map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v2"}},
		map[string]string{"A": "v0", "B": "v0"})
	// sources 只有 A（B 仅目标）：候选 = {v1}。
	d := decideUnit(unitKey{"ruleset_setting", "k1", ""}, row, []string{"A"}, []string{"B"})
	if !d.decided || d.fp != "v1" {
		t.Fatalf("判定 = %+v，期望决定值 v1", d)
	}
	if !d.protected["B"] {
		t.Error("B 自行改动应进保护集")
	}
	if d.writable["B"] {
		t.Error("B 不应可写")
	}
}

func TestDecideUnitNoBaselineDivergentValues(t *testing.T) {
	// 无同步史 + 两端异值 → 候选多值 = 冲突（§0.3 规则 5）。
	_, row := unit("ruleset_setting", "k1", "",
		map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v2"}}, nil)
	d := decideUnit(unitKey{"ruleset_setting", "k1", ""}, row, []string{"A", "B"}, []string{"A", "B"})
	if !d.conflict {
		t.Fatalf("无基线双源异值应判冲突: %+v", d)
	}
}

func TestDecideUnitAbsentIsNotCandidate(t *testing.T) {
	// 基线 v0；A 删除（缺席）→ 缺席不是变更来源 → 无候选，两端不动。
	_, row := unit("ruleset_setting", "k1", "",
		map[string]unitVal{"B": {fp: "v0"}},
		map[string]string{"A": "v0", "B": "v0"})
	d := decideUnit(unitKey{"ruleset_setting", "k1", ""}, row, []string{"A", "B"}, []string{"A", "B"})
	if d.decided || d.conflict {
		t.Fatalf("删除不传播：应无候选: %+v", d)
	}
}

func TestDecideUnitNoBaselineFillMissing(t *testing.T) {
	// 无同步史：A 持 v1，B 缺席 → 唯一候选 v1，B 可补缺。
	_, row := unit("ruleset_setting", "k1", "",
		map[string]unitVal{"A": {fp: "v1"}}, nil)
	d := decideUnit(unitKey{"ruleset_setting", "k1", ""}, row, []string{"A"}, []string{"B"})
	if !d.decided || !d.writable["B"] {
		t.Fatalf("B 应可补缺: %+v", d)
	}
}

func TestAssembleCollectionPerMember(t *testing.T) {
	// 基线：A/B 都持成员 {m1}；A 加了 m2 → B 应得含 m2 的 update，
	// PayloadJSON 携带应有成员集 {m1,m2}。
	in := decideFixture(t)
	in.kinds[hub.KindCollection] = true
	in.sources = []string{"A"}
	in.targets = []string{"A", "B"}
	in.live = map[string]bool{"A": true, "B": true}
	in.canon.collections["夹"] = `{"ID":"id-1","Name":"夹","BeatmapMD5Hashes":["m1","m2"],"LastModified":"2024-01-01T00:00:00Z"}`

	units := map[unitKey]*unitRow{}
	units[unitKey{string(hub.KindCollection), "夹", ""}] = &unitRow{
		cur:  map[string]unitVal{"A": {fp: "held"}, "B": {fp: "held"}},
		base: map[string]string{"A": "held", "B": "held"},
	}
	units[unitKey{string(hub.KindCollection), "夹", "m1"}] = &unitRow{
		cur:  map[string]unitVal{"A": {fp: "1"}, "B": {fp: "1"}},
		base: map[string]string{"A": "1", "B": "1"},
	}
	units[unitKey{string(hub.KindCollection), "夹", "m2"}] = &unitRow{
		cur:  map[string]unitVal{"A": {fp: "1"}},
		base: map[string]string{"A": "0", "B": "0"}, // 上轮两端都无 m2
	}
	decs := decideAll(units, in.sources, in.targets)
	out := assemblePlans(in, units, decs)

	if out.pending.Conflicts != 0 {
		t.Fatalf("不应有冲突: %+v", out.pending)
	}
	items := out.plans["B"]
	if len(items) != 1 || items[0].Action != deploy.ActionUpdate {
		t.Fatalf("B 计划 = %+v，期望 1 条 update", items)
	}
	var c hub.Collection
	if err := jsonUnmarshal(items[0].PayloadJSON, &c); err != nil {
		t.Fatalf("载荷解码: %v", err)
	}
	if len(c.BeatmapMD5Hashes) != 2 {
		t.Errorf("应有成员集 = %v，期望 {m1,m2}", c.BeatmapMD5Hashes)
	}
	if len(out.plans["A"]) != 0 {
		t.Errorf("A 不应有计划项: %+v", out.plans["A"])
	}
}

func TestAssembleKeyBindingSetCreateDelete(t *testing.T) {
	// 三端：基线 lazer/delta={c1}、g0v0={c1,c3}；lazer 改成 {c1,c2}
	//（唯一变更来源）→ 决定集 {c1,c2}。delta 未变 → 只补 create c2；
	// g0v0 未变但持多余 c3 → create c2 + delete c3（集合值同步）。
	//（交集预检要求注册 fork——用真实 ForkID。）
	in := decideFixture(t)
	in.kinds[hub.KindKeyBinding] = true
	in.sources = []string{"lazer"}
	in.targets = []string{"lazer", "delta", "g0v0"}
	in.live = map[string]bool{"lazer": true, "delta": true, "g0v0": true}

	rn := "osu"
	variant := 0
	kbPayload := `{"ID":"g","RulesetName":"osu","Variant":0,"ActionInt":1,"KeyCombination":"c1"}`
	ik := hub.KeyBindingIdentityKey(&rn, &variant, 1)
	units := map[unitKey]*unitRow{
		{string(hub.KindKeyBinding), ik, ""}: {
			cur: map[string]unitVal{
				"lazer": {fp: `["c1","c2"]`, payload: kbPayload},
				"delta": {fp: `["c1"]`, payload: kbPayload},
				"g0v0":  {fp: `["c1","c3"]`, payload: kbPayload},
			},
			base: map[string]string{"lazer": `["c1"]`, "delta": `["c1"]`, "g0v0": `["c1","c3"]`},
		},
	}
	decs := decideAll(units, in.sources, in.targets)
	out := assemblePlans(in, units, decs)
	if out.pending.Conflicts != 0 {
		t.Fatalf("不应有冲突: %+v", out.pending)
	}

	assertCreatesDeletes := func(fork string, wantCreate, wantDelete int) {
		t.Helper()
		var creates, deletes int
		for _, it := range out.plans[fork] {
			switch it.Action {
			case deploy.ActionCreate:
				creates++
				if it.PayloadJSON == "" {
					t.Errorf("%s create 项缺决定值载荷", fork)
				}
			case deploy.ActionDelete:
				deletes++
			}
		}
		if creates != wantCreate || deletes != wantDelete {
			t.Errorf("%s 计划 = %+v，期望 create×%d + delete×%d",
				fork, out.plans[fork], wantCreate, wantDelete)
		}
	}
	assertCreatesDeletes("delta", 1, 0)
	assertCreatesDeletes("g0v0", 1, 1)
}

func TestAssembleGameIniPerKey(t *testing.T) {
	// 逐键判定：k1 只有 A 改了 → B 补缺；k2 两端异值无基线 → 冲突不写。
	in := decideFixture(t)
	in.gameIni = true
	in.sources = []string{"A"}
	in.targets = []string{"A", "B"}
	in.live = map[string]bool{"A": true, "B": true}
	in.ini = map[string]map[string]string{
		"A": {"k1": "v1", "k2": "x"},
		"B": {"k2": "y"}, // k1 在 B 缺席 → 可补缺
	}
	units, _ := buildUnits(in)
	decs := decideAll(units, in.sources, in.targets)
	out := assemblePlans(in, units, decs)

	iv, ok := out.ini["B"]["k1"]
	if !ok || iv.Value != "v1" || iv.SourceFork != "A" {
		t.Fatalf("B 的 k1 决定值 = %+v(ok=%v)，期望 {v1,A}", iv, ok)
	}
	if _, ok := out.ini["B"]["k2"]; ok {
		t.Errorf("k2 冲突不应写: %+v", out.ini["B"])
	}
	if out.pending.Conflicts < 1 {
		t.Errorf("k2 应计冲突: %+v", out.pending)
	}
}

func TestAssembleBeatmapVersionReplace(t *testing.T) {
	// 基线：两端都持 v1；A 换成 v2 → B 得 version-replace（旧键 v1）。
	in := decideFixture(t)
	in.kinds[hub.KindBeatmapSet] = true
	in.sources = []string{"A"}
	in.targets = []string{"A", "B"}
	in.live = map[string]bool{"A": true, "B": true}
	in.evidence["online:42"] = true
	in.canon.sets["h2"] = true

	uk := unitKey{string(hub.KindBeatmapSet), "online:42", ""}
	units := map[unitKey]*unitRow{
		uk: {
			cur:  map[string]unitVal{"A": {fp: `["h2"]`}, "B": {fp: `["h1"]`}},
			base: map[string]string{"A": `["h1"]`, "B": `["h1"]`},
		},
	}
	decs := decideAll(units, in.sources, in.targets)
	out := assemblePlans(in, units, decs)
	items := out.plans["B"]
	if len(items) != 1 || items[0].Action != deploy.ActionVersionReplace ||
		items[0].ContentKey != "h2" || items[0].OldContentKey != "h1" ||
		items[0].SourceFork != "A" {
		t.Fatalf("B 计划 = %+v，期望 version-replace h1→h2 来源 A", items)
	}
}

func TestAssembleBeatmapWeakIdentityNeedsConfirm(t *testing.T) {
	// 弱键身份未确认 → 版本替换不入计划、计 NeedsConfirm。
	in := decideFixture(t)
	in.kinds[hub.KindBeatmapSet] = true
	in.sources = []string{"A"}
	in.targets = []string{"A", "B"}
	in.live = map[string]bool{"A": true, "B": true}
	in.canon.sets["h2"] = true
	// evidence 不含 meta: 键 → 弱键未确认。

	units := map[unitKey]*unitRow{
		{string(hub.KindBeatmapSet), "meta:a|t|u", ""}: {
			cur:  map[string]unitVal{"A": {fp: `["h2"]`}, "B": {fp: `["h1"]`}},
			base: map[string]string{"A": `["h1"]`, "B": `["h1"]`},
		},
	}
	out := assemblePlans(in, units, decideAll(units, in.sources, in.targets))
	if len(out.plans["B"]) != 0 {
		t.Fatalf("弱键未确认不应出计划: %+v", out.plans["B"])
	}
	if out.pending.NeedsConfirm != 1 {
		t.Errorf("NeedsConfirm = %d，期望 1", out.pending.NeedsConfirm)
	}
}

func TestAssembleTombstoneGoesToRestore(t *testing.T) {
	// 目标端 tombstone 命中 → 恢复组计数，不出计划项（删除不复活）。
	in := decideFixture(t)
	in.kinds[hub.KindSkin] = true
	in.sources = []string{"A"}
	in.targets = []string{"A", "B"}
	in.live = map[string]bool{"A": true, "B": true}
	in.canon.skins["sk1"] = `{"Hash":"sk1","Name":"s"}`
	in.tombs["B"] = map[hub.EntityKind]map[string]int64{
		hub.KindSkin: {"sk1": 123},
	}
	units := map[unitKey]*unitRow{
		{string(hub.KindSkin), "sk1", ""}: {
			cur: map[string]unitVal{"A": {fp: "held"}},
		},
	}
	out := assemblePlans(in, units, decideAll(units, in.sources, in.targets))
	if len(out.plans["B"]) != 0 {
		t.Fatalf("tombstone 命中不应出计划: %+v", out.plans["B"])
	}
	if out.pending.Restore != 1 {
		t.Errorf("Restore = %d，期望 1", out.pending.Restore)
	}
}

func TestBaselineRowsFreeze(t *testing.T) {
	// 基线推进：冲突单位整体冻结；protected 端冻结；非 fresh 端不写行。
	units := map[unitKey]*unitRow{
		{"ruleset_setting", "ok", ""}:   {cur: map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v0"}}},
		{"ruleset_setting", "cfl", ""}:  {cur: map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v2"}}},
		{"ruleset_setting", "prot", ""}: {cur: map[string]unitVal{"A": {fp: "v1"}, "B": {fp: "v2"}}},
	}
	decs := map[unitKey]*unitDecision{
		{"ruleset_setting", "ok", ""}:   {decided: true, fp: "v1", writable: map[string]bool{"B": true}, protected: map[string]bool{}, divergent: map[string]bool{}},
		{"ruleset_setting", "cfl", ""}:  {conflict: true, writable: map[string]bool{}, protected: map[string]bool{}, divergent: map[string]bool{}},
		{"ruleset_setting", "prot", ""}: {decided: true, fp: "v1", writable: map[string]bool{}, protected: map[string]bool{"B": true}, divergent: map[string]bool{}},
	}
	fresh := map[string]bool{"A": true, "B": true}
	rows := baselineRows(units, decs, func(string) map[string]bool { return fresh }, false, 7)
	got := map[string]string{}
	for _, r := range rows {
		got[r.ForkID+"/"+r.Identity] = r.Value
	}
	if got["A/ok"] != "v1" || got["B/ok"] != "v0" {
		t.Errorf("ok 单位基线 = %+v，期望 A=v1 B=v0（推进最终值）", got)
	}
	if _, ok := got["A/cfl"]; ok {
		t.Error("冲突单位任何端都不应推进")
	}
	if _, ok := got["B/prot"]; ok {
		t.Error("protected 端不应推进")
	}
	if got["A/prot"] != "v1" {
		t.Errorf("prot 单位 A 端应推进: %+v", got)
	}
	// 缺席 → BaselineAbsent 哨兵（已知缺席，非无行）。
	units2 := map[unitKey]*unitRow{
		{"ruleset_setting", "gone", ""}: {cur: map[string]unitVal{}},
	}
	decs2 := map[unitKey]*unitDecision{
		{"ruleset_setting", "gone", ""}: {writable: map[string]bool{}, protected: map[string]bool{}, divergent: map[string]bool{}},
	}
	rows2 := baselineRows(units2, decs2, func(string) map[string]bool { return fresh }, false, 7)
	if len(rows2) != 2 || rows2[0].Value != hub.BaselineAbsent {
		t.Fatalf("缺席端基线行 = %+v，期望 BaselineAbsent", rows2)
	}
}

// jsonUnmarshal 小封装（测试面少一次 import 噪音）。
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}
