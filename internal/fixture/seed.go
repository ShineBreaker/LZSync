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

// MaterializeSet 把原料固化为自洽 DTO：难度 Hash = .osu SHA-256、MD5Hash =
// .osu 原始字节 MD5、集 Hash = 集内全部 .osu 按文件名排序拼接的 SHA-256
// （lazer-baseline §6.1）。新文件字节并入 files（哈希 → 字节）。
func MaterializeSet(onlineID int64, artist, title, author string, diffs []DiffMaterial, audio []byte, files map[string][]byte) (hub.BeatmapSet, error) {
	if len(diffs) == 0 {
		return hub.BeatmapSet{}, fmt.Errorf("fixture: 谱面集至少需要一个难度")
	}
	set := hub.BeatmapSet{
		OnlineID:       onlineID,
		Artist:         artist,
		Title:          title,
		AuthorUsername: author,
		StatusInt:      1, // BeatmapOnlineStatus.Ranked（lazer-baseline §5）
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

	for _, d := range diffs {
		bm := hub.Beatmap{
			Hash:             sha256Hex(d.OsuBytes),
			MD5Hash:          md5Hex(d.OsuBytes),
			DifficultyName:   d.Name,
			RulesetShortName: "osu",
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

// StandardSeed 四端共底种子：1 个谱面集（2 难度 + 音频）+ 1 个收藏夹
// （按 MD5 追踪，lazer-baseline §2.8）+ 1 条本地成绩。
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

// DeltaGimmickSeed delta 夹具：标准种子 + 一个含 gimmick section 的 .osu
// 难度（数据全在 .osu 文件内，不进库表——fork-delta §5.1）。
func DeltaGimmickSeed() (Seed, error) {
	seed, err := StandardSeed()
	if err != nil {
		return Seed{}, err
	}
	files := seed.Files
	g, err := MaterializeSet(4004, "Delta Artist", "Gimmick Set", "mapper",
		[]DiffMaterial{{Name: "Gimmick", OsuBytes: []byte(gimmickOsu)}}, nil, files)
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
	seed.BeatmapSets = append(seed.BeatmapSets, bms)
	seed.Files = files

	// JudgeMode≠0（EZ2AC=31，fork-ez2lazer §2.7）+ 未知键 + 非官方 mod。
	// 排在首位：动态重开断言经 FirstObject 取行。
	judge, err := MaterializeScore(bms.Beatmaps[0], "ez2lazer 52011", []byte("ez judge replay"), seed.Files)
	if err != nil {
		return Seed{}, err
	}
	judge.TotalScore = 1234567
	judge.RankInt = 1 // SS
	judge.JudgeMode = 31
	judge.HealthMode = 34 // Ez2Ac
	judge.StatisticsJson = `{"great":900,"ez_unknown_result":42,"legacy_combo_increment":7}`
	judge.ModsJson = `[{"acronym":"EZX","settings":{}}]`
	seed.Scores = append([]SeedScore{judge}, seed.Scores...)

	seed.ExternalSets = []ExternalSetSpec{{
		OnlineID:    5006,
		ContentRoot: externalRoot,
	}}
	seed.EzSkillRows = true
	seed.Skins = append(seed.Skins, SeedSkin{Name: "Ez Default", Creator: "Ez2Lazer"})
	return seed, nil
}

// G0v0DiffSeed g0v0 差异夹具（design §8.2-4）：市场规则集行（OnlineID≥10，
// fork-g0v0 §2.1）+ 指向它的成绩 + 带 PP 列占位（double 列当前不可写非默认
// 值，PP 断言待 realmc 写入面补齐，见包 notes）。
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

// MaterializeScore 把回放字节固化为自洽成绩行：Hash = 全部 .osr 内容拼接
// SHA-256（lazer-baseline §6.1）、BeatmapHash = 打分时难度的 Hash。
func MaterializeScore(bm hub.Beatmap, clientVersion string, replay []byte, files map[string][]byte) (SeedScore, error) {
	if bm.Hash == "" {
		return SeedScore{}, fmt.Errorf("fixture: 成绩挂接的难度缺 Hash")
	}
	replayHash := sha256Hex(replay)
	files[replayHash] = replay
	return SeedScore{
		BeatmapHash:       bm.Hash,
		RulesetShortName:  bm.RulesetShortName,
		ClientVersion:     clientVersion,
		Hash:              replayHash,
		TotalScoreVersion: scoreTotalScoreVersionLatest,
		OnlineID:          -1,
		LegacyOnlineID:    -1,
		ReplayHashes:      []string{replayHash},
	}, nil
}
