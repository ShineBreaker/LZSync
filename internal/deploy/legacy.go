// legacy.go 是 stable 端（StorageLegacy）的投放实现：Deploy 按 Profile.
// Storage == forks.StorageLegacy 分派到本文件（gameini.go 的「文件级原子
// 写独立编排」先例——stable 无 realm 事务，约束平移：S2 = collection.db
// 写前备份；S9 = 临时产物→回读自检→rename 原子替换；S6 对偶 = 链接先行
//（Songs/ 文件先落，osu!.db 行由游戏启动导入时自建——LZSync 永不写
// osu!.db，stable-legacy §5）。占用防线：进程守卫（osu!.exe）+ S9 原子
// 替换（即使竞态旧文件也一字不损）。
package deploy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"lzsync/internal/hub"
	"lzsync/internal/legacydb"
)

// deployLegacy 是 stable 端投放编排：谱面集 = Songs/ 下链接目录落地
//（EnsureLinked，字节在仓、游戏侧链接持有——与收集回链同一形态）；
// 收藏 = collection.db 整文件合并重写。skin 面随 P4 接入；其余 kind 计数
// 跳过（stable 不同步成绩/键位/设置，§0 板块交集自然排除）。
func (r *Runner) deployLegacy(ctx context.Context, plan Plan, prog ProgressFunc) (Report, error) {
	report := Report{}
	target := plan.Target
	if target.DataDir == "" {
		return report, fmt.Errorf("deploy: 计划缺 stable 目录引用（plan.Target.DataDir 为空）")
	}
	if plan.TargetFork != target.ForkID {
		return report, fmt.Errorf("deploy: 计划目标 fork %q 与目标端 %q 不符", plan.TargetFork, target.ForkID)
	}
	if prog != nil {
		prog(StageGuard, 0, 1, 0, 1)
	}
	if r.deps.Guard != nil {
		if err := r.deps.Guard.EnsureClearFork(plan.TargetFork); err != nil {
			return report, err
		}
	}

	var setItems, collectionItems, skinItems []PlanItem
	skips := map[string]int{}
	for _, it := range plan.Items {
		if it.Action == ActionSkip {
			skips[it.SkipReason]++
			continue
		}
		switch it.Kind {
		case hub.KindBeatmapSet:
			setItems = append(setItems, it)
		case hub.KindCollection:
			collectionItems = append(collectionItems, it)
		case hub.KindSkin:
			skinItems = append(skinItems, it)
		default:
			skips["stable 端不支持该板块（成绩/键位/设置不适用）"]++
		}
	}

	// 收藏面：一次读改写（多个收藏项合并为单次原子替换）。
	if len(collectionItems) > 0 {
		if prog != nil {
			prog(StageWrite, 0, 1, 0, len(collectionItems))
		}
		applied, colSkips, backupPath, cerr := r.deployLegacyCollections(ctx, target.DataDir, collectionItems)
		if cerr != nil {
			report.FailedBatches = append(report.FailedBatches, BatchFailure{
				Reason: cerr.Error(), Items: collectionItems,
			})
			report.NotRun = append(report.NotRun, setItems...)
			report.Skipped = sortedSkips(skips)
			return report, cerr
		}
		report.Applied = append(report.Applied, applied...)
		for reason, n := range colSkips {
			skips[reason] += n
		}
		report.BackupPath = backupPath
	}

	// 谱面集面：逐集落地（端间独立，单集失败不中止后续；失败集计入
	// FailedBatches 供重续）。皮肤面同款逐项落地（Skins/ 链接目录）。
	for i, item := range setItems {
		if err := ctx.Err(); err != nil {
			report.NotRun = append(report.NotRun, setItems[i:]...)
			report.Skipped = sortedSkips(skips)
			return report, err
		}
		if prog != nil {
			prog(StageLink, 0, len(setItems), i, len(setItems))
		}
		res, skipReason, err := r.deployLegacySet(ctx, target.DataDir, item)
		if err != nil {
			report.FailedBatches = append(report.FailedBatches, BatchFailure{
				Reason: err.Error(), Items: []PlanItem{item},
			})
			report.NotRun = append(report.NotRun, setItems[i+1:]...)
			report.Skipped = sortedSkips(skips)
			return report, err
		}
		if skipReason != "" {
			skips[skipReason]++
			continue
		}
		report.Applied = append(report.Applied, res)
	}
	for i, item := range skinItems {
		if err := ctx.Err(); err != nil {
			report.NotRun = append(report.NotRun, skinItems[i:]...)
			report.Skipped = sortedSkips(skips)
			return report, err
		}
		res, skipReason, err := r.deployLegacySkin(ctx, target.DataDir, item)
		if err != nil {
			report.FailedBatches = append(report.FailedBatches, BatchFailure{
				Reason: err.Error(), Items: []PlanItem{item},
			})
			report.NotRun = append(report.NotRun, skinItems[i+1:]...)
			report.Skipped = sortedSkips(skips)
			return report, err
		}
		if skipReason != "" {
			skips[skipReason]++
			continue
		}
		report.Applied = append(report.Applied, res)
	}
	report.Skipped = sortedSkips(skips)

	// 恢复项投放成功 → 清 tombstone（与 realm 端同收尾，写 hub 与端无关）。
	if err := r.clearRestoredTombstones(ctx, plan); err != nil {
		report.Skipped = append(report.Skipped, hub.SkipStat{
			Reason: "投放成功但清除恢复项 tombstone 失败（下次收集会自动清除）: " + err.Error(),
			Count:  1,
		})
	}
	return report, nil
}

// deployLegacySet 落地单个谱面集：现场复核（同集已在 / 版本分歧 / 仓缺
// 字节三类跳过）→ 构造 Songs/<setID> <artist> - <title>/ 目录 → 逐
// Files EnsureLinked（链接先行，S6 对偶）。
func (r *Runner) deployLegacySet(ctx context.Context, dataDir string, item PlanItem) (ItemResult, string, error) {
	bs, err := r.loadLegacySetPayload(ctx, item)
	if err != nil {
		return ItemResult{}, "", err
	}
	res := ItemResult{Kind: hub.KindBeatmapSet, ContentKey: item.ContentKey, Action: item.Action}

	// 现场复核一：目录名冲突与内容比对（难度集合兜底——lazer 集哈希是
	// 导入缓存、可能 stale，stable-legacy §4.1）。
	dir := filepath.Join(dataDir, "Songs", legacySetDirName(bs))
	if existing, ok := legacySetDifficultyHashes(dir); ok {
		planned := map[string]bool{}
		for _, b := range bs.Beatmaps {
			planned[b.Hash] = true
		}
		same := len(existing) == len(planned)
		if same {
			for h := range existing {
				if !planned[h] {
					same = false
					break
				}
			}
		}
		if same {
			return res, "目标端已持有同内容谱面集（难度 Hash 集合一致，跳过）", nil
		}
		return res, "目标端已持有该谱面的不同版本（替换走确认流程，不自动写入）", nil
	}
	// 目录名前缀冲突检查：同 setID 已有别的目录名（如游戏导入时改过名）。
	if bs.OnlineID > 0 {
		if _, found := legacyFindSetDir(dataDir, bs.OnlineID); found {
			return res, "目标端已有同 setID 目录但目录名不同（需先收集对齐，不自动写入）", nil
		}
	}

	// 现场复核二：仓内字节完备（来源端收集不完整时绝不落半套目录）。
	if r.deps.Files == nil {
		return res, "", fmt.Errorf("deploy: 谱面集投放需 Deps.Files（链接引擎）")
	}
	for _, f := range bs.Files {
		if has, herr := r.deps.Files.Store.Has(f.Hash); herr != nil || !has {
			return res, fmt.Sprintf("仓内缺字节（来源收集不完整）：%s", f.Filename), nil
		}
	}

	// 落地：目录 + 逐文件链接（链接先行——文件全部可见后游戏导入才有
	// 完整输入；osu!.db 行由游戏自建）。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, "", fmt.Errorf("deploy: 建集目录 %s: %w", dir, err)
	}
	for _, f := range bs.Files {
		if _, err := r.deps.Files.EnsureLinked(f.Hash, filepath.Join(dir, f.Filename)); err != nil {
			return res, "", fmt.Errorf("deploy: 建链 %s: %w", f.Filename, err)
		}
	}
	// 验证：全部文件在位（.osu 至少一个）。
	osuCount := 0
	for _, f := range bs.Files {
		if _, err := os.Lstat(filepath.Join(dir, f.Filename)); err != nil {
			return res, "", fmt.Errorf("deploy: 落地验证失败 %s: %w", f.Filename, err)
		}
		if strings.HasSuffix(strings.ToLower(f.Filename), ".osu") {
			osuCount++
		}
	}
	if osuCount == 0 {
		return res, "", fmt.Errorf("deploy: 集载荷无 .osu 文件（数据异常）：%s", item.ContentKey)
	}
	return res, "", nil
}

// deployLegacySkin 落地单个皮肤：Skins/<Name>/ 链接目录（同 deployLegacySet
// 模式——现场复核同名目录已在 / 仓缺字节两路跳过，逐文件 EnsureLinked）。
// stable 皮肤内容键（目录规范化哈希）与 lazer 不同构，跨端配对靠 Name
// 弱键：同名目录存在即视为已持有（不比较内容——用户本地改过的皮肤
// 绝不覆盖）。
func (r *Runner) deployLegacySkin(ctx context.Context, dataDir string, item PlanItem) (ItemResult, string, error) {
	skin, err := r.loadLegacySkinPayload(ctx, item)
	if err != nil {
		return ItemResult{}, "", err
	}
	res := ItemResult{Kind: hub.KindSkin, ContentKey: item.ContentKey, Action: item.Action}
	dir := filepath.Join(dataDir, "Skins", sanitizeDirComponent(skin.Name))
	if _, serr := os.Stat(dir); serr == nil {
		return res, "目标端已有同名皮肤目录（Name 弱键配对，不覆盖）", nil
	}
	if r.deps.Files == nil {
		return res, "", fmt.Errorf("deploy: 皮肤投放需 Deps.Files（链接引擎）")
	}
	for _, f := range skin.Files {
		if has, herr := r.deps.Files.Store.Has(f.Hash); herr != nil || !has {
			return res, fmt.Sprintf("仓内缺字节（来源收集不完整）：%s", f.Filename), nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, "", fmt.Errorf("deploy: 建皮肤目录 %s: %w", dir, err)
	}
	for _, f := range skin.Files {
		target := filepath.Join(dir, f.Filename)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return res, "", fmt.Errorf("deploy: 建皮肤子目录 %s: %w", f.Filename, err)
		}
		if _, lerr := r.deps.Files.EnsureLinked(f.Hash, target); lerr != nil {
			return res, "", fmt.Errorf("deploy: 建链 %s: %w", f.Filename, lerr)
		}
	}
	return res, "", nil
}

// loadLegacySkinPayload 取皮肤决定值：PayloadJSON 优先，回退 hub canonical。
func (r *Runner) loadLegacySkinPayload(ctx context.Context, item PlanItem) (hub.Skin, error) {
	var skin hub.Skin
	if item.PayloadJSON != "" {
		if err := json.Unmarshal([]byte(item.PayloadJSON), &skin); err != nil {
			return skin, fmt.Errorf("皮肤项 %s 决定值解码: %w", item.ContentKey, err)
		}
		return skin, nil
	}
	var payload string
	err := r.deps.Hub.DB().QueryRowContext(ctx,
		`SELECT payload FROM skin WHERE content_hash=?`, item.ContentKey).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return skin, fmt.Errorf("deploy: hub 缺皮肤行 %s（并发清除？）", item.ContentKey)
	}
	if err != nil {
		return skin, err
	}
	if err := json.Unmarshal([]byte(payload), &skin); err != nil {
		return skin, fmt.Errorf("deploy: hub 行 %s 解码: %w", item.ContentKey, err)
	}
	return skin, nil
}

// deployLegacyCollections 合并重写 collection.db（S9）：读现有 → 逐项
// 替换/追加同名收藏（成员 = 三方判定决定值；成员已一致 → 重续跳过）→
// 备份原文件 → 临时写 → 回读自检 → rename 原子替换。返回
// (applied, skips, 备份路径)；无旧文件可备时备份路径为空。
func (r *Runner) deployLegacyCollections(ctx context.Context, dataDir string, items []PlanItem) ([]ItemResult, map[string]int, string, error) {
	var applied []ItemResult
	skips := map[string]int{}
	dbPath := filepath.Join(dataDir, "collection.db")
	existing := &legacydb.CollectionDB{}
	if data, err := os.ReadFile(dbPath); err == nil {
		if parsed, perr := legacydb.ParseCollectionDB(data); perr == nil {
			existing = parsed
		} else {
			return nil, nil, "", fmt.Errorf("deploy: 现有 collection.db 解析失败（拒绝在其上重写）: %w", perr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, "", fmt.Errorf("deploy: 读 collection.db: %w", err)
	}

	// 原文件备份（存在才需要；首次投放 = 新建，无旧可备）。
	backupPath := ""
	if _, err := os.Stat(dbPath); err == nil {
		var berr error
		backupPath, berr = backupTarget(r.deps.Backups, dbPath)
		if berr != nil {
			return nil, nil, "", berr // S2：备份不可绕过
		}
	}

	changed := false
	for _, item := range items {
		var c hub.Collection
		if item.PayloadJSON != "" {
			if err := json.Unmarshal([]byte(item.PayloadJSON), &c); err != nil {
				return nil, nil, "", fmt.Errorf("收藏项 %s 决定值解码: %w", item.ContentKey, err)
			}
		} else {
			loaded, _, ok, lerr := r.deps.Hub.LoadCollection(ctx, item.ContentKey)
			if lerr != nil {
				return nil, nil, "", lerr
			}
			if !ok {
				continue // hub 缺行（并发清除），跳过不落地
			}
			c = loaded
		}
		replaced := false
		for i := range existing.Collections {
			if existing.Collections[i].Name == c.Name {
				if legacySameMembers(existing.Collections[i].BeatmapMD5s, c.BeatmapMD5Hashes) {
					skips["重续已一致（stable 收藏成员与决定值相同）"]++
					replaced = true // 标记已处理（跳过）
					break
				}
				existing.Collections[i].BeatmapMD5s = c.BeatmapMD5Hashes
				replaced = true
				changed = true
				break
			}
		}
		if replaced {
			continue
		}
		existing.Collections = append(existing.Collections, legacydb.Collection{
			Name:        c.Name,
			BeatmapMD5s: c.BeatmapMD5Hashes,
		})
		changed = true
		applied = append(applied, ItemResult{
			Kind: hub.KindCollection, ContentKey: item.ContentKey, Action: item.Action,
		})
	}
	if !changed {
		return applied, skips, backupPath, nil // 全部重续一致：不重写文件
	}

	out := legacydb.WriteCollectionDB(*existing)
	tmp := dbPath + ".lzsync-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return nil, nil, "", fmt.Errorf("deploy: 写临时 collection.db: %w", err)
	}
	// 回读自检（S9 结构三查精神：能完整解析回等值对象）。
	if data, err := os.ReadFile(tmp); err == nil {
		verify, verr := legacydb.ParseCollectionDB(data)
		if verr != nil || len(verify.Collections) != len(existing.Collections) {
			os.Remove(tmp)
			return nil, nil, "", fmt.Errorf("deploy: 临时 collection.db 自检失败")
		}
	} else {
		os.Remove(tmp)
		return nil, nil, "", fmt.Errorf("deploy: 回读自检: %w", err)
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		os.Remove(tmp)
		return nil, nil, "", fmt.Errorf("deploy: 原子替换 collection.db: %w", err)
	}
	return applied, skips, backupPath, nil
}

// legacySameMembers 判定两组收藏成员是否同一集合（顺序无关——stable
// 文件内成员序是写入时序，语义身份是成员集）。
func legacySameMembers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, v := range a {
		set[v]++
	}
	for _, v := range b {
		set[v]--
		if set[v] < 0 {
			return false
		}
	}
	return true
}

// loadLegacySetPayload 取谱面集决定值：PayloadJSON（三方判定决定值）优先，
// 回退 hub canonical 行（同 planFileHashes 的直查先例）。
func (r *Runner) loadLegacySetPayload(ctx context.Context, item PlanItem) (hub.BeatmapSet, error) {
	var bs hub.BeatmapSet
	if item.PayloadJSON != "" {
		if err := json.Unmarshal([]byte(item.PayloadJSON), &bs); err != nil {
			return bs, fmt.Errorf("谱面集项 %s 决定值解码: %w", item.ContentKey, err)
		}
		return bs, nil
	}
	var payload string
	err := r.deps.Hub.DB().QueryRowContext(ctx,
		`SELECT payload FROM beatmap_set WHERE content_hash=? AND superseded_by IS NULL`, item.ContentKey).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return bs, fmt.Errorf("deploy: hub 缺谱面集行 %s（并发清除？）", item.ContentKey)
	}
	if err != nil {
		return bs, err
	}
	if err := json.Unmarshal([]byte(payload), &bs); err != nil {
		return bs, fmt.Errorf("deploy: hub 行 %s 解码: %w", item.ContentKey, err)
	}
	return bs, nil
}

// legacySetDifficultyHashes 读 Songs/ 集目录内全部 .osu 的 SHA-256 集合
//（目录存在且含 .osu 才返回 ok；与收集侧难度 Hash 同键）。
func legacySetDifficultyHashes(dir string) (map[string]bool, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	out := map[string]bool{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(e.Name()), ".osu") {
			continue
		}
		content, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			return nil, false
		}
		sum := sha256.Sum256(content)
		out[hex.EncodeToString(sum[:])] = true
	}
	return out, len(out) > 0
}

// legacyFindSetDir 在 Songs/ 下找同 setID 前缀的既有目录。
func legacyFindSetDir(dataDir string, setID int64) (string, bool) {
	prefix := strconv.FormatInt(setID, 10) + " "
	entries, err := os.ReadDir(filepath.Join(dataDir, "Songs"))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			return e.Name(), true
		}
	}
	return "", false
}

// legacySetDirName 构造投放目录名 "<setID> <artist> - <title>"。目录名只
// 服务人类识别与 setID 前缀锚定（游戏不依赖其解析内容），规范化仅移除
// 文件系统非法字符；无 setID（未提交谱面）不带前缀。
func legacySetDirName(bs hub.BeatmapSet) string {
	artist := sanitizeDirComponent(bs.Artist)
	title := sanitizeDirComponent(bs.Title)
	if artist == "" {
		artist = "Unknown Artist"
	}
	if title == "" {
		title = "Unknown Title"
	}
	name := artist + " - " + title
	if bs.OnlineID > 0 {
		name = strconv.FormatInt(bs.OnlineID, 10) + " " + name
	}
	return name
}

// sanitizeDirComponent 移除 Windows 保留字符与控制符并折叠空白
//（wine 写 NTFS/FAT 名与游戏读取两侧安全）。
func sanitizeDirComponent(s string) string {
	const illegal = `\/:*?"<>|`
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(illegal, r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	fields := strings.Fields(b.String())
	return strings.Join(fields, " ")
}
