package realmc

import (
	"fmt"
	"runtime"
	"unsafe"
)

// Realm 是一次 realm_open 的动态会话（realm_t*）。句柄纪律（design §2）：
// Close = realm_close + realm_release；所有中间句柄（schema/results/object）
// 由各方法用后即 release。
type Realm struct {
	lib *Library
	h   unsafe.Pointer // realm_t*，Close 后置 nil
}

// ClassInfo 是 realm_class_info_t 的 Go 投影（字符串已拷贝，脱离 C 生命周期）。
type ClassInfo struct {
	Name       string
	PrimaryKey string
	Key        uint32 // realm_class_key_t，供后续按 key 访问
}

// PropertyInfo 是 realm_property_info_t 的 Go 投影。
type PropertyInfo struct {
	Name string
	Type PropType
	Key  int64 // realm_property_key_t
}

// ClassDef / PropertyDef / Schema 是 realm_schema_new 的构造面（realm.h:1121）。
type PropertyDef struct {
	Name         string
	Type         PropType
	IsPrimaryKey bool
}

type ClassDef struct {
	Name       string
	PrimaryKey string // 主键属性名，空串表示无主键类
	Properties []PropertyDef
}

type Schema struct {
	Classes []ClassDef
}

func (l *Library) open(path string, schema unsafe.Pointer, version uint64, mode SchemaMode) (*Realm, error) {
	cfg := l.realmConfigNew()
	if cfg == nil {
		return nil, fmt.Errorf("realm_open(%s): realm_config_new: %w", path, l.lastError())
	}
	defer l.realmRelease(cfg)
	l.realmConfigSetPath(cfg, path)
	l.realmConfigSetSchemaMode(cfg, mode)
	if schema != nil {
		// realm.h:641：config 不接管 schema 所有权，open 后仍需我们 release。
		l.realmConfigSetSchema(cfg, schema)
		l.realmConfigSetSchemaVersion(cfg, version)
	}
	// 必须显式注入 scheduler：本构建无平台默认实现（见 lib.go cbAlwaysOnThread 注释）。
	sched, err := l.newDummyScheduler()
	if err != nil {
		return nil, fmt.Errorf("realm_open(%s): %w", path, err)
	}
	defer l.realmRelease(sched)
	l.realmConfigSetScheduler(cfg, sched)
	h := l.realmOpen(cfg)
	if h == nil {
		return nil, fmt.Errorf("realm_open(%s): %w", path, l.lastError())
	}
	return &Realm{lib: l, h: h}, nil
}

// OpenDynamicReadOnly 动态只读打开（不传 schema、RLM_SCHEMA_MODE_READ_ONLY）。
// 打开必在库旁创建 .lock/.management 伴生文件（design §2 事实约束），
// 故目标路径必须位于可写目录。
func (l *Library) OpenDynamicReadOnly(path string) (*Realm, error) {
	return l.open(path, nil, 0, SchemaModeReadOnly)
}

// OpenWithSchema 显式 schema 打开：文件不存在则按 schema 新建空库，
// 已存在则按版本比对（RLM_SCHEMA_MODE_AUTOMATIC）。仅用于测试体系建库
// （design §8.2），产品路径一律走动态打开（S3）。
func (l *Library) OpenWithSchema(path string, s *Schema, version uint64) (*Realm, error) {
	schemaH, err := l.buildSchema(s)
	if err != nil {
		return nil, fmt.Errorf("realm_schema_new(%s): %w", path, err)
	}
	defer l.realmSchemaRelease(schemaH)
	return l.open(path, schemaH, version, SchemaModeAutomatic)
}

// buildSchema 把 Go 侧定义编译成 realm_schema_t（realm.h:1121）。
// 结构体内的字符串指针指向 Go 分配的 NUL 结尾字节，靠 KeepAlive
// 存活到 realm_schema_new 返回（C 侧构造时拷贝进 std::string）。
//
// 实测约束（realm-core conversion.hpp:398 from_capi / schema.cpp:14-16）：
// realm_class_info_t.primary_key 与 realm_property_info_t 的 public_name/
// link_target/link_origin_property_name 都是 std::string 赋值，NULL 会
// strlen(NULL) 直接 SIGSEGV——必须全部指向有效 NUL 串（空串 "" 即可，
// 空串在 realm-core 语义里 = 无主键/无链接目标）。
func (l *Library) buildSchema(s *Schema) (unsafe.Pointer, error) {
	n := len(s.Classes)
	emptyB := cBytes("")
	classInfos := make([]realmClassInfo, n)
	propArrays := make([][]realmPropertyInfo, n)
	propPtrs := make([]*realmPropertyInfo, n)
	var keep [][]byte
	keep = append(keep, emptyB)
	emptyPtr := unsafe.Pointer(unsafe.SliceData(emptyB))
	for i, c := range s.Classes {
		nameB := cBytes(c.Name)
		keep = append(keep, nameB)
		classInfos[i].name = unsafe.Pointer(unsafe.SliceData(nameB))
		if c.PrimaryKey != "" {
			pkB := cBytes(c.PrimaryKey)
			keep = append(keep, pkB)
			classInfos[i].primaryKey = unsafe.Pointer(unsafe.SliceData(pkB))
		} else {
			classInfos[i].primaryKey = emptyPtr
		}
		classInfos[i].numProperties = uint64(len(c.Properties))
		classInfos[i].flags = classFlagNormal

		props := make([]realmPropertyInfo, len(c.Properties))
		for j, p := range c.Properties {
			pnB := cBytes(p.Name)
			keep = append(keep, pnB)
			props[j].name = unsafe.Pointer(unsafe.SliceData(pnB))
			props[j].publicName = emptyPtr
			props[j].linkTarget = emptyPtr
			props[j].linkOriginPropName = emptyPtr
			props[j].typ = uint32(p.Type)
			props[j].collectionType = collectionNone
			flags := int32(propFlagNormal)
			if p.IsPrimaryKey {
				flags |= propFlagPrimaryKey
			}
			props[j].flags = flags
		}
		propArrays[i] = props
		propPtrs[i] = &props[0]
	}
	h := l.realmSchemaNew(&classInfos[0], uint64(n), &propPtrs[0])
	runtime.KeepAlive(keep)
	runtime.KeepAlive(propArrays)
	runtime.KeepAlive(propPtrs)
	if h == nil {
		return nil, l.lastError()
	}
	if !l.realmSchemaValidate(h, 0 /* RLM_SCHEMA_VALIDATION_BASIC, realm.h:150 */) {
		l.realmSchemaRelease(h)
		return nil, fmt.Errorf("schema 校验失败: %w", l.lastError())
	}
	return h, nil
}

func cBytes(s string) []byte {
	return append([]byte(s), 0)
}

// Close 安全关闭：realm_close（realm.h:990）后 realm_release。
// 幂等；close 失败也继续 release 并返回错误。
func (r *Realm) Close() error {
	if r.h == nil {
		return nil
	}
	var err error
	if !r.lib.realmClose(r.h) {
		err = fmt.Errorf("realm_close: %w", r.lib.lastError())
	}
	r.lib.realmRelease(r.h)
	r.h = nil
	return err
}

func (r *Realm) BeginRead() error {
	if !r.lib.realmBeginRead(r.h) {
		return fmt.Errorf("realm_begin_read: %w", r.lib.lastError())
	}
	return nil
}

func (r *Realm) BeginWrite() error {
	if !r.lib.realmBeginWrite(r.h) {
		return fmt.Errorf("realm_begin_write: %w", r.lib.lastError())
	}
	return nil
}

func (r *Realm) Commit() error {
	if !r.lib.realmCommit(r.h) {
		return fmt.Errorf("realm_commit: %w", r.lib.lastError())
	}
	return nil
}

func (r *Realm) Rollback() error {
	if !r.lib.realmRollback(r.h) {
		return fmt.Errorf("realm_rollback: %w", r.lib.lastError())
	}
	return nil
}

// SchemaVersion 返回磁盘 schema 版本（realm.h:1137）。
func (r *Realm) SchemaVersion() uint64 {
	return r.lib.realmGetSchemaVersion(r.h)
}

// Classes 枚举全部 class（名称 + 主键 + key）。
// realm_get_class_keys 两段式取容量再填数（realm.h:1239）。
func (r *Realm) Classes() ([]ClassInfo, error) {
	var n uint64
	if !r.lib.realmGetClassKeys(r.h, nil, 0, &n) {
		return nil, fmt.Errorf("realm_get_class_keys(探测): %w", r.lib.lastError())
	}
	keys := make([]uint32, n)
	if !r.lib.realmGetClassKeys(r.h, &keys[0], n, &n) {
		return nil, fmt.Errorf("realm_get_class_keys: %w", r.lib.lastError())
	}
	out := make([]ClassInfo, 0, n)
	for _, k := range keys {
		var ci realmClassInfo
		if !r.lib.realmGetClass(r.h, k, &ci) {
			return nil, fmt.Errorf("realm_get_class(%d): %w", k, r.lib.lastError())
		}
		out = append(out, ClassInfo{
			Name:       goString(ci.name),
			PrimaryKey: goString(ci.primaryKey),
			Key:        ci.key,
		})
	}
	return out, nil
}

// FindClass 按名找 class，返回其 key。
func (r *Realm) FindClass(name string) (uint32, error) {
	var found bool
	var ci realmClassInfo
	if !r.lib.realmFindClass(r.h, name, &found, &ci) {
		return 0, fmt.Errorf("realm_find_class(%s): %w", name, r.lib.lastError())
	}
	if !found {
		return 0, fmt.Errorf("realm_find_class(%s): class 不存在", name)
	}
	return ci.key, nil
}

// Properties 枚举某 class 的全部属性（realm.h:1281，两段式）。
func (r *Realm) Properties(classKey uint32) ([]PropertyInfo, error) {
	var n uint64
	if !r.lib.realmGetClassProperties(r.h, classKey, nil, 0, &n) {
		return nil, fmt.Errorf("realm_get_class_properties(探测): %w", r.lib.lastError())
	}
	props := make([]realmPropertyInfo, n)
	if !r.lib.realmGetClassProperties(r.h, classKey, &props[0], n, &n) {
		return nil, fmt.Errorf("realm_get_class_properties: %w", r.lib.lastError())
	}
	out := make([]PropertyInfo, 0, n)
	for _, p := range props {
		out = append(out, PropertyInfo{Name: goString(p.name), Type: PropType(p.typ), Key: p.key})
	}
	return out, nil
}

// FindProperty 按名找属性，返回其 key。
func (r *Realm) FindProperty(classKey uint32, name string) (int64, error) {
	var found bool
	var pi realmPropertyInfo
	if !r.lib.realmFindProperty(r.h, classKey, name, &found, &pi) {
		return 0, fmt.Errorf("realm_find_property(%s): %w", name, r.lib.lastError())
	}
	if !found {
		return 0, fmt.Errorf("realm_find_property(%s): 属性不存在", name)
	}
	return pi.key, nil
}

// ObjectCount 返回 class 行数（realm.h:1375）。
func (r *Realm) ObjectCount(classKey uint32) (uint64, error) {
	var n uint64
	if !r.lib.realmGetNumObjects(r.h, classKey, &n) {
		return 0, fmt.Errorf("realm_get_num_objects: %w", r.lib.lastError())
	}
	return n, nil
}

// FirstObject 取 class 首行（realm_object_find_all + realm_results_get_object(0)）。
// 返回的 realm_object_t* 由调用方负责用 ReleaseObject 释放。
func (r *Realm) FirstObject(classKey uint32) (unsafe.Pointer, error) {
	results := r.lib.realmObjectFindAll(r.h, classKey)
	if results == nil {
		return nil, fmt.Errorf("realm_object_find_all: %w", r.lib.lastError())
	}
	defer r.lib.realmRelease(results)
	var n uint64
	if !r.lib.realmResultsCount(results, &n) {
		return nil, fmt.Errorf("realm_results_count: %w", r.lib.lastError())
	}
	if n == 0 {
		return nil, fmt.Errorf("realm_object_find_all: class 无行")
	}
	obj := r.lib.realmResultsGetObject(results, 0)
	if obj == nil {
		return nil, fmt.Errorf("realm_results_get_object(0): %w", r.lib.lastError())
	}
	return obj, nil
}

// ReleaseObject 释放 realm_object_t* 句柄。
func (r *Realm) ReleaseObject(obj unsafe.Pointer) {
	r.lib.realmRelease(obj)
}

// CreateObject 在写事务内建新行（无主键类，realm.h:1428）。
func (r *Realm) CreateObject(classKey uint32) (unsafe.Pointer, error) {
	obj := r.lib.realmObjectCreate(r.h, classKey)
	if obj == nil {
		return nil, fmt.Errorf("realm_object_create: %w", r.lib.lastError())
	}
	return obj, nil
}

// GetInt64 读 RLM_TYPE_INT 属性。
func (r *Realm) GetInt64(obj unsafe.Pointer, propKey int64) (int64, error) {
	v, err := r.getValue(obj, propKey)
	if err != nil {
		return 0, err
	}
	if v.typ != rlmTypeInt {
		return 0, fmt.Errorf("属性类型非 INT（实际 type=%d）", v.typ)
	}
	return v.asInt64(), nil
}

// GetString 读 RLM_TYPE_STRING 属性（立即拷贝出 realm 内部内存）。
func (r *Realm) GetString(obj unsafe.Pointer, propKey int64) (string, error) {
	v, err := r.getValue(obj, propKey)
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
		return "", fmt.Errorf("属性类型非 STRING（实际 type=%d）", v.typ)
	}
}

func (r *Realm) getValue(obj unsafe.Pointer, propKey int64) (realmValue, error) {
	var v realmValue
	if !r.lib.realmGetValue(obj, propKey, &v) {
		return v, fmt.Errorf("realm_get_value: %w", r.lib.lastError())
	}
	return v, nil
}

// SetString 写 RLM_TYPE_STRING 属性（realm_set_value，value 按值传）。
func (r *Realm) SetString(obj unsafe.Pointer, propKey int64, s string) error {
	b := cBytes(s)
	v := stringValue(unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b)-1))
	ok := r.lib.realmSetValue(obj, propKey, v, false)
	runtime.KeepAlive(b) // C 拷贝完成前字节必须存活
	if !ok {
		return fmt.Errorf("realm_set_value(string): %w", r.lib.lastError())
	}
	return nil
}

// SetInt64 写 RLM_TYPE_INT 属性。
func (r *Realm) SetInt64(obj unsafe.Pointer, propKey int64, n int64) error {
	if !r.lib.realmSetValue(obj, propKey, int64Value(n), false) {
		return fmt.Errorf("realm_set_value(int): %w", r.lib.lastError())
	}
	return nil
}
