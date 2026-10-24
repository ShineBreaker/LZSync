package hub

import (
	"context"
	"time"
)

// Snapshot 是一次收集投影的完整入仓载荷（§3.2 收集步骤②）。
// 「投影完整成功」是生成 tombstone 的前置条件：部分失败/中断的收集不得
// 构造本结构落库（防误标删除，HB2）。
type Snapshot struct {
	ForkID      string
	CollectedAt time.Time
	Collections []Collection
	BeatmapSets []BeatmapSet
	Skips       []SkipStat // 投影期跳过（进 collect_log）
}

// ApplyResult 是 ApplySnapshot 的执行摘要。
type ApplyResult struct {
	EntitiesWritten     map[EntityKind]int
	TombstonesAdded     int
	TombstonesCleared   int
	VersionLinksCreated int // 新维护的 superseded_by 指针数（HB1）
}

// ApplySnapshot 以单次 SQLite 事务应用一次收集快照（§3.3 收集方向原子性），
// 五个效果要么完整生效要么整体不生效：
//  1. 实体 upsert：整行覆盖并随行更新 origin_fork（HB3）；
//  2. presence 按 fork 整段重写（快照语义，幂等）；
//  3. 与上次快照差集生成 tombstone——被版本链取代的消失键除外（HB2）；
//  4. 经 beatmap_identity 配对维护版本链 superseded_by（HB1）；
//  5. 写 collect_log 审计行。
func (s *Store) ApplySnapshot(ctx context.Context, snap Snapshot) (ApplyResult, error) {
	return ApplyResult{}, ErrNotImplemented
}
