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
		hub.KeyBindingContentKey(nil, nil, 100, "Ctrl,Shift+Z"):          "Ctrl,Shift+Z",
		hub.KeyBindingContentKey(&bmsName, nil, 3, "Space"):              "Space",
		hub.KeyBindingContentKey(&bmsName, &mania4, 12, "F"):             "F",
		hub.KeyBindingContentKey(&bmsName, &mania7, 12, "D,F,J,K"):       "D,F,J,K",
	}
	rd.eachKeyBinding(func(kb hub.KeyBinding) bool {
		want, ok := wantKB[hub.KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination)]
		if !ok {
			t.Errorf("多余键位行: %+v", kb)
			return false
		}
		delete(wantKB, hub.KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt, kb.KeyCombination))
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

// TestDeployKeyBindingActionDomainFilter D20 枚举域交集过滤（四端真数据：
// ForkProfile.KeyBindingAction，2026-10-25 上游实测取样——lazer/g0v0/
// ez2lazer 键位枚举完全一致〔GlobalAction 0..120；规则集 Action 含
// Editor*=10000 显式段，最大 OsuAction 10005〕，delta 基线〔Framework
// 2026.731.0〕全面落后〔GlobalAction 0..98、规则集无 Editor* 段〕），
// 交集过滤以 delta↔官方代双向验证。
func TestDeployKeyBindingActionDomainFilter(t *testing.T) {
	kbOf := func(action int, ruleset string) hub.KeyBinding {
		out := hub.KeyBinding{ActionInt: action, KeyCombination: "Z"}
		if ruleset != "" {
			name := ruleset
			out.RulesetName = &name
		}
		return out
	}
	for _, c := range []struct {
		name   string
		origin string
		target string
		kb     hub.KeyBinding
		want   bool
	}{
		// delta→官方代：delta 是官方的前缀子集，域内值全部放行。
		{"delta 全局边界 98 放行", "delta", "lazer", kbOf(98, ""), true},
		{"delta 规则集边界 19 放行", "delta", "lazer", kbOf(19, "mania"), true},
		// 官方代→delta：官方尾部新增（delta 无此枚举）越目标域拒。
		{"官方全局 99 越目标端 delta 域拒", "lazer", "delta", kbOf(99, ""), false},
		{"官方全局 120 越目标端 delta 域拒", "lazer", "delta", kbOf(120, ""), false},
		{"官方规则集 Editor 段 10000 越 delta 域拒", "lazer", "delta", kbOf(10000, "osu"), false},
		// 官方代同代（lazer/g0v0/ez 互投）：全域放行（含 Editor* 段）。
		{"官方代全局 120 同代放行", "lazer", "g0v0", kbOf(120, ""), true},
		{"官方代 Editor 段 10005 同代放行", "ez2lazer", "lazer", kbOf(10005, "osu"), true},
		// 负值坏数据恒拒；未注册 fork 交集不可判定放行。
		{"负值坏数据拒", "lazer", "delta", kbOf(-1, ""), false},
		{"未注册 fork 放行", "ghost", "lazer", kbOf(99999, ""), true},
	} {
		ok, reason := keyBindingActionAllowed(c.origin, c.target, c.kb)
		if ok != c.want {
			t.Errorf("%s: keyBindingActionAllowed = %v（%q），期望 %v", c.name, ok, reason, c.want)
		}
		if !ok && reason == "" {
			t.Errorf("%s: 拒绝必须带人话原因（D20）", c.name)
		}
	}

	// 集成面：官方代源（lazer 快照）→ delta 目标库投放，官方新增键位行
	//（全局 100、规则集 Editor 段）D20 越域跳过计数、delta 域内行落地。
	lib := mustLib(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	schema, err := fixture.SchemaFor(fixture.ForkDelta, fixture.VersionDelta)
	if err != nil {
		t.Fatalf("SchemaFor(delta): %v", err)
	}
	w, err := lib.OpenWithSchema(path, schema, fixture.VersionDelta)
	if err != nil {
		t.Fatalf("建 delta 目标库: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭目标库: %v", err)
	}
	refDelta := forks.LibraryRef{
		ForkID: "delta", DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionDelta, VersionSource: forks.VersionFromOpen,
	}
	osuName := "osu"
	h := openMemHub(t)
	applySettingsSnap(t, h, hub.Snapshot{
		ForkID: "lazer", CollectedAt: t0,
		KeyBindings: []hub.KeyBinding{
			{ID: "44444444-0001-0000-0000-000000000001", ActionInt: 98, KeyCombination: "A"}, // delta 全局域内边界
			{ID: "44444444-0002-0000-0000-000000000002", ActionInt: 100, KeyCombination: "B"}, // 官方新增，delta 越域 → 跳
			{ID: "44444444-0003-0000-0000-000000000003", RulesetName: &osuName,
				ActionInt: 2, KeyCombination: "C"}, // 规则集域内 → 落
			{ID: "44444444-0004-0000-0000-000000000004", RulesetName: &osuName,
				ActionInt: 10000, KeyCombination: "D"}, // Editor 段，delta 越域 → 跳
			{ID: "44444444-0005-0000-0000-000000000005", ActionInt: -1, KeyCombination: "E"}, // 负值坏数据 → 跳
		},
	})
	_ = ctx
	report := deploySettingsOnce(t, h, refDelta)
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
