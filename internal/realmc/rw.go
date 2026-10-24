package realmc

// 动态读写面补充（PoC 已有 GetInt64/SetInt64/GetString/SetString 之外的全部
// 属性类型），以及对象链接、embedded 对象与对象生命周期原语。
// 可空列纪律：typed getter 对 NULL 一律报错，调用方先 IsNull 判空（NULL 的
// 语义归调用方解释，绑定层不静默折算零值——lazer 的 long? 0 与 null 语义不同）。

import (
	"fmt"
	"runtime"
	"unsafe"
)

// IsNull 探测属性当前是否为 NULL（可空列的判空入口）。
func (r *Realm) IsNull(obj unsafe.Pointer, propKey int64) (bool, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return false, err
	}
	return v.typ == rlmTypeNull, nil
}

// SetNull 清空可空属性（对象链接的解绑：Score.BeatmapInfo 置 null 而
// BeatmapHash 保留，§5.4 版本更新处置第 3 步）。非可空列会报错。
func (r *Realm) SetNull(obj unsafe.Pointer, propKey int64) error {
	if !r.lib.realmSetValue(obj, propKey, nullValue(), false) {
		return fmt.Errorf("realm_set_value(null): %w", r.lib.lastError())
	}
	return nil
}

// GetFloat32 读 RLM_TYPE_FLOAT 属性（BeatmapDifficulty.CircleSize 等 float 列）。
func (r *Realm) GetFloat32(obj unsafe.Pointer, propKey int64) (float32, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return 0, err
	}
	if v.typ != rlmTypeFloat {
		return 0, fmt.Errorf("属性类型非 FLOAT（实际 type=%d）", v.typ)
	}
	return v.asFloat32(), nil
}

// SetFloat32 写 RLM_TYPE_FLOAT 属性。
func (r *Realm) SetFloat32(obj unsafe.Pointer, propKey int64, f float32) error {
	if !r.lib.realmSetValue(obj, propKey, float32Value(f), false) {
		return fmt.Errorf("realm_set_value(float): %w", r.lib.lastError())
	}
	return nil
}

// GetFloat64 读 RLM_TYPE_DOUBLE 属性（StarRating / Accuracy / PP 等）。
func (r *Realm) GetFloat64(obj unsafe.Pointer, propKey int64) (float64, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return 0, err
	}
	if v.typ != rlmTypeDouble {
		return 0, fmt.Errorf("属性类型非 DOUBLE（实际 type=%d）", v.typ)
	}
	return v.asFloat64(), nil
}

// SetFloat64 写 RLM_TYPE_DOUBLE 属性。
func (r *Realm) SetFloat64(obj unsafe.Pointer, propKey int64, f float64) error {
	if !r.lib.realmSetValue(obj, propKey, float64Value(f), false) {
		return fmt.Errorf("realm_set_value(double): %w", r.lib.lastError())
	}
	return nil
}

// GetGuid 读 RLM_TYPE_UUID 属性（lazer 各表的主键 ID）。
func (r *Realm) GetGuid(obj unsafe.Pointer, propKey int64) ([16]byte, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return [16]byte{}, err
	}
	if v.typ != rlmTypeUUID {
		return [16]byte{}, fmt.Errorf("属性类型非 UUID（实际 type=%d）", v.typ)
	}
	return v.asUUID(), nil
}

// SetGuid 写 RLM_TYPE_UUID 属性。
func (r *Realm) SetGuid(obj unsafe.Pointer, propKey int64, id [16]byte) error {
	if !r.lib.realmSetValue(obj, propKey, uuidValue(id), false) {
		return fmt.Errorf("realm_set_value(uuid): %w", r.lib.lastError())
	}
	return nil
}

// GetBinary 读 RLM_TYPE_BINARY 属性。返回独立副本——realm 内部内存仅
// 在下一次 C API 调用前有效（realm.h:383-393 同款约束）。
func (r *Realm) GetBinary(obj unsafe.Pointer, propKey int64) ([]byte, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return nil, err
	}
	if v.typ != rlmTypeBinary {
		return nil, fmt.Errorf("属性类型非 BINARY（实际 type=%d）", v.typ)
	}
	data, size := v.asBinary()
	if data == nil || size == 0 {
		return []byte{}, nil
	}
	src := unsafe.Slice((*byte)(data), size)
	out := make([]byte, size)
	copy(out, src)
	return out, nil
}

// SetBinary 写 RLM_TYPE_BINARY 属性。
func (r *Realm) SetBinary(obj unsafe.Pointer, propKey int64, data []byte) error {
	// 空数据也指向有效字节：realm_string 的 null 态（data==NULL）与空串态
	// 语义不同（realm.h:160-168），非可空 binary 列写 null 会报错。
	buf := data
	if len(buf) == 0 {
		buf = []byte{0}
	}
	v := binaryValue(unsafe.Pointer(unsafe.SliceData(buf)), uint64(len(data)))
	ok := r.lib.realmSetValue(obj, propKey, v, false)
	runtime.KeepAlive(buf)
	if !ok {
		return fmt.Errorf("realm_set_value(binary): %w", r.lib.lastError())
	}
	return nil
}

// GetLinkedObject 取对象链接属性的目标行句柄（含 embedded 单值属性——
// Beatmap.Difficulty、Score.User）。空链接返回 (nil, nil)；句柄由调用方
// ReleaseObject 释放。
func (r *Realm) GetLinkedObject(obj unsafe.Pointer, propKey int64) (unsafe.Pointer, error) {
	// 指针返回 NULL 有「空链接」与「异常」双解：先清线程错误再查，
	// 防 last error 残留把成功调用误报成异常（realm.h:383-393）。
	r.lib.clearLastError()
	target := r.lib.realmGetLinkedObject(obj, propKey)
	if target == nil {
		var e realmError
		if r.lib.realmGetLastError(&e) {
			return nil, r.lib.lastError()
		}
		return nil, nil
	}
	return target, nil
}

// SetLinkedObject 写对象链接（BeatmapInfo→BeatmapSet、Score→BeatmapInfo）。
// target 必须是目标 class 的行句柄；realm.h 未提供独立 set_link 入口，
// 链接统一走 realm_set_value + RLM_TYPE_LINK（与 realm_object_as_link 的
// 字节布局一致，经 realm_object_get_table/get_key 标量返回组出）。
func (r *Realm) SetLinkedObject(obj unsafe.Pointer, propKey int64, target unsafe.Pointer) error {
	v := linkValue(r.lib.realmObjectGetTable(target), r.lib.realmObjectGetKey(target))
	if !r.lib.realmSetValue(obj, propKey, v, false) {
		return fmt.Errorf("realm_set_value(link): %w", r.lib.lastError())
	}
	return nil
}

// SetEmbedded 在单值 embedded 属性上创建并挂接新 embedded 对象
// （realm_set_embedded，realm.h:1591），返回其行句柄。实证语义：属性已有
// embedded 对象时是**替换**——旧 embedded 对象随之失效销毁（旧句柄再操作
// 报 errno=2010 invalidated），与 realm_list_set_embedded 一致。
func (r *Realm) SetEmbedded(obj unsafe.Pointer, propKey int64) (unsafe.Pointer, error) {
	emb := r.lib.realmSetEmbedded(obj, propKey)
	if emb == nil {
		return nil, fmt.Errorf("realm_set_embedded: %w", r.lib.lastError())
	}
	return emb, nil
}

// CreateObjectWithGuidPK 在写事务内以 Guid 主键建新行（BeatmapSet/Beatmap/
// Score/Skin/Collection/ModPreset 全部走此入口）。同主键已存在时报错。
func (r *Realm) CreateObjectWithGuidPK(classKey uint32, id [16]byte) (unsafe.Pointer, error) {
	obj := r.lib.realmObjectCreateWithPK(r.h, classKey, uuidValue(id))
	if obj == nil {
		return nil, fmt.Errorf("realm_object_create_with_primary_key(guid): %w", r.lib.lastError())
	}
	return obj, nil
}

// GetOrCreateObjectWithGuidPK 按 Guid 主键取行，不存在则创建（RealmFile
// 的 Find ?? Create 语义由调用方以 String 版本组合，Guid 版本供幂等重跑）。
// 返回行句柄与是否新建。
func (r *Realm) GetOrCreateObjectWithGuidPK(classKey uint32, id [16]byte) (obj unsafe.Pointer, created bool, err error) {
	obj = r.lib.realmObjectGetOrCreateWithPK(r.h, classKey, uuidValue(id), &created)
	if obj == nil {
		return nil, false, fmt.Errorf("realm_object_get_or_create_with_primary_key(guid): %w", r.lib.lastError())
	}
	return obj, created, nil
}

// CreateObjectWithStringPK 以 string 主键建新行（RealmFile.Hash）。
// 同主键已存在时报错。
func (r *Realm) CreateObjectWithStringPK(classKey uint32, pk string) (unsafe.Pointer, error) {
	b := cBytes(pk)
	obj := r.lib.realmObjectCreateWithPK(r.h, classKey, stringValue(unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b)-1)))
	runtime.KeepAlive(b)
	if obj == nil {
		return nil, fmt.Errorf("realm_object_create_with_primary_key(string): %w", r.lib.lastError())
	}
	return obj, nil
}

// GetOrCreateObjectWithStringPK 按 string 主键取行，不存在则创建
// （§5.3 的 RealmFile `Find(File, hash) ?? Create` 原语）。
func (r *Realm) GetOrCreateObjectWithStringPK(classKey uint32, pk string) (obj unsafe.Pointer, created bool, err error) {
	b := cBytes(pk)
	obj = r.lib.realmObjectGetOrCreateWithPK(r.h, classKey, stringValue(unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b)-1)), &created)
	runtime.KeepAlive(b)
	if obj == nil {
		return nil, false, fmt.Errorf("realm_object_get_or_create_with_primary_key(string): %w", r.lib.lastError())
	}
	return obj, created, nil
}

// DeleteObject 物理删除行（realm_object_delete，realm.h:1454）。S4 纪律：
// BeatmapSet/Score/Skin/ModPreset 一律走 SetBool(DeletePending=true) 软删，
// 物理删除仅收藏夹等无软删语义的表使用。不释放传入的 realm_object_t*。
func (r *Realm) DeleteObject(obj unsafe.Pointer) error {
	if !r.lib.realmObjectDelete(obj) {
		return fmt.Errorf("realm_object_delete: %w", r.lib.lastError())
	}
	return nil
}
