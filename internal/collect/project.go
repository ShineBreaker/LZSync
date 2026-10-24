package collect

// 只读投影（design §4.1）：动态只读会话 → hub.Snapshot。跳过集逐条计数
// （不静默吞，§1.5-4）；目标库缺某列时该列不进 DTO（列缺失自然降级，
// ezrealmsync-design §3.3）。M1 类型面 = collection / beatmap_set /
// beatmap（hub schema v1）；skin 的收集（含内置皮肤与 fork 专有皮肤
// 跳过）随 hub 后续 schema 版本启用，当前不打开 Skin 类。

import (
	"encoding/hex"
	"fmt"
	"sort"
	"time"
	"unsafe"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// 投影跳过原因（稳定字符串，进 hub.SkipStat 与 collect_log）。
const (
	skipSoftDeleted  = "软删谱面集（DeletePending）"
	skipProtectedSet = "Protected 系统谱面集"
	skipExternalSet  = "Ez 外部托管谱面集"
	skipHiddenDiff   = "Hidden 难度"
)

// ezExtSetHashPrefix 是 Ez 外部托管集 Hash 前缀（fork-ez2lazer §5.1）。
const ezExtSetHashPrefix = "ext:set:"

// reader 缓存一次读事务内的 class/prop 寻址，并把「列缺失 → 该列不进
// DTO」的降级收敛到唯一入口。
type reader struct {
	r         *realmc.Realm
	classKeys map[string]uint32
	propKeys  map[string]map[string]int64
}

func newReader(r *realmc.Realm) *reader {
	return &reader{r: r, classKeys: map[string]uint32{}, propKeys: map[string]map[string]int64{}}
}

// class 按名取 class key；class 不存在（类型面外/缺表）返回 false。
func (rd *reader) class(name string) (uint32, bool) {
	if k, ok := rd.classKeys[name]; ok {
		return k, true
	}
	k, err := rd.r.FindClass(name)
	if err != nil {
		return 0, false
	}
	rd.classKeys[name] = k
	return k, true
}

// prop 按名取列 key；缺列返回 false（调用方按 §4.1 末条降级）。
func (rd *reader) prop(class string, classKey uint32, name string) (int64, bool) {
	pk, ok := rd.propKeys[class]
	if !ok {
		props, err := rd.r.Properties(classKey)
		if err != nil {
			return 0, false
		}
		pk = make(map[string]int64, len(props))
		for _, p := range props {
			pk[p.Name] = p.Key
		}
		rd.propKeys[class] = pk
	}
	k, ok := pk[name]
	return k, ok
}

// project 在已开启的读事务内把 fork 库投影为收集快照（§3.2 步骤②）。
func project(r *realmc.Realm, target forks.LibraryRef, prog ProgressFunc) (hub.Snapshot, error) {
	rd := newReader(r)
	snap := hub.Snapshot{
		ForkID:      target.ForkID,
		CollectedAt: time.Now().UTC(),
	}
	skips := map[string]int{}
	count := func(reason string) { skips[reason]++ }

	total := 0
	if ck, ok := rd.class("BeatmapSet"); ok {
		if n, err := r.ObjectCount(ck); err == nil {
			total += int(n)
		}
	}
	if ck, ok := rd.class("BeatmapCollection"); ok {
		if n, err := r.ObjectCount(ck); err == nil {
			total += int(n)
		}
	}
	current := 0
	report := func() {
		if prog != nil {
			prog(StageProject, current, total)
		}
	}

	// 谱面集（含难度归属与文件清单；链接列表列缺列时自然为空——
	// fixture 镜像即此形态，真实库则走列表）。
	isEz := target.ForkID == "ez2lazer"
	if setKey, ok := rd.class("BeatmapSet"); ok {
		if err := r.ForEach(setKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			return projectBeatmapSet(rd, obj, &snap, count, isEz)
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 BeatmapSet: %w", err)
		}
	}

	// 收藏夹（内容键 Name；MD5 列表缺列时为空列表）。
	if colKey, ok := rd.class("BeatmapCollection"); ok {
		if err := r.ForEach(colKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			name, ok := rd.str(obj, "BeatmapCollection", colKey, "Name")
			if !ok || name == "" {
				return nil // 无名行无内容键，不构成收藏夹
			}
			c := hub.Collection{ID: rd.guid(obj, "BeatmapCollection", colKey, "ID"), Name: name}
			if listKey, ok := rd.prop("BeatmapCollection", colKey, "BeatmapMD5Hashes"); ok {
				if lst, err := r.GetList(obj, listKey); err == nil {
					if n, err := lst.Size(); err == nil {
						for i := uint64(0); i < n; i++ {
							if h, err := lst.GetString(i); err == nil {
								c.BeatmapMD5Hashes = append(c.BeatmapMD5Hashes, h)
							}
						}
					}
					lst.Close()
				}
			}
			if tsKey, ok := rd.prop("BeatmapCollection", colKey, "LastModified"); ok {
				if null, err := r.IsNull(obj, tsKey); err == nil && !null {
					c.LastModified, _ = r.GetTimestamp(obj, tsKey)
				}
			}
			snap.Collections = append(snap.Collections, c)
			return nil
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 BeatmapCollection: %w", err)
		}
	}

	// 跳过计数 → 稳定顺序的 SkipStat 切片（collect_log 可断言）。
	for reason, n := range skips {
		snap.Skips = append(snap.Skips, hub.SkipStat{Reason: reason, Count: n})
	}
	sort.Slice(snap.Skips, func(i, j int) bool { return snap.Skips[i].Reason < snap.Skips[j].Reason })
	return snap, nil
}

// projectBeatmapSet 投影单个谱面集行（跳过规则前置，命中即计数返回）。
func projectBeatmapSet(rd *reader, obj unsafe.Pointer, snap *hub.Snapshot, count func(string), isEz bool) error {
	const cls = "BeatmapSet"
	classKey, _ := rd.class(cls)

	if rd.boolOrFalse(obj, cls, classKey, "DeletePending") {
		count(skipSoftDeleted)
		return nil
	}
	if rd.boolOrFalse(obj, cls, classKey, "Protected") {
		count(skipProtectedSet)
		return nil
	}
	hash, _ := rd.str(obj, cls, classKey, "Hash")
	if isEz && (len(hash) >= len(ezExtSetHashPrefix) && hash[:len(ezExtSetHashPrefix)] == ezExtSetHashPrefix ||
		rd.intOrZero(obj, cls, classKey, "HostingKind") == 1) {
		count(skipExternalSet)
		return nil
	}
	if hash == "" {
		return nil // 无内容键的行不构成谱面集（防御，正常无此行）
	}

	bs := hub.BeatmapSet{
		ID:        rd.guid(obj, cls, classKey, "ID"),
		Hash:      hash,
		OnlineID:  rd.intOrZero(obj, cls, classKey, "OnlineID"),
		StatusInt: int(rd.intOrZero(obj, cls, classKey, "Status")),
	}
	if k, ok := rd.prop(cls, classKey, "DateAdded"); ok {
		if null, err := rd.r.IsNull(obj, k); err == nil && !null {
			bs.DateAdded, _ = rd.r.GetTimestamp(obj, k)
		}
	}

	// 难度列表（真实库 Beatmaps 前向对象列表；缺列为空）。
	if listKey, ok := rd.prop(cls, classKey, "Beatmaps"); ok {
		if lst, err := rd.r.GetList(obj, listKey); err == nil {
			if n, err := lst.Size(); err == nil {
				for i := uint64(0); i < n; i++ {
					bm, err := lst.GetLinkedObject(i)
					if err != nil || bm == nil {
						continue
					}
					if b, ok := projectBeatmap(rd, bm, count); ok {
						bs.Beatmaps = append(bs.Beatmaps, b)
						if len(bs.Beatmaps) == 1 {
							// 首难度的 Metadata 是谱面身份键字段载体（§4.2 第二层）。
							fillMetadata(rd, bm, &bs)
						}
					}
					rd.r.ReleaseObject(bm)
				}
			}
			lst.Close()
		}
	}

	// 文件清单（RealmNamedFileUsage embedded 列表；缺列为空）。
	if listKey, ok := rd.prop(cls, classKey, "Files"); ok {
		if lst, err := rd.r.GetList(obj, listKey); err == nil {
			if n, err := lst.Size(); err == nil {
				for i := uint64(0); i < n; i++ {
					usage, err := lst.GetLinkedObject(i)
					if err != nil || usage == nil {
						continue
					}
					nf := hub.NamedFile{Filename: rd.objStr(usage, "RealmNamedFileUsage", "Filename")}
					if fileKey, ok := rd.class("File"); ok {
						if file, err := rd.r.GetLinkedObject(usage, mustProp(rd, "RealmNamedFileUsage", fileKey, "File")); err == nil && file != nil {
							nf.Hash = rd.objStr(file, "File", "Hash")
							rd.r.ReleaseObject(file)
						}
					}
					bs.Files = append(bs.Files, nf)
					rd.r.ReleaseObject(usage)
				}
			}
			lst.Close()
		}
	}

	snap.BeatmapSets = append(snap.BeatmapSets, bs)
	return nil
}

// projectBeatmap 投影单个难度行（Hidden 跳过；父集跳过时难度随集消失，
// 不重复计数）。
func projectBeatmap(rd *reader, obj unsafe.Pointer, count func(string)) (hub.Beatmap, bool) {
	const cls = "Beatmap"
	classKey, _ := rd.class(cls)
	if rd.boolOrFalse(obj, cls, classKey, "Hidden") {
		count(skipHiddenDiff)
		return hub.Beatmap{}, false
	}
	return hub.Beatmap{
		ID:             rd.guid(obj, cls, classKey, "ID"),
		Hash:           rd.objStr(obj, cls, "Hash"),
		MD5Hash:        rd.objStr(obj, cls, "MD5Hash"),
		DifficultyName: rd.objStr(obj, cls, "DifficultyName"),
	}, true
}

// fillMetadata 从首难度的 Metadata 关系行读谱面身份键字段（Artist/Title
// 直读；AuthorUsername 走 Author → embedded RealmUser.Username，§2.6）。
// 缺列/无关系行为空（弱键配对自然失配，不构成错误）。
func fillMetadata(rd *reader, bm unsafe.Pointer, bs *hub.BeatmapSet) {
	classKey, ok := rd.class("Beatmap")
	if !ok {
		return
	}
	mdKey, ok := rd.prop("Beatmap", classKey, "Metadata")
	if !ok {
		return
	}
	md, err := rd.r.GetLinkedObject(bm, mdKey)
	if err != nil || md == nil {
		return
	}
	defer rd.r.ReleaseObject(md)
	bs.Artist = rd.objStr(md, "BeatmapMetadata", "Artist")
	bs.Title = rd.objStr(md, "BeatmapMetadata", "Title")
	if mdClass, ok := rd.class("BeatmapMetadata"); ok {
		if authorKey, ok := rd.prop("BeatmapMetadata", mdClass, "Author"); ok {
			if author, err := rd.r.GetLinkedObject(md, authorKey); err == nil && author != nil {
				bs.AuthorUsername = rd.objStr(author, "RealmUser", "Username")
				rd.r.ReleaseObject(author)
			}
		}
	}
}

// ---- reader 的类型化读面（缺列/NULL 一律零值降级） ----

func (rd *reader) str(obj unsafe.Pointer, cls string, classKey uint32, name string) (string, bool) {
	k, ok := rd.prop(cls, classKey, name)
	if !ok {
		return "", false
	}
	v, err := rd.r.GetString(obj, k)
	if err != nil {
		return "", false
	}
	return v, true
}

func (rd *reader) objStr(obj unsafe.Pointer, cls, name string) string {
	classKey, ok := rd.class(cls)
	if !ok {
		return ""
	}
	v, _ := rd.str(obj, cls, classKey, name)
	return v
}

func (rd *reader) boolOrFalse(obj unsafe.Pointer, cls string, classKey uint32, name string) bool {
	k, ok := rd.prop(cls, classKey, name)
	if !ok {
		return false
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return false
	}
	v, err := rd.r.GetBool(obj, k)
	return err == nil && v
}

func (rd *reader) intOrZero(obj unsafe.Pointer, cls string, classKey uint32, name string) int64 {
	k, ok := rd.prop(cls, classKey, name)
	if !ok {
		return 0
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return 0
	}
	v, err := rd.r.GetInt64(obj, k)
	if err != nil {
		return 0
	}
	return v
}

func (rd *reader) guid(obj unsafe.Pointer, cls string, classKey uint32, name string) string {
	k, ok := rd.prop(cls, classKey, name)
	if !ok {
		return ""
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return ""
	}
	g, err := rd.r.GetGuid(obj, k)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(g[:])
}

// mustProp 是 prop 的断言形态（结构上已确认 class 存在的调用点使用，
// 失败返回 0 并由后续 GetLinkedObject 报错路径兜底）。
func mustProp(rd *reader, cls string, classKey uint32, name string) int64 {
	k, _ := rd.prop(cls, classKey, name)
	return k
}
