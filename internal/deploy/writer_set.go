package deploy

// 谱面集写入器（M2 主投放载体，design.md §5.1-M2 / §5.3 / §4.2 写入纪律）。
// 单批事务内的写入序列（§5.3：realm 行与文件链接同批，CopyMissing 已先行）：
//   RealmFile 行（Find(File, hash) ?? Create，跨谱面共享 blob 行不重复建）
//   → Set.Files 列表（RealmNamedFileUsage：File 链接 + Filename）
//   → 逐难度（BeatmapInfo 行 + 独立 BeatmapMetadata 行 + embedded
//     BeatmapDifficulty / BeatmapUserSettings + RealmUser + Ruleset 链接）
//   → Set 本行白名单标量列。
// 写入集 = 官方基线白名单 ∩ 目标库实际列（S1，缺列计数不静默吞）；
// 结构列（列表 / embedded / 链接）缺失视为整类不可用——结构残缺的行是
// 坏数据，宁可不写。
//
// 就地更新（同 Hash 内容键命中，§4.2）：只更新集行标量白名单列，保目标
// Guid 与目标端本地引用；同 Hash 必同难度内容集合（集 Hash = 集内全部
// .osu 拼接），不替换 Beatmaps / Files 列表——难度行的新建/复用对偶只出
// 现在版本更新的整集替换（§5.4）。
//
// 版本替换（version-replace，§5.4 三步，单批事务内）：① 以 origin_guid
// 新建新版本整套（复用 createSet）；② 目标旧版本集写 DeletePending=true
//（S4 软删）；③ 解绑旧难度上的本地成绩（Score.BeatmapInfo 关系置 null、
// BeatmapHash 字符串列保留）。
//
// S8：OnlineID 仅同 ServerRealm（origin_fork → ForkProfile 派生）携带；
// 异域新行置 -1、更新行不触碰该列（§4.4 / §5.4）。

import (
	"fmt"
	"time"

	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// 官方四规则集固定行（lazer-baseline §2.9——官方基线知识，非 fork 差异，
// 不属 ForkProfile 面）：目标缺行且属此四集时新建（Available=true），
// §5.1-M2 的难度规则集解析规则。
var officialRulesets = map[string]struct {
	onlineID          int64
	name              string
	instantiationInfo string
}{
	"osu":    {0, "osu!", "osu.Game.Rulesets.Osu.OsuRuleset, osu.Game.Rulesets.Osu"},
	"taiko":  {1, "osu!taiko", "osu.Game.Rulesets.Taiko.TaikoRuleset, osu.Game.Rulesets.Taiko"},
	"fruits": {2, "osu!catch", "osu.Game.Rulesets.Catch.CatchRuleset, osu.Game.Rulesets.Catch"},
	"mania":  {3, "osu!mania", "osu.Game.Rulesets.Mania.ManiaRuleset, osu.Game.Rulesets.Mania"},
}

// 各类标量白名单（official v51/v52 基线；磁盘列名 = MapTo 名，StatusInt
// 落盘为 "Status"，RankInt 落盘为 "Rank" 等）。ID/Hash 是主键与内容键面，
// 不参与缺列降级计数。
var (
	setScalarCols = []string{
		"ID", "OnlineID", "DateAdded", "DateSubmitted", "DateRanked",
		"Status", "DeletePending", "Hash", "Protected",
	}
	bmScalarCols = []string{
		"ID", "DifficultyName", "Status", "OnlineID", "Length", "BPM", "Hash",
		"StarRating", "MD5Hash", "OnlineMD5Hash", "LastLocalUpdate",
		"LastOnlineUpdate", "Hidden", "EndTimeObjectCount", "TotalObjectCount",
		"LastPlayed", "BeatDivisor", "EditorTimestamp",
	}
	mdScalarCols = []string{
		"Title", "TitleUnicode", "Artist", "ArtistUnicode", "Author", "Source",
		"Tags", "UserTags", "PreviewTime", "AudioFile", "BackgroundFile",
	}
	rulesetColsWanted = []string{"ShortName", "OnlineID", "Name", "InstantiationInfo", "Available"}
)

// beatmapSetWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type beatmapSetWriter struct {
	r *realmc.Realm
	// class key（embedded 类经列表/单值挂接，但其列仍按 class 寻址）。
	setCK, bmCK, mdCK, fileCK, rulesetCK           uint32
	usageCK, userCK, diffCK, usCK                  uint32
	setCols, bmCols, mdCols, fileCols, rulesetCols map[string]int64
	usageCols, userCols, diffCols, usCols          map[string]int64
	setPK, bmPK, filePK                            bool
	absent                                         bool
	absentWhy                                      string
	// diffSkips 批内难度级跳过计数（fork 专有规则集不可解析等）——集本身
	// 仍落地，计数由 Runner 在批末聚合（与整项 skip 分开的通道）。
	diffSkips map[string]int
}

func prepareBeatmapSetWriter(r *realmc.Realm) (*beatmapSetWriter, error) {
	w := &beatmapSetWriter{r: r, diffSkips: map[string]int{}}
	w.setCols, w.bmCols, w.mdCols = map[string]int64{}, map[string]int64{}, map[string]int64{}
	w.fileCols, w.rulesetCols = map[string]int64{}, map[string]int64{}
	w.usageCols, w.userCols, w.diffCols, w.usCols =
		map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}

	absent := func(why string) (*beatmapSetWriter, error) {
		w.absent, w.absentWhy = true, why
		return w, nil
	}

	for _, c := range []struct {
		name string
		key  *uint32
		cols map[string]int64
		want []string
	}{
		// 标量白名单 + 结构列一并寻址（结构列进必备检查；缺列计数只覆盖
		// 标量白名单，见 missingColumns）。
		{"BeatmapSet", &w.setCK, w.setCols, append(append([]string(nil), setScalarCols...), "Beatmaps", "Files")},
		{"Beatmap", &w.bmCK, w.bmCols, append(append([]string(nil), bmScalarCols...),
			"Ruleset", "Difficulty", "Metadata", "UserSettings", "BeatmapSet")},
		{"BeatmapMetadata", &w.mdCK, w.mdCols, mdScalarCols}, // Author/UserTags 已在白名单
		{"File", &w.fileCK, w.fileCols, []string{"Hash"}},
		{"Ruleset", &w.rulesetCK, w.rulesetCols, rulesetColsWanted},
		{"RealmNamedFileUsage", &w.usageCK, w.usageCols, []string{"File", "Filename"}},
		{"RealmUser", &w.userCK, w.userCols, []string{"OnlineID", "Username", "CountryString"}},
		{"BeatmapDifficulty", &w.diffCK, w.diffCols, []string{
			"DrainRate", "CircleSize", "OverallDifficulty", "ApproachRate",
			"SliderMultiplier", "SliderTickRate"}},
		{"BeatmapUserSettings", &w.usCK, w.usCols, []string{"Offset"}},
	} {
		ck, err := r.FindClass(c.name)
		if err != nil {
			switch c.name {
			case "BeatmapSet":
				return absent("目标库没有 BeatmapSet 表（版本过旧或非 lazer 系库）")
			case "Beatmap", "BeatmapMetadata", "File":
				return absent(fmt.Sprintf("目标库没有 %s 表，无法写谱面集全链", c.name))
			default:
				return absent(fmt.Sprintf("目标库没有 %s 类（embedded 结构缺类）", c.name))
			}
		}
		*c.key = ck
		for _, name := range c.want {
			if key, err := r.FindProperty(ck, name); err == nil {
				c.cols[name] = key
			}
		}
	}

	// 身份匹配列必备：BeatmapSet.Hash（内容键，§4.2 第一层）。
	if _, ok := w.setCols["Hash"]; !ok {
		return absent("目标库 BeatmapSet 缺 Hash 列，无法做身份匹配")
	}
	// 结构列必备（缺失 = 无法挂接全链，宁可不写；标量缺列走降级计数）。
	for _, c := range []struct {
		cls  string
		cols map[string]int64
		want []string
	}{
		{"BeatmapSet", w.setCols, []string{"Beatmaps", "Files"}},
		{"Beatmap", w.bmCols, []string{"Ruleset", "Difficulty", "Metadata", "UserSettings", "BeatmapSet"}},
		{"BeatmapMetadata", w.mdCols, []string{"Author", "UserTags"}},
		{"RealmNamedFileUsage", w.usageCols, []string{"File", "Filename"}},
		{"File", w.fileCols, []string{"Hash"}},
		{"Ruleset", w.rulesetCols, []string{"ShortName"}},
	} {
		for _, name := range c.want {
			if _, ok := c.cols[name]; !ok {
				return absent(fmt.Sprintf("目标库 %s 缺结构列 %s，无法写谱面集全链", c.cls, name))
			}
		}
	}

	w.setPK = classPrimaryKeyIs(r, w.setCK, "ID")
	w.bmPK = classPrimaryKeyIs(r, w.bmCK, "ID")
	w.filePK = classPrimaryKeyIs(r, w.fileCK, "Hash")
	return w, nil
}

// exec 执行单个谱面集计划项（§3.3 执行期现场复核 + 写入）。oldKey 是
// version-replace 项的目标旧版本集内容键（其他动作空串）。sameRealm 是
// S8 判定结果（origin_fork 与目标 fork 的 ServerRealm 相同）。
func (w *beatmapSetWriter) exec(bs hub.BeatmapSet, oldKey string, sameRealm bool, action Action) (applied, skipStat, string, error) {
	if action == ActionVersionReplace {
		return w.execVersionReplace(bs, oldKey, sameRealm)
	}
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	// 现场身份匹配：内容键 = Hash（§4.2 第一层）。
	existing, err := w.r.FindFirstByString(w.setCK, w.setCols["Hash"], bs.Hash)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配谱面集 %s: %w", bs.Hash, err)
	}
	repoint := ""
	switch {
	case existing != nil && action == ActionCreate:
		// 目标已出现同键行：新建 → 就地更新（§3.3 改判例）。
		action = ActionUpdate
		repoint = "目标已出现同键行：新建 → 就地更新"
	case existing == nil && action == ActionUpdate:
		action = ActionCreate
		repoint = "目标缺同键行：就地更新 → 新建"
	}

	if existing != nil {
		defer w.r.ReleaseObject(existing)
		// 重续安全：已一致 → 跳过。判定面 = 写面的对偶（写面不写的字段
		// 不比：难度 / Files 列表在同 Hash 语义下内容等价，§4.2）。
		same, err := w.matchesCanonical(existing, bs, sameRealm)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		if same {
			return applied{}, skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}, repoint, nil
		}
		if err := w.writeSetScalars(existing, bs, false, sameRealm); err != nil {
			return applied{}, skipStat{}, "", err
		}
		return applied{action: action}, skipStat{}, repoint, nil
	}

	obj, err := w.createSet(bs, sameRealm)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	w.r.ReleaseObject(obj)
	return applied{action: ActionCreate}, skipStat{}, repoint, nil
}

// execVersionReplace 版本更新的整集原子替换（§5.4 三步，单批事务内）：
// ① 写入新版本整套（以 origin_guid 新建 Set+难度+Metadata+Files+RealmFile）；
// ② 目标旧版本集写 DeletePending=true（S4 软删——不是删除，是「同一谱面
// 换新版本」原子动作的一半，物理清理由目标客户端兑现）；③ 解绑旧难度上
// 的本地成绩。旧集由对账定位的旧键（OldContentKey）在现场重寻址——预览
// 与执行之间目标库可能漂移（§3.3），按现场「新行在否 × 旧行在否 × 旧行
// 软删否」组合复核改判：
//
//	新不在+旧不在  → 改判 create（旧版本已被清理，无旧可替换）
//	新不在+旧在    → 完整三步（正常路径）
//	新在+旧已软删/消失 → 跳过（重续判定：三步要么整批做过要么没做过，S1）
//	新在+旧未软删  → 跳过重建只补②③（新行已由第三方/前次批落位）
//
// 重续时不比对新行标量（本地行为字段如 LastPlayed 默认保留目标值，D22；
// 整集身份以内容键幂等锚定）。
func (w *beatmapSetWriter) execVersionReplace(bs hub.BeatmapSet, oldKey string, sameRealm bool) (applied, skipStat, string, error) {
	if w.absent {
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	// DeletePending 与难度 Hash 是本动作的锚定列（软删面 + 成绩解绑面），
	// 缺列则整集替换无法执行，计数跳过（缺列不静默吞，§1.5-4）。
	dpKey, ok := w.setCols["DeletePending"]
	if !ok {
		return applied{}, skipStat{reason: "目标库 BeatmapSet 缺 DeletePending 列，无法软删旧版本（版本替换跳过）", count: 1}, "", nil
	}
	if _, ok := w.bmCols["Hash"]; !ok {
		return applied{}, skipStat{reason: "目标库 Beatmap 缺 Hash 列，无法锚定旧难度成绩解绑（版本替换跳过）", count: 1}, "", nil
	}
	newRow, err := w.r.FindFirstByString(w.setCK, w.setCols["Hash"], bs.Hash)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配谱面集 %s: %w", bs.Hash, err)
	}
	if newRow != nil {
		w.r.ReleaseObject(newRow)
	}
	oldRow, err := w.r.FindFirstByString(w.setCK, w.setCols["Hash"], oldKey)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 现场匹配旧版本谱面集 %s: %w", oldKey, err)
	}

	switch {
	case newRow != nil && oldRow == nil:
		// 旧版本已被目标端清理/手删而新版本已在：动作已兑现，无处软删。
		return applied{}, skipStat{reason: "目标已是新版本且旧版本已不在现场（重续判定：已存在 → 跳过）", count: 1}, "", nil
	case newRow != nil:
		defer w.r.ReleaseObject(oldRow)
		dp, err := w.r.GetBool(oldRow, dpKey)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		if dp {
			return applied{}, skipStat{reason: "目标已是新版本且旧版本已软删（重续判定：已存在 → 跳过）", count: 1}, "", nil
		}
		// 新行已在（第三方落位或前次批提交），跳过重建只补②③。
		if err := w.softDeleteAndUnlink(oldRow, dpKey); err != nil {
			return applied{}, skipStat{}, "", err
		}
		return applied{action: ActionVersionReplace}, skipStat{},
			"目标已出现新版本行：跳过重建，仅补软删与解绑", nil
	case oldRow == nil:
		// 新旧都不在现场：降级为普通新建（改判入报告）。
		obj, err := w.createSet(bs, sameRealm)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		w.r.ReleaseObject(obj)
		return applied{action: ActionCreate}, skipStat{},
			"目标旧版本与新版本均不在现场：版本替换 → 新建", nil
	}

	defer w.r.ReleaseObject(oldRow)
	// ① 新版本整套（§5.4：以 origin_guid 新建全链，未变难度的 blob 由
	// 链接先行保证幂等，§5.3）。
	obj, err := w.createSet(bs, sameRealm)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	w.r.ReleaseObject(obj)
	// ②③ 软删旧集 + 解绑旧难度成绩（§5.4 官方谱面更新语义的保守子集）。
	if err := w.softDeleteAndUnlink(oldRow, dpKey); err != nil {
		return applied{}, skipStat{}, "", err
	}
	return applied{action: ActionVersionReplace}, skipStat{}, "", nil
}

// softDeleteAndUnlink §5.4 三步的后两步：旧集行写 DeletePending=true（S4），
// 再解绑 Score.BeatmapInfo 关系指向旧集难度的本地成绩——关系列置 null、
// BeatmapHash 字符串列保留作历史锚点（官方 UpdateLocalScores 的保守子集；
// 不解绑则客户端兑现软删、清理旧难度行时会撞上悬挂关系）。目标库无 Score
// 表或无 BeatmapInfo 关系列时无成绩面可解绑，自然 no-op。
func (w *beatmapSetWriter) softDeleteAndUnlink(oldRow realmObjectPtr, dpKey int64) error {
	if err := w.r.SetBool(oldRow, dpKey, true); err != nil {
		return fmt.Errorf("deploy: 软删旧版本集: %w", err)
	}
	scoreCK, err := w.r.FindClass("Score")
	if err != nil {
		return nil // 无成绩表 = 无本地成绩可解绑
	}
	bmLink, err := w.r.FindProperty(scoreCK, "BeatmapInfo")
	if err != nil {
		return nil // 无关系列 = 无关系可解绑（BeatmapHash 列本就不动）
	}
	oldHashes, err := w.setBeatmapHashes(oldRow)
	if err != nil {
		return err
	}
	if len(oldHashes) == 0 {
		return nil
	}
	return w.r.ForEach(scoreCK, func(score realmObjectPtr) error {
		bm, err := w.r.GetLinkedObject(score, bmLink)
		if err != nil {
			return err
		}
		if bm == nil {
			return nil // 已解绑（历史成绩常态，lazer-baseline §6.3）
		}
		hash, err := w.r.GetString(bm, w.bmCols["Hash"])
		w.r.ReleaseObject(bm)
		if err != nil {
			return err
		}
		if !oldHashes[hash] {
			return nil
		}
		return w.r.SetNull(score, bmLink)
	})
}

// setBeatmapHashes 读集行 Beatmaps 列表内全部难度的 Hash 集合（解绑锚定面：
// Score.BeatmapInfo 关系目标 ∈ 旧集难度）。
func (w *beatmapSetWriter) setBeatmapHashes(setRow realmObjectPtr) (map[string]bool, error) {
	lst, err := w.r.GetList(setRow, w.setCols["Beatmaps"])
	if err != nil {
		return nil, fmt.Errorf("deploy: 取旧集 Beatmaps 列表: %w", err)
	}
	defer lst.Close()
	out := map[string]bool{}
	n, err := lst.Size()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < n; i++ {
		bm, err := lst.GetLinkedObject(i)
		if err != nil {
			return nil, err
		}
		if bm == nil {
			continue
		}
		hash, err := w.r.GetString(bm, w.bmCols["Hash"])
		w.r.ReleaseObject(bm)
		if err != nil {
			return nil, err
		}
		out[hash] = true
	}
	return out, nil
}

// createSet 新建整集全链（集行 → Files usage → 逐难度），返回集行句柄
// （调用方释放）。同 Guid 主键已存在 = 内容键外的罕见冲突，如实上抛
// 整批回滚（§4.2「新建以 origin_guid 为主键」的碰撞语义）。
func (w *beatmapSetWriter) createSet(bs hub.BeatmapSet, sameRealm bool) (realmObjectPtr, error) {
	var setObj realmObjectPtr
	var err error
	if w.setPK {
		id, err := parseGuid(bs.ID)
		if err != nil {
			return nil, err
		}
		setObj, err = w.r.CreateObjectWithGuidPK(w.setCK, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建谱面集 %s（Guid 主键）: %w", bs.Hash, err)
		}
	} else {
		setObj, err = w.r.CreateObject(w.setCK)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建谱面集 %s: %w", bs.Hash, err)
		}
	}
	fail := func(err error) (realmObjectPtr, error) {
		w.r.ReleaseObject(setObj)
		return nil, err
	}
	if err := w.writeSetScalars(setObj, bs, true, sameRealm); err != nil {
		return fail(err)
	}

	// Files 列表（RealmNamedFileUsage：File 链接 + Filename，§5.3）。
	if len(bs.Files) > 0 {
		lst, err := w.r.GetList(setObj, w.setCols["Files"])
		if err != nil {
			return fail(fmt.Errorf("deploy: 取谱面集 %s Files 列表: %w", bs.Hash, err))
		}
		defer lst.Close()
		for _, nf := range bs.Files {
			if err := w.appendUsage(lst, nf); err != nil {
				return fail(err)
			}
		}
	}

	// 逐难度（Beatmaps 前向列表；顺序 = canonical 序）。
	if len(bs.Beatmaps) > 0 {
		lst, err := w.r.GetList(setObj, w.setCols["Beatmaps"])
		if err != nil {
			return fail(fmt.Errorf("deploy: 取谱面集 %s Beatmaps 列表: %w", bs.Hash, err))
		}
		defer lst.Close()
		for _, bm := range bs.Beatmaps {
			rsObj, ok, err := w.resolveRuleset(bm.RulesetShortName)
			if err != nil {
				return fail(err)
			}
			if !ok {
				w.diffSkips[fmt.Sprintf("难度 %s 引用规则集 %q 在目标不可解析（fork 专有规则集不凭空插，§5.1）",
					bm.DifficultyName, bm.RulesetShortName)]++
				continue
			}
			bmObj, err := w.createBeatmap(bm, setObj, rsObj, sameRealm)
			w.r.ReleaseObject(rsObj)
			if err != nil {
				return fail(err)
			}
			err = lst.AddLinkedObject(bmObj)
			w.r.ReleaseObject(bmObj)
			if err != nil {
				return fail(fmt.Errorf("deploy: 挂接难度 %s: %w", bm.DifficultyName, err))
			}
		}
	}
	return setObj, nil
}

// writeSetScalars 写集行白名单标量列。isUpdate 时不动 ID / Hash（保目标
// Guid，§4.2；内容键命中必相等，写它是死代码）。DeletePending/Protected
// 恒写 canonical 值 false（正常进 hub 的行即如此，§4.1）。
func (w *beatmapSetWriter) writeSetScalars(obj realmObjectPtr, bs hub.BeatmapSet, isCreate, sameRealm bool) error {
	if isCreate {
		// 主键形态的 ID 已由 CreateObjectWithGuidPK 落定（主键列不可再 set）。
		if !w.setPK {
			if key, ok := w.setCols["ID"]; ok {
				id, err := parseGuid(bs.ID)
				if err != nil {
					return err
				}
				if err := w.r.SetGuid(obj, key, id); err != nil {
					return fmt.Errorf("deploy: 写谱面集 %s ID: %w", bs.Hash, err)
				}
			}
		}
		if key, ok := w.setCols["Hash"]; ok {
			if err := w.r.SetString(obj, key, bs.Hash); err != nil {
				return fmt.Errorf("deploy: 写谱面集 Hash: %w", err)
			}
		}
	}
	// OnlineID（S8）：同域随行携带；异域新行置 -1；异域更新不触碰。
	if key, ok := w.setCols["OnlineID"]; ok {
		want := bs.OnlineID
		if !sameRealm && isCreate {
			want = -1
		}
		if sameRealm || isCreate {
			if err := w.r.SetInt64(obj, key, want); err != nil {
				return fmt.Errorf("deploy: 写谱面集 %s OnlineID: %w", bs.Hash, err)
			}
		}
	}
	if key, ok := w.setCols["DateAdded"]; ok {
		if err := w.r.SetTimestamp(obj, key, bs.DateAdded); err != nil {
			return fmt.Errorf("deploy: 写谱面集 %s DateAdded: %w", bs.Hash, err)
		}
	}
	// 可空时间列：nil 显式归位（SetNull），非 nil 写值——就地更新语义下
	// 两种状态都是 canonical 覆盖面。
	for _, c := range []struct {
		name string
		val  *time.Time
	}{
		{"DateSubmitted", bs.DateSubmitted},
		{"DateRanked", bs.DateRanked},
	} {
		key, ok := w.setCols[c.name]
		if !ok {
			continue
		}
		var err error
		if c.val == nil {
			err = w.r.SetNull(obj, key)
		} else {
			err = w.r.SetTimestamp(obj, key, *c.val)
		}
		if err != nil {
			return fmt.Errorf("deploy: 写谱面集 %s %s: %w", bs.Hash, c.name, err)
		}
	}
	if key, ok := w.setCols["Status"]; ok {
		if err := w.r.SetInt64(obj, key, int64(bs.StatusInt)); err != nil {
			return fmt.Errorf("deploy: 写谱面集 %s Status: %w", bs.Hash, err)
		}
	}
	for _, c := range []struct {
		name string
		val  bool
	}{
		{"DeletePending", bs.DeletePending},
		{"Protected", bs.Protected},
	} {
		if key, ok := w.setCols[c.name]; ok {
			if err := w.r.SetBool(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写谱面集 %s %s: %w", bs.Hash, c.name, err)
			}
		}
	}
	return nil
}

// matchesCanonical 现场集行是否已与 canonical 写面一致（重续判定输入）。
// 与 writeSetScalars 严格对偶：不写的列（ID/Hash/难度/Files 列表、异域
// 更新的 OnlineID）不比。
func (w *beatmapSetWriter) matchesCanonical(obj realmObjectPtr, bs hub.BeatmapSet, sameRealm bool) (bool, error) {
	if sameRealm {
		if key, ok := w.setCols["OnlineID"]; ok {
			v, err := w.r.GetInt64(obj, key)
			if err != nil || v != bs.OnlineID {
				return false, err
			}
		}
	}
	if key, ok := w.setCols["DateAdded"]; ok {
		ts, err := w.r.GetTimestamp(obj, key)
		if err != nil || !ts.UTC().Equal(bs.DateAdded) {
			return false, err
		}
	}
	for _, c := range []struct {
		name string
		val  *time.Time
	}{
		{"DateSubmitted", bs.DateSubmitted},
		{"DateRanked", bs.DateRanked},
	} {
		key, ok := w.setCols[c.name]
		if !ok {
			continue
		}
		null, err := w.r.IsNull(obj, key)
		if err != nil {
			return false, err
		}
		if c.val == nil {
			if !null {
				return false, nil
			}
			continue
		}
		if null {
			return false, nil
		}
		ts, err := w.r.GetTimestamp(obj, key)
		if err != nil {
			return false, err
		}
		if !ts.UTC().Equal(*c.val) {
			return false, nil
		}
	}
	if key, ok := w.setCols["Status"]; ok {
		v, err := w.r.GetInt64(obj, key)
		if err != nil || v != int64(bs.StatusInt) {
			return false, err
		}
	}
	for _, c := range []struct {
		name string
		val  bool
	}{
		{"DeletePending", bs.DeletePending},
		{"Protected", bs.Protected},
	} {
		if key, ok := w.setCols[c.name]; ok {
			v, err := w.r.GetBool(obj, key)
			if err != nil || v != c.val {
				return false, err
			}
		}
	}
	return true, nil
}

// appendUsage 向 Files 列表追加一条 RealmNamedFileUsage（File 链接 +
// Filename）。File 行由 findOrCreateFileRow 取/建（Find ?? Create）。
func (w *beatmapSetWriter) appendUsage(lst *realmc.List, nf hub.NamedFile) error {
	return appendNamedFileUsage(w.r, lst, w.fileCK, w.filePK, w.fileCols, w.usageCols, nf)
}

// appendNamedFileUsage 向 Files 列表追加一条 RealmNamedFileUsage（File
// 链接 + Filename；谱面集/皮肤/成绩写入器共用，S6：RealmFile 行与模型引用
// 同事务成对写入）。File 行走 §5.3 的 `Find(File, hash) ?? Create`——跨
// 实体共享 blob 行不重复建。
func appendNamedFileUsage(r *realmc.Realm, lst *realmc.List, fileCK uint32, filePK bool,
	fileCols, usageCols map[string]int64, nf hub.NamedFile) error {
	fileRow, err := findOrCreateFileRow(r, fileCK, filePK, fileCols, nf.Hash)
	if err != nil {
		return err
	}
	defer r.ReleaseObject(fileRow)
	usage, err := lst.AddEmbedded()
	if err != nil {
		return fmt.Errorf("deploy: 建 Files usage（%s）: %w", nf.Filename, err)
	}
	defer r.ReleaseObject(usage)
	if err := r.SetString(usage, usageCols["Filename"], nf.Filename); err != nil {
		return fmt.Errorf("deploy: 写 Files usage Filename %q: %w", nf.Filename, err)
	}
	if err := r.SetLinkedObject(usage, usageCols["File"], fileRow); err != nil {
		return fmt.Errorf("deploy: 链 Files usage → RealmFile %s: %w", nf.Hash, err)
	}
	return nil
}

// findOrCreateFileRow 取/建 RealmFile 行（§5.3 `Find(File, hash) ?? Create`）。
// 主键形态走 GetOrCreate；无主键形态（fixture 镜像）查建组合。返回句柄由
// 调用方释放。
func findOrCreateFileRow(r *realmc.Realm, fileCK uint32, filePK bool, fileCols map[string]int64, hash string) (realmObjectPtr, error) {
	if filePK {
		obj, _, err := r.GetOrCreateObjectWithStringPK(fileCK, hash)
		if err != nil {
			return nil, fmt.Errorf("deploy: RealmFile %s GetOrCreate: %w", hash, err)
		}
		return obj, nil
	}
	obj, err := r.FindFirstByString(fileCK, fileCols["Hash"], hash)
	if err != nil {
		return nil, fmt.Errorf("deploy: 查 RealmFile %s: %w", hash, err)
	}
	if obj != nil {
		return obj, nil
	}
	obj, err = r.CreateObject(fileCK)
	if err != nil {
		return nil, fmt.Errorf("deploy: 新建 RealmFile %s: %w", hash, err)
	}
	if err := r.SetString(obj, fileCols["Hash"], hash); err != nil {
		r.ReleaseObject(obj)
		return nil, fmt.Errorf("deploy: 写 RealmFile %s Hash: %w", hash, err)
	}
	return obj, nil
}

// resolveRuleset 目标端规则集解析（§5.1-M2，公共实现见 resolveRulesetObj）。
func (w *beatmapSetWriter) resolveRuleset(shortName string) (realmObjectPtr, bool, error) {
	return resolveRulesetObj(w.r, w.rulesetCK, w.rulesetCols, shortName)
}

// resolveRulesetObj 目标端规则集解析（§5.1-M2；难度与成绩写入器共用，
// §5.2-4 的成绩规则集解析即此语义）：已有同名行复用；缺行且属官方四
// 规则集新建（Available=true）；fork 专有不凭空插（ok=false，调用方跳过
// 该项并计数）。返回行句柄由调用方释放。
func resolveRulesetObj(r *realmc.Realm, rulesetCK uint32, rulesetCols map[string]int64, shortName string) (realmObjectPtr, bool, error) {
	obj, err := r.FindFirstByString(rulesetCK, rulesetCols["ShortName"], shortName)
	if err != nil {
		return nil, false, fmt.Errorf("deploy: 查规则集 %q: %w", shortName, err)
	}
	if obj != nil {
		return obj, true, nil
	}
	def, ok := officialRulesets[shortName]
	if !ok {
		return nil, false, nil
	}
	obj, err = r.CreateObject(rulesetCK)
	if err != nil {
		return nil, false, fmt.Errorf("deploy: 新建官方规则集 %q: %w", shortName, err)
	}
	fail := func(err error) (realmObjectPtr, bool, error) {
		r.ReleaseObject(obj)
		return nil, false, err
	}
	writes := []func() error{
		func() error { return r.SetString(obj, rulesetCols["ShortName"], shortName) },
		func() error { return r.SetInt64(obj, rulesetCols["OnlineID"], def.onlineID) },
		func() error { return r.SetString(obj, rulesetCols["Name"], def.name) },
		func() error { return r.SetString(obj, rulesetCols["InstantiationInfo"], def.instantiationInfo) },
	}
	if key, ok := rulesetCols["Available"]; ok {
		writes = append(writes, func() error { return r.SetBool(obj, key, true) })
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return fail(fmt.Errorf("deploy: 写官方规则集 %q: %w", shortName, err))
		}
	}
	return obj, true, nil
}

// createBeatmap 新建一行难度并挂好全部结构（标量白名单全列 + Ruleset
// 链接 + Difficulty/UserSettings embedded + 独立 Metadata 行 + 父集反向
// 链接），返回行句柄（调用方释放并挂入 Beatmaps 列表）。sameRealm 是
// S8 判定结果（透传给 Metadata.Author 的 RealmUser.OnlineID 面）。
func (w *beatmapSetWriter) createBeatmap(bm hub.Beatmap, setObj, rsObj realmObjectPtr, sameRealm bool) (realmObjectPtr, error) {
	var bmObj realmObjectPtr
	var err error
	if w.bmPK {
		id, err := parseGuid(bm.ID)
		if err != nil {
			return nil, err
		}
		bmObj, err = w.r.CreateObjectWithGuidPK(w.bmCK, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建难度 %s（Guid 主键）: %w", bm.DifficultyName, err)
		}
	} else {
		bmObj, err = w.r.CreateObject(w.bmCK)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建难度 %s: %w", bm.DifficultyName, err)
		}
	}
	fail := func(err error) (realmObjectPtr, error) {
		w.r.ReleaseObject(bmObj)
		return nil, err
	}
	if !w.bmPK {
		if key, ok := w.bmCols["ID"]; ok {
			id, err := parseGuid(bm.ID)
			if err != nil {
				return fail(err)
			}
			if err := w.r.SetGuid(bmObj, key, id); err != nil {
				return fail(fmt.Errorf("deploy: 写难度 %s ID: %w", bm.DifficultyName, err))
			}
		}
	}
	for _, c := range []struct{ name, val string }{
		{"Hash", bm.Hash}, {"MD5Hash", bm.MD5Hash}, {"OnlineMD5Hash", bm.OnlineMD5Hash},
		{"DifficultyName", bm.DifficultyName},
	} {
		if key, ok := w.bmCols[c.name]; ok {
			if err := w.r.SetString(bmObj, key, c.val); err != nil {
				return fail(fmt.Errorf("deploy: 写难度 %s %s: %w", bm.DifficultyName, c.name, err))
			}
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
		if key, ok := w.bmCols[c.name]; ok {
			if err := w.r.SetInt64(bmObj, key, c.val); err != nil {
				return fail(fmt.Errorf("deploy: 写难度 %s %s: %w", bm.DifficultyName, c.name, err))
			}
		}
	}
	for _, c := range []struct {
		name string
		val  float64
	}{
		{"Length", bm.Length}, {"BPM", bm.BPM}, {"StarRating", bm.StarRating},
	} {
		if key, ok := w.bmCols[c.name]; ok {
			if err := w.r.SetFloat64(bmObj, key, c.val); err != nil {
				return fail(fmt.Errorf("deploy: 写难度 %s %s: %w", bm.DifficultyName, c.name, err))
			}
		}
	}
	if key, ok := w.bmCols["Hidden"]; ok {
		if err := w.r.SetBool(bmObj, key, bm.Hidden); err != nil {
			return fail(fmt.Errorf("deploy: 写难度 %s Hidden: %w", bm.DifficultyName, err))
		}
	}
	// 可空时间列：nil 保持新建默认 NULL（不写即归位）。
	for _, c := range []struct {
		name string
		val  *time.Time
	}{
		{"LastLocalUpdate", bm.LastLocalUpdate},
		{"LastOnlineUpdate", bm.LastOnlineUpdate},
		{"LastPlayed", bm.LastPlayed},
	} {
		if key, ok := w.bmCols[c.name]; ok && c.val != nil {
			if err := w.r.SetTimestamp(bmObj, key, *c.val); err != nil {
				return fail(fmt.Errorf("deploy: 写难度 %s %s: %w", bm.DifficultyName, c.name, err))
			}
		}
	}
	if bm.EditorTimestamp != nil {
		if key, ok := w.bmCols["EditorTimestamp"]; ok {
			if err := w.r.SetFloat64(bmObj, key, *bm.EditorTimestamp); err != nil {
				return fail(fmt.Errorf("deploy: 写难度 %s EditorTimestamp: %w", bm.DifficultyName, err))
			}
		}
	}

	if err := w.r.SetLinkedObject(bmObj, w.bmCols["Ruleset"], rsObj); err != nil {
		return fail(fmt.Errorf("deploy: 链难度 %s → Ruleset: %w", bm.DifficultyName, err))
	}

	// Difficulty embedded（六个数值列；embedded 对象结构性建出，列缺失只
	// 降级数值面）。
	if key, ok := w.bmCols["Difficulty"]; ok {
		diff, err := w.r.SetEmbedded(bmObj, key)
		if err != nil {
			return fail(fmt.Errorf("deploy: 建难度 %s Difficulty embedded: %w", bm.DifficultyName, err))
		}
		for _, c := range []struct {
			name string
			val  float32
		}{
			{"DrainRate", bm.Difficulty.DrainRate}, {"CircleSize", bm.Difficulty.CircleSize},
			{"OverallDifficulty", bm.Difficulty.OverallDifficulty},
			{"ApproachRate", bm.Difficulty.ApproachRate},
		} {
			if key, ok := w.diffCols[c.name]; ok {
				if err := w.r.SetFloat32(diff, key, c.val); err != nil {
					w.r.ReleaseObject(diff)
					return fail(fmt.Errorf("deploy: 写难度数值 %s: %w", c.name, err))
				}
			}
		}
		for _, c := range []struct {
			name string
			val  float64
		}{
			{"SliderMultiplier", bm.Difficulty.SliderMultiplier},
			{"SliderTickRate", bm.Difficulty.SliderTickRate},
		} {
			if key, ok := w.diffCols[c.name]; ok {
				if err := w.r.SetFloat64(diff, key, c.val); err != nil {
					w.r.ReleaseObject(diff)
					return fail(fmt.Errorf("deploy: 写难度数值 %s: %w", c.name, err))
				}
			}
		}
		w.r.ReleaseObject(diff)
	}

	// UserSettings embedded（唯一列 Offset 的扁平投影）。
	if key, ok := w.bmCols["UserSettings"]; ok {
		us, err := w.r.SetEmbedded(bmObj, key)
		if err != nil {
			return fail(fmt.Errorf("deploy: 建难度 %s UserSettings embedded: %w", bm.DifficultyName, err))
		}
		if key, ok := w.usCols["Offset"]; ok {
			if err := w.r.SetFloat64(us, key, bm.UserSettingsOffset); err != nil {
				w.r.ReleaseObject(us)
				return fail(fmt.Errorf("deploy: 写难度 %s Offset: %w", bm.DifficultyName, err))
			}
		}
		w.r.ReleaseObject(us)
	}

	// Metadata 独立行（每难度一份，lazer-baseline §2.5/§2.6）。
	mdObj, err := w.createMetadata(bm.Metadata, sameRealm)
	if err != nil {
		return fail(err)
	}
	if err := w.r.SetLinkedObject(bmObj, w.bmCols["Metadata"], mdObj); err != nil {
		w.r.ReleaseObject(mdObj)
		return fail(fmt.Errorf("deploy: 链难度 %s → Metadata: %w", bm.DifficultyName, err))
	}
	w.r.ReleaseObject(mdObj)

	// 父集前向链接（BeatmapInfo.BeatmapSet，lazer-baseline §2.5）。
	if err := w.r.SetLinkedObject(bmObj, w.bmCols["BeatmapSet"], setObj); err != nil {
		return fail(fmt.Errorf("deploy: 链难度 %s → BeatmapSet: %w", bm.DifficultyName, err))
	}
	return bmObj, nil
}

// createMetadata 新建一行 BeatmapMetadata（C2 白名单全列：标量 + Author
// embedded RealmUser + UserTags 列表），返回行句柄（调用方释放）。
// sameRealm 是 S8 判定结果：Author.OnlineID 同域随行携带、异域置 -1
//（本函数只服务新建链路——就地更新不动难度/Metadata，无「异域更新」态）。
func (w *beatmapSetWriter) createMetadata(md hub.Metadata, sameRealm bool) (realmObjectPtr, error) {
	mdObj, err := w.r.CreateObject(w.mdCK)
	if err != nil {
		return nil, fmt.Errorf("deploy: 新建 Metadata 行: %w", err)
	}
	fail := func(err error) (realmObjectPtr, error) {
		w.r.ReleaseObject(mdObj)
		return nil, err
	}
	for _, c := range []struct{ name, val string }{
		{"Title", md.Title}, {"TitleUnicode", md.TitleUnicode},
		{"Artist", md.Artist}, {"ArtistUnicode", md.ArtistUnicode},
		{"Source", md.Source}, {"Tags", md.Tags},
		{"AudioFile", md.AudioFile}, {"BackgroundFile", md.BackgroundFile},
	} {
		if key, ok := w.mdCols[c.name]; ok {
			if err := w.r.SetString(mdObj, key, c.val); err != nil {
				return fail(fmt.Errorf("deploy: 写 Metadata %s: %w", c.name, err))
			}
		}
	}
	if key, ok := w.mdCols["PreviewTime"]; ok {
		if err := w.r.SetInt64(mdObj, key, int64(md.PreviewTime)); err != nil {
			return fail(fmt.Errorf("deploy: 写 Metadata PreviewTime: %w", err))
		}
	}
	if key, ok := w.mdCols["Author"]; ok {
		author, err := w.r.SetEmbedded(mdObj, key)
		if err != nil {
			return fail(fmt.Errorf("deploy: 建 Metadata Author embedded: %w", err))
		}
		if key, ok := w.userCols["OnlineID"]; ok {
			// S8（RealmUser.OnlineID）：异域新行置 -1——跨服务器命名空间的
			// 用户 id 不落目标库（对齐集行 OnlineID 与成绩写入器 User.OnlineID
			// 的处理面）。
			want := int64(md.Author.OnlineID)
			if !sameRealm {
				want = -1
			}
			if err := w.r.SetInt64(author, key, want); err != nil {
				w.r.ReleaseObject(author)
				return fail(fmt.Errorf("deploy: 写 Author OnlineID: %w", err))
			}
		}
		for _, c := range []struct{ name, val string }{
			{"Username", md.Author.Username}, {"CountryString", md.Author.CountryString},
		} {
			if key, ok := w.userCols[c.name]; ok {
				if err := w.r.SetString(author, key, c.val); err != nil {
					w.r.ReleaseObject(author)
					return fail(fmt.Errorf("deploy: 写 Author %s: %w", c.name, err))
				}
			}
		}
		w.r.ReleaseObject(author)
	}
	if key, ok := w.mdCols["UserTags"]; ok && len(md.UserTags) > 0 {
		lst, err := w.r.GetList(mdObj, key)
		if err != nil {
			return fail(fmt.Errorf("deploy: 取 Metadata UserTags: %w", err))
		}
		for _, tag := range md.UserTags {
			if err := lst.AddString(tag); err != nil {
				lst.Close()
				return fail(fmt.Errorf("deploy: 写 UserTags %q: %w", tag, err))
			}
		}
		lst.Close()
	}
	return mdObj, nil
}

// missingColumns 报告各标量白名单 ∩ 目标实际列的差集（"类.列" 形态，
// 人话原因的列名清单；结构列缺失已走 absent 不在此列）。
func (w *beatmapSetWriter) missingColumns() []string {
	if w.absent {
		return nil
	}
	var out []string
	for _, c := range []struct {
		cls  string
		cols map[string]int64
		want []string
		skip map[string]bool
	}{
		{"BeatmapSet", w.setCols, setScalarCols, map[string]bool{"ID": true, "Hash": true}},
		{"Beatmap", w.bmCols, bmScalarCols, map[string]bool{"ID": true}},
		{"BeatmapMetadata", w.mdCols, mdScalarCols, nil},
		{"Ruleset", w.rulesetCols, rulesetColsWanted, nil},
	} {
		for _, name := range c.want {
			if c.skip[name] {
				continue
			}
			if _, ok := c.cols[name]; !ok {
				out = append(out, c.cls+"."+name)
			}
		}
	}
	return out
}
