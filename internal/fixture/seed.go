package fixture

// 种子数据构造（design.md §8.2-3「自洽图」）：任意字节充当 .osu/.osr/音频，
// SHA-256/MD5 按官方算法定义现场计算（lazer-baseline §6.1）——内容寻址
// 不要求「能玩的谱面」，只要求哈希与行一致。差异夹具按 §8.2-4 清单提供。

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"lzsync/internal/hub"
)

// 官方四规则集固定行（lazer-baseline §2.9；ShortName 取官方现值
// osu/taiko/fruits/mania，fork-g0v0 §2 常量与 fruits 一致）。
type builtinRuleset struct {
	shortName         string
	onlineID          int64
	name              string
	instantiationInfo string
}

var builtinRulesets = []builtinRuleset{
	{"osu", 0, "osu!", "osu.Game.Rulesets.Osu.OsuRuleset, osu.Game.Rulesets.Osu"},
	{"taiko", 1, "osu!taiko", "osu.Game.Rulesets.Taiko.TaikoRuleset, osu.Game.Rulesets.Taiko"},
	{"fruits", 2, "osu!catch", "osu.Game.Rulesets.Catch.CatchRuleset, osu.Game.Rulesets.Catch"},
	{"mania", 3, "osu!mania", "osu.Game.Rulesets.Mania.ManiaRuleset, osu.Game.Rulesets.Mania"},
}

// 内置皮肤固定 Guid（lazer-baseline §2.10 原文；同步时必须保留不可随机化）。
// Name/Creator 摘录未给出确证值，取工程标识作占位（Guid 才是指纹锚点）。
var builtinSkins = []struct {
	guid string
	name string
}{
	{"2991cfD8-2140-469A-BCB9-2EC23FBCE4AD", "triangles"},
	{"CFFA69DE-B3E3-4DEE-8563-3C4F425C05D0", "argon"},
	{"9FC9CF5D-0F16-4C71-8256-98868321AC43", "argon-pro"},
	{"81F02CD3-EEC6-4865-AC23-FAE26A386187", "classic"},
	{"0555C76A-CC6B-4BB4-9548-DF76BA72EF25", "retro"},
	{"D39DFEFB-477C-4372-B1EA-2BCEA5FB8908", "random"},
}

const builtinSkinCreator = "osu! team"

// scoreTotalScoreVersionLatest = LegacyScoreEncoder.LATEST_VERSION
// （lazer-baseline §2.7 默认值 30000019）。
const scoreTotalScoreVersionLatest = int64(30000019)

// extSetHashPrefix 是 Ez 外部托管集 Hash 前缀（fork-ez2lazer §5.1）。
const extSetHashPrefix = "ext:set:"

// seedTimeBase 种子时间基准（确定性常量，避开本地时钟差异；全列时间列
// 由此派生，逐字段断言可复现）。
var seedTimeBase = time.UnixMilli(1770000000000).UTC()

// skinInstantiationInfoLegacySkin 用户皮肤默认 InstantiationInfo（空则
// lazer 按 LegacySkin 实例化，lazer-baseline §2.10——取显式值便于逐字段
// 断言非零）。
const skinInstantiationInfoLegacySkin = "osu.Game.Skinning.DefaultLegacySkin, osu.Game"

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func md5Hex(b []byte) string {
	// mimosa-ignore -- osu! 谱面格式规定 MD5 内容哈希（与 Realm 列值逐字节一致），非安全用途，已裁定误报
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// DiffMaterial 一个难度的原料：名字 + 任意字节当 .osu 内容。
type DiffMaterial struct {
	Name     string
	OsuBytes []byte
}

// MaterializeSet 把原料固化为自洽 DTO（C2 白名单全列，§8.2-3）：难度
// Hash = .osu SHA-256、MD5Hash = .osu 原始字节 MD5、集 Hash = 集内全部
// .osu 按文件名排序拼接的 SHA-256（lazer-baseline §6.1）。全列取确定性
// 派生值（seedTimeBase 基准 + 难度序号偏移，float 取二进制可精确表示的
// 离散值），收集往返逐字段断言据此可复现。新文件字节并入 files（哈希 →
// 字节）。
func MaterializeSet(onlineID int64, artist, title, author string, diffs []DiffMaterial, audio []byte, files map[string][]byte) (hub.BeatmapSet, error) {
	if len(diffs) == 0 {
		return hub.BeatmapSet{}, fmt.Errorf("fixture: 谱面集至少需要一个难度")
	}
	submitted := seedTimeBase.Add(12 * time.Hour)
	ranked := seedTimeBase.Add(30 * 24 * time.Hour)
	set := hub.BeatmapSet{
		OnlineID:       onlineID,
		Artist:         artist,
		Title:          title,
		AuthorUsername: author,
		StatusInt:      1, // BeatmapOnlineStatus.Ranked（lazer-baseline §5）
		DateAdded:      seedTimeBase,
		DateSubmitted:  &submitted,
		DateRanked:     &ranked,
	}
	// 集哈希 = 全部 .osu 按文件名排序后顺序拼接的 SHA-256
	// （RealmArchiveModelImporter.ComputeHash 语义，lazer-baseline §6.1）。
	byName := make([]DiffMaterial, len(diffs))
	copy(byName, diffs)
	sort.Slice(byName, func(i, j int) bool { return byName[i].Name < byName[j].Name })
	concat := make([]byte, 0)
	for _, d := range byName {
		concat = append(concat, d.OsuBytes...)
	}
	set.Hash = sha256Hex(concat)

	for i, d := range diffs {
		md5 := md5Hex(d.OsuBytes)
		localUpdate := seedTimeBase.Add(time.Duration(i) * 24 * time.Hour)
		onlineUpdate := seedTimeBase.Add(time.Duration(i)*24*time.Hour + time.Hour)
		played := seedTimeBase.Add(time.Duration(i) * 48 * time.Hour)
		editorTS := 5000.5 + float64(i)
		bm := hub.Beatmap{
			Hash:               sha256Hex(d.OsuBytes),
			MD5Hash:            md5,
			OnlineMD5Hash:      md5, // 本地与在线版本一致（MatchesOnlineVersion）
			DifficultyName:     d.Name,
			RulesetShortName:   "osu",
			StatusInt:          1, // Ranked（lazer-baseline §5）
			OnlineID:           int(onlineID)*10 + i,
			Length:             62000.5 + float64(i)*1000,
			BPM:                174.25 + float64(i)*7.5,
			StarRating:         2.75 + float64(i)*1.25,
			LastLocalUpdate:    &localUpdate,
			LastOnlineUpdate:   &onlineUpdate,
			EndTimeObjectCount: 100 + i,
			TotalObjectCount:   620 + i,
			LastPlayed:         &played,
			BeatDivisor:        4,
			EditorTimestamp:    &editorTS,
			Difficulty: hub.Difficulty{
				DrainRate:         4.5 + float32(i)*0.25,
				CircleSize:        3.5 + float32(i)*0.5,
				OverallDifficulty: 7.25 + float32(i)*0.25,
				ApproachRate:      8.5 - float32(i)*0.25,
				SliderMultiplier:  1.64,
				SliderTickRate:    1.9,
			},
			Metadata: hub.Metadata{
				Title:          title,
				TitleUnicode:   title,
				Artist:         artist,
				ArtistUnicode:  artist,
				Author:         hub.RealmUser{OnlineID: 100042, Username: author, CountryString: "JP"},
				Source:         "LZSync Fixture Source",
				Tags:           "lzsync fixture tags",
				UserTags:       []string{"tag-a", "tag-b"},
				PreviewTime:    12345,
				AudioFile:      "audio.mp3",
				BackgroundFile: "bg.jpg",
			},
			UserSettingsOffset: -12.5 + float64(i)*2.5,
		}
		set.Beatmaps = append(set.Beatmaps, bm)
		set.Files = append(set.Files, hub.NamedFile{Hash: bm.Hash, Filename: fmt.Sprintf("%s.osu", d.Name)})
		files[bm.Hash] = d.OsuBytes
	}
	if audio != nil {
		audioHash := sha256Hex(audio)
		set.Files = append(set.Files, hub.NamedFile{Hash: audioHash, Filename: "audio.mp3"})
		files[audioHash] = audio
	}
	return set, nil
}

// SkinFileMaterial 皮肤文件原料：文件名 + 任意字节。哈希只并入
// HashableFileTypes（.ini/.json，lazer-baseline §2.10）的文件。
type SkinFileMaterial struct {
	Filename string
	Bytes    []byte
}

// MaterializeSkin 把皮肤原料固化为自洽种子行：Hash = 全部 .ini/.json 文件
// 按文件名排序拼接的 SHA-256（SkinImporter 语义，lazer-baseline §2.10）；
// 全部文件（含非哈希型，如 .png）进 Files 与字节池。
func MaterializeSkin(name, creator string, mats []SkinFileMaterial, files map[string][]byte) (SeedSkin, error) {
	if len(mats) == 0 {
		return SeedSkin{}, fmt.Errorf("fixture: 皮肤至少需要一个文件")
	}
	byName := make([]SkinFileMaterial, len(mats))
	copy(byName, mats)
	sort.Slice(byName, func(i, j int) bool { return byName[i].Filename < byName[j].Filename })
	concat := make([]byte, 0)
	sk := SeedSkin{
		Name:              name,
		Creator:           creator,
		InstantiationInfo: skinInstantiationInfoLegacySkin,
	}
	for _, m := range byName {
		sk.Files = append(sk.Files, hub.NamedFile{Hash: sha256Hex(m.Bytes), Filename: m.Filename})
		if strings.HasSuffix(m.Filename, ".ini") || strings.HasSuffix(m.Filename, ".json") {
			concat = append(concat, m.Bytes...)
		}
		files[sha256Hex(m.Bytes)] = m.Bytes
	}
	sk.Hash = sha256Hex(concat)
	return sk, nil
}

// EncodeExtSetHash 计算 Ez 外部托管集 Hash："ext:set:" + Base64(UTF-8(规范化
// 绝对路径))（fork-ez2lazer §5.1 ExternalBeatmapPathEncoding）。
func EncodeExtSetHash(contentRoot string) string {
	return extSetHashPrefix + base64.StdEncoding.EncodeToString([]byte(contentRoot))
}

// PathRealmFileHash 计算 BMS 外部谱面难度文件的 RealmFile 路径哈希：
// SHA256(lowercase(规范化全路径))——不哈希内容（fork-ez2lazer §5.1）。
func PathRealmFileHash(fullPath string) string {
	return sha256Hex([]byte(toLowerNormalized(fullPath)))
}

func toLowerNormalized(p string) string {
	b := []byte(p)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// StandardSeed 四端共底种子：1 个谱面集（2 难度 + 音频，全列）+ 1 个收藏夹
// （按 MD5 追踪，lazer-baseline §2.8）+ 1 条本地成绩（全列）。皮肤不入
// 共底种子——deploy 等既有用例以 Files=nil 收敛夹具面，皮肤种子由收集
// 用例自带（MaterializeSkin）。
func StandardSeed() (Seed, error) {
	files := map[string][]byte{}
	set, err := MaterializeSet(1001, "Fixture Artist", "Fixture Title", "mapper",
		[]DiffMaterial{
			{Name: "Normal", OsuBytes: []byte("osu file format v14\n\n[Difficulty]\nHPDrainRate:4\n")},
			{Name: "Another", OsuBytes: []byte("osu file format v14\n\n[Difficulty]\nHPDrainRate:6\n")},
		},
		[]byte("fixture-audio-bytes"), files)
	if err != nil {
		return Seed{}, err
	}
	replay := []byte("fixture-replay-bytes")
	score, err := MaterializeScore(set.Beatmaps[0], "lazer 2026.1001.0", replay, files)
	if err != nil {
		return Seed{}, err
	}
	score.TotalScore = 9876543
	score.MaxCombo = 731
	score.RankInt = 2 // ScoreRank A
	score.OnlineID = -1
	score.StatisticsJson = `{"great":950,"ok":37,"meh":12,"miss":1}`
	score.MaximumStatisticsJson = `{"great":1000}`
	return Seed{
		BeatmapSets: []hub.BeatmapSet{set},
		Collections: []hub.Collection{{
			Name:             "标准收藏夹",
			BeatmapMD5Hashes: []string{set.Beatmaps[0].MD5Hash, set.Beatmaps[1].MD5Hash},
		}},
		Scores: []SeedScore{score},
		Files:  files,
	}, nil
}

// VersionChangeSeeds 版本变更夹具（HB1 版本链用例输入）：同 OnlineID 的
// v1/v2 两份种子，难度 .osu 内容不同 → Hash 全链不同。
func VersionChangeSeeds() (Seed, Seed, error) {
	files := map[string][]byte{}
	v1, err := MaterializeSet(2002, "Version Artist", "Version Title", "mapper",
		[]DiffMaterial{{Name: "Only", OsuBytes: []byte("osu file format v14\nv1\n")}}, nil, files)
	if err != nil {
		return Seed{}, Seed{}, err
	}
	files2 := map[string][]byte{}
	v2, err := MaterializeSet(2002, "Version Artist", "Version Title", "mapper",
		[]DiffMaterial{{Name: "Only", OsuBytes: []byte("osu file format v14\nv2-edited\n")}}, nil, files2)
	if err != nil {
		return Seed{}, Seed{}, err
	}
	return Seed{BeatmapSets: []hub.BeatmapSet{v1}, Files: files},
		Seed{BeatmapSets: []hub.BeatmapSet{v2}, Files: files2}, nil
}

// DeletionSeeds 删除夹具（HB2 tombstone 用例输入）：before 含两个集，
// after 缺第二个（模拟游戏侧删除后再次收集）。
func DeletionSeeds() (Seed, Seed, error) {
	files := map[string][]byte{}
	keep, err := MaterializeSet(3003, "Keep Artist", "Keep Title", "mapper",
		[]DiffMaterial{{Name: "Keep", OsuBytes: []byte("keep osu bytes")}}, nil, files)
	if err != nil {
		return Seed{}, Seed{}, err
	}
	gone, err := MaterializeSet(3004, "Gone Artist", "Gone Title", "mapper",
		[]DiffMaterial{{Name: "Gone", OsuBytes: []byte("gone osu bytes")}}, nil, files)
	if err != nil {
		return Seed{}, Seed{}, err
	}
	before := Seed{BeatmapSets: []hub.BeatmapSet{keep, gone}, Files: files}
	after := Seed{BeatmapSets: []hub.BeatmapSet{keep}, Files: files}
	return before, after, nil
}

// gimmickOsu delta gimmick section 的 .osu 样例（fork-delta §5.1 测试用例
// 原文行；格式 Id,StartTime,EndTime,Key=Value 串）。
const gimmickOsu = `osu file format v14

[Metadata]
Title:Gimmick Fixture

[BeatmapSectionGimmicks]
0,0,1500,EnableHPGimmick=True|NoDrain=True|HP300=-0.02|HP100=-0.05|HP50=0.08|HPMiss=-0.12|EnableNoMiss=True
`

// hitObjectGimmickOsu 物件级 gimmick 的 .osu 样例（fork-delta §5.1 行格式
// {StartTime},{ComboIndexWithOffsets},[ObjectId=|]{Key=Value}；字段取自
// HitObjectGimmickSettings）。
const hitObjectGimmickOsu = `osu file format v14

[Metadata]
Title:Gimmick Fixture

[BeatmapHitObjectGimmicks]
1000.5,2,EnableCountLimits=True|EnableNoMiss=True
500,1,ObjectId=42,EnableGreatOffsetPenalty=True
`

// DeltaGimmickSeed delta 夹具：标准种子 + 一个含两类 gimmick section 的
// 谱面集（各一难度；数据全在 .osu 文件内，不进库表——fork-delta §5.1）。
func DeltaGimmickSeed() (Seed, error) {
	seed, err := StandardSeed()
	if err != nil {
		return Seed{}, err
	}
	files := seed.Files
	g, err := MaterializeSet(4004, "Delta Artist", "Gimmick Set", "mapper",
		[]DiffMaterial{
			{Name: "Gimmick", OsuBytes: []byte(gimmickOsu)},
			{Name: "Fake", OsuBytes: []byte(hitObjectGimmickOsu)},
		}, nil, files)
	if err != nil {
		return Seed{}, err
	}
	seed.BeatmapSets = append(seed.BeatmapSets, g)
	seed.Files = files
	return seed, nil
}

// EzDiffSeed Ez 差异夹具（design §8.2-4）：JudgeMode≠0 成绩、未知
// HitResult 键的 StatisticsJson、非官方 mod、外部托管 set、7 张技能表
// 演示行、Ez 专有皮肤指纹行。
func EzDiffSeed(externalRoot string) (Seed, error) {
	seed, err := StandardSeed()
	if err != nil {
		return Seed{}, err
	}
	files := seed.Files
	bms, err := MaterializeSet(5005, "Ez Artist", "BMS Set", "mapper",
		[]DiffMaterial{{Name: "BMS", OsuBytes: []byte("bms chart placeholder bytes")}},
		[]byte("bms audio bytes"), files)
	if err != nil {
		return Seed{}, err
	}
	// bms 集难度走 bms 规则集（难度级 OwnRulesets 过滤夹具，D6）。
	bms.Beatmaps[0].RulesetShortName = "bms"
	seed.BeatmapSets = append(seed.BeatmapSets, bms)
	seed.Files = files

	// bms Ruleset 行（Ez 允许外部规则集取 -1，fork-ez2lazer §5.3）+
	// 指向它的成绩（成绩级过滤夹具）。
	seed.ExtraRulesets = []SeedRuleset{{
		ShortName:         "bms",
		OnlineID:          -1,
		Name:              "Beatmap Spectrum",
		InstantiationInfo: "osu.Game.Rulesets.BMS.BMSRuleset, osu.Game.Rulesets.BMS",
	}}
	bmsScore, err := MaterializeScore(seed.BeatmapSets[0].Beatmaps[0], "ez2lazer 52011",
		[]byte("bms replay bytes"), seed.Files)
	if err != nil {
		return Seed{}, err
	}
	bmsScore.RulesetShortName = "bms"
	bmsScore.TotalScore = 888888
	seed.Scores = append(seed.Scores, bmsScore)

	// JudgeMode≠0（EZ2AC=31，fork-ez2lazer §2.7）+ 未知键 + 非官方 mod。
	// 排在首位：动态重开断言经 FirstObject 取行。规则集显式归官方 osu
	//（宿主难度是 bms——R7 夹具要求「官方规则集 × 非 Lazer 判定」形态，
	// 与 bms 规则集夹具（D6）互不遮蔽）。
	judge, err := MaterializeScore(bms.Beatmaps[0], "ez2lazer 52011", []byte("ez judge replay"), seed.Files)
	if err != nil {
		return Seed{}, err
	}
	judge.RulesetShortName = "osu"
	judge.TotalScore = 1234567
	judge.RankInt = 1 // SS
	judge.JudgeMode = 31
	judge.HealthMode = 34 // Ez2Ac
	// StatisticsJson：官方已知键 great/legacy_combo_increase（HitResult
	// 枚举成员 LegacyComboIncrease 的 snake_case 键形）+ 未知键
	// ez_unknown_result（BMS 自定义判定名，D7 过滤夹具）。
	judge.StatisticsJson = `{"great":900,"ez_unknown_result":42,"legacy_combo_increase":7}`
	judge.ModsJson = `[{"acronym":"EZX","settings":{}}]`
	seed.Scores = append([]SeedScore{judge}, seed.Scores...)

	seed.ExternalSets = []ExternalSetSpec{{
		OnlineID:    5006,
		ContentRoot: externalRoot,
	}}
	seed.EzSkillRows = true
	// Ez 专有皮肤指纹行：Guid 固定为 EZRealmSync IsEzOnlySkin 的专有皮肤
	// 常量（fork-comparison D21），Name/Creator 只是行内容——判定走
	// forks.OwnSkins 的 Guid 通道。
	ezSkin, err := MaterializeSkin("Ez Default", "Ez2Lazer", []SkinFileMaterial{
		{Filename: "ez-default.ini", Bytes: []byte("[General]\nName:Ez Default\n")},
	}, seed.Files)
	if err != nil {
		return Seed{}, err
	}
	ezSkin.Guid = "fc372386-381d-4f8e-897a-c1d89ef39f9c"
	seed.Skins = append(seed.Skins, ezSkin)
	return seed, nil
}

// G0v0DiffSeed g0v0 差异夹具（design §8.2-4）：市场规则集行（OnlineID≥10，
// fork-g0v0 §2.1）+ 指向它的成绩（PP 等 double 列已随 M2 全列播种生效）。
func G0v0DiffSeed() (Seed, error) {
	seed, err := StandardSeed()
	if err != nil {
		return Seed{}, err
	}
	seed.ExtraRulesets = []SeedRuleset{{
		ShortName:         "g0v0-custom",
		OnlineID:          10,
		Name:              "g0v0 Market Ruleset",
		InstantiationInfo: "osu.Game.Rulesets.G0v0Custom.G0v0CustomRuleset, osu.Game.Rulesets.G0v0Custom",
	}}
	market, err := MaterializeScore(seed.BeatmapSets[0].Beatmaps[0], "g0v0 52", []byte("g0v0 market replay"), seed.Files)
	if err != nil {
		return Seed{}, err
	}
	market.TotalScore = 555555
	market.RankInt = 3
	market.OnlineID = 99001
	market.RulesetShortName = "g0v0-custom"
	market.StatisticsJson = `{"great":800,"ok":10,"miss":0}`
	// 排在首位：动态重开断言经 FirstObject 取行。
	seed.Scores = append([]SeedScore{market}, seed.Scores...)
	return seed, nil
}

// MaterializeScore 把回放字节固化为自洽成绩行（C2 白名单全列）：Hash =
// 全部 .osr 内容拼接 SHA-256（lazer-baseline §6.1/§6.2）、BeatmapHash =
// 打分时难度的 Hash。User/Pauses/Accuracy/PP 取确定性默认（PP 非空 +
// LegacyTotalScore 空，一次覆盖 nullable 两态）；调用方覆盖业务列
// （TotalScore/RankInt 等）。Files = 每个 ReplayHashes 一条 .osr usage。
func MaterializeScore(bm hub.Beatmap, clientVersion string, replay []byte, files map[string][]byte) (SeedScore, error) {
	if bm.Hash == "" {
		return SeedScore{}, fmt.Errorf("fixture: 成绩挂接的难度缺 Hash")
	}
	replayHash := sha256Hex(replay)
	files[replayHash] = replay
	pp := 271.828
	return SeedScore{
		BeatmapHash:       bm.Hash,
		RulesetShortName:  bm.RulesetShortName,
		ClientVersion:     clientVersion,
		Hash:              replayHash,
		TotalScoreVersion: scoreTotalScoreVersionLatest,
		OnlineID:          -1,
		LegacyOnlineID:    -1,
		Accuracy:          0.9825,
		Date:              seedTimeBase.Add(3 * time.Hour),
		PP:                &pp,
		User:              hub.RealmUser{OnlineID: 1, Username: "fixture-player", CountryString: "JP"},
		Pauses:            []int32{1500, 61500},
		ReplayHashes:      []string{replayHash},
	}, nil
}

// SettingsSeed M3 设置类种子（§5.1-M3 / §8.2-4 扩展承载）：键位（全局
// null 键段 / 规则集级 / mania 4K/7K 多变体 + fork 专有规则集 bms 行）、
// 规则集设置（官方键 + mania 变体键 + fork 专有 bms 键）、ModPreset（官方
// osu/mania 各一 + fork 专有 bms 预设 + 名录外 mod 预设 EZX（D7）+ 软删
// 预设（S4 收集侧跳过夹具））。bms 行配 ExtraRulesets 的 bms Ruleset 行
// 装配（EzDiffSeed 先例）；Guid 取确定性常量（往返断言锚点）。
// 键位全收（hub 保留原文，D20 过滤在投放侧）；fork 专有键的跳过分支由
// 投放测试消费。
func SettingsSeed() (Seed, error) {
	mania4, mania7 := 4, 7
	return Seed{
		KeyBindings: []SeedKeyBinding{
			{Guid: "3f8b7a10-0001-4a01-9001-000000000001", RulesetName: "", Variant: nil,
				ActionInt: 100, KeyCombination: "Ctrl,Shift+Z"}, // 全局键位（null 键段）
			{Guid: "3f8b7a10-0002-4a01-9001-000000000002", RulesetName: "osu", Variant: nil,
				ActionInt: 7, KeyCombination: "Z"},
			{Guid: "3f8b7a10-0003-4a01-9001-000000000003", RulesetName: "mania", Variant: &mania4,
				ActionInt: 12, KeyCombination: "F"},
			{Guid: "3f8b7a10-0004-4a01-9001-000000000004", RulesetName: "mania", Variant: &mania7,
				ActionInt: 12, KeyCombination: "D,F,J,K"}, // 同 ActionInt 不同变体 = 不同内容键
			{Guid: "3f8b7a10-0005-4a01-9001-000000000005", RulesetName: "bms", Variant: nil,
				ActionInt: 3, KeyCombination: "Space"}, // fork 专有规则集键位（字符串列不解析，随行带走）
		},
		RulesetSettings: []SeedRulesetSetting{
			{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"bottomleft"`},
			{RulesetName: "mania", Variant: 4, Key: "speed_change_style", Value: "1"},
			{RulesetName: "bms", Variant: 0, Key: "bms_scroll_speed", Value: "25"}, // fork 专有键
		},
		ModPresets: []SeedModPreset{
			{Guid: "5c1d2e30-0001-4b02-a001-000000000001", RulesetShortName: "osu",
				Name: "Score Preset", Description: "for score runs",
				ModsJson: `[{"acronym":"HD","settings":{}}]`},
			{Guid: "5c1d2e30-0002-4b02-a001-000000000002", RulesetShortName: "mania",
				Name: "Key7 Preset", Description: "",
				ModsJson: `[{"acronym":"DT","settings":{}}]`},
			{Guid: "5c1d2e30-0003-4b02-a001-000000000003", RulesetShortName: "bms",
				Name: "BMS Preset", ModsJson: `[{"acronym":"NM","settings":{}}]`}, // fork 专有规则集（投放跳过夹具）
			{Guid: "5c1d2e30-0004-4b02-a001-000000000004", RulesetShortName: "osu",
				Name: "Exotic Preset", ModsJson: `[{"acronym":"EZX","settings":{}}]`}, // 名录外 mod（D7 跳过夹具）
			{Guid: "5c1d2e30-0005-4b02-a001-000000000005", RulesetShortName: "osu",
				Name: "Deleted Preset", DeletePending: true}, // 软删（S4 收集侧跳过夹具）
		},
	}, nil
}
