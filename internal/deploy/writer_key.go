package deploy

// 键位写入器（M3，design.md §5.1-M3 / §4.2 / §5.4-D20）：
//   - 身份匹配：内容键 (RulesetName, Variant, ActionInt)（§4.2 第一层）。
//     RulesetName/Variant 是可空列（null = 全局键位/无变体），null 值无法
//     走 FindFirstByString 的单列查询——全表扫定位首键匹配行，匹配行的
//     全部处理在 ForEach 回调内完成（realmc 契约：句柄仅回调内有效，
//     session.go），哨兵错误中断迭代；
//   - D20：ActionInt 按源端（origin_fork）与目标端枚举域交集过滤，越域
//     跳过计数（compat.keyBindingActionAllowed）；RulesetName 是字符串列
//     无需解析（§4.2），fork 专有规则集的键位行随行带走；
//   - 键位表无软删列（lazer-baseline §2.12），无 HB2 复活防线面；
//     就地更新只写 KeyCombination（唯一非键业务列，保目标 Guid，§4.2）。

import (
	"errors"
	"fmt"

	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// errKeyBindingRowFound 是 ForEach 定位到键位匹配行后的哨兵（中断迭代，
// 上层以 errors.Is 识别为正常路径）。
var errKeyBindingRowFound = errors.New("deploy: 键位匹配行已定位")

// keyBindingWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type keyBindingWriter struct {
	r         *realmc.Realm
	class     uint32 // KeyBinding class key；absent=true 时无效
	cols      map[string]int64
	pkID      bool
	absent    bool
	absentWhy string
}

func prepareKeyBindingWriter(r *realmc.Realm) (*keyBindingWriter, error) {
	w := &keyBindingWriter{r: r, cols: map[string]int64{}}
	ck, err := r.FindClass("KeyBinding")
	if err != nil {
		w.absent, w.absentWhy = true, "目标库没有 KeyBinding 表（版本过旧或非 lazer 系库）"
		return w, nil
	}
	w.class = ck
	for _, name := range []string{"ID", "RulesetName", "Variant", "Action", "KeyCombination"} {
		if key, err := r.FindProperty(ck, name); err == nil {
			w.cols[name] = key
		}
	}
	// 身份匹配列必备：内容键三段（RulesetName/Variant/Action）。缺任一段
	// 无法读出现场行键 → 整类不可用（宁可不写）。
	for _, name := range []string{"RulesetName", "Variant", "Action"} {
		if _, ok := w.cols[name]; !ok {
			w.absent, w.absentWhy = true,
				fmt.Sprintf("目标库 KeyBinding 缺 %s 列，无法做身份匹配", name)
			return w, nil
		}
	}
	w.pkID = classPrimaryKeyIs(r, ck, "ID")
	return w, nil
}

// exec 执行单个键位计划项（§3.3 执行期现场复核 + D20 过滤 + 写入）。
// originFork/ targetFork 驱动枚举域交集判定（HB3 origin_fork 面与目标端）。
func (w *keyBindingWriter) exec(kb hub.KeyBinding, originFork, targetFork string, action Action) (applied, skipStat, string, error) {
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	if ok, reject := keyBindingActionAllowed(originFork, targetFork, kb); !ok {
		return applied{}, skipStat{reason: reject, count: 1}, "", nil
	}
	targetKey := hub.KeyBindingContentKey(kb.RulesetName, kb.Variant, kb.ActionInt)
	var (
		found   bool
		updated bool
		skipped skipStat
		repoint string
	)
	err := w.r.ForEach(w.class, func(obj realmObjectPtr) error {
		key, kErr := w.rowContentKey(obj)
		if kErr != nil {
			return kErr
		}
		if key != targetKey {
			return nil
		}
		found = true
		if action == ActionCreate {
			action = ActionUpdate // 目标已出现同键行：新建 → 就地更新（§3.3 改判例）
			repoint = "目标已出现同键行：新建 → 就地更新"
		}
		same, mErr := w.matchesCanonical(obj, kb)
		if mErr != nil {
			return mErr
		}
		if same {
			skipped = skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}
			return errKeyBindingRowFound
		}
		if wErr := w.writeFields(obj, kb, false); wErr != nil {
			return wErr
		}
		updated = true
		return errKeyBindingRowFound
	})
	if err != nil && !errors.Is(err, errKeyBindingRowFound) {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配键位 %s: %w", targetKey, err)
	}
	if found {
		if skipped.count > 0 {
			return applied{}, skipped, repoint, nil
		}
		_ = updated // 匹配即已就地更新（否则上方已 return）
		return applied{action: action}, skipStat{}, repoint, nil
	}
	if action == ActionUpdate {
		action = ActionCreate
		repoint = "目标缺同键行：就地更新 → 新建"
	}
	obj, err := w.createRow(kb)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	w.r.ReleaseObject(obj)
	return applied{action: action}, skipStat{}, repoint, nil
}

// rowContentKey 读现场行的内容键（可空键段 IsNull 判定 → 指针语义 → 编码；
// 与投影端 projectKeyBinding 同一对偶形态）。
func (w *keyBindingWriter) rowContentKey(obj realmObjectPtr) (string, error) {
	var rulesetName *string
	if null, err := w.r.IsNull(obj, w.cols["RulesetName"]); err != nil {
		return "", err
	} else if !null {
		v, gErr := w.r.GetString(obj, w.cols["RulesetName"])
		if gErr != nil {
			return "", gErr
		}
		rulesetName = &v
	}
	var variant *int
	if null, err := w.r.IsNull(obj, w.cols["Variant"]); err != nil {
		return "", err
	} else if !null {
		v, gErr := w.r.GetInt64(obj, w.cols["Variant"])
		if gErr != nil {
			return "", gErr
		}
		vv := int(v)
		variant = &vv
	}
	action, err := w.r.GetInt64(obj, w.cols["Action"])
	if err != nil {
		return "", err
	}
	return hub.KeyBindingContentKey(rulesetName, variant, int(action)), nil
}

// createRow 新建键位行：Guid 主键形态以 origin_guid 直接建，无主键形态
// 建空行后写 ID；随后落内容键三段（可空段写 null 保真）与业务列。
func (w *keyBindingWriter) createRow(kb hub.KeyBinding) (realmObjectPtr, error) {
	var obj realmObjectPtr
	var err error
	if w.pkID {
		id, pErr := parseGuid(kb.ID)
		if pErr != nil {
			return nil, pErr
		}
		obj, err = w.r.CreateObjectWithGuidPK(w.class, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建键位（Guid 主键）: %w", err)
		}
	} else {
		obj, err = w.r.CreateObject(w.class)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建键位: %w", err)
		}
	}
	if err := w.writeFields(obj, kb, true); err != nil {
		w.r.ReleaseObject(obj)
		return nil, err
	}
	return obj, nil
}

// writeFields 写键位行白名单列。isCreate 时落内容键三段（RulesetName/
// Variant 可空段写 null 保真——与投影端对偶）；就地更新不动键三段
// （内容键命中必相等，写它是死代码，§4.2）与 ID（保目标 Guid）。
func (w *keyBindingWriter) writeFields(obj realmObjectPtr, kb hub.KeyBinding, isCreate bool) error {
	if isCreate {
		if !w.pkID {
			if key, ok := w.cols["ID"]; ok {
				id, err := parseGuid(kb.ID)
				if err != nil {
					return err
				}
				if err := w.r.SetGuid(obj, key, id); err != nil {
					return fmt.Errorf("deploy: 写键位 ID: %w", err)
				}
			}
		}
		if kb.RulesetName == nil {
			if err := w.r.SetNull(obj, w.cols["RulesetName"]); err != nil {
				return fmt.Errorf("deploy: 置键位 RulesetName null: %w", err)
			}
		} else if err := w.r.SetString(obj, w.cols["RulesetName"], *kb.RulesetName); err != nil {
			return fmt.Errorf("deploy: 写键位 RulesetName: %w", err)
		}
		if kb.Variant == nil {
			if err := w.r.SetNull(obj, w.cols["Variant"]); err != nil {
				return fmt.Errorf("deploy: 置键位 Variant null: %w", err)
			}
		} else {
			if err := w.r.SetInt64(obj, w.cols["Variant"], int64(*kb.Variant)); err != nil {
				return fmt.Errorf("deploy: 写键位 Variant: %w", err)
			}
		}
		if err := w.r.SetInt64(obj, w.cols["Action"], int64(kb.ActionInt)); err != nil {
			return fmt.Errorf("deploy: 写键位 Action: %w", err)
		}
	}
	if key, ok := w.cols["KeyCombination"]; ok {
		if err := w.r.SetString(obj, key, kb.KeyCombination); err != nil {
			return fmt.Errorf("deploy: 写键位 KeyCombination: %w", err)
		}
	}
	return nil
}

// matchesCanonical 现场键位行是否已与 canonical 写面一致（重续判定输入）。
// 与 writeFields 严格对偶：只比 KeyCombination（写面 update 的唯一列）；
// 该列缺失时无写入面亦无判别面，视为一致（无谓的重写只会空转）。
func (w *keyBindingWriter) matchesCanonical(obj realmObjectPtr, kb hub.KeyBinding) (bool, error) {
	key, ok := w.cols["KeyCombination"]
	if !ok {
		return true, nil
	}
	null, err := w.r.IsNull(obj, key)
	if err != nil {
		return false, err
	}
	if null {
		return kb.KeyCombination == "", nil
	}
	v, err := w.r.GetString(obj, key)
	if err != nil {
		return false, err
	}
	return v == kb.KeyCombination, nil
}

// missingColumns 报告白名单 ∩ 目标实际列的差集（身份键三段缺失已走
// absent 不在此列）。
func (w *keyBindingWriter) missingColumns() []string {
	if w.absent {
		return nil
	}
	var out []string
	for _, name := range []string{"ID", "KeyCombination"} {
		if _, ok := w.cols[name]; !ok {
			out = append(out, "KeyBinding."+name)
		}
	}
	return out
}
