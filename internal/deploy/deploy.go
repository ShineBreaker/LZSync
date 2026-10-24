package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"lzsync/internal/filestore"
	"lzsync/internal/forks"
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

// batchSkinSize 皮肤批大小上限（§3.3：皮肤 ≤ 500 行/批）。
const batchSkinSize = 500

// batchBeatmapSetSize 谱面集批大小上限（§3.3：谱面集 ≤ 50 集/批——含
// 难度、Metadata、RealmNamedFileUsage 与对应 RealmFile 行的事务内存预算）。
const batchBeatmapSetSize = 50

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
// 写入面：收藏夹（M1）+ 谱面集（M2 create/update + 版本更新整集原子替换
// §5.4 三步——RealmFile 行与模型引用同批事务，§5.3 顺序铁律）+ 皮肤
// （M2，§5.1：Set 行白名单列 + Files usage，链接先行）。
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
		switch it.Kind {
		case hub.KindCollection, hub.KindBeatmapSet, hub.KindSkin:
		default:
			return report, fmt.Errorf("deploy: 写入面暂支持收藏夹、谱面集与皮肤，计划含 %q（后续块扩面）", it.Kind)
		}
	}
	batches := splitKindBatches(plan.Items)
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

	// 阶段 3：files CopyMissing 先行（S6 链接铁律；无文件面 = no-op）。
	// 哈希取自 hub payload 的 NamedFile（谱面集/皮肤 Files；成绩 .osr
	// kind 接入时在 planFileHashes 内扩）。
	hashes, err := planFileHashes(ctx, r.deps.Hub, plan)
	if err != nil {
		return report, err
	}
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
		applied, repointed, batchSkips, err := r.runBatch(ctx, realm, plan.TargetFork, batch, func(cur, n int) {
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

// runBatch 执行一个同 kind 批（§3.3 批 = 事务）：DriftGuard 骨架内逐项
// 现场复核 + 写入；任何写入异常整批回滚。skip 计数不触发回滚（合法落地
// 形态）。targetFork 用于 S8 判定（谱面集 OnlineID 携带面）。
func (r *Runner) runBatch(ctx context.Context, realm *realmc.Realm, targetFork string, batch []PlanItem,
	emit func(cur, total int)) ([]ItemResult, map[int]string, map[string]int, error) {
	dg, err := beginDriftGuard(realm)
	if err != nil {
		return nil, nil, nil, err
	}
	defer dg.rollback()

	var (
		colW  *collectionWriter
		setW  *beatmapSetWriter
		skinW *skinWriter
	)
	// 白名单 ∩ 目标实际列的缺列预演（人话原因，§1.5-4：不静默吞）。
	var colMissing, setMissing, skinMissing []string
	switch batch[0].Kind {
	case hub.KindCollection:
		if colW, err = prepareCollectionWriter(realm); err != nil {
			return nil, nil, nil, err
		}
		colMissing = colW.missingColumns()
	case hub.KindBeatmapSet:
		if setW, err = prepareBeatmapSetWriter(realm); err != nil {
			return nil, nil, nil, err
		}
		setMissing = setW.missingColumns()
	case hub.KindSkin:
		if skinW, err = prepareSkinWriter(realm); err != nil {
			return nil, nil, nil, err
		}
		skinMissing = skinW.missingColumns()
	}

	var applied []ItemResult
	repointed := map[int]string{}
	skips := map[string]int{}
	for i, item := range batch {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		// 计划面预演的跳过项（如版本更新动作待启用）：如实计数，不落地。
		if item.Action == ActionSkip {
			skips[item.SkipReason]++
			continue
		}
		switch item.Kind {
		case hub.KindCollection:
			c, origin, ok, err := r.deps.Hub.LoadCollection(ctx, item.ContentKey)
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				// hub 不删实体（§4.4）；缺行只可能是并发清除类操作，按跳过计。
				skips["hub 实体表中无此收藏（计划与真相源不符，跳过）"]++
				continue
			}
			_ = origin // M1 收藏夹无 S8 OnlineID 面；skin/score 扩面时消费 origin.ServerRealm
			ap, skip, rep, err := colW.exec(c, item.Action)
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
		case hub.KindBeatmapSet:
			bs, origin, ok, err := r.loadBeatmapSet(ctx, item.ContentKey)
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				skips["hub 实体表中无此谱面集当前版本（计划与真相源不符，跳过）"]++
				continue
			}
			ap, skip, rep, err := setW.exec(bs, item.OldContentKey, s8SameRealm(origin.Fork, targetFork), item.Action)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("批内项 %s: %w", item.ContentKey, err)
			}
			if skip.count > 0 {
				skips[skip.reason] += skip.count
			} else {
				applied = append(applied, ItemResult{Kind: item.Kind, ContentKey: item.ContentKey, Action: ap.action})
				for _, col := range setMissing {
					skips["目标库缺列 "+col+"（该列值未写入）"]++
				}
			}
			if rep != "" {
				repointed[i] = rep
			}
		case hub.KindSkin:
			// 皮肤无 OnlineID 面（DTO 无该列），origin 不参与 S8 判定。
			sk, _, ok, err := r.deps.Hub.LoadSkin(ctx, item.ContentKey)
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				skips["hub 实体表中无此皮肤（计划与真相源不符，跳过）"]++
				continue
			}
			ap, skip, rep, err := skinW.exec(sk, item.Action)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("批内项 %s: %w", item.ContentKey, err)
			}
			if skip.count > 0 {
				skips[skip.reason] += skip.count
			} else {
				applied = append(applied, ItemResult{Kind: item.Kind, ContentKey: item.ContentKey, Action: ap.action})
				for _, col := range skinMissing {
					skips["目标库缺列 "+col+"（该列值未写入）"]++
				}
			}
			if rep != "" {
				repointed[i] = rep
			}
		}
		emit(i+1, len(batch))
	}
	// 难度级跳过聚合（集落地但个别难度被拒：fork 专有规则集等）。
	if setW != nil {
		for reason, n := range setW.diffSkips {
			skips[reason] += n
		}
	}
	if err := dg.finish(); err != nil {
		return nil, nil, nil, err
	}
	return applied, repointed, skips, nil
}

// loadBeatmapSet 按内容键读取谱面集 canonical 值及其来源声明（只认版本
// 链尾——历史版本永不进入投放面，HB1）。hub 增补单键领域读方法
// （LoadBeatmapSet）后应切换；此处以 DB() 直查承载（LoadCollection 的
// 同构语义，只读面；写 tombstone 的既有先例见 clearRestoredTombstones）。
func (r *Runner) loadBeatmapSet(ctx context.Context, contentHash string) (hub.BeatmapSet, hub.Origin, bool, error) {
	var (
		payload    string
		originGUID string
		originFork string
	)
	err := r.deps.Hub.DB().QueryRowContext(ctx,
		`SELECT payload, origin_guid, origin_fork FROM beatmap_set WHERE content_hash=? AND superseded_by IS NULL`,
		contentHash).Scan(&payload, &originGUID, &originFork)
	if errors.Is(err, sql.ErrNoRows) {
		return hub.BeatmapSet{}, hub.Origin{}, false, nil
	}
	if err != nil {
		return hub.BeatmapSet{}, hub.Origin{}, false,
			fmt.Errorf("deploy: 读谱面集 %q: %w", contentHash, err)
	}
	var bs hub.BeatmapSet
	if err := json.Unmarshal([]byte(payload), &bs); err != nil {
		return hub.BeatmapSet{}, hub.Origin{}, false,
			fmt.Errorf("deploy: 解码谱面集 %q payload: %w", contentHash, err)
	}
	return bs, hub.Origin{GUID: originGUID, Fork: originFork}, true, nil
}

// s8SameRealm 判定 canonical 来源与目标是否同服务器命名空间（S8：
// origin_fork → ForkProfile.ServerRealm vs 目标 fork）。任一 fork 不在
// 注册表时保守视为异域（宁少勿错，§5.4「能少不能错」）。
func s8SameRealm(originFork, targetFork string) bool {
	op, ok1 := forks.Profile(originFork)
	tp, ok2 := forks.Profile(targetFork)
	if !ok1 || !ok2 {
		return false
	}
	return op.ServerRealm == tp.ServerRealm
}

// clearRestoredTombstones 投放成功后清除恢复项的 tombstone（§6.2：勾选
// 投放即同时清除）。hub 侧尚无对应的领域写方法（ApplySnapshot 的「同键
// 重现」闸是其只读对偶），此处以单句幂等 DELETE 经 DB() 执行——该写入
// 无多表原子性诉求，不破坏 hub 的事务纪律；hub 增补领域方法后应切换。
func (r *Runner) clearRestoredTombstones(ctx context.Context, plan Plan) error {
	type restoreKey struct {
		kind hub.EntityKind
		key  string
	}
	var keys []restoreKey
	for _, it := range plan.Items {
		if it.Restore {
			keys = append(keys, restoreKey{it.Kind, it.ContentKey})
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].key < keys[j].key
	})
	for _, k := range keys {
		if _, err := r.deps.Hub.DB().ExecContext(ctx,
			`DELETE FROM tombstone WHERE fork_id=? AND kind=? AND content_key=?`,
			plan.TargetFork, k.kind, k.key); err != nil {
			return fmt.Errorf("清 tombstone %s: %w", k.key, err)
		}
	}
	return nil
}

// planFileHashes 从 hub payload 收集计划涉及的实体文件哈希（§5.3 链接
// 先行的输入）：谱面集与皮肤取 Files 的 NamedFile（成绩 kind 接入时在此
// 扩面）。缺行不报错（批内现场按「计划与真相源不符」计数）。
func planFileHashes(ctx context.Context, h *hub.Store, plan Plan) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	addFiles := func(files []hub.NamedFile) {
		for _, f := range files {
			if !seen[f.Hash] {
				seen[f.Hash] = true
				out = append(out, f.Hash)
			}
		}
	}
	var setKeys, skinKeys []string
	for _, it := range plan.Items {
		if it.Action == ActionSkip {
			continue
		}
		switch it.Kind {
		case hub.KindBeatmapSet:
			setKeys = append(setKeys, it.ContentKey)
		case hub.KindSkin:
			skinKeys = append(skinKeys, it.ContentKey)
		}
	}
	for _, k := range setKeys {
		var payload string
		err := h.DB().QueryRowContext(ctx,
			`SELECT payload FROM beatmap_set WHERE content_hash=? AND superseded_by IS NULL`, k).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("deploy: 读谱面集 %s payload（链接先行）: %w", k, err)
		}
		var bs hub.BeatmapSet
		if err := json.Unmarshal([]byte(payload), &bs); err != nil {
			return nil, fmt.Errorf("deploy: 解码谱面集 %s payload（链接先行）: %w", k, err)
		}
		addFiles(bs.Files)
	}
	for _, k := range skinKeys {
		var payload string
		err := h.DB().QueryRowContext(ctx,
			`SELECT payload FROM skin WHERE content_hash=?`, k).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("deploy: 读皮肤 %s payload（链接先行）: %w", k, err)
		}
		var sk hub.Skin
		if err := json.Unmarshal([]byte(payload), &sk); err != nil {
			return nil, fmt.Errorf("deploy: 解码皮肤 %s payload（链接先行）: %w", k, err)
		}
		addFiles(sk.Files)
	}
	sort.Strings(out)
	return out, nil
}

// splitKindBatches 按对象类型分组切批（§3.3 批切分策略：收藏 ≤500 行/批、
// 谱面集 ≤50 集/批、皮肤 ≤500 行/批）；批内 kind 单一（runBatch 按首项
// kind 准备写入器）。批序 = 收藏 → 谱面集 → 皮肤（成绩批接入后排谱面集
// 之后——成绩挂接的难度索引惰性构建于谱面写入之后，§5.2-3）。
func splitKindBatches(items []PlanItem) [][]PlanItem {
	var cols, sets, skins []PlanItem
	for _, it := range items {
		switch it.Kind {
		case hub.KindBeatmapSet:
			sets = append(sets, it)
		case hub.KindSkin:
			skins = append(skins, it)
		default:
			cols = append(cols, it)
		}
	}
	return append(append(splitBatches(cols, batchCollectionSize),
		splitBatches(sets, batchBeatmapSetSize)...),
		splitBatches(skins, batchSkinSize)...)
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
