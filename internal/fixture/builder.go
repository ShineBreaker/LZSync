package fixture

// 播种执行（design.md §8.2-2/3）：一次写事务内按「规则集 → File 行 →
// 内置/用户皮肤 → 谱面集/难度/Metadata → 收藏夹 → 成绩 → 外部托管集 →
// Ez 技能表」顺序写入（File 行先于一切 usage 挂接——链接需要目标行在场）。
// M2 起 int/string/bool/timestamp/double/UUID 标量列与链接列、embedded 列、
// 集合列全量可写（realmc 完整写面）；逐列按镜像名寻址，缺列即报错
//（不静默跳过）。

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

func (s *seeder) setBool(obj unsafe.Pointer, class, name string, v bool) error {
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetBool(obj, key, v)
}

func (s *seeder) setFloat32(obj unsafe.Pointer, class, name string, v float32) error {
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetFloat32(obj, key, v)
}

func (s *seeder) setFloat64(obj unsafe.Pointer, class, name string, v float64) error {
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetFloat64(obj, key, v)
}

func (s *seeder) setTimestamp(obj unsafe.Pointer, class, name string, t time.Time) error {
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetTimestamp(obj, key, t)
}

// setGuid 写 UUID 列（接受带连字符的规范 Guid 串；空串跳过——行身份走
// 内容哈希列，Guid 仅内置皮肤等固定常量需要）。
func (s *seeder) setGuid(obj unsafe.Pointer, class, name, guid string) error {
	if guid == "" {
		return nil
	}
	clean := strings.ReplaceAll(guid, "-", "")
	if len(clean) != 32 {
		return fmt.Errorf("fixture: Guid %q 非 32 位 hex", guid)
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return fmt.Errorf("fixture: Guid %q hex 解码: %w", guid, err)
	}
	var id [16]byte
	copy(id[:], b)
	key, err := s.prop(class, name)
	if err != nil {
		return err
	}
	return s.r.SetGuid(obj, key, id)
}

// findRowByString 按 string 列值取首行句柄（Ruleset.ShortName / File.Hash
// 的行解析）；无匹配返回 (nil, nil)，调用方负责 ReleaseObject。
func (s *seeder) findRowByString(class, prop, value string) (unsafe.Pointer, error) {
	ck, err := s.class(class)
	if err != nil {
		return nil, err
	}
	pk, err := s.prop(class, prop)
	if err != nil {
		return nil, err
	}
	return s.r.FindFirstByString(ck, pk, value)
}

// appendNamedFiles 向对象的 Files 列表追加 RealmNamedFileUsage 行并链接
// File 行（File 行必须已播种——seedAll 的顺序契约）。
func (s *seeder) appendNamedFiles(obj unsafe.Pointer, class string, files []hub.NamedFile) error {
	if len(files) == 0 {
		return nil
	}
	listKey, err := s.prop(class, "Files")
	if err != nil {
		return err
	}
	lst, err := s.r.GetList(obj, listKey)
	if err != nil {
		return err
	}
	defer lst.Close()
	for _, f := range files {
		usage, err := lst.AddEmbedded()
		if err != nil {
			return err
		}
		if err := s.setStr(usage, "RealmNamedFileUsage", "Filename", f.Filename); err != nil {
			s.r.ReleaseObject(usage)
			return err
		}
		fileRow, err := s.findRowByString("File", "Hash", f.Hash)
		if err != nil {
			s.r.ReleaseObject(usage)
			return err
		}
		if fileRow == nil {
			s.r.ReleaseObject(usage)
			return fmt.Errorf("fixture: %s 的文件 %s 无 File 行（字节池缺失）", class, f.Hash)
		}
		fileKey, err := s.prop("RealmNamedFileUsage", "File")
		if err != nil {
			s.r.ReleaseObject(usage)
			s.r.ReleaseObject(fileRow)
			return err
		}
		err = s.r.SetLinkedObject(usage, fileKey, fileRow)
		s.r.ReleaseObject(fileRow)
		if err != nil {
			s.r.ReleaseObject(usage)
			return err
		}
		s.r.ReleaseObject(usage)
	}
	return nil
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
		func() error { return seedFileRows(s, spec.Seed.Files) },
		func() error { return seedSkins(s, spec.Seed) },
		func() error { return seedBeatmapSets(s, spec.Seed) },
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
	write := func(sk SeedSkin) error {
		return s.row("Skin", func(obj unsafe.Pointer) error {
			if err := s.setGuid(obj, "Skin", "ID", sk.Guid); err != nil {
				return err
			}
			for _, c := range []struct{ name, val string }{
				{"Name", sk.Name}, {"Creator", sk.Creator},
				{"InstantiationInfo", sk.InstantiationInfo}, {"Hash", sk.Hash},
			} {
				if err := s.setStr(obj, "Skin", c.name, c.val); err != nil {
					return err
				}
			}
			if err := s.setBool(obj, "Skin", "Protected", sk.Protected); err != nil {
				return err
			}
			if err := s.setBool(obj, "Skin", "DeletePending", sk.DeletePending); err != nil {
				return err
			}
			return s.appendNamedFiles(obj, "Skin", sk.Files)
		})
	}
	// 内置六皮肤固定 Guid + Protected（lazer-baseline §2.10；固定 Guid
	// 四端自备，投影端按 Protected 跳过——同步无收益）。
	for _, sk := range builtinSkins {
		if err := write(SeedSkin{
			Guid:      sk.guid,
			Name:      sk.name,
			Creator:   builtinSkinCreator,
			Protected: true,
		}); err != nil {
			return err
		}
	}
	for _, sk := range seed.Skins {
		if err := write(sk); err != nil {
			return err
		}
	}
	return nil
}

// seedBeatmapSets 播谱面集（C2 白名单全列）：集行标量列 + Files usage
// 列表 + 逐难度 Beatmap 行（标量全列 + Difficulty/UserSettings embedded +
// Ruleset/Metadata/父集链接）挂入 Beatmaps 前向列表。每难度一行独立
// Metadata（lazer 的 Metadata 每难度一份，lazer-baseline §2.5/§2.6）。
func seedBeatmapSets(s *seeder, seed Seed) error {
	for _, set := range seed.BeatmapSets {
		if err := seedOneSet(s, set); err != nil {
			return err
		}
	}
	return nil
}

func seedOneSet(s *seeder, set hub.BeatmapSet) error {
	setCk, err := s.class("BeatmapSet")
	if err != nil {
		return err
	}
	setObj, err := s.r.CreateObject(setCk)
	if err != nil {
		return err
	}
	defer s.r.ReleaseObject(setObj)

	if err := s.setStr(setObj, "BeatmapSet", "Hash", set.Hash); err != nil {
		return err
	}
	if err := s.setInt(setObj, "BeatmapSet", "OnlineID", set.OnlineID); err != nil {
		return err
	}
	if err := s.setInt(setObj, "BeatmapSet", "Status", int64(set.StatusInt)); err != nil {
		return err
	}
	if err := s.setBool(setObj, "BeatmapSet", "DeletePending", set.DeletePending); err != nil {
		return err
	}
	if err := s.setBool(setObj, "BeatmapSet", "Protected", set.Protected); err != nil {
		return err
	}
	if !set.DateAdded.IsZero() {
		if err := s.setTimestamp(setObj, "BeatmapSet", "DateAdded", set.DateAdded); err != nil {
			return err
		}
	}
	if set.DateSubmitted != nil {
		if err := s.setTimestamp(setObj, "BeatmapSet", "DateSubmitted", *set.DateSubmitted); err != nil {
			return err
		}
	}
	if set.DateRanked != nil {
		if err := s.setTimestamp(setObj, "BeatmapSet", "DateRanked", *set.DateRanked); err != nil {
			return err
		}
	}
	if err := s.appendNamedFiles(setObj, "BeatmapSet", set.Files); err != nil {
		return err
	}

	listKey, err := s.prop("BeatmapSet", "Beatmaps")
	if err != nil {
		return err
	}
	lst, err := s.r.GetList(setObj, listKey)
	if err != nil {
		return err
	}
	defer lst.Close()
	for _, bm := range set.Beatmaps {
		bmObj, err := seedOneBeatmap(s, bm, set, setObj)
		if err != nil {
			return err
		}
		err = lst.AddLinkedObject(bmObj)
		s.r.ReleaseObject(bmObj)
		if err != nil {
			return err
		}
	}
	return nil
}

// seedOneBeatmap 播一行难度并挂好全部结构关系，返回行句柄（调用方释放
// 并负责挂入父集 Beatmaps 列表）。Metadata 为零值时回落集级三字段
//（手工构造的旧式种子兼容）。
func seedOneBeatmap(s *seeder, bm hub.Beatmap, set hub.BeatmapSet, setObj unsafe.Pointer) (unsafe.Pointer, error) {
	bmCk, err := s.class("Beatmap")
	if err != nil {
		return nil, err
	}
	bmObj, err := s.r.CreateObject(bmCk)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (unsafe.Pointer, error) {
		s.r.ReleaseObject(bmObj)
		return nil, err
	}
	for _, c := range []struct{ name, val string }{
		{"Hash", bm.Hash}, {"MD5Hash", bm.MD5Hash}, {"OnlineMD5Hash", bm.OnlineMD5Hash},
		{"DifficultyName", bm.DifficultyName},
	} {
		if err := s.setStr(bmObj, "Beatmap", c.name, c.val); err != nil {
			return fail(err)
		}
	}
	for _, c := range []struct {
		name string
		val  int64
	}{
		{"Status", int64(bm.StatusInt)}, {"OnlineID", int64(bm.OnlineID)},
		{"EndTimeObjectCount", int64(bm.EndTimeObjectCount)},
		{"TotalObjectCount", int64(bm.TotalObjectCount)},
		{"BeatDivisor", int64(bm.BeatDivisor)},
	} {
		if err := s.setInt(bmObj, "Beatmap", c.name, c.val); err != nil {
			return fail(err)
		}
	}
	for _, c := range []struct {
		name string
		val  float64
	}{
		{"Length", bm.Length}, {"BPM", bm.BPM}, {"StarRating", bm.StarRating},
	} {
		if err := s.setFloat64(bmObj, "Beatmap", c.name, c.val); err != nil {
			return fail(err)
		}
	}
	if err := s.setBool(bmObj, "Beatmap", "Hidden", bm.Hidden); err != nil {
		return fail(err)
	}
	for _, c := range []struct {
		name string
		val  *time.Time
	}{
		{"LastLocalUpdate", bm.LastLocalUpdate}, {"LastOnlineUpdate", bm.LastOnlineUpdate},
		{"LastPlayed", bm.LastPlayed},
	} {
		if c.val != nil {
			if err := s.setTimestamp(bmObj, "Beatmap", c.name, *c.val); err != nil {
				return fail(err)
			}
		}
	}
	if bm.EditorTimestamp != nil {
		if err := s.setFloat64(bmObj, "Beatmap", "EditorTimestamp", *bm.EditorTimestamp); err != nil {
			return fail(err)
		}
	}

	// Ruleset 链接（ShortName 行解析；空名留 null）。
	if bm.RulesetShortName != "" {
		rsObj, err := s.findRowByString("Ruleset", "ShortName", bm.RulesetShortName)
		if err != nil {
			return fail(err)
		}
		if rsObj == nil {
			return fail(fmt.Errorf("fixture: 难度 %s 引用规则集 %q 无行", bm.DifficultyName, bm.RulesetShortName))
		}
		rsKey, err := s.prop("Beatmap", "Ruleset")
		if err != nil {
			s.r.ReleaseObject(rsObj)
			return fail(err)
		}
		err = s.r.SetLinkedObject(bmObj, rsKey, rsObj)
		s.r.ReleaseObject(rsObj)
		if err != nil {
			return fail(err)
		}
	}

	// Difficulty embedded（六个数值列）。
	diffKey, err := s.prop("Beatmap", "Difficulty")
	if err != nil {
		return fail(err)
	}
	diffObj, err := s.r.SetEmbedded(bmObj, diffKey)
	if err != nil {
		return fail(err)
	}
	for _, c := range []struct {
		name string
		val  float32
	}{
		{"DrainRate", bm.Difficulty.DrainRate}, {"CircleSize", bm.Difficulty.CircleSize},
		{"OverallDifficulty", bm.Difficulty.OverallDifficulty}, {"ApproachRate", bm.Difficulty.ApproachRate},
	} {
		if err := s.setFloat32(diffObj, "BeatmapDifficulty", c.name, c.val); err != nil {
			s.r.ReleaseObject(diffObj)
			return fail(err)
		}
	}
	for _, c := range []struct {
		name string
		val  float64
	}{
		{"SliderMultiplier", bm.Difficulty.SliderMultiplier}, {"SliderTickRate", bm.Difficulty.SliderTickRate},
	} {
		if err := s.setFloat64(diffObj, "BeatmapDifficulty", c.name, c.val); err != nil {
			s.r.ReleaseObject(diffObj)
			return fail(err)
		}
	}
	s.r.ReleaseObject(diffObj)

	// UserSettings embedded（唯一列 Offset 的扁平投影）。
	usKey, err := s.prop("Beatmap", "UserSettings")
	if err != nil {
		return fail(err)
	}
	usObj, err := s.r.SetEmbedded(bmObj, usKey)
	if err != nil {
		return fail(err)
	}
	if err := s.setFloat64(usObj, "BeatmapUserSettings", "Offset", bm.UserSettingsOffset); err != nil {
		s.r.ReleaseObject(usObj)
		return fail(err)
	}
	s.r.ReleaseObject(usObj)

	// Metadata 独立行（每难度一份）+ Author embedded + UserTags 列表。
	md := bm.Metadata
	if md.Title == "" && md.Artist == "" {
		md.Title, md.Artist = set.Title, set.Artist
		md.Author.Username = set.AuthorUsername
	}
	mdObj, err := seedOneMetadata(s, md)
	if err != nil {
		return fail(err)
	}
	mdKey, err := s.prop("Beatmap", "Metadata")
	if err != nil {
		s.r.ReleaseObject(mdObj)
		return fail(err)
	}
	err = s.r.SetLinkedObject(bmObj, mdKey, mdObj)
	s.r.ReleaseObject(mdObj)
	if err != nil {
		return fail(err)
	}

	// 父集前向链接（lazer-baseline §2.5 BeatmapInfo.BeatmapSet）。
	setKey, err := s.prop("Beatmap", "BeatmapSet")
	if err != nil {
		return fail(err)
	}
	if err := s.r.SetLinkedObject(bmObj, setKey, setObj); err != nil {
		return fail(err)
	}
	return bmObj, nil
}

// seedOneMetadata 播一行 BeatmapMetadata（C2 白名单全列：Author embedded
// RealmUser + UserTags 列表），返回行句柄（调用方释放）。
func seedOneMetadata(s *seeder, md hub.Metadata) (unsafe.Pointer, error) {
	mdCk, err := s.class("BeatmapMetadata")
	if err != nil {
		return nil, err
	}
	mdObj, err := s.r.CreateObject(mdCk)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (unsafe.Pointer, error) {
		s.r.ReleaseObject(mdObj)
		return nil, err
	}
	for _, c := range []struct{ name, val string }{
		{"Title", md.Title}, {"TitleUnicode", md.TitleUnicode},
		{"Artist", md.Artist}, {"ArtistUnicode", md.ArtistUnicode},
		{"Source", md.Source}, {"Tags", md.Tags},
		{"AudioFile", md.AudioFile}, {"BackgroundFile", md.BackgroundFile},
	} {
		if err := s.setStr(mdObj, "BeatmapMetadata", c.name, c.val); err != nil {
			return fail(err)
		}
	}
	if err := s.setInt(mdObj, "BeatmapMetadata", "PreviewTime", int64(md.PreviewTime)); err != nil {
		return fail(err)
	}

	authorKey, err := s.prop("BeatmapMetadata", "Author")
	if err != nil {
		return fail(err)
	}
	author, err := s.r.SetEmbedded(mdObj, authorKey)
	if err != nil {
		return fail(err)
	}
	if err := s.setInt(author, "RealmUser", "OnlineID", int64(md.Author.OnlineID)); err != nil {
		s.r.ReleaseObject(author)
		return fail(err)
	}
	for _, c := range []struct{ name, val string }{
		{"Username", md.Author.Username}, {"CountryString", md.Author.CountryString},
	} {
		if err := s.setStr(author, "RealmUser", c.name, c.val); err != nil {
			s.r.ReleaseObject(author)
			return fail(err)
		}
	}
	s.r.ReleaseObject(author)

	if len(md.UserTags) > 0 {
		tagsKey, err := s.prop("BeatmapMetadata", "UserTags")
		if err != nil {
			return fail(err)
		}
		tags, err := s.r.GetList(mdObj, tagsKey)
		if err != nil {
			return fail(err)
		}
		for _, tag := range md.UserTags {
			if err := tags.AddString(tag); err != nil {
				tags.Close()
				return fail(err)
			}
		}
		tags.Close()
	}
	return mdObj, nil
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
	// BeatmapMD5Hashes 列不进镜像（M1 已知边界：deploy 冲突链路测试依赖
	// fixture 库无 MD5 列的形态）；LastModified 暂不播（M1 收敛面）——
	// 当前仅落 Name（内容键）。
	for _, c := range cols {
		if err := s.row("BeatmapCollection", func(obj unsafe.Pointer) error {
			return s.setStr(obj, "BeatmapCollection", "Name", c.Name)
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedScores 播成绩行（C2 白名单全列）：标量列（含 nullable 的
// LegacyTotalScore/PP 与 timestamp Date）、Ruleset 链接、User embedded、
// Pauses 列表、Files usage 列表（每个回放一条 .osr）。
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
			if sc.LegacyTotalScore != nil {
				if err := s.setInt(obj, "Score", "LegacyTotalScore", *sc.LegacyTotalScore); err != nil {
					return err
				}
			}
			for _, c := range []struct {
				name string
				val  bool
			}{
				{"DeletePending", sc.DeletePending},
				{"BackgroundReprocessingFailed", sc.BackgroundReprocessingFailed},
				{"IsLegacyScore", sc.IsLegacyScore},
			} {
				if err := s.setBool(obj, "Score", c.name, c.val); err != nil {
					return err
				}
			}
			if err := s.setFloat64(obj, "Score", "Accuracy", sc.Accuracy); err != nil {
				return err
			}
			if sc.PP != nil {
				if err := s.setFloat64(obj, "Score", "PP", *sc.PP); err != nil {
					return err
				}
			}
			if !sc.Date.IsZero() {
				if err := s.setTimestamp(obj, "Score", "Date", sc.Date); err != nil {
					return err
				}
			}

			// Ruleset 链接（ShortName 行解析；空名留 null——跳过夹具行）。
			if sc.RulesetShortName != "" {
				rsObj, err := s.findRowByString("Ruleset", "ShortName", sc.RulesetShortName)
				if err != nil {
					return err
				}
				if rsObj == nil {
					return fmt.Errorf("fixture: 成绩引用规则集 %q 无行", sc.RulesetShortName)
				}
				rsKey, err := s.prop("Score", "Ruleset")
				if err != nil {
					s.r.ReleaseObject(rsObj)
					return err
				}
				err = s.r.SetLinkedObject(obj, rsKey, rsObj)
				s.r.ReleaseObject(rsObj)
				if err != nil {
					return err
				}
			}

			// User embedded（归属用户三列）。
			userKey, err := s.prop("Score", "User")
			if err != nil {
				return err
			}
			user, err := s.r.SetEmbedded(obj, userKey)
			if err != nil {
				return err
			}
			if err := s.setInt(user, "RealmUser", "OnlineID", int64(sc.User.OnlineID)); err != nil {
				s.r.ReleaseObject(user)
				return err
			}
			for _, c := range []struct{ name, val string }{
				{"Username", sc.User.Username}, {"CountryString", sc.User.CountryString},
			} {
				if err := s.setStr(user, "RealmUser", c.name, c.val); err != nil {
					s.r.ReleaseObject(user)
					return err
				}
			}
			s.r.ReleaseObject(user)

			// Pauses 列表（毫秒偏移）。
			if len(sc.Pauses) > 0 {
				pausesKey, err := s.prop("Score", "Pauses")
				if err != nil {
					return err
				}
				pauses, err := s.r.GetList(obj, pausesKey)
				if err != nil {
					return err
				}
				for _, p := range sc.Pauses {
					if err := pauses.AddInt64(int64(p)); err != nil {
						pauses.Close()
						return err
					}
				}
				pauses.Close()
			}

			// Files usage 列表（回放文件，lazer-baseline §2.7）。
			files := make([]hub.NamedFile, 0, len(sc.ReplayHashes))
			for _, h := range sc.ReplayHashes {
				files = append(files, hub.NamedFile{Hash: h, Filename: "replay.osr"})
			}
			return s.appendNamedFiles(obj, "Score", files)
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
