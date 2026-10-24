// Package realmc 是 realm-core C API 的 purego 绑定子集（design.md §3.1：
// 纯绑定层，不 import 任何领域包）。所有结构体按 build/native/include/realm.h
// 的 C ABI 镜像：嵌入 structs.HostLayout 由编译器按主机 ABI 排布字段，
// 但尾部 padding（C 编译器按 union/成员最大对齐补齐的部分）必须显式补
// 字段——purego 不校验结构体布局（purego func.go 文档：padding 是调用者责任）。
package realmc

import (
	"encoding/binary"
	"structs"
	"unsafe"
)

// realm_value_type_e（realm.h:132-147）
const (
	rlmTypeNull       = 0
	rlmTypeInt        = 1
	rlmTypeBool       = 2
	rlmTypeString     = 3
	rlmTypeBinary     = 4
	rlmTypeTimestamp  = 5
	rlmTypeFloat      = 6
	rlmTypeDouble     = 7
	rlmTypeDecimal128 = 8
	rlmTypeObjectID   = 9
	rlmTypeLink       = 10
	rlmTypeUUID       = 11
)

// realm_schema_mode_e（realm.h:102-111）
type SchemaMode uint32

const (
	SchemaModeAutomatic          SchemaMode = 0
	SchemaModeImmutable          SchemaMode = 1
	SchemaModeReadOnly           SchemaMode = 2
	SchemaModeSoftResetFile      SchemaMode = 3
	SchemaModeHardResetFile      SchemaMode = 4
	SchemaModeAdditiveDiscovered SchemaMode = 5
	SchemaModeAdditiveExplicit   SchemaMode = 6
	SchemaModeManual             SchemaMode = 7
)

// realm_class_flags_e（realm.h:305-310）
const classFlagNormal = 0

// realm_property_flags_e（realm.h:312-318）
const (
	propFlagNormal     = 0
	propFlagNullable   = 1
	propFlagPrimaryKey = 2
)

// realm_property_type_e（realm.h:260-275）
type PropType uint32

const (
	PropTypeInt           PropType = 0
	PropTypeBool          PropType = 1
	PropTypeString        PropType = 2
	PropTypeTimestamp     PropType = 8
	PropTypeObject        PropType = 12
	PropTypeLinkingObject PropType = 14
)

// realm_collection_type_e（realm.h:277-282）
const collectionNone = 0

// realmValue 镜像 realm_value_t（realm.h:201-218）：
//
//	union { ...; char data[16]; } values;  // offset 0..16
//	realm_value_type_e type;               // offset 16..20
//	                                       // pad 20..24（union 对齐 8）
//
// 总大小 24 字节。union 以原始字节承载，具体成员经 little-endian 编解码访问
// （目标平台 linux-amd64 为小端；structs.HostLayout 本就只面向本机 ABI）。
type realmValue struct {
	_   structs.HostLayout
	u   [16]byte
	typ uint32
	_   uint32 // 显式尾部 padding：C 侧 union 对齐 8 → sizeof(realm_value_t)=24
}

func int64Value(n int64) realmValue {
	var v realmValue
	binary.LittleEndian.PutUint64(v.u[:8], uint64(n))
	v.typ = rlmTypeInt
	return v
}

// stringValue 构造 RLM_TYPE_STRING。data 指向的字节必须在 C 调用返回前保持存活。
// union 槽位经指针视图结构体 + 字节拷贝填充（纯 Go 布局下 ptr/uint64 落在
// 0/8 偏移，与本机 C union 槽位一致，且无 uintptr 转换）。
func stringValue(data unsafe.Pointer, size uint64) realmValue {
	view := struct {
		data unsafe.Pointer
		size uint64
	}{data: data, size: size}
	var v realmValue
	copy(v.u[:], unsafe.Slice((*byte)(unsafe.Pointer(&view)), 16))
	v.typ = rlmTypeString
	return v
}

// asInt64 取 RLM_TYPE_INT 的值（前置检查由调用方做）。
func (v realmValue) asInt64() int64 {
	return int64(binary.LittleEndian.Uint64(v.u[:8]))
}

// asString 取 RLM_TYPE_STRING 的 (data 指针, 长度)。指针指向 realm 内部
// 内存，仅在下一次 C API 调用前有效，须立即拷贝。
func (v realmValue) asString() (unsafe.Pointer, uint64) {
	var view struct {
		data unsafe.Pointer
		size uint64
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&view)), 16), v.u[:])
	return view.data, view.size
}

// realmClassInfo 镜像 realm_class_info_t（realm.h:296-303）：
//
//	const char* name;                    0..8
//	const char* primary_key;             8..16
//	size_t num_properties;               16..24
//	size_t num_computed_properties;      24..32
//	realm_class_key_t key;               32..36
//	int flags;                           36..40
type realmClassInfo struct {
	_ structs.HostLayout
	// name、primaryKey 指向 Go 侧分配的 NUL 结尾字节；构造 schema 时须
	// KeepAlive 到 realm_schema_new 返回。
	name                  unsafe.Pointer
	primaryKey            unsafe.Pointer
	numProperties         uint64
	numComputedProperties uint64
	key                   uint32
	flags                 int32
}

// realmPropertyInfo 镜像 realm_property_info_t（realm.h:284-294）：
//
//	const char* name;                    0..8
//	const char* public_name;             8..16
//	realm_property_type_e type;          16..20
//	realm_collection_type_e coll;        20..24
//	const char* link_target;             24..32
//	const char* link_origin_prop_name;   32..40
//	realm_property_key_t key;            40..48
//	int flags;                           48..52
//	                                     pad 52..56
type realmPropertyInfo struct {
	_                  structs.HostLayout
	name               unsafe.Pointer
	publicName         unsafe.Pointer
	typ                uint32
	collectionType     uint32
	linkTarget         unsafe.Pointer
	linkOriginPropName unsafe.Pointer
	key                int64
	flags              int32
	_                  uint32 // 显式尾部 padding → sizeof=56
}

// realmError 镜像 realm_error_t（realm.h:235-243）：
//
//	realm_errno_e error;          0..4
//	realm_error_categories cats;  4..8
//	const char* message;          8..16
//	void* user_code_error;        16..24
//	const char* path;             24..32
type realmError struct {
	_           structs.HostLayout
	errNo       int32
	categories  uint32
	message     unsafe.Pointer
	userCodeErr unsafe.Pointer
	path        unsafe.Pointer
}
