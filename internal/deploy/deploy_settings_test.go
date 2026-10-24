package deploy

// M3 设置类投放验收（design.md §5.1-M3 / §5.4 / §7-M3-3）：键位（D20
// 枚举域交集过滤的注入态分支 + 可空键段全形态落地 + 重续）、规则集设置
//（fork 专有规则集不可解析跳过）、ModPreset（Ruleset 三态解析 + D7 mod
// 白名单 + 软删防复活）逐分支断言计数与人话原因；批模型三类并组。
// librealm 缺失时显式 SKIP（§8.1 补充门：不得静默绿）。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// applySettingsSnap 把三类实体快照落进 hub（测试构造面，收集链路由
// itests 覆盖）。
func applySettingsSnap(t *testing.T, h *hub.Store, snap hub.Snapshot) {
	t.Helper()
	if _, err := h.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
}

// settingsSnapshot M3 三类实体快照：含正常行与各跳过分支夹具行。
func settingsSnapshot() hub.Snapshot {
	bms := "bms"
	mania4, mania7 := 4, 7
	return hub.Snapshot{
		ForkID: "ez2lazer", CollectedAt: t0,
		KeyBindings: []hub.KeyBinding{
			{ID: "11111111-0001-0000-0000-000000000001", ActionInt: 100, KeyCombination: "Ctrl,Shift+Z"}, // 全局
			{ID: "11111111-0002-0000-0000-000000000002", RulesetName: &bms, ActionInt: 3, KeyCombination: "Space"},
			{ID: "11111111-0003-0000-0000-000000000003", RulesetName: &bms, Variant: &mania4,
				ActionInt: 12, KeyCombination: "F"},
			{ID: "11111111-0004-0000-0000-000000000004", RulesetName: &bms, Variant: &mania7,
				ActionInt: 12, KeyCombination: "D,F,J,K"},
		},
		RulesetSettings: []hub.RulesetSetting{
			{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"bottomleft"`},
			{RulesetName: "mania", Variant: 4, Key: "speed_change_style", Value: "1"},
			{RulesetName: "bms", Variant: 0, Key: "bms_scroll_speed", Value: "25"}, // fork 专有
		},
		ModPresets: []hub.ModPreset{
			{ID: "22222222-0001-0000-0000-000000000001", RulesetShortName: "osu", Name: "Score Preset",
				Description: "d1", ModsJson: `[{"acronym":"HD","settings":{}}]`},
			{ID: "22222222-0002-0000-0000-000000000002", RulesetShortName: "mania", Name: "Key7 Preset",
				ModsJson: `[{"acronym":"DT","settings":{}}]`},
			{ID: "22222222-0003-0000-0000-000000000003", RulesetShortName: "bms", Name: "BMS Preset",
				ModsJson: `[{"acronym":"NM","settings":{}}]`}, // fork 专有规则集
			{ID: "22222222-0004-0000-0000-000000000004", RulesetShortName: "osu", Name: "Exotic Preset",
				ModsJson: `[{"acronym":"EZX","settings":{}}]`}, // D7 名录外
		},
	}
}

// buildSettingsLib 构造一个含设置类 schema 的空目标库（lazer 镜像，官方
// 四规则集与内置皮肤不播种——fixture 镜像不播 Ruleset 行，官方四集缺行
// 新建分支由此覆盖）。
func buildSettingsLib(t *testing.T, lib *realmc.Library) (forks.LibraryRef, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	schema, err := fixture.SchemaFor(fixture.ForkLazer, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("建目标库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭目标库: %v", err)
	}
	return forks.LibraryRef{
		ForkID: "lazer", DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionLazer, VersionSource: forks.VersionFromOpen,
	}, path
}

// deploySettingsOnce 单轮投放：hub 已收快照 → BuildPlan → Deploy。
func deploySettingsOnce(t *testing.T, h *hub.Store, ref forks.LibraryRef) Report {
	t.Helper()
	plan, err := BuildPlan(context.Background(), h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := NewRunner(Deps{Hub: h}).Deploy(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return report
}

// TestDeploySettingsFullChain 设置类三类全链：正常行落地 + 跳过分支逐条
// 计数 + 重续幂等。
func TestDeploySettingsFullChain(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	ref, realmPath := buildSettingsLib(t, lib)
	h := openMemHub(t)
	applySettingsSnap(t, h, settingsSnapshot())

	report := deploySettingsOnce(t, h, ref)

	// 键位 4 行全落地（字符串列不解析，bms 键位随行带走；域数据待激活态
	// 全放行）。设置 2 行落地（bms 跳过）。预设 2 行落地（bms 规则集与
	// EZX mod 各跳过 1）。
	if len(report.Applied) != 8 {
		t.Fatalf("Applied = %d，期望 8（%+v）", len(report.Applied), report.Applied)
	}
	skipOf := func(rep Report, sub string) (n int) {
		for _, s := range rep.Skipped {
			if strings.Contains(s.Reason, sub) {
				n += s.Count
			}
		}
		return n
	}
	if got := skipOf(report, "规则集设置引用规则集"); got != 1 {
		t.Errorf("设置 fork 专有规则集跳过 = %d，期望 1（Skips=%+v）", got, report.Skipped)
	}
	if got := skipOf(report, "模组预设 \"BMS Preset\" 引用规则集"); got != 1 {
		t.Errorf("预设 fork 专有规则集跳过 = %d，期望 1", got)
	}
	if got := skipOf(report, "含目标端不认识的 mod"); got != 1 {
		t.Errorf("预设 D7 名录外跳过 = %d，期望 1", got)
	}
	if len(report.FailedBatches) != 0 {
		t.Fatalf("失败批 = %+v", report.FailedBatches)
	}

	// 现场读回逐字段断言（只读会话用完即关）。
	bmsName := "bms"
	mania4, mania7 := 4, 7
	ro, err := lib.OpenDynamicReadOnly(realmPath)
	if err != nil {
		t.Fatalf("开只读会话: %v", err)
	}
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("开读事务: %v", err)
	}
	rd := newSettingsReader(t, ro)
	defer ro.Close()
	// 键位：全键段形态（null/null、bms/null、bms/4、bms/7）逐一读回。
	wantKB := map[string]string{
		hub.KeyBindingContentKey(nil, nil, 100):         "Ctrl,Shift+Z",
		hub.KeyBindingContentKey(&bmsName, nil, 3):      "Space",
		hub.KeyBindingContentKey(&bmsName, &mania4, 12): "F",
		hub.KeyBindingContentKey(&bmsName, &mania7, 12): "D,F,J,K",
	}
	rd.eachKeyBinding(func(kb hub.KeyBinding) bool {
		want, ok := wantKB[hub.KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt)]
		if !ok {
			t.Errorf("多余键位行: %+v", kb)
			return false
		}
		delete(wantKB, hub.KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt))
		if kb.KeyCombination != want {
			t.Errorf("键位组合不一致: %q ≠ %q", kb.KeyCombination, want)
		}
		return true
	})
	if len(wantKB) != 0 {
		t.Errorf("缺失键位行: %v", wantKB)
	}
	// 设置：osu/mania 落地、bms 缺席。
	gotSettings := map[string]bool{}
	rd.eachRulesetSetting(func(rs hub.RulesetSetting) bool {
		gotSettings[hub.RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key)] = true
		return true
	})
	for _, key := range []string{
		hub.RulesetSettingContentKey("osu", 0, "hud_position"),
		hub.RulesetSettingContentKey("mania", 4, "speed_change_style"),
	} {
		if !gotSettings[key] {
			t.Errorf("缺失设置行 %s", key)
		}
	}
	if gotSettings[hub.RulesetSettingContentKey("bms", 0, "bms_scroll_speed")] {
		t.Errorf("bms 设置行不应落地（目标不可解析）")
	}
	// 预设：osu/mania 落地（mania 走官方四集缺行新建）、bms 与 EZX 缺席。
	gotPresets := map[string]bool{}
	rd.eachModPreset(func(mp hub.ModPreset) bool {
		gotPresets[hub.ModPresetContentKey(mp.RulesetShortName, mp.Name)] = true
		return true
	})
	for _, key := range []string{
		hub.ModPresetContentKey("osu", "Score Preset"),
		hub.ModPresetContentKey("mania", "Key7 Preset"),
	} {
		if !gotPresets[key] {
			t.Errorf("缺失预设行 %s", key)
		}
	}
	if gotPresets[hub.ModPresetContentKey("bms", "BMS Preset")] ||
		gotPresets[hub.ModPresetContentKey("osu", "Exotic Preset")] {
		t.Errorf("bms/EZX 预设不应落地")
	}
	// mania 规则集行被凭空新建（官方四集缺行，§4.2/§5.2-4）。
	if _, err := ro.FindClass("Ruleset"); err == nil {
		// FindFirstByString 查 mania 行。
		ck, _ := ro.FindClass("Ruleset")
		pk, err := ro.FindProperty(ck, "ShortName")
		if err != nil {
			t.Fatalf("FindProperty(ShortName): %v", err)
		}
		obj, err := ro.FindFirstByString(ck, pk, "mania")
		if err != nil || obj == nil {
			t.Errorf("mania 规则集行未新建（%v, %v）", obj, err)
		} else {
			ro.ReleaseObject(obj)
		}
	}
	ro.Close()

	// 重续：同计划重跑 → 落地项全部「已存在 → 跳过」，跳过分支计数不变。
	report2 := deploySettingsOnce(t, h, ref)
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %d，期望 0", len(report2.Applied))
	}
	if got := skipOf(report2, "目标已是 canonical 值"); got != 8 {
		t.Errorf("重跑已一致跳过 = %d，期望 8（Skips=%+v）", got, report2.Skipped)
	}
	if got := skipOf(report2, "规则集设置引用规则集"); got != 1 {
		t.Errorf("重跑设置 fork 专有跳过 = %d，期望 1", got)
	}
	_ = ctx
}

// TestDeployKeyBindingActionDomainFilter D20 枚举域交集过滤（注入域数据
// 的分支验证——数值清单无调研出处，四端域待数据激活态，此处以注入形态
// 验证机制与跳过计数）。
func TestDeployKeyBindingActionDomainFilter(t *testing.T) {
	defer func(orig map[string]keyBindingActionDomain) {
		keyBindingActionDomains = orig
	}(keyBindingActionDomains)
	keyBindingActionDomains = map[string]keyBindingActionDomain{
		"ez2lazer": {globalMax: 200, rulesetMax: 20}, // 源端全域
		"lazer":    {globalMax: 150, rulesetMax: 10}, // 目标端较窄（低版本端）
	}

	snap := hub.Snapshot{
		ForkID: "ez2lazer", CollectedAt: t0,
		KeyBindings: []hub.KeyBinding{
			{ID: "33333333-0001-0000-0000-000000000001", ActionInt: 100, KeyCombination: "A"}, // 全局域内
			{ID: "33333333-0002-0000-0000-000000000002", ActionInt: 180, KeyCombination: "B"}, // 全局越目标域
			{ID: "33333333-0003-0000-0000-000000000003", RulesetName: strPtrD("osu"),
				ActionInt: 15, KeyCombination: "C"}, // 规则集越源端域
			{ID: "33333333-0004-0000-0000-000000000004", RulesetName: strPtrD("osu"),
				ActionInt: 8, KeyCombination: "D"}, // 规则集域内
			{ID: "33333333-0005-0000-0000-000000000005", ActionInt: -1, KeyCombination: "E"}, // 负值坏数据
		},
	}
	// 越域与负值全部拒绝、域内放行。
	cases := []struct {
		kb   hub.KeyBinding
		want bool
	}{
		{snap.KeyBindings[0], true},
		{snap.KeyBindings[1], false},
		{snap.KeyBindings[2], false},
		{snap.KeyBindings[3], true},
		{snap.KeyBindings[4], false},
	}
	for _, c := range cases {
		ok, reason := keyBindingActionAllowed("ez2lazer", "lazer", c.kb)
		if ok != c.want {
			t.Errorf("keyBindingActionAllowed(%+v) = %v（%q），期望 %v", c.kb, ok, reason, c.want)
		}
		if !ok && reason == "" {
			t.Errorf("拒绝必须带人话原因（D20）")
		}
	}
	// 任一端未取样 → 放行（待激活态）。
	delete(keyBindingActionDomains, "lazer")
	if ok, _ := keyBindingActionAllowed("ez2lazer", "lazer", hub.KeyBinding{ActionInt: 99999}); !ok {
		t.Errorf("目标端域未取样应放行（待激活态）")
	}

	// 集成面：注入域数据投放 → 越域项跳过计数、域内项落地。
	lib := mustLib(t)
	ref, _ := buildSettingsLib(t, lib)
	h := openMemHub(t)
	applySettingsSnap(t, h, snap)
	keyBindingActionDomains = map[string]keyBindingActionDomain{
		"ez2lazer": {globalMax: 200, rulesetMax: 20},
		"lazer":    {globalMax: 150, rulesetMax: 10},
	}
	report := deploySettingsOnce(t, h, ref)
	if len(report.Applied) != 2 {
		t.Fatalf("Applied = %d，期望 2（%+v）", len(report.Applied), report.Applied)
	}
	d20 := 0
	for _, s := range report.Skipped {
		if strings.Contains(s.Reason, "D20 越域跳过") || strings.Contains(s.Reason, "枚举值域非负") {
			d20 += s.Count
		}
	}
	if d20 != 3 {
		t.Errorf("D20 越域跳过 = %d，期望 3（Skips=%+v）", d20, report.Skipped)
	}
}

// TestDeployModPresetSoftDeleteGuard ModPreset 软删防复活（HB2 执行面闭环）：
// 计划构建后现场软删的同键行跳过，恢复项放行。
func TestDeployModPresetSoftDeleteGuard(t *testing.T) {
	lib := mustLib(t)
	ref, realmPath := buildSettingsLib(t, lib)
	h := openMemHub(t)
	// hub 快照的 fork 必须与目标 fork 不同——presence 按 fork 记账，同名
	// 会让对账把 canonical 视为「目标已持有」（收集/投放两方向的语义分离）。
	applySettingsSnap(t, h, hub.Snapshot{
		ForkID: "ez2lazer", CollectedAt: t0,
		ModPresets: []hub.ModPreset{
			{ID: "44444444-0001-0000-0000-000000000001", RulesetShortName: "osu",
				Name: "Guarded Preset", ModsJson: `[{"acronym":"HD","settings":{}}]`},
		},
	})
	// 第一轮投放落地。
	if report := deploySettingsOnce(t, h, ref); len(report.Applied) != 1 {
		t.Fatalf("首轮 Applied = %d，期望 1（Skips=%+v Failed=%+v）",
			len(report.Applied), report.Skipped, report.FailedBatches)
	}
	// 模拟游戏内软删（动态写会话直接置 DeletePending=true）。
	rw, err := lib.OpenDynamicReadWrite(realmPath)
	if err != nil {
		t.Fatalf("开写会话: %v", err)
	}
	if err := rw.BeginWrite(); err != nil {
		t.Fatalf("开写事务: %v", err)
	}
	ck, err := rw.FindClass("ModPreset")
	if err != nil {
		t.Fatalf("FindClass(ModPreset): %v", err)
	}
	pk, err := rw.FindProperty(ck, "Name")
	if err != nil {
		t.Fatalf("FindProperty(Name): %v", err)
	}
	obj, err := rw.FindFirstByString(ck, pk, "Guarded Preset")
	if err != nil || obj == nil {
		t.Fatalf("定位预设行: %v", err)
	}
	dpKey, err := rw.FindProperty(ck, "DeletePending")
	if err != nil {
		t.Fatalf("FindProperty(DeletePending): %v", err)
	}
	if err := rw.SetBool(obj, dpKey, true); err != nil {
		t.Fatalf("软删: %v", err)
	}
	rw.ReleaseObject(obj)
	if err := rw.Commit(); err != nil {
		t.Fatalf("提交软删: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("关闭写会话: %v", err)
	}
	// 冲突态制造（canonical Description 与现场不同 → 走就地更新通道才会
	// 触碰 DeletePending）：hub 侧更新 canonical。
	applySettingsSnap(t, h, hub.Snapshot{
		ForkID: "ez2lazer", CollectedAt: t0.Add(1),
		ModPresets: []hub.ModPreset{
			{ID: "44444444-0001-0000-0000-000000000001", RulesetShortName: "osu",
				Name: "Guarded Preset", Description: "changed", ModsJson: `[{"acronym":"HD","settings":{}}]`},
		},
	})
	// 非恢复计划：conflict → update → 现场软删行跳过防复活。
	report := deploySettingsOnce(t, h, ref)
	found := false
	for _, s := range report.Skipped {
		if strings.Contains(s.Reason, "跳过防复活") {
			found = true
		}
	}
	if !found {
		t.Errorf("现场软删行未跳过防复活（Skips=%+v）", report.Skipped)
	}
}

// TestSplitKindBatchesSettingsOrder 批模型：三类按 500 行/批并入既有分组
// （批内 kind 单一是设计——runBatch 按首项 kind 准备写入器），批序在成绩
// 之后；500 上限触发拆批。
func TestSplitKindBatchesSettingsOrder(t *testing.T) {
	var items []PlanItem
	add := func(kind hub.EntityKind, n int) {
		for i := 0; i < n; i++ {
			items = append(items, PlanItem{Kind: kind,
				ContentKey: string(rune('a'+i%26)) + string(rune('0'+i%10)), Action: ActionCreate})
		}
	}
	add(hub.KindCollection, 2)
	add(hub.KindScore, 2)
	add(hub.KindKeyBinding, 3)
	add(hub.KindRulesetSetting, 2)
	add(hub.KindModPreset, 1)
	batches := splitKindBatches(items)
	if len(batches) != 5 {
		t.Fatalf("批数 = %d，期望 5（每 kind 一批，行数远小于上限）", len(batches))
	}
	// 批序断言：收藏 → … → 成绩 → 键位 → 规则集设置 → 模组预设。
	wantOrder := []hub.EntityKind{hub.KindCollection, hub.KindScore,
		hub.KindKeyBinding, hub.KindRulesetSetting, hub.KindModPreset}
	for i, want := range wantOrder {
		if batches[i][0].Kind != want {
			t.Errorf("批[%d] kind = %s，期望 %s", i, batches[i][0].Kind, want)
		}
	}
	// 三类分批上限：501 键位 → 2 批。
	big := make([]PlanItem, 501)
	for i := range big {
		big[i] = PlanItem{Kind: hub.KindKeyBinding, ContentKey: "k", Action: ActionCreate}
	}
	if got := len(splitKindBatches(big)); got != 2 {
		t.Errorf("501 键位批数 = %d，期望 2", got)
	}
}

// strPtrD 测试内 *string 构造。
func strPtrD(s string) *string { return &s }

// newSettingsReader 覆盖目标库只读读回的设置类三类断言面（collect 投影的
// deploy 侧内联形态——往返逐字段断言的读回端）。
func newSettingsReader(t *testing.T, r *realmc.Realm) *settingsReader {
	t.Helper()
	return &settingsReader{t: t, r: r}
}

type settingsReader struct {
	t *testing.T
	r *realmc.Realm
}

func (sr *settingsReader) eachKeyBinding(fn func(hub.KeyBinding) bool) {
	ck, err := sr.r.FindClass("KeyBinding")
	if err != nil {
		sr.t.Fatalf("FindClass(KeyBinding): %v", err)
	}
	if err := sr.r.ForEach(ck, func(obj realmObjectPtr) error {
		var rulesetName *string
		var variant *int
		pk, _ := sr.r.FindProperty(ck, "RulesetName")
		if null, nErr := sr.r.IsNull(obj, pk); nErr == nil && !null {
			if v, gErr := sr.r.GetString(obj, pk); gErr == nil {
				rulesetName = &v
			}
		}
		vk, _ := sr.r.FindProperty(ck, "Variant")
		if null, nErr := sr.r.IsNull(obj, vk); nErr == nil && !null {
			if v, gErr := sr.r.GetInt64(obj, vk); gErr == nil {
				vv := int(v)
				variant = &vv
			}
		}
		ak, _ := sr.r.FindProperty(ck, "Action")
		action, err := sr.r.GetInt64(obj, ak)
		if err != nil {
			return err
		}
		ck2, _ := sr.r.FindProperty(ck, "KeyCombination")
		kc, err := sr.r.GetString(obj, ck2)
		if err != nil {
			return err
		}
		if !fn(hub.KeyBinding{RulesetName: rulesetName, Variant: variant, ActionInt: int(action), KeyCombination: kc}) {
			return errStopIteration
		}
		return nil
	}); err != nil && err != errStopIteration {
		sr.t.Fatalf("遍历 KeyBinding: %v", err)
	}
}

func (sr *settingsReader) eachRulesetSetting(fn func(hub.RulesetSetting) bool) {
	ck, err := sr.r.FindClass("RulesetSetting")
	if err != nil {
		sr.t.Fatalf("FindClass(RulesetSetting): %v", err)
	}
	if err := sr.r.ForEach(ck, func(obj realmObjectPtr) error {
		get := func(name string) (string, error) {
			pk, pErr := sr.r.FindProperty(ck, name)
			if pErr != nil {
				return "", pErr
			}
			return sr.r.GetString(obj, pk)
		}
		name, err := get("RulesetName")
		if err != nil {
			return err
		}
		key, err := get("Key")
		if err != nil {
			return err
		}
		value, err := get("Value")
		if err != nil {
			return err
		}
		vk, _ := sr.r.FindProperty(ck, "Variant")
		variant, err := sr.r.GetInt64(obj, vk)
		if err != nil {
			return err
		}
		if !fn(hub.RulesetSetting{RulesetName: name, Variant: int(variant), Key: key, Value: value}) {
			return errStopIteration
		}
		return nil
	}); err != nil && err != errStopIteration {
		sr.t.Fatalf("遍历 RulesetSetting: %v", err)
	}
}

func (sr *settingsReader) eachModPreset(fn func(hub.ModPreset) bool) {
	ck, err := sr.r.FindClass("ModPreset")
	if err != nil {
		sr.t.Fatalf("FindClass(ModPreset): %v", err)
	}
	if err := sr.r.ForEach(ck, func(obj realmObjectPtr) error {
		get := func(name string) (string, error) {
			pk, pErr := sr.r.FindProperty(ck, name)
			if pErr != nil {
				return "", pErr
			}
			return sr.r.GetString(obj, pk)
		}
		name, err := get("Name")
		if err != nil {
			return err
		}
		rsKey, _ := sr.r.FindProperty(ck, "Ruleset")
		short := ""
		if rs, gErr := sr.r.GetLinkedObject(obj, rsKey); gErr == nil && rs != nil {
			rck, _ := sr.r.FindClass("Ruleset")
			rpk, _ := sr.r.FindProperty(rck, "ShortName")
			if v, sErr := sr.r.GetString(rs, rpk); sErr == nil {
				short = v
			}
			sr.r.ReleaseObject(rs)
		}
		if !fn(hub.ModPreset{RulesetShortName: short, Name: name}) {
			return errStopIteration
		}
		return nil
	}); err != nil && err != errStopIteration {
		sr.t.Fatalf("遍历 ModPreset: %v", err)
	}
}

// errStopIteration 是读回遍历的中断哨兵（fn 返回 false 时停止迭代）。
var errStopIteration = errors.New("deploy: 断言遍历提前结束")
