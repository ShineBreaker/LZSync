package forks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lzsync/internal/realmc"
)

// PropertySnapshot / ClassSnapshot / StructureSnapshot 是结构档案（schema
// 快照）的持久化形态（§4.3 运行时数据源 1）：对每个碰过的 (fork,
// diskVersion) 从真实库采集的 class/列/类型清单。用途：① 写入集 =
// 官方白名单 ∩ 源库实际列 ∩ 目标库实际列；② fork 专有列 = 目标库实际列
// − 同代官方基线档案；③ fork 身份指纹识别。
type PropertySnapshot struct {
	Name       string
	Type       realmc.PropType
	Nullable   bool
	PrimaryKey bool
}

// ClassSnapshot 一个 class（表）的结构档案行。
type ClassSnapshot struct {
	Name       string
	PrimaryKey string
	Properties []PropertySnapshot
}

// StructureSnapshot 一次 (fork, diskVersion) 的完整结构档案。
// SourcePath 记录采集时的来源库绝对路径：IdentifyCached 降级链第 ③ 段
//（开库失败且文件名不可推断时）按它反查档案，取回 (ForkID, DiskVersion)。
type StructureSnapshot struct {
	ForkID      string
	DiskVersion uint64
	SourcePath  string
	TakenAt     time.Time
	Classes     []ClassSnapshot
}

// SnapshotStore 管理结构档案目录（hub 目录布局下的 snapshots/，§3.4）：
// 落盘为 snapshots/{fork}-{diskVersion}.json；同版本只留首份；坏文件按
// 不存在处理（ezrealmsync-design §4.1）。
type SnapshotStore struct {
	Dir string
}

// OpenSnapshotStore 打开（不存在则创建）结构档案目录。
func OpenSnapshotStore(dir string) (*SnapshotStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("forks: 创建结构档案目录 %s: %w", dir, err)
	}
	return &SnapshotStore{Dir: dir}, nil
}

func (s *SnapshotStore) fileFor(forkID string, diskVersion uint64) string {
	return filepath.Join(s.Dir, fmt.Sprintf("%s-%d.json", forkID, diskVersion))
}

// Load 读取 (fork, diskVersion) 的档案；不存在返回 (nil, nil)；文件损坏
// 按不存在处理（返回 nil, nil——调用方按「无档案」降级并记日志即可，
// ezrealmsync-design §4.1 同款语义：坏 JSON 当没有这份快照）。
func (s *SnapshotStore) Load(forkID string, diskVersion uint64) (*StructureSnapshot, error) {
	b, err := os.ReadFile(s.fileFor(forkID, diskVersion))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("forks: 读取结构档案: %w", err)
	}
	var snap StructureSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, nil // 坏文件按不存在处理
	}
	return &snap, nil
}

// SaveIfAbsent 落盘档案；同 (fork, diskVersion) 已存在则不覆盖并返回 false
//（同版本只留首份）。写入以 O_CREATE|O_EXCL 直开目标文件保证首份语义；
// 进程中途崩溃留下的半截文件在 Load 侧按坏文件（= 不存在）自愈，下次
// 采集可重写，与「临时文件 + rename」手法（ezrealmsync-design §4.1）在
// 本仓库的坏文件语义下等价。
func (s *SnapshotStore) SaveIfAbsent(snap *StructureSnapshot) (bool, error) {
	path := s.fileFor(snap.ForkID, snap.DiskVersion)
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return false, fmt.Errorf("forks: 序列化结构档案: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil // 同版本只留首份
		}
		return false, fmt.Errorf("forks: 写入结构档案 %s: %w", path, err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return false, fmt.Errorf("forks: 写入结构档案 %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("forks: 关闭结构档案 %s: %w", path, err)
	}
	return true, nil
}

// findBySourcePath 按来源库路径反查档案（IdentifyCached 降级链第 ③ 段）。
// 档案目录内文件数量 = 碰过的 (fork, 版本) 数（个位数级），全量扫描可
// 接受；同路径多份时取采集时间最新。坏文件跳过。
func (s *SnapshotStore) findBySourcePath(realmPath string) *StructureSnapshot {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	abs, err := filepath.Abs(realmPath)
	if err != nil {
		abs = realmPath
	}
	var best *StructureSnapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			continue
		}
		var snap StructureSnapshot
		if json.Unmarshal(b, &snap) != nil || snap.SourcePath != abs {
			continue
		}
		if best == nil || snap.TakenAt.After(best.TakenAt) {
			best = &snap
		}
	}
	return best
}

// Capture 动态只读打开 sourcePath 并采集一份结构档案（collect 的「顺手
// 采集」步骤，§3.2/§4.3）。PrimaryKey 按 class 级主键名推导；Nullable 需
// 属性级 flags，realmc.PropertyInfo 暂未暴露，恒为 false（M2 绑定层补
// flags 后填实，不影响指纹判定与列求交用途）。
func Capture(lib *realmc.Library, forkID, sourcePath string) (*StructureSnapshot, error) {
	r, err := lib.OpenDynamicReadOnly(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("forks: 采集结构档案（打开 %s）: %w", sourcePath, err)
	}
	defer r.Close()
	classes, err := r.Classes()
	if err != nil {
		return nil, fmt.Errorf("forks: 采集结构档案（枚举 class）: %w", err)
	}
	snap := &StructureSnapshot{
		ForkID:      forkID,
		DiskVersion: r.SchemaVersion(),
		TakenAt:     time.Now().UTC(),
		Classes:     make([]ClassSnapshot, 0, len(classes)),
	}
	if snap.SourcePath, err = filepath.Abs(sourcePath); err != nil {
		snap.SourcePath = sourcePath
	}
	for _, c := range classes {
		props, err := r.Properties(c.Key)
		if err != nil {
			return nil, fmt.Errorf("forks: 采集结构档案（枚举 %s 属性）: %w", c.Name, err)
		}
		row := ClassSnapshot{Name: c.Name, PrimaryKey: c.PrimaryKey, Properties: make([]PropertySnapshot, 0, len(props))}
		for _, pr := range props {
			row.Properties = append(row.Properties, PropertySnapshot{
				Name:       pr.Name,
				Type:       pr.Type,
				PrimaryKey: pr.Name == c.PrimaryKey,
			})
		}
		snap.Classes = append(snap.Classes, row)
	}
	return snap, nil
}
