package legacydb

import (
	"fmt"
)

// ParseBeatmapDB 解析 osu!.db（stable-legacy §1.2/§1.3）。版本分叉按
// 文档处理（<20140609 / <20191106 / 星数 pair 形态）；解析完允许尾部有
// 剩余字节（未来版本可能追加尾部字段，前向兼容），调用方按需校验。
func ParseBeatmapDB(data []byte) (*BeatmapDB, error) {
	r := &reader{data: data}
	db := &BeatmapDB{}
	var err error
	if db.Version, err = r.int32(); err != nil {
		return nil, err
	}
	if db.FolderCount, err = r.int32(); err != nil {
		return nil, err
	}
	if db.AccountUnlocked, err = r.boolean(); err != nil {
		return nil, err
	}
	var unlock uint64
	if unlock, err = r.u64(); err != nil {
		return nil, err
	}
	db.DateUnlocked = dotNetTicks(unlock)
	if db.PlayerName, err = r.str(); err != nil {
		return nil, err
	}
	var count uint32
	if count, err = r.u32(); err != nil {
		return nil, err
	}
	for i := uint32(0); i < count; i++ {
		entry, err := parseBeatmapEntry(r, db.Version)
		if err != nil {
			return nil, fmt.Errorf("第 %d 条难度: %w", i, err)
		}
		db.Beatmaps = append(db.Beatmaps, entry)
	}
	// 尾部用户权限（stable-legacy §1.2）；缺失时保留已解析主体。
	if perms, err := r.u32(); err == nil {
		db.UserPermissions = int(perms)
	}
	db.Trailing = len(r.data) - r.pos
	return db, nil
}

// parseBeatmapEntry 按版本分叉解析单个难度条目（stable-legacy §1.3）。
func parseBeatmapEntry(r *reader, version int) (e BeatmapEntry, err error) {
	oldDiff := version < verByteDifficulties // Byte 难度 + 未知 Short 形态
	if version < verNoEntrySize {
		// 条目字节大小（仅旧版读取器用来跳条目），不保留。
		if _, err = r.u32(); err != nil {
			return e, err
		}
	}
	strs := []*string{
		&e.Artist, &e.ArtistUnicode, &e.Title, &e.TitleUnicode,
		&e.Creator, &e.DifficultyName, &e.AudioFilename,
		&e.MD5Hash, &e.OsuFilename,
	}
	for _, dst := range strs {
		if *dst, err = r.str(); err != nil {
			return e, err
		}
	}
	if e.Status, err = r.int8(); err != nil {
		return e, err
	}
	if e.CircleCount, err = r.int16(); err != nil {
		return e, err
	}
	if e.SliderCount, err = r.int16(); err != nil {
		return e, err
	}
	if e.SpinnerCount, err = r.int16(); err != nil {
		return e, err
	}
	var lastMod uint64
	if lastMod, err = r.u64(); err != nil {
		return e, err
	}
	e.LastModified = dotNetTicks(lastMod)
	if oldDiff {
		// v<20140609：AR/CS/HP/OD 为 Byte（stable-legacy §1.3）。
		for _, dst := range []*float32{&e.AR, &e.CS, &e.HP, &e.OD} {
			var v byte
			if v, err = r.u8(); err != nil {
				return e, err
			}
			*dst = float32(v)
		}
	} else {
		if e.AR, err = r.single(); err != nil {
			return e, err
		}
		if e.CS, err = r.single(); err != nil {
			return e, err
		}
		if e.HP, err = r.single(); err != nil {
			return e, err
		}
		if e.OD, err = r.single(); err != nil {
			return e, err
		}
	}
	if e.SliderVelocity, err = r.double(); err != nil {
		return e, err
	}
	if !oldDiff {
		// 四模式星数表：v>=20250107 为 Int-Float pair，此前 Int-Double
		//（taggedPair 按 tag 自适应，分叉点只约束语义，读侧两者兼容）。
		for mode := 0; mode < 4; mode++ {
			var n uint32
			if n, err = r.u32(); err != nil {
				return e, err
			}
			for i := uint32(0); i < n; i++ {
				var mods int
				var stars float64
				if mods, stars, err = r.taggedPair(); err != nil {
					return e, fmt.Errorf("星数表 mode=%d 第 %d 对: %w", mode, i, err)
				}
				e.StarRatings[mode] = append(e.StarRatings[mode],
					StarRating{Mods: mods, Stars: stars})
			}
		}
	}
	if e.DrainTimeSec, err = r.int32(); err != nil {
		return e, err
	}
	if e.TotalTimeMs, err = r.int32(); err != nil {
		return e, err
	}
	if e.PreviewTimeMs, err = r.int32(); err != nil {
		return e, err
	}
	var tpCount uint32
	if tpCount, err = r.u32(); err != nil {
		return e, err
	}
	for i := uint32(0); i < tpCount; i++ {
		var bpm, offset float64
		var rawInherited bool
		if bpm, err = r.double(); err != nil {
			return e, err
		}
		if offset, err = r.double(); err != nil {
			return e, err
		}
		if rawInherited, err = r.boolean(); err != nil {
			return e, err
		}
		// 布尔位 false = 继承点（stable-legacy §1.1），Inherited 取反映射。
		e.TimingPoints = append(e.TimingPoints,
			TimingPoint{BPM: bpm, OffsetMs: offset, Inherited: !rawInherited})
	}
	if e.DifficultyID, err = r.int32(); err != nil {
		return e, err
	}
	if e.BeatmapID, err = r.int32(); err != nil {
		return e, err
	}
	if e.ThreadID, err = r.int32(); err != nil {
		return e, err
	}
	for i := range e.Grades {
		if e.Grades[i], err = r.int8(); err != nil {
			return e, err
		}
	}
	if e.LocalOffset, err = r.i16(); err != nil {
		return e, err
	}
	if e.StackLeniency, err = r.single(); err != nil {
		return e, err
	}
	if e.Mode, err = r.int8(); err != nil {
		return e, err
	}
	if e.Source, err = r.str(); err != nil {
		return e, err
	}
	if e.Tags, err = r.str(); err != nil {
		return e, err
	}
	if e.OnlineOffset, err = r.i16(); err != nil {
		return e, err
	}
	if e.Font, err = r.str(); err != nil {
		return e, err
	}
	if e.Unplayed, err = r.boolean(); err != nil {
		return e, err
	}
	var lastPlayed uint64
	if lastPlayed, err = r.u64(); err != nil {
		return e, err
	}
	e.LastPlayed = dotNetTicks(lastPlayed)
	if e.IsOsz2, err = r.boolean(); err != nil {
		return e, err
	}
	if e.FolderName, err = r.str(); err != nil {
		return e, err
	}
	var lastChecked uint64
	if lastChecked, err = r.u64(); err != nil {
		return e, err
	}
	e.LastChecked = dotNetTicks(lastChecked)
	bools := []*bool{
		&e.IgnoreSound, &e.IgnoreSkin, &e.DisableStoryboard,
		&e.DisableVideo, &e.VisualOverride,
	}
	for _, dst := range bools {
		if *dst, err = r.boolean(); err != nil {
			return e, err
		}
	}
	if oldDiff {
		if e.UnknownShort, err = r.u16(); err != nil {
			return e, err
		}
	}
	if e.UnknownInt, err = r.u32(); err != nil {
		return e, err
	}
	if e.ManiaScrollSpeed, err = r.int8(); err != nil {
		return e, err
	}
	return e, nil
}
