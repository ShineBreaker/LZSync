package realmc

// M1 动态读写会话与查询（design.md §3.1）。本文件原为契约桩，现按
// build/native/include/realm.h 原文填实。PoC 三纪律不变：DummyScheduler
// 注入（lib.go）、C 结构体字符串字段空串填充（session.go buildSchema）、
// ABI Sizeof/Offsetof 断言（poc_test.go 与 dynamic_test.go）。

import (
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"
)

// OpenDynamicReadWrite 以动态模式读写打开既有库：不传 schema、磁盘版本
// 钉死只读（S3 绝不迁移）。写会话打开失败本身即占用信号——realm 的
// .management 内部锁会拒绝并发打开（S5：这是最强的占用探测，进程名扫描
// 只负责提前给出友好提示）。
//
// SchemaMode 实证取值：RLM_SCHEMA_MODE_AUTOMATIC + 不传 schema = 纯动态
// 会话，realm 按磁盘现状打开、不发起任何迁移（无目标 schema 时 Automatic
// 的迁移比对不触发；TestDynamicReadWriteSession 验证写后磁盘版本不变）。
// 路径不存在时直接报错——动态打开不携带 schema，交给 realm 只会造出空库。
func (l *Library) OpenDynamicReadWrite(path string) (*Realm, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("realmc: 动态读写打开要求既有库，%s 不存在（动态打开不建库）: %w", path, err)
	}
	return l.open(path, nil, 0, SchemaModeAutomatic)
}

// GetBool 读 RLM_TYPE_BOOL 属性（DeletePending / Hidden / Protected，
// §4.1 投影跳过规则的判定输入）。可空列须先 IsNull 判空，NULL 时本方法报错。
func (r *Realm) GetBool(obj unsafe.Pointer, propKey int64) (bool, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return false, err
	}
	if v.typ != rlmTypeBool {
		return false, fmt.Errorf("属性类型非 BOOL（实际 type=%d）", v.typ)
	}
	return v.asBool(), nil
}

// SetBool 写 RLM_TYPE_BOOL 属性（S4 软删：DeletePending = true）。
func (r *Realm) SetBool(obj unsafe.Pointer, propKey int64, v bool) error {
	if !r.lib.realmSetValue(obj, propKey, boolValue(v), false) {
		return fmt.Errorf("realm_set_value(bool): %w", r.lib.lastError())
	}
	return nil
}

// GetTimestamp 读 RLM_TYPE_TIMESTAMP 属性（realm_timestamp_t 为 Unix 纪元
// {秒, 纳秒}，转 time.Time 返回；DateAdded / LastModified / Score.Date 等）。
// 可空列（DateSubmitted 等）须先 IsNull 判空，NULL 时本方法报错。
func (r *Realm) GetTimestamp(obj unsafe.Pointer, propKey int64) (time.Time, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return time.Time{}, err
	}
	if v.typ != rlmTypeTimestamp {
		return time.Time{}, fmt.Errorf("属性类型非 TIMESTAMP（实际 type=%d）", v.typ)
	}
	sec, nsec := v.asTimestamp()
	return time.Unix(sec, int64(nsec)), nil
}

// SetTimestamp 写 RLM_TYPE_TIMESTAMP 属性。
func (r *Realm) SetTimestamp(obj unsafe.Pointer, propKey int64, t time.Time) error {
	if !r.lib.realmSetValue(obj, propKey, timestampValue(t), false) {
		return fmt.Errorf("realm_set_value(timestamp): %w", r.lib.lastError())
	}
	return nil
}

// FindFirstByString 返回首个「string 属性 == value」的行句柄
// （realm_object_t*，调用方负责 ReleaseObject 释放），无匹配返回 (nil, nil)。
// 成绩挂接的目标库 BeatmapHash 查询（§5.2-3）依赖此原语。
//
// 实现取舍（按设计 §2 工作量修正的授权）：优先 RQL 查询语言
// （realm_query_parse "属性 == $0" + realm_query_find_first，走引擎索引与
// 查询计划）；RQL 解析失败（如属性名不能进查询串）才降级全表扫描逐行
// GetString 比对。两条路径都必须在已开启的事务内调用。
func (r *Realm) FindFirstByString(classKey uint32, propKey int64, value string) (unsafe.Pointer, error) {
	if obj, err := r.findFirstByStringRQL(classKey, propKey, value); err == nil {
		return obj, nil
	}
	// RQL 不可用（解析失败）→ 全表扫描兜底；扫描自身的错误如实上抛。
	return r.findFirstByStringScan(classKey, propKey, value)
}

// findFirstByStringRQL 走 RQL。返回 (nil, nil) 表示无匹配；解析失败返回
// 错误（调用方降级），其余错误如实上抛。
func (r *Realm) findFirstByStringRQL(classKey uint32, propKey int64, value string) (unsafe.Pointer, error) {
	var pi realmPropertyInfo
	if !r.lib.realmGetProperty(r.h, classKey, propKey, &pi) {
		return nil, fmt.Errorf("realm_get_property(%d): %w", propKey, r.lib.lastError())
	}
	b := cBytes(value)
	val := stringValue(unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b)-1))
	args := []realmQueryArg{{nbArgs: 1, isList: false, arg: &val}}
	queryStr := goString(pi.name) + " == $0"
	q := r.lib.realmQueryParse(r.h, classKey, queryStr, 1, &args[0])
	runtime.KeepAlive(b) // C 拷贝查询参数前字节必须存活
	if q == nil {
		return nil, fmt.Errorf("realm_query_parse(%q): %w", queryStr, r.lib.lastError())
	}
	defer r.lib.realmRelease(q)

	var out realmValue
	var found bool
	if !r.lib.realmQueryFindFirst(q, &out, &found) {
		return nil, fmt.Errorf("realm_query_find_first: %w", r.lib.lastError())
	}
	if !found {
		return nil, nil
	}
	if out.typ != rlmTypeLink {
		return nil, fmt.Errorf("realm_query_find_first 返回非 LINK（实际 type=%d）", out.typ)
	}
	table, key := out.asLink()
	obj := r.lib.realmGetObject(r.h, table, key)
	if obj == nil {
		return nil, fmt.Errorf("realm_get_object(class=%d key=%d): %w", table, key, r.lib.lastError())
	}
	return obj, nil
}

// findFirstByStringScan 全表扫描 + 逐行字符串比对（RQL 不可用的兜底路径）。
func (r *Realm) findFirstByStringScan(classKey uint32, propKey int64, value string) (unsafe.Pointer, error) {
	results := r.lib.realmObjectFindAll(r.h, classKey)
	if results == nil {
		return nil, fmt.Errorf("realm_object_find_all: %w", r.lib.lastError())
	}
	defer r.lib.realmRelease(results)
	var n uint64
	if !r.lib.realmResultsCount(results, &n) {
		return nil, fmt.Errorf("realm_results_count: %w", r.lib.lastError())
	}
	for i := uint64(0); i < n; i++ {
		obj := r.lib.realmResultsGetObject(results, i)
		if obj == nil {
			return nil, fmt.Errorf("realm_results_get_object(%d): %w", i, r.lib.lastError())
		}
		got, err := r.GetString(obj, propKey)
		if err != nil {
			r.lib.realmRelease(obj)
			return nil, err
		}
		if got == value {
			return obj, nil // 命中，句柄移交调用方
		}
		r.lib.realmRelease(obj)
	}
	return nil, nil
}
