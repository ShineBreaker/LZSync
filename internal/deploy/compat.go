package deploy

// §5.4 目标端兼容过滤的成绩面（表前两行的成绩规则）：mods acronym
// 白名单（宁拒勿杀）与 StatisticsJson/MaximumStatisticsJson 的目标端
// HitResult 键白名单（过滤后写，解析失败整条跳过，D7）。判定/转换是
// 纯函数——预演（对账/计划面复用）与执行期现场复核（§3.3）共用同一
// 实现，两处判定天然一致。规则数据全部来自 ForkProfile（§4.3：内核
// 不知道任何具体 fork）。

import (
	"encoding/json"
	"fmt"
	"strings"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
)

// externalSetHash 判定内容键是否外部托管集的路径编码 Hash（任一注册
// fork 的 ExtSetHashPrefix 前缀命中，§4.3 数据化）。预演（BuildPlan）
// 与执行期（beatmapSetWriter.exec 防线）共用，两处一致（§3.3）。
func externalSetHash(hash string) bool {
	for _, p := range forks.All() {
		if p.ExtSetHashPrefix != "" && strings.HasPrefix(hash, p.ExtSetHashPrefix) {
			return true
		}
	}
	return false
}

// apiMod 是 ModsJson（APIMod[] JSON，lazer-baseline §2.7）的最小解码面：
// 白名单只消费 acronym（Ordinal 区分大小写，fork-comparison D7）。
type apiMod struct {
	Acronym string `json:"acronym"`
}

// ---- 键位 ActionInt 枚举域（D20，§5.4） ----

// keyBindingActionAllowed D20：键位行投放前按源端（origin_fork）与目标
// 端的枚举域交集过滤，越域跳过（宁少勿错——高版本端的新增动作在低版本
// 端可能指向错误动作或越界，fork-comparison D20：官方 2026-07/08 后新增
// 22 个尾部键位〔含 EditorSelectTool 等 13 编辑器键位〕、delta 基线全缺）。
// 域数据落 ForkProfile.KeyBindingAction（四端 2026-10-25 实测取样，见该
// 字段注释）；任一端域未取样（0）或 fork 未注册 → 放行（交集不可判定；
// 结构一致（C10）下放行不产生坏库，仅语义可能错位）。ActionInt 为负
// 恒拒（枚举值域非负，负值是坏数据）。
func keyBindingActionAllowed(originFork, targetFork string, kb hub.KeyBinding) (bool, string) {
	if kb.ActionInt < 0 {
		return false, fmt.Sprintf("键位 ActionInt=%d 为负（枚举值域非负，坏数据）", kb.ActionInt)
	}
	src, srcOK := forks.Profile(originFork)
	dst, dstOK := forks.Profile(targetFork)
	if !srcOK || !dstOK {
		return true, ""
	}
	domainMax := func(d forks.KeyBindingActionDomain) int {
		if kb.RulesetName == nil {
			return d.GlobalMax
		}
		return d.RulesetMax
	}
	if m := domainMax(src.KeyBindingAction); m > 0 && kb.ActionInt >= m {
		return false, fmt.Sprintf("键位 ActionInt=%d 超出源端 %s 枚举域 [0,%d)（D20 越域跳过）",
			kb.ActionInt, originFork, m)
	}
	if m := domainMax(dst.KeyBindingAction); m > 0 && kb.ActionInt >= m {
		return false, fmt.Sprintf("键位 ActionInt=%d 超出目标端 %s 枚举域 [0,%d)（D20 越域跳过）",
			kb.ActionInt, targetFork, m)
	}
	return true, ""
}

// adaptModPresetModsForTarget D7：ModPreset 的 ModsJson（APIMod[] JSON）
// 过目标端 mod 白名录，名录外（或解析失败）整条拒绝——白名单制宁拒勿杀
// （§5.4 任意端行，与成绩 mods 同一规则面）。targetFork 未注册时保守拒绝。
// 空串（无 mod 的预设）放行。
func adaptModPresetModsForTarget(mp hub.ModPreset, targetFork string) (rejectReason string) {
	p, ok := forks.Profile(targetFork)
	if !ok {
		return fmt.Sprintf("目标 fork %q 未注册，无法判定模组预设兼容性（跳过）", targetFork)
	}
	if mp.ModsJson == "" {
		return ""
	}
	allowed := make(map[string]bool, len(p.ModAcronyms))
	for _, a := range p.ModAcronyms {
		allowed[a] = true
	}
	var mods []apiMod
	if err := json.Unmarshal([]byte(mp.ModsJson), &mods); err != nil {
		return fmt.Sprintf("模组预设 %q 的 ModsJson 无法解析（整条跳过，D7）: %v", mp.Name, err)
	}
	for _, m := range mods {
		if !allowed[m.Acronym] {
			return fmt.Sprintf("模组预设 %q 含目标端不认识的 mod %q（白名单制宁拒勿杀，整条跳过，D7）",
				mp.Name, m.Acronym)
		}
	}
	return ""
}

// skipReasonStatsKeyDropped 是键级过滤（整条仍落地）的计数原因——被
// 过滤的每个未知 HitResult 键计 1 次（§1.5-4 不静默吞）。
const skipReasonStatsKeyDropped = "成绩 Statistics 含目标端不认识的 HitResult 键（过滤后写入，D7）"

// adaptScoreForTarget 按目标 fork 的 Profile 白名单适配成绩 JSON：
// 返回的 hub.Score 已把 Statistics/MaximumStatistics 替换为键过滤后的
// 重序列化结果（键排序、确定性输出，与写面对偶）；rejectReason 非空
// = 整条拒绝（跳过计数的人话原因）。targetFork 未注册时保守拒绝。
func adaptScoreForTarget(sc hub.Score, targetFork string) (adapted hub.Score, droppedKeys int, rejectReason string) {
	p, ok := forks.Profile(targetFork)
	if !ok {
		return sc, 0, fmt.Sprintf("目标 fork %q 未注册，无法判定成绩兼容性（跳过）", targetFork)
	}
	allowedMods := make(map[string]bool, len(p.ModAcronyms))
	for _, a := range p.ModAcronyms {
		allowedMods[a] = true
	}
	if sc.ModsJson != "" {
		var mods []apiMod
		if err := json.Unmarshal([]byte(sc.ModsJson), &mods); err != nil {
			return sc, 0, fmt.Sprintf("成绩 %s 的 ModsJson 无法解析（整条跳过，D7）: %v",
				hashPrefix(sc.Hash), err)
		}
		for _, m := range mods {
			if !allowedMods[m.Acronym] {
				return sc, 0, fmt.Sprintf("成绩 %s 含目标端不认识的 mod %q（白名单制宁拒勿杀，整条跳过，D7）",
					hashPrefix(sc.Hash), m.Acronym)
			}
		}
	}
	allowedKeys := make(map[string]bool, len(p.StatisticsKeys))
	for _, k := range p.StatisticsKeys {
		allowedKeys[k] = true
	}
	out := sc
	filter := func(field, in string) (string, int, string) {
		if in == "" {
			return "", 0, ""
		}
		var stats map[string]int
		if err := json.Unmarshal([]byte(in), &stats); err != nil {
			return "", 0, fmt.Sprintf("成绩 %s 的 %s 无法解析（整条跳过，D7）: %v",
				hashPrefix(sc.Hash), field, err)
		}
		kept := make(map[string]int, len(stats))
		dropped := 0
		for k, v := range stats {
			if allowedKeys[k] {
				kept[k] = v
			} else {
				dropped++
			}
		}
		if dropped == 0 {
			return in, 0, "" // 全键已知：原文原样（过滤是恒等变换，不重排）
		}
		// 有键被滤：重序列化过滤后的键集（map 按键排序，输出确定性——
		// 重续判定 matchesCanonical 比较稳定）。
		b, err := json.Marshal(kept)
		if err != nil {
			return "", 0, fmt.Sprintf("成绩 %s 的 %s 重序列化失败（整条跳过）: %v",
				hashPrefix(sc.Hash), field, err)
		}
		return string(b), dropped, ""
	}
	stats, dropped, reject := filter("StatisticsJson", sc.StatisticsJson)
	if reject != "" {
		return sc, 0, reject
	}
	out.StatisticsJson = stats
	maxStats, droppedMax, reject := filter("MaximumStatisticsJson", sc.MaximumStatisticsJson)
	if reject != "" {
		return sc, 0, reject
	}
	out.MaximumStatisticsJson = maxStats
	return out, dropped + droppedMax, ""
}

// ---- 全局交集（§0-B3/G6） ----
//
// 一轮同步的写入面必须满足**全部**目标端（含运行中被跳过的端）的静态
// 兼容性：任一端拒绝，所有端都不写。以下三个 peer 版判定与单端版同构：
// 目标端自身先判（其拒绝保持原有原因文本，不加前缀）；其余 peer 逐端
// 判定，拒绝时原因加 IntersectionRejectPrefix 前缀并点名拒绝端。

// adaptScoreForIntersection 成绩的交集适配：先按目标端适配，再逐 peer
// 适配（键过滤在已过滤形态上累积 = 各端白名单的键级交集，所有目标写同一
// 形态）；任一端拒绝（mod 名录外 / JSON 解析失败）→ 整条跳过。
func adaptScoreForIntersection(sc hub.Score, targetFork string, peers []string) (adapted hub.Score, droppedKeys int, rejectReason string) {
	out, dropped, reject := adaptScoreForTarget(sc, targetFork)
	if reject != "" {
		return sc, 0, reject
	}
	total := dropped
	for _, f := range peers {
		if f == targetFork {
			continue
		}
		a, d, r := adaptScoreForTarget(out, f)
		if r != "" {
			return sc, 0, IntersectionRejectPrefix + f + " 不支持 — " + r
		}
		out = a
		total += d
	}
	return out, total, ""
}

// adaptModPresetForIntersection 模组预设的交集判定：任一端 mod 名录外 →
// 整条拒绝（peer 拒绝加前缀）。
func adaptModPresetForIntersection(mp hub.ModPreset, targetFork string, peers []string) (rejectReason string) {
	if r := adaptModPresetModsForTarget(mp, targetFork); r != "" {
		return r
	}
	for _, f := range peers {
		if f == targetFork {
			continue
		}
		if r := adaptModPresetModsForTarget(mp, f); r != "" {
			return IntersectionRejectPrefix + f + " 不支持 — " + r
		}
	}
	return ""
}

// keyBindingActionAllowedIntersection 键位的交集判定：源端×目标端枚举域
// 交集之外，再逐 peer 检查其枚举域（D20 域数据的交集面）；任一端越域 →
// 跳过（peer 拒绝加前缀）。
func keyBindingActionAllowedIntersection(originFork, targetFork string, peers []string, kb hub.KeyBinding) (bool, string) {
	if ok, reject := keyBindingActionAllowed(originFork, targetFork, kb); !ok {
		return false, reject
	}
	for _, f := range peers {
		if f == targetFork {
			continue
		}
		if ok, reject := keyBindingActionAllowed(originFork, f, kb); !ok {
			return false, IntersectionRejectPrefix + f + " 不支持 — " + reject
		}
	}
	return true, ""
}

// ---- 裁决期交集预检（§0-B3/G7：appsvc 三方判定在计划装配前按单位过滤） ----
//
// 以下导出判定与执行期交集函数同构，但无「目标端自身」语境（判定层在
// 单位级一次判全部 peers——任一 peer 拒绝即该单位不进任何端的计划，
// 原因统一带 IntersectionRejectPrefix 以便计入 Pending.Unsupported）。

// ScoreIntersectionReject 成绩对全部目标端的兼容性预检：规则集可解析性
//（G11 后专有规则集成绩也进 hub，靠此拦截出口）+ 逐 peer 跑
// adaptScoreForTarget 的拒绝面（mod 名录外 / StatisticsJson 解析失败），
// 首个拒绝返回带前缀原因；全过返回空串。
func ScoreIntersectionReject(sc hub.Score, peers []string) string {
	if r := RulesetIntersectionReject(sc.RulesetShortName, peers); r != "" {
		return r
	}
	for _, f := range peers {
		if _, _, r := adaptScoreForTarget(sc, f); r != "" {
			return IntersectionRejectPrefix + f + " 不支持 — " + r
		}
	}
	return ""
}

// SkinIntersectionReject 皮肤对全部目标端的兼容性预检（§0.2-B1 收全的
// 出口对偶）：Protected 内置皮肤与命中任一已注册 fork 专有指纹的皮肤
// 永不导出（它们是宿主 fork 的固定自备物，跨端投放无收益且会污染目标
// 皮肤库）。peers 形参为同构签名留位——规则本身与端无关。
func SkinIntersectionReject(sk hub.Skin, peers []string) string {
	if sk.Protected {
		return fmt.Sprintf("皮肤 %q 是 Protected 内置皮肤（固定 Guid 四端自备，不投放）", sk.Name)
	}
	for _, p := range forks.All() {
		for _, f := range p.OwnSkins {
			if f.Guid != "" && sk.ID == f.Guid {
				return fmt.Sprintf("皮肤 %q 命中 fork %s 专有指纹（Guid 等值），属宿主自备物不投放", sk.Name, p.ID)
			}
			if f.Name != "" && f.Creator != "" && f.Name == sk.Name && f.Creator == sk.Creator {
				return fmt.Sprintf("皮肤 %q 命中 fork %s 专有指纹（Name+Creator），属宿主自备物不投放", sk.Name, p.ID)
			}
		}
	}
	return ""
}

// ModPresetIntersectionReject 模组预设对全部目标端的兼容性预检
// （adaptModPresetModsForTarget 的 peer 版）。
func ModPresetIntersectionReject(mp hub.ModPreset, peers []string) string {
	for _, f := range peers {
		if r := adaptModPresetModsForTarget(mp, f); r != "" {
			return IntersectionRejectPrefix + f + " 不支持 — " + r
		}
	}
	return ""
}

// KeyBindingIntersectionReject 键位对全部目标端的枚举域预检（D20）：
// originFork 是裁决决定值的来源端；ActionInt 为负恒拒（坏数据不加前缀）。
func KeyBindingIntersectionReject(originFork string, peers []string, kb hub.KeyBinding) string {
	if kb.ActionInt < 0 {
		return fmt.Sprintf("键位 ActionInt=%d 为负（枚举值域非负，坏数据）", kb.ActionInt)
	}
	for _, f := range peers {
		if ok, r := keyBindingActionAllowed(originFork, f, kb); !ok {
			return IntersectionRejectPrefix + f + " 不支持 — " + r
		}
	}
	return ""
}

// RulesetIntersectionReject 规则集名对全部目标端的可解析性预检（§0-B3
// 全局交集在「规则集行」上的静态近似）：短名须在每端 official ∪
// OwnRulesets 内，或该端有市场规则集面（MarketRulesetMinOnlineID>0，
// 动态名录静态不可枚举——放行，真缺时执行期失败如实上报）；任一端
// 不可解析 → 带前缀原因。
func RulesetIntersectionReject(rulesetName string, peers []string) string {
	if _, ok := officialRulesets[rulesetName]; ok {
		return "" // 官方四集全部端可解析
	}
	for _, f := range peers {
		p, ok := forks.Profile(f)
		if !ok {
			return IntersectionRejectPrefix + f + " 不支持 — 该 fork 未注册，规则集可解析性不可判定"
		}
		if p.MarketRulesetMinOnlineID > 0 {
			continue // 市场规则集名录动态——静态放行，执行期兜底
		}
		own := false
		for _, r := range p.OwnRulesets {
			if r == rulesetName {
				own = true
				break
			}
		}
		if !own {
			return IntersectionRejectPrefix + f + " 不支持 — 规则集 " + rulesetName + " 在该端不可解析"
		}
	}
	return ""
}
