package deploy

// 谱面集投放验收（design.md §7-M2-2 谱面集/难度投放 + §5.3 链接先行）：
// fixture 双库全链——投放 N 个合成集后目标库行数/内容键集合/逐字段相等、
// 游戏侧 files/ 哈希实体齐全且字节读回校验、结构档案零漂移、重续幂等；
// S8 双向（同域携带 / 异域新行 -1 / 异域更新不触碰该列）；缺列降级 +
// 官方规则集缺行新建分支；版本更新 skip 项呈现（留待块 4）；执行期改判。
// librealm 缺失时显式 SKIP（§8.1 补充门：不得静默绿）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// ---- 读回投影面（collect/project.go 的读法在 deploy 测试侧的直连形态） ----

// setReader 只读会话内的结构寻址缓存（读回逐字段断言的数据源）。
type setReader struct {
	t         *testing.T
	r         *realmc.Realm
	classKeys map[string]uint32
	propKeys  map[string]map[string]int64
}

func newSetReader(t *testing.T, r *realmc.Realm) *setReader {
	return &setReader{t: t, r: r, classKeys: map[string]uint32{}, propKeys: map[string]map[string]int64{}}
}

func (sr *setReader) class(name string) uint32 {
	sr.t.Helper()
	if k, ok := sr.classKeys[name]; ok {
		return k
	}
	k, err := sr.r.FindClass(name)
	if err != nil {
		sr.t.Fatalf("FindClass(%s): %v", name, err)
	}
	sr.classKeys[name] = k
	return k
}

func (sr *setReader) prop(cls, name string) int64 {
	sr.t.Helper()
	pk, ok := sr.propKeys[cls]
	if !ok {
		ck := sr.class(cls)
		props, err := sr.r.Properties(ck)
		if err != nil {
			sr.t.Fatalf("Properties(%s): %v", cls, err)
		}
		pk = map[string]int64{}
		for _, p := range props {
			pk[p.Name] = p.Key
		}
		sr.propKeys[cls] = pk
	}
	key, ok := pk[name]
	if !ok {
		sr.t.Fatalf("类 %s 缺列 %s（读回投影应只用于全列镜像库）", cls, name)
	}
	return key
}

func (sr *setReader) str(obj unsafePointer, cls, name string) string {
	sr.t.Helper()
	v, err := sr.r.GetString(obj, sr.prop(cls, name))
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	return v
}

func (sr *setReader) int64Of(obj unsafePointer, cls, name string) int64 {
	sr.t.Helper()
	v, err := sr.r.GetInt64(obj, sr.prop(cls, name))
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	return v
}

func (sr *setReader) float64Of(obj unsafePointer, cls, name string) float64 {
	sr.t.Helper()
	v, err := sr.r.GetFloat64(obj, sr.prop(cls, name))
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	return v
}

func (sr *setReader) float32Of(obj unsafePointer, cls, name string) float32 {
	sr.t.Helper()
	v, err := sr.r.GetFloat32(obj, sr.prop(cls, name))
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	return v
}

func (sr *setReader) boolOf(obj unsafePointer, cls, name string) bool {
	sr.t.Helper()
	v, err := sr.r.GetBool(obj, sr.prop(cls, name))
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	return v
}

func (sr *setReader) guidOf(obj unsafePointer, cls, name string) string {
	sr.t.Helper()
	b, err := sr.r.GetGuid(obj, sr.prop(cls, name))
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	return formatGuid(b)
}

func (sr *setReader) tsPtrOf(obj unsafePointer, cls, name string) *time.Time {
	sr.t.Helper()
	key := sr.prop(cls, name)
	null, err := sr.r.IsNull(obj, key)
	if err != nil {
		sr.t.Fatalf("判空 %s.%s: %v", cls, name, err)
	}
	if null {
		return nil
	}
	v, err := sr.r.GetTimestamp(obj, key)
	if err != nil {
		sr.t.Fatalf("读 %s.%s: %v", cls, name, err)
	}
	utc := v.UTC()
	return &utc
}

// tsPtrOptOf 可空时间列的可选形态（缺列 / NULL → nil，缺列自然降级）。
func (sr *setReader) tsPtrOptOf(obj unsafePointer, cls, name string) *time.Time {
	sr.t.Helper()
	key, err := sr.r.FindProperty(sr.class(cls), name)
	if err != nil {
		return nil
	}
	null, err := sr.r.IsNull(obj, key)
	if err != nil || null {
		return nil
	}
	v, err := sr.r.GetTimestamp(obj, key)
	if err != nil {
		return nil
	}
	utc := v.UTC()
	return &utc
}

// linked 取单值链接/embedded 的目标行句柄（空链接 fatal——全列镜像库的
// 结构列在投放后应非空），由调用方释放。
func (sr *setReader) linked(obj unsafePointer, cls, prop string) unsafePointer {
	sr.t.Helper()
	target, err := sr.r.GetLinkedObject(obj, sr.prop(cls, prop))
	if err != nil {
		sr.t.Fatalf("读链接 %s.%s: %v", cls, prop, err)
	}
	if target == nil {
		sr.t.Fatalf("链接 %s.%s 为空（投放后应为非空结构）", cls, prop)
	}
	return target
}

// unsafePointer 保持与 realmc 句柄类型一致的本地别名。
type unsafePointer = realmObjectPtr

// readSetProjection 读回整集投影（集标量 + Files usage + 逐难度全列）。
func (sr *setReader) readSetProjection(setHash string) (hub.BeatmapSet, bool) {
	sr.t.Helper()
	ck := sr.class("BeatmapSet")
	obj, err := sr.r.FindFirstByString(ck, sr.prop("BeatmapSet", "Hash"), setHash)
	if err != nil {
		sr.t.Fatalf("查谱面集 %s: %v", setHash, err)
	}
	if obj == nil {
		return hub.BeatmapSet{}, false
	}
	defer sr.r.ReleaseObject(obj)

	bs := hub.BeatmapSet{
		ID:            sr.guidOf(obj, "BeatmapSet", "ID"),
		Hash:          setHash,
		OnlineID:      sr.int64Of(obj, "BeatmapSet", "OnlineID"),
		StatusInt:     int(sr.int64Of(obj, "BeatmapSet", "Status")),
		DateAdded:     derefTime(sr.tsPtrOf(obj, "BeatmapSet", "DateAdded")),
		DateSubmitted: sr.tsPtrOf(obj, "BeatmapSet", "DateSubmitted"),
		DateRanked:    sr.tsPtrOptOf(obj, "BeatmapSet", "DateRanked"),
		DeletePending: sr.boolOf(obj, "BeatmapSet", "DeletePending"),
		Protected:     sr.boolOf(obj, "BeatmapSet", "Protected"),
	}
	// Files usage 列表。
	filesLst, err := sr.r.GetList(obj, sr.prop("BeatmapSet", "Files"))
	if err != nil {
		sr.t.Fatalf("取 Files 列表: %v", err)
	}
	n, _ := filesLst.Size()
	for i := uint64(0); i < n; i++ {
		usage, err := filesLst.GetLinkedObject(i)
		if err != nil || usage == nil {
			sr.t.Fatalf("读 Files[%d]: %v", i, err)
		}
		nf := hub.NamedFile{Filename: sr.str(usage, "RealmNamedFileUsage", "Filename")}
		file := sr.linked(usage, "RealmNamedFileUsage", "File")
		nf.Hash = sr.str(file, "File", "Hash")
		sr.r.ReleaseObject(file)
		sr.r.ReleaseObject(usage)
		bs.Files = append(bs.Files, nf)
	}
	filesLst.Close()
	// Beatmaps 列表（按写入序读回，断言侧按键匹配）。
	bmLst, err := sr.r.GetList(obj, sr.prop("BeatmapSet", "Beatmaps"))
	if err != nil {
		sr.t.Fatalf("取 Beatmaps 列表: %v", err)
	}
	n, _ = bmLst.Size()
	for i := uint64(0); i < n; i++ {
		bmObj, err := bmLst.GetLinkedObject(i)
		if err != nil || bmObj == nil {
			sr.t.Fatalf("读 Beatmaps[%d]: %v", i, err)
		}
		bs.Beatmaps = append(bs.Beatmaps, sr.readBeatmap(bmObj))
		sr.r.ReleaseObject(bmObj)
	}
	bmLst.Close()
	return bs, true
}

func (sr *setReader) readBeatmap(obj unsafePointer) hub.Beatmap {
	sr.t.Helper()
	bm := hub.Beatmap{
		ID:                 sr.guidOf(obj, "Beatmap", "ID"),
		Hash:               sr.str(obj, "Beatmap", "Hash"),
		MD5Hash:            sr.str(obj, "Beatmap", "MD5Hash"),
		OnlineMD5Hash:      sr.str(obj, "Beatmap", "OnlineMD5Hash"),
		DifficultyName:     sr.str(obj, "Beatmap", "DifficultyName"),
		StatusInt:          int(sr.int64Of(obj, "Beatmap", "Status")),
		OnlineID:           int(sr.int64Of(obj, "Beatmap", "OnlineID")),
		Length:             sr.float64Of(obj, "Beatmap", "Length"),
		BPM:                sr.float64Of(obj, "Beatmap", "BPM"),
		StarRating:         sr.float64Of(obj, "Beatmap", "StarRating"),
		EndTimeObjectCount: int(sr.int64Of(obj, "Beatmap", "EndTimeObjectCount")),
		TotalObjectCount:   int(sr.int64Of(obj, "Beatmap", "TotalObjectCount")),
		BeatDivisor:        int(sr.int64Of(obj, "Beatmap", "BeatDivisor")),
		Hidden:             sr.boolOf(obj, "Beatmap", "Hidden"),
		LastLocalUpdate:    sr.tsPtrOf(obj, "Beatmap", "LastLocalUpdate"),
		LastOnlineUpdate:   sr.tsPtrOf(obj, "Beatmap", "LastOnlineUpdate"),
		LastPlayed:         sr.tsPtrOf(obj, "Beatmap", "LastPlayed"),
		UserSettingsOffset: 0,
	}
	if ts := sr.tsPtrOf(obj, "Beatmap", "LastPlayed"); ts != nil {
		bm.LastPlayed = ts
	}
	if key, err := sr.r.FindProperty(sr.class("Beatmap"), "EditorTimestamp"); err == nil {
		if null, err := sr.r.IsNull(obj, key); err == nil && !null {
			if v, err := sr.r.GetFloat64(obj, key); err == nil {
				bm.EditorTimestamp = &v
			}
		}
	}
	// Ruleset 链接 → ShortName。
	rs := sr.linked(obj, "Beatmap", "Ruleset")
	bm.RulesetShortName = sr.str(rs, "Ruleset", "ShortName")
	sr.r.ReleaseObject(rs)
	// Difficulty embedded。
	diff := sr.linked(obj, "Beatmap", "Difficulty")
	bm.Difficulty = hub.Difficulty{
		DrainRate:         sr.float32Of(diff, "BeatmapDifficulty", "DrainRate"),
		CircleSize:        sr.float32Of(diff, "BeatmapDifficulty", "CircleSize"),
		OverallDifficulty: sr.float32Of(diff, "BeatmapDifficulty", "OverallDifficulty"),
		ApproachRate:      sr.float32Of(diff, "BeatmapDifficulty", "ApproachRate"),
		SliderMultiplier:  sr.float64Of(diff, "BeatmapDifficulty", "SliderMultiplier"),
		SliderTickRate:    sr.float64Of(diff, "BeatmapDifficulty", "SliderTickRate"),
	}
	sr.r.ReleaseObject(diff)
	// UserSettings embedded。
	us := sr.linked(obj, "Beatmap", "UserSettings")
	bm.UserSettingsOffset = sr.float64Of(us, "BeatmapUserSettings", "Offset")
	sr.r.ReleaseObject(us)
	// Metadata 行。
	md := sr.linked(obj, "Beatmap", "Metadata")
	bm.Metadata = sr.readMetadata(md)
	sr.r.ReleaseObject(md)
	return bm
}

func (sr *setReader) readMetadata(obj unsafePointer) hub.Metadata {
	sr.t.Helper()
	md := hub.Metadata{
		Title:          sr.str(obj, "BeatmapMetadata", "Title"),
		TitleUnicode:   sr.str(obj, "BeatmapMetadata", "TitleUnicode"),
		Artist:         sr.str(obj, "BeatmapMetadata", "Artist"),
		ArtistUnicode:  sr.str(obj, "BeatmapMetadata", "ArtistUnicode"),
		Source:         sr.str(obj, "BeatmapMetadata", "Source"),
		Tags:           sr.str(obj, "BeatmapMetadata", "Tags"),
		PreviewTime:    int(sr.int64Of(obj, "BeatmapMetadata", "PreviewTime")),
		AudioFile:      sr.str(obj, "BeatmapMetadata", "AudioFile"),
		BackgroundFile: sr.str(obj, "BeatmapMetadata", "BackgroundFile"),
	}
	author := sr.linked(obj, "BeatmapMetadata", "Author")
	md.Author = hub.RealmUser{
		OnlineID:      int(sr.int64Of(author, "RealmUser", "OnlineID")),
		Username:      sr.str(author, "RealmUser", "Username"),
		CountryString: sr.str(author, "RealmUser", "CountryString"),
	}
	sr.r.ReleaseObject(author)
	tagsLst, err := sr.r.GetList(obj, sr.prop("BeatmapMetadata", "UserTags"))
	if err != nil {
		sr.t.Fatalf("取 UserTags: %v", err)
	}
	n, _ := tagsLst.Size()
	for i := uint64(0); i < n; i++ {
		tag, err := tagsLst.GetString(i)
		if err != nil {
			sr.t.Fatalf("读 UserTags[%d]: %v", i, err)
		}
		md.UserTags = append(md.UserTags, tag)
	}
	tagsLst.Close()
	return md
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// guidEqual 归一化比较（读回 formatGuid 为 36 位小写；夹具常量同形态，
// 容忍大小写与连字符差异）。
func guidEqual(a, b string) bool {
	strip := func(s string) string {
		out := make([]byte, 0, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '-' {
				continue
			}
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			out = append(out, c)
		}
		return string(out)
	}
	return strip(a) == strip(b)
}

// ---- 逐字段断言 ----

func tsPtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func namedFilesEqual(a, b []hub.NamedFile) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(nf hub.NamedFile) string { return nf.Hash + "\x00" + nf.Filename }
	seen := map[string]int{}
	for _, nf := range a {
		seen[key(nf)]++
	}
	for _, nf := range b {
		seen[key(nf)]--
		if seen[key(nf)] < 0 {
			return false
		}
	}
	return true
}

// assertSetDeepEqual 逐字段断言。wantOnlineID 是集行 OnlineID 的独立期望
//（S8 异域/重续场景读回值 ≠ canonical 值）；crossRealmCreate 标记「本次
// 写入是异域新建形态」——该形态下难度行 OnlineID 与 Metadata.Author 的
// RealmUser.OnlineID（各难度 canonical 值不同/相同，故不取单值参数）期望
// 均为 -1，其余场景期望各自 canonical 值。
func assertSetDeepEqual(t *testing.T, got, want hub.BeatmapSet, wantOnlineID int64, crossRealmCreate bool) {
	t.Helper()
	if got.ID != want.ID && !guidEqual(got.ID, want.ID) {
		t.Errorf("集 Guid = %s，期望 %s", got.ID, want.ID)
	}
	if got.OnlineID != wantOnlineID {
		t.Errorf("集 OnlineID = %d，期望 %d", got.OnlineID, wantOnlineID)
	}
	if got.StatusInt != want.StatusInt {
		t.Errorf("集 StatusInt = %d，期望 %d", got.StatusInt, want.StatusInt)
	}
	if !got.DateAdded.Equal(want.DateAdded) {
		t.Errorf("集 DateAdded = %v，期望 %v", got.DateAdded, want.DateAdded)
	}
	if !tsPtrEqual(got.DateSubmitted, want.DateSubmitted) {
		t.Errorf("集 DateSubmitted = %v，期望 %v", got.DateSubmitted, want.DateSubmitted)
	}
	if !tsPtrEqual(got.DateRanked, want.DateRanked) {
		t.Errorf("集 DateRanked = %v，期望 %v", got.DateRanked, want.DateRanked)
	}
	if got.DeletePending || got.Protected {
		t.Errorf("集 DeletePending/Protected = %v/%v，期望 false/false", got.DeletePending, got.Protected)
	}
	if !namedFilesEqual(got.Files, want.Files) {
		t.Errorf("集 Files = %v，期望 %v", got.Files, want.Files)
	}
	if len(got.Beatmaps) != len(want.Beatmaps) {
		t.Fatalf("难度数 = %d，期望 %d", len(got.Beatmaps), len(want.Beatmaps))
	}
	byHash := map[string]hub.Beatmap{}
	for _, bm := range want.Beatmaps {
		byHash[bm.Hash] = bm
	}
	for _, bm := range got.Beatmaps {
		wantBM, ok := byHash[bm.Hash]
		if !ok {
			t.Errorf("多出难度 %s（Hash %s）", bm.DifficultyName, bm.Hash)
			continue
		}
		assertBeatmapDeepEqual(t, bm, wantBM, crossRealmCreate)
	}
}

func assertBeatmapDeepEqual(t *testing.T, got, want hub.Beatmap, crossRealmCreate bool) {
	t.Helper()
	if got.ID != want.ID && !guidEqual(got.ID, want.ID) {
		t.Errorf("难度 %s Guid = %s，期望 %s", want.DifficultyName, got.ID, want.ID)
	}
	for _, c := range []struct {
		name string
		a, b string
	}{
		{"MD5Hash", got.MD5Hash, want.MD5Hash},
		{"OnlineMD5Hash", got.OnlineMD5Hash, want.OnlineMD5Hash},
		{"DifficultyName", got.DifficultyName, want.DifficultyName},
		{"RulesetShortName", got.RulesetShortName, want.RulesetShortName},
	} {
		if c.a != c.b {
			t.Errorf("难度 %s %s = %q，期望 %q", want.DifficultyName, c.name, c.a, c.b)
		}
	}
	for _, c := range []struct {
		name string
		a, b int
	}{
		{"StatusInt", got.StatusInt, want.StatusInt},
		{"EndTimeObjectCount", got.EndTimeObjectCount, want.EndTimeObjectCount},
		{"TotalObjectCount", got.TotalObjectCount, want.TotalObjectCount},
		{"BeatDivisor", got.BeatDivisor, want.BeatDivisor},
	} {
		if c.a != c.b {
			t.Errorf("难度 %s %s = %d，期望 %d", want.DifficultyName, c.name, c.a, c.b)
		}
	}
	wantOnlineID := int64(want.OnlineID)
	if crossRealmCreate {
		wantOnlineID = -1
	}
	if int64(got.OnlineID) != wantOnlineID {
		t.Errorf("难度 %s OnlineID = %d，期望 %d", want.DifficultyName, got.OnlineID, wantOnlineID)
	}
	for _, c := range []struct {
		name string
		a, b float64
	}{
		{"Length", got.Length, want.Length},
		{"BPM", got.BPM, want.BPM},
		{"StarRating", got.StarRating, want.StarRating},
		{"UserSettingsOffset", got.UserSettingsOffset, want.UserSettingsOffset},
	} {
		if c.a != c.b {
			t.Errorf("难度 %s %s = %v，期望 %v", want.DifficultyName, c.name, c.a, c.b)
		}
	}
	if got.Hidden != want.Hidden {
		t.Errorf("难度 %s Hidden = %v，期望 %v", want.DifficultyName, got.Hidden, want.Hidden)
	}
	for _, c := range []struct {
		name string
		a, b *time.Time
	}{
		{"LastLocalUpdate", got.LastLocalUpdate, want.LastLocalUpdate},
		{"LastOnlineUpdate", got.LastOnlineUpdate, want.LastOnlineUpdate},
		{"LastPlayed", got.LastPlayed, want.LastPlayed},
	} {
		if !tsPtrEqual(c.a, c.b) {
			t.Errorf("难度 %s %s = %v，期望 %v", want.DifficultyName, c.name, c.a, c.b)
		}
	}
	if !tsPtrEqual(nil, nil) || (got.EditorTimestamp != nil) != (want.EditorTimestamp != nil) {
		t.Errorf("难度 %s EditorTimestamp 空态不符", want.DifficultyName)
	} else if got.EditorTimestamp != nil && *got.EditorTimestamp != *want.EditorTimestamp {
		t.Errorf("难度 %s EditorTimestamp = %v，期望 %v", want.DifficultyName, *got.EditorTimestamp, *want.EditorTimestamp)
	}
	if got.Difficulty != want.Difficulty {
		t.Errorf("难度 %s Difficulty = %+v，期望 %+v", want.DifficultyName, got.Difficulty, want.Difficulty)
	}
	if got.Metadata.Title != want.Metadata.Title || got.Metadata.TitleUnicode != want.Metadata.TitleUnicode ||
		got.Metadata.Artist != want.Metadata.Artist || got.Metadata.ArtistUnicode != want.Metadata.ArtistUnicode ||
		got.Metadata.Source != want.Metadata.Source || got.Metadata.Tags != want.Metadata.Tags ||
		got.Metadata.PreviewTime != want.Metadata.PreviewTime ||
		got.Metadata.AudioFile != want.Metadata.AudioFile || got.Metadata.BackgroundFile != want.Metadata.BackgroundFile {
		t.Errorf("难度 %s Metadata 标量 = %+v，期望 %+v", want.DifficultyName, got.Metadata, want.Metadata)
	}
	if got.Metadata.Author.Username != want.Metadata.Author.Username ||
		got.Metadata.Author.CountryString != want.Metadata.Author.CountryString {
		t.Errorf("难度 %s Author 标量 = %+v，期望 %+v", want.DifficultyName, got.Metadata.Author, want.Metadata.Author)
	}
	wantAuthorOnlineID := int64(want.Metadata.Author.OnlineID)
	if crossRealmCreate {
		wantAuthorOnlineID = -1
	}
	if int64(got.Metadata.Author.OnlineID) != wantAuthorOnlineID {
		t.Errorf("难度 %s Author.OnlineID = %d，期望 %d", want.DifficultyName, got.Metadata.Author.OnlineID, wantAuthorOnlineID)
	}
	if !equalUnordered(got.Metadata.UserTags, want.Metadata.UserTags) {
		t.Errorf("难度 %s UserTags = %v，期望 %v", want.DifficultyName, got.Metadata.UserTags, want.Metadata.UserTags)
	}
}

// fillStore 把夹具字节逐哈希入仓（模拟收集方向收仓后的形态；投放链接
// 先行的源）。
func fillStore(t *testing.T, store *filestore.Store, files map[string][]byte) {
	t.Helper()
	for hash, b := range files {
		src := filepath.Join(t.TempDir(), hash)
		if err := os.WriteFile(src, b, 0o644); err != nil {
			t.Fatalf("落仓源文件 %s: %v", hash, err)
		}
		if err := store.Put(src); err != nil {
			t.Fatalf("入仓 %s: %v", hash, err)
		}
	}
}

// ---- 夹具构造 ----

// buildTwoSetFixture 两个合成集（集 1：2 难度 + 音频；集 2：1 难度）+
// 固定行 Guid（origin_guid 投放断言锚点）。返回种子与集 DTO。
func buildTwoSetFixture(t *testing.T) (fixture.Seed, []hub.BeatmapSet) {
	t.Helper()
	files := map[string][]byte{}
	set1, err := fixture.MaterializeSet(6001, "Alpha Artist", "Alpha Title", "mapperA",
		[]fixture.DiffMaterial{
			{Name: "Normal", OsuBytes: []byte("alpha normal osu bytes")},
			{Name: "Another", OsuBytes: []byte("alpha another osu bytes")},
		}, []byte("alpha audio bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeSet(alpha): %v", err)
	}
	set2, err := fixture.MaterializeSet(6002, "Beta Artist", "Beta Title", "mapperB",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("beta only osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet(beta): %v", err)
	}
	set1.ID = "61111111-1111-4111-8111-111111111111"
	set2.ID = "62222222-2222-4222-8222-222222222222"
	for i := range set1.Beatmaps {
		set1.Beatmaps[i].ID = fmt.Sprintf("61333333-3333-4333-8333-3333333333%02d", i)
	}
	for i := range set2.Beatmaps {
		set2.Beatmaps[i].ID = fmt.Sprintf("62444444-4444-4444-8444-4444444444%02d", i)
	}
	sets := []hub.BeatmapSet{set1, set2}
	return fixture.Seed{BeatmapSets: sets, Files: files}, sets
}

// withSetReader 只读会话执行 fn 后即关（会话不残留，占用探测不受影响）。
func withSetReader(t *testing.T, lib *realmc.Library, path string, fn func(sr *setReader)) {
	t.Helper()
	r, err := lib.OpenDynamicReadOnly(path)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", path, err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	fn(newSetReader(t, r))
}

// classRowCount 某 class 行数。
func classRowCount(t *testing.T, sr *setReader, cls string) uint64 {
	t.Helper()
	n, err := sr.r.ObjectCount(sr.class(cls))
	if err != nil {
		t.Fatalf("ObjectCount(%s): %v", cls, err)
	}
	return n
}

// ---- 核心验收：fixture 双库全链 ----

// TestDeployBeatmapSetFullChain 投放 2 个合成集（lazer → delta）后：
// 行数 / 内容键集合 / 逐字段相等；files/ 哈希实体齐全且字节读回校验；
// 结构档案零漂移；重续幂等（重跑全判「已存在 → 跳过」）。
func TestDeployBeatmapSetFullChain(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	seedA, sets := buildTwoSetFixture(t)
	refA, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(), Seed: seedA,
	})
	if err != nil {
		t.Fatalf("fixture.Build(A): %v", err)
	}
	h := openMemHub(t)
	// 收集入 hub（canonical origin_fork=lazer → ServerRealm "ppy"；delta
	// 亦 "ppy" → 同域，OnlineID 随行携带，S8）。
	applySnap(t, h, hub.Snapshot{ForkID: refA.ForkID, CollectedAt: t0, BeatmapSets: sets})

	// B：delta 空库（fixture 恒播官方四规则集行——难度解析走复用分支）。
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}

	// 仓入字节（模拟收集方向收仓后的形态；CopyMissing 的源，gimmick 头
	// 扫描的读取面）。
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, seedA.Files)

	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("计划项 = %d（期望 2 create）: %+v", len(plan.Items), plan.Items)
	}
	for _, it := range plan.Items {
		if it.Kind != hub.KindBeatmapSet || it.Action != ActionCreate || it.Restore {
			t.Errorf("计划项 = %+v，期望 beatmap_set create 非 Restore", it)
		}
	}

	snapBefore := schemaSnapshotOf(t, lib, refB.RealmPath)
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#1: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 2 || len(report.FailedBatches) != 0 ||
		len(report.Repointed) != 0 || len(report.NotRun) != 0 {
		t.Fatalf("报告 = applied %+v failed %+v repointed %+v notRun %+v",
			report.Applied, report.FailedBatches, report.Repointed, report.NotRun)
	}
	for _, ap := range report.Applied {
		if ap.Action != ActionCreate {
			t.Errorf("applied 动作 = %s，期望 create", ap.Action)
		}
	}
	if len(report.Skipped) != 0 {
		t.Errorf("全列镜像库不应有缺列/跳过计数: %+v", report.Skipped)
	}

	// 结构档案零漂移（DriftGuard 之外的独立第二校验，§8.2-7）。
	if !snapBefore.Equal(schemaSnapshotOf(t, lib, refB.RealmPath)) {
		t.Error("投放前后目标结构档案漂移")
	}

	// 行数与逐字段读回。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "BeatmapSet"); got != 2 {
			t.Errorf("BeatmapSet 行数 = %d（期望 2）", got)
		}
		if got := classRowCount(t, sr, "Beatmap"); got != 3 {
			t.Errorf("Beatmap 行数 = %d（期望 3）", got)
		}
		if got := classRowCount(t, sr, "BeatmapMetadata"); got != 3 {
			t.Errorf("BeatmapMetadata 行数 = %d（期望 3——每难度一行）", got)
		}
		if got := classRowCount(t, sr, "File"); got != uint64(len(seedA.Files)) {
			t.Errorf("File 行数 = %d（期望 %d——全部实体哈希）", got, len(seedA.Files))
		}
		if got := classRowCount(t, sr, "Ruleset"); got != 4 {
			t.Errorf("Ruleset 行数 = %d（期望 4——复用既有官方四规则集行，不新建）", got)
		}
		for _, want := range sets {
			got, ok := sr.readSetProjection(want.Hash)
			if !ok {
				t.Errorf("目标库缺谱面集 %s", want.Hash)
				continue
			}
			assertSetDeepEqual(t, got, want, want.OnlineID, false)
		}
	})

	// files/ 哈希实体齐全且字节读回校验（CopyMissing 复制形态，§5.3）。
	filesDir := filestore.ResolveFilesDir(refB.RealmPath)
	for hash, want := range seedA.Files {
		got, err := os.ReadFile(filepath.Join(filesDir, hash[:1], hash[:2], hash))
		if err != nil {
			t.Errorf("目标 files/ 缺 %s: %v", hash, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("目标 files/ %s 字节与仓内源不一致", hash)
		}
	}

	// 重续幂等：同计划重跑 → 全部「已存在 → 跳过」，零 applied（§3.3）。
	report2, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %d（期望 0）", len(report2.Applied))
	}
	skipTotal := 0
	for _, st := range report2.Skipped {
		skipTotal += st.Count
	}
	if skipTotal != 2 {
		t.Errorf("重跑 skip 计数 = %d（期望 2）: %+v", skipTotal, report2.Skipped)
	}
}

// TestDeployBeatmapSetS8 S8 双向：同域携带；异域新行置 -1；异域更新
// 不触碰 OnlineID 列。
func TestDeployBeatmapSetS8(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(7001, "S8 Artist", "S8 Title", "mapperS8",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("s8 osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	set.ID = "70777777-7777-4777-8777-777777777777"
	set.Beatmaps[0].ID = "70888888-8888-4888-8888-888888888888"

	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})

	// ① 同域（lazer → delta，均 "ppy"）：OnlineID 随行携带。
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0, BeatmapSets: []hub.BeatmapSet{set}})
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := runner.Deploy(ctx, plan, nil); err != nil {
		t.Fatalf("Deploy(同域): %v", err)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(set.Hash)
		if !ok {
			t.Fatal("同域投放后读回缺集")
		}
		assertSetDeepEqual(t, got, set, set.OnlineID, false)
	})

	// 目标 presence 建立（收集语义；投放不写 presence，§4.4）。
	applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0.Add(time.Hour),
		BeatmapSets: []hub.BeatmapSet{set}})

	// ② 异域新行置 -1：canonical 来源换 g0v0（ServerRealm "g0v0.top"），
	// 新集（不同 Hash）投放 → OnlineID 落 -1。
	set2, err := fixture.MaterializeSet(7002, "S8 Artist", "S8 Title II", "mapperS8",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("s8 osu bytes ii")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet(2): %v", err)
	}
	set2.ID = "70999999-9999-4999-8999-999999999999"
	set2.Beatmaps[0].ID = "70000000-0000-4000-8000-000000000002"
	fillStore(t, store, files) // 补 set2 的实体哈希入仓
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkG0v0, CollectedAt: t0.Add(2 * time.Hour),
		BeatmapSets: []hub.BeatmapSet{set2}})
	plan2, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan(异域新行): %v", err)
	}
	if len(plan2.Items) != 1 || plan2.Items[0].Action != ActionCreate {
		t.Fatalf("计划 = %+v（期望 1 项 create）", plan2.Items)
	}
	if _, err := runner.Deploy(ctx, plan2, nil); err != nil {
		t.Fatalf("Deploy(异域新行): %v", err)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(set2.Hash)
		if !ok {
			t.Fatal("异域新行投放后读回缺集")
		}
		assertSetDeepEqual(t, got, set2, -1, true) // S8：异域新行集/难度/Author 三面 OnlineID 均 -1
	})

	// ③ 异域更新不触碰（§0-B2/B4：canonical 首见即定 + 冲突默认不落地
	// ——g0v0 的同名新值只在它自己的 presence，不改 hub canonical；更新
	// 覆盖路径由显式构造的 update 动作项承载，新 canonical 场景换第二个
	// hub）：g0v0 持有集 1 的同 Hash 新值（DateAdded/StatusInt 改）→
	// update → OnlineID 保持同域投放的 7001。
	// presence 是整段快照语义——模拟完整收集必须带上目标现存的全部集。
	applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0.Add(3 * time.Hour),
		BeatmapSets: []hub.BeatmapSet{set, set2}})
	modified := set
	modified.DateAdded = set.DateAdded.Add(48 * time.Hour)
	modified.StatusInt = 3 // Qualified
	h2 := openMemHub(t)
	applySnap(t, h2, hub.Snapshot{ForkID: fixture.ForkG0v0, CollectedAt: t0.Add(4 * time.Hour),
		BeatmapSets: []hub.BeatmapSet{modified}})
	plan3, err := NewPlan(refB.ForkID, []PlanItem{{
		Kind: hub.KindBeatmapSet, ContentKey: set.Hash,
		Action: ActionUpdate, Summary: "谱面集 " + set.Hash,
	}})
	if err != nil {
		t.Fatalf("NewPlan(异域更新): %v", err)
	}
	plan3.Target = refB
	runner3 := NewRunner(Deps{Hub: h2, Files: filestore.NewEngine(store)})
	report3, err := runner3.Deploy(ctx, plan3, nil)
	if err != nil {
		t.Fatalf("Deploy(异域更新): %v", err)
	}
	if len(report3.Applied) != 1 || report3.Applied[0].Action != ActionUpdate {
		t.Errorf("Deploy(异域更新) applied = %+v，期望 1 update", report3.Applied)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(set.Hash)
		if !ok {
			t.Fatal("异域更新后读回缺集 1")
		}
		assertSetDeepEqual(t, got, modified, set.OnlineID, false) // 集行 OnlineID 保持 7001 不被触碰；难度/Author 不动（读回①段同域投放值）
		if got.DateAdded.Equal(set.DateAdded) {
			t.Error("异域更新未覆盖 DateAdded（S8 只豁免 OnlineID 列）")
		}
	})
}

// TestDeployBeatmapSetMissingColumnsAndRulesetSeed 缺列降级 + 官方规则集
// 缺行新建：删两列的 lazer 镜像空库（无 Ruleset 行）投放成功，缺列计数
// 上报，难度解析走「官方四集新建（Available=true）」分支。
func TestDeployBeatmapSetMissingColumnsAndRulesetSeed(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	schema, err := fixture.SchemaFor(fixture.ForkLazer, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	for i, c := range schema.Classes {
		var kept []realmc.PropertyDef
		for _, p := range c.Properties {
			if (c.Name == "BeatmapSet" && p.Name == "DateRanked") ||
				(c.Name == "Beatmap" && p.Name == "EditorTimestamp") {
				continue
			}
			kept = append(kept, p)
		}
		schema.Classes[i].Properties = kept
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("建缺列库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭缺列库: %v", err)
	}
	refB := forks.LibraryRef{
		ForkID: fixture.ForkLazer, DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionLazer, VersionSource: forks.VersionFromOpen,
	}

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(8001, "MC Artist", "MC Title", "mapperMC",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("missing-column osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	set.ID = "80111111-1111-4111-8111-111111111181"
	set.Beatmaps[0].ID = "80222222-2222-4222-8222-222222222282"

	h := openMemHub(t)
	// canonical 来源取 delta（与目标 lazer 同域 "ppy"，OnlineID 断言不受
	// S8 影响；且避免与目标 fork 的 presence 撞库——对账按 fork_id 分键）。
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkDelta, CollectedAt: t0,
		BeatmapSets: []hub.BeatmapSet{set}})
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy(缺列库): %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望 1）", report.Applied)
	}
	// 缺列逐列计数（人话原因）。
	missing := map[string]int{}
	for _, st := range report.Skipped {
		if contains(st.Reason, "BeatmapSet.DateRanked") {
			missing["DateRanked"] += st.Count
		}
		if contains(st.Reason, "Beatmap.EditorTimestamp") {
			missing["EditorTimestamp"] += st.Count
		}
	}
	if missing["DateRanked"] != 1 || missing["EditorTimestamp"] != 1 {
		t.Errorf("缺列计数 = %v（期望各 1），Skipped=%+v", missing, report.Skipped)
	}

	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(set.Hash)
		if !ok {
			t.Fatal("缺列库投放后读回缺集")
		}
		// 缺列不进 DTO：期望值按列缺失自然降级断言。
		want := set
		want.DateRanked = nil
		want.Beatmaps[0].EditorTimestamp = nil
		assertSetDeepEqual(t, got, want, set.OnlineID, false)
		// 官方规则集缺行按需新建：难度引用 osu → 只建 osu（Available=
		// true、OnlineID 对号；不预播四集——按需解析语义）。
		if got := classRowCount(t, sr, "Ruleset"); got != 1 {
			t.Errorf("Ruleset 行数 = %d（期望 1——按难度引用新建 osu）", got)
		}
		ck := sr.class("Ruleset")
		obj, err := sr.r.FindFirstByString(ck, sr.prop("Ruleset", "ShortName"), "osu")
		if err != nil || obj == nil {
			t.Fatalf("查新建的 osu 规则集行: %v", err)
		}
		defer sr.r.ReleaseObject(obj)
		if got := sr.int64Of(obj, "Ruleset", "OnlineID"); got != 0 {
			t.Errorf("新建规则集 OnlineID = %d（期望 0）", got)
		}
		if got := sr.boolOf(obj, "Ruleset", "Available"); !got {
			t.Error("新建规则集 Available = false（期望 true，§5.1-M2）")
		}
	})
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestDeployBeatmapSetRulesetMissingScalarCols Ruleset 表缺标量列（OnlineID/
// Name/InstantiationInfo，保留身份键 ShortName 与 Available）时的官方规则集
// 新建降级：逐列 ok 检查缺列跳过（该列值不写入，缺列计数上报），不再依赖
// 底层 errno=3009 兜底回滚整批（S1 白名单∩自查与 missingColumns 预演一致）。
func TestDeployBeatmapSetRulesetMissingScalarCols(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	schema, err := fixture.SchemaFor(fixture.ForkLazer, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	for i, c := range schema.Classes {
		if c.Name != "Ruleset" {
			continue
		}
		var kept []realmc.PropertyDef
		for _, p := range c.Properties {
			if p.Name == "OnlineID" || p.Name == "Name" || p.Name == "InstantiationInfo" {
				continue
			}
			kept = append(kept, p)
		}
		schema.Classes[i].Properties = kept
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("建 Ruleset 缺列库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭缺列库: %v", err)
	}
	refB := forks.LibraryRef{
		ForkID: fixture.ForkLazer, DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionLazer, VersionSource: forks.VersionFromOpen,
	}

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(8101, "RMC Artist", "RMC Title", "mapperRMC",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("ruleset-missing-col osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	set.ID = "81333333-3333-4333-8333-333333333383"
	set.Beatmaps[0].ID = "81444444-4444-4444-8444-444444444484"

	h := openMemHub(t)
	// canonical 来源取 delta（与目标 lazer 同域 "ppy"，S8 面不干扰）。
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkDelta, CollectedAt: t0,
		BeatmapSets: []hub.BeatmapSet{set}})
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy(Ruleset 缺列库): %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望 1——缺列走降级不整批回滚）", report.Applied)
	}
	missing := map[string]int{}
	for _, st := range report.Skipped {
		for _, col := range []string{"Ruleset.OnlineID", "Ruleset.Name", "Ruleset.InstantiationInfo"} {
			if contains(st.Reason, col) {
				missing[col] += st.Count
			}
		}
	}
	if missing["Ruleset.OnlineID"] != 1 || missing["Ruleset.Name"] != 1 ||
		missing["Ruleset.InstantiationInfo"] != 1 {
		t.Errorf("Ruleset 缺列计数 = %v（期望各 1），Skipped=%+v", missing, report.Skipped)
	}

	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "Ruleset"); got != 1 {
			t.Errorf("Ruleset 行数 = %d（期望 1——按需新建走降级）", got)
		}
		ck := sr.class("Ruleset")
		obj, err := sr.r.FindFirstByString(ck, sr.prop("Ruleset", "ShortName"), "osu")
		if err != nil || obj == nil {
			t.Fatalf("查降级新建的 osu 规则集行: %v", err)
		}
		defer sr.r.ReleaseObject(obj)
		if got := sr.boolOf(obj, "Ruleset", "Available"); !got {
			t.Error("降级新建规则集 Available = false（期望 true——该列未缺）")
		}
	})
}

// TestDeployBeatmapSetVersionReplace 版本更新分类（版本链尾 ≠ 目标
// presence 版本，强键）→ ActionVersionReplace 计划项 → 投放落地 §5.4 三步
// （本测试聚焦集行形态：新版本整链 + 旧集软删保留），重续幂等。
// 成绩解绑（第三步）的全链断言在 itests B1（fixture 播 Score 行 + 现场
// 注入 BeatmapInfo 关系）。
func TestDeployBeatmapSetVersionReplace(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	seedV1, seedV2, err := fixture.VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	v1, v2 := seedV1.BeatmapSets[0], seedV2.BeatmapSets[0]
	// 夹具不设 Guid（版本链用例只关心 Hash），逐字段断言需要固定锚点。
	v2.ID = "71111111-2222-4333-8444-555555555556"
	for i := range v2.Beatmaps {
		v2.Beatmaps[i].ID = fmt.Sprintf("71333333-3333-4333-8333-3333333333%02d", i)
	}

	// 目标库现场播种 v1（对账 presence 与现场一致——收集方向收的就是它）。
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{BeatmapSets: []hub.BeatmapSet{v1}, Files: seedV1.Files},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	// 目标先持有 v1；源收集 v2（同 OnlineID 强键 → 版本链 v1 → v2）。
	applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0, BeatmapSets: []hub.BeatmapSet{v1}})
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0.Add(time.Hour),
		BeatmapSets: []hub.BeatmapSet{v2}})

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, seedV2.Files)

	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("计划项 = %d（期望 1）: %+v", len(plan.Items), plan.Items)
	}
	it := plan.Items[0]
	if it.Kind != hub.KindBeatmapSet || it.Action != ActionVersionReplace {
		t.Fatalf("计划项 = %+v，期望 beatmap_set version-replace", it)
	}
	if it.ContentKey != v2.Hash || it.OldContentKey != v1.Hash {
		t.Errorf("计划项键 = new %s / old %s，期望 new %s / old %s",
			it.ContentKey, it.OldContentKey, v2.Hash, v1.Hash)
	}

	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 1 || report.Applied[0].Action != ActionVersionReplace {
		t.Fatalf("Applied = %+v（期望 1 项 version-replace）", report.Applied)
	}

	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		// 新版本整链逐字段 + 未软删；旧版本行保留且软删（S4：物理清理是
		// 客户端职责）。
		gotV2, ok := sr.readSetProjection(v2.Hash)
		if !ok {
			t.Fatal("版本替换后读回缺新版本集")
		}
		assertSetDeepEqual(t, gotV2, v2, v2.OnlineID, false)
		gotV1, ok := sr.readSetProjection(v1.Hash)
		if !ok {
			t.Fatal("版本替换后旧版本行应保留（软删非物理删）")
		}
		if !gotV1.DeletePending {
			t.Error("旧版本集 DeletePending = false，期望 true（§5.4 三步之②）")
		}
		// 无孤儿难度行：两版本难度各挂各的父集（Beatmap 无独立软删/清理
		// 通道，生命周期挂父集，design §4.3）。
		if got := classRowCount(t, sr, "Beatmap"); got != uint64(len(v1.Beatmaps)+len(v2.Beatmaps)) {
			t.Errorf("Beatmap 行数 = %d（期望 %d——新旧两版本难度各一）", got, len(v1.Beatmaps)+len(v2.Beatmaps))
		}
		if got := classRowCount(t, sr, "BeatmapMetadata"); got != uint64(len(v1.Beatmaps)+len(v2.Beatmaps)) {
			t.Errorf("BeatmapMetadata 行数 = %d（期望 %d）", got, len(v1.Beatmaps)+len(v2.Beatmaps))
		}
		if got := classRowCount(t, sr, "File"); got != uint64(len(seedV1.Files)+len(seedV2.Files)) {
			t.Errorf("File 行数 = %d（期望 %d——新旧版本实体各一）", got, len(seedV1.Files)+len(seedV2.Files))
		}
	})

	// 重续幂等：同计划重跑 → 新在+旧已软删 → 跳过计数，零 applied。
	report2, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %+v（期望 0）", report2.Applied)
	}
	if len(report2.Skipped) != 1 || report2.Skipped[0].Count != 1 {
		t.Errorf("重跑 Skipped = %+v（期望 1 条计数 1）", report2.Skipped)
	}
}

// TestBuildPlanVersionReplaceWeakEvidence 弱键「疑似」版本更新：默认不进
// 计划（NeedsConfirm 语义，§5.4 三选一）；显式确认（hub 链尾键）后按
// version-replace 进入。
func TestBuildPlanVersionReplaceWeakEvidence(t *testing.T) {
	ctx := context.Background()

	files := map[string][]byte{}
	wOld, err := fixture.MaterializeSet(0, "Weak Artist", "Weak Title", "mapperW",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("weak old osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet(old): %v", err)
	}
	wNew, err := fixture.MaterializeSet(0, "Weak Artist", "Weak Title", "mapperW",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("weak new osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet(new): %v", err)
	}

	h := openMemHub(t)
	// 目标持 wOld、源收 wNew（同 meta 弱键、不同 Hash；OnlineID=0 不建链，
	// 两版本并存为链尾——弱键「疑似」配对由对账现场建议）。
	applySnap(t, h, hub.Snapshot{ForkID: "fork-t", CollectedAt: t0, BeatmapSets: []hub.BeatmapSet{wOld}})
	applySnap(t, h, hub.Snapshot{ForkID: "fork-s", CollectedAt: t0.Add(time.Hour),
		BeatmapSets: []hub.BeatmapSet{wNew}})
	ref := forks.LibraryRef{ForkID: "fork-t", RealmPath: "/tmp/weak-plan.client.realm", DiskVersion: 52}
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)

	// 默认计划：弱键项不进（不得以任何动作静默投放——三选一未拍板）。
	planDefault, err := BuildPlan(ctx, h, ref, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan(默认): %v", err)
	}
	if len(planDefault.Items) != 0 {
		t.Fatalf("默认计划 = %+v（期望空：弱键版本更新需用户拍板）", planDefault.Items)
	}

	// 显式确认（以 hub 链尾键为锚）→ version-replace 进入。
	plan, err := BuildPlan(ctx, h, ref, BuildOptions{Files: store, ConfirmVersionReplaceKeys: []string{wNew.Hash}})
	if err != nil {
		t.Fatalf("BuildPlan(确认): %v", err)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("确认后计划 = %+v（期望 1 项）", plan.Items)
	}
	it := plan.Items[0]
	if it.Action != ActionVersionReplace || it.ContentKey != wNew.Hash || it.OldContentKey != wOld.Hash {
		t.Errorf("确认后计划项 = %+v，期望 version-replace new=%s old=%s", it, wNew.Hash, wOld.Hash)
	}
}

// insertBareSet 向目标库现场插入一行最小形态集（Hash + DateAdded +
// Status；deletePending=true 时模拟软删行），模拟计划构建后第三方向目标
// 写入同键行 / 用户游戏内删除（§3.3 执行期现场复核夹具；值刻意异于
// canonical → 走 update 写面）。
func insertBareSet(t *testing.T, lib *realmc.Library, realmPath, hash string, dateAdded time.Time, status int64, deletePending bool) {
	t.Helper()
	r, err := lib.OpenDynamicReadWrite(realmPath)
	if err != nil {
		t.Fatalf("写会话打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginWrite(); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	ck, err := r.FindClass("BeatmapSet")
	if err != nil {
		t.Fatalf("FindClass: %v", err)
	}
	obj, err := r.CreateObject(ck)
	if err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("建行: %v", err)
	}
	defer r.ReleaseObject(obj)
	hashKey, err := r.FindProperty(ck, "Hash")
	if err != nil {
		t.Fatalf("FindProperty(Hash): %v", err)
	}
	dateKey, err := r.FindProperty(ck, "DateAdded")
	if err != nil {
		t.Fatalf("FindProperty(DateAdded): %v", err)
	}
	statusKey, err := r.FindProperty(ck, "Status")
	if err != nil {
		t.Fatalf("FindProperty(Status): %v", err)
	}
	writes := []func() error{
		func() error { return r.SetString(obj, hashKey, hash) },
		func() error { return r.SetTimestamp(obj, dateKey, dateAdded) },
		func() error { return r.SetInt64(obj, statusKey, status) },
	}
	if deletePending {
		dpKey, err := r.FindProperty(ck, "DeletePending")
		if err != nil {
			t.Fatalf("FindProperty(DeletePending): %v", err)
		}
		writes = append(writes, func() error { return r.SetBool(obj, dpKey, true) })
	}
	for _, w := range writes {
		if err := w(); err != nil {
			r.Rollback() //nolint:errcheck
			t.Fatalf("现场注入写入: %v", err)
		}
	}
	if err := r.Commit(); err != nil {
		t.Fatalf("提交注入: %v", err)
	}
}

// TestDeployBeatmapSetRepointed 执行期现场复核：计划 create，但投放前
// 目标已被第三方写入同键行 → 改判 update（报告计数）+ 就地覆盖标量，
// 不新建第二行。
func TestDeployBeatmapSetRepointed(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(9001, "RP Artist", "RP Title", "mapperRP",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("repoint osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	set.ID = "90111111-1111-4111-8111-111111111191"
	set.Beatmaps[0].ID = "90222222-2222-4222-8222-222222222292"

	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0,
		BeatmapSets: []hub.BeatmapSet{set}})
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != ActionCreate {
		t.Fatalf("计划 = %+v（期望 1 create——改判发生在执行期）", plan.Items)
	}
	fillStore(t, store, files)

	// 计划后、投放前：第三方写入同 Hash 裸行（DateAdded/Status 异于
	// canonical）。
	insertBareSet(t, lib, refB.RealmPath, set.Hash, set.DateAdded.Add(72*time.Hour), 4, false)

	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(report.Applied) != 1 || report.Applied[0].Action != ActionUpdate {
		t.Errorf("applied = %+v（期望 1 项 update——create 改判）", report.Applied)
	}
	if len(report.Repointed) != 1 || report.Repointed[0].Reason != "目标已出现同键行：新建 → 就地更新" {
		t.Errorf("Repointed = %+v（期望 1 条新建→就地更新）", report.Repointed)
	}
	// 就地覆盖：单行、DateAdded 回到 canonical，不新建第二行。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "BeatmapSet"); got != 1 {
			t.Errorf("BeatmapSet 行数 = %d（期望 1——改判不建第二行）", got)
		}
		got, ok := sr.readSetProjection(set.Hash)
		if !ok {
			t.Fatal("改判后读回缺集")
		}
		if !got.DateAdded.Equal(set.DateAdded) {
			t.Errorf("改判后 DateAdded = %v，期望覆盖回 canonical %v", got.DateAdded, set.DateAdded)
		}
	})
}

// TestDeployBeatmapSetSoftDeleteGuard HB2 执行面闭环：计划构建后、执行前
// 用户在游戏内删除该谱面（现场同键行 DeletePending=true，tombstone 尚未
// 产生——收集才产 tombstone，计划侧锚拦不住该窗口）。非恢复项遇现场
// 软删行必须跳过防复活（就地更新会把 DeletePending 写回 false 静默复活
// 删除）；恢复项（用户显式勾选「恢复（已删除）」）的复活才是合法意图。
func TestDeployBeatmapSetSoftDeleteGuard(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(9201, "SG Artist", "SG Title", "mapperSG",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("softdelete guard osu bytes")}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	set.ID = "92333333-3333-4333-8333-333333333393"
	set.Beatmaps[0].ID = "92444444-4444-4444-8444-444444444494"

	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0,
		BeatmapSets: []hub.BeatmapSet{set}})
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != ActionCreate {
		t.Fatalf("计划 = %+v（期望 1 create）", plan.Items)
	}
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})

	// 计划后、投放前：游戏内软删（S4 写 DeletePending=true，行保留）。
	goneAt := set.DateAdded.Add(96 * time.Hour)
	insertBareSet(t, lib, refB.RealmPath, set.Hash, goneAt, 1, true)

	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy(现场软删): %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 0 {
		t.Errorf("applied = %+v（期望 0——软删行不复活）", report.Applied)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Count != 1 ||
		!contains(report.Skipped[0].Reason, "防复活") {
		t.Errorf("Skipped = %+v（期望 1 条防复活计数）", report.Skipped)
	}
	if len(report.Repointed) != 1 || report.Repointed[0].Reason != "目标已出现同键行：新建 → 就地更新" {
		t.Errorf("Repointed = %+v（期望 1 条新建→就地更新改判）", report.Repointed)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(set.Hash)
		if !ok {
			t.Fatal("防复活动断后读回缺集行（软删行应保留）")
		}
		if !got.DeletePending {
			t.Error("现场软删集被复活：DeletePending = false，期望保持 true（HB2）")
		}
		if !got.DateAdded.Equal(goneAt) {
			t.Errorf("软删行 DateAdded = %v，期望未被触碰 %v", got.DateAdded, goneAt)
		}
	})

	// 恢复项（用户显式勾选）：软删行写回 DeletePending=false 是明确意图。
	restorePlan, err := NewPlan(refB.ForkID, []PlanItem{{
		Kind: hub.KindBeatmapSet, ContentKey: set.Hash, Action: ActionCreate, Restore: true,
	}})
	if err != nil {
		t.Fatalf("NewPlan(恢复): %v", err)
	}
	restorePlan.Target = refB
	report2, err := runner.Deploy(ctx, restorePlan, nil)
	if err != nil {
		t.Fatalf("Deploy(恢复): %v（report=%+v）", err, report2)
	}
	if len(report2.Applied) != 1 || report2.Applied[0].Action != ActionUpdate {
		t.Errorf("恢复 applied = %+v（期望 1 项 update——软删行现场命中改判）", report2.Applied)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(set.Hash)
		if !ok {
			t.Fatal("恢复后读回缺集行")
		}
		if got.DeletePending {
			t.Error("恢复后 DeletePending = true（期望 false——复活是用户显式意图）")
		}
		if !got.DateAdded.Equal(set.DateAdded) {
			t.Errorf("恢复后 DateAdded = %v，期望 canonical %v", got.DateAdded, set.DateAdded)
		}
	})
}
