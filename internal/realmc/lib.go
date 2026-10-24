package realmc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Library 持有一次 dlopen 的 realm-core 共享库句柄与全部符号绑定。
// 符号缺失会让 RegisterLibFunc panic——这是绑定层的 FAIL 信号，不吞掉。
type Library struct {
	handle uintptr

	// 全部签名按 build/native/include/realm.h 原文镜像。
	// bool 返回值 = "true if no exception occurred"；错误细节走 realm_get_last_error。
	realmGetLibraryVersion func() string // realm.h:366，返回 char*，purego 自动拷贝为 Go string

	realmGetLastError func(*realmError) bool // realm.h:401
	realmRelease      func(unsafe.Pointer)   // realm.h:472（接受 NULL）

	realmConfigNew              func() unsafe.Pointer                // realm.h:586
	realmConfigSetPath          func(unsafe.Pointer, string)         // realm.h:600，string 自动转 char*
	realmConfigSetSchema        func(unsafe.Pointer, unsafe.Pointer) // realm.h:641
	realmConfigSetSchemaVersion func(unsafe.Pointer, uint64)         // realm.h:655
	realmConfigSetSchemaMode    func(unsafe.Pointer, SchemaMode)     // realm.h:669
	realmConfigSetScheduler     func(unsafe.Pointer, unsafe.Pointer) // realm.h:770

	// realm_scheduler_new（realm.h:838）。函数指针一律 uintptr 传递；
	// 仅 is_on_thread 需要真回调（恒真），其余传 0（NULL）。
	realmSchedulerNew func(userdata, freeFunc, notify, isOnThread, isSameAs, canDeliver uintptr) unsafe.Pointer

	realmSchemaNew      func(*realmClassInfo, uint64, **realmPropertyInfo) unsafe.Pointer // realm.h:1121
	realmSchemaRelease  func(unsafe.Pointer)
	realmSchemaValidate func(unsafe.Pointer, uint64) bool // realm.h:1218

	realmOpen  func(unsafe.Pointer) unsafe.Pointer // realm.h:874
	realmClose func(unsafe.Pointer) bool           // realm.h:990

	realmBeginRead  func(unsafe.Pointer) bool // realm.h:1004
	realmBeginWrite func(unsafe.Pointer) bool // realm.h:1011
	realmCommit     func(unsafe.Pointer) bool // realm.h:1025
	realmRollback   func(unsafe.Pointer) bool // realm.h:1032

	realmGetSchemaVersion   func(unsafe.Pointer) uint64                                            // realm.h:1137
	realmGetNumClasses      func(unsafe.Pointer) uint64                                            // realm.h:1225
	realmGetClassKeys       func(unsafe.Pointer, *uint32, uint64, *uint64) bool                    // realm.h:1239
	realmFindClass          func(unsafe.Pointer, string, *bool, *realmClassInfo) bool              // realm.h:1252
	realmGetClass           func(unsafe.Pointer, uint32, *realmClassInfo) bool                     // realm.h:1265
	realmGetClassProperties func(unsafe.Pointer, uint32, *realmPropertyInfo, uint64, *uint64) bool // realm.h:1281
	realmFindProperty       func(unsafe.Pointer, uint32, string, *bool, *realmPropertyInfo) bool   // realm.h:1336
	realmGetNumObjects      func(unsafe.Pointer, uint32, *uint64) bool                             // realm.h:1375

	realmObjectFindAll    func(unsafe.Pointer, uint32) unsafe.Pointer // realm.h:1421
	realmObjectCreate     func(unsafe.Pointer, uint32) unsafe.Pointer // realm.h:1428
	realmResultsCount     func(unsafe.Pointer, *uint64) bool          // realm.h:2525
	realmResultsGetObject func(unsafe.Pointer, uint64) unsafe.Pointer // realm.h:2616

	realmGetValue  func(unsafe.Pointer, int64, *realmValue) bool                // realm.h:1541
	realmSetValue  func(unsafe.Pointer, int64, realmValue, bool) bool           // realm.h:1576，value 按值传（24B MEMORY 类）
	realmSetValues func(unsafe.Pointer, uint64, *int64, *realmValue, bool) bool // realm.h:1639，指针变体（备用）
}

var (
	loadOnce sync.Once
	loaded   *Library
	loadErr  error
)

// defaultLibPath 按任务约定返回默认库路径：
// 相对仓库根的 build/native/linux-amd64/librealm-ffi.so。
// go test 的进程 cwd 是包目录，故以本源文件位置推导仓库根。
func defaultLibPath() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("realmc: 无法定位源码路径以推导仓库根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // .../internal/realmc/lib.go -> 仓库根
	return filepath.Join(root, "build", "native", "linux-amd64", "librealm-ffi.so"), nil
}

// Load 加载 realm-core 共享库（进程内单例）。优先级：
// LZSYNC_LIBREALM 环境变量 > 仓库根默认路径 > cwd 相对默认路径。
func Load() (*Library, error) {
	loadOnce.Do(func() {
		path := os.Getenv("LZSYNC_LIBREALM")
		if path == "" {
			if p, err := defaultLibPath(); err == nil {
				path = p
			} else if p, err2 := filepath.Abs("build/native/linux-amd64/librealm-ffi.so"); err2 == nil {
				path = p
			}
		}
		handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("realmc: Dlopen %s: %w", path, err)
			return
		}
		lib := &Library{handle: handle}
		lib.bind()
		loaded = lib
	})
	return loaded, loadErr
}

// bind 注册全部符号；任一符号缺失即 panic（消息含符号名），
// 由调用方作为绑定层缺陷呈现，绝不静默降级。
func (l *Library) bind() {
	r := purego.RegisterLibFunc
	r(&l.realmGetLibraryVersion, l.handle, "realm_get_library_version")

	r(&l.realmGetLastError, l.handle, "realm_get_last_error")
	r(&l.realmRelease, l.handle, "realm_release")

	r(&l.realmConfigNew, l.handle, "realm_config_new")
	r(&l.realmConfigSetPath, l.handle, "realm_config_set_path")
	r(&l.realmConfigSetSchema, l.handle, "realm_config_set_schema")
	r(&l.realmConfigSetSchemaVersion, l.handle, "realm_config_set_schema_version")
	r(&l.realmConfigSetSchemaMode, l.handle, "realm_config_set_schema_mode")
	r(&l.realmConfigSetScheduler, l.handle, "realm_config_set_scheduler")
	r(&l.realmSchedulerNew, l.handle, "realm_scheduler_new")

	r(&l.realmSchemaNew, l.handle, "realm_schema_new")
	r(&l.realmSchemaRelease, l.handle, "realm_release") // realm_schema_t 也是 C Wrapper 对象
	r(&l.realmSchemaValidate, l.handle, "realm_schema_validate")

	r(&l.realmOpen, l.handle, "realm_open")
	r(&l.realmClose, l.handle, "realm_close")

	r(&l.realmBeginRead, l.handle, "realm_begin_read")
	r(&l.realmBeginWrite, l.handle, "realm_begin_write")
	r(&l.realmCommit, l.handle, "realm_commit")
	r(&l.realmRollback, l.handle, "realm_rollback")

	r(&l.realmGetSchemaVersion, l.handle, "realm_get_schema_version")
	r(&l.realmGetNumClasses, l.handle, "realm_get_num_classes")
	r(&l.realmGetClassKeys, l.handle, "realm_get_class_keys")
	r(&l.realmFindClass, l.handle, "realm_find_class")
	r(&l.realmGetClass, l.handle, "realm_get_class")
	r(&l.realmGetClassProperties, l.handle, "realm_get_class_properties")
	r(&l.realmFindProperty, l.handle, "realm_find_property")
	r(&l.realmGetNumObjects, l.handle, "realm_get_num_objects")

	r(&l.realmObjectFindAll, l.handle, "realm_object_find_all")
	r(&l.realmObjectCreate, l.handle, "realm_object_create")
	r(&l.realmResultsCount, l.handle, "realm_results_count")
	r(&l.realmResultsGetObject, l.handle, "realm_results_get_object")

	r(&l.realmGetValue, l.handle, "realm_get_value")
	r(&l.realmSetValue, l.handle, "realm_set_value")
	r(&l.realmSetValues, l.handle, "realm_set_values")
}

// Version 返回 realm-core 库版本串（realm.h:366）。
func (l *Library) Version() string {
	return l.realmGetLibraryVersion()
}

// cbAlwaysOnThread 是注入 realm-core 的唯一 Go 回调：无状态、恒真、不重入
// （realm.h:346 realm_scheduler_is_on_thread_func_t）。之所以必须：
// 本构建（Linux、无 UV/CF/ALOOPER）没有平台默认 scheduler，
// realm_open 内部 Scheduler::make_platform_default 会直接 REALM_TERMINATE
// （realm-core scheduler.cpp:130-142）；C API 也未暴露 set_default_factory。
// 其余回调全传 NULL：is_on_thread→恒真（否则 verify_thread 抛 WrongThread），
// can_invoke→false（仅 async 事务/通知投递需要，同步路径不依赖），
// notify→NULL（invoke 变 no-op，通知闭包被丢弃，PoC 不用通知）。
// 组合语义等同 realm-core 自带的 DummyScheduler（scheduler.cpp:78-92）。
var cbAlwaysOnThread = purego.NewCallback(func(unsafe.Pointer) bool { return true })

// newDummyScheduler 构造无通知、无线程限制的 scheduler（每次打开一个）。
func (l *Library) newDummyScheduler() (unsafe.Pointer, error) {
	h := l.realmSchedulerNew(0, 0, 0, cbAlwaysOnThread, 0, 0)
	if h == nil {
		return nil, fmt.Errorf("realm_scheduler_new: %w", l.lastError())
	}
	return h, nil
}

// lastError 解码线程局部的最近一次错误（realm.h:383-393：消息仅在
// 下一次 API 调用前安全，故立即拷贝为 Go string）。
func (l *Library) lastError() error {
	var e realmError
	if !l.realmGetLastError(&e) {
		return errors.New("realmc: C 调用失败且无线程错误信息")
	}
	return fmt.Errorf("realmc: %s (errno=%d path=%q)",
		goString(e.message), e.errNo, goString(e.path))
}

// goString 拷贝 C 侧 NUL 结尾字符串；nil 返回空串。
func goString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	var n int
	for (*(*[1 << 20]byte)(p))[n] != 0 { // 上限防御，正常 NUL 远早出现
		n++
	}
	return string((*(*[1 << 20]byte)(p))[:n:n])
}
