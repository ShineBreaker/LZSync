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
