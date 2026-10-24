package realmc

// list 列操作（realm.h:1649-1832）：覆盖 lazer 系四类列表列——
// Beatmaps（对象列表，前向链接）、Files（embedded 对象列表）、
// BeatmapMD5Hashes（string 列表）、Pauses（int 列表）。

import (
	"fmt"
	"runtime"
	"unsafe"
)

// List 是 realm_list_t 的包装。非并发安全；必须在拥有它的 Realm 关闭前
// Close，且仅在取列表时所处的同一事务内有效。
type List struct {
	r *Realm
	h unsafe.Pointer
}

// GetList 取对象的 list 属性句柄（realm_get_list，realm.h:1649）。
// 用毕 Close；同一 (对象, 属性) 反复取会得到等价的新句柄，各自释放。
func (r *Realm) GetList(obj unsafe.Pointer, propKey int64) (*List, error) {
	h := r.lib.realmGetList(obj, propKey)
	if h == nil {
		return nil, fmt.Errorf("realm_get_list: %w", r.lib.lastError())
	}
	return &List{r: r, h: h}, nil
}

// Close 释放 realm_list_t 句柄。幂等。
func (l *List) Close() {
	if l.h != nil {
		l.r.lib.realmRelease(l.h)
		l.h = nil
	}
}

// Size 返回列表元素数（realm_list_size，realm.h:1698）。
func (l *List) Size() (uint64, error) {
	var n uint64
	if !l.r.lib.realmListSize(l.h, &n) {
		return 0, fmt.Errorf("realm_list_size: %w", l.r.lib.lastError())
	}
	return n, nil
}

// GetString 读 string 列表元素（BeatmapMD5Hashes；立即拷贝）。
func (l *List) GetString(index uint64) (string, error) {
	v, err := l.get(index)
	if err != nil {
		return "", err
	}
	switch v.typ {
	case rlmTypeString:
		data, size := v.asString()
		if data == nil {
			return "", nil
		}
		return string(unsafe.Slice((*byte)(data), size)), nil
	case rlmTypeNull:
		return "", nil
	default:
		return "", fmt.Errorf("列表元素类型非 STRING（实际 type=%d）", v.typ)
	}
}

// AddString 追加 string 元素（realm_list_insert 尾插）。
func (l *List) AddString(s string) error {
	b := cBytes(s)
	v := stringValue(unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b)-1))
	err := l.insertAtEnd(v)
	runtime.KeepAlive(b) // C 拷贝完成前字节必须存活
	return err
}

// SetString 覆写 index 处的 string 元素。
func (l *List) SetString(index uint64, s string) error {
	b := cBytes(s)
	v := stringValue(unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b)-1))
	err := l.set(index, v)
	runtime.KeepAlive(b)
	return err
}

// GetInt64 读 int 列表元素（Pauses，毫秒偏移列表）。
func (l *List) GetInt64(index uint64) (int64, error) {
	v, err := l.get(index)
	if err != nil {
		return 0, err
	}
	if v.typ != rlmTypeInt {
		return 0, fmt.Errorf("列表元素类型非 INT（实际 type=%d）", v.typ)
	}
	return v.asInt64(), nil
}

// AddInt64 追加 int 元素。
func (l *List) AddInt64(n int64) error {
	return l.insertAtEnd(int64Value(n))
}

// SetInt64 覆写 index 处的 int 元素。
func (l *List) SetInt64(index uint64, n int64) error {
	return l.set(index, int64Value(n))
}

// GetLinkedObject 读对象列表元素（Beatmaps），返回 realm_object_t* 句柄，
// 由调用方 ReleaseObject 释放。指向已删除对象时返回 (nil, nil)。
func (l *List) GetLinkedObject(index uint64) (unsafe.Pointer, error) {
	l.r.lib.clearLastError() // NULL 双解：空/悬空槽位 vs 异常，先清后查
	obj := l.r.lib.realmListGetLinkedObject(l.h, index)
	if obj == nil {
		var e realmError
		if l.r.lib.realmGetLastError(&e) {
			return nil, l.r.lib.lastError()
		}
		return nil, nil
	}
	return obj, nil
}

// AddLinkedObject 追加对象链接元素（BeatmapSet.Beatmaps.Add(beatmap)）。
func (l *List) AddLinkedObject(obj unsafe.Pointer) error {
	return l.insertAtEnd(l.r.linkValueOf(obj))
}

// SetLinkedObject 覆写 index 处的对象链接元素。
func (l *List) SetLinkedObject(index uint64, obj unsafe.Pointer) error {
	return l.set(index, l.r.linkValueOf(obj))
}

// AddEmbedded 在列表尾部创建并追加新 embedded 对象（Files.Add：
// realm_list_insert_embedded，realm.h:1795），返回其行句柄。
func (l *List) AddEmbedded() (unsafe.Pointer, error) {
	n, err := l.Size()
	if err != nil {
		return nil, err
	}
	emb := l.r.lib.realmListInsertEmbedded(l.h, n)
	if emb == nil {
		return nil, fmt.Errorf("realm_list_insert_embedded: %w", l.r.lib.lastError())
	}
	return emb, nil
}

// SetEmbedded 在 index 处创建 embedded 对象并替换原元素（旧 embedded
// 对象随之销毁——embedded 生命周期由容器独占，realm.h:1802）。
func (l *List) SetEmbedded(index uint64) (unsafe.Pointer, error) {
	emb := l.r.lib.realmListSetEmbedded(l.h, index)
	if emb == nil {
		return nil, fmt.Errorf("realm_list_set_embedded: %w", l.r.lib.lastError())
	}
	return emb, nil
}

// Erase 删除 index 处元素（对象列表只摘除链接不删目标对象；embedded
// 列表的元素随删除销毁，realm.h:1816）。
func (l *List) Erase(index uint64) error {
	if !l.r.lib.realmListErase(l.h, index) {
		return fmt.Errorf("realm_list_erase(%d): %w", index, l.r.lib.lastError())
	}
	return nil
}

// Clear 清空列表（对象列表仅摘除全部链接；embedded 元素随之销毁，
// realm.h:1824）。
func (l *List) Clear() error {
	if !l.r.lib.realmListClear(l.h) {
		return fmt.Errorf("realm_list_clear: %w", l.r.lib.lastError())
	}
	return nil
}

func (l *List) get(index uint64) (realmValue, error) {
	var v realmValue
	if !l.r.lib.realmListGet(l.h, index, &v) {
		return v, fmt.Errorf("realm_list_get(%d): %w", index, l.r.lib.lastError())
	}
	return v, nil
}

func (l *List) set(index uint64, v realmValue) error {
	if !l.r.lib.realmListSet(l.h, index, v) {
		return fmt.Errorf("realm_list_set(%d): %w", index, l.r.lib.lastError())
	}
	return nil
}

func (l *List) insertAtEnd(v realmValue) error {
	n, err := l.Size()
	if err != nil {
		return err
	}
	if !l.r.lib.realmListInsert(l.h, n, v) {
		return fmt.Errorf("realm_list_insert(%d): %w", n, l.r.lib.lastError())
	}
	return nil
}

// linkValueOf 把行句柄编成 RLM_TYPE_LINK 值（写入对象链接/对象列表用）。
// 经 realm_object_get_table / realm_object_get_key 两个标量返回组出，
// 与 realm_object_as_link 的 realm_link_t 字节布局一致（realm.h:188-191、
// 1494-1501）。
func (r *Realm) linkValueOf(obj unsafe.Pointer) realmValue {
	return linkValue(r.lib.realmObjectGetTable(obj), r.lib.realmObjectGetKey(obj))
}
