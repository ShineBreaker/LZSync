package fixture

// 单测（design.md §8.2-1/4）：schema 镜像形状断言（纯 Go，无 librealm 依赖）
// + 建库 → 动态重开 → 行数与关键列断言（需 librealm，缺失显式 SKIP，
// §8.1「不得静默绿」）。差异夹具每条对应一组断言（§8.2-4）。

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"lzsync/internal/forks"
	"lzsync/internal/realmc"
)

// ---- schema 镜像形状（纯 Go） ----

// classMap 把镜像折叠为 class → 列名 → 类型，供形状断言。
func classMap(t *testing.T, forkID string, version uint64) map[string]map[string]realmc.PropType {
	t.Helper()
	schema, err := SchemaFor(forkID, version)
	if err != nil {
		t.Fatalf("SchemaFor(%s,%d): %v", forkID, version, err)
	}
	m := make(map[string]map[string]realmc.PropType, len(schema.Classes))
	for _, c := range schema.Classes {
		cols := make(map[string]realmc.PropType, len(c.Properties))
		for _, p := range c.Properties {
			cols[p.Name] = p.Type
		}
		m[c.Name] = cols
	}
	return m
}

func TestSchemaForLazer52(t *testing.T) {
	m := classMap(t, ForkLazer, VersionLazer)
	wantClasses := []string{
		"File", "BeatmapSet", "Beatmap", "BeatmapMetadata", "Score",
		"BeatmapCollection", "Ruleset", "Skin", "ModPreset", "KeyBinding",
		"RulesetSetting", "RealmOnlineAsset",
		// 4 个 EmbeddedObject（lazer-baseline §2.2/§2.3/§2.5.1/§2.5.2）。
		"RealmNamedFileUsage", "RealmUser", "BeatmapDifficulty", "BeatmapUserSettings",
	}
	if len(m) != len(wantClasses) {
		t.Fatalf("lazer 52 class 数 = %d，期望 %d（多出/缺少: %v）", len(m), len(wantClasses), diffKeys(m, wantClasses))
	}
	for _, c := range wantClasses {
		if _, ok := m[c]; !ok {
			t.Errorf("lazer 52 缺 class %s", c)
		}
	}
	// 关键列的类型忠实性（磁盘名 = MapTo 名，lazer-baseline §2）。
	for col, want := range map[string]realmc.PropType{
		"ID": PropTypeUUID, "OnlineID": realmc.PropTypeInt, "Status": realmc.PropTypeInt,
		"DeletePending": realmc.PropTypeBool, "Hash": realmc.PropTypeString, "Protected": realmc.PropTypeBool,
	} {
		if got := m["BeatmapSet"][col]; got != want {
			t.Errorf("BeatmapSet.%s 类型 = %d，期望 %d", col, got, want)
		}
	}
	for col, want := range map[string]realmc.PropType{
		"MD5Hash": realmc.PropTypeString, "StarRating": PropTypeDouble,
		"EditorTimestamp": PropTypeDouble, "LastPlayed": realmc.PropTypeTimestamp,
		"Ruleset": realmc.PropTypeObject, "Metadata": realmc.PropTypeObject,
	} {
		if got := m["Beatmap"][col]; got != want {
			t.Errorf("Beatmap.%s 类型 = %d，期望 %d", col, got, want)
		}
	}
	for col, want := range map[string]realmc.PropType{
		"BeatmapHash": realmc.PropTypeString, "Mods": realmc.PropTypeString,
		"Statistics": realmc.PropTypeString, "Rank": realmc.PropTypeInt,
		"PP": PropTypeDouble, "Accuracy": PropTypeDouble,
		"User": realmc.PropTypeObject,
	} {
		if got := m["Score"][col]; got != want {
			t.Errorf("Score.%s 类型 = %d，期望 %d", col, got, want)
		}
	}
	for col, want := range map[string]realmc.PropType{
		"DrainRate": PropTypeFloat, "SliderMultiplier": PropTypeDouble,
	} {
		if got := m["BeatmapDifficulty"][col]; got != want {
			t.Errorf("BeatmapDifficulty.%s 类型 = %d，期望 %d", col, got, want)
		}
	}
}

func diffKeys(got map[string]map[string]realmc.PropType, want []string) []string {
	var d []string
	for _, w := range want {
		if _, ok := got[w]; !ok {
			d = append(d, "-"+w)
		}
	}
	for c := range got {
		found := false
		for _, w := range want {
			if c == w {
				found = true
				break
			}
		}
		if !found {
			d = append(d, "+"+c)
		}
	}
	return d
}

func TestSchemaForDelta51(t *testing.T) {
	m := classMap(t, ForkDelta, VersionDelta)
	if _, ok := m["RealmOnlineAsset"]; ok {
		t.Error("delta 51 不应有 RealmOnlineAsset（fork-delta §2.2：v52 唯一新增表）")
	}
	if len(m) != 15 {
		t.Fatalf("delta 51 class 数 = %d，期望 15（12 RealmObject − RealmOnlineAsset + 4 embedded）", len(m))
	}
}

func TestSchemaForG0v0(t *testing.T) {
	m := classMap(t, ForkG0v0, VersionG0v0)
	if len(m) != 16 {
		t.Fatalf("g0v0 52 class 数 = %d，期望 16（与官方全等，fork-g0v0 §2）", len(m))
	}
	if _, ok := m["Score"]["JudgeMode"]; ok {
		t.Error("g0v0 52 不应含 Ez 扩展列 JudgeMode")
	}
}

func TestSchemaForEz52011(t *testing.T) {
	m := classMap(t, ForkEz2Lazer, VersionEzDisk)
	if len(m) != 23 {
		t.Fatalf("ez 52011 class 数 = %d，期望 23（官方 16 含 embedded + Ez 7 表）", len(m))
	}
	// 10 扩展列（fork-ez2lazer §2.4）。
	for _, tc := range []struct{ class, col string }{
		{"Beatmap", "XxyStarRating"}, {"Beatmap", "PerformancePoints"},
		{"Beatmap", "HasVideo"}, {"Beatmap", "HasStoryboard"},
		{"Score", "JudgeMode"}, {"Score", "HealthMode"},
		{"BeatmapSet", "ExternalContentRoot"}, {"BeatmapSet", "HostingKind"},
		{"Ruleset", "LastAppliedXxySrVersion"}, {"Ruleset", "LastAppliedManiaSkillVersion"},
	} {
		if _, ok := m[tc.class][tc.col]; !ok {
			t.Errorf("ez 52011 缺扩展列 %s.%s", tc.class, tc.col)
		}
	}
	// 7 张技能表（§2.5）。
	for _, c := range []string{
		"EzBeatmapSkillValue", "EzPlayerSkillValue", "EzDanEstimate",
		"EzPlayerSkillHistoryPoint", "EzBeatmapChartSkillInfo",
		"EzPlayerDanSkillsetValue", "EzBeatmapChartDan",
	} {
		if _, ok := m[c]; !ok {
			t.Errorf("ez 52011 缺技能表 %s", c)
		}
	}
	if got := m["Beatmap"]["XxyStarRating"]; got != PropTypeDouble {
		t.Errorf("Beatmap.XxyStarRating 类型 = %d，期望 Double", got)
	}
	if got := m["BeatmapSet"]["HostingKind"]; got != realmc.PropTypeInt {
		t.Errorf("BeatmapSet.HostingKind 类型 = %d，期望 Int", got)
	}
}

func TestSchemaForRejectsUnknown(t *testing.T) {
	for _, tc := range []struct {
		fork string
		ver  uint64
	}{
		{"lazer", 51}, {"delta", 52}, {"ez2lazer", 52}, {"g0v0", 51}, {"unknown", 52},
	} {
		if _, err := SchemaFor(tc.fork, tc.ver); err == nil {
			t.Errorf("SchemaFor(%s,%d) 应报错（镜像档案不存在该组合）", tc.fork, tc.ver)
		}
	}
}

// ---- 建库 → 动态重开 → 断言（需 librealm） ----

// openedRO 动态只读重开并保持读事务到清理。
type openedRO struct {
	t *testing.T
	r *realmc.Realm
}

func mustOpenDyn(t *testing.T, path string) *openedRO {
	t.Helper()
	lib, err := realmc.Load()
	if err != nil {
		t.Skipf("realm-core 库不可用，跳过建库断言（§8.1 显式 SKIP）: %v", err)
	}
	r, err := lib.OpenDynamicReadOnly(path)
	if err != nil {
		t.Fatalf("动态只读重开 %s: %v", path, err)
	}
	if err := r.BeginRead(); err != nil {
		r.Close()
		t.Fatalf("begin_read: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("关闭重开会话: %v", err)
		}
	})
	return &openedRO{t: t, r: r}
}

func (o *openedRO) count(class string) uint64 {
	o.t.Helper()
	ck, err := o.r.FindClass(class)
	if err != nil {
		o.t.Fatalf("FindClass(%s): %v", class, err)
	}
	n, err := o.r.ObjectCount(ck)
	if err != nil {
		o.t.Fatalf("ObjectCount(%s): %v", class, err)
	}
	return n
}

func (o *openedRO) firstStr(class, prop string) string {
	o.t.Helper()
	ck, err := o.r.FindClass(class)
	if err != nil {
		o.t.Fatalf("FindClass(%s): %v", class, err)
	}
	obj, err := o.r.FirstObject(ck)
	if err != nil {
		o.t.Fatalf("FirstObject(%s): %v", class, err)
	}
	defer o.r.ReleaseObject(obj)
	key, err := o.r.FindProperty(ck, prop)
	if err != nil {
		o.t.Fatalf("FindProperty(%s.%s): %v", class, prop, err)
	}
	s, err := o.r.GetString(obj, key)
	if err != nil {
		o.t.Fatalf("GetString(%s.%s): %v", class, prop, err)
	}
	return s
}

func (o *openedRO) firstInt(class, prop string) int64 {
	o.t.Helper()
	ck, err := o.r.FindClass(class)
	if err != nil {
		o.t.Fatalf("FindClass(%s): %v", class, err)
	}
	obj, err := o.r.FirstObject(ck)
	if err != nil {
		o.t.Fatalf("FirstObject(%s): %v", class, err)
	}
	defer o.r.ReleaseObject(obj)
	key, err := o.r.FindProperty(ck, prop)
	if err != nil {
		o.t.Fatalf("FindProperty(%s.%s): %v", class, prop, err)
	}
	n, err := o.r.GetInt64(obj, key)
	if err != nil {
		o.t.Fatalf("GetInt64(%s.%s): %v", class, prop, err)
	}
	return n
}

func mustBuild(t *testing.T, forkID string, ver uint64, seed Seed) forks.LibraryRef {
	t.Helper()
	ref, err := Build(context.Background(), Spec{
		ForkID: forkID, SchemaVersion: ver, DataDir: t.TempDir(), Seed: seed,
	})
	if err != nil {
		t.Fatalf("Build(%s): %v", forkID, err)
	}
	return ref
}

func TestBuildStandardLazer(t *testing.T) {
	seed, err := StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	ref := mustBuild(t, ForkLazer, VersionLazer, seed)

	if !strings.HasSuffix(ref.RealmPath, "client.realm") {
		t.Errorf("官方端库文件名 = %q，期望 client.realm", ref.RealmPath)
	}
	if ref.UpstreamVersion != 52 || ref.ForkRev != 0 {
		t.Errorf("lazer 版本解码 = (%d,%d)，期望 (52,0)", ref.UpstreamVersion, ref.ForkRev)
	}

	// files/ 落盘与字节一致（lazer-baseline §1.4 两级目录布局）。
	for h, want := range seed.Files {
		b, err := os.ReadFile(filepath.Join(ref.DataDir, "files", h[:1], h[:2], h))
		if err != nil {
			t.Fatalf("files/ 落盘缺失 %s: %v", h, err)
		}
		if string(b) != string(want) {
			t.Errorf("files/%s 字节与种子不一致", h)
		}
	}

	o := mustOpenDyn(t, ref.RealmPath)
	if got := o.r.SchemaVersion(); got != VersionLazer {
		t.Fatalf("动态重开磁盘版本 = %d，期望 52", got)
	}
	for class, want := range map[string]uint64{
		"BeatmapSet": 1, "Beatmap": 2, "BeatmapMetadata": 2,
		"File":    4, // 2 .osu + 1 audio + 1 replay
		"Ruleset": 4, "Skin": 6, "Score": 1, "BeatmapCollection": 1,
	} {
		if got := o.count(class); got != want {
			t.Errorf("%s 行数 = %d，期望 %d", class, got, want)
		}
	}
	// 关键列：哈希关系链（§8.2-3 自洽图）。
	if got := o.firstStr("BeatmapSet", "Hash"); got != seed.BeatmapSets[0].Hash {
		t.Errorf("BeatmapSet.Hash = %q，期望 %q", got, seed.BeatmapSets[0].Hash)
	}
	if got := o.firstStr("Beatmap", "MD5Hash"); got != seed.BeatmapSets[0].Beatmaps[0].MD5Hash {
		t.Errorf("Beatmap.MD5Hash = %q，期望 %q", got, seed.BeatmapSets[0].Beatmaps[0].MD5Hash)
	}
	if got := o.firstStr("Score", "BeatmapHash"); got != seed.BeatmapSets[0].Beatmaps[0].Hash {
		t.Errorf("Score.BeatmapHash = %q，期望 %q（打分时难度 Hash）",
			got, seed.BeatmapSets[0].Beatmaps[0].Hash)
	}
	if got := o.firstStr("Score", "Statistics"); got != seed.Scores[0].StatisticsJson {
		t.Errorf("Score.Statistics = %q，期望 %q", got, seed.Scores[0].StatisticsJson)
	}
	if got := o.firstStr("Ruleset", "ShortName"); got != "osu" {
		t.Errorf("Ruleset 首行 ShortName = %q，期望 osu（官方四规则集固定播种）", got)
	}
	if got := o.firstInt("BeatmapSet", "OnlineID"); got != 1001 {
		t.Errorf("BeatmapSet.OnlineID = %d，期望 1001", got)
	}
	if got := o.firstInt("BeatmapSet", "Status"); got != 1 {
		t.Errorf("BeatmapSet.Status = %d，期望 1（Ranked）", got)
	}
	if got := o.firstInt("Score", "TotalScoreVersion"); got != 30000019 {
		t.Errorf("Score.TotalScoreVersion = %d，期望 30000019（LATEST_VERSION）", got)
	}
}

// TestBuildFullColumns 全列播种可读回（M2 §8.2-3）：皮肤固定 Guid + Files
// usage、成绩 User embedded/Pauses/PP、难度 Difficulty/Metadata/Ruleset
// 结构关系经动态只读重开逐点读回。
func TestBuildFullColumns(t *testing.T) {
	seed, err := StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	files := seed.Files
	skin, err := MaterializeSkin("Full Skin", "maker", []SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:Full Skin\n")},
		{Filename: "cursor.png", Bytes: []byte("full-skin-cursor-bytes")},
	}, files)
	if err != nil {
		t.Fatalf("MaterializeSkin: %v", err)
	}
	seed.Skins = []SeedSkin{skin}
	seed.Files = files

	ref := mustBuild(t, ForkLazer, VersionLazer, seed)
	o := mustOpenDyn(t, ref.RealmPath)

	// 内置皮肤固定 Guid 逐个可寻（Protected 内置，lazer-baseline §2.10）。
	for _, b := range builtinSkins {
		if b.guid == "" {
			t.Fatal("内置皮肤 Guid 常量为空")
		}
	}
	skinCk := mustClassKeyRO(o, "Skin")
	skinHashKey := mustPropKeyRO(o, skinCk, "Hash")
	skinGuidKey := mustPropKeyRO(o, skinCk, "ID")
	guidSeen := map[string]bool{}
	if err := o.r.ForEach(skinCk, func(obj unsafe.Pointer) error {
		g, err := o.r.GetGuid(obj, skinGuidKey)
		if err != nil {
			return err
		}
		guidSeen[strings.ToLower(hex.EncodeToString(g[:]))] = true
		return nil
	}); err != nil {
		t.Fatalf("遍历 Skin: %v", err)
	}
	for _, b := range builtinSkins {
		want := strings.ToLower(strings.ReplaceAll(b.guid, "-", ""))
		if !guidSeen[want] {
			t.Errorf("内置皮肤固定 Guid %s 未播（实播集含 %d 个）", b.guid, len(guidSeen))
		}
	}

	// 用户皮肤行：Hash/Name 可寻（usage 列表细节由收集集成测试往返断言）。
	skinObj, err := o.r.FindFirstByString(skinCk, skinHashKey, skin.Hash)
	if err != nil {
		t.Fatalf("寻用户皮肤 %s: %v", skin.Hash, err)
	}
	if skinObj == nil {
		t.Fatalf("用户皮肤 Hash=%s 未播种", skin.Hash)
	}
	o.r.ReleaseObject(skinObj)

	// 难度结构列：StarRating（double）+ Ruleset 链接 → ShortName。
	bmCk := mustClassKeyRO(o, "Beatmap")
	bm := seed.BeatmapSets[0].Beatmaps[0]
	bmHashKey := mustPropKeyRO(o, bmCk, "Hash")
	bmObj, err := o.r.FindFirstByString(bmCk, bmHashKey, bm.Hash)
	if err != nil {
		t.Fatalf("寻难度: %v", err)
	}
	defer o.r.ReleaseObject(bmObj)
	srKey := mustPropKeyRO(o, bmCk, "StarRating")
	if got, err := o.r.GetFloat64(bmObj, srKey); err != nil || got != bm.StarRating {
		t.Errorf("Beatmap.StarRating = %v(%v)，期望 %v", got, err, bm.StarRating)
	}
	rsKey := mustPropKeyRO(o, bmCk, "Ruleset")
	rs, err := o.r.GetLinkedObject(bmObj, rsKey)
	if err != nil || rs == nil {
		t.Fatalf("Beatmap.Ruleset 链接 = (%v,%v)，期望挂接", rs != nil, err)
	}
	defer o.r.ReleaseObject(rs)
	rsCk := mustClassKeyRO(o, "Ruleset")
	snKey := mustPropKeyRO(o, rsCk, "ShortName")
	if got, err := o.r.GetString(rs, snKey); err != nil || got != "osu" {
		t.Errorf("难度 Ruleset.ShortName = %q(%v)，期望 osu", got, err)
	}

	// 成绩结构列：User embedded Username + PP（nullable double 已写值）。
	scCk := mustClassKeyRO(o, "Score")
	scObj, err := o.r.FirstObject(scCk)
	if err != nil {
		t.Fatalf("FirstObject(Score): %v", err)
	}
	defer o.r.ReleaseObject(scObj)
	userKey := mustPropKeyRO(o, scCk, "User")
	user, err := o.r.GetLinkedObject(scObj, userKey)
	if err != nil || user == nil {
		t.Fatalf("Score.User embedded = (%v,%v)，期望挂接", user != nil, err)
	}
	defer o.r.ReleaseObject(user)
	ruCk := mustClassKeyRO(o, "RealmUser")
	unKey := mustPropKeyRO(o, ruCk, "Username")
	if got, err := o.r.GetString(user, unKey); err != nil || got != seed.Scores[0].User.Username {
		t.Errorf("Score.User.Username = %q(%v)，期望 %q", got, err, seed.Scores[0].User.Username)
	}
	ppKey := mustPropKeyRO(o, scCk, "PP")
	if seed.Scores[0].PP == nil {
		t.Fatal("布置失败：标准种子 PP 应非空（MaterializeScore 默认）")
	}
	if got, err := o.r.GetFloat64(scObj, ppKey); err != nil || got != *seed.Scores[0].PP {
		t.Errorf("Score.PP = %v(%v)，期望 %v", got, err, *seed.Scores[0].PP)
	}
	pausesKey := mustPropKeyRO(o, scCk, "Pauses")
	pauses, err := o.r.GetList(scObj, pausesKey)
	if err != nil {
		t.Fatalf("GetList(Pauses): %v", err)
	}
	defer pauses.Close()
	if n, err := pauses.Size(); err != nil || int(n) != len(seed.Scores[0].Pauses) {
		t.Errorf("Score.Pauses 长度 = %d(%v)，期望 %d", n, err, len(seed.Scores[0].Pauses))
	}
}

func mustClassKeyRO(o *openedRO, class string) uint32 {
	o.t.Helper()
	ck, err := o.r.FindClass(class)
	if err != nil {
		o.t.Fatalf("FindClass(%s): %v", class, err)
	}
	return ck
}

func mustPropKeyRO(o *openedRO, classKey uint32, prop string) int64 {
	o.t.Helper()
	key, err := o.r.FindProperty(classKey, prop)
	if err != nil {
		o.t.Fatalf("FindProperty(%d, %s): %v", classKey, prop, err)
	}
	return key
}

func TestBuildDeltaGimmick(t *testing.T) {
	seed, err := DeltaGimmickSeed()
	if err != nil {
		t.Fatalf("DeltaGimmickSeed: %v", err)
	}
	ref := mustBuild(t, ForkDelta, VersionDelta, seed)

	o := mustOpenDyn(t, ref.RealmPath)
	if got := o.r.SchemaVersion(); got != VersionDelta {
		t.Fatalf("delta 磁盘版本 = %d，期望 51", got)
	}
	if _, err := o.r.FindClass("RealmOnlineAsset"); err == nil {
		t.Error("delta 51 不应有 RealmOnlineAsset")
	}
	if got := o.count("BeatmapSet"); got != 2 {
		t.Errorf("BeatmapSet 行数 = %d，期望 2（标准 + gimmick 集）", got)
	}
	// gimmick section 数据全在 .osu 文件内（fork-delta §5.1）。
	gHash := seed.BeatmapSets[1].Beatmaps[0].Hash
	b, err := os.ReadFile(filepath.Join(ref.DataDir, "files", gHash[:1], gHash[:2], gHash))
	if err != nil {
		t.Fatalf("gimmick .osu 落盘缺失: %v", err)
	}
	if !strings.Contains(string(b), "[BeatmapSectionGimmicks]") {
		t.Errorf("gimmick .osu 缺 [BeatmapSectionGimmicks] section:\n%s", b)
	}
	if !strings.Contains(string(b), "EnableHPGimmick=True") {
		t.Errorf("gimmick .osu 缺示例 gimmick 行")
	}
}

func TestBuildEzDiff(t *testing.T) {
	extRoot := filepath.Join(t.TempDir(), "external", "bms-lib")
	seed, err := EzDiffSeed(extRoot)
	if err != nil {
		t.Fatalf("EzDiffSeed: %v", err)
	}
	ref := mustBuild(t, ForkEz2Lazer, VersionEzDisk, seed)

	if !strings.HasSuffix(ref.RealmPath, "client_52011.realm") {
		t.Errorf("Ez 库文件名 = %q，期望 client_52011.realm（fork-ez2lazer §2.2）", ref.RealmPath)
	}
	if ref.UpstreamVersion != 52 || ref.ForkRev != 11 {
		t.Errorf("ez 版本解码 = (%d,%d)，期望 (52,11)", ref.UpstreamVersion, ref.ForkRev)
	}

	o := mustOpenDyn(t, ref.RealmPath)
	if got := o.r.SchemaVersion(); got != VersionEzDisk {
		t.Fatalf("ez 磁盘版本 = %d，期望 52011", got)
	}
	if got := o.count("BeatmapSet"); got != 3 {
		t.Errorf("BeatmapSet 行数 = %d，期望 3（标准 + BMS + 外部托管）", got)
	}
	for class := range map[string]bool{
		"EzBeatmapSkillValue": true, "EzPlayerSkillValue": true, "EzDanEstimate": true,
		"EzPlayerSkillHistoryPoint": true, "EzBeatmapChartSkillInfo": true,
		"EzPlayerDanSkillsetValue": true, "EzBeatmapChartDan": true,
	} {
		if got := o.count(class); got != 1 {
			t.Errorf("%s 行数 = %d，期望 1（技能表演示行）", class, got)
		}
	}
	// JudgeMode≠0 成绩（首行即差异夹具行）+ 未知 Statistics 键 + 非官方 mod。
	if got := o.firstInt("Score", "JudgeMode"); got != 31 {
		t.Errorf("Score.JudgeMode = %d，期望 31（EZ2AC）", got)
	}
	if got := o.firstInt("Score", "HealthMode"); got != 34 {
		t.Errorf("Score.HealthMode = %d，期望 34（Ez2Ac）", got)
	}
	stat := o.firstStr("Score", "Statistics")
	if !strings.Contains(stat, "ez_unknown_result") {
		t.Errorf("Score.Statistics 缺未知 HitResult 键: %q", stat)
	}
	if mods := o.firstStr("Score", "Mods"); !strings.Contains(mods, "EZX") {
		t.Errorf("Score.Mods 缺非官方 mod: %q", mods)
	}
	// 外部托管 set：ext:set: Hash 编码 + 路径哈希 RealmFile（不复制字节）。
	extHash := EncodeExtSetHash(extRoot)
	if extHash == seed.BeatmapSets[0].Hash {
		t.Fatal("ext:set: hash 意外等于内容 hash")
	}
	pathHash := PathRealmFileHash(filepath.Join(extRoot, "chart.bms"))
	if _, err := os.Stat(filepath.Join(ref.DataDir, "files", pathHash[:1], pathHash[:2], pathHash)); !os.IsNotExist(err) {
		t.Errorf("外部托管路径哈希不应落盘 files/（RegisterExternalHash 只登记）: err=%v", err)
	}
	// File 行数 = 内容池 + 路径哈希行。
	if got := o.count("File"); got != uint64(len(seed.Files)+1) {
		t.Errorf("File 行数 = %d，期望 %d（内容 %d + 路径哈希 1）", got, len(seed.Files)+1, len(seed.Files))
	}
}

func TestBuildG0v0MarketRuleset(t *testing.T) {
	seed, err := G0v0DiffSeed()
	if err != nil {
		t.Fatalf("G0v0DiffSeed: %v", err)
	}
	ref := mustBuild(t, ForkG0v0, VersionG0v0, seed)

	o := mustOpenDyn(t, ref.RealmPath)
	if got := o.count("Ruleset"); got != 5 {
		t.Errorf("Ruleset 行数 = %d，期望 5（官方四 + 市场 1）", got)
	}
	if got := o.count("Score"); got != 2 {
		t.Errorf("Score 行数 = %d，期望 2", got)
	}
	// 首行即差异夹具行：指向市场规则集的成绩。
	if got := o.firstInt("Score", "OnlineID"); got != 99001 {
		t.Errorf("Score.OnlineID = %d，期望 99001（g0v0 服务器本地标识）", got)
	}
	// g0v0 与官方 schema 全等（fork-g0v0 §2）：无 Ez 扩展列。
	if _, err := o.r.FindProperty(mustClassKey(o, "Score"), "JudgeMode"); err == nil {
		t.Error("g0v0 Score 不应含 Ez 扩展列 JudgeMode")
	}
}

func mustClassKey(o *openedRO, class string) uint32 {
	o.t.Helper()
	ck, err := o.r.FindClass(class)
	if err != nil {
		o.t.Fatalf("FindClass(%s): %v", class, err)
	}
	return ck
}

func TestVersionChangeFixture(t *testing.T) {
	v1, v2, err := VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	if v1.BeatmapSets[0].OnlineID != v2.BeatmapSets[0].OnlineID {
		t.Fatal("版本变更夹具必须同 OnlineID（HB1 谱面身份键）")
	}
	if v1.BeatmapSets[0].Hash == v2.BeatmapSets[0].Hash {
		t.Fatal("版本变更夹具必须不同 Hash（版本指纹）")
	}
	ref1 := mustBuild(t, ForkLazer, VersionLazer, v1)
	ref2 := mustBuild(t, ForkDelta, VersionDelta, v2) // 跨端同身份
	o1, o2 := mustOpenDyn(t, ref1.RealmPath), mustOpenDyn(t, ref2.RealmPath)
	if got := o1.firstInt("BeatmapSet", "OnlineID"); got != 2002 {
		t.Errorf("v1 OnlineID = %d，期望 2002", got)
	}
	if got := o2.firstInt("BeatmapSet", "OnlineID"); got != 2002 {
		t.Errorf("v2 OnlineID = %d，期望 2002（同谱面身份）", got)
	}
	if o1.firstStr("BeatmapSet", "Hash") == o2.firstStr("BeatmapSet", "Hash") {
		t.Error("v1/v2 库的集 Hash 应不同（同 OnlineID 不同版本，HB1 版本更新用例输入）")
	}
}

func TestDeletionFixture(t *testing.T) {
	before, after, err := DeletionSeeds()
	if err != nil {
		t.Fatalf("DeletionSeeds: %v", err)
	}
	refB := mustBuild(t, ForkLazer, VersionLazer, before)
	refA := mustBuild(t, ForkLazer, VersionLazer, after)
	oB, oA := mustOpenDyn(t, refB.RealmPath), mustOpenDyn(t, refA.RealmPath)
	if got := oB.count("BeatmapSet"); got != 2 {
		t.Errorf("before BeatmapSet 行数 = %d，期望 2", got)
	}
	if got := oA.count("BeatmapSet"); got != 1 {
		t.Errorf("after BeatmapSet 行数 = %d，期望 1（删除后仅剩保留集，HB2 tombstone 用例输入）", got)
	}
	if got := oA.firstStr("BeatmapSet", "Hash"); got != after.BeatmapSets[0].Hash {
		t.Errorf("after 保留集 Hash = %q，期望 %q", got, after.BeatmapSets[0].Hash)
	}
}

func TestBuildValidatesSeedConsistency(t *testing.T) {
	seed, err := StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	// 哈希与字节不一致。
	bad := seed
	bad.Files = map[string][]byte{"deadbeef": []byte("x")}
	if _, err := Build(context.Background(), Spec{ForkID: ForkLazer, SchemaVersion: VersionLazer, DataDir: t.TempDir(), Seed: bad}); err == nil {
		t.Error("Files 键与内容不一致应报错")
	}
	// score 挂到不存在的难度。
	bad2 := seed
	bad2.Scores = []SeedScore{{BeatmapHash: "not-exist"}}
	if _, err := Build(context.Background(), Spec{ForkID: ForkLazer, SchemaVersion: VersionLazer, DataDir: t.TempDir(), Seed: bad2}); err == nil {
		t.Error("Score.BeatmapHash 未命中难度应报错")
	}
	// Ez 专有值落在官方端。
	bad3 := seed
	sc := seed.Scores[0]
	sc.JudgeMode = 31
	bad3.Scores = []SeedScore{sc}
	if _, err := Build(context.Background(), Spec{ForkID: ForkLazer, SchemaVersion: VersionLazer, DataDir: t.TempDir(), Seed: bad3}); err == nil {
		t.Error("非 Ez 端携带 JudgeMode≠0 应报错")
	}
}
