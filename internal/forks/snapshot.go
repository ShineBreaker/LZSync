package forks

import (
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
type StructureSnapshot struct {
	ForkID      string
	DiskVersion uint64
	TakenAt     time.Time
	Classes     []ClassSnapshot
}

// SnapshotStore 管理结构档案目录（hub 目录布局下的 snapshots/，§3.4）：
// 落盘为 snapshots/{fork}-{diskVersion}.json；同版本只留首份；采集失败只
// 记日志（ezrealmsync-design §4.1）。
type SnapshotStore struct {
	Dir string
}

// OpenSnapshotStore 打开（不存在则创建）结构档案目录。
func OpenSnapshotStore(dir string) (*SnapshotStore, error) {
	return nil, ErrNotImplemented
}

// Load 读取 (fork, diskVersion) 的档案；不存在返回 (nil, nil)；文件存在但
// 损坏返回错误（调用方可按「无档案 + 记日志」降级处理）。
func (s *SnapshotStore) Load(forkID string, diskVersion uint64) (*StructureSnapshot, error) {
	return nil, ErrNotImplemented
}

// SaveIfAbsent 落盘档案；同 (fork, diskVersion) 已存在则不覆盖并返回 false
// （同版本只留首份）。
func (s *SnapshotStore) SaveIfAbsent(snap *StructureSnapshot) (bool, error) {
	return false, ErrNotImplemented
}
