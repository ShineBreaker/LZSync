package deploy

// 收藏夹写入器（M1 唯一投放载体，§5.1：无 files、无跨表关系、无 Guid
// 依赖——纯 MD5 列表）。写入纪律：
//   - 写入集 = 白名单列 ∩ 目标库实际列（S1）；缺列计数 + 人话原因，
//     不静默吞（§1.5-4）；
//   - 每项以目标库现场状态重跑身份匹配（§3.3 执行期现场复核），计划与
//     现场不符按现场判定执行并计入改判；
//   - 就地更新保目标 Guid（§4.2）；新建以 origin_guid 为主键（§4.4）；
//   - 删除语义：收藏夹无软删，整行 Remove（S4 唯一例外）——M1 计划面
//     只有 create/update，删除投放不在默认链路。

import (
	"fmt"
	"sort"
	"unsafe"

	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// realmObjectPtr 是 realm_object_t* 的包内别名（realmc 行句柄）。
type realmObjectPtr = unsafe.Pointer

// 收藏夹白名单列（official v51 基线，hub DTO 的 realm 侧落点）。
const (
	clsCollection   = "BeatmapCollection"
	colCollectionID = "ID"
	colCollectionName = "Name"
	colCollectionMD5  = "BeatmapMD5Hashes"
	colCollectionLast = "LastModified"
)

// collectionWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type collectionWriter struct {
	r     *realmc.Realm
	class uint32 // BeatmapCollection class key；absent=true 时无效
	// 白名单 ∩ 目标实际列（S1）。Name 缺失视为类不可用（无法身份匹配）。
	cols map[string]int64
	// pkID 为 true 表示目标库 BeatmapCollection.ID 是主键（真实库形态）；
	// false 为无主键形态（fixture 镜像）——仅影响建行入口。
	pkID    bool
	absent  bool // 目标库无 BeatmapCollection 类或缺 Name 列
	absentWhy string
}

func prepareCollectionWriter(r *realmc.Realm) (*collectionWriter, error) {
	w := &collectionWriter{r: r, cols: map[string]int64{}}
	ck, err := r.FindClass(clsCollection)
	if err != nil {
		w.absent, w.absentWhy = true, "目标库没有 BeatmapCollection 表（版本过旧或非 lazer 系库）"
		return w, nil
	}
	w.class = ck
	for _, name := range []string{colCollectionID, colCollectionName, colCollectionMD5, colCollectionLast} {
		if key, err := r.FindProperty(ck, name); err == nil {
			w.cols[name] = key
		}
	}
	if _, ok := w.cols[colCollectionName]; !ok {
		w.absent, w.absentWhy = true, "目标库 BeatmapCollection 缺 Name 列，无法做身份匹配"
		return w, nil
	}
	w.pkID = classPrimaryKeyIs(r, ck, colCollectionID)
	return w, nil
}

// classPrimaryKeyIs 判断 class 的主键列是否为指定属性。
func classPrimaryKeyIs(r *realmc.Realm, ck uint32, prop string) bool {
	classes, err := r.Classes()
	if err != nil {
		return false
	}
	for _, c := range classes {
		if c.Key == ck {
			return c.PrimaryKey == prop
		}
	}
	return false
}

// applied 与 skipStat 是单计划项的执行产物。
type applied struct{ action Action }
type skipStat struct {
	reason string
	count  int
}

// exec 执行单个收藏夹计划项（§3.3 执行期现场复核 + 写入）。
// 返回 (applied, skip, repoint 原因, error)；error 非空 = 写入异常，
// 调用方整批回滚。skip 非空时该项不落地（计数 + 人话原因）。
func (w *collectionWriter) exec(c hub.Collection, action Action) (applied, skipStat, string, error) {
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	// 现场身份匹配：内容键 = Name（§4.2 第一层）。
	existing, err := w.r.FindFirstByString(w.class, w.cols[colCollectionName], c.Name)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配收藏 %q: %w", c.Name, err)
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

	var obj realmObjectPtr
	if existing != nil {
		obj = existing
		defer w.r.ReleaseObject(obj)
		// 重续安全：已一致 → 跳过（§3.3「已存在 → 跳过」）。
		same, err := w.matchesCanonical(obj, c)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		if same {
			return applied{}, skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}, repoint, nil
		}
	} else {
		obj, err = w.createRow(c)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		defer w.r.ReleaseObject(obj)
	}

	if err := w.writeFields(obj, c, existing != nil); err != nil {
		return applied{}, skipStat{}, "", err
	}
	return applied{action: action}, skipStat{}, repoint, nil
}

// createRow 建新行：主键形态以 origin_guid 直接建（同 Guid 已存在 =
// 内容键外的罕见冲突，如实上抛整批回滚）；无主键形态建空行后写 ID。
func (w *collectionWriter) createRow(c hub.Collection) (realmObjectPtr, error) {
	id, err := parseGuid(c.ID)
	if err != nil {
		return nil, err
	}
	if w.pkID {
		obj, err := w.r.CreateObjectWithGuidPK(w.class, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建收藏 %q（Guid 主键）: %w", c.Name, err)
		}
		return obj, nil
	}
	obj, err := w.r.CreateObject(w.class)
	if err != nil {
		return nil, fmt.Errorf("deploy: 新建收藏 %q: %w", c.Name, err)
	}
	if key, ok := w.cols[colCollectionID]; ok {
		if err := w.r.SetGuid(obj, key, id); err != nil {
			w.r.ReleaseObject(obj)
			return nil, fmt.Errorf("deploy: 写收藏 %q ID: %w", c.Name, err)
		}
	}
	return obj, nil
}

// writeFields 写白名单列 ∩ 目标实际列。isUpdate 时不动 ID（就地更新保
// 目标 Guid，§4.2）；缺列计数由调用侧聚合（返回缺失列名切片）。
func (w *collectionWriter) writeFields(obj realmObjectPtr, c hub.Collection, isUpdate bool) error {
	if err := w.r.SetString(obj, w.cols[colCollectionName], c.Name); err != nil {
		return fmt.Errorf("deploy: 写收藏 %q Name: %w", c.Name, err)
	}
	if key, ok := w.cols[colCollectionLast]; ok {
		if err := w.r.SetTimestamp(obj, key, c.LastModified); err != nil {
			return fmt.Errorf("deploy: 写收藏 %q LastModified: %w", c.Name, err)
		}
	}
	if key, ok := w.cols[colCollectionMD5]; ok {
		if err := w.replaceMD5List(obj, key, c.BeatmapMD5Hashes); err != nil {
			return err
		}
	}
	return nil
}

// replaceMD5List 整段重写 BeatmapMD5Hashes 列表（清空重灌——收藏成员是
// 无序集合语义，就地 diff 无收益）。
func (w *collectionWriter) replaceMD5List(obj realmObjectPtr, key int64, hashes []string) error {
	lst, err := w.r.GetList(obj, key)
	if err != nil {
		return fmt.Errorf("deploy: 取收藏成员列表: %w", err)
	}
	defer lst.Close()
	if err := lst.Clear(); err != nil {
		return fmt.Errorf("deploy: 清收藏成员列表: %w", err)
	}
	for _, h := range hashes {
		if err := lst.AddString(h); err != nil {
			return fmt.Errorf("deploy: 写收藏成员 %s: %w", h, err)
		}
	}
	return nil
}

// matchesCanonical 现场行是否已与 canonical 值一致（重续判定输入）。
// 只比白名单 ∩ 实际列（缺列无从比较，视为可写面而非一致性面）。
func (w *collectionWriter) matchesCanonical(obj realmObjectPtr, c hub.Collection) (bool, error) {
	name, err := w.r.GetString(obj, w.cols[colCollectionName])
	if err != nil || name != c.Name {
		return false, err
	}
	if key, ok := w.cols[colCollectionLast]; ok {
		ts, err := w.r.GetTimestamp(obj, key)
		if err != nil || !ts.Equal(c.LastModified) {
			return false, err
		}
	}
	if key, ok := w.cols[colCollectionMD5]; ok {
		same, err := w.listMatches(obj, key, c.BeatmapMD5Hashes)
		if err != nil || !same {
			return false, err
		}
	}
	return true, nil
}

func (w *collectionWriter) listMatches(obj realmObjectPtr, key int64, want []string) (bool, error) {
	lst, err := w.r.GetList(obj, key)
	if err != nil {
		return false, err
	}
	defer lst.Close()
	n, err := lst.Size()
	if err != nil {
		return false, err
	}
	if int(n) != len(want) {
		return false, nil
	}
	got := make([]string, 0, n)
	for i := uint64(0); i < n; i++ {
		s, err := lst.GetString(i)
		if err != nil {
			return false, err
		}
		got = append(got, s)
	}
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	for i := range got {
		if got[i] != sortedWant[i] {
			return false, nil
		}
	}
	return true, nil
}

// missingColumns 报告白名单 ∩ 目标实际列的差集（人话原因的列名清单）。
func (w *collectionWriter) missingColumns() []string {
	if w.absent {
		return nil
	}
	var out []string
	for _, name := range []string{colCollectionID, colCollectionMD5, colCollectionLast} {
		if _, ok := w.cols[name]; !ok {
			out = append(out, name)
		}
	}
	return out
}
