package fixture

// 播种执行（design.md §8.2-2/3）：一次写事务内按「规则集 → 内置皮肤 →
// 谱面集/难度/Metadata/File → 收藏夹 → 成绩 → 外部托管集 → Ez 技能表」
// 顺序写入。行内仅 int/string 列可写值（见包文档能力边界），逐列按镜像
// 名寻址，缺列即报错（不静默跳过）。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"unsafe"

	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// seeder 缓存一次写事务内的 class/prop key 寻址，避免逐行重查。
type seeder struct {
	r         *realmc.Realm
	classKeys map[string]uint32
	propKeys  map[string]map[string]int64
}

func newSeeder(r *realmc.Realm) *seeder {
	return &seeder{r: r, classKeys: map[string]uint32{}, propKeys: map[string]map[string]int64{}}
}

func (s *seeder) class(name string) (uint32, error) {
	if k, ok := s.classKeys[name]; ok {
		return k, nil
	}
	k, err := s.r.FindClass(name)
	if err != nil {
		return 0, err
	}
	s.classKeys[name] = k
	return k, nil
}

// prop 返回列 key；类不在镜像或缺列都报错（种子→镜像 的字段契约）。
func (s *seeder) prop(class, name string) (int64, error) {
	pk, ok := s.propKeys[class]
	if !ok {
		ck, err := s.class(class)
		if err != nil {
			return 0, err
		}
		props, err := s.r.Properties(ck)
		if err != nil {
			return 0, err
		}
		pk = make(map[string]int64, len(props))
		for _, p := range props {
			pk[p.Name] = p.Key
		}
		s.propKeys[class] = pk
	}
	key, ok := pk[name]
	if !ok {
		return 0, fmt.Errorf("镜像 %s 缺列 %s", class, name)
	}
	return key, nil
}

func (s *seeder) setInt(obj unsafe.Pointer, class, name string, v int64) error {
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetInt64(obj, key, v)
}

func (s *seeder) setStr(obj unsafe.Pointer, class, name, v string) error {
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetString(obj, key, v)
}

// row 建一行并执行 fill，返回前释放行句柄。
func (s *seeder) row(class string, fill func(obj unsafe.Pointer) error) error {
	ck, err := s.class(class)
	if err != nil {
		return err
	}
	obj, err := s.r.CreateObject(ck)
	if err != nil {
		return err
	}
	defer s.r.ReleaseObject(obj)
	return fill(obj)
}

func seedAll(r *realmc.Realm, spec Spec) error {
	s := newSeeder(r)
	for _, step := range []func() error{
		func() error { return seedRulesets(s, spec.Seed) },
		func() error { return seedSkins(s, spec.Seed) },
		func() error { return seedBeatmapSets(s, spec.Seed) },
		func() error { return seedFileRows(s, spec.Seed.Files) },
		func() error { return seedCollections(s, spec.Seed.Collections) },
		func() error { return seedScores(s, spec) },
		func() error { return seedExternalSets(s, spec.Seed) },
		func() error {
			if !spec.Seed.EzSkillRows {
				return nil
			}
			return seedEzSkillRows(s)
		},
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func seedRulesets(s *seeder, seed Seed) error {
	write := func(shortName, name, inst string, onlineID int64) error {
		return s.row("Ruleset", func(obj unsafe.Pointer) error {
			if err := s.setStr(obj, "Ruleset", "ShortName", shortName); err != nil {
				return err
			}
			if err := s.setStr(obj, "Ruleset", "Name", name); err != nil {
				return err
			}
			if err := s.setStr(obj, "Ruleset", "InstantiationInfo", inst); err != nil {
				return err
			}
			return s.setInt(obj, "Ruleset", "OnlineID", onlineID)
		})
	}
	for _, rs := range builtinRulesets {
		if err := write(rs.shortName, rs.name, rs.instantiationInfo, rs.onlineID); err != nil {
			return err
		}
	}
	for _, rs := range seed.ExtraRulesets {
		if err := write(rs.ShortName, rs.Name, rs.InstantiationInfo, rs.OnlineID); err != nil {
			return err
		}
	}
	return nil
}

func seedSkins(s *seeder, seed Seed) error {
	write := func(name, creator string) error {
		return s.row("Skin", func(obj unsafe.Pointer) error {
			if err := s.setStr(obj, "Skin", "Name", name); err != nil {
				return err
			}
			return s.setStr(obj, "Skin", "Creator", creator)
		})
	}
	// 内置六皮肤固定 Guid（lazer-baseline §2.10）。Guid 列为 UUID 类型
	// 当前不可写值（realmc 无 UUID 构造面），行身份以 Name/Creator 指纹
	// 承载——Guid 锚点待 realmc 写入面补齐后回填（见包 notes）。
	for _, sk := range builtinSkins {
		if err := write(sk.name, builtinSkinCreator); err != nil {
			return err
		}
	}
	for _, sk := range seed.Skins {
		if err := write(sk.Name, sk.Creator); err != nil {
			return err
		}
	}
	return nil
}

func seedBeatmapSets(s *seeder, seed Seed) error {
	for _, set := range seed.BeatmapSets {
		if err := s.row("BeatmapSet", func(obj unsafe.Pointer) error {
			if err := s.setStr(obj, "BeatmapSet", "Hash", set.Hash); err != nil {
				return err
			}
			if err := s.setInt(obj, "BeatmapSet", "OnlineID", set.OnlineID); err != nil {
				return err
			}
			return s.setInt(obj, "BeatmapSet", "Status", int64(set.StatusInt))
		}); err != nil {
			return err
		}
		// 每难度一行 Beatmap + 一行 Metadata（lazer 的 Metadata 每难度
		// 独立；夹具持 set 级收缩投影 → 同内容复制。AuthorUsername 落
		// 嵌入 RealmUser 列，镜像不承载）。
		for _, bm := range set.Beatmaps {
			if err := s.row("Beatmap", func(obj unsafe.Pointer) error {
				if err := s.setStr(obj, "Beatmap", "Hash", bm.Hash); err != nil {
					return err
				}
				if err := s.setStr(obj, "Beatmap", "MD5Hash", bm.MD5Hash); err != nil {
					return err
				}
				if err := s.setStr(obj, "Beatmap", "DifficultyName", bm.DifficultyName); err != nil {
					return err
				}
				return s.setInt(obj, "Beatmap", "OnlineID", -1)
			}); err != nil {
				return err
			}
			if err := s.row("BeatmapMetadata", func(obj unsafe.Pointer) error {
				if err := s.setStr(obj, "BeatmapMetadata", "Title", set.Title); err != nil {
					return err
				}
				if err := s.setStr(obj, "BeatmapMetadata", "TitleUnicode", set.Title); err != nil {
					return err
				}
				if err := s.setStr(obj, "BeatmapMetadata", "Artist", set.Artist); err != nil {
					return err
				}
				return s.setStr(obj, "BeatmapMetadata", "ArtistUnicode", set.Artist)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedFileRows 为字节池全部哈希播 File 行（File.Hash 主键语义 = 内容寻址）。
func seedFileRows(s *seeder, files map[string][]byte) error {
	hashes := make([]string, 0, len(files))
	for h := range files {
		hashes = append(hashes, h)
	}
	// map 迭代乱序，排序保证多次构造库的行序稳定（可复现）。
	sort.Strings(hashes)
	for _, h := range hashes {
		if err := s.row("File", func(obj unsafe.Pointer) error {
			return s.setStr(obj, "File", "Hash", h)
		}); err != nil {
			return err
		}
	}
	return nil
}

func seedCollections(s *seeder, cols []hub.Collection) error {
	// BeatmapMD5Hashes 是集合列、LastModified 是 timestamp 列，镜像均
	// 不可承载（见包 notes）——当前仅落 Name（内容键）。
	for _, c := range cols {
		if err := s.row("BeatmapCollection", func(obj unsafe.Pointer) error {
			return s.setStr(obj, "BeatmapCollection", "Name", c.Name)
		}); err != nil {
			return err
		}
	}
	return nil
}

func seedScores(s *seeder, spec Spec) error {
	for _, sc := range spec.Seed.Scores {
		if err := s.row("Score", func(obj unsafe.Pointer) error {
			strCols := []struct{ name, val string }{
				{"BeatmapHash", sc.BeatmapHash},
				{"Hash", sc.Hash},
				{"ClientVersion", sc.ClientVersion},
				{"Statistics", sc.StatisticsJson},
				{"MaximumStatistics", sc.MaximumStatisticsJson},
				{"Mods", sc.ModsJson},
			}
			for _, c := range strCols {
				if err := s.setStr(obj, "Score", c.name, c.val); err != nil {
					return err
				}
			}
			intCols := []struct {
				name string
				val  int64
			}{
				{"TotalScore", sc.TotalScore},
				{"TotalScoreWithoutMods", sc.TotalScoreWithoutMods},
				{"TotalScoreVersion", sc.TotalScoreVersion},
				{"MaxCombo", sc.MaxCombo},
				{"OnlineID", sc.OnlineID},
				{"LegacyOnlineID", sc.LegacyOnlineID},
				{"Rank", sc.RankInt},
				{"Combo", sc.Combo},
			}
			if spec.ForkID == ForkEz2Lazer {
				intCols = append(intCols,
					struct {
						name string
						val  int64
					}{"JudgeMode", sc.JudgeMode},
					struct {
						name string
						val  int64
					}{"HealthMode", sc.HealthMode},
				)
			}
			for _, c := range intCols {
				if err := s.setInt(obj, "Score", c.name, c.val); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedExternalSets 播 Ez 外部托管集：BeatmapSet 行 Hash = ext:set: 编码、
// HostingKind=1(External)、ExternalContentRoot；另播一行路径哈希
// RealmFile（RegisterExternalHash：只登记不复制字节，fork-ez2lazer §5.1）。
func seedExternalSets(s *seeder, seed Seed) error {
	for _, ext := range seed.ExternalSets {
		if err := s.row("BeatmapSet", func(obj unsafe.Pointer) error {
			if err := s.setStr(obj, "BeatmapSet", "Hash", EncodeExtSetHash(ext.ContentRoot)); err != nil {
				return err
			}
			if err := s.setInt(obj, "BeatmapSet", "OnlineID", ext.OnlineID); err != nil {
				return err
			}
			if err := s.setStr(obj, "BeatmapSet", "ExternalContentRoot", ext.ContentRoot); err != nil {
				return err
			}
			return s.setInt(obj, "BeatmapSet", "HostingKind", 1)
		}); err != nil {
			return err
		}
		// BMS 难度文件的 RealmFile 是路径哈希（不哈希内容）。
		pathHash := PathRealmFileHash(filepath.Join(ext.ContentRoot, "chart.bms"))
		if err := s.row("File", func(obj unsafe.Pointer) error {
			return s.setStr(obj, "File", "Hash", pathHash)
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedEzSkillRows 在 7 张技能表各播 1 行固定演示行（仅可写列赋值）。
func seedEzSkillRows(s *seeder) error {
	type rowSpec struct {
		class string
		strs  map[string]string
		ints  map[string]int64
	}
	rows := []rowSpec{
		{"EzBeatmapSkillValue",
			map[string]string{"BeatmapHash": demoBeatmapHash, "SystemId": "beatmap_msd", "SkillId": "stream"},
			map[string]int64{"AlgorithmVersion": 1}},
		{"EzPlayerSkillValue",
			map[string]string{"Username": "fixture", "SystemId": "player_ssr", "SkillId": "stream"},
			map[string]int64{"KeyCount": 7, "AnalyzedPlays": 12, "AlgorithmVersion": 1}},
		{"EzDanEstimate",
			map[string]string{"Username": "fixture", "Side": "rc", "Label": "dan1"},
			map[string]int64{"KeyCount": 7, "Clears": 3, "AlgorithmVersion": 1}},
		{"EzPlayerSkillHistoryPoint",
			map[string]string{"Username": "fixture", "SkillId": "stream"},
			map[string]int64{"KeyCount": 7, "AlgorithmVersion": 1}},
		{"EzBeatmapChartSkillInfo",
			map[string]string{"BeatmapHash": demoBeatmapHash, "PatternTagsJoined": "stream␟jack"},
			map[string]int64{"InfoVersion": 1}},
		{"EzPlayerDanSkillsetValue",
			map[string]string{"Username": "fixture", "Side": "ln", "SkillsetId": "stamina", "Label": "i5"},
			map[string]int64{"KeyCount": 7, "Clears": 1, "AlgorithmVersion": 1}},
		{"EzBeatmapChartDan",
			map[string]string{"BeatmapHash": demoBeatmapHash, "RcLabel": "rc1", "LnLabel": "ln1"},
			map[string]int64{"AlgorithmVersion": 1, "KeyCount": 7}},
	}
	for _, row := range rows {
		if err := s.row(row.class, func(obj unsafe.Pointer) error {
			for name, v := range row.strs {
				if err := s.setStr(obj, row.class, name, v); err != nil {
					return err
				}
			}
			for name, v := range row.ints {
				if err := s.setInt(obj, row.class, name, v); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// demoBeatmapHash 技能表演示行引用的占位难度哈希（64 hex；内容不指向
// 实际文件——技能行语义允许 hash 变更后失效重算）。
const demoBeatmapHash = "00000000000000000000000000000000000000000000000000000000c0ffee01"

// writeFilesDir 落盘内容寻址仓：files/<hash[0:1]>/<hash[0:2]>/<hash>
// （lazer-baseline §1.4 两级目录布局）。
func writeFilesDir(dataDir string, files map[string][]byte) error {
	for h, b := range files {
		if len(h) < 2 {
			return fmt.Errorf("文件哈希 %q 过短", h)
		}
		dir := filepath.Join(dataDir, "files", h[:1], h[:2])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, h), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
