package deploy

// 目标库会话辅助：非阻塞占用探测（S5）、写前备份（S2）、批事务骨架。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"lzsync/internal/guard"
	"lzsync/internal/realmc"
)

// ErrTargetOccupied 目标库被占用（S5 / §3.3 四值枚举的 FileInUse）：
// 拒绝写入并把「请先关闭游戏」作为一等公民提示，不做等待重试写入。
var ErrTargetOccupied = errors.New("deploy: 目标库正被其他进程占用，请先关闭游戏后再同步")

// ErrDriftGuard DriftGuard 失败（写前/写后结构档案不一致）：只可能是
// LZSync 自身缺陷（动态会话不迁移，S3），整批回滚并上报（§3.3）。
var ErrDriftGuard = errors.New("deploy: 写前/写后结构档案漂移（工具缺陷，请报告）")

// probeOccupied 在打开目标库前做非阻塞占用探测（S5）。平台实现见
// lock_unix.go / lock_windows.go。
func probeOccupied(realmPath string) error {
	return probeOccupiedPlatform(realmPath)
}

// backupStamp 与 guard 包的备份时间戳格式一致（UTC，毫秒精度）。
const backupStamp = "20060102T150405.000Z"

// backupTarget 写前自动备份（S2）：deps.Backups 非空走 guard 的集中备份
// 目录；未配置（nil）退化为库同目录带时间戳副本——S2 的既定退化语义。
// 已存在同名备份绝不覆盖（返回错误，中止投放）。
func backupTarget(backups *guard.Backups, realmPath string) (string, error) {
	if backups != nil {
		return backups.BackupFile(realmPath)
	}
	dst := filepath.Join(filepath.Dir(realmPath),
		filepath.Base(realmPath)+"."+time.Now().UTC().Format(backupStamp))
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("deploy: 备份 %s 已存在，绝不覆盖（S2）", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("deploy: 检查备份目标 %s: %w", dst, err)
	}
	if err := copyFile(dst, realmPath); err != nil {
		return "", fmt.Errorf("deploy: 生成备份 %s: %w", dst, err)
	}
	return dst, nil
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".lzsync-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// 同目录 rename 原子入位（S12 纪律同源）：中途崩溃不留半份「正式名」备份。
	return os.Rename(tmp, dst)
}

// touchAfterWrite realm 写事务提交后更新库文件 mtime：mmap 写入不更新
// mtime，快照过期判定（presence.collected_at vs 库文件 mtime）会失真
//（ezrealmsync-design §5.6）。guard.TouchAfterWrite 仍为契约桩，此处为
// 其等价实现（os.Chtimes），guard 落地后切换调用。
func touchAfterWrite(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// driftGuardSession 是一个写事务批的骨架：开启时捕获写前结构档案，
// Finish 时再捕获写后档案并比对（任何漂移整批回滚，S1/§3.3）。
type driftGuardSession struct {
	r    *realmc.Realm
	pre  *realmc.SchemaSnapshot
	done bool
}

// beginDriftGuard 开写事务并捕获写前结构档案（要求尚未 BeginWrite）。
func beginDriftGuard(r *realmc.Realm) (*driftGuardSession, error) {
	if err := r.BeginWrite(); err != nil {
		return nil, fmt.Errorf("deploy: 开写事务: %w", err)
	}
	pre, err := r.CaptureSchemaSnapshot()
	if err != nil {
		r.Rollback() //nolint:errcheck // 回滚失败只能上抛原始错误
		return nil, fmt.Errorf("deploy: 捕获写前结构档案: %w", err)
	}
	return &driftGuardSession{r: r, pre: pre}, nil
}

// finish 校验结构零漂移后提交；任何失败（写入异常由调用方先 Rollback）
// 都保证事务已回滚。
func (d *driftGuardSession) finish() error {
	post, err := d.r.CaptureSchemaSnapshot()
	if err != nil {
		d.r.Rollback() //nolint:errcheck
		return fmt.Errorf("deploy: 捕获写后结构档案: %w", err)
	}
	if !d.pre.Equal(post) {
		d.r.Rollback() //nolint:errcheck
		return ErrDriftGuard
	}
	if err := d.r.Commit(); err != nil {
		d.r.Rollback() //nolint:errcheck
		return fmt.Errorf("deploy: 提交批事务: %w", err)
	}
	d.done = true
	return nil
}

// rollback 回滚整批（批内写入异常 / 执行期中止）。
func (d *driftGuardSession) rollback() {
	if !d.done {
		d.r.Rollback() //nolint:errcheck
	}
}
