//go:build windows

package deploy

// Windows 平台的占用探测（S5）。实证（realm-core 20.1.1 源码，
// src/realm/util/interprocess_mutex.hpp + src/realm/db.cpp）：
//
//   - Windows 上 realm 的跨进程互斥不是 <db>.lock 文件锁（flock 语义
//     与共享冲突都探测不到——实测 CreateFileW(FILE_SHARE_NONE) 恒成功，
//     CI Windows 门禁首跑拦下），而是命名互斥体：
//     InterprocessMutex::set_shared_part 构造
//     "Local\realm_named_intermutex_<prefix 转义>write"
//     （db.cpp :1114，mutex 后缀 "write" 与 prefix 直接拼接无分隔符）。
//   - 写互斥由 DB::do_begin_write 持有直到 do_end_write（commit/
//     rollback）——「写事务进行中」的精确信号。
//
// 探测 = 试拿同名互斥体（WaitForSingleObject 0ms）：拿到 = 无写事务
// 进行中（放行）；超时 = 活动 realm 写事务（判 ErrTargetOccupied）。
// 与 Unix 的 flock 探测语义对齐（Unix 拦任意会话，Windows 拦写事务
// ——写冲突才是 S5 要防的数据危险）。探测成功立即释放；探测自身与
// realm 打开互不干扰。互斥体名钉死 realm-core 20.1.1 语义，升级
// realm 版本时需复核此名。

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// realmWriteMutexName 构造 realm-core 写互斥的命名互斥体名（UTF-16
// 指针），逐字符对齐 interprocess_mutex.hpp set_shared_part 的 Windows
// 分支。
func realmWriteMutexName(realmPath string) (*uint16, error) {
	// db.cpp: lockfile_prefix = get_core_file(path, Management) + "/access_control"
	prefix := realmPath + ".management/access_control"
	// interprocess_mutex.hpp: 反斜杠全部替换为正斜杠（内核对象名语义）
	escaped := strings.ReplaceAll(prefix, `\`, `/`)
	name := `Local\realm_named_intermutex_` + escaped + "write"
	// interprocess_mutex.hpp: std::wstring(name.begin(), name.end()) 是
	// 逐字节 Latin-1 宽化（非 UTF-8 转码）——非 ASCII 路径（如中文用户
	// 名）的互斥体名是 UTF-8 字节逐个宽化的「乱码名」，探测必须复现同
	// 一宽化才能对上 realm 持有的锁。
	w := make([]uint16, 0, len(name)+1)
	for i := 0; i < len(name); i++ {
		w = append(w, uint16(name[i]))
	}
	w = append(w, 0)
	return &w[0], nil
}

func probeOccupiedPlatform(realmPath string) error {
	name, err := realmWriteMutexName(realmPath)
	if err != nil {
		return fmt.Errorf("deploy: 探测占用（互斥体名编码）: %w", err)
	}
	// InitialOwner=false：创建不持有，仅借句柄试锁。互斥体不存在时
	// CreateMutexW 会新建（=无会话），CloseHandle 后引用归零自动销毁，
	// 对后续 realm 打开无影响。
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		// 与 Unix 探测同语义：占用信号不可得本身即异常形态，按占用拒
		// （保守侧，S5 防写坏优先）。
		return fmt.Errorf("%w（写互斥不可探测: %v）", ErrTargetOccupied, err)
	}
	defer windows.CloseHandle(h) //nolint:errcheck // 探测句柄，关闭失败不影响判定
	event, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return fmt.Errorf("%w（写互斥不可探测: %v）", ErrTargetOccupied, err)
	}
	switch event {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
		// 拿到锁 = 无写事务进行中（abandoned = 持有进程已死、锁已授予
		// 本进程，realm 自身有崩溃恢复机制兜底）。立即释放放行。
		if err := windows.ReleaseMutex(h); err != nil {
			return fmt.Errorf("deploy: 释放占用探测锁: %w", err)
		}
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("%w（持有者: 活动 realm 写事务）", ErrTargetOccupied)
	default:
		return fmt.Errorf("%w（写互斥不可探测: 未知等待结果 %d）", ErrTargetOccupied, event)
	}
}
