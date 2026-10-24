package realmc

// M1 缺口契约（design.md §3.1：动态读写会话与查询）。
// 本文件只补 PoC 未覆盖的公共签名面；实现统一返回 ErrNotImplemented，
// 由后续 worker 按 build/native/include/realm.h 填实。现有 PoC 代码不动。

import (
	"errors"
	"time"
	"unsafe"
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("realmc: 契约桩未实现")

// OpenDynamicReadWrite 以动态模式读写打开既有库：不传 schema、磁盘版本
// 钉死只读（S3 绝不迁移）。写会话打开失败本身即占用信号——realm 的
// .management 内部锁会拒绝并发打开（S5：这是最强的占用探测，进程名扫描
// 只负责提前给出友好提示）。确切 SchemaMode 取值施工时按 realm.h 实证后固化。
func (l *Library) OpenDynamicReadWrite(path string) (*Realm, error) {
	return nil, ErrNotImplemented
}

// GetBool 读 RLM_TYPE_BOOL 属性（DeletePending / Hidden / Protected，
// §4.1 投影跳过规则的判定输入）。
func (r *Realm) GetBool(obj unsafe.Pointer, propKey int64) (bool, error) {
	return false, ErrNotImplemented
}

// SetBool 写 RLM_TYPE_BOOL 属性（S4 软删：DeletePending = true）。
func (r *Realm) SetBool(obj unsafe.Pointer, propKey int64, v bool) error {
	return ErrNotImplemented
}

// GetTimestamp 读 RLM_TYPE_TIMESTAMP 属性（realm_timestamp_t 为 Unix 纪元
// 纳秒，转 time.Time 返回；DateAdded / LastModified / Score.Date 等）。
func (r *Realm) GetTimestamp(obj unsafe.Pointer, propKey int64) (time.Time, error) {
	return time.Time{}, ErrNotImplemented
}

// SetTimestamp 写 RLM_TYPE_TIMESTAMP 属性。
func (r *Realm) SetTimestamp(obj unsafe.Pointer, propKey int64, t time.Time) error {
	return ErrNotImplemented
}

// FindFirstByString 返回首个「string 属性 == value」的行句柄
// （realm_object_t*，调用方负责 ReleaseObject 释放），无匹配返回 (nil, nil)。
// 成绩挂接的目标库 BeatmapHash 查询（§5.2-3）依赖此原语；实现可用 C API
// 查询语言或全表扫描，由施工按实测性能取舍。
func (r *Realm) FindFirstByString(classKey uint32, propKey int64, value string) (unsafe.Pointer, error) {
	return nil, ErrNotImplemented
}
