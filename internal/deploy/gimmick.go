package deploy

// delta gimmick 谱面检测告警（design.md §5.4 R6 / fork-delta §5.2/§7.2）：
// delta 端编辑保存的 .osu 可能携带 [BeatmapSectionGimmicks] /
// [BeatmapHitObjectGimmicks] 两个 section（数据全在 .osu 文件内、不进库
// 表，fork-delta §5.1）。官方端读入容忍（只记日志跳过），但官方编辑器
// 重新保存会静默丢弃这两节——谱面集流出 delta 生态的计划必须逐集告警；
// 绝不改写 .osu 内容（改写即破坏内容寻址与成绩锚点，§1 硬约束表）。
//
// 检测 = 纯头扫描（.osu 是纯文本，逐行精确匹配 section 头），只读仓内
// 副本（filestore 两级哈希路径直寻）；命中即标 PlanItem.Warning，默认
// 动作不变（官方端只读不编辑则安全，§5.4），双向回传链路（delta→官方
// →编辑→回 delta）由告警阻断。

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"lzsync/internal/filestore"
	"lzsync/internal/hub"
)

// gimmickSectionHeaders delta 端编辑保存产生的两个 .osu section 头
// （fork-delta §5.1）。adapter 规则数据（§3 adapter 模块在本包的过渡
// 承载位，externalSetHash 的同构）；forks 增补 ForkProfile 数据面后
// 应切换。
var gimmickSectionHeaders = []string{
	"[BeatmapSectionGimmicks]",
	"[BeatmapHitObjectGimmicks]",
}

// gimmickWarningText 命中谱面集的告警文案（预演、执行报告与 UI 一致
// 呈现）。
const gimmickWarningText = "此谱面含 delta gimmick section，经非 delta 端编辑保存会静默丢失 gimmick 数据（R6）；默认仍可同步——官方端只读不编辑则安全，双向回传链路（delta→官方→编辑→回 delta）由本告警阻断"

// markGimmickWarnings 对计划内谱面集动作项做 gimmick 头扫描（BuildPlan
// 期预演，§6.2 adapter 规则在预览期即跑）：hub 读集 payload 取 .osu
// 文件哈希 → filestore 仓内副本头扫描 → 命中项填 Warning。skip 项不
// 投放不检测；hub 无该集行时如实放行（执行期现场按「计划与真相源
// 不符」计数）。仓内缺 .osu 副本时跳过该文件检测——无法检测 ≠ 无
// gimmick，不产生告警也不阻断预演；缺文件的集在投放期 CopyMissing
// 自然失败并给全链错误，此处不重复设卡。
func markGimmickWarnings(ctx context.Context, h *hub.Store, files *filestore.Store, items []PlanItem) error {
	setIdx := map[string][]*PlanItem{}
	for i := range items {
		it := &items[i]
		if it.Kind == hub.KindBeatmapSet && it.Action != ActionSkip {
			setIdx[it.ContentKey] = append(setIdx[it.ContentKey], it)
		}
	}
	if len(setIdx) == 0 {
		return nil
	}
	// 集键 → .osu 文件哈希（payload 只读直查，loadBeatmapSet 的同款
	// 过渡承载；version-replace 项的 ContentKey 已是 hub 链尾新版本键）。
	osuHashes := map[string][]string{}
	for key := range setIdx {
		var payload string
		err := h.DB().QueryRowContext(ctx,
			`SELECT payload FROM beatmap_set WHERE content_hash=? AND superseded_by IS NULL`, key).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("deploy: 读谱面集 %s payload（gimmick 检测）: %w", key, err)
		}
		var bs hub.BeatmapSet
		if err := json.Unmarshal([]byte(payload), &bs); err != nil {
			return fmt.Errorf("deploy: 解码谱面集 %s payload（gimmick 检测）: %w", key, err)
		}
		for _, f := range bs.Files {
			if strings.HasSuffix(strings.ToLower(f.Filename), ".osu") {
				osuHashes[key] = append(osuHashes[key], f.Hash)
			}
		}
	}
	hit := map[string]bool{}
	scanned := map[string]bool{} // 跨集共享的 .osu 副本只扫一次
	for key, hashes := range osuHashes {
		for _, hash := range hashes {
			if scanned[hash] {
				continue
			}
			scanned[hash] = true
			ok, err := osuHasGimmickSection(files.Path(hash))
			if err != nil {
				continue // 仓内缺副本：能力边界，见函数注
			}
			if ok {
				hit[key] = true
			}
		}
	}
	for key, its := range setIdx {
		if !hit[key] {
			continue
		}
		for _, it := range its {
			it.Warning = gimmickWarningText
		}
	}
	return nil
}

// osuHasGimmickSection 流式逐行扫描 .osu 纯文本的 section 头（只读，
// 绝不改写文件内容）。path 为空（非规范哈希）或仓内不存在副本时返回
// 错误，由调用方按能力边界处理。
func osuHasGimmickSection(path string) (bool, error) {
	if path == "" {
		return false, errors.New("deploy: 非法 .osu 哈希（filestore.Path 为空）")
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		for _, header := range gimmickSectionHeaders {
			if line == header {
				return true, nil
			}
		}
	}
	return false, sc.Err()
}

// appliedGimmickWarnings 从计划项里筛出已提交的告警项（R6：执行报告
// 与预演一致呈现——applied 命中才计数，未执行/失败批的项不虚报）。
func appliedGimmickWarnings(plan Plan, applied []ItemResult) []PlanItem {
	warned := map[string]PlanItem{}
	for _, it := range plan.Items {
		if it.Warning != "" {
			warned[string(it.Kind)+"\x00"+it.ContentKey] = it
		}
	}
	if len(warned) == 0 {
		return nil
	}
	var out []PlanItem
	for _, ap := range applied {
		if it, ok := warned[string(ap.Kind)+"\x00"+ap.ContentKey]; ok {
			out = append(out, it)
		}
	}
	return out
}
