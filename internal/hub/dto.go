// Package hub 是 SQLite 中枢（单一真相源，design.md §3.1/§4.4）：hub.db 的
// schema 与版本化迁移、实体表、presence 快照、tombstone、版本链、仓清单
// 与收集接口。中立 DTO 亦定义于此（M1 从简并入；设计中的独立 neutral 包
// 待 M2 实体扩面时再评估拆分——hub 是收集与投放两段之间的唯一交接面）。
package hub

import "time"

// EntityKind 实体类型（§4.4）。key_binding / ruleset_setting / mod_preset
// 由后续版本的 schema 迁移启用。
type EntityKind string

const (
	KindCollection EntityKind = "collection"
	KindBeatmapSet EntityKind = "beatmap_set"
	KindBeatmap    EntityKind = "beatmap"
	KindSkin       EntityKind = "skin"
	KindScore      EntityKind = "score"
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

// RealmUser 嵌入用户（§4.1 NeutralMetadata.Author 与 NeutralScore.User 共用
// 形态，lazer-baseline §2.3：等值判定 OnlineID+Username，CountryString 为
// 枚举字符串列）。
type RealmUser struct {
	OnlineID      int
	Username      string
	CountryString string
}

// Difficulty 难度数值（§4.1 NeutralBeatmapDifficulty；realm 侧是
// BeatmapInfo 内嵌的 EmbeddedObject，lazer-baseline §2.5.1）。
type Difficulty struct {
	DrainRate         float32
	CircleSize        float32
	OverallDifficulty float32
	ApproachRate      float32
	SliderMultiplier  float64
	SliderTickRate    float64
}

// Metadata 谱面元数据（§4.1 NeutralMetadata，lazer-baseline §2.6）。hub 侧
// 内联为 beatmap 行载荷（一对一）；投放时写入器再拆写回 realm 的独立
// Metadata 行并遵守复用规则（§4.2）。
type Metadata struct {
	Title          string
	TitleUnicode   string
	Artist         string
	ArtistUnicode  string
	Author         RealmUser
	Source         string
	Tags           string
	UserTags       []string
	PreviewTime    int
	AudioFile      string
	BackgroundFile string
}

// BeatmapSet 谱面集中立 DTO（v2 起扩为 C2 白名单全列，§4.1；加字段不破坏
// 既有列名——v1 落库的旧 payload 缺列解码为零值）。
// 内容键 = Hash（集内全部 .osu 拼接 SHA-256，语义是版本指纹而非谱面身份）。
// DeletePending/Protected 列集完整性保留：投影端按 §4.1 跳过软删行与
// Protected 系统集，正常进入 hub 的行恒为 false。
type BeatmapSet struct {
	ID             string
	Hash           string
	OnlineID       int64
	StatusInt      int
	DateAdded      time.Time
	DateSubmitted  *time.Time
	DateRanked     *time.Time
	DeletePending  bool
	Protected      bool
	Artist         string // 首难度 Metadata 的谱面身份键字段（§4.2 第二层）
	Title          string
	AuthorUsername string
	Beatmaps       []Beatmap
	Files          []NamedFile
}

// Beatmap 难度中立 DTO（v2 起扩为 C2 白名单全列，§4.1 NeutralBeatmap：
// Difficulty embedded、Metadata 内联、UserSettingsOffset 为唯一列
// BeatmapUserSettings.Offset 的扁平投影，lazer-baseline §2.5/§2.5.1/
// §2.5.2/§2.6）。内容键 = Hash（归一化 .osu SHA-256）。
type Beatmap struct {
	ID                 string
	Hash               string
	MD5Hash            string
	OnlineMD5Hash      string
	DifficultyName     string
	RulesetShortName   string
	StatusInt          int
	OnlineID           int
	Length             float64
	BPM                float64
	StarRating         float64
	LastLocalUpdate    *time.Time
	LastOnlineUpdate   *time.Time
	Hidden             bool
	EndTimeObjectCount int
	TotalObjectCount   int
	LastPlayed         *time.Time
	BeatDivisor        int
	EditorTimestamp    *float64
	Difficulty         Difficulty
	Metadata           Metadata
	UserSettingsOffset float64
}

// Skin 皮肤中立 DTO（§4.1 NeutralSkin，lazer-baseline §2.10）。
// 内容键 = Hash（.ini+.json 拼接 SHA-256，§4.2 第一层）；Hash 为空的行
// 由投影端跳过（无法判定身份）。Protected 内置皮肤行同样由投影端跳过
// （固定 Guid 四端自备，同步无收益），列集完整性保留。
type Skin struct {
	ID                string
	Hash              string
	Name              string
	Creator           string
	InstantiationInfo string
	Protected         bool
	DeletePending     bool
	Files             []NamedFile
}

// Score 成绩中立 DTO（§4.1 NeutralScore，lazer-baseline §2.7）。
// 内容键 = (BeatmapHash, Hash) 组合键（§4.2 第一层，Date 消歧）。
// 可空列（LegacyTotalScore/PP）用指针承载，与 realm 侧 nullable 对齐。
type Score struct {
	ID                           string
	BeatmapHash                  string // 成绩与谱面的锚点（S7：投放端按此挂接）
	Hash                         string // .osr 内容哈希
	RulesetShortName             string
	ClientVersion                string
	DeletePending                bool
	TotalScore                   int64
	TotalScoreWithoutMods        int64
	TotalScoreVersion            int
	LegacyTotalScore             *int64
	BackgroundReprocessingFailed bool
	MaxCombo                     int
	Accuracy                     float64
	Date                         time.Time
	PP                           *float64
	OnlineID                     int64
	LegacyOnlineID               int64
	User                         RealmUser
	ModsJson                     string
	StatisticsJson               string
	MaximumStatisticsJson        string
	Pauses                       []int32
	RankInt                      int
	Combo                        int
	IsLegacyScore                bool
	Files                        []NamedFile
}

// SkipStat 投影期跳过计数——每一条被丢弃/跳过的数据必须计数并给出原因，
// 不静默吞掉（§1.5-4）。
type SkipStat struct {
	Reason string
	Count  int
}
