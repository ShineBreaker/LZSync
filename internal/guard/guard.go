// Package guard 承载写入安全三件套（design.md §3.1）：进程守卫（S5）、
// 时间戳备份（S2）与写入后 mtime 戳（mmap 写入不更新 mtime 的坑）。
package guard

import "errors"

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("guard: 契约桩未实现")

// ErrGameRunning 目标游戏进程正在运行（§3.3 用户可解错误四值枚举中的
// FileInUse）：写入前必须拒绝，把「请先关闭游戏」作为一等公民提示，
// 不做等待重试写入。
var ErrGameRunning = errors.New("guard: 目标游戏进程正在运行，请先关闭游戏后再同步")

// Guard 是按进程名名单工作的进程守卫。占用探测的最强形式是「写会话打开
// 本身」（realmc.OpenDynamicReadWrite，S5）——本守卫只负责提前给出友好
// 提示。名单来自 ForkProfile.ProcessNames + 用户补充（§6.4；注意 g0v0
// 进程名为 g0v0! 带感叹号，不可想当然去掉）。
type Guard struct {
	ProcessNames []string
}

// NewGuard 创建守卫。
func NewGuard(processNames []string) *Guard {
	return &Guard{ProcessNames: processNames}
}

// Running 返回当前正在运行的名单内进程名（全平台枚举）。
func (g *Guard) Running() []string {
	return nil
}

// EnsureClear 确认无名单内进程在运行；有则返回 ErrGameRunning
// （错误消息携带运行中进程名单）。
func (g *Guard) EnsureClear() error {
	return ErrNotImplemented
}

// Backups 是集中管理的时间戳备份目录（hub 目录布局下的 backups/，§3.4）。
type Backups struct {
	Dir string
}

// OpenBackups 打开（不存在则创建）备份目录。
func OpenBackups(dir string) (*Backups, error) {
	return nil, ErrNotImplemented
}

// BackupFile 为 srcPath 生成时间戳备份（S2 写前自动备份）：复制为
// <Dir>/<文件名>.<UTC 时间戳>；已存在同名备份绝不覆盖（返回错误）；
// 目录不可写时由调用方退化为库同目录带时间戳副本。
func (b *Backups) BackupFile(srcPath string) (string, error) {
	return "", ErrNotImplemented
}

// Restore 从 backupPath 还原到 targetPath：还原前自动对 targetPath 再做
// 一次安全备份（ezrealmsync-design §5.2），校验后覆盖。
func (b *Backups) Restore(backupPath, targetPath string) error {
	return ErrNotImplemented
}

// TouchAfterWrite 在 realm 写事务提交后更新文件 mtime：mmap 写入不更新
// mtime，快照过期判定（presence.collected_at vs 库文件 mtime）会失真
// （ezrealmsync-design §5.6）。
func TouchAfterWrite(path string) error {
	return ErrNotImplemented
}
