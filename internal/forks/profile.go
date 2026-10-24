// Package forks 承载四端 fork 的静态元数据注册表（ForkProfile）、库身份
// 识别与结构档案仓库（design.md §3.1、§4.3）。内核不内置任何具体 fork
// 知识——全部「Ez 怎样、g0v0 怎样」的差异只存在于本包的 Profile 数据与
// 运行时结构档案中：新增第五个 fork = 新增一份 Profile，不改内核。
//
// 全部静态数据固化自 docs/research/ 的字段级摘录（C-1 自包含纪律）：
// fork-comparison §0/§1（四端速览与总览矩阵）、fork-delta §1–§4、
// fork-ez2lazer §1–§2/§5.2、fork-g0v0 §1–§3。
package forks

// SkinFingerprint 标识 fork 专有皮肤（读取投影跳过，§4.1）。
// 判定通道二选一命中即专有：Guid（hex 32 字符，与 hub.Skin.ID 同形，
// EZRealmSync IsEzOnlySkin 的 GUID 手法，ezrealmsync-design §6.2）或
// Name+Creator 双等值。空字段不参与判定。
type SkinFingerprint struct {
	Name    string
	Creator string
	Guid    string // 行 Guid 的 hex（无连字符小写），固定常量类专有皮肤用
}

// IniKeyPolicy 是 game.ini 键策略（S10 红灯排除 + M3 白名单同步）。
type IniKeyPolicy struct {
	Shared []string // 四端共有白名单键
	Own    []string // 本端专有键（只在本端管理）
	Locked []string // 红灯硬锁定键（Token/Username 等，永不同步、UI 不可勾选）
}

// ForkProfile 是单个 fork 的静态差异声明（design.md §4.3）。
type ForkProfile struct {
	ID           string // "lazer" | "delta" | "ez2lazer" | "g0v0"
	DataDirName  string // osu / deltalazer / osu-Ez2Lazer / g0v0
	RealmFilePat string // client.realm；Ez 为 client_<diskVersion>.realm + 前驱链
	ProcessNames []string
	// DecodeVersion 解码磁盘 schema 版本：Ez 为 v>=1000 ? v/1000, v%1000 : v, 0。
	DecodeVersion func(uint64) (upstream int, forkRev int)
	// DiskVersion 当前已知的磁盘 schema 版本（fork-comparison §1.1：
	// 52 / 51 / 52011 / 52），版本探测降级链的身份判定数据落点。
	DiskVersion uint64
	// ServerRealm 服务器命名空间指纹（"ppy" vs "g0v0.top"），S8 判定依据。
	ServerRealm string
	// ExtColumns 本 fork 在官方基线之外追加的列（表名 → 列名；Ez 的 10 列）。
	ExtColumns map[string][]string
	// ExtTables 本 fork 专有表（Ez 7 张技能表）。
	ExtTables []string
	// OwnRulesets 专有规则集 ShortName（Ez: bms；g0v0 市场规则集 ShortName
	// 动态不可枚举，走 MarketRulesetMinOnlineID 判定）。
	OwnRulesets []string
	// OwnSkins 专有皮肤指纹。
	OwnSkins []SkinFingerprint
	// ExtSetHashPrefix 外部托管谱面集的内容键前缀（Ez 的 "ext:set:"——
	// 该形态 Hash 是路径编码而非内容哈希，files/ 无字节，fork-ez2lazer
	// §5.1；收集侧跳过 + 投放侧对 hub 已收行的防线判定，D5）。空 = 无
	// 此机制。
	ExtSetHashPrefix string
	// MarketRulesetMinOnlineID 规则集市场的 OnlineID 下界（g0v0：服务端
	// 市场第三方规则集 OnlineID >= 10 落库，fork-g0v0 §2.1）。> 0 时指向
	// 该域规则集的成绩在收集侧跳过（D8）。0 = 无市场机制。
	MarketRulesetMinOnlineID int64
	// ScoreJudgeColumns 成绩行的判定语义列（Ez 的 JudgeMode/HealthMode：
	// 非 0 = 非 Lazer 判定档成绩，官方端重算会错，R7）。列缺省自然 0。
	// 读取投影时任一列非 0 → 跳过并计数。
	ScoreJudgeColumns []string
	// ModAcronyms 本端可解析的 mod acronym 名录（Ordinal 区分大小写，
	// fork-comparison D7 的 shared 集先例）。投放成绩时 acronym 不在
	// 名录 → 整条跳过（白名单制宁拒勿杀，§5.4）。
	ModAcronyms []string
	// StatisticsKeys 本端 HitResult 枚举的 StatisticsJson 键名录
	//（snake_case 键形；四端同值，fork-comparison C11）。投放时目标端
	// 名录外的键过滤后写（D7）。
	StatisticsKeys []string
	// IniKeys game.ini 键策略。
	IniKeys IniKeyPolicy
}

// G0v0MarketRulesetMinOnlineID 是 g0v0 规则集市场行的 OnlineID 下界：
// OnlineID >= 该值即市场第三方规则集（g0v0-server 从 10 起编号，
// fork-g0v0 §2.1 IsBuiltinRuleset 的 < 10 判据）。这些行 ShortName 随
// 服务器市场动态变化、无法静态枚举进 OwnRulesets，adapter 按本常量过滤
//（design §5.4-D8：行与指向它的成绩一并丢弃并计数）。
const G0v0MarketRulesetMinOnlineID = 10

// decodePlain 非 Ez 端解码：磁盘版本即裸上游版本，无 fork 修订
//（fork-comparison §1.1「裸上游版本号」）。
func decodePlain(v uint64) (int, int) { return int(v), 0 }

// decodeEz Ez 版本编码解码（fork-ez2lazer §2.1 RealmAccess：
// 磁盘 = schema_version*1000 + EZ_REALM_SCHEMA_VERSION；decodeDiskSchemaVersion：
// v>=1000 时 upstream=v/1000、ez=v%1000，<1000 视为纯上游版本 ez=0）。
func decodeEz(v uint64) (int, int) {
	if v >= 1000 {
		return int(v / 1000), int(v % 1000)
	}
	return int(v), 0
}

// EncodeEzVersion 是 decodeEz 的逆（官方基线×1000 + Ez 修订），
// 供 fixture 构造 Ez 版本号库使用（§8.2）。
func EncodeEzVersion(upstream, forkRev int) uint64 {
	return uint64(upstream)*1000 + uint64(forkRev)
}

// lockedIniKeys 四端一致的红灯键（S10）：Token/Username 是 OAuth 凭据
//（D15），ReleaseStream 是每端身份配置、枚举域四端互不相同（D14）。
var lockedIniKeys = []string{"Token", "Username", "ReleaseStream"}

// officialModAcronyms 官方（ppy 系四端共同基线）mod acronym 名录：
// EZRealmSync OfficialModList.json 的 shared 集 29 项原文（fork-comparison
// D7；Ordinal 区分大小写）。各规则集专属集未在调研摘录中展开——按
// shared 全集保守过滤，宁拒勿杀（§5.4）。Ez 专有 mod（LAsMods 等）的
// acronym 取值未取得调研数据（fork-ez2lazer §9），ez2lazer 端暂随基线。
var officialModAcronyms = []string{
	"AC", "AS", "AT", "BR", "CL", "CN", "DA", "DC", "DT", "EZ", "FL", "HD",
	"HR", "HT", "MR", "MU", "NC", "NF", "NM", "NS", "PF", "RD", "RX", "SD",
	"SV2", "SY", "TD", "WD", "WU",
}

// officialStatisticsKeys 官方 HitResult 枚举的 StatisticsJson 键名录
//（snake_case 键形，lazer API 序列化约定；四端同值——delta 明确不引入
// 新 HitResult 枚举值（fork-delta §5.3），Ez/g0v0 未改该枚举，
// fork-comparison C11）。枚举成员清单不在 docs/research 摘录内，按官方
// osu.Game/Rulesets/Scoring/HitResult.cs 逐成员转写（15 项）。
var officialStatisticsKeys = []string{
	"none", "ignore", "miss",
	"small_tick_miss", "large_tick_miss", "small_tick_hit", "large_tick_hit",
	"slider_tail_hit", "small_bonus", "large_bonus",
	"ok", "meh", "great", "combo_break", "legacy_combo_increase",
}

var profiles = []ForkProfile{
	{
		// 官方 lazer：一切对比的基线（fork-comparison §0）。
		ID:            "lazer",
		DataDirName:   "osu",             // fork-comparison §1.1（debug 为 osu-development）
		RealmFilePat:  "client.realm",    // release；DEBUG 构建为 client_52.realm（§1.1）
		ProcessNames:  []string{"osu!", "osu"}, // 程序集名 "osu!"（fork-g0v0 §1.1 对照）+ EZRealmSync 守卫名单先例（ezrealmsync-design §5.5）
		DecodeVersion: decodePlain,
		DiskVersion:   52,                // fork-comparison §1.1
		ServerRealm:   "ppy",             // osu.ppy.sh
		// 目标端兼容基线（§5.4 adapter 输入）：官方 mod/HitResult 名录。
		ModAcronyms:    officialModAcronyms,
		StatisticsKeys: officialStatisticsKeys,
		IniKeys:        IniKeyPolicy{Locked: lockedIniKeys},
	},
	{
		// DeltaLazer：Realm 与官方完全同构、schema 停在 51、连官方服务器但
		// 禁用登录（成绩全落本地、无 OnlineID，fork-delta §0/§4.2-4.3）。
		ID:            "delta",
		DataDirName:   "deltalazer",   // fork-delta §1.1（debug 为 deltalazer-development）
		RealmFilePat:  "client.realm", // fork-delta §1.2
		ProcessNames:  nil,            // 可执行名未记录（fork-comparison U13/T2），守卫名单用户可扩展（§6.4）
		DecodeVersion: decodePlain,
		DiskVersion:   51, // fork-delta §2.1（唯一缺官方 v52 的 RealmOnlineAsset）
		ServerRealm:   "ppy", // 未改任何端点（fork-delta §1.3）
		// Realm 与官方同构（fork-delta §2）：mod/HitResult 名录随官方基线。
		ModAcronyms:    officialModAcronyms,
		StatisticsKeys: officialStatisticsKeys,
		IniKeys: IniKeyPolicy{
			Own: []string{ // fork 新增 6 键（fork-delta §3.1，官方不识别）
				"MenuCursorFromSkin", "MenuCursorFromSkinConfirmed", "MenuCursorFromSkinSize",
				"MenuCursorTrail", "MenuCursorScaleX", "MenuCursorScaleY",
			},
			Locked: lockedIniKeys,
		},
	},
	{
		// Ez2Lazer：磁盘 schema 52011 = 52×1000+11，主库 client_52011.realm
		//（任何构建，含 release），官方列全保留 + 10 规范列 + 7 张技能表
		//（fork-ez2lazer §0–§2）。
		ID:            "ez2lazer",
		DataDirName:   "osu-Ez2Lazer", // fork-ez2lazer §1.1（debug 为 osu-Ez2Lazer-development）
		RealmFilePat:  "client_<diskVersion>.realm", // §2.2：#if DEBUG 被注释掉，release 也带版本后缀
		ProcessNames:  []string{"Ez2Lazer"}, // EZRealmSync 守卫名单（ezrealmsync-design §5.5）
		DecodeVersion: decodeEz,
		DiskVersion:   52011, // 52×1000+11（fork-ez2lazer §2.1）
		ServerRealm:   "ppy", // 默认官方服务器（ServerPreset 官方/手动/g0v0 可切，fork-ez2lazer §4.4；静态 Profile 取默认）
		// 10 个追加规范列（fork-ez2lazer §2.4 字段级摘录；表名用 Realm 侧
		// 表名，与 fork-comparison §2-C2 白名单键同风格；HostingKindInt 的
		// [MapTo] 列名为 HostingKind，§7A / ezrealmsync-design §2.5 静态名单同款）。
		ExtColumns: map[string][]string{
			"Beatmap":    {"XxyStarRating", "PerformancePoints", "HasVideo", "HasStoryboard"},
			"Score":      {"JudgeMode", "HealthMode"},
			"BeatmapSet": {"ExternalContentRoot", "HostingKind"},
			"Ruleset":    {"LastAppliedXxySrVersion", "LastAppliedManiaSkillVersion"},
		},
		// 7 张技能系列表，全部是派生分析缓存、可由 Ez 重算（fork-ez2lazer §2.5）。
		ExtTables: []string{
			"EzBeatmapSkillValue", "EzPlayerSkillValue", "EzDanEstimate",
			"EzPlayerSkillHistoryPoint", "EzBeatmapChartSkillInfo",
			"EzPlayerDanSkillsetValue", "EzBeatmapChartDan",
		},
		// BMS 规则集在仓库内（ShortName="bms"）；diva 不在仓库、未确认
		//（fork-ez2lazer §5.2/§9），不列入。
		OwnRulesets: []string{"bms"},
		// 外部托管集 Hash 前缀（fork-ez2lazer §5.1 HASH_PREFIX）。
		ExtSetHashPrefix: "ext:set:",
		// Ez 专有皮肤指纹：EZRealmSync IsEzOnlySkin 写死的三个 GUID 之一
		//（fork-comparison D21 / ezrealmsync-design §6.2——另两个 GUID
		// 调研摘录未列明，取得后补录）。
		OwnSkins: []SkinFingerprint{
			{Guid: "fc372386381d4f8e897ac1d89ef39f9c"},
		},
		// JudgeMode/HealthMode 非 0 = 非 Lazer 判定档成绩（fork-ez2lazer
		// §2.4/§2.7 取值域；R7：官方重算会错，收集侧跳过）。
		ScoreJudgeColumns: []string{"JudgeMode", "HealthMode"},
		// Ez 继承官方 mod 体系（专有 mod acronym 未取得调研数据）。
		ModAcronyms:    officialModAcronyms,
		StatisticsKeys: officialStatisticsKeys,
		IniKeys:        IniKeyPolicy{Locked: lockedIniKeys}, // OsuSetting 枚举与官方一致（fork-ez2lazer §3.1）
	},
	{
		// g0v0!：Realm schema 与官方 52 完全一致（零结构改动），差异全在
		// 在线链路与规则集管理（fork-g0v0 §0–§2）。
		ID:            "g0v0",
		DataDirName:   "g0v0",         // fork-g0v0 §1.1（debug 为 g0v0-development）
		RealmFilePat:  "client.realm", // fork-g0v0 §1.1 CLIENT_DATABASE_FILENAME 同官方
		ProcessNames:  []string{"g0v0!"}, // 程序集名带感叹号（fork-g0v0 §1.1 <AssemblyName>g0v0!</AssemblyName>，匹配时不可去掉 !）
		DecodeVersion: decodePlain,
		DiskVersion:   52, // fork-g0v0 §2
		ServerRealm:   "g0v0.top", // 全部端点 *.g0v0.top、独立账号体系（fork-g0v0 §1.3、§7.5）
		// 市场规则集（OnlineID>=10）ShortName 动态，走常量判定；RX/AP 特殊
		// 规则集（osurx 等 OnlineID 4–7）是内存概念不落库（fork-g0v0 §2.1、§5.3）。
		OwnRulesets: nil,
		// g0v0-server 规则集市场：第三方规则集 OnlineID >= 10 落库
		//（fork-g0v0 §2.1 IsBuiltinRuleset 的 < 10 判据，D8）。
		MarketRulesetMinOnlineID: G0v0MarketRulesetMinOnlineID,
		// Realm schema 与官方全等（fork-g0v0 §2）：mod/HitResult 名录随基线
		//（RX/AP 是内存概念不落库——APIMods 为 [Ignored] 属性，D9）。
		ModAcronyms:    officialModAcronyms,
		StatisticsKeys: officialStatisticsKeys,
		IniKeys: IniKeyPolicy{
			Own: []string{ // fork 新增 11 键（fork-g0v0 §3）
				"UseRulesetSkins", "RulesetSkins", "UseV2ResultsScreen",
				"ToriiLegacyFooterUseSkin", "ToriiLegacySongSelectFooter", "ToriiStableResults",
				"ToriiLegacyFont", "UnslantedSongSelectUI", "CustomApiUrl",
				"DisableAutomaticUpdates",
				// HitsoundVolume 归属两份调研冲突（fork-delta §3.2 记为官方
				// 新增 delta 无；fork-g0v0 §3 记为 g0v0 新增官方无；U16 未确认）
				// ——至少 delta 端没有，保守不进任何端 Shared，归 g0v0.Own。
				"HitsoundVolume",
			},
			Locked: lockedIniKeys,
		},
	},
}

// All 返回全部已注册 Profile（四端：lazer / delta / ez2lazer / g0v0）。
// 返回切片是注册表的拷贝，调用方改写不影响注册表。
func All() []ForkProfile {
	out := make([]ForkProfile, len(profiles))
	copy(out, profiles)
	return out
}

// Profile 按 ID 取 Profile；不存在返回 false。
func Profile(id string) (ForkProfile, bool) {
	for _, p := range profiles {
		if p.ID == id {
			return p, true
		}
	}
	return ForkProfile{}, false
}

// ownsDir 判断目录名是否属于本 profile（release 名或 debug 变体
// <name>-development，fork-comparison §1.1 的 debug 构建目录）。
func (p ForkProfile) ownsDir(dirName string) bool {
	return dirName == p.DataDirName || dirName == p.DataDirName+"-development"
}

// matchesFingerprint 判断结构视图是否命中本 profile 的专有表/列指纹
//（fork 身份 = 版本编码 + 结构指纹，§3.2；ezrealmsync-design §6.2-2
// HasEzFingerprint 思路的泛化：任意 fork 声明的专有结构命中即认定）。
func (p ForkProfile) matchesFingerprint(v structureView) bool {
	for _, t := range p.ExtTables {
		if v.classes[t] {
			return true
		}
	}
	for cls, cols := range p.ExtColumns {
		for _, c := range cols {
			if v.props[cls][c] {
				return true
			}
		}
	}
	return false
}
