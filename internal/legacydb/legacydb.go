// Package legacydb 解析与序列化旧版 osu!（stable）的二进制数据库文件：
// osu!.db 与 collection.db（scores.db 属二期成绩板块，本期不实现）。
// 格式固化自 docs/research/stable-legacy.md（§1/§2，源出 ppy/osu wiki
// 「Legacy database file structure」；已在本机 20260924 样本逐字节验证：
// 208 难度解析终点与文件尾精确重合、谱面 MD5 与 .osu 文件全量一致）。
// 本包只做内存态解析/序列化，不落盘；WriteCollectionDB 供投放侧整文件
// 原子重写（S9：临时产物→自检→rename）。
package legacydb

import "time"

// 版本分叉点（stable-legacy §1.3 版本差异汇总）。
const (
	// verByteDifficulties 之前：AR/CS/HP/OD 为 Byte、无四模式星数表、
	// 条目末尾多一个未知 Short。
	verByteDifficulties = 20140609
	// verNoEntrySize 之前：条目首字段为字节大小 Int。
	verNoEntrySize = 20191106
	// verFloatStarPairs 起：星数 pair 从 Int-Double 改 Int-Float。
	verFloatStarPairs = 20250107
)

// BeatmapDB 是 osu!.db 的完整投影（stable-legacy §1.2）。
type BeatmapDB struct {
	Version         int
	FolderCount     int
	AccountUnlocked bool
	DateUnlocked    time.Time // .NET ticks（0 = 未解锁占位，stable-legacy §1.1）
	PlayerName      string
	UserPermissions int
	Beatmaps        []BeatmapEntry
	// Trailing 是解析完全部条目与尾部权限后的剩余字节数：0 = 与已知
	// 格式精确收官；>0 = 该版本追加了未知晓的尾部结构（前向兼容信号，
	// 调用方可据此告警而非拒读）。
	Trailing int
}

// StarRating 是星数表一对（Int = mod 组合位标志，stable-legacy §1.3）。
type StarRating struct {
	Mods  int
	Stars float64
}

// TimingPoint 是 osu!.db 的定时间点（stable-legacy §1.1）。
type TimingPoint struct {
	BPM       float64
	OffsetMs  float64
	Inherited bool
}

// BeatmapEntry 是单个难度的索引行（stable-legacy §1.3，字段名随原文档）。
type BeatmapEntry struct {
	Artist, ArtistUnicode       string
	Title, TitleUnicode         string
	Creator, DifficultyName     string
	AudioFilename               string
	MD5Hash                     string // 谱面 MD5 = .osu 文件 MD5（跨端对齐锚点，stable-legacy §4）
	OsuFilename                 string
	Status                      int    // Ranked 状态 0..7；0=未知（离线下载未提交）
	CircleCount, SliderCount, SpinnerCount int
	LastModified                time.Time
	AR, CS, HP, OD              float32
	SliderVelocity              float64
	StarRatings                 [4][]StarRating // osu!/taiko/catch/mania（v<20140609 恒空）
	DrainTimeSec, TotalTimeMs, PreviewTimeMs   int
	TimingPoints                []TimingPoint
	DifficultyID, BeatmapID, ThreadID          int
	Grades                      [4]int
	LocalOffset                 int16
	StackLeniency               float32
	Mode                        int // 0=osu! 1=taiko 2=catch 3=mania
	Source, Tags                string
	OnlineOffset                int16
	Font                        string
	Unplayed                    bool
	LastPlayed                  time.Time
	IsOsz2                      bool
	FolderName                  string // 相对 Songs/ 的目录名（含 <setID> 前缀）
	LastChecked                 time.Time
	IgnoreSound, IgnoreSkin     bool
	DisableStoryboard           bool
	DisableVideo                bool
	VisualOverride              bool
	UnknownShort                uint16 // 仅 v<20140609（stable-legacy §1.3）
	UnknownInt                  uint32 // 文档「最后修改时间(?)」，语义未确证
	ManiaScrollSpeed            int
}

// CollectionDB 是 collection.db 的完整投影（stable-legacy §2）。
type CollectionDB struct {
	Version     int
	Collections []Collection
}

// Collection 是单条收藏：名称 + 谱面 MD5 列表。成员即 MD5、可跨安装
// 转移——与 lazer BeatmapCollection.BeatmapMD5Hashes 一一对应（stable-legacy §4）。
type Collection struct {
	Name        string
	BeatmapMD5s []string
}
