// legacy.go 是 stable 端（StorageLegacy）的收集实现：CollectWith 按
// Profile.Storage == forks.StorageLegacy 分派到本文件。与 realm 端的
// 差异（stable-legacy §0/§5）：无库可开——投影 = Songs/ 目录扫描 +
// osu!.db 元数据补充 + collection.db 收藏；快照以目录扫描为准（投放后
// 游戏未导入的目录也立即可见，闭环不依赖游戏跑过一遍），db 只供难度
// MD5/星数等补充字段；实体文件是 Songs/ 下解包目录的普通文件（P4 再加
// Skins/），收仓/回链逐文件走 filestore。
package collect

import (
	"context"
	"crypto/md5" // mimosa-ignore -- osu! 谱面格式规定 MD5 内容哈希（与 osu!.db 列值逐字节一致），非安全用途，已裁定误报
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/legacydb"
)

// collectLegacyWith 是 CollectWith 的 stable 分派体（编排步骤与 realm 端
// 对齐：守卫 → 投影 → 板块圈定 → 入 hub → 文件面按 Mode）。差异：无
// 结构档案段（realm 专属）；game.ini 恒缺席（collectGameIni 的 -1 语义）。
func (r *Runner) collectLegacyWith(ctx context.Context, target forks.LibraryRef, opts Options, prog ProgressFunc) (Report, error) {
	report := Report{}
	if target.ForkID == "" || target.DataDir == "" {
		return report, fmt.Errorf("collect: stable 端目录未解析（ForkID/DataDir 为空）")
	}
	if err := r.deps.Guard.EnsureClearFork(target.ForkID); err != nil {
		return report, err
	}
	snap, err := legacyProject(target.DataDir, prog)
	if err != nil {
		return report, fmt.Errorf("collect: stable 投影 %s: %w", target.DataDir, err)
	}
	filterSnapshotKinds(&snap, opts.Kinds)
	if opts.Kinds != nil {
		snap.Kinds = opts.Kinds
	}
	if opts.Mode == ModeSnapshotOnly {
		snap.PresenceOnly = true
	}
	if prog != nil {
		prog(StageApply, 0, len(snap.Collections)+len(snap.BeatmapSets))
	}
	applyRes, err := r.deps.Hub.ApplySnapshot(ctx, snap)
	if err != nil {
		return report, fmt.Errorf("collect: 入 hub: %w", err)
	}
	report.Entities = applyRes.EntitiesWritten
	report.Skips = snap.Skips

	// game.ini 面：stable 数据目录无此文件，-1 = 从未有的正常态。
	if r.deps.GameIni != nil && opts.GameIni {
		if n, _ := collectGameIni(target, r.deps.GameIni); n < 0 {
			report.IniKeysCollected = -1
			report.Skips = append(report.Skips, hub.SkipStat{
				Reason: "stable 端无 game.ini（板块不适用）", Count: 1,
			})
		}
	}

	// 文件面按 Mode：Songs/ 下实体文件逐个收仓（Skins/ 随 P4 皮肤板块）。
	if prog != nil {
		prog(StageAdopt, 0, 0)
	}
	switch opts.Mode {
	case ModeFull, ModeSourceOnly:
		stats := r.adoptLegacyFiles(target.DataDir, opts.Mode)
		report.FilesAdopted = stats.adopted
		report.FilesSkipped = stats.skipped
		report.AdoptModeCounts = stats.modes
	case ModeSnapshotOnly:
		// 仅目标端：不碰任何文件。
	}
	return report, nil
}

// legacyAdoptStats 是 stable 文件面的计数汇总。
type legacyAdoptStats struct {
	adopted int
	skipped int
	modes   map[filestore.LinkMode]int
}

// adoptLegacyFiles 收仓 Songs/ 与 Skins/ 下全部实体文件。ModeFull = 收仓
// + 原位回链（AdoptFile 的 HB4 四步，与 realm 端 files/ 同语义——字节
// 进仓、游戏侧换成指向仓的链接）；ModeSourceOnly = 只入仓不回链（链接
// 文件直接跳过：能回链说明字节必已在仓）。
func (r *Runner) adoptLegacyFiles(root string, mode Mode) legacyAdoptStats {
	stats := legacyAdoptStats{modes: map[filestore.LinkMode]int{}}
	// 链接能力探测（与 AdoptDir 同纪律）：失败 = 本环境不可收仓，整体
	// 跳过（纯增益操作，只损失磁盘节约，文件留原地）。
	if _, err := r.deps.Files.Probe(); err != nil {
		return stats
	}
	for _, dir := range []string{filepath.Join(root, "Songs"), filepath.Join(root, "Skins")} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 目录不存在（没下过谱面/皮肤）等：正常态，整体静默
			}
			if d.IsDir() || !isRegularOrLink(d.Type()) {
				return nil
			}
			switch mode {
			case ModeFull:
				linkMode, aerr := r.deps.Files.AdoptFile(path)
				if aerr != nil {
					stats.skipped++
					return nil
				}
				stats.adopted++
				stats.modes[linkMode]++
			case ModeSourceOnly:
				if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
					return nil // 已回链文件：字节在仓
				}
				if perr := r.deps.Files.Store.Put(path); perr != nil {
					stats.skipped++
					return nil
				}
				stats.adopted++
			}
			return nil
		})
	}
	return stats
}

// legacyProject 把 stable 安装目录只读投影为 hub.Snapshot：
//   - Songs/ 目录扫描 → BeatmapSets/Beatmaps（真相源；集哈希与难度 Hash
//     按 lazer 同算法复算，跨端同键，stable-legacy §4）；
//   - Skins/ 目录扫描 → Skins（皮肤名 = 目录名；内容键为目录规范化
//     哈希——与 lazer 的 .ini+.json 哈希不同构、各端各为变体，B2）；
//   - osu!.db → 难度补充字段（MD5、星数、AR/CS/HP/OD、音频名等），
//     解析失败只计数不阻断（目录扫描自足）；
//   - collection.db → Collections。
func legacyProject(root string, prog ProgressFunc) (hub.Snapshot, error) {
	snap := hub.Snapshot{
		ForkID:      "stable",
		CollectedAt: time.Now().UTC(),
	}
	skips := map[string]int{}
	count := func(reason string) { skips[reason]++ }

	// osu!.db 元数据索引（FolderName → 难度行）。
	dbIndex := map[string][]legacydb.BeatmapEntry{}
	if data, err := os.ReadFile(filepath.Join(root, "osu!.db")); err == nil {
		db, perr := legacydb.ParseBeatmapDB(data)
		if perr != nil {
			count("osu!.db 解析失败（以目录扫描为准）")
		} else {
			for _, e := range db.Beatmaps {
				dbIndex[e.FolderName] = append(dbIndex[e.FolderName], e)
			}
		}
	} else {
		count("无 osu!.db（以目录扫描为准，投放后未开过游戏属正常）")
	}

	songsDir := filepath.Join(root, "Songs")
	dirEntries, err := os.ReadDir(songsDir)
	if err != nil && !os.IsNotExist(err) {
		return snap, fmt.Errorf("扫描 Songs/: %w", err)
	}
	var setDirs []fs.DirEntry
	for _, e := range dirEntries {
		if e.IsDir() {
			setDirs = append(setDirs, e)
		}
	}
	for i, e := range setDirs {
		if prog != nil {
			prog(StageProject, i, len(setDirs))
		}
		if bs := projectLegacySet(filepath.Join(songsDir, e.Name()), dbIndex[e.Name()], count); bs != nil {
			snap.BeatmapSets = append(snap.BeatmapSets, *bs)
		}
	}

	if data, err := os.ReadFile(filepath.Join(root, "collection.db")); err == nil {
		if cdb, perr := legacydb.ParseCollectionDB(data); perr == nil {
			for _, c := range cdb.Collections {
				snap.Collections = append(snap.Collections, hub.Collection{
					Name:            c.Name,
					BeatmapMD5Hashes: c.BeatmapMD5s,
				})
			}
		} else {
			count("collection.db 解析失败")
		}
	} else {
		count("无 collection.db（从没建过收藏的正常态）")
	}

	// Skins/ 目录扫描：每目录一皮肤（stable 皮肤名即目录名）。skin.ini
	// 存在才认（目录特征——.osk_FILES 之类的解包产物都含 skin.ini）。
	skinsDir := filepath.Join(root, "Skins")
	skinEntries, err := os.ReadDir(skinsDir)
	if err != nil && !os.IsNotExist(err) {
		return snap, fmt.Errorf("扫描 Skins/: %w", err)
	}
	for _, e := range skinEntries {
		if !e.IsDir() {
			continue
		}
		if skin := projectLegacySkin(filepath.Join(skinsDir, e.Name()), e.Name(), count); skin != nil {
			snap.Skins = append(snap.Skins, *skin)
		}
	}

	// 内容键去重（与 realm 投影同防线：同一谱面装成两个 Songs/ 目录时
	// 集哈希相同，presence 主键不容两行；保留首个并计数）。
	dedupeSnapshot(&snap, skips)

	for reason, n := range skips {
		snap.Skips = append(snap.Skips, hub.SkipStat{Reason: reason, Count: n})
	}
	sort.Slice(snap.Skips, func(i, j int) bool { return snap.Skips[i].Reason < snap.Skips[j].Reason })
	return snap, nil
}

// projectLegacySet 投影单个 Songs/ 集目录。dbRows 是 osu!.db 中 FolderName
// 等于该目录名的难度行（可为空——投放后未导入、或 db 损坏）。返回 nil =
// 该目录不是谱面集（无 .osu，已计数）。
func projectLegacySet(dir string, dbRows []legacydb.BeatmapEntry, count func(string)) *hub.BeatmapSet {
	entries, err := os.ReadDir(dir)
	if err != nil {
		count("集目录不可读")
		return nil
	}
	var names []string
	for _, e := range entries {
		if isRegularOrLink(e.Type()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var osuNames []string
	for _, n := range names {
		if strings.HasSuffix(strings.ToLower(n), ".osu") {
			osuNames = append(osuNames, n)
		}
	}
	if len(osuNames) == 0 {
		count("Songs/ 子目录无难度文件（非谱面集，跳过）")
		return nil
	}

	// 集哈希 = 全部 .osu 内容按文件名序拼接的 SHA-256（与 lazer
	// BeatmapSetInfo.Hash 同算法，跨端同键；lazer 侧该值是导入缓存、
	// 谱面更新后可能 stale，判定层以难度 Hash 集合兜底，stable-legacy §4）。
	hasher := sha256.New()
	contents := make(map[string][]byte, len(osuNames))
	for _, n := range osuNames {
		content, rerr := os.ReadFile(filepath.Join(dir, n))
		if rerr != nil {
			count("难度文件不可读")
			return nil
		}
		contents[n] = content
		hasher.Write(content)
	}
	setHash := hex.EncodeToString(hasher.Sum(nil))

	// db 行按 .osu 文件名索引（stable 的 OsuFilename 即集内相对名）。
	byOsuFile := make(map[string]legacydb.BeatmapEntry, len(dbRows))
	for _, row := range dbRows {
		byOsuFile[row.OsuFilename] = row
	}

	bs := &hub.BeatmapSet{Hash: setHash}
	for _, n := range osuNames {
		content := contents[n]
		row, hasRow := byOsuFile[n]
		md5sum := md5.Sum(content) // mimosa-ignore -- osu! 谱面格式规定 MD5，与 osu!.db 列值一致，已裁定误报
		b := hub.Beatmap{
			Hash:     fileSHA256Hex(content),
			MD5Hash:  hex.EncodeToString(md5sum[:]),
			OnlineID: row.BeatmapID,
		}
		if hasRow {
			b.DifficultyName = row.DifficultyName
			b.RulesetShortName = legacyRulesetName(row.Mode)
			b.StarRating = legacyDefaultStars(row)
			b.Difficulty = hub.Difficulty{
				DrainRate:         float32(sane(float64(row.HP))),
				CircleSize:        float32(sane(float64(row.CS))),
				OverallDifficulty: float32(sane(float64(row.OD))),
				ApproachRate:      float32(sane(float64(row.AR))),
				SliderMultiplier:  sane(row.SliderVelocity),
			}
			b.Metadata = hub.Metadata{
				Title: row.Title, TitleUnicode: row.TitleUnicode,
				Artist: row.Artist, ArtistUnicode: row.ArtistUnicode,
				Author: hub.RealmUser{Username: row.Creator},
				Source: row.Source, Tags: row.Tags,
				PreviewTime: row.PreviewTimeMs / 1000,
				AudioFile:   row.AudioFilename,
			}
		} else {
			meta, ok := parseOsuMetadata(content)
			if !ok {
				count("难度文件元数据不可解析（db 无记录）")
				continue
			}
			b.DifficultyName = meta.Version
			b.RulesetShortName = legacyRulesetName(meta.Mode)
			b.OnlineID = int(meta.BeatmapID)
			b.Metadata = hub.Metadata{
				Title: meta.Title, TitleUnicode: meta.TitleUnicode,
				Artist: meta.Artist, ArtistUnicode: meta.ArtistUnicode,
				Author: hub.RealmUser{Username: meta.Creator},
			}
			if bs.OnlineID == 0 {
				bs.OnlineID = meta.BeatmapSetID
			}
		}
		if bs.Artist == "" {
			bs.Artist = b.Metadata.Artist
			bs.Title = b.Metadata.Title
			bs.AuthorUsername = b.Metadata.Author.Username
		}
		bs.Beatmaps = append(bs.Beatmaps, b)
	}
	// 集身份键兜底次序：目录名数字前缀（stable 事实标准）→ .osu 元数据
	//（上面已填）→ 0（弱键 meta: 配对，§4.2 第二层）。
	if id := legacyDirSetID(filepath.Base(dir)); id != 0 {
		bs.OnlineID = id
	}

	// Files = 目录内全部常规文件（Filename 为集内相对名，与 lazer 端
	// RealmNamedFileUsage.Filename 同形态）。
	for _, n := range names {
		var h string
		if c, ok := contents[n]; ok {
			h = fileSHA256Hex(c)
		} else if content, rerr := os.ReadFile(filepath.Join(dir, n)); rerr == nil {
			h = fileSHA256Hex(content)
		} else {
			count("集内文件不可读")
			continue
		}
		bs.Files = append(bs.Files, hub.NamedFile{Hash: h, Filename: n})
	}
	sort.Slice(bs.Files, func(i, j int) bool { return bs.Files[i].Filename < bs.Files[j].Filename })
	return bs
}

// projectLegacySkin 投影单个 Skins/ 皮肤目录。内容键 = 规范化目录哈希
//（全部文件按相对路径排序，每项 "relpath\n"+文件 SHA-256 拼接后整体
// SHA-256）——与 lazer 的 .ini+.json 哈希不同构，stable 皮肤作为该端
// 变体自有其键（B2 各端分别存；跨端配对走 Name 弱键，stable-legacy §4）。
func projectLegacySkin(dir, name string, count func(string)) *hub.Skin {
	if _, err := os.Stat(filepath.Join(dir, "skin.ini")); err != nil {
		count("Skins/ 子目录无 skin.ini（非皮肤目录，跳过）")
		return nil
	}
	type skinFile struct{ rel, hash string }
	var files []skinFile
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !isRegularOrLink(d.Type()) {
			return nil
		}
		content, rerr := os.ReadFile(p)
		if rerr != nil {
			count("皮肤文件不可读（跳过该文件）")
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, skinFile{rel: rel, hash: fileSHA256Hex(content)})
		return nil
	})
	if len(files) == 0 {
		count("皮肤目录无实体文件（跳过）")
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	hasher := sha256.New()
	skin := &hub.Skin{Name: name, Creator: legacySkinIniField(filepath.Join(dir, "skin.ini"), "Creator")}
	for _, f := range files {
		hasher.Write([]byte(f.rel + "\n" + f.hash + "\n"))
		skin.Files = append(skin.Files, hub.NamedFile{Hash: f.hash, Filename: f.rel})
	}
	skin.Hash = hex.EncodeToString(hasher.Sum(nil))
	return skin
}

// legacySkinIniField 读 skin.ini [General] 节的指定键（缺失返回空）。
func legacySkinIniField(path, key string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	section := ""
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if section != "[General]" {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseOsuMetadata 是 .osu 文件元数据的最小解析面（[Metadata] 节 +
// [General] 的 Mode 键）。db 无记录的目录（投放后未导入、手动放入）经
// 此获得谱面身份；Version 与 Title 均缺才判失败（部分键缺省容忍）。
func parseOsuMetadata(content []byte) (osuMeta, bool) {
	m := osuMeta{Mode: -1}
	section := ""
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch section {
		case "[Metadata]":
			switch key {
			case "Artist":
				m.Artist = val
			case "ArtistUnicode":
				m.ArtistUnicode = val
			case "Title":
				m.Title = val
			case "TitleUnicode":
				m.TitleUnicode = val
			case "Creator":
				m.Creator = val
			case "Version":
				m.Version = val
			case "BeatmapID":
				m.BeatmapID, _ = strconv.ParseInt(val, 10, 64)
			case "BeatmapSetID":
				m.BeatmapSetID, _ = strconv.ParseInt(val, 10, 64)
			}
		case "[General]":
			if key == "Mode" {
				if v, perr := strconv.Atoi(val); perr == nil {
					m.Mode = v
				}
			}
		}
	}
	if m.Version == "" && m.Title == "" {
		return m, false
	}
	if m.Mode < 0 {
		m.Mode = 0 // 缺 Mode 键 = osu! 模式（.osu 规范默认值）
	}
	return m, true
}

// osuMeta 是 .osu [Metadata]/[General] 解析结果。
type osuMeta struct {
	Artist, ArtistUnicode   string
	Title, TitleUnicode     string
	Creator, Version        string
	BeatmapID, BeatmapSetID int64
	Mode                    int
}

// legacyRulesetName 把 stable 模式数字映射为 lazer 的 ShortName
//（hub 端值域 osu/taiko/fruits/mania，与本机 lazer 真库实测一致）。
func legacyRulesetName(mode int) string {
	switch mode {
	case 1:
		return "taiko"
	case 2:
		return "fruits"
	case 3:
		return "mania"
	default:
		return "osu"
	}
}

// legacyDefaultStars 取该模式星数表中 mods=0 的无 mod 星数（无或 NaN
// 则 0；展示字段，非判定键——真机样本实测 db 中存在 NaN 星数，JSON
// 无法序列化，必须归零）。
func legacyDefaultStars(row legacydb.BeatmapEntry) float64 {
	if row.Mode < 0 || row.Mode > 3 {
		return 0
	}
	for _, sr := range row.StarRatings[row.Mode] {
		if sr.Mods == 0 {
			return sane(sr.Stars)
		}
	}
	return 0
}

// sane 把 db 浮点字段的 NaN/Inf 净化为 0（真机样本实测存在 NaN 星数与
// 难度值；JSON 无法序列化这些值，且它们均非判定字段）。
func sane(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// legacyDirSetID 解析 Songs/ 目录名的数字前缀（"<setID> <artist> - <title>"
// 是 stable 事实标准命名；无数字前缀 = 未提交谱面，返回 0 走弱键）。
func legacyDirSetID(dirName string) int64 {
	prefix, _, _ := strings.Cut(dirName, " ")
	id, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// isRegularOrLink 判定目录项是否为实体文件：常规文件或符号链接。stable
// 端收集/投放采用链接形态持有字节（回链后 Songs//Skins/ 内均为链接），
// DirEntry.Type() 是链接自身的类型位、不含 IsRegular——链接必须显式放行
//（读侧 os.ReadFile 跟随链接取内容，语义等价）。
func isRegularOrLink(t fs.FileMode) bool {
	return t.IsRegular() || t&os.ModeSymlink != 0
}

// fileSHA256Hex 是内容 SHA-256 的 hex（stable 侧内容键形态，与 filestore
// 及 lazer 端 64 位小写 hex 同构）。
func fileSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
