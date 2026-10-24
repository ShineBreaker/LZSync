package deploy

import (
	"context"
	"fmt"
	"sort"

	"lzsync/internal/filestore"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// Deps 是 Runner 的协作依赖。
type Deps struct {
	Hub     *hub.Store
	Files   *filestore.Engine // M1 收藏夹无 files 依赖，可为 nil；M2 起必填
	Backups *guard.Backups    // nil 时备份退化为库同目录时间戳副本（S2 退化语义）
	Guard   *guard.Guard      // nil 时跳过进程名扫描——占用探测以 .lock 非阻塞
	// 探测 + 写会话打开为最终防线（S5：写会话打开本身是最强占用探测）
}

// Stage 投放进度阶段（§3.3 批内顺序 + 两个一次性阶段）。
type Stage string

const (
	StageGuard  Stage = "guard"  // 守卫复查（投放启动时一次通过）
	StageBackup Stage = "backup" // 自动备份（首批前一次性生成，全计划共用，S2）
	StageLink   Stage = "link"   // 批内：建链接先行（文件计数，S6）
	StageWrite  Stage = "write"  // 批内：单事务写 realm 行（对象计数，File 表行与模型行同事务）
	StageVerify Stage = "verify" // 批内：DriftGuard 写前/写后结构快照比对
)

// ProgressFunc 批级进度回调；批事务进行中不回调（批内不可打断，§3.3）。
type ProgressFunc func(stage Stage, batchIndex, batchTotal, current, total int)

// ItemResult 单个已提交项结果。
type ItemResult struct {
	Kind       hub.EntityKind
	ContentKey string
	Action     Action
}

// BatchFailure 失败批：整批回滚 + 原因（§3.3 部分失败语义）。
type BatchFailure struct {
	BatchIndex int
	Reason     string
	Items      []PlanItem
}

// RepointedItem 执行期改判：现场复核与计划不符的项（§3.3——预览与执行
// 之间目标库可能漂移，绝不按计划快照盲目写入）。
type RepointedItem struct {
	Item   PlanItem
	Reason string // 如「目标已出现同键行：新建 → 就地更新」
}

// Report 投放执行报告（三段式 + 改判 + 备份，§3.3/§6.3）。
type Report struct {
	Applied       []ItemResult    // 已提交批的 applied 计数明细
	Skipped       []hub.SkipStat  // 跳过计数（原因分组：列缺失 / 重续已一致 / 目标无表等）
	FailedBatches []BatchFailure  // 失败批（整批回滚 + 原因）
	NotRun        []PlanItem      // 未执行批清单（「从失败处重续」入口的输入）
	Repointed     []RepointedItem // 执行期改判
	BackupPath    string          // 本次共用的写前备份路径
}

// batchCollectionSize 收藏夹批大小上限（§3.3：收藏 ≤ 500 行/批）。
const batchCollectionSize = 500

// Runner 执行投放。
type Runner struct {
	deps Deps
}

// NewRunner 创建投放编排器。
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Deploy 执行计划（§3.3 执行模型）：
// 守卫复查（进程扫描 + 非阻塞占用探测）→ 首批前一次性备份 → files
// CopyMissing 先行（S6 链接形态的 M1 复制形态；收藏夹无文件时 no-op）→
// 按对象类型切批（批 = 事务）→ 每批「单事务写 realm 行 → DriftGuard
// （任何结构漂移整批回滚并上报工具缺陷）」→ 每计划项以目标库现场状态
// 重跑身份匹配与判定（改判入报告）。
// 批间是取消点（ctx 取消 → 停止后续批，已提交批保留）；批失败 → 整批回滚
// 并停止，报告部分完成（合法终态，不强制整体撤销）。
// M1 范围：收藏夹（无 File 行依赖，§5.1；File 表行与模型引用成对写入的
// 纪律在 M2 谱面集写入器落地）。
//
// 目标库取 plan.Target（投放执行需要 RealmPath；字符串 forkID 不足以定位
// 库文件——计划构建方 BuildPlan 已填，手工构造计划的调用方须自填）。
func (r *Runner) Deploy(ctx context.Context, plan Plan, prog ProgressFunc) (Report, error) {
	report := Report{}
	if r.deps.Hub == nil {
		return report, fmt.Errorf("deploy: Deps.Hub 未配置")
	}
	target := plan.Target
	if target.RealmPath == "" {
		return report, fmt.Errorf("deploy: 计划缺目标库引用（plan.Target 未解析，RealmPath 为空）")
	}
	if plan.TargetFork != target.ForkID {
		return report, fmt.Errorf("deploy: 计划目标 fork %q 与目标库 %q 不符", plan.TargetFork, target.ForkID)
	}
	for _, it := range plan.Items {
		if it.Kind != hub.KindCollection {
			return report, fmt.Errorf("deploy: M1 写入面仅收藏夹，计划含 %q（M2 扩面）", it.Kind)
		}
	}
	batches := splitBatches(plan.Items, batchCollectionSize)
	total := len(batches)
	emit := func(stage Stage, b, cur, n int) {
		if prog != nil {
			prog(stage, b, total, cur, n)
		}
	}

	// 阶段 1：守卫复查（进程名扫描可用则先行给友好提示；占用探测随后）。
	emit(StageGuard, 0, 0, 1)
	if r.deps.Guard != nil {
		if err := r.deps.Guard.EnsureClear(); err != nil {
			return report, err
		}
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := probeOccupied(target.RealmPath); err != nil {
		return report, err
	}

	lib, err := realmc.Load()
	if err != nil {
		return report, fmt.Errorf("deploy: native 库不可用（NativeLibMissing，P-2 降级由 UI 呈现）: %w", err)
	}

	// 阶段 2：写会话打开（S5：打开本身就是最强占用探测）+ 首批前一次性备份。
	realm, err := lib.OpenDynamicReadWrite(target.RealmPath)
	if err != nil {
		return report, fmt.Errorf("deploy: 打开目标库 %s: %w", target.RealmPath, err)
	}
	defer realm.Close()
	emit(StageBackup, 0, 0, 1)
	backupPath, err := backupTarget(r.deps.Backups, target.RealmPath)
	if err != nil {
		return report, err // S2：备份不可绕过，失败即中止
	}
	report.BackupPath = backupPath

	// 阶段 3：files CopyMissing 先行（S6；收藏夹 = 空文件集 no-op）。
	hashes := planFileHashes(plan)
	emit(StageLink, 0, 0, len(hashes))
	if len(hashes) > 0 {
		if r.deps.Files == nil {
			return report, fmt.Errorf("deploy: 计划涉及 %d 个文件哈希但 Deps.Files 未配置（M2 起必填）", len(hashes))
		}
		if _, err := filestore.CopyMissing(
			r.deps.Files.Store.Root,
			filestore.ResolveFilesDir(target.RealmPath),
			hashes,
		); err != nil {
			return report, fmt.Errorf("deploy: files 先行搬运失败（本计划与后续全部中止）: %w", err)
		}
	}

	// 阶段 4：逐批执行。批失败 → 整批回滚并停止；已提交批保留（§3.3）。
	var skipped = map[string]int{}
	for bi, batch := range batches {
		if err := ctx.Err(); err != nil { // 批间取消点
			report.NotRun = flatten(batches[bi:])
			return report, err
		}
		applied, repointed, batchSkips, err := r.runCollectionBatch(ctx, realm, batch, func(cur, n int) {
			emit(StageWrite, bi, cur, n)
		})
		if err == nil {
			emit(StageVerify, bi, 1, 1)
		}
		for reason, n := range batchSkips {
			skipped[reason] += n
		}
		if err != nil {
			report.FailedBatches = append(report.FailedBatches, BatchFailure{
				BatchIndex: bi, Reason: err.Error(), Items: batch,
			})
			report.NotRun = flatten(batches[bi+1:])
			report.Skipped = sortedSkips(skipped)
			return report, err
		}
		report.Applied = append(report.Applied, applied...)
		for it, reason := range repointed {
			report.Repointed = append(report.Repointed, RepointedItem{Item: batch[it], Reason: reason})
		}
	}
	report.Skipped = sortedSkips(skipped)

	// 恢复项投放成功 → 清除对应 tombstone（§5.4 删除不复活：确认动作即
	// 清除）。清失败不回滚 realm（已提交批保留）；下次收集的「同键重现」
	// 第三道闸会自然清掉（hub.reconcile），此处仅计数说明。
	if err := r.clearRestoredTombstones(ctx, plan); err != nil {
		report.Skipped = append(report.Skipped, hub.SkipStat{
			Reason: "投放成功但清除恢复项 tombstone 失败（下次收集会自动清除）: " + err.Error(),
			Count:  1,
		})
	}

	// 写后 mtime 戳（mmap 写入不更新 mtime，ezrealmsync-design §5.6）。
	if err := touchAfterWrite(target.RealmPath); err != nil {
		return report, fmt.Errorf("deploy: 写后 mtime 戳: %w", err)
	}
	return report, nil
}

// runCollectionBatch 执行一个收藏夹批：DriftGuard 骨架内逐项现场复核 +
// 写入；任何写入异常整批回滚。skip 计数不触发回滚（合法落地形态）。
func (r *Runner) runCollectionBatch(ctx context.Context, realm *realmc.Realm, batch []PlanItem,
	emit func(cur, total int)) ([]ItemResult, map[int]string, map[string]int, error) {
	dg, err := beginDriftGuard(realm)
	if err != nil {
		return nil, nil, nil, err
	}
	defer dg.rollback()

	w, err := prepareCollectionWriter(realm)
	if err != nil {
		return nil, nil, nil, err
	}
	// 白名单 ∩ 目标实际列的缺列预演（人话原因，§1.5-4：不静默吞）。
	colMissing := w.missingColumns()

	var applied []ItemResult
	repointed := map[int]string{}
	skips := map[string]int{}
	for i, item := range batch {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		c, origin, ok, err := r.deps.Hub.LoadCollection(ctx, item.ContentKey)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			// hub 不删实体（§4.4）；缺行只可能是并发清除类操作，按跳过计。
			skips["hub 实体表中无此收藏（计划与真相源不符，跳过）"]++
			continue
		}
		_ = origin // M1 收藏夹无 S8 OnlineID 面；M2 扩面时消费 origin.ServerRealm
		ap, skip, rep, err := w.exec(c, item.Action)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("批内项 %s: %w", item.ContentKey, err)
		}
		if skip.count > 0 {
			skips[skip.reason] += skip.count
		} else {
			applied = append(applied, ItemResult{Kind: item.Kind, ContentKey: item.ContentKey, Action: ap.action})
			// 落地项的缺列逐列计数（每项 × 每缺列一条人话原因）。
			for _, col := range colMissing {
				skips["目标库缺列 "+col+"（该列值未写入）"]++
			}
		}
		if rep != "" {
			repointed[i] = rep
		}
		emit(i+1, len(batch))
	}
	if err := dg.finish(); err != nil {
		return nil, nil, nil, err
	}
	return applied, repointed, skips, nil
}

// clearRestoredTombstones 投放成功后清除恢复项的 tombstone（§6.2：勾选
// 投放即同时清除）。hub 侧尚无对应的领域写方法（ApplySnapshot 的「同键
// 重现」闸是其只读对偶），此处以单句幂等 DELETE 经 DB() 执行——该写入
// 无多表原子性诉求，不破坏 hub 的事务纪律；hub 增补领域方法后应切换。
func (r *Runner) clearRestoredTombstones(ctx context.Context, plan Plan) error {
	var keys []string
	for _, it := range plan.Items {
		if it.Restore {
			keys = append(keys, it.ContentKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := r.deps.Hub.DB().ExecContext(ctx,
			`DELETE FROM tombstone WHERE fork_id=? AND kind=? AND content_key=?`,
			plan.TargetFork, hub.KindCollection, k); err != nil {
			return fmt.Errorf("清 tombstone %s: %w", k, err)
		}
	}
	return nil
}

// planFileHashes 计划涉及的实体文件哈希（M1 收藏夹恒空；M2 谱面集/皮肤/
// 成绩扩面时从 canonical Files/ReplayHashes 聚合）。
func planFileHashes(plan Plan) []string {
	return nil
}

func splitBatches(items []PlanItem, size int) [][]PlanItem {
	if len(items) == 0 {
		return nil
	}
	var out [][]PlanItem
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[start:end])
	}
	return out
}

func flatten(batches [][]PlanItem) []PlanItem {
	var out []PlanItem
	for _, b := range batches {
		out = append(out, b...)
	}
	return out
}

func sortedSkips(m map[string]int) []hub.SkipStat {
	reasons := make([]string, 0, len(m))
	for r := range m {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	out := make([]hub.SkipStat, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, hub.SkipStat{Reason: r, Count: m[r]})
	}
	return out
}
