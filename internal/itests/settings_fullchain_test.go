package itests

// M3 设置类全链验收（design.md §5.1-M3 / §7-M3-3）：三类实体构造库收集
// → BuildPlan → 投放 → 重投影逐字段相等；跳过分支逐条计数断言——
// 收集侧软删预设（S4）、投放侧 fork 专有规则集设置行（§5.1-M3）与 bms
// 预设（§4.2）、名录外 mod 预设（D7）。结构档案写前后零漂移（§8.2-7）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

// settingsSourceSeed 源座种子（ez2lazer）：SettingsSeed + bms Ruleset 行
//（bms 预设/设置/键位行随 SettingsSeed 装配，ExtraRulesets 先例）。
func settingsSourceSeed(t *testing.T) fixture.Seed {
	t.Helper()
	seed, err := fixture.SettingsSeed()
	if err != nil {
		t.Fatalf("SettingsSeed: %v", err)
	}
	seed.ExtraRulesets = []fixture.SeedRuleset{{
		ShortName:         "bms",
		OnlineID:          -1,
		Name:              "Beatmap Spectrum",
		InstantiationInfo: "osu.Game.Rulesets.BMS.BMSRuleset, osu.Game.Rulesets.BMS",
	}}
	return seed
}

// wantSettingsKeyBindings 投放落地后的预期键位集合（源 5 行全落地——
// RulesetName 是字符串列不解析，bms 行随行带走；Guid 为投影形态）。
func wantSettingsKeyBindings(seed fixture.Seed) map[string]hub.KeyBinding {
	conv := func(kb fixture.SeedKeyBinding) hub.KeyBinding {
		out := hub.KeyBinding{
			ID:             guidHex(kb.Guid),
			ActionInt:      kb.ActionInt,
			KeyCombination: kb.KeyCombination,
		}
		if kb.RulesetName != "" {
			name := kb.RulesetName
			out.RulesetName = &name
		}
		if kb.Variant != nil {
			out.Variant = kb.Variant
		}
		return out
	}
	out := map[string]hub.KeyBinding{}
	for _, kb := range seed.KeyBindings {
		c := conv(kb)
		out[hub.KeyBindingContentKey(c.RulesetName, c.Variant, c.ActionInt)] = c
	}
	return out
}

// wantSettingsRows 预期规则集设置与模组预设（源 − 投放跳过行）。
func wantSettingsRows(seed fixture.Seed) (map[string]hub.RulesetSetting, map[string]hub.ModPreset) {
	rs := map[string]hub.RulesetSetting{}
	for _, s := range seed.RulesetSettings {
		if s.RulesetName == "bms" {
			continue // fork 专有：目标不可解析，不落地
		}
		rs[hub.RulesetSettingContentKey(s.RulesetName, s.Variant, s.Key)] =
			hub.RulesetSetting{RulesetName: s.RulesetName, Variant: s.Variant, Key: s.Key, Value: s.Value}
	}
	mp := map[string]hub.ModPreset{}
	for _, p := range seed.ModPresets {
		if p.RulesetShortName == "bms" || strings.Contains(p.ModsJson, "EZX") || p.DeletePending {
			continue // fork 专有规则集 / 名录外 mod（D7）/ 软删（S4 收集侧已滤）
		}
		mp[hub.ModPresetContentKey(p.RulesetShortName, p.Name)] = hub.ModPreset{
			ID: guidHex(p.Guid), RulesetShortName: p.RulesetShortName, Name: p.Name,
			Description: p.Description, ModsJson: p.ModsJson,
		}
	}
	return rs, mp
}

// TestSettingsFullChainRoundtrip 收集（ez2lazer）→ 计划 → 投放（lazer）
// → 重投影逐字段相等；跳过分支逐条计数。
func TestSettingsFullChainRoundtrip(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)
	seed := settingsSourceSeed(t)

	// 源座（ez2lazer）与目标座（lazer，空种子——官方四集与内置皮肤恒播）。
	refSrc := buildLib(t, fixture.ForkEz2Lazer, fixture.VersionEzDisk, seed)
	refDst := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{})

	// ---- 收集：三类入仓 + 软删预设跳过计数（S4）----
	repSrc := e.collectInto(refSrc)
	for kind, want := range map[hub.EntityKind]int{
		hub.KindKeyBinding: 5, hub.KindRulesetSetting: 3, hub.KindModPreset: 4,
	} {
		if got := repSrc.Entities[kind]; got != want {
			t.Fatalf("源收集 Entities[%s] = %d，期望 %d", kind, got, want)
		}
	}
	if got := collectSkip(repSrc, "软删模组预设"); got != 1 {
		t.Errorf("收集侧软删预设跳过 = %d，期望 1（Skips=%+v）", got, repSrc.Skips)
	}

	// ---- 计划：三类 create 项共 12（hub 保留原文，跳过分支在执行期拦）----
	plan, err := deploy.BuildPlan(ctx, e.hub, refDst, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	planKinds := map[hub.EntityKind]int{}
	for _, it := range plan.Items {
		if it.Action != deploy.ActionCreate {
			t.Errorf("计划项 %s 非预期动作 %s（三类无历史版本，全 create）", it.Kind, it.Action)
		}
		planKinds[it.Kind]++
	}
	for kind, want := range map[hub.EntityKind]int{
		hub.KindKeyBinding: 5, hub.KindRulesetSetting: 3, hub.KindModPreset: 4,
	} {
		if got := planKinds[kind]; got != want {
			t.Fatalf("计划 %s 项 = %d，期望 %d（plan=%+v）", kind, got, want, plan.Items)
		}
	}

	// ---- 投放：9 落地 + 三条跳过分支逐条计数 ----
	schemaBefore := schemaSnapshotOf(t, lib, refDst.RealmPath)
	report, err := e.deployRunner().Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.FailedBatches) != 0 || len(report.NotRun) != 0 {
		t.Fatalf("失败/未执行段非空: %+v / %+v", report.FailedBatches, report.NotRun)
	}
	appliedKinds := map[hub.EntityKind]int{}
	for _, ap := range report.Applied {
		appliedKinds[ap.Kind]++
	}
	for kind, want := range map[hub.EntityKind]int{
		hub.KindKeyBinding: 5, hub.KindRulesetSetting: 2, hub.KindModPreset: 2,
	} {
		if got := appliedKinds[kind]; got != want {
			t.Errorf("Applied[%s] = %d，期望 %d（Applied=%+v）", kind, got, want, report.Applied)
		}
	}
	if got := deploySkip(report, "规则集设置引用规则集 \"bms\" 在目标不可解析"); got != 1 {
		t.Errorf("fork 专有规则集设置跳过 = %d，期望 1（Skips=%+v）", got, report.Skipped)
	}
	if got := deploySkip(report, "模组预设 \"BMS Preset\" 引用规则集"); got != 1 {
		t.Errorf("fork 专有规则集预设跳过 = %d，期望 1", got)
	}
	if got := deploySkip(report, "含目标端不认识的 mod \"EZX\""); got != 1 {
		t.Errorf("D7 名录外 mod 预设跳过 = %d，期望 1", got)
	}

	// ---- 结构档案写前后零漂移（§8.2-7 独立第二校验）----
	if !schemaBefore.Equal(schemaSnapshotOf(t, lib, refDst.RealmPath)) {
		t.Error("投放前后目标结构档案漂移")
	}

	// ---- 重投影：再收集目标库 → 三类 presence 与预期逐键逐字段相等 ----
	e.collectInto(refDst)
	wantKB := wantSettingsKeyBindings(seed)
	wantRS, wantMP := wantSettingsRows(seed)
	assertSettingsProjection(t, e.hub, hub.KindKeyBinding, wantKB)
	assertSettingsProjection(t, e.hub, hub.KindRulesetSetting, wantRS)
	assertSettingsProjection(t, e.hub, hub.KindModPreset, wantMP)
}

// assertSettingsProjection 逐键逐字段比对重投影 presence（canonical 与
// presence 由同一次 ApplySnapshot 写入，presence 即重投影读面）。
func assertSettingsProjection[T any](t *testing.T, h *hub.Store, kind hub.EntityKind, want map[string]T) {
	t.Helper()
	rows, err := h.DB().Query(
		`SELECT content_key, local_values FROM presence WHERE fork_id=? AND kind=?`, "lazer", kind)
	if err != nil {
		t.Fatalf("读 presence(%s): %v", kind, err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("读 presence(%s) 行: %v", kind, err)
		}
		got[k] = v
	}
	if len(got) != len(want) {
		t.Errorf("重投影 %s presence = %d 行，期望 %d（got 键集 %+v）", kind, len(got), len(want), got)
	}
	for key, wantVal := range want {
		raw, ok := got[key]
		if !ok {
			t.Errorf("重投影 %s 缺键 %s", kind, key)
			continue
		}
		var gotVal T
		if err := json.Unmarshal([]byte(raw), &gotVal); err != nil {
			t.Fatalf("解码 %s presence %s: %v", kind, key, err)
		}
		if !equalDTO(gotVal, wantVal) {
			t.Errorf("重投影 %s 键 %s 逐字段不等:\n得到 %+v\n期望 %+v", kind, key, gotVal, wantVal)
		}
	}
}

// equalDTO 以 JSON 规范形态比对两个同型 DTO（键位含指针字段，反射.DeepEqual
// 亦可用；JSON 比对对 nil 指针/零值区分更直观）。
func equalDTO(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// collectSkip 收集报告按原因子串取计数（不存在返回 -1）。
func collectSkip(rep collect.Report, sub string) int {
	for _, s := range rep.Skips {
		if strings.Contains(s.Reason, sub) {
			return s.Count
		}
	}
	return -1
}

// deploySkip 投放报告按原因子串取计数（不存在返回 -1）。
func deploySkip(rep deploy.Report, sub string) int {
	for _, s := range rep.Skipped {
		if strings.Contains(s.Reason, sub) {
			return s.Count
		}
	}
	return -1
}

// guidHex Guid 串 → 投影形态（32 位小写 hex 无连字符）。
func guidHex(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", ""))
}
