package fixture

// 四端 schema 镜像（design.md §8.2-1）：字段级定义逐列抄自
// docs/research/lazer-baseline.md §2（官方 52 基线）与
// docs/research/fork-ez2lazer.md §2.4/§2.5（Ez 10 扩展列 + 7 表）；
// delta 51 = 官方 52 − RealmOnlineAsset（fork-delta §2.2，v52 仅新增该表）；
// g0v0 52 与官方全等（fork-g0v0 §2）。
//
// 镜像忠实度受 realmc 构造面（realmc.Schema/PropertyDef）约束的边界：
//   - 列名与类型逐列忠实（磁盘名 = MapTo 名，如 StatusInt→"Status"）；
//   - 链接列 / 嵌入对象列 / 集合列（IList）不进镜像——realmc.buildSchema
//     硬编码 collectionNone + 空 link_target，无法表达；
//   - 主键不声明——realmc 未绑定 realm_object_create_with_primary_key，
//     声明主键的类经 realm_object_create 播种会被拒（errno=3005，
//     本包施工期探针实测）；
//   - 可空标志不表达（realmc.PropertyDef 无 nullable 位）。

import (
	"fmt"

	"lzsync/internal/realmc"
)

// realmc 未导出的属性类型常量，按 realm.h:260-275 补齐镜像所需：
// RLM_PROPERTY_TYPE_FLOAT=9 / DOUBLE=10 / UUID=17。
const (
	PropTypeDouble realmc.PropType = 10
	PropTypeUUID   realmc.PropType = 17
)

// 四端 fork ID 与各自唯一可构造的磁盘 schema 版本。
const (
	ForkLazer    = "lazer"
	ForkDelta    = "delta"
	ForkEz2Lazer = "ez2lazer"
	ForkG0v0     = "g0v0"

	VersionLazer  = uint64(52)
	VersionDelta  = uint64(51)
	VersionEzDisk = uint64(52011) // 52×1000+11（fork-ez2lazer §2.1）
	VersionG0v0   = uint64(52)
)

// cl / p 是紧凑构造面（go 无助于逐行表格的长度）。
type cl = realmc.ClassDef
type p = realmc.PropertyDef

func pi(name string) p { return p{Name: name, Type: realmc.PropTypeInt} }
func pb(name string) p { return p{Name: name, Type: realmc.PropTypeBool} }
func ps(name string) p { return p{Name: name, Type: realmc.PropTypeString} }
func pt(name string) p { return p{Name: name, Type: realmc.PropTypeTimestamp} }
func pd(name string) p { return p{Name: name, Type: PropTypeDouble} }
func pu(name string) p { return p{Name: name, Type: PropTypeUUID} }

// officialClasses 返回官方基线的表定义（lazer-baseline §2 逐列抄录；
// 磁盘列名 = MapTo 名）。withOnlineAsset 控制仅 v52 存在的 RealmOnlineAsset。
func officialClasses(withOnlineAsset bool) []cl {
	classes := []cl{
		{Name: "File", Properties: []p{ps("Hash")}}, // §2.1 RealmFile
		{Name: "BeatmapSet", Properties: []p{ // §2.4
			pu("ID"), pi("OnlineID"), pt("DateAdded"), pt("DateSubmitted"), pt("DateRanked"),
			pi("Status"), pb("DeletePending"), ps("Hash"), pb("Protected"),
		}},
		{Name: "Beatmap", Properties: []p{ // §2.5
			pu("ID"), ps("DifficultyName"), pi("Status"), pi("OnlineID"),
			pd("Length"), pd("BPM"), ps("Hash"), pd("StarRating"), ps("MD5Hash"), ps("OnlineMD5Hash"),
			pt("LastLocalUpdate"), pt("LastOnlineUpdate"), pb("Hidden"),
			pi("EndTimeObjectCount"), pi("TotalObjectCount"), pt("LastPlayed"),
			pi("BeatDivisor"), pd("EditorTimestamp"),
		}},
		{Name: "BeatmapMetadata", Properties: []p{ // §2.6（UserTags 是集合列不进镜像）
			ps("Title"), ps("TitleUnicode"), ps("Artist"), ps("ArtistUnicode"),
			ps("Source"), ps("Tags"), pi("PreviewTime"), ps("AudioFile"), ps("BackgroundFile"),
		}},
		{Name: "Score", Properties: []p{ // §2.7（Pauses 是集合列不进镜像）
			pu("ID"), ps("ClientVersion"), ps("BeatmapHash"), ps("Hash"), pb("DeletePending"),
			pi("TotalScore"), pi("TotalScoreWithoutMods"), pi("TotalScoreVersion"), pi("LegacyTotalScore"),
			pb("BackgroundReprocessingFailed"), pi("MaxCombo"), pd("Accuracy"), pt("Date"), pd("PP"),
			pi("OnlineID"), pi("LegacyOnlineID"), ps("Mods"), ps("Statistics"), ps("MaximumStatistics"),
			pi("Rank"), pi("Combo"), pb("IsLegacyScore"),
		}},
		{Name: "BeatmapCollection", Properties: []p{ // §2.8（BeatmapMD5Hashes 集合列不进镜像）
			pu("ID"), ps("Name"), pt("LastModified"),
		}},
		{Name: "Ruleset", Properties: []p{ // §2.9
			ps("ShortName"), pi("OnlineID"), ps("Name"), ps("InstantiationInfo"),
			pi("LastAppliedDifficultyVersion"), pb("Available"),
		}},
		{Name: "Skin", Properties: []p{ // §2.10
			pu("ID"), ps("Name"), ps("Creator"), ps("InstantiationInfo"), ps("Hash"),
			pb("Protected"), pb("DeletePending"),
		}},
		{Name: "ModPreset", Properties: []p{ // §2.11
			pu("ID"), ps("Name"), ps("Description"), ps("Mods"), pb("DeletePending"),
		}},
		{Name: "KeyBinding", Properties: []p{ // §2.12
			pu("ID"), ps("RulesetName"), pi("Variant"), pi("Action"), ps("KeyCombination"),
		}},
		{Name: "RulesetSetting", Properties: []p{ // §2.13
			ps("RulesetName"), pi("Variant"), ps("Key"), ps("Value"),
		}},
	}
	if withOnlineAsset {
		classes = append(classes, cl{Name: "RealmOnlineAsset", Properties: []p{ // §2.14（v52；File 列是嵌入不进镜像）
			pt("LastAccessed"),
		}})
	}
	return classes
}

// applyEzExtensions 把 Ez 的 10 扩展列 + 7 张技能表并入官方基线
// （fork-ez2lazer §2.4/§2.5；HostingKindInt 的 MapTo 磁盘名 = "HostingKind"）。
func applyEzExtensions(classes []cl) []cl {
	byName := make(map[string]*cl, len(classes))
	for i := range classes {
		byName[classes[i].Name] = &classes[i]
	}
	byName["Beatmap"].Properties = append(byName["Beatmap"].Properties,
		pd("XxyStarRating"), pd("PerformancePoints"), pb("HasVideo"), pb("HasStoryboard"))
	byName["Score"].Properties = append(byName["Score"].Properties,
		pi("JudgeMode"), pi("HealthMode"))
	byName["BeatmapSet"].Properties = append(byName["BeatmapSet"].Properties,
		ps("ExternalContentRoot"), pi("HostingKind"))
	byName["Ruleset"].Properties = append(byName["Ruleset"].Properties,
		pi("LastAppliedXxySrVersion"), pi("LastAppliedManiaSkillVersion"))

	return append(classes, []cl{
		{Name: "EzBeatmapSkillValue", Properties: []p{ // §2.5
			pu("ID"), ps("BeatmapHash"), pu("BeatmapId"), ps("SystemId"), ps("SkillId"),
			pd("Value"), pi("AlgorithmVersion"), pt("ComputedAt"),
		}},
		{Name: "EzPlayerSkillValue", Properties: []p{
			pu("ID"), ps("Username"), pi("KeyCount"), ps("SystemId"), ps("SkillId"),
			pd("Value"), pi("AnalyzedPlays"), pb("Provisional"), pb("Stale"),
			pi("AlgorithmVersion"), pt("ComputedAt"),
		}},
		{Name: "EzDanEstimate", Properties: []p{
			pu("ID"), ps("Username"), pi("KeyCount"), ps("Side"), pd("RawDan"), ps("Label"),
			pi("Clears"), pb("BeyondTable"), ps("CourseName"), pd("CourseAccuracy"),
			pi("ClearWindowHave"), pi("ClearWindowNeed"), pi("AlgorithmVersion"), pt("ComputedAt"),
		}},
		{Name: "EzPlayerSkillHistoryPoint", Properties: []p{
			pu("ID"), ps("Username"), pi("KeyCount"), ps("SkillId"), pd("Value"),
			pt("RecordedAt"), pi("AlgorithmVersion"),
		}},
		{Name: "EzBeatmapChartSkillInfo", Properties: []p{
			pu("ID"), ps("BeatmapHash"), pu("BeatmapId"), pi("InfoVersion"), pt("ComputedAt"),
			ps("PatternTagsJoined"), pb("JackDemand"), pd("JackShare"), pd("StreamShare"),
			pb("TechCategory"), pb("ClusterTrill"), pb("HandstreamCluster"), pb("HandstreamEndurance"),
			pd("TechScore"), pd("ChordjackScore"), pd("JackScore"),
			pd("MotionRhythmBreak"), pd("MotionCrossHandTrill"), pd("MotionMiniJack"), pd("MotionSameHand"),
			pd("LnRatio"), pb("Vibro"), pb("DanEligible"), pd("LengthSeconds"), pi("KeyCount"),
		}},
		{Name: "EzPlayerDanSkillsetValue", Properties: []p{
			pu("ID"), ps("Username"), pi("KeyCount"), ps("Side"), ps("SkillsetId"),
			pd("RawDan"), ps("Label"), pi("Clears"), pi("AlgorithmVersion"), pt("ComputedAt"),
		}},
		{Name: "EzBeatmapChartDan", Properties: []p{
			pu("ID"), ps("BeatmapHash"), pu("BeatmapId"), pi("AlgorithmVersion"), pi("KeyCount"),
			pd("HoldRatio"), pd("OverallMsd"), pd("RcRawDan"), ps("RcLabel"),
			pd("LnRawDan"), ps("LnLabel"), ps("RcSkillsetLabelsJoined"), ps("LnSkillsetLabelsJoined"),
			pt("ComputedAt"),
		}},
	}...)
}

// SchemaFor 返回 (fork, version) 的 schema 镜像。镜像档案现存四份：
// lazer 52 / delta 51 / ez2lazer 52011 / g0v0 52，其余组合报错。
func SchemaFor(forkID string, schemaVersion uint64) (*realmc.Schema, error) {
	var classes []cl
	switch forkID {
	case ForkLazer, ForkG0v0:
		if schemaVersion != VersionLazer {
			return nil, fmt.Errorf("fixture: %s 镜像仅支持 schema 版本 %d，got %d", forkID, VersionLazer, schemaVersion)
		}
		classes = officialClasses(true)
	case ForkDelta:
		if schemaVersion != VersionDelta {
			return nil, fmt.Errorf("fixture: %s 镜像仅支持 schema 版本 %d，got %d", forkID, VersionDelta, schemaVersion)
		}
		classes = officialClasses(false) // v52 的 RealmOnlineAsset 不在（fork-delta §2.2）
	case ForkEz2Lazer:
		if schemaVersion != VersionEzDisk {
			return nil, fmt.Errorf("fixture: %s 镜像仅支持 schema 版本 %d，got %d", forkID, VersionEzDisk, schemaVersion)
		}
		classes = applyEzExtensions(officialClasses(true))
	default:
		return nil, fmt.Errorf("fixture: 未知 fork %q", forkID)
	}
	return &realmc.Schema{Classes: classes}, nil
}

// decodeVersion 是 forks.DecodeVersion 的镜像实现（forks 包尚为桩，fixture
// 自产 LibraryRef 需要；Ez 编码规则 fork-ez2lazer §2.1）。
func decodeVersion(forkID string, v uint64) (upstream int, forkRev int) {
	if forkID == ForkEz2Lazer && v >= 1000 {
		return int(v / 1000), int(v % 1000)
	}
	return int(v), 0
}

// realmFileName 返回该 fork 实际打开的主库文件名（fork-ez2lazer §2.2：
// Ez 任何构建都带版本后缀；官方 release 用 client.realm）。
func realmFileName(forkID string, diskVersion uint64) string {
	if forkID == ForkEz2Lazer {
		return fmt.Sprintf("client_%d.realm", diskVersion)
	}
	return "client.realm"
}
