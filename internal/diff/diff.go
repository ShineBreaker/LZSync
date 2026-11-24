// Package diff 是 hub 内对账查询（design.md §3.1/§4.4）：全部对比都是对
// hub.db 的 SQL 查询，绝不触碰任何 fork 库（§6.2）。产出四分类 + 冲突摘要
// + tombstone 恢复组；跨版本对账的谱面身份键配对缓存（beatmap_identity）
// 由 hub 维护，本包只消费其查询结果。
package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"lzsync/internal/hub"
)

// Category 对账四分类（§4.2：在 EZRealmSync 三分类上加第四类）。
type Category string

const (
	CategorySourceOnly    Category = "source-only"    // 源独有
	CategoryTargetOnly    Category = "target-only"    // 目标独有
	CategoryConflict      Category = "conflict"       // 内容键相等、非键字段不同
	CategoryVersionUpdate Category = "version-update" // 内容键不等、谱面身份键相等
)

// FieldDiff 冲突字段级对比。冲突字段只取内容键之外的字段（内容键本身
// 键匹配时必相等，比它是死代码，§4.2）。
type FieldDiff struct {
	Field     string
	HubValue  string // canonical 值（最近一次收集确立，origin_fork 指向它）
	ForkValue string // 目标 fork presence 值
}

// Evidence 谱面身份键证据强度（§4.2 第二层）。
type Evidence string

const (
	EvidenceStrong Evidence = "strong" // OnlineID 强键（同服务器）→ 默认按版本更新执行
	EvidenceWeak   Evidence = "weak"   // 元数据弱键「疑似」→ 需用户确认组，默认不勾选
)

// Entry 单条对账结果。
type Entry struct {
	Kind           hub.EntityKind
	ContentKey     string
	Category       Category
	Summary        string      // 人类可读摘要（曲名/难度名/日期/分数）
	ConflictFields []FieldDiff // Category == CategoryConflict 时填充
	IdentityKey    string      // 版本更新时的谱面身份键（online:/meta: 前缀）
	Evidence       Evidence    // 版本更新时的证据强度
}

// CompareForks fork↔fork 快照对比（§4.4）：两份 presence 的差集，谱面集按
// 身份键归并、取各自 presence 中实际存在的版本；字段取实体表规范值。
func CompareForks(ctx context.Context, h *hub.Store, source, target string, kinds []hub.EntityKind) ([]Entry, error) {
	if h == nil || source == "" || target == "" || source == target {
		return nil, fmt.Errorf("diff: source 和 target 必须是不同的已提供端")
	}
	sourcePresence, err := h.Presence(ctx, source, kinds)
	if err != nil {
		return nil, err
	}
	targetPresence, err := h.Presence(ctx, target, kinds)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, kind := range kinds {
		sourceOnly, targetOnly := difference(sourcePresence[kind], targetPresence[kind]), difference(targetPresence[kind], sourcePresence[kind])
		consumed := make(map[string]bool)
		for _, key := range sourceOnly {
			entry := Entry{Kind: kind, ContentKey: key, Category: CategorySourceOnly}
			if kind == hub.KindBeatmapSet {
				if identity, evidence, matched := matchBeatmapIdentity(sourcePresence[kind][key], targetPresence[kind], targetOnly, consumed); matched {
					entry.Category = CategoryVersionUpdate
					entry.IdentityKey, entry.Evidence = identity, evidence
				}
			}
			out = append(out, entry)
		}
		for _, key := range targetOnly {
			if !consumed[key] {
				out = append(out, Entry{Kind: kind, ContentKey: key, Category: CategoryTargetOnly})
			}
		}
		for key, sourceValue := range sourcePresence[kind] {
			if targetValue, ok := targetPresence[kind][key]; ok && !sameJSON(sourceValue, targetValue) {
				out = append(out, Entry{Kind: kind, ContentKey: key, Category: CategoryConflict, ConflictFields: []FieldDiff{{Field: "payload", HubValue: sourceValue, ForkValue: targetValue}}})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ContentKey < out[j].ContentKey
	})
	return out, nil
}

func difference(left, right map[string]string) []string {
	keys := make([]string, 0)
	for key := range left {
		if _, ok := right[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func matchBeatmapIdentity(sourceJSON string, target map[string]string, targetKeys []string, consumed map[string]bool) (string, Evidence, bool) {
	var sourceSet hub.BeatmapSet
	if json.Unmarshal([]byte(sourceJSON), &sourceSet) != nil {
		return "", "", false
	}
	sourceIdentity, sourceStrong := hub.BeatmapSetIdentityKey(&sourceSet)
	for _, key := range targetKeys {
		if consumed[key] {
			continue
		}
		var targetSet hub.BeatmapSet
		if json.Unmarshal([]byte(target[key]), &targetSet) != nil {
			continue
		}
		targetIdentity, targetStrong := hub.BeatmapSetIdentityKey(&targetSet)
		if sourceIdentity == targetIdentity {
			consumed[key] = true
			if sourceStrong && targetStrong {
				return sourceIdentity, EvidenceStrong, true
			}
			return sourceIdentity, EvidenceWeak, true
		}
	}
	return "", "", false
}

// DiffHubAgainst hub 真相 → 单 fork 的投放预演差异（§4.4 对账语义）：
// 当前版本集合（superseded_by IS NULL）vs 目标 presence 差集；历史版本
// （superseded 行）已排除、永不进入计划。目标 presence 中的版本 ≠ hub
// 当前版本 → 版本更新分类。
func DiffHubAgainst(ctx context.Context, h *hub.Store, targetFork string, kinds []hub.EntityKind) ([]Entry, error) {
	items, err := h.ReconcileAgainst(ctx, targetFork, kinds)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		category := CategorySourceOnly
		switch item.Category {
		case hub.ReconcileConflict:
			category = CategoryConflict
		case hub.ReconcileVersionUpdate:
			category = CategoryVersionUpdate
		}
		fields := make([]FieldDiff, 0, len(item.ConflictFields))
		for _, field := range item.ConflictFields {
			fields = append(fields, FieldDiff{Field: field.Field, HubValue: field.HubValue, ForkValue: field.ForkValue})
		}
		out = append(out, Entry{Kind: item.Kind, ContentKey: item.ContentKey, Category: category, ConflictFields: fields, IdentityKey: item.IdentityKey, Evidence: Evidence(item.Evidence)})
	}
	return out, nil
}

// TombstoneHits 目标 fork 的 tombstone 命中项（HB2「恢复（已删除）」组，
// §6.2）：不进默认计划、默认不勾选、展示删除时间；用户显式勾选投放时
// 同时清除该 tombstone（§5.4 删除不复活）。
func TombstoneHits(ctx context.Context, h *hub.Store, targetFork string, kinds []hub.EntityKind) ([]Entry, error) {
	tombstones, err := h.Tombstones(ctx, targetFork)
	if err != nil {
		return nil, err
	}
	allowed := make(map[hub.EntityKind]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	var out []Entry
	for kind, values := range tombstones {
		if !allowed[kind] {
			continue
		}
		for key := range values {
			out = append(out, Entry{Kind: kind, ContentKey: key, Category: CategoryTargetOnly})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ContentKey < out[j].ContentKey
	})
	return out, nil
}

func unionKeys(left, right map[string]string) []string {
	seen := make(map[string]bool, len(left)+len(right))
	for key := range left {
		seen[key] = true
	}
	for key := range right {
		seen[key] = true
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sameJSON(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return left == right
	}
	return reflect.DeepEqual(a, b)
}
