package legacydb

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
	"time"
)

// twriter 是测试侧的小端构造器（与 reader 对偶），用于按各版本形态
// 手工生成 osu!.db / collection.db 样本。
type twriter struct{ bytes.Buffer }

func (w *twriter) u8(v byte)    { w.WriteByte(v) }
func (w *twriter) u16(v uint16) { binary.Write(&w.Buffer, binary.LittleEndian, v) }
func (w *twriter) u32(v uint32) { binary.Write(&w.Buffer, binary.LittleEndian, v) }
func (w *twriter) u64(v uint64) { binary.Write(&w.Buffer, binary.LittleEndian, v) }
func (w *twriter) single(v float32) {
	w.u32(math.Float32bits(v))
}
func (w *twriter) double(v float64) {
	w.u64(math.Float64bits(v))
}

// str 与 writeStr 同规则：空串单字节 0x00，非空 0x0b + ULEB128 + UTF-8。
func (w *twriter) str(s string) {
	if s == "" {
		w.u8(0x00)
		return
	}
	w.u8(0x0b)
	v := len(s)
	for v >= 0x80 {
		w.u8(byte(v&0x7f) | 0x80)
		v >>= 7
	}
	w.u8(byte(v))
	w.WriteString(s)
}

func (w *twriter) pair(mods uint32, val float64, float32Tag bool) {
	w.u8(0x08)
	w.u32(mods)
	if float32Tag {
		w.u8(0x0c)
		w.single(float32(val))
	} else {
		w.u8(0x0d)
		w.double(val)
	}
}

// sampleEntry 是构造一条难度条目的取值模板。
var sampleEntry = BeatmapEntry{
	Artist: "Aiobahn feat. KOTOKO", ArtistUnicode: "藍原 feat. 琉姫",
	Title: "INTERNET YAMERO", TitleUnicode: "インターネット・ヤメロ",
	Creator:        "osu!team",
	DifficultyName: "Aspire",
	AudioFilename:  "audio.mp3",
	MD5Hash:        "0123456789abcdef0123456789abcdef",
	OsuFilename:    "yamero (osu!team) [Aspire].osu",
	Status:         4,
	CircleCount:    100, SliderCount: 20, SpinnerCount: 3,
	LastModified: time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC),
	AR:            9.4, CS: 4, HP: 8, OD: 9,
	SliderVelocity: 1.4,
	StarRatings: [4][]StarRating{
		{{Mods: 0, Stars: 7.5}, {Mods: 64, Stars: 9.5}},
	},
	DrainTimeSec: 120, TotalTimeMs: 240000, PreviewTimeMs: 40000,
	TimingPoints: []TimingPoint{{BPM: 175, OffsetMs: 1000, Inherited: true}},
	DifficultyID: 2608395, BeatmapID: 2608394, ThreadID: 123456,
	Grades:      [4]int{9, 9, 0, 9},
	LocalOffset: -12,
	Mode:        2,
	Source:      "stable-legacy", Tags: "yamero test",
	OnlineOffset: 0, Font: "Arial",
	LastPlayed: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	FolderName:  "2608394 Aiobahn feat KOTOKO - INTERNET YAMERO",
	LastChecked: time.Date(2026, 9, 30, 12, 1, 0, 0, time.UTC),
	UnknownInt:  7, ManiaScrollSpeed: 15,
}

// writeEntry 按版本形态序列化一条难度条目（仅测试用，与 parseBeatmapEntry 对偶）。
func writeEntry(w *twriter, version int, e BeatmapEntry) {
	if version < verNoEntrySize {
		w.u32(0) // 条目字节大小（旧版字段）
	}
	for _, s := range []string{e.Artist, e.ArtistUnicode, e.Title, e.TitleUnicode,
		e.Creator, e.DifficultyName, e.AudioFilename, e.MD5Hash, e.OsuFilename} {
		w.str(s)
	}
	w.u8(byte(e.Status))
	w.u16(uint16(e.CircleCount))
	w.u16(uint16(e.SliderCount))
	w.u16(uint16(e.SpinnerCount))
	ticks := func(t time.Time) uint64 {
		return uint64(t.Unix())*1e7 + uint64(t.Nanosecond())/100 + unixEpochTicks
	}
	w.u64(ticks(e.LastModified))
	if version < verByteDifficulties {
		w.u8(byte(e.AR))
		w.u8(byte(e.CS))
		w.u8(byte(e.HP))
		w.u8(byte(e.OD))
	} else {
		w.single(e.AR)
		w.single(e.CS)
		w.single(e.HP)
		w.single(e.OD)
	}
	w.double(e.SliderVelocity)
	if version >= verByteDifficulties {
		for mode := 0; mode < 4; mode++ {
			w.u32(uint32(len(e.StarRatings[mode])))
			for _, sr := range e.StarRatings[mode] {
				w.pair(uint32(sr.Mods), sr.Stars, version >= verFloatStarPairs)
			}
		}
	}
	w.u32(uint32(e.DrainTimeSec))
	w.u32(uint32(e.TotalTimeMs))
	w.u32(uint32(e.PreviewTimeMs))
	w.u32(uint32(len(e.TimingPoints)))
	for _, tp := range e.TimingPoints {
		w.double(tp.BPM)
		w.double(tp.OffsetMs)
		w.u8(boolByte(!tp.Inherited)) // 布尔位 false = 继承点（§1.1）
	}
	w.u32(uint32(e.DifficultyID))
	w.u32(uint32(e.BeatmapID))
	w.u32(uint32(e.ThreadID))
	for _, g := range e.Grades {
		w.u8(byte(g))
	}
	w.u16(uint16(int16(e.LocalOffset))) // 有符号往返
	w.single(e.StackLeniency)
	w.u8(byte(e.Mode))
	w.str(e.Source)
	w.str(e.Tags)
	w.u16(uint16(int16(e.OnlineOffset)))
	w.str(e.Font)
	w.u8(boolByte(e.Unplayed))
	w.u64(ticks(e.LastPlayed))
	w.u8(boolByte(e.IsOsz2))
	w.str(e.FolderName)
	w.u64(ticks(e.LastChecked))
	w.u8(boolByte(e.IgnoreSound))
	w.u8(boolByte(e.IgnoreSkin))
	w.u8(boolByte(e.DisableStoryboard))
	w.u8(boolByte(e.DisableVideo))
	w.u8(boolByte(e.VisualOverride))
	if version < verByteDifficulties {
		w.u16(e.UnknownShort)
	}
	w.u32(e.UnknownInt)
	w.u8(byte(e.ManiaScrollSpeed))
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// buildOsuDB 构造一份单条目 osu!.db（指定版本形态）。
func buildOsuDB(version int, e BeatmapEntry) []byte {
	w := &twriter{}
	w.u32(uint32(version))
	w.u32(1)   // folder count
	w.u8(1)    // account unlocked
	w.u64(0)   // date unlocked（0 = 占位）
	w.str("")  // player
	w.u32(1)   // beatmap count
	writeEntry(w, version, e)
	w.u32(1) // user permissions
	return w.Bytes()
}

func TestParseBeatmapDBModern(t *testing.T) {
	// 20260924 形态（本机样本同代）：Single 难度 + Int-Float 星数。
	db, err := ParseBeatmapDB(buildOsuDB(20260924, sampleEntry))
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if db.Version != 20260924 || db.FolderCount != 1 || !db.AccountUnlocked {
		t.Fatalf("头部字段错: %+v", db)
	}
	if len(db.Beatmaps) != 1 {
		t.Fatalf("条目数 %d", len(db.Beatmaps))
	}
	if db.Trailing != 0 {
		t.Fatalf("Trailing = %d，应精确收官", db.Trailing)
	}
	got := db.Beatmaps[0]
	if !reflect.DeepEqual(got, sampleEntry) {
		t.Fatalf("条目往返不一致:\n got  %+v\n want %+v", got, sampleEntry)
	}
}

func TestParseBeatmapDBLegacyForms(t *testing.T) {
	// 三个历史形态：Byte 难度（<20140609）、条目字节大小（<20191106）、
	// Int-Double 星数（<20250107）。Byte 难度形态下 AR 等按整数截断。
	old := sampleEntry
	old.AR, old.CS, old.HP, old.OD = 9, 4, 8, 9
	old.StarRatings = [4][]StarRating{} // 无星数表形态
	old.UnknownShort = 0
	forms := []struct {
		version int
		entry   BeatmapEntry
	}{{20140608, old}, {20191105, sampleEntry}, {20250106, sampleEntry}}
	for _, f := range forms {
		db, err := ParseBeatmapDB(buildOsuDB(f.version, f.entry))
		if err != nil {
			t.Fatalf("v=%d 解析: %v", f.version, err)
		}
		if db.Trailing != 0 {
			t.Fatalf("v=%d Trailing = %d", f.version, db.Trailing)
		}
		if got := db.Beatmaps[0]; !reflect.DeepEqual(got, f.entry) {
			t.Fatalf("v=%d 条目往返不一致:\n got  %+v\n want %+v", f.version, got, f.entry)
		}
	}
}

func TestParseBeatmapDBTruncated(t *testing.T) {
	full := buildOsuDB(20260924, sampleEntry)
	// 头部/条目中间截断必须报错；尾部用户权限缺失按前向兼容容忍
	//（主体已可投影，UserPermissions 保持零值）。
	for _, cut := range []int{1, 5, 30, len(full) - 12, len(full) - 8} {
		if _, err := ParseBeatmapDB(full[:cut]); err == nil {
			t.Fatalf("截断至 %d 字节应报错", cut)
		}
	}
	db, err := ParseBeatmapDB(full[:len(full)-3])
	if err != nil {
		t.Fatalf("尾权限缺失应容忍: %v", err)
	}
	if db.UserPermissions != 0 {
		t.Fatalf("尾权限缺失时 UserPermissions 应为零，got %d", db.UserPermissions)
	}
}

func TestParseBeatmapDBBadString(t *testing.T) {
	w := &twriter{}
	w.u32(20260924)
	w.u32(0)
	w.u8(1)
	w.u64(0)
	w.u8(0x07) // 非法字符串头
	w.u32(0)
	w.u32(0)
	if _, err := ParseBeatmapDB(w.Bytes()); err == nil {
		t.Fatal("非法字符串头应报错")
	}
}

func TestCollectionRoundTrip(t *testing.T) {
	in := CollectionDB{
		Version: 20260924,
		Collections: []Collection{
			{Name: "favourites", BeatmapMD5s: []string{"aa", "bb", "cc"}},
			{Name: "空成员"}, // BeatmapMD5s nil → 0 个成员
			{Name: "中文名收藏", BeatmapMD5s: []string{"dd"}},
		},
	}
	out, err := ParseCollectionDB(WriteCollectionDB(in))
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if !reflect.DeepEqual(*out, in) {
		t.Fatalf("往返不一致:\n got  %+v\n want %+v", *out, in)
	}
}

func TestCollectionParseMachineEmpty(t *testing.T) {
	// 本机 collection.db 实测 8 字节：版本 20260711 + 0 条收藏。
	raw := []byte{0x67, 0x27, 0x35, 0x01, 0, 0, 0, 0}
	db, err := ParseCollectionDB(raw)
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if db.Version != 20260711 || len(db.Collections) != 0 {
		t.Fatalf("got version=%d collections=%d", db.Version, len(db.Collections))
	}
}

func TestCollectionParseTruncated(t *testing.T) {
	if _, err := ParseCollectionDB([]byte{0x67, 0x27}); err == nil {
		t.Fatal("截断 collection.db 应报错")
	}
}
