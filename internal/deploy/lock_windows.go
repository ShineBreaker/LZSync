//go:build windows

package deploy

// Windows 平台的占用探测（S5 独占打开探测，等价 EZRealmSync 的
// FileShare.None 开-即-释）。语义与 lock_unix.go 的 flock 探测对齐：
// realm 会话存在即持有 <db>.lock 的打开句柄（realm-core 的
// InterprocessMutex 跨平台都走 .lock 文件，Windows 上为句柄 +
// LockFileEx），因此对 .lock 以 FILE_SHARE_NONE 尝试打开：任何持有
// 非零访问权句柄的进程（活动的 realm 会话）都会使打开失败
// （ERROR_SHARING_VIOLATION）→ 判占用。本探测自身以 0 访问权打开，
// 并发探测互不冲突。.lock 不存在 = 从未有会话打开过 = 无占用。

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func probeOccupiedPlatform(realmPath string) error {
	lockPath := realmPath + ".lock"
	p, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return fmt.Errorf("deploy: 探测占用（路径编码 %s）: %w", lockPath, err)
	}
	h, err := windows.CreateFile(
		p,
		0, // 无读写访问权，仅探测共享冲突
		0, // FILE_SHARE_NONE：与任何已持有访问权的句柄互斥
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND),
			errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
			return nil
		case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
			return fmt.Errorf("%w（持有者: 活动的 realm 会话）: %v", ErrTargetOccupied, err)
		default:
			// .lock 无法打开本身即异常占用形态（权限被夺等），按占用拒。
			return fmt.Errorf("%w（%s 无法访问: %v）", ErrTargetOccupied, lockPath, err)
		}
	}
	windows.CloseHandle(h) // 探测成功即释放，不持锁
	return nil
}
