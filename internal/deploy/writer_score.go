package deploy

// 成绩写入器（M2，design.md §5.2 全部跳过分支 + §5.4 目标端兼容过滤）：
// 目标无 Score 表 / BeatmapHash 为空 / 目标缺对应难度 Hash / 规则集不可
// 解析（fork 专有，市场规则集行 D8 的兜底防线——目标端解析不了市场行
// 同样走此分支）/ mod 名录外或 JSON 解析失败（D7）→ 逐分支计数 + 人话
// 原因，绝不落地悬空行（S7）。
//
// 挂接成功：Score.BeatmapInfo 关系链到目标难度对象 + BeatmapHash 列存
// 字符串 + Files=[.osr] usage + RealmFile 行同事务（S6；.osr 链接先行由
// CopyMissing 在批前完成，§5.3）。难度索引惰性构建（??= 语义，§5.2-3）：
// 目标全部非隐藏难度的 Hash 全表扫一次，批内复用——谱面集批先于成绩批
//（splitKindBatches 批序），本次刚同步进来的难度也能挂上。
//
// 同键 (BeatmapHash, Hash) 已存在 → 就地更新白名单列（§5.2 去重）：
// OnlineID/LegacyOnlineID/User.OnlineID 列不触碰（S8——更新行不动该列；
// 新行同域随行携带、异域置 -1）。Files 列表同键必同回放（.osr 内容哈希
// 是内容键的一半），不重写（谱面集 update 不动列表的同款先例）。

import (
	"fmt"

	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// 成绩白名单标量列（official v51/v52 基线，lazer-baseline §2.7；磁盘列名
// = MapTo 名：Mods/Statistics/MaximumStatistics/Rank）。ID/Hash/BeatmapHash
// 是主键与内容键面，不参与缺列降级计数。
var scoreScalarCols = []string{
	"ID", "ClientVersion", "BeatmapHash", "Hash", "DeletePending",
	"TotalScore", "TotalScoreWithoutMods", "TotalScoreVersion", "LegacyTotalScore",
	"BackgroundReprocessingFailed", "MaxCombo", "Accuracy", "Date", "PP",
	"OnlineID", "LegacyOnlineID", "Mods", "Statistics", "MaximumStatistics",
	"Rank", "Combo", "IsLegacyScore",
}

// scoreWriter 缓存一次写事务内的目标库结构寻址与白名单交集。
type scoreWriter struct {
	r *realmc.Realm
	// class key（RealmUser 是 embedded 类，经 User 单值挂接）。
	scoreCK, bmCK, rulesetCK, fileCK, usageCK, userCK             uint32
	scoreCols, bmCols, rulesetCols, fileCols, usageCols, userCols map[string]int64
	scorePK, filePK                                               bool
	absent                                                        bool
	absentWhy                                                     string
	// statKeyDrops 批内键级过滤计数（Statistics 未知 HitResult 键——整条
	// 仍落地，被滤键逐个计数，§1.5-4；diffSkips 的成绩版先例）。
	statKeyDrops map[string]int
	// bmIndex 目标全部非隐藏难度 Hash 集合（惰性构建：首个成绩项执行时
	// 全表扫一次，批内复用——谱面写入之后构建，本次刚同步的难度可挂，
	// §5.2-3）。nil = 未构建；构建后即使空集也不再重建（??= 语义）。
	bmIndex map[string]bool
}

func prepareScoreWriter(r *realmc.Realm) (*scoreWriter, error) {
	w := &scoreWriter{r: r, statKeyDrops: map[string]int{}}
	w.scoreCols, w.bmCols, w.rulesetCols = map[string]int64{}, map[string]int64{}, map[string]int64{}
	w.fileCols, w.usageCols, w.userCols = map[string]int64{}, map[string]int64{}, map[string]int64{}
	absent := func(why string) (*scoreWriter, error) {
		w.absent, w.absentWhy = true, why
		return w, nil
	}

	for _, c := range []struct {
		name string
		key  *uint32
		cols map[string]int64
		want []string
	}{
		// 标量白名单 + 结构列一并寻址（结构列进必备检查）。
		{"Score", &w.scoreCK, w.scoreCols, append(append([]string(nil), scoreScalarCols...),
			"BeatmapInfo", "Ruleset", "User", "Pauses", "Files")},
		{"Beatmap", &w.bmCK, w.bmCols, []string{"Hash", "Hidden"}},
		{"Ruleset", &w.rulesetCK, w.rulesetCols, rulesetColsWanted},
		{"File", &w.fileCK, w.fileCols, []string{"Hash"}},
		{"RealmNamedFileUsage", &w.usageCK, w.usageCols, []string{"File", "Filename"}},
		{"RealmUser", &w.userCK, w.userCols, []string{"OnlineID", "Username", "CountryString"}},
	} {
		ck, err := r.FindClass(c.name)
		if err != nil {
			switch c.name {
			case "Score":
				// §5.2-1：目标库没有 Score 表（版本过旧）→ 整面跳过。
				return absent("目标库没有 Score 表（版本过旧或非 lazer 系库）")
			case "Beatmap":
				return absent("目标库没有 Beatmap 表，无法挂接成绩（S7）")
			case "Ruleset":
				return absent("目标库没有 Ruleset 表，无法解析成绩规则集")
			case "File":
				return absent("目标库没有 File 表，无法写成绩回放链路")
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

	// 身份匹配列必备：组合内容键 (BeatmapHash, Hash)（§4.2 第一层）。
	for _, name := range []string{"BeatmapHash", "Hash"} {
		if _, ok := w.scoreCols[name]; !ok {
			return absent(fmt.Sprintf("目标库 Score 缺 %s 列，无法做身份匹配", name))
		}
	}
	// 结构列必备（缺失 = 无法挂接全链，宁可不写；标量缺列走降级计数）。
	for _, c := range []struct {
		cls  string
		cols map[string]int64
		want []string
	}{
		{"Score", w.scoreCols, []string{"BeatmapInfo", "Ruleset", "User", "Pauses", "Files"}},
		{"Beatmap", w.bmCols, []string{"Hash"}},
		{"Ruleset", w.rulesetCols, []string{"ShortName"}},
		{"RealmNamedFileUsage", w.usageCols, []string{"File", "Filename"}},
		{"File", w.fileCols, []string{"Hash"}},
	} {
		for _, name := range c.want {
			if _, ok := c.cols[name]; !ok {
				return absent(fmt.Sprintf("目标库 %s 缺结构列 %s，无法写成绩全链", c.cls, name))
			}
		}
	}

	w.scorePK = classPrimaryKeyIs(r, w.scoreCK, "ID")
	w.filePK = classPrimaryKeyIs(r, w.fileCK, "Hash")
	return w, nil
}

// exec 执行单个成绩计划项（§3.3 执行期现场复核 + §5.4 目标端兼容过滤）。
// sameRealm 是 S8 判定结果（origin_fork 与目标 fork 的 ServerRealm 相同）；
// targetFork 驱动 mod/HitResult 白名单（Profile 数据，D7）。
func (w *scoreWriter) exec(sc hub.Score, sameRealm bool, targetFork string, action Action) (applied, skipStat, string, error) {
	if w.absent {
		// §5.2-1：目标库没有 Score 表。
		return applied{}, skipStat{reason: w.absentWhy, count: 1}, "", nil
	}
	// §5.4-D7：目标端 mod 名录与 HitResult 键白名单。整条拒绝 → 计数
	// 跳过；键级过滤 → 替换 sc 后续读写共用（重续判定天然对偶），被滤
	// 键累计入批报告。
	var reject string
	var dropped int
	sc, dropped, reject = adaptScoreForTarget(sc, targetFork)
	if reject != "" {
		return applied{}, skipStat{reason: reject, count: 1}, "", nil
	}
	if dropped > 0 {
		w.statKeyDrops[skipReasonStatsKeyDropped] += dropped
	}
	// §5.2-2：BeatmapHash 为空 → 跳过（无谱面锚点，落地即悬空行，S7）。
	if sc.BeatmapHash == "" {
		return applied{}, skipStat{reason: "成绩 BeatmapHash 为空（无谱面锚点，不落地悬空行）", count: 1}, "", nil
	}
	// §5.2-3：目标库缺对应难度 Hash → 跳过并计数（惰性索引 ??= 构建）。
	if err := w.ensureBeatmapIndex(); err != nil {
		return applied{}, skipStat{}, "", err
	}
	if !w.bmIndex[sc.BeatmapHash] {
		return applied{}, skipStat{
			reason: fmt.Sprintf("目标库缺少对应谱面 Hash %s（成绩跳过，S7 不落地悬空行）", hashPrefix(sc.BeatmapHash)),
			count:  1,
		}, "", nil
	}
	// §5.2-4：规则集不可解析（fork 专有规则集在目标端不可解析）→ 跳过。
	rsObj, ok, err := resolveRulesetObj(w.r, w.rulesetCK, w.rulesetCols, sc.RulesetShortName)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	if !ok {
		return applied{}, skipStat{
			reason: fmt.Sprintf("成绩引用规则集 %q 在目标不可解析（fork 专有规则集不凭空插，§5.2-4）", sc.RulesetShortName),
			count:  1,
		}, "", nil
	}
	defer w.r.ReleaseObject(rsObj)

	// 现场身份匹配：组合内容键 (BeatmapHash, Hash)。
	existing, mismatch, err := w.findScoreRow(sc)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	repoint := ""
	switch {
	case mismatch:
		// 异常形态（同 .osr Hash 挂不同难度，理论上 .osr 头锚定 BeatmapHash
		// 不该出现）：无法安全判定身份，宁少勿错（§5.4）。
		return applied{}, skipStat{
			reason: fmt.Sprintf("目标存在同回放 Hash 但 BeatmapHash 不符的异常行（%s），无法安全判定身份，跳过", hashPrefix(sc.Hash)),
			count:  1,
		}, "", nil
	case existing != nil && action == ActionCreate:
		// 目标已出现同键行：新建 → 就地更新（§3.3 改判例；§5.2 去重）。
		action = ActionUpdate
		repoint = "目标已出现同键行：新建 → 就地更新"
	case existing == nil && action == ActionUpdate:
		action = ActionCreate
		repoint = "目标缺同键行：就地更新 → 新建"
	}

	// 难度行句柄（挂接目标：索引已确认存在，按 Hash 现场取行）。
	bmRow, err := w.r.FindFirstByString(w.bmCK, w.bmCols["Hash"], sc.BeatmapHash)
	if err != nil {
		return applied{}, skipStat{}, "", fmt.Errorf("deploy: 取目标难度 %s: %w", hashPrefix(sc.BeatmapHash), err)
	}
	if bmRow == nil {
		// 索引与现场之间目标库被并发改动（批事务内理论不可达）；防御性
		// 跳过而非写悬空关系。
		return applied{}, skipStat{
			reason: fmt.Sprintf("目标难度 %s 在挂接时消失（现场漂移，跳过）", hashPrefix(sc.BeatmapHash)),
			count:  1,
		}, repoint, nil
	}
	defer w.r.ReleaseObject(bmRow)

	if existing != nil {
		defer w.r.ReleaseObject(existing)
		// 重续安全：已一致 → 跳过。判定面 = 写面的对偶（Files 列表同键必
		// 同回放，不比；S8 豁免列异域更新不写、不比）。
		same, err := w.matchesCanonical(existing, sc, sameRealm)
		if err != nil {
			return applied{}, skipStat{}, "", err
		}
		if same {
			return applied{}, skipStat{reason: "目标已是 canonical 值（重续判定：已存在 → 跳过）", count: 1}, repoint, nil
		}
		if err := w.writeScore(existing, sc, false, sameRealm, bmRow, rsObj); err != nil {
			return applied{}, skipStat{}, "", err
		}
		return applied{action: action}, skipStat{}, repoint, nil
	}

	obj, err := w.createScore(sc, sameRealm, bmRow, rsObj)
	if err != nil {
		return applied{}, skipStat{}, "", err
	}
	w.r.ReleaseObject(obj)
	return applied{action: ActionCreate}, skipStat{}, repoint, nil
}

// ensureBeatmapIndex 惰性构建目标难度索引（§5.2-3：目标全部非隐藏难度的
// Hash；Hidden 列缺失时按「全部难度非隐藏」降级——缺列自然降级，§4.1）。
func (w *scoreWriter) ensureBeatmapIndex() error {
	if w.bmIndex != nil {
		return nil
	}
	w.bmIndex = map[string]bool{}
	hiddenKey, hasHidden := w.bmCols["Hidden"]
	hashKey := w.bmCols["Hash"]
	return w.r.ForEach(w.bmCK, func(obj realmObjectPtr) error {
		if hasHidden {
			hidden, err := w.r.GetBool(obj, hiddenKey)
			if err != nil {
				return fmt.Errorf("deploy: 读难度 Hidden（成绩索引）: %w", err)
			}
			if hidden {
				return nil
			}
		}
		hash, err := w.r.GetString(obj, hashKey)
		if err != nil {
			return fmt.Errorf("deploy: 读难度 Hash（成绩索引）: %w", err)
		}
		w.bmIndex[hash] = true
		return nil
	})
}

// findScoreRow 按组合键 (BeatmapHash, Hash) 找目标现场行。快路径按 Hash
// 列查首行（.osr 内容哈希唯一性极强）再校验 BeatmapHash；命中但键不符
// 返回 mismatch=true（异常形态，调用方跳过——宁少勿错）。
func (w *scoreWriter) findScoreRow(sc hub.Score) (obj realmObjectPtr, mismatch bool, err error) {
	first, err := w.r.FindFirstByString(w.scoreCK, w.scoreCols["Hash"], sc.Hash)
	if err != nil {
		return nil, false, fmt.Errorf("deploy: 现场匹配成绩 %s: %w", hashPrefix(sc.Hash), err)
	}
	if first == nil {
		return nil, false, nil
	}
	bh, err := w.r.GetString(first, w.scoreCols["BeatmapHash"])
	if err != nil {
		w.r.ReleaseObject(first)
		return nil, false, err
	}
	if bh == sc.BeatmapHash {
		return first, false, nil
	}
	w.r.ReleaseObject(first)
	return nil, true, nil
}

// createScore 新建成绩行全链（行 + 全部白名单列与结构 + Files usage）。
// 同 Guid 主键已存在 = 内容键外的罕见冲突，如实上抛整批回滚（§4.2）。
func (w *scoreWriter) createScore(sc hub.Score, sameRealm bool, bmRow, rsObj realmObjectPtr) (realmObjectPtr, error) {
	var obj realmObjectPtr
	var err error
	if w.scorePK {
		id, err := parseGuid(sc.ID)
		if err != nil {
			return nil, err
		}
		obj, err = w.r.CreateObjectWithGuidPK(w.scoreCK, id)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建成绩 %s（Guid 主键）: %w", hashPrefix(sc.Hash), err)
		}
	} else {
		obj, err = w.r.CreateObject(w.scoreCK)
		if err != nil {
			return nil, fmt.Errorf("deploy: 新建成绩 %s: %w", hashPrefix(sc.Hash), err)
		}
	}
	fail := func(err error) (realmObjectPtr, error) {
		w.r.ReleaseObject(obj)
		return nil, err
	}
	if err := w.writeScore(obj, sc, true, sameRealm, bmRow, rsObj); err != nil {
		return fail(err)
	}
	if len(sc.Files) > 0 {
		lst, err := w.r.GetList(obj, w.scoreCols["Files"])
		if err != nil {
			return fail(fmt.Errorf("deploy: 取成绩 Files 列表: %w", err))
		}
		defer lst.Close()
		for _, nf := range sc.Files {
			if err := appendNamedFileUsage(w.r, lst, w.fileCK, w.filePK, w.fileCols, w.usageCols, nf); err != nil {
				return fail(err)
			}
		}
	}
	return obj, nil
}

// writeScore 写成绩白名单列与结构。isCreate 时写 ID/Hash/BeatmapHash
// （内容键）；OnlineID/LegacyOnlineID/User.OnlineID 按 S8：同域随行携带、
// 异域新行置 -1、异域更新不触碰（§4.4）。
func (w *scoreWriter) writeScore(obj realmObjectPtr, sc hub.Score, isCreate, sameRealm bool, bmRow, rsObj realmObjectPtr) error {
	if isCreate {
		if !w.scorePK {
			if key, ok := w.scoreCols["ID"]; ok {
				id, err := parseGuid(sc.ID)
				if err != nil {
					return err
				}
				if err := w.r.SetGuid(obj, key, id); err != nil {
					return fmt.Errorf("deploy: 写成绩 ID: %w", err)
				}
			}
		}
		for _, c := range []struct{ name, val string }{
			{"Hash", sc.Hash}, {"BeatmapHash", sc.BeatmapHash},
		} {
			if err := w.r.SetString(obj, w.scoreCols[c.name], c.val); err != nil {
				return fmt.Errorf("deploy: 写成绩 %s: %w", c.name, err)
			}
		}
	}
	if key, ok := w.scoreCols["ClientVersion"]; ok {
		if err := w.r.SetString(obj, key, sc.ClientVersion); err != nil {
			return fmt.Errorf("deploy: 写成绩 ClientVersion: %w", err)
		}
	}
	for _, c := range []struct {
		name string
		val  int64
	}{
		{"TotalScore", sc.TotalScore}, {"TotalScoreWithoutMods", sc.TotalScoreWithoutMods},
		{"TotalScoreVersion", int64(sc.TotalScoreVersion)}, {"MaxCombo", int64(sc.MaxCombo)},
		{"Rank", int64(sc.RankInt)}, {"Combo", int64(sc.Combo)},
	} {
		if key, ok := w.scoreCols[c.name]; ok {
			if err := w.r.SetInt64(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写成绩 %s: %w", c.name, err)
			}
		}
	}
	// 可空列：nil 显式归位（SetNull），非 nil 写值——就地更新语义下两种
	// 状态都是 canonical 覆盖面。
	if key, ok := w.scoreCols["LegacyTotalScore"]; ok {
		var err error
		if sc.LegacyTotalScore == nil {
			err = w.r.SetNull(obj, key)
		} else {
			err = w.r.SetInt64(obj, key, *sc.LegacyTotalScore)
		}
		if err != nil {
			return fmt.Errorf("deploy: 写成绩 LegacyTotalScore: %w", err)
		}
	}
	if key, ok := w.scoreCols["PP"]; ok {
		var err error
		if sc.PP == nil {
			err = w.r.SetNull(obj, key)
		} else {
			err = w.r.SetFloat64(obj, key, *sc.PP)
		}
		if err != nil {
			return fmt.Errorf("deploy: 写成绩 PP: %w", err)
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
		if key, ok := w.scoreCols[c.name]; ok {
			if err := w.r.SetBool(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写成绩 %s: %w", c.name, err)
			}
		}
	}
	if key, ok := w.scoreCols["Accuracy"]; ok {
		if err := w.r.SetFloat64(obj, key, sc.Accuracy); err != nil {
			return fmt.Errorf("deploy: 写成绩 Accuracy: %w", err)
		}
	}
	if key, ok := w.scoreCols["Date"]; ok {
		if err := w.r.SetTimestamp(obj, key, sc.Date); err != nil {
			return fmt.Errorf("deploy: 写成绩 Date: %w", err)
		}
	}
	// S8 三列：Score.OnlineID/LegacyOnlineID（§1.1-S8 明列）——同域随行
	// 携带；异域新行置 -1；异域更新不触碰。
	s8Int := func(name string, val int64) error {
		key, ok := w.scoreCols[name]
		if !ok {
			return nil
		}
		want := val
		if !sameRealm && isCreate {
			want = -1
		}
		if !sameRealm && !isCreate {
			return nil // 更新行不触碰该列
		}
		if err := w.r.SetInt64(obj, key, want); err != nil {
			return fmt.Errorf("deploy: 写成绩 %s: %w", name, err)
		}
		return nil
	}
	if err := s8Int("OnlineID", sc.OnlineID); err != nil {
		return err
	}
	if err := s8Int("LegacyOnlineID", sc.LegacyOnlineID); err != nil {
		return err
	}
	for _, c := range []struct{ name, val string }{
		{"Mods", sc.ModsJson}, {"Statistics", sc.StatisticsJson},
		{"MaximumStatistics", sc.MaximumStatisticsJson},
	} {
		if key, ok := w.scoreCols[c.name]; ok {
			if err := w.r.SetString(obj, key, c.val); err != nil {
				return fmt.Errorf("deploy: 写成绩 %s: %w", c.name, err)
			}
		}
	}
	// Pauses 列表整段重写（清空重灌——顺序数据，就地 diff 无收益）。
	if key, ok := w.scoreCols["Pauses"]; ok {
		lst, err := w.r.GetList(obj, key)
		if err != nil {
			return fmt.Errorf("deploy: 取成绩 Pauses 列表: %w", err)
		}
		if err := lst.Clear(); err != nil {
			lst.Close()
			return fmt.Errorf("deploy: 清成绩 Pauses 列表: %w", err)
		}
		for _, p := range sc.Pauses {
			if err := lst.AddInt64(int64(p)); err != nil {
				lst.Close()
				return fmt.Errorf("deploy: 写成绩 Pauses: %w", err)
			}
		}
		lst.Close()
	}
	// 结构挂接：BeatmapInfo → 目标难度（§5.2-5）、Ruleset → 解析行。
	if err := w.r.SetLinkedObject(obj, w.scoreCols["BeatmapInfo"], bmRow); err != nil {
		return fmt.Errorf("deploy: 链成绩 → BeatmapInfo: %w", err)
	}
	if err := w.r.SetLinkedObject(obj, w.scoreCols["Ruleset"], rsObj); err != nil {
		return fmt.Errorf("deploy: 链成绩 → Ruleset: %w", err)
	}
	// User embedded（归属用户三列）。SetEmbedded 是替换语义（旧 embedded
	// 随之销毁，realmc rw.go 实证）——异域更新无法逐列豁免 OnlineID，
	// 先读出现场值、替换后回写，等价于「更新行不触碰该列」（S8）。
	userOnlineID := int64(-1)
	if !sameRealm && !isCreate {
		old, err := w.r.GetLinkedObject(obj, w.scoreCols["User"])
		if err != nil {
			return fmt.Errorf("deploy: 读成绩 User embedded（S8 保留 OnlineID）: %w", err)
		}
		if old != nil {
			if key, ok := w.userCols["OnlineID"]; ok {
				if v, err := w.r.GetInt64(old, key); err == nil {
					userOnlineID = v
				}
			}
			w.r.ReleaseObject(old)
		}
	}
	user, err := w.r.SetEmbedded(obj, w.scoreCols["User"])
	if err != nil {
		return fmt.Errorf("deploy: 建成绩 User embedded: %w", err)
	}
	fail := func(err error) error {
		w.r.ReleaseObject(user)
		return err
	}
	if key, ok := w.userCols["OnlineID"]; ok {
		want := int64(sc.User.OnlineID)
		switch {
		case sameRealm: // 同域随行携带
		case isCreate: // 异域新行置 -1
			want = -1
		default: // 异域更新：现场值回写（S8 不触碰的 embedded 等价）
			want = userOnlineID
		}
		if err := w.r.SetInt64(user, key, want); err != nil {
			return fail(fmt.Errorf("deploy: 写成绩 User.OnlineID: %w", err))
		}
	}
	for _, c := range []struct{ name, val string }{
		{"Username", sc.User.Username}, {"CountryString", sc.User.CountryString},
	} {
		if key, ok := w.userCols[c.name]; ok {
			if err := w.r.SetString(user, key, c.val); err != nil {
				return fail(fmt.Errorf("deploy: 写成绩 User.%s: %w", c.name, err))
			}
		}
	}
	w.r.ReleaseObject(user)
	return nil
}

// matchesCanonical 现场成绩行是否已与 canonical 写面一致（重续判定输入）。
// 与 writeScore 严格对偶：不写的列（ID/Hash/BeatmapHash/Files 列表、异域
// 更新的 S8 三列）不比。
func (w *scoreWriter) matchesCanonical(obj realmObjectPtr, sc hub.Score, sameRealm bool) (bool, error) {
	if key, ok := w.scoreCols["ClientVersion"]; ok {
		v, err := w.r.GetString(obj, key)
		if err != nil || v != sc.ClientVersion {
			return false, err
		}
	}
	for _, c := range []struct {
		name string
		val  int64
	}{
		{"TotalScore", sc.TotalScore}, {"TotalScoreWithoutMods", sc.TotalScoreWithoutMods},
		{"TotalScoreVersion", int64(sc.TotalScoreVersion)}, {"MaxCombo", int64(sc.MaxCombo)},
		{"Rank", int64(sc.RankInt)}, {"Combo", int64(sc.Combo)},
	} {
		if key, ok := w.scoreCols[c.name]; ok {
			v, err := w.r.GetInt64(obj, key)
			if err != nil || v != c.val {
				return false, err
			}
		}
	}
	if key, ok := w.scoreCols["LegacyTotalScore"]; ok {
		null, err := w.r.IsNull(obj, key)
		if err != nil {
			return false, err
		}
		if sc.LegacyTotalScore == nil {
			if !null {
				return false, nil
			}
		} else if null {
			return false, nil
		} else if v, err := w.r.GetInt64(obj, key); err != nil || v != *sc.LegacyTotalScore {
			return false, err
		}
	}
	if key, ok := w.scoreCols["PP"]; ok {
		null, err := w.r.IsNull(obj, key)
		if err != nil {
			return false, err
		}
		if sc.PP == nil {
			if !null {
				return false, nil
			}
		} else if null {
			return false, nil
		} else if v, err := w.r.GetFloat64(obj, key); err != nil || v != *sc.PP {
			return false, err
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
		if key, ok := w.scoreCols[c.name]; ok {
			v, err := w.r.GetBool(obj, key)
			if err != nil || v != c.val {
				return false, err
			}
		}
	}
	if key, ok := w.scoreCols["Accuracy"]; ok {
		v, err := w.r.GetFloat64(obj, key)
		if err != nil || v != sc.Accuracy {
			return false, err
		}
	}
	if key, ok := w.scoreCols["Date"]; ok {
		v, err := w.r.GetTimestamp(obj, key)
		if err != nil || !v.UTC().Equal(sc.Date) {
			return false, err
		}
	}
	if sameRealm {
		if key, ok := w.scoreCols["OnlineID"]; ok {
			v, err := w.r.GetInt64(obj, key)
			if err != nil || v != sc.OnlineID {
				return false, err
			}
		}
		if key, ok := w.scoreCols["LegacyOnlineID"]; ok {
			v, err := w.r.GetInt64(obj, key)
			if err != nil || v != sc.LegacyOnlineID {
				return false, err
			}
		}
	}
	for _, c := range []struct{ name, val string }{
		{"Mods", sc.ModsJson}, {"Statistics", sc.StatisticsJson},
		{"MaximumStatistics", sc.MaximumStatisticsJson},
	} {
		if key, ok := w.scoreCols[c.name]; ok {
			v, err := w.r.GetString(obj, key)
			if err != nil || v != c.val {
				return false, err
			}
		}
	}
	if key, ok := w.scoreCols["Pauses"]; ok {
		lst, err := w.r.GetList(obj, key)
		if err != nil {
			return false, err
		}
		n, err := lst.Size()
		if err != nil {
			lst.Close()
			return false, err
		}
		if int(n) != len(sc.Pauses) {
			lst.Close()
			return false, nil
		}
		for i, want := range sc.Pauses {
			v, err := lst.GetInt64(uint64(i))
			if err != nil || v != int64(want) {
				lst.Close()
				return false, err
			}
		}
		lst.Close()
	}
	// 结构挂接对偶：BeatmapInfo 关系指向的目标难度 Hash 与 Ruleset 指向
	// 的 ShortName（挂接面是写面的一部分）。
	if bm, err := w.r.GetLinkedObject(obj, w.scoreCols["BeatmapInfo"]); err != nil {
		return false, err
	} else if bm == nil {
		return false, nil // 写面恒挂接，空关系必不一致
	} else {
		v, err := w.r.GetString(bm, w.bmCols["Hash"])
		w.r.ReleaseObject(bm)
		if err != nil {
			return false, err
		}
		if v != sc.BeatmapHash {
			return false, nil
		}
	}
	if rs, err := w.r.GetLinkedObject(obj, w.scoreCols["Ruleset"]); err != nil {
		return false, err
	} else if rs == nil {
		return false, nil
	} else {
		v, err := w.r.GetString(rs, w.rulesetCols["ShortName"])
		w.r.ReleaseObject(rs)
		if err != nil {
			return false, err
		}
		if v != sc.RulesetShortName {
			return false, nil
		}
	}
	// User embedded 对偶（异域更新不写 OnlineID，故只同域才比）。
	if user, err := w.r.GetLinkedObject(obj, w.scoreCols["User"]); err != nil {
		return false, err
	} else if user == nil {
		return false, nil
	} else {
		if sameRealm {
			if key, ok := w.userCols["OnlineID"]; ok {
				v, err := w.r.GetInt64(user, key)
				if err != nil || v != int64(sc.User.OnlineID) {
					w.r.ReleaseObject(user)
					return false, err
				}
			}
		}
		for _, c := range []struct{ name, val string }{
			{"Username", sc.User.Username}, {"CountryString", sc.User.CountryString},
		} {
			if key, ok := w.userCols[c.name]; ok {
				v, err := w.r.GetString(user, key)
				if err != nil || v != c.val {
					w.r.ReleaseObject(user)
					return false, err
				}
			}
		}
		w.r.ReleaseObject(user)
	}
	return true, nil
}

// missingColumns 报告白名单 ∩ 目标实际列的差集（"类.列" 形态；结构列缺失
// 已走 absent 不在此列）。
func (w *scoreWriter) missingColumns() []string {
	if w.absent {
		return nil
	}
	var out []string
	for _, name := range scoreScalarCols {
		if name == "ID" || name == "Hash" || name == "BeatmapHash" {
			continue
		}
		if _, ok := w.scoreCols[name]; !ok {
			out = append(out, "Score."+name)
		}
	}
	return out
}
