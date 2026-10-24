package deploy

// 规则集设置与模组预设写入器（M3，design.md §5.1-M3 / §4.2 / §5.4）：
//
// RulesetSetting（表名 RulesetSetting，无主键表，lazer-baseline §2.13）：
//   - 身份匹配：内容键 (RulesetName, Variant, Key) 三元组——三列在真实库
//     均非空（[Indexed]/[Required]），但同 RulesetName 多行无法走单列
//     FindFirstByString，全表扫定位（键位写入器的哨兵中断同款）；
//   - 跳过分支：fork 专有规则集的行在目标端不可解析 → 跳过计数（§5.1-M3）。
//     可解析性 = 目标已有同名行 ∪ 官方四集（目标端 RulesetStore 首启播种，
//     §5.1）——纯查询判定（设置行的 RulesetName 是字符串列不挂接，无需
//     ModPreset 那样的凭空新建）；
//   - 就地更新只写 Value（唯一非键业务列）。
//
// ModPreset（表名 ModPreset，lazer-baseline §2.11）：内容键 = 业务键
// (RulesetShortName, Name)（§4.2 无内容哈希）。
//   - Ruleset 关系列走目标端解析（§4.2/§5.2-4 同款）：目标已有同名行复用、
//     官方四集缺行新建（Available=true）、fork 专有跳过并计数；
//   - ModsJson 过目标端 mod 白名录（D7，compat.adaptModPresetModsForTarget），
//     名录外整条跳过；
//   - 软删防复活（HB2，谱面集/皮肤同款）：现场 DeletePending=true 的同键
//     行除非恢复项一律跳过——就地更新会把 DeletePending 写回 false。

import (
	"errors"
	"fmt"

	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// errSettingRowFound 是 ForEach 定位到匹配行后的哨兵（中断迭代）。
var errSettingRowFound = errors.New("deploy: 设置类匹配行已定位")

// rulesetSettingWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type rulesetSettingWriter struct {
	r           *realmc.Realm
	setCK       uint32
	setCols     map[string]int64
	rulesetCK   uint32
	rulesetCols map[string]int64
	absent      bool
	absentWhy   string
}

func prepareRulesetSettingWriter(r *realmc.Realm) (*rulesetSettingWriter, error) {
	w := &rulesetSettingWriter{r: r, setCols: map[string]int64{}, rulesetCols: map[string]int64{}}
	absent := func(why string) (*rulesetSettingWriter, error) {
		w.absent, w.absentWhy = true, why
		return w, nil
	}
	ck, err := r.FindClass("RulesetSetting")
	if err != nil {
		return absent("目标库没有 RulesetSetting 表（版本过旧或非 lazer 系库）")
	}
	w.setCK = ck
	for _, name := range []string{"RulesetName", "Variant", "Key", "Value"} {
		if key, err := r.FindProperty(ck, name); err == nil {
			w.setCols[name] = key
		}
	}
	for _, name := range []string{"RulesetName", "Variant", "Key", "Value"} {
		if _, ok := w.setCols[name]; !ok {
			return absent(fmt.Sprintf("目标库 RulesetSetting 缺 %s 列，无法做身份匹配或写入", name))
		}
	}
	// 规则集解析面：目标 Ruleset 表（可解析性判定输入）。
	rsCK, err := r.FindClass("Ruleset")
	if err != nil {
		return absent("目标库没有 Ruleset 表，无法判定规则集设置可解析性")
	}
	w.rulesetCK = rsCK
	for _, name := range []string{"ShortName"} {
		if key, err := r.FindProperty(rsCK, name); err == nil {
			w.rulesetCols[name] = key
		}
	}
	if _, ok := w.rulesetCols["ShortName"]; !ok {
		return absent("目标库 Ruleset 缺 ShortName 列，无法判定规则集设置可解析性")
	}
	return w, nil
}

// exec 执行单个规则集设置计划项。fork 专有规则集行不可解析即跳过（§5.1-M3）。
func (w *rulesetSettingWriter) exec(rs hub.RulesetSetting, action Action) (applied, skipStat, string, error) {
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	resolvable, err := rulesetResolvable(w.r, w.rulesetCK, w.rulesetCols, rs.RulesetName)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	if !resolvable {
		return applied{}, skipStat{
			reason: fmt.Sprintf("规则集设置引用规则集 %q 在目标不可解析（fork 专有规则集不凭空插，§5.1-M3）", rs.RulesetName),
			count:  1,
		}, "", nil
	}
	targetKey := hub.RulesetSettingContentKey(rs.RulesetName, rs.Variant, rs.Key)
	var (
		found   bool
		skipped skipStat
		repoint string
	)
	err = w.r.ForEach(w.setCK, func(obj realmObjectPtr) error {
		name, gErr := w.r.GetString(obj, w.setCols["RulesetName"])
		if gErr != nil {
			return gErr
		}
		variant, gErr := w.r.GetInt64(obj, w.setCols["Variant"])
		if gErr != nil {
			return gErr
		}
		key, gErr := w.r.GetString(obj, w.setCols["Key"])
		if gErr != nil {
			return gErr
		}
		if hub.RulesetSettingContentKey(name, int(variant), key) != targetKey {
			return nil
		}
		found = true
		if action == ActionCreate {
			action = ActionUpdate
			repoint = "目标已出现同键行：新建 → 就地更新"
		}
		v, gErr := w.r.GetString(obj, w.setCols["Value"])
		if gErr != nil {
			return gErr
		}
		if v == rs.Value {
			skipped = skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}
			return errSettingRowFound
		}
		if wErr := w.r.SetString(obj, w.setCols["Value"], rs.Value); wErr != nil {
			return wErr
		}
		return errSettingRowFound
	})
	if err != nil && !errors.Is(err, errSettingRowFound) {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配规则集设置 %s: %w", targetKey, err)
	}
	if found {
		if skipped.count > 0 {
			return applied{}, skipped, repoint, nil
		}
		return applied{action: action}, skipStat{}, repoint, nil
	}
	if action == ActionUpdate {
		action = ActionCreate
		repoint = "目标缺同键行：就地更新 → 新建"
	}
	obj, err := w.r.CreateObject(w.setCK)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 新建规则集设置 %s: %w", targetKey, err)
	}
	fail := func(err error) (applied, skipStat, string, error) {
		w.r.ReleaseObject(obj)
		return applied{}, skipStat{}, "", err
	}
	for _, c := range []struct {
		name string
		val  string
	}{
		{"RulesetName", rs.RulesetName}, {"Key", rs.Key}, {"Value", rs.Value},
	} {
		if sErr := w.r.SetString(obj, w.setCols[c.name], c.val); sErr != nil {
			return fail(fmt.Errorf("deploy: 写规则集设置 %s: %w", c.name, sErr))
		}
	}
	if vErr := w.r.SetInt64(obj, w.setCols["Variant"], int64(rs.Variant)); vErr != nil {
		return fail(fmt.Errorf("deploy: 写规则集设置 Variant: %w", vErr))
	}
	w.r.ReleaseObject(obj)
	return applied{action: action}, skipStat{}, repoint, nil
}

// missingColumns 报告白名单 ∩ 目标实际列的差集（全列已必备，恒空——保留
// 与其他写入器一致的报告面）。
func (w *rulesetSettingWriter) missingColumns() []string { return nil }

// rulesetResolvable 目标端规则集可解析性（纯查询，不产生新建副作用）：
// 目标已有同名行 → 可解析；缺行但属官方四集 → 可解析（目标端 RulesetStore
// 首启播种，§5.1-M2）；其余（fork 专有规则集）不可解析。
func rulesetResolvable(r *realmc.Realm, rulesetCK uint32, rulesetCols map[string]int64, shortName string) (bool, error) {
	obj, err := r.FindFirstByString(rulesetCK, rulesetCols["ShortName"], shortName)
	if err != nil {
		return false, fmt.Errorf("deploy: 查规则集 %q: %w", shortName, err)
	}
	if obj != nil {
		r.ReleaseObject(obj)
		return true, nil
	}
	_, ok := officialRulesets[shortName]
	return ok, nil
}

// modPresetWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type modPresetWriter struct {
	r           *realmc.Realm
	presetCK    uint32
	presetCols  map[string]int64
	rulesetCK   uint32
	rulesetCols map[string]int64
	presetPK    bool
	absent      bool
	absentWhy   string
}

func prepareModPresetWriter(r *realmc.Realm) (*modPresetWriter, error) {
	w := &modPresetWriter{r: r, presetCols: map[string]int64{}, rulesetCols: map[string]int64{}}
	absent := func(why string) (*modPresetWriter, error) {
		w.absent, w.absentWhy = true, why
		return w, nil
	}
	ck, err := r.FindClass("ModPreset")
	if err != nil {
		return absent("目标库没有 ModPreset 表（版本过旧或非 lazer 系库）")
	}
	w.presetCK = ck
	for _, name := range []string{"ID", "Name", "Description", "Mods", "DeletePending", "Ruleset"} {
		if key, err := r.FindProperty(ck, name); err == nil {
			w.presetCols[name] = key
		}
	}
	// 身份匹配列必备：Name（内容键的目标准入列）。Ruleset 关系列缺失则
	// 无法承载挂接面 → 整类不可用（宁可不写）。
	for _, name := range []string{"Name", "Ruleset"} {
		if _, ok := w.presetCols[name]; !ok {
			return absent(fmt.Sprintf("目标库 ModPreset 缺 %s 列，无法做身份匹配或规则集挂接", name))
		}
	}
	rsCK, err := r.FindClass("Ruleset")
	if err != nil {
		return absent("目标库没有 Ruleset 表，无法解析模组预设规则集")
	}
	w.rulesetCK = rsCK
	for _, name := range rulesetColsWanted {
		if key, err := r.FindProperty(rsCK, name); err == nil {
			w.rulesetCols[name] = key
		}
	}
	if _, ok := w.rulesetCols["ShortName"]; !ok {
		return absent("目标库 Ruleset 缺 ShortName 列，无法解析模组预设规则集")
	}
	w.presetPK = classPrimaryKeyIs(r, ck, "ID")
	return w, nil
}

// exec 执行单个模组预设计划项：D7 mod 白名单 → Ruleset 目标端解析（§4.2
// 三态）→ 现场复核（软删防复活，HB2）→ 写入。
func (w *modPresetWriter) exec(mp hub.ModPreset, targetFork string, action Action, restore bool) (applied, skipStat, string, error) {
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	if reject := adaptModPresetModsForTarget(mp, targetFork); reject != "" {
		return applied{}, skipStat{reason: reject, count: 1}, "", nil
	}
	rsObj, ok, err := resolveRulesetObj(w.r, w.rulesetCK, w.rulesetCols, mp.RulesetShortName)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	if !ok {
		return applied{}, skipStat{
			reason: fmt.Sprintf("模组预设 %q 引用规则集 %q 在目标不可解析（fork 专有规则集不凭空插，§4.2）",
				mp.Name, mp.RulesetShortName),
			count: 1,
		}, "", nil
	}
	defer w.r.ReleaseObject(rsObj)

	var (
		found   bool
		updated bool
		skipped skipStat
		repoint string
	)
	err = w.r.ForEach(w.presetCK, func(obj realmObjectPtr) error {
		name, gErr := w.r.GetString(obj, w.presetCols["Name"])
		if gErr != nil {
			return gErr
		}
		if name != mp.Name {
			return nil
		}
		short, gErr := w.rowRulesetShortName(obj)
		if gErr != nil {
			return gErr
		}
		if short != mp.RulesetShortName {
			return nil
		}
		found = true
		if action == ActionCreate {
			action = ActionUpdate
			repoint = "目标已出现同键行：新建 → 就地更新"
		}
		// HB2 删除不复活（执行面闭环，皮肤/谱面集同款）：现场软删行唯有
		// 恢复项（用户显式勾选）才允许写回 DeletePending=false。
		if dpKey, has := w.presetCols["DeletePending"]; has {
			dp, gErr := w.r.GetBool(obj, dpKey)
			if gErr != nil {
				return gErr
			}
			if dp && !restore {
				skipped = skipStat{
					reason: "目标已软删（计划后删除），跳过防复活（§5.4 删除不复活）",
					count:  1,
				}
				return errSettingRowFound
			}
		}
		same, mErr := w.matchesCanonical(obj, mp)
		if mErr != nil {
			return mErr
		}
		if same {
			skipped = skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}
			return errSettingRowFound
		}
		if wErr := w.writeFields(obj, mp, false); wErr != nil {
			return wErr
		}
		updated = true
		return errSettingRowFound
	})
	if err != nil && !errors.Is(err, errSettingRowFound) {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配模组预设 %q: %w", mp.Name, err)
	}
	if found {
		if skipped.count > 0 {
			return applied{}, skipped, repoint, nil
		}
		_ = updated
		return applied{action: action}, skipStat{}, repoint, nil
	}
	if action == ActionUpdate {
		action = ActionCreate
		repoint = "目标缺同键行：就地更新 → 新建"
	}
	obj, err := w.createPreset(mp, rsObj)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	w.r.ReleaseObject(obj)
	return applied{action: action}, skipStat{}, repoint, nil
}

// rowRulesetShortName 读现场预设行 Ruleset 关系指向的 ShortName（空关系
// 归一为空串——业务键 ("", name) 的异常形态行，不与正常键冲突）。
func (w *modPresetWriter) rowRulesetShortName(obj realmObjectPtr) (string, error) {
	rs, err := w.r.GetLinkedObject(obj, w.presetCols["Ruleset"])
	if err != nil {
		return "", err
	}
	if rs == nil {
		return "", nil
	}
	defer w.r.ReleaseObject(rs)
	return w.r.GetString(rs, w.rulesetCols["ShortName"])
}

// createPreset 新建预设行（Guid 主键形态以 origin_guid 直接建）。Ruleset
// 挂接目标端解析行（调用方持有句柄并负责释放）。
func (w *modPresetWriter) createPreset(mp hub.ModPreset, rsObj realmObjectPtr) (realmObjectPtr, error) {
	var obj realmObjectPtr
	var err error
	if w.presetPK {
		id, pErr := parseGuid(mp.ID)
		if pErr != nil {
			return nil, pErr
		}
		obj, err = w.r.CreateObjectWithGuidPK(w.presetCK, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建模组预设 %q（Guid 主键）: %w", mp.Name, err)
		}
	} else {
		obj, err = w.r.CreateObject(w.presetCK)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建模组预设 %q: %w", mp.Name, err)
		}
	}
	if err := w.writeFields(obj, mp, true); err != nil {
		w.r.ReleaseObject(obj)
		return nil, err
	}
	if err := w.r.SetLinkedObject(obj, w.presetCols["Ruleset"], rsObj); err != nil {
		w.r.ReleaseObject(obj)
		return nil, fmt.Errorf("deploy: 链模组预设 %q → Ruleset: %w", mp.Name, err)
	}
	return obj, nil
}

// writeFields 写预设行白名单标量列。isCreate 时落 Name（内容键段）；就地
// 更新不动 Name/Ruleset（内容键命中必相等）、不动 ID（保目标 Guid）。
// DeletePending 恒写 canonical false（正常进 hub 的行即如此，S4）。
func (w *modPresetWriter) writeFields(obj realmObjectPtr, mp hub.ModPreset, isCreate bool) error {
	if isCreate {
		if !w.presetPK {
			if key, ok := w.presetCols["ID"]; ok {
				id, err := parseGuid(mp.ID)
				if err != nil {
					return err
				}
				if err := w.r.SetGuid(obj, key, id); err != nil {
					return fmt.Errorf("deploy: 写模组预设 %q ID: %w", mp.Name, err)
				}
			}
		}
		if err := w.r.SetString(obj, w.presetCols["Name"], mp.Name); err != nil {
			return fmt.Errorf("deploy: 写模组预设 %q Name: %w", mp.Name, err)
		}
	}
	for _, c := range []struct {
		name, val string
	}{
		{"Description", mp.Description}, {"Mods", mp.ModsJson},
	} {
		if key, ok := w.presetCols[c.name]; ok {
			if err := w.r.SetString(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写模组预设 %q %s: %w", mp.Name, c.name, err)
			}
		}
	}
	if key, ok := w.presetCols["DeletePending"]; ok {
		if err := w.r.SetBool(obj, key, mp.DeletePending); err != nil {
			return fmt.Errorf("deploy: 写模组预设 %q DeletePending: %w", mp.Name, err)
		}
	}
	return nil
}

// matchesCanonical 现场预设行是否已与 canonical 写面一致（重续判定输入）。
// 与 writeFields 严格对偶：比 Description/Mods/DeletePending（白名单 ∩
// 实际列）；ID/Name/Ruleset 是键与 Guid 面，不比。
func (w *modPresetWriter) matchesCanonical(obj realmObjectPtr, mp hub.ModPreset) (bool, error) {
	for _, c := range []struct {
		name, val string
	}{
		{"Description", mp.Description}, {"Mods", mp.ModsJson},
	} {
		key, ok := w.presetCols[c.name]
		if !ok {
			continue
		}
		null, err := w.r.IsNull(obj, key)
		if err != nil {
			return false, err
		}
		if null {
			if c.val != "" {
				return false, nil
			}
			continue
		}
		v, err := w.r.GetString(obj, key)
		if err != nil {
			return false, err
		}
		if v != c.val {
			return false, nil
		}
	}
	if key, ok := w.presetCols["DeletePending"]; ok {
		v, err := w.r.GetBool(obj, key)
		if err != nil {
			return false, err
		}
		if v != mp.DeletePending {
			return false, nil
		}
	}
	return true, nil
}

// missingColumns 报告白名单 ∩ 目标实际列的差集（"类.列" 形态；结构列缺失
// 已走 absent 不在此列）。
func (w *modPresetWriter) missingColumns() []string {
	if w.absent {
		return nil
	}
	var out []string
	for _, name := range []string{"ID", "Description", "Mods", "DeletePending"} {
		if _, ok := w.presetCols[name]; !ok {
			out = append(out, "ModPreset."+name)
		}
	}
	return out
}
