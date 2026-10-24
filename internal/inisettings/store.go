package inisettings

// hub 目录布局下的 game.ini 快照仓（§3.4：与 snapshots/（结构档案）同款
// 的「hub 目录自有文件」模式——forks.SnapshotStore 先例）。每 fork 一份
// JSON：最近一次收集确立的白名单键 canonical 值（HB3 的 ini 形态：整份
// 覆盖、来源 fork 即文件名）。写入走临时文件 + rename 原子替换（S12 同源
// 纪律——快照半份同样是坏状态）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Store 管理 hub 目录下的 gameini/ 快照目录。
type Store struct {
	Dir string
}

// OpenStore 打开（不存在则创建）快照目录。
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("inisettings: 快照目录路径为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("inisettings: 创建快照目录 %s: %w", dir, err)
	}
	return &Store{Dir: dir}, nil
}

// forkIDPat 限制快照文件名仅由小写字母/数字/连字符构成（forkID 来自
// forks 注册表；未注册 ID 含路径分隔符时拒绝而非拼路径）。
var forkIDPat = regexp.MustCompile(`^[a-z0-9-]+$`)

// snapshot 是快照文件的 JSON 载荷。
type snapshot struct {
	ForkID      string            `json:"fork_id"`
	CollectedAt time.Time         `json:"collected_at"`
	Keys        map[string]string `json:"keys"`
}

func (s *Store) fileFor(forkID string) (string, error) {
	if !forkIDPat.MatchString(forkID) {
		return "", fmt.Errorf("inisettings: 非法 fork id %q（快照文件名受限字符集）", forkID)
	}
	return filepath.Join(s.Dir, forkID+".json"), nil
}

// Save 整份覆盖某 fork 的白名单键快照（临时文件 + rename 原子替换）。
func (s *Store) Save(forkID string, keys map[string]string, at time.Time) error {
	path, err := s.fileFor(forkID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(snapshot{ForkID: forkID, CollectedAt: at.UTC(), Keys: keys}, "", "  ")
	if err != nil {
		return fmt.Errorf("inisettings: 序列化 %s 快照: %w", forkID, err)
	}
	return writeFileAtomic(path, b, 0o644)
}

// Load 读某 fork 的白名单键快照；无快照返回 false（调用方按「源端无
// ini 快照」计数跳过）。
func (s *Store) Load(forkID string) (map[string]string, time.Time, bool, error) {
	path, err := s.fileFor(forkID)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("inisettings: 读 %s 快照: %w", forkID, err)
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, time.Time{}, false, fmt.Errorf("inisettings: 解码 %s 快照: %w", forkID, err)
	}
	return snap.Keys, snap.CollectedAt, true, nil
}

// writeFileAtomic 是 S12 的文件级原子写原语：同目录临时文件 → rename
// 原子替换，绝不就地覆写。beforeRename 注入缝在 rename 前调用（崩溃
// 注入测试：返回错误 → 清理临时文件、原文件一字未动）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lzsync-ini-*")
	if err != nil {
		return fmt.Errorf("inisettings: 建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName) //nolint:errcheck // 清理失败只能泄漏临时文件
		return fmt.Errorf("inisettings: 写临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("inisettings: 关临时文件: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("inisettings: 设临时文件权限: %w", err)
	}
	// 崩溃注入缝（测试专用）：rename 前中断 = 原文件未动、无半份正式文件。
	if hook := beforeRenameHook; hook != nil {
		if err := hook(); err != nil {
			os.Remove(tmpName) //nolint:errcheck
			return fmt.Errorf("inisettings: 原子替换前中断（原文件未动）: %w", err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("inisettings: 原子替换 %s: %w", path, err)
	}
	return nil
}

// beforeRenameHook 是 writeFileAtomic 的崩溃注入缝（S12 验收「rename 前
// 杀不留半份文件」的可测性）。生产恒 nil。
var beforeRenameHook func() error
