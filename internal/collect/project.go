package collect

// 只读投影（design §4.1）：动态只读会话 → hub.Snapshot。跳过集逐条计数
// （不静默吞，§1.5-4）；目标库缺某列时该列不进 DTO（列缺失自然降级，
// ezrealmsync-design §3.3）。M2 类型面 = collection / beatmap_set /
// beatmap / skin / score（hub schema v2），谱面与皮肤/成绩均按 C2
// 白名单全列投影。

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unsafe"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// 投影跳过原因（稳定字符串，进 hub.SkipStat 与 collect_log）。
const (
	skipSoftDeleted    = "软删谱面集（DeletePending）"
	skipProtectedSet   = "Protected 系统谱面集"
	skipExternalSet    = "Ez 外部托管谱面集"
	skipHiddenDiff     = "Hidden 难度"
	skipOwnRulesetDiff = "fork 专有规则集难度（OwnRulesets）"
	skipSkinSoftDel    = "软删皮肤（DeletePending）"
	skipProtectedSkin  = "Protected 内置皮肤"
	skipOwnSkin        = "fork 专有皮肤（OwnSkins 指纹）"
	skipSkinNoHash     = "皮肤 Hash 为空（无法判定身份）"
	skipScoreSoftDel   = "软删成绩（DeletePending）"
	skipScoreNoBeatmap = "成绩 BeatmapHash 为空（无谱面锚点）"
	skipOwnRuleset     = "fork 专有规则集成绩（OwnRulesets）"
	skipMarketRuleset  = "规则集市场行成绩（OnlineID 超内置域，服务器市场产物不跨端）"
	skipJudgedScore    = "fork 专有判定档成绩（判定语义列非 0，官方端重算会错）"
	skipPresetSoftDel  = "软删模组预设（DeletePending）"
)

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

	// §5.4 收集侧兼容过滤的规则数据（§4.3：fork 知识只在 Profile——
	// 专有规则集/市场规则集下界/判定档列/外部集前缀/专有皮肤指纹）。
	// 未注册 fork 的零值 Profile 使全部判定自然放行（投放侧仍有防线）。
	prof, _ := forks.Profile(target.ForkID)
	ownRulesets := map[string]bool{}
	for _, s := range prof.OwnRulesets {
		ownRulesets[s] = true
	}
	extPrefix := prof.ExtSetHashPrefix

	total := 0
	for _, cls := range []string{"BeatmapSet", "BeatmapCollection", "Skin", "Score", "KeyBinding", "RulesetSetting", "ModPreset"} {
		if ck, ok := rd.class(cls); ok {
			if n, err := r.ObjectCount(ck); err == nil {
				total += int(n)
			}
		}
	}
	current := 0
	report := func() {
		if prog != nil {
			prog(StageProject, current, total)
		}
	}

	// 谱面集（含难度归属与文件清单；链接列表列缺列时自然为空——
	// 真实库与 M2 镜像走列表）。
	if setKey, ok := rd.class("BeatmapSet"); ok {
		if err := r.ForEach(setKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			return projectBeatmapSet(rd, obj, &snap, count, extPrefix, ownRulesets)
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
					c.LastModified = c.LastModified.UTC()
				}
			}
			snap.Collections = append(snap.Collections, c)
			return nil
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 BeatmapCollection: %w", err)
		}
	}

	// 皮肤（跳过规则 §4.1：软删 / Protected 内置 / fork 专有指纹 /
	// 空 Hash；指纹清单来自 forks 注册表）。
	ownSkins := ownSkinsOf(target.ForkID)
	if skinKey, ok := rd.class("Skin"); ok {
		if err := r.ForEach(skinKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			return projectSkin(rd, obj, &snap, count, ownSkins)
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 Skin: %w", err)
		}
	}

	// 成绩（跳过规则 §4.1 + §5.4 兼容过滤：软删 / 空 BeatmapHash /
	// fork 专有规则集 / 市场规则集行（D8）/ 判定档（R7）；User embedded、
	// Mods/Statistics 原样字符串、Files=.osr usage——mods/Statistics 的
	// 目标端白名单过滤在投放侧转换（D7，hub 保留原文）。
	if scoreKey, ok := rd.class("Score"); ok {
		if err := r.ForEach(scoreKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			return projectScore(rd, obj, &snap, count, ownRulesets,
				prof.MarketRulesetMinOnlineID, prof.ScoreJudgeColumns)
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 Score: %w", err)
		}
	}

	// 设置类三面（§5.1-M3）：键位/规则集设置全收（hub 保留原文——D20 键位
	// 枚举域、fork 专有规则集可解析性、D7 mod 名录全部在投放侧按目标端
	// 过滤，与成绩 mods/Statistics 的哲学一致）；ModPreset 软删行跳过
	//（S4：软删行在读取投影中当作不存在，§5.4）。
	if kbKey, ok := rd.class("KeyBinding"); ok {
		if err := r.ForEach(kbKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			snap.KeyBindings = append(snap.KeyBindings, projectKeyBinding(rd, obj))
			return nil
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 KeyBinding: %w", err)
		}
	}
	if rsKey, ok := rd.class("RulesetSetting"); ok {
		if err := r.ForEach(rsKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			snap.RulesetSettings = append(snap.RulesetSettings, projectRulesetSetting(rd, obj))
			return nil
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 RulesetSetting: %w", err)
		}
	}
	if mpKey, ok := rd.class("ModPreset"); ok {
		if err := r.ForEach(mpKey, func(obj unsafe.Pointer) error {
			defer func() { current++; report() }()
			if rd.boolOrFalse(obj, "ModPreset", mpKey, "DeletePending") {
				count(skipPresetSoftDel)
				return nil
			}
			snap.ModPresets = append(snap.ModPresets, projectModPreset(rd, obj))
			return nil
		}); err != nil {
			return hub.Snapshot{}, fmt.Errorf("投影 ModPreset: %w", err)
		}
	}

	// 跳过计数 → 稳定顺序的 SkipStat 切片（collect_log 可断言）。
	for reason, n := range skips {
		snap.Skips = append(snap.Skips, hub.SkipStat{Reason: reason, Count: n})
	}
	sort.Slice(snap.Skips, func(i, j int) bool { return snap.Skips[i].Reason < snap.Skips[j].Reason })
	return snap, nil
}

// ownSkinsOf 取目标 fork 注册表声明的专有皮肤指纹（§4.3：fork 知识只在
// forks 包）。当前四端注册表未登记专有皮肤——规则处于待数据激活态，
// 注册表填充后无需改本处。
func ownSkinsOf(forkID string) []forks.SkinFingerprint {
	if p, ok := forks.Profile(forkID); ok {
		return p.OwnSkins
	}
	return nil
}

// skinIsOwn 判定皮肤是否命中 fork 专有指纹（§4.1）：Guid 等值（固定
// 常量类专有皮肤，D21）或 Name+Creator 双等值，任一通道命中即专有。
func skinIsOwn(own []forks.SkinFingerprint, guid, name, creator string) bool {
	for _, f := range own {
		if f.Guid != "" && guid != "" && f.Guid == guid {
			return true
		}
		if f.Name != "" && f.Creator != "" && f.Name == name && f.Creator == creator {
			return true
		}
	}
	return false
}

// projectSkin 投影单个皮肤行（跳过规则前置，命中即计数返回；正常入
// hub 的行 DeletePending/Protected 恒 false，列集完整性保留）。
func projectSkin(rd *reader, obj unsafe.Pointer, snap *hub.Snapshot, count func(string), own []forks.SkinFingerprint) error {
	const cls = "Skin"
	classKey, _ := rd.class(cls)
	if rd.boolOrFalse(obj, cls, classKey, "DeletePending") {
		count(skipSkinSoftDel)
		return nil
	}
	if rd.boolOrFalse(obj, cls, classKey, "Protected") {
		count(skipProtectedSkin)
		return nil
	}
	name, creator := rd.objStr(obj, cls, "Name"), rd.objStr(obj, cls, "Creator")
	if skinIsOwn(own, rd.guid(obj, cls, classKey, "ID"), name, creator) {
		count(skipOwnSkin)
		return nil
	}
	hash := rd.objStr(obj, cls, "Hash")
	if hash == "" {
		count(skipSkinNoHash)
		return nil
	}
	snap.Skins = append(snap.Skins, hub.Skin{
		ID:                rd.guid(obj, cls, classKey, "ID"),
		Hash:              hash,
		Name:              name,
		Creator:           creator,
		InstantiationInfo: rd.objStr(obj, cls, "InstantiationInfo"),
		Files:             readNamedFiles(rd, obj, cls),
	})
	return nil
}

// projectScore 投影单个成绩行（跳过规则前置 + §5.4 兼容过滤；全列含
// User embedded、Pauses 列表、Files=.osr usage 与 nullable 的
// LegacyTotalScore/PP）。
func projectScore(rd *reader, obj unsafe.Pointer, snap *hub.Snapshot, count func(string),
	ownRulesets map[string]bool, marketRulesetMin int64, judgeCols []string) error {
	const cls = "Score"
	classKey, _ := rd.class(cls)
	if rd.boolOrFalse(obj, cls, classKey, "DeletePending") {
		count(skipScoreSoftDel)
		return nil
	}
	beatmapHash := rd.objStr(obj, cls, "BeatmapHash")
	if beatmapHash == "" {
		count(skipScoreNoBeatmap)
		return nil
	}
	rulesetName := rd.linkedShortName(obj, cls, "Ruleset")
	// §5.4 D6：fork 专有规则集（OwnRulesets）的成绩——目标端无
	// InstantiationInfo 可解析，整条不入 hub。
	if ownRulesets[rulesetName] {
		count(skipOwnRuleset)
		return nil
	}
	// §5.4 D8：指向规则集市场行（OnlineID ≥ 注册下界）的成绩——市场
	// 产物属宿主服务器，跨端无意义。下界 0 = 本 fork 无市场机制。
	if marketRulesetMin > 0 &&
		rd.linkedRulesetOnlineID(obj, cls, "Ruleset") >= marketRulesetMin {
		count(skipMarketRuleset)
		return nil
	}
	// §5.4 R7：判定档成绩（判定语义列任一非 0）——官方端重算会错，
	//「能少不能错」。列缺省自然 0（非宿主 fork 不触发）。
	for _, col := range judgeCols {
		if rd.intOrZero(obj, cls, classKey, col) != 0 {
			count(skipJudgedScore)
			return nil
		}
	}
	date := time.Time{}
	if k, ok := rd.prop(cls, classKey, "Date"); ok {
		if null, err := rd.r.IsNull(obj, k); err == nil && !null {
			date, _ = rd.r.GetTimestamp(obj, k)
			date = date.UTC()
		}
	}
	sc := hub.Score{
		ID:                           rd.guid(obj, cls, classKey, "ID"),
		BeatmapHash:                  beatmapHash,
		Hash:                         rd.objStr(obj, cls, "Hash"),
		RulesetShortName:             rulesetName,
		ClientVersion:                rd.objStr(obj, cls, "ClientVersion"),
		TotalScore:                   rd.intOrZero(obj, cls, classKey, "TotalScore"),
		TotalScoreWithoutMods:        rd.intOrZero(obj, cls, classKey, "TotalScoreWithoutMods"),
		TotalScoreVersion:            int(rd.intOrZero(obj, cls, classKey, "TotalScoreVersion")),
		BackgroundReprocessingFailed: rd.boolOrFalse(obj, cls, classKey, "BackgroundReprocessingFailed"),
		MaxCombo:                     int(rd.intOrZero(obj, cls, classKey, "MaxCombo")),
		Accuracy:                     rd.objFloat64(obj, cls, "Accuracy"),
		Date:                         date,
		OnlineID:                     rd.intOrZero(obj, cls, classKey, "OnlineID"),
		LegacyOnlineID:               rd.intOrZero(obj, cls, classKey, "LegacyOnlineID"),
		User:                         readEmbeddedUser(rd, obj, cls, "User"),
		ModsJson:                     rd.objStr(obj, cls, "Mods"),
		StatisticsJson:               rd.objStr(obj, cls, "Statistics"),
		MaximumStatisticsJson:        rd.objStr(obj, cls, "MaximumStatistics"),
		RankInt:                      int(rd.intOrZero(obj, cls, classKey, "Rank")),
		Combo:                        int(rd.intOrZero(obj, cls, classKey, "Combo")),
		IsLegacyScore:                rd.boolOrFalse(obj, cls, classKey, "IsLegacyScore"),
		Files:                        readNamedFiles(rd, obj, cls),
	}
	if v := rd.objInt64Ptr(obj, cls, "LegacyTotalScore"); v != nil {
		sc.LegacyTotalScore = v
	}
	if v := rd.objFloat64Ptr(obj, cls, "PP"); v != nil {
		sc.PP = v
	}
	if pauses := readPauses(rd, obj, cls); len(pauses) > 0 {
		sc.Pauses = pauses
	}
	snap.Scores = append(snap.Scores, sc)
	return nil
}

// projectKeyBinding 投影单个键位行（lazer-baseline §2.12 全列）。可空键段
// 用指针保真：RulesetName null = 全局键位、Variant null = 无变体（内容键
// 的组成段，§4.2 第一层）。
func projectKeyBinding(rd *reader, obj unsafe.Pointer) hub.KeyBinding {
	const cls = "KeyBinding"
	classKey, _ := rd.class(cls)
	kb := hub.KeyBinding{
		ID:        rd.guid(obj, cls, classKey, "ID"),
		ActionInt: int(rd.intOrZero(obj, cls, classKey, "Action")),
	}
	if k, ok := rd.prop(cls, classKey, "RulesetName"); ok {
		if null, err := rd.r.IsNull(obj, k); err == nil && !null {
			if v, err := rd.r.GetString(obj, k); err == nil {
				name := v
				kb.RulesetName = &name
			}
		}
	}
	if k, ok := rd.prop(cls, classKey, "Variant"); ok {
		if null, err := rd.r.IsNull(obj, k); err == nil && !null {
			if v, err := rd.r.GetInt64(obj, k); err == nil {
				variant := int(v)
				kb.Variant = &variant
			}
		}
	}
	if k, ok := rd.prop(cls, classKey, "KeyCombination"); ok {
		if null, err := rd.r.IsNull(obj, k); err == nil && !null {
			kb.KeyCombination, _ = rd.r.GetString(obj, k)
		}
	}
	return kb
}

// projectRulesetSetting 投影单个规则集设置行（lazer-baseline §2.13 全列，
// 无主键表；内容键 = (RulesetName, Variant, Key)）。fork 专有规则集的行
// 照收——hub 保留原文，投放侧按目标端可解析性跳过（§5.1-M3）。
func projectRulesetSetting(rd *reader, obj unsafe.Pointer) hub.RulesetSetting {
	const cls = "RulesetSetting"
	classKey, _ := rd.class(cls)
	return hub.RulesetSetting{
		RulesetName: rd.objStr(obj, cls, "RulesetName"),
		Variant:     int(rd.intOrZero(obj, cls, classKey, "Variant")),
		Key:         rd.objStr(obj, cls, "Key"),
		Value:       rd.objStr(obj, cls, "Value"),
	}
}

// projectModPreset 投影单个模组预设行（lazer-baseline §2.11 全列）。
// Ruleset 关系列投影为 ShortName（业务键的组成段）；关系为 null 时投影
// 空串（源端数据形态，投放侧空名规则集不可解析自然跳过）。
func projectModPreset(rd *reader, obj unsafe.Pointer) hub.ModPreset {
	const cls = "ModPreset"
	classKey, _ := rd.class(cls)
	return hub.ModPreset{
		ID:               rd.guid(obj, cls, classKey, "ID"),
		RulesetShortName: rd.linkedShortName(obj, cls, "Ruleset"),
		Name:             rd.objStr(obj, cls, "Name"),
		Description:      rd.objStr(obj, cls, "Description"),
		ModsJson:         rd.objStr(obj, cls, "Mods"),
		DeletePending:    false, // 软删行已在调用侧跳过（S4）
	}
}

// projectBeatmapSet 投影单个谱面集行（跳过规则前置，命中即计数返回）。
// extPrefix 是本 fork 的外部托管集 Hash 前缀（Profile 数据化，D5——
// 空 = 无此机制）；ownRulesets 供难度级过滤（D6）。
func projectBeatmapSet(rd *reader, obj unsafe.Pointer, snap *hub.Snapshot, count func(string),
	extPrefix string, ownRulesets map[string]bool) error {
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
	if extPrefix != "" && strings.HasPrefix(hash, extPrefix) ||
		rd.intOrZero(obj, cls, classKey, "HostingKind") == 1 {
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
			bs.DateAdded = bs.DateAdded.UTC()
		}
	}
	if v := rd.objTSPtr(obj, cls, "DateSubmitted"); v != nil {
		bs.DateSubmitted = v
	}
	if v := rd.objTSPtr(obj, cls, "DateRanked"); v != nil {
		bs.DateRanked = v
	}

	// 难度列表（Beatmaps 前向对象列表；缺列为空）。首难度的 Metadata 是
	// 谱面身份键字段载体（§4.2 第二层）。
	if listKey, ok := rd.prop(cls, classKey, "Beatmaps"); ok {
		if lst, err := rd.r.GetList(obj, listKey); err == nil {
			if n, err := lst.Size(); err == nil {
				for i := uint64(0); i < n; i++ {
					bm, err := lst.GetLinkedObject(i)
					if err != nil || bm == nil {
						continue
					}
					if b, ok := projectBeatmap(rd, bm, count, ownRulesets); ok {
						bs.Beatmaps = append(bs.Beatmaps, b)
						if len(bs.Beatmaps) == 1 {
							bs.Artist = b.Metadata.Artist
							bs.Title = b.Metadata.Title
							bs.AuthorUsername = b.Metadata.Author.Username
						}
					}
					rd.r.ReleaseObject(bm)
				}
			}
			lst.Close()
		}
	}

	// 文件清单（RealmNamedFileUsage embedded 列表；缺列为空）。
	bs.Files = readNamedFiles(rd, obj, cls)

	snap.BeatmapSets = append(snap.BeatmapSets, bs)
	return nil
}

// projectBeatmap 投影单个难度行（C2 白名单全列：Hidden 跳过、fork 专有
// 规则集难度跳过（D6，机制与成绩同源）；父集跳过时难度随集消失，
// 不重复计数）。
func projectBeatmap(rd *reader, obj unsafe.Pointer, count func(string), ownRulesets map[string]bool) (hub.Beatmap, bool) {
	const cls = "Beatmap"
	classKey, _ := rd.class(cls)
	if rd.boolOrFalse(obj, cls, classKey, "Hidden") {
		count(skipHiddenDiff)
		return hub.Beatmap{}, false
	}
	bm := hub.Beatmap{
		ID:                 rd.guid(obj, cls, classKey, "ID"),
		Hash:               rd.objStr(obj, cls, "Hash"),
		MD5Hash:            rd.objStr(obj, cls, "MD5Hash"),
		OnlineMD5Hash:      rd.objStr(obj, cls, "OnlineMD5Hash"),
		DifficultyName:     rd.objStr(obj, cls, "DifficultyName"),
		RulesetShortName:   rd.linkedShortName(obj, cls, "Ruleset"),
		StatusInt:          int(rd.intOrZero(obj, cls, classKey, "Status")),
		OnlineID:           int(rd.intOrZero(obj, cls, classKey, "OnlineID")),
		Length:             rd.objFloat64(obj, cls, "Length"),
		BPM:                rd.objFloat64(obj, cls, "BPM"),
		StarRating:         rd.objFloat64(obj, cls, "StarRating"),
		EndTimeObjectCount: int(rd.intOrZero(obj, cls, classKey, "EndTimeObjectCount")),
		TotalObjectCount:   int(rd.intOrZero(obj, cls, classKey, "TotalObjectCount")),
		BeatDivisor:        int(rd.intOrZero(obj, cls, classKey, "BeatDivisor")),
	}
	if v := rd.objTSPtr(obj, cls, "LastLocalUpdate"); v != nil {
		bm.LastLocalUpdate = v
	}
	if v := rd.objTSPtr(obj, cls, "LastOnlineUpdate"); v != nil {
		bm.LastOnlineUpdate = v
	}
	if v := rd.objTSPtr(obj, cls, "LastPlayed"); v != nil {
		bm.LastPlayed = v
	}
	if v := rd.objFloat64Ptr(obj, cls, "EditorTimestamp"); v != nil {
		bm.EditorTimestamp = v
	}
	bm.Difficulty = readEmbeddedDifficulty(rd, obj, cls)
	bm.Metadata = readMetadata(rd, obj, cls)
	bm.UserSettingsOffset = readUserSettingsOffset(rd, obj, cls)
	if ownRulesets[bm.RulesetShortName] {
		count(skipOwnRulesetDiff)
		return hub.Beatmap{}, false
	}
	return bm, true
}

// readNamedFiles 读对象的 Files 列表（RealmNamedFileUsage embedded →
// File.Hash + Filename；缺列/空链接自然为空，§4.1 降级）。
func readNamedFiles(rd *reader, obj unsafe.Pointer, cls string) []hub.NamedFile {
	classKey, ok := rd.class(cls)
	if !ok {
		return nil
	}
	listKey, ok := rd.prop(cls, classKey, "Files")
	if !ok {
		return nil
	}
	var out []hub.NamedFile
	lst, err := rd.r.GetList(obj, listKey)
	if err != nil {
		return nil
	}
	defer lst.Close()
	n, err := lst.Size()
	if err != nil {
		return nil
	}
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
		out = append(out, nf)
		rd.r.ReleaseObject(usage)
	}
	return out
}

// readEmbeddedDifficulty 读难度的 BeatmapDifficulty embedded（六个数值
// 列；无挂接/缺列为零值）。
func readEmbeddedDifficulty(rd *reader, obj unsafe.Pointer, cls string) hub.Difficulty {
	diff, ok := rd.linked(obj, cls, "Difficulty")
	if !ok {
		return hub.Difficulty{}
	}
	defer rd.r.ReleaseObject(diff)
	return hub.Difficulty{
		DrainRate:         rd.objFloat32(diff, "BeatmapDifficulty", "DrainRate"),
		CircleSize:        rd.objFloat32(diff, "BeatmapDifficulty", "CircleSize"),
		OverallDifficulty: rd.objFloat32(diff, "BeatmapDifficulty", "OverallDifficulty"),
		ApproachRate:      rd.objFloat32(diff, "BeatmapDifficulty", "ApproachRate"),
		SliderMultiplier:  rd.objFloat64(diff, "BeatmapDifficulty", "SliderMultiplier"),
		SliderTickRate:    rd.objFloat64(diff, "BeatmapDifficulty", "SliderTickRate"),
	}
}

// readMetadata 读难度的 Metadata 关系行（C2 白名单全列：Author 走 embedded
// RealmUser，UserTags 走 string 列表，§2.6）。缺列/无关系行为零值。
func readMetadata(rd *reader, obj unsafe.Pointer, cls string) hub.Metadata {
	md, ok := rd.linked(obj, cls, "Metadata")
	if !ok {
		return hub.Metadata{}
	}
	defer rd.r.ReleaseObject(md)
	out := hub.Metadata{
		Title:          rd.objStr(md, "BeatmapMetadata", "Title"),
		TitleUnicode:   rd.objStr(md, "BeatmapMetadata", "TitleUnicode"),
		Artist:         rd.objStr(md, "BeatmapMetadata", "Artist"),
		ArtistUnicode:  rd.objStr(md, "BeatmapMetadata", "ArtistUnicode"),
		Source:         rd.objStr(md, "BeatmapMetadata", "Source"),
		Tags:           rd.objStr(md, "BeatmapMetadata", "Tags"),
		PreviewTime:    int(rd.objInt64OrZero(md, "BeatmapMetadata", "PreviewTime")),
		AudioFile:      rd.objStr(md, "BeatmapMetadata", "AudioFile"),
		BackgroundFile: rd.objStr(md, "BeatmapMetadata", "BackgroundFile"),
	}
	if author, ok := rd.linked(md, "BeatmapMetadata", "Author"); ok {
		out.Author = readUserRow(rd, author)
		rd.r.ReleaseObject(author)
	}
	if mdKey, ok := rd.class("BeatmapMetadata"); ok {
		if tagsKey, ok := rd.prop("BeatmapMetadata", mdKey, "UserTags"); ok {
			if lst, err := rd.r.GetList(md, tagsKey); err == nil {
				if n, err := lst.Size(); err == nil {
					for i := uint64(0); i < n; i++ {
						if tag, err := lst.GetString(i); err == nil {
							out.UserTags = append(out.UserTags, tag)
						}
					}
				}
				lst.Close()
			}
		}
	}
	return out
}

// readUserSettingsOffset 读难度的 BeatmapUserSettings embedded（唯一列
// Offset 的扁平投影；无挂接为零值）。
func readUserSettingsOffset(rd *reader, obj unsafe.Pointer, cls string) float64 {
	us, ok := rd.linked(obj, cls, "UserSettings")
	if !ok {
		return 0
	}
	defer rd.r.ReleaseObject(us)
	return rd.objFloat64(us, "BeatmapUserSettings", "Offset")
}

// readEmbeddedUser 读成绩的 User embedded（RealmUser 三列）。
func readEmbeddedUser(rd *reader, obj unsafe.Pointer, cls, prop string) hub.RealmUser {
	user, ok := rd.linked(obj, cls, prop)
	if !ok {
		return hub.RealmUser{}
	}
	defer rd.r.ReleaseObject(user)
	return readUserRow(rd, user)
}

// readUserRow 读 RealmUser 行句柄的三列（调用方负责释放句柄）。
func readUserRow(rd *reader, user unsafe.Pointer) hub.RealmUser {
	return hub.RealmUser{
		OnlineID:      int(rd.objInt64OrZero(user, "RealmUser", "OnlineID")),
		Username:      rd.objStr(user, "RealmUser", "Username"),
		CountryString: rd.objStr(user, "RealmUser", "CountryString"),
	}
}

// readPauses 读成绩的 Pauses int 列表（缺列为空）。
func readPauses(rd *reader, obj unsafe.Pointer, cls string) []int32 {
	classKey, ok := rd.class(cls)
	if !ok {
		return nil
	}
	listKey, ok := rd.prop(cls, classKey, "Pauses")
	if !ok {
		return nil
	}
	lst, err := rd.r.GetList(obj, listKey)
	if err != nil {
		return nil
	}
	defer lst.Close()
	n, err := lst.Size()
	if err != nil {
		return nil
	}
	out := make([]int32, 0, n)
	for i := uint64(0); i < n; i++ {
		if v, err := lst.GetInt64(i); err == nil {
			out = append(out, int32(v))
		}
	}
	return out
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

// objInt64OrZero 是 intOrZero 的按名形态（class 内部解析）。
func (rd *reader) objInt64OrZero(obj unsafe.Pointer, cls, name string) int64 {
	classKey, ok := rd.class(cls)
	if !ok {
		return 0
	}
	return rd.intOrZero(obj, cls, classKey, name)
}

func (rd *reader) objFloat32(obj unsafe.Pointer, cls, name string) float32 {
	k, clsOK := rd.propByName(cls, name)
	if !clsOK {
		return 0
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return 0
	}
	v, err := rd.r.GetFloat32(obj, k)
	if err != nil {
		return 0
	}
	return v
}

func (rd *reader) objFloat64(obj unsafe.Pointer, cls, name string) float64 {
	k, ok := rd.propByName(cls, name)
	if !ok {
		return 0
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return 0
	}
	v, err := rd.r.GetFloat64(obj, k)
	if err != nil {
		return 0
	}
	return v
}

// objTSPtr 读可空时间列（缺列/NULL → nil；非空取值指针）。
func (rd *reader) objTSPtr(obj unsafe.Pointer, cls, name string) *time.Time {
	k, ok := rd.propByName(cls, name)
	if !ok {
		return nil
	}
	if null, err := rd.r.IsNull(obj, k); err != nil || null {
		return nil
	}
	v, err := rd.r.GetTimestamp(obj, k)
	if err != nil {
		return nil
	}
	v = v.UTC() // realm 读回带本地 zone；投影 DTO 统一 UTC（payload 可复现）
	return &v
}

// objInt64Ptr 读可空 long 列（LegacyTotalScore；缺列/NULL → nil）。
func (rd *reader) objInt64Ptr(obj unsafe.Pointer, cls, name string) *int64 {
	k, ok := rd.propByName(cls, name)
	if !ok {
		return nil
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return nil
	}
	v, err := rd.r.GetInt64(obj, k)
	if err != nil {
		return nil
	}
	return &v
}

// objFloat64Ptr 读可空 double 列（PP/EditorTimestamp；缺列/NULL → nil）。
func (rd *reader) objFloat64Ptr(obj unsafe.Pointer, cls, name string) *float64 {
	k, ok := rd.propByName(cls, name)
	if !ok {
		return nil
	}
	if null, err := rd.r.IsNull(obj, k); err == nil && null {
		return nil
	}
	v, err := rd.r.GetFloat64(obj, k)
	if err != nil {
		return nil
	}
	return &v
}

// propByName 按类名 + 列名取 key（class 不存在或缺列返回 false）。
func (rd *reader) propByName(cls, name string) (int64, bool) {
	classKey, ok := rd.class(cls)
	if !ok {
		return 0, false
	}
	return rd.prop(cls, classKey, name)
}

// linked 取单值对象链接/embedded 属性的目标行句柄（空链接返回 false；
// 句柄由调用方 ReleaseObject 释放）。
func (rd *reader) linked(obj unsafe.Pointer, cls, prop string) (unsafe.Pointer, bool) {
	k, ok := rd.propByName(cls, prop)
	if !ok {
		return nil, false
	}
	target, err := rd.r.GetLinkedObject(obj, k)
	if err != nil || target == nil {
		return nil, false
	}
	return target, true
}

// linkedShortName 读链接列指向的 Ruleset 行 ShortName（Score.Ruleset 与
// Beatmap.Ruleset 共用；空链接/缺列/缺目标列为空串）。
func (rd *reader) linkedShortName(obj unsafe.Pointer, cls, linkProp string) string {
	target, ok := rd.linked(obj, cls, linkProp)
	if !ok {
		return ""
	}
	defer rd.r.ReleaseObject(target)
	return rd.objStr(target, "Ruleset", "ShortName")
}

// linkedRulesetOnlineID 读链接列指向的 Ruleset 行 OnlineID（市场规则集
// 判定输入，D8；空链接/缺列/缺目标列为 0——官方四规则集 0–3 不会触界）。
func (rd *reader) linkedRulesetOnlineID(obj unsafe.Pointer, cls, linkProp string) int64 {
	target, ok := rd.linked(obj, cls, linkProp)
	if !ok {
		return 0
	}
	defer rd.r.ReleaseObject(target)
	return rd.objInt64OrZero(target, "Ruleset", "OnlineID")
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
