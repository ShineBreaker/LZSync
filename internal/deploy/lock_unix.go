//go:build !windows

package deploy

// Unix 平台的占用探测实现（S5 的「写会话打开本身是最强占用探测」的可
// 非阻塞化形态）。

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// probeOccupiedPlatform 实证依据（本包施工探针，librealm 20.1.1
// linux-amd64）：realm_open 即对 <db>.lock 持有 flock（哪怕尚未开写事务）；
// 跨进程持有会话/写事务时，realm_open 与 realm_begin_write 都不报错、
// 只会阻塞。因此可用的非阻塞占用信号是 .lock 上的排他 flock——且必须在
// 自身 open 之前探测（自身 open 之后锁已归自己）。.lock 不存在 = 从未有
// 会话打开过 = 无占用。探测成功后立即释放，open 与写事务的最终互斥仍由
// realm 自身保证。
func probeOccupiedPlatform(realmPath string) error {
	lockPath := realmPath + ".lock"
	if _, err := os.Stat(lockPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("deploy: 探测占用（stat %s）: %w", lockPath, err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		// .lock 无法打开本身即异常占用形态（权限被夺等），按占用拒。
		return fmt.Errorf("%w（%s 无法访问: %v）", ErrTargetOccupied, lockPath, err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("%w（持有者: 活动的 realm 会话）: %v", ErrTargetOccupied, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("deploy: 释放占用探测锁: %w", err)
	}
	return nil
}
