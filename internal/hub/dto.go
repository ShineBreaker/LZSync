// Package hub 是 SQLite 中枢（单一真相源，design.md §3.1/§4.4）：hub.db 的
// schema 与版本化迁移、实体表、presence 快照、tombstone、版本链、仓清单
// 与收集接口。中立 DTO 亦定义于此（M1 从简并入；设计中的独立 neutral 包
// 待 M2 实体扩面时再评估拆分——hub 是收集与投放两段之间的唯一交接面）。
package hub

import "time"

// EntityKind 实体类型（§4.4）。M1 启用下列三种；skin / score / key_binding /
// ruleset_setting / mod_preset 由后续版本的 schema 迁移启用。
type EntityKind string

const (
	KindCollection EntityKind = "collection"
	KindBeatmapSet EntityKind = "beatmap_set"
	KindBeatmap    EntityKind = "beatmap"
)

// NamedFile 哈希 ↔ 文件名对（RealmFile 引用形态，§4.1）。
type NamedFile struct {
	Hash     string
	Filename string
}

// Collection 收藏夹中立 DTO（M1 投放载体：无 files、无跨表关系、无 Guid
// 依赖，§5.1）。内容键 = Name（§4.2 第一层）。
type Collection struct {
	ID               string   // 源 fork 侧 Guid（落库为 origin_guid，§4.4）
	Name             string   // 内容键（用户心智名）
	BeatmapMD5Hashes []string // 排序指纹作冲突判据
	LastModified     time.Time
}

// BeatmapSet 谱面集中立 DTO（M1 仅承载对比与版本链所需字段的收缩投影；
// M2 投放施工时按 C2 白名单扩列——加字段不破坏既有签名）。
// 内容键 = Hash（集内全部 .osu 拼接 SHA-256，语义是版本指纹而非谱面身份）。
type BeatmapSet struct {
	ID             string
	Hash           string
	OnlineID       int64
	StatusInt      int
	DateAdded      time.Time
	Artist         string // 首难度 Metadata 的谱面身份键字段（§4.2 第二层）
	Title          string
	AuthorUsername string
	Beatmaps       []Beatmap
	Files          []NamedFile
}

// Beatmap 难度中立 DTO（收缩投影）。内容键 = Hash（归一化 .osu SHA-256）。
type Beatmap struct {
	ID               string
	Hash             string
	MD5Hash          string
	DifficultyName   string
	RulesetShortName string
}

// SkipStat 投影期跳过计数——每一条被丢弃/跳过的数据必须计数并给出原因，
// 不静默吞掉（§1.5-4）。
type SkipStat struct {
	Reason string
	Count  int
}
