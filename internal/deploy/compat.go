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
