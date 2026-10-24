package deploy

// 皮肤写入器（M2，design.md §5.1-M2 / §5.3）：Set 行白名单标量列 +
// Files 列表（RealmNamedFileUsage：File 链接 + Filename）+ RealmFile 行
// 同事务（Find ?? Create，S6；files/ 链接先行由 CopyMissing 在批前完成，
// §5.3 顺序铁律）。
//
// 写入防线（投影跳过规则 §4.1 的对偶——正常进 hub 的行恒非 Protected /
// 非 fork 专有；hub 出现即异常路径，写入器侧宁可不写并计数）：
//   - Protected 内置皮肤：固定 Guid 四端自备，同步无收益（lazer-baseline
//     §2.10 / fork-comparison C6）；
//   - fork 专有皮肤（OwnSkins 指纹命中）：跨端投放无意义且可能覆盖目标
//     自备行（§5.4-D21 的泛化判定）。
//
// 就地更新（同 Hash 内容键命中，§4.2）：只更新标量白名单列，保目标 Guid；
// 同 Hash 不替换 Files 列表（Skin Hash = .ini/.json 拼接，同 Hash 的哈希型
// 文件内容等价，非哈希文件保留目标端现状——与谱面集 update 不动列表的
// 先例一致）。皮肤无 OnlineID 列，无 S8 面。

import (
	"fmt"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// 皮肤白名单标量列（official v51/v52 基线，lazer-baseline §2.10）。
// ID/Hash 是主键与内容键面，不参与缺列降级计数。
var skinScalarCols = []string{
	"ID", "Name", "Creator", "InstantiationInfo", "Hash", "Protected", "DeletePending",
}

// skinWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type skinWriter struct {
	r *realmc.Realm
	// class key（RealmNamedFileUsage 是 embedded 类，经列表挂接）。
	skinCK, fileCK, usageCK       uint32
	skinCols, fileCols, usageCols map[string]int64
	skinPK, filePK                bool
	absent                        bool
	absentWhy                     string
}

func prepareSkinWriter(r *realmc.Realm) (*skinWriter, error) {
	w := &skinWriter{r: r}
	w.skinCols, w.fileCols, w.usageCols = map[string]int64{}, map[string]int64{}, map[string]int64{}
	absent := func(why string) (*skinWriter, error) {
		w.absent, w.absentWhy = true, why
		return w, nil
	}

	for _, c := range []struct {
		name string
		key  *uint32
		cols map[string]int64
		want []string
	}{
		{"Skin", &w.skinCK, w.skinCols, append(append([]string(nil), skinScalarCols...), "Files")},
		{"File", &w.fileCK, w.fileCols, []string{"Hash"}},
		{"RealmNamedFileUsage", &w.usageCK, w.usageCols, []string{"File", "Filename"}},
	} {
		ck, err := r.FindClass(c.name)
		if err != nil {
			switch c.name {
			case "Skin":
				return absent("目标库没有 Skin 表（版本过旧或非 lazer 系库）")
			case "File":
				return absent("目标库没有 File 表，无法写皮肤 Files 链路")
			default:
				return absent("目标库没有 RealmNamedFileUsage 类（embedded 结构缺类）")
			}
		}
		*c.key = ck
		for _, name := range c.want {
			if key, err := r.FindProperty(ck, name); err == nil {
				c.cols[name] = key
			}
		}
	}

	// 身份匹配列必备：Skin.Hash（内容键，§4.2 第一层）。
	if _, ok := w.skinCols["Hash"]; !ok {
		return absent("目标库 Skin 缺 Hash 列，无法做身份匹配")
	}
	// 结构列必备（缺失 = 无法挂接 Files 全链，宁可不写；标量缺列走降级计数）。
	for _, c := range []struct {
		cls  string
		cols map[string]int64
		want []string
	}{
		{"Skin", w.skinCols, []string{"Files"}},
		{"RealmNamedFileUsage", w.usageCols, []string{"File", "Filename"}},
		{"File", w.fileCols, []string{"Hash"}},
	} {
		for _, name := range c.want {
			if _, ok := c.cols[name]; !ok {
				return absent(fmt.Sprintf("目标库 %s 缺结构列 %s，无法写皮肤 Files 链路", c.cls, name))
			}
		}
	}

	w.skinPK = classPrimaryKeyIs(r, w.skinCK, "ID")
	w.filePK = classPrimaryKeyIs(r, w.fileCK, "Hash")
	return w, nil
}

// forkOwnSkinHit 判定皮肤指纹是否命中任一注册 fork 的专有皮肤声明
// （§4.3：fork 知识只在 ForkProfile；投放侧泛化为全注册表判定——专有
// 皮肤属于其宿主 fork 自备，跨端落地无收益且可能覆盖目标端自备行）。
func forkOwnSkinHit(profiles []forks.ForkProfile, name, creator string) bool {
	for _, p := range profiles {
		for _, fp := range p.OwnSkins {
			if fp.Name == name && fp.Creator == creator {
				return true
			}
		}
	}
	return false
}

// exec 执行单个皮肤计划项（§3.3 执行期现场复核 + 写入）。restore 标记
// 恢复项：现场同键行已软删时唯有恢复项才允许写回 DeletePending=false
//（HB2 删除不复活，谱面集写入器同款）。
func (w *skinWriter) exec(sk hub.Skin, action Action, restore bool) (applied, skipStat, string, error) {
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	if sk.Protected {
		return applied{}, skipStat{reason: fmt.Sprintf("皮肤 %q 是 Protected 内置皮肤（固定 Guid 四端自备，不投放）", sk.Name), count: 1}, "", nil
	}
	if forkOwnSkinHit(forks.All(), sk.Name, sk.Creator) {
		return applied{}, skipStat{reason: fmt.Sprintf("皮肤 %q（作者 %q）命中 fork 专有皮肤指纹（OwnSkins），不跨端投放", sk.Name, sk.Creator), count: 1}, "", nil
	}
	// 现场身份匹配：内容键 = Hash（§4.2 第一层）。
	existing, err := w.r.FindFirstByString(w.skinCK, w.skinCols["Hash"], sk.Hash)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配皮肤 %s: %w", sk.Hash, err)
	}
	repoint := ""
	switch {
	case existing != nil && action == ActionCreate:
		// 目标已出现同键行：新建 → 就地更新（§3.3 改判例）。
		action = ActionUpdate
		repoint = "目标已出现同键行：新建 → 就地更新"
	case existing == nil && action == ActionUpdate:
		action = ActionCreate
		repoint = "目标缺同键行：就地更新 → 新建"
	}

	if existing != nil {
		defer w.r.ReleaseObject(existing)
		// HB2 删除不复活（执行面闭环，谱面集写入器同款）：计划后删除的
		// 现场软删行跳过防复活——就地更新会把 DeletePending 写回 false；
		// 恢复项的复活是用户显式意图，放行。
		if dpKey, ok := w.skinCols["DeletePending"]; ok {
			dp, err := w.r.GetBool(existing, dpKey)
			if err != nil {
				return applied{}, skipStat{}, "", fmt.Errorf("deploy: 读现场皮肤 %s DeletePending: %w", sk.Hash, err)
			}
			if dp && !restore {
				return applied{}, skipStat{
					reason: "目标已软删（计划后删除），跳过防复活（§5.4 删除不复活）",
					count:  1,
				}, repoint, nil
			}
		}
		// 重续安全：已一致 → 跳过。判定面 = 写面的对偶（Files 列表在同
		// Hash 语义下哈希型内容等价，不比，§4.2）。
		same, err := w.matchesCanonical(existing, sk)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		if same {
			return applied{}, skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}, repoint, nil
		}
		if err := w.writeSkinScalars(existing, sk, false); err != nil {
			return applied{}, skipStat{}, "", err
		}
		return applied{action: action}, skipStat{}, repoint, nil
	}

	obj, err := w.createSkin(sk)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	w.r.ReleaseObject(obj)
	return applied{action: ActionCreate}, skipStat{}, repoint, nil
}

// createSkin 新建皮肤行全链（行 + 标量白名单 + Files usage）。同 Guid 主键
// 已存在 = 内容键外的罕见冲突，如实上抛整批回滚（§4.2 碰撞语义）。
func (w *skinWriter) createSkin(sk hub.Skin) (realmObjectPtr, error) {
	var obj realmObjectPtr
	var err error
	if w.skinPK {
		id, err := parseGuid(sk.ID)
		if err != nil {
			return nil, err
		}
		obj, err = w.r.CreateObjectWithGuidPK(w.skinCK, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建皮肤 %q（Guid 主键）: %w", sk.Name, err)
		}
	} else {
		obj, err = w.r.CreateObject(w.skinCK)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建皮肤 %q: %w", sk.Name, err)
		}
	}
	fail := func(err error) (realmObjectPtr, error) {
		w.r.ReleaseObject(obj)
		return nil, err
	}
	if err := w.writeSkinScalars(obj, sk, true); err != nil {
		return fail(err)
	}
	if len(sk.Files) > 0 {
		lst, err := w.r.GetList(obj, w.skinCols["Files"])
		if err != nil {
			return fail(fmt.Errorf("deploy: 取皮肤 %q Files 列表: %w", sk.Name, err))
		}
		defer lst.Close()
		for _, nf := range sk.Files {
			if err := appendNamedFileUsage(w.r, lst, w.fileCK, w.filePK, w.fileCols, w.usageCols, nf); err != nil {
				return fail(err)
			}
		}
	}
	return obj, nil
}

// writeSkinScalars 写皮肤行白名单标量列。isUpdate 时不动 ID / Hash（保目标
// Guid，§4.2；内容键命中必相等，写它是死代码）。Protected/DeletePending
// 恒写 canonical 值（正常进 hub 的行即 false，§4.1）。
func (w *skinWriter) writeSkinScalars(obj realmObjectPtr, sk hub.Skin, isCreate bool) error {
	if isCreate {
		if !w.skinPK {
			if key, ok := w.skinCols["ID"]; ok {
				id, err := parseGuid(sk.ID)
				if err != nil {
					return err
				}
				if err := w.r.SetGuid(obj, key, id); err != nil {
					return fmt.Errorf("deploy: 写皮肤 %q ID: %w", sk.Name, err)
				}
			}
		}
		if key, ok := w.skinCols["Hash"]; ok {
			if err := w.r.SetString(obj, key, sk.Hash); err != nil {
				return fmt.Errorf("deploy: 写皮肤 %q Hash: %w", sk.Name, err)
			}
		}
	}
	for _, c := range []struct{ name, val string }{
		{"Name", sk.Name}, {"Creator", sk.Creator},
		{"InstantiationInfo", sk.InstantiationInfo},
	} {
		if key, ok := w.skinCols[c.name]; ok {
			if err := w.r.SetString(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写皮肤 %q %s: %w", sk.Name, c.name, err)
			}
		}
	}
	for _, c := range []struct {
		name string
		val  bool
	}{
		{"Protected", sk.Protected}, {"DeletePending", sk.DeletePending},
	} {
		if key, ok := w.skinCols[c.name]; ok {
			if err := w.r.SetBool(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写皮肤 %q %s: %w", sk.Name, c.name, err)
			}
		}
	}
	return nil
}

// matchesCanonical 现场皮肤行是否已与 canonical 写面一致（重续判定输入）。
// 与 writeSkinScalars 严格对偶：不写的列（ID/Hash/Files 列表）不比。
func (w *skinWriter) matchesCanonical(obj realmObjectPtr, sk hub.Skin) (bool, error) {
	for _, c := range []struct{ name, val string }{
		{"Name", sk.Name}, {"Creator", sk.Creator},
		{"InstantiationInfo", sk.InstantiationInfo},
	} {
		if key, ok := w.skinCols[c.name]; ok {
			v, err := w.r.GetString(obj, key)
			if err != nil || v != c.val {
				return false, err
			}
		}
	}
	for _, c := range []struct {
		name string
		val  bool
	}{
		{"Protected", sk.Protected}, {"DeletePending", sk.DeletePending},
	} {
		if key, ok := w.skinCols[c.name]; ok {
			v, err := w.r.GetBool(obj, key)
			if err != nil || v != c.val {
				return false, err
			}
		}
	}
	return true, nil
}

// missingColumns 报告白名单 ∩ 目标实际列的差集（"类.列" 形态；结构列缺失
// 已走 absent 不在此列）。
func (w *skinWriter) missingColumns() []string {
	if w.absent {
		return nil
	}
	var out []string
	for _, name := range skinScalarCols {
		if name == "ID" || name == "Hash" {
			continue
		}
		if _, ok := w.skinCols[name]; !ok {
			out = append(out, "Skin."+name)
		}
	}
	return out
}
