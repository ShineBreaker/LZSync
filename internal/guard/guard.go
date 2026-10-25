// Package guard 承载写入安全三件套（design.md §3.1）：进程守卫（S5）、
// 时间戳备份（S2）与写入后 mtime 戳（mmap 写入不更新 mtime 的坑）。
package guard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

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

// Running 返回当前正在运行的名单内进程名（全平台枚举）。返回按名排序，
// 便于错误消息稳定可断言。
func (g *Guard) Running() []string {
	names := make(map[string]bool, len(g.ProcessNames))
	for _, n := range g.ProcessNames {
		if n != "" {
			names[n] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	var hit []string
	for proc := range listProcessNames() {
		if matchProcess(proc, names) {
			hit = append(hit, proc)
		}
	}
	sort.Strings(hit)
	return hit
}

// matchProcess 判断枚举到的进程名是否命中名单：精确相等；Windows 的
// tasklist 输出带 .exe 后缀而名单按程序集名登记（osu! vs osu!.exe），
// 追加后缀匹配（大小写不敏感，Win32 映像名语义）。
func matchProcess(proc string, names map[string]bool) bool {
	if names[proc] {
		return true
	}
	if runtime.GOOS == "windows" {
		if withExe := proc + ".exe"; !names[withExe] {
			for n := range names {
				if strings.EqualFold(n, withExe) {
					return true
				}
			}
		}
	}
	return false
}

// EnsureClear 确认无名单内进程在运行；有则返回 ErrGameRunning
// （错误消息携带运行中进程名单）。
func (g *Guard) EnsureClear() error {
	running := g.Running()
	if len(running) == 0 {
		return nil
	}
	return fmt.Errorf("%w：%s", ErrGameRunning, strings.Join(running, "、"))
}

// listProcessNames 枚举当前系统全部进程名（每进程贡献两个候选名：
// 内核进程名与 argv[0] 基名——脚本/解释器启动的进程后者更可靠）。
// 平台实现：Linux 读 /proc（无子进程开销）；darwin 走 ps；windows 走
// tasklist。枚举自身的失败按「无进程」降级（守卫是友好提示层，最强
// 占用探测在写会话打开本身，S5）。
func listProcessNames() map[string]bool {
	out := map[string]bool{}
	switch runtime.GOOS {
	case "linux":
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return out
		}
		for _, e := range entries {
			if !e.IsDir() || !isAllDigits(e.Name()) {
				continue
			}
			dir := filepath.Join("/proc", e.Name())
			if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
				out[strings.TrimSpace(string(b))] = true
			}
			if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(b) > 0 {
				// NUL 分隔，首段即 argv[0]
				argv0, _, _ := bytes.Cut(b, []byte{0})
				if len(argv0) > 0 {
					out[filepath.Base(string(argv0))] = true
				}
			}
		}
	case "darwin":
		// ps -axco command：仅输出每个进程的可执行名（无路径），逐行一条。
		if b, err := exec.Command("ps", "-axco", "command").Output(); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if name := strings.TrimSpace(line); name != "" && !strings.HasPrefix(name, "COMMAND") {
					out[name] = true
				}
			}
		}
	case "windows":
		// tasklist /FO CSV /NH：每行 "\"映像名\",\"PID\",…"。
		if b, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output(); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				fields := strings.Split(line, ",")
				if len(fields) > 0 {
					out[strings.Trim(fields[0], "\"")] = true
				}
			}
		}
	}
	return out
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// nowFunc 是时间戳备份的时钟（唯一注入缝：S2「同名绝不覆盖」分支的
// 可测性）。时间戳格式带毫秒，两次自然调用通常不撞名；固定时钟时
// 第二次备份必然撞名。
var nowFunc = time.Now

// backupStamp 是备份文件名的时间戳格式（UTC，毫秒精度）。
const backupStamp = "20060102T150405.000Z"

// Backups 是集中管理的时间戳备份目录（hub 目录布局下的 backups/，§3.4）。
type Backups struct {
	Dir string
}

// OpenBackups 打开（不存在则创建）备份目录。
func OpenBackups(dir string) (*Backups, error) {
	if dir == "" {
		return nil, errors.New("guard: 备份目录路径为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("guard: 创建备份目录 %s: %w", dir, err)
	}
	return &Backups{Dir: dir}, nil
}

// BackupFile 为 srcPath 生成时间戳备份（S2 写前自动备份）：复制为
// <Dir>/<文件名>.<UTC 时间戳>；已存在同名备份绝不覆盖（返回错误）；
// 目录不可写时由调用方退化为库同目录带时间戳副本。
// 毫秒级时间戳同 tick 内会撞名（测试连续两次 Backup 常见）：撞名时等
// 一毫秒重试，最多三次。「绝不覆盖」语义保留。
func (b *Backups) BackupFile(srcPath string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("guard: 打开备份源 %s: %w", srcPath, err)
	}
	defer src.Close()
	var dst string
	var dstFile *os.File
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Millisecond)
		}
		dst = filepath.Join(b.Dir, filepath.Base(srcPath)+"."+nowFunc().UTC().Format(backupStamp))
		dstFile, err = os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("guard: 创建备份文件 %s: %w", dst, err)
		}
		// 撞名，等下一毫秒重试。
	}
	if dstFile == nil {
		return "", fmt.Errorf("guard: 同名备份已存在，绝不覆盖: %s", dst)
	}
	if _, err := io.Copy(dstFile, src); err != nil {
		dstFile.Close()
		return "", fmt.Errorf("guard: 写备份 %s: %w", dst, err)
	}
	if err := dstFile.Close(); err != nil {
		return "", fmt.Errorf("guard: 关闭备份 %s: %w", dst, err)
	}
	return dst, nil
}

// Restore 从 backupPath 还原到 targetPath：还原前自动对 targetPath 再做
// 一次安全备份（ezrealmsync-design §5.2），校验后覆盖（临时名 + rename
// 原子替换——还原途中崩溃不留半份文件）。
func (b *Backups) Restore(backupPath, targetPath string) error {
	bf, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("guard: 打开备份 %s: %w", backupPath, err)
	}
	defer bf.Close()
	// 还原前安全备份（目标存在时；S2 同源纪律）。
	if _, err := os.Stat(targetPath); err == nil {
		if _, err := b.BackupFile(targetPath); err != nil {
			return fmt.Errorf("guard: 还原前安全备份失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("guard: 检查还原目标 %s: %w", targetPath, err)
	}
	// 校验后覆盖：先整份复制到同目录临时名，再原子替换。
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".lzsync-restore-*")
	if err != nil {
		return fmt.Errorf("guard: 建还原临时文件: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, bf); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("guard: 复制备份到临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("guard: 关闭还原临时文件: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("guard: 原子替换还原目标 %s: %w", targetPath, err)
	}
	return nil
}

// TouchAfterWrite 在 realm 写事务提交后更新文件 mtime：mmap 写入不更新
// mtime，快照过期判定（presence.collected_at vs 库文件 mtime）会失真
// （ezrealmsync-design §5.6）。
func TouchAfterWrite(path string) error {
	now := nowFunc()
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("guard: 更新 mtime %s: %w", path, err)
	}
	return nil
}
