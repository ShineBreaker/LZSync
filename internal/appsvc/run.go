package appsvc

// 一轮同步编排（design.md §0.4）：PrepareRun/PrepareSelected 读同步范围 →
// 逐端守卫 → 按角色收集 → 全局交集预演 → CommitRun 逐目标端写入 →
// 重收集刷新快照。冲突/需确认/恢复项一律不进自动计划（§0-B4/B5），
// 在 Pending 计数中呈现供用户手动处置。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
)

// PendingSummary 一轮同步的待处理计数（§0-B4/B5/B6：绝不自动落地的行）。
type PendingSummary struct {
	Conflicts    int            // 冲突：多端取值不同（B5 无歧义检查拦下的 create 也在此计）
	NeedsConfirm int            // 需确认：版本更新（P1 一律不自动执行）
	Restore      int            // 恢复组（tombstone 命中的已删除项）
	Unsupported  int            // 不支持：全局交集排除等兼容跳过（CommitRun 后累加）
	ByKind       map[string]int // 冲突按 kind 分解（数据页未展示的类仍可处置——经服务面 Selection 拍板）
}

// RunTargetPreview 单个目标端的预览（将写入的动作项数，不含 skip）。
// Breakdown 是 kind|action → 项数的聚合诊断面（realdbtool 与执行页可
// 复核计划构成；键序按字典序序列化为 "kind|action" 形式）。
type RunTargetPreview struct {
	ForkID    string
	Items     int
	Breakdown map[string]int
}

// RunSkippedFork 本轮被跳过的端（运行中/收集失败等），携原因。
type RunSkippedFork struct {
	ForkID string
	Reason string
}

// RunPreview PrepareRun/PrepareSelected 的返回：runID + 逐目标端预览 +
// 跳过端 + 待处理计数。
type RunPreview struct {
	RunID   string
	Targets []RunTargetPreview
	Skipped []RunSkippedFork
	Pending PendingSummary
}

// ForkRunReport 单端的一轮执行摘要（收集/投放报告指针，nil = 未走到该步）。
type ForkRunReport struct {
	ForkID     string
	Source     bool // 本轮按角色拉取
	Target     bool // 本轮按角色写入
	Skipped    bool // 守卫/收集跳过（本轮不读不写）
	SkipReason string
	Collect    *collect.Report // 首轮收集；写入成功的目标端为投放后重收集
	Deploy     *deploy.Report
	Error      string // 投放/收尾失败（如实记录，不阻断其余目标端）
}

// RunReport CommitRun 的执行摘要。
type RunReport struct {
	Forks   []ForkRunReport
	Pending PendingSummary
	// BaselineError 基线推进失败（§0.3：推进失败下一轮会重判——写入本身
	// 幂等，如实呈现不阻断）。
	BaselineError string
}

// preparedRun 是 Prepare 与 Commit 之间的计划快照（只保留最新一轮）。
type preparedRun struct {
	plans          map[string]*deploy.Plan
	ini            map[string]map[string]deploy.IniValue // 逐端 ini 决定值（§0.3 逐键）
	forced         map[unitKey]*unitDecision             // 勾选拍板的 forced 判定（基线推进写决定值）
	skipped        map[string]string
	modes          map[string]collect.Mode // 各端本轮收集模式（写后重收集复用）
	roles          map[string]ForkRole
	refs           map[string]forks.LibraryRef
	kinds          map[hub.EntityKind]bool
	gameIni        bool
	sources        []string // 全部 Source 端（含被跳过——最后已知值参与判定）
	intersection   []string // 全局交集端清单（全部 Target=true 端）
	collectReports map[string]*collect.Report
	pending        PendingSummary
	order          []string        // 参与端排序（ForkID 序）
	fresh          map[string]bool // 本轮收集成功的端（presence 快照可信）
	freshIni       map[string]bool // 本轮 ini 快照成功的端
	// 基线推进面（§0.3）：全量轮 = 全部评估单位；勾选轮 = selUnits 子集。
	units    map[unitKey]*unitRow
	decs     map[unitKey]*unitDecision
	sel      []Selection
	selUnits map[unitKey]bool
}

var runSeq atomic.Int64

// sectionKinds 板块 → 实体类的圈定（game_ini 无实体类，只控 ini 收集面；
// beatmap_set 板块含 beatmap 难度）。
var sectionKinds = map[Section][]hub.EntityKind{
	SectionBeatmaps:        {hub.KindBeatmapSet, hub.KindBeatmap},
	SectionCollections:     {hub.KindCollection},
	SectionSkins:           {hub.KindSkin},
	SectionScores:          {hub.KindScore},
	SectionKeyBindings:     {hub.KindKeyBinding},
	SectionRulesetSettings: {hub.KindRulesetSetting},
	SectionModPresets:      {hub.KindModPreset},
}

// scopeKinds 板块开关 → 本轮收集/预演的 kind 面（§0：全板块开时恒非 nil
// 也无妨——恒真映射；全关时 nil 会让 hub 当「全部」处理，故恒给非 nil）。
func scopeKinds(cfg *configStore) map[hub.EntityKind]bool {
	out := map[hub.EntityKind]bool{}
	for _, sec := range AllSections {
		if cfg.sectionEnabled(sec) {
			for _, k := range sectionKinds[sec] {
				out[k] = true
			}
		}
	}
	return out
}

// collectMode 按角色取收集模式（§0 角色模型）。
func collectMode(role ForkRole) collect.Mode {
	switch {
	case role.Source && role.Target:
		return collect.ModeFull
	case role.Source:
		return collect.ModeSourceOnly
	default:
		return collect.ModeSnapshotOnly
	}
}

// PrepareRun 准备一轮同步（§0.4 步骤 1-4）：参与端 = 已注册且 Source||Target；
// 逐端守卫（运行中 → 跳过，本轮不读不写）；按角色选 Mode、按开启板块圈定
// Kinds 逐端收集；对每个未跳过的目标端预演 —— 只取 create 且非恢复组的项
// 入计划（冲突/版本更新/恢复计入 Pending，B4/B5/B6）。计划保存在内存
// （只保留最新一轮），CommitRun 凭 runID 执行。
func (s *realSync) PrepareRun() (RunPreview, error) {
	return s.prepareRun(context.Background(), nil)
}

// PrepareSelected 数据页勾选触发的一轮：计划只含 sel 中的项；sel 中的
// 冲突（→update）、恢复（Restore）、弱键版本（NeedsConfirm）视为用户
// 显式处置，允许入计划；仍受板块开关、目标角色与全局交集约束。不在任何
// 目标端预演中的勾选按「快照过期」记 skip 项。
func (s *realSync) PrepareSelected(sel []Selection) (RunPreview, error) {
	return s.prepareRun(context.Background(), sel)
}

func (s *realSync) prepareRun(ctx context.Context, sel []Selection) (RunPreview, error) {
	if err := s.begin("run", ""); err != nil {
		return RunPreview{}, err
	}
	defer s.finish()
	ctx, cancel := context.WithCancel(ctx)
	s.setCancel(cancel)
	defer cancel()

	kinds := scopeKinds(s.cfg)
	// ini：板块开即收快照 + 建判定单位（勾选轮的 ini 处置项需要单位
	// 在场且值为当前值——快照仓可能从未填充，不能只靠存量）。
	wantGameIni := s.cfg.sectionEnabled(SectionGameIni)
	intersection := s.cfg.targetForkIDs() // 全部 Target 端（含将被跳过的）

	// 参与端：已注册且 Source||Target。
	var parts []struct {
		ref  forks.LibraryRef
		role ForkRole
	}
	for _, ref := range s.reg.all() {
		role, ok := s.cfg.role(ref.ForkID)
		if !ok || (!role.Source && !role.Target) {
			continue
		}
		parts = append(parts, struct {
			ref  forks.LibraryRef
			role ForkRole
		}{ref, role})
	}

	run := &preparedRun{
		plans:          map[string]*deploy.Plan{},
		ini:            map[string]map[string]deploy.IniValue{},
		skipped:        map[string]string{},
		modes:          map[string]collect.Mode{},
		roles:          map[string]ForkRole{},
		refs:           map[string]forks.LibraryRef{},
		kinds:          kinds,
		gameIni:        wantGameIni,
		intersection:   intersection,
		collectReports: map[string]*collect.Report{},
		fresh:          map[string]bool{},
		freshIni:       map[string]bool{},
		sel:            sel,
	}

	// §0.4 步骤 2：逐端守卫 + 按角色收集。
	for _, p := range parts {
		run.roles[p.ref.ForkID] = p.role
		run.refs[p.ref.ForkID] = p.ref
		run.order = append(run.order, p.ref.ForkID)
		run.modes[p.ref.ForkID] = collectMode(p.role)
		if err := s.guardClear(p.ref.ForkID); err != nil {
			run.skipped[p.ref.ForkID] = fmt.Sprintf("游戏运行中：%s", err.Error())
			continue
		}
		opts := collect.Options{
			Mode:    run.modes[p.ref.ForkID],
			Kinds:   kinds,
			GameIni: wantGameIni, // 仅目标端也收 ini 快照（v(t) 与基线输入，读侧不改文件）
		}
		s.prog(ProgressEvent{Service: "run", Stage: string(collect.StageProject), At: time.Now(),
			Message: fmt.Sprintf("开始收集 %s", p.ref.ForkID)})
		cr, err := s.cRunner().CollectWith(ctx, p.ref, opts, func(stage collect.Stage, current, total int) {
			s.prog(ProgressEvent{Service: "run", Stage: string(stage), Current: current, Total: total,
				At: time.Now(), Message: fmt.Sprintf("%s · %s：%d/%d", p.ref.ForkID, stageLabel(stage), current, total)})
		})
		if err != nil {
			run.skipped[p.ref.ForkID] = err.Error() // 收集失败的端不写（presence 失真）
			continue
		}
		run.collectReports[p.ref.ForkID] = &cr
		run.fresh[p.ref.ForkID] = true
		if s.deps.GameIni != nil && wantGameIni {
			run.freshIni[p.ref.ForkID] = true
		}
	}
	sort.Strings(run.order)

	// §0.3 三方判定：候选来源 = 全部 Source 端（含被跳过——presence 是
	// 最后已知取值）；目标 = 全部 Target 端（含被跳过——它们仍参与全局
	// 交集与保护判定，只是不出计划）。
	sourceIDs := s.cfg.sourceForkIDs()
	sort.Strings(sourceIDs)
	run.sources = sourceIDs
	live := map[string]bool{}
	for _, p := range parts {
		if p.role.Target && run.skipped[p.ref.ForkID] == "" {
			live[p.ref.ForkID] = true
		}
	}
	outcome, err := runDecisions(ctx, &s.deps, sourceIDs, intersection, live, kinds, wantGameIni, run.freshIni)
	if err != nil {
		return RunPreview{}, fmt.Errorf("三方判定: %w", err)
	}
	run.units, run.decs, run.pending = outcome.units, outcome.decs, outcome.pending

	if sel == nil {
		// §0.4 步骤 3-4（全量轮）：计划与 ini 决定值由三方判定产出，
		// 逐 live 目标端装计划。
		for _, p := range parts {
			if !p.role.Target || !live[p.ref.ForkID] {
				continue
			}
			plan, err := deploy.NewPlan(p.ref.ForkID, outcome.plans[p.ref.ForkID])
			if err != nil {
				run.skipped[p.ref.ForkID] = err.Error()
				continue
			}
			plan.Target = p.ref
			plan.Intersection = intersection
			run.plans[p.ref.ForkID] = &plan
		}
		run.ini = outcome.ini
	} else {
		// 勾选轮（§0.4 手动处置）：勾选项即用户对冲突/恢复/弱键的显式
		// 拍板，按 canonical 变体生成计划（planItemFrom）；判定产物只用于
		// 待处理计数与基线推进面（selUnits 在 CommitRun 计算）。
		// 带 Winner 的勾选项走「以该端为准」处置装配（resolveSel）：胜者
		// 端当前取值作决定值、forced 语义解锁弱键/恢复门。
		var resolved []Selection
		selSet := make(map[string]bool, len(sel))
		for _, sl := range sel {
			if sl.Winner != "" {
				resolved = append(resolved, sl)
			} else {
				selSet[string(sl.Kind)+"\x00"+sl.ContentKey] = true
			}
		}
		resolvedPlans, resolvedIni, forced, rejects, err := s.resolveSel(ctx, resolved, outcome)
		if err != nil {
			return RunPreview{}, fmt.Errorf("处置装配: %w", err)
		}
		for t, m := range resolvedIni {
			if run.ini[t] == nil {
				run.ini[t] = map[string]deploy.IniValue{}
			}
			for k, v := range m {
				run.ini[t][k] = v
			}
		}
		run.forced = forced
		sum := newSummarizer(s.deps.Hub)
		for _, p := range parts {
			if !p.role.Target || !live[p.ref.ForkID] {
				continue
			}
			items, err := s.cmp.allReconcileItems(ctx, p.ref.ForkID)
			if err != nil {
				run.skipped[p.ref.ForkID] = err.Error()
				continue
			}
			// 合并胜者处置项：同键时胜者项胜（显式拍板优先）——resolved
			// 键先占位，canonical 项跳过已占键，防 NewPlan 同键报错。
			seen := map[string]bool{}
			for _, it := range resolvedPlans[p.ref.ForkID] {
				seen[string(it.Kind)+"\x00"+it.ContentKey] = true
			}
			var planItems []deploy.PlanItem
			for _, it := range items {
				key := string(it.Kind) + "\x00" + it.ContentKey
				if !kinds[it.Kind] || !selSet[key] || seen[key] {
					continue
				}
				planItems = append(planItems, planItemFrom(it, sum))
			}
			planItems = append(planItems, resolvedPlans[p.ref.ForkID]...)
			// 勾选但不在本端预演中的项：逐端如实记 skip（快照可能已过期）。
			inPlan := seen
			for _, it := range items {
				inPlan[string(it.Kind)+"\x00"+it.ContentKey] = true
			}
			for _, sl := range sel {
				key := string(sl.Kind) + "\x00" + sl.ContentKey
				reason := rejects[key]
				if reason == "" && inPlan[key] {
					continue
				}
				if reason == "" {
					reason = "预演数据中不存在该键（快照可能已过期，请刷新预览）"
				}
				planItems = append(planItems, deploy.PlanItem{
					Kind: sl.Kind, ContentKey: sl.ContentKey, Action: deploy.ActionSkip,
					Summary:    sum.summary(sl.Kind, sl.ContentKey),
					SkipReason: reason,
				})
			}
			plan, err := deploy.NewPlan(p.ref.ForkID, planItems)
			if err != nil {
				run.skipped[p.ref.ForkID] = err.Error()
				continue
			}
			plan.Target = p.ref
			plan.Intersection = intersection
			run.plans[p.ref.ForkID] = &plan
		}
	}

	// 预览：将写入的动作项数（不含 skip）。
	targets := []RunTargetPreview{}
	for _, fid := range run.order {
		plan, ok := run.plans[fid]
		if !ok {
			continue
		}
		n := 0
		bd := map[string]int{}
		for _, it := range plan.Items {
			if it.Action != deploy.ActionSkip {
				n++
				bd[string(it.Kind)+"|"+string(it.Action)]++
			}
		}
		targets = append(targets, RunTargetPreview{ForkID: fid, Items: n, Breakdown: bd})
	}
	skipped := []RunSkippedFork{}
	for fid, reason := range run.skipped {
		skipped = append(skipped, RunSkippedFork{ForkID: fid, Reason: reason})
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ForkID < skipped[j].ForkID })

	runID := fmt.Sprintf("run-%d-%d", time.Now().UnixMilli(), runSeq.Add(1))
	s.mu.Lock()
	s.runs = map[string]*preparedRun{runID: run} // 只保留最新一轮
	s.mu.Unlock()
	return RunPreview{RunID: runID, Targets: targets, Skipped: skipped, Pending: run.pending}, nil
}

// CommitRun 执行已准备的一轮（§0.4 步骤 6-7）：逐目标端投放（一端失败
// 记 Error 继续下一端）→ game.ini 按交集随后（板块开且有来源快照，源从
// Source 端中挑最新快照）→ 对写入成功的目标端按原 Mode 重收集刷新快照。
// 未知/过期 runID → 错误「请重新同步」。
func (s *realSync) CommitRun(runID string) (RunReport, error) {
	// 先拿忙锁再消费 runID——busy 时直接报错而非把计划删掉丢掉。
	if err := s.begin("run", ""); err != nil {
		return RunReport{}, err
	}
	defer s.finish()
	s.mu.Lock()
	run, ok := s.runs[runID]
	if ok {
		delete(s.runs, runID) // 一轮计划只消费一次
	}
	s.mu.Unlock()
	if !ok {
		return RunReport{}, errors.New("appsvc: 本轮同步计划已失效或不存在，请重新同步")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.setCancel(cancel)
	defer cancel()

	report := RunReport{Pending: run.pending, Forks: []ForkRunReport{}}
	// 基线推进的新鲜度面（§0.3「成功快照/写后重读」）：presence 基线要求
	// 本轮收集成功且（有写入 → 投放+重收集成功）；ini 基线要求本轮 ini
	// 快照成功且（有写入 → 写后重读成功）。任一环节失败的端基线整体
	// 不动——陈旧快照推进成基线会吞掉真实改动。
	fresh := map[string]bool{}
	for fid := range run.fresh {
		fresh[fid] = true
	}
	freshIni := map[string]bool{}
	for fid := range run.freshIni {
		freshIni[fid] = true
	}
	for _, fid := range run.order {
		role := run.roles[fid]
		fr := ForkRunReport{ForkID: fid, Source: role.Source, Target: role.Target}
		if cr, ok := run.collectReports[fid]; ok {
			fr.Collect = cr
		}
		if reason, skip := run.skipped[fid]; skip {
			fr.Skipped, fr.SkipReason = true, reason
			report.Forks = append(report.Forks, fr)
			continue
		}
		plan, hasPlan := run.plans[fid]
		actionable := false
		if hasPlan {
			for _, it := range plan.Items {
				if it.Action != deploy.ActionSkip {
					actionable = true
					break
				}
			}
		}
		realmOK := false // realm 投放成功（重收集的唯一触发条件）
		if role.Target && hasPlan && actionable {
			if ctx.Err() != nil {
				fr.Error = ctx.Err().Error()
				fresh[fid] = false
				report.Forks = append(report.Forks, fr)
				continue
			}
			s.prog(ProgressEvent{Service: "run", Stage: string(deploy.StageGuard), At: time.Now(),
				Message: fmt.Sprintf("开始投放到 %s（%d 项）", fid, len(plan.Items))})
			rep, derr := s.dRunner().Deploy(ctx, *plan, func(stage deploy.Stage, batchIndex, batchTotal, current, total int) {
				s.prog(ProgressEvent{Service: "run", Stage: string(stage), Current: current, Total: total,
					At: time.Now(), Message: fmt.Sprintf("%s · 批 %d/%d · %s：%d/%d", fid, batchIndex, batchTotal, stageLabel(stage), current, total)})
			})
			fr.Deploy = &rep
			if derr != nil {
				// realm 投放失败 → 本端不做 ini、不重收集、基线不动。
				fr.Error = derr.Error()
				fresh[fid] = false
				report.Forks = append(report.Forks, fr)
				continue
			}
			realmOK = true
			report.Pending.Unsupported += intersectionSkipCount(rep)
		}
		// game.ini 逐键决定值写入（§0.3/G8：板块开 + 未跳过 Target 且
		// 有决定键值即跑——「只有 ini 不同」必须能同步）。
		if role.Target && run.gameIni && len(run.ini[fid]) > 0 {
			if gi, gerr := deploy.SyncGameIniValues(run.refs[fid], run.ini[fid], run.intersection,
				s.deps.Backups, s.deps.Guard); gerr == nil {
				if fr.Deploy != nil {
					fr.Deploy.GameIni = &gi
				} else {
					fr.Deploy = &deploy.Report{GameIni: &gi}
				}
				// 写后重读快照（基线推进的最终值输入；勾选轮未重拍
				// ini 快照，写成功的端补记新鲜度好让处置单位基线推进）。
				if rerr := s.refreshIniSnapshot(run.refs[fid]); rerr != nil {
					freshIni[fid] = false
					if fr.Error == "" {
						fr.Error = fmt.Sprintf("ini 写入成功但快照重读失败：%s", rerr.Error())
					}
				} else {
					freshIni[fid] = true
				}
			} else {
				fr.Error = gerr.Error()
				freshIni[fid] = false // ini 写入失败 → 该端 ini 基线不动
			}
		}
		if realmOK {
			// 重收集：写入成功的目标端按原 Mode 刷新快照/presence。
			opts := collect.Options{
				Mode:    run.modes[fid],
				Kinds:   run.kinds,
				GameIni: run.gameIni,
			}
			if cr, cerr := s.cRunner().CollectWith(ctx, run.refs[fid], opts, nil); cerr == nil {
				fr.Collect = &cr
			} else {
				fresh[fid] = false // 重收集失败 → 最终值不可信，基线不动
				if fr.Error == "" {
					fr.Error = fmt.Sprintf("投放成功但重收集失败：%s", cerr.Error())
				}
			}
		}
		report.Forks = append(report.Forks, fr)
	}

	// §0.3 基线推进：全量轮 = 全部评估单位（冲突/保护冻结）；勾选轮 =
	// 勾选处置覆盖的单位子集（用户拍板即解析，解冻推进）。
	scope := run.units
	var only map[unitKey]bool
	if run.sel != nil {
		su, serr := s.selUnits(ctx, run.sel, run.units)
		if serr != nil {
			report.BaselineError = serr.Error()
			return report, nil
		}
		only = su
		scope = suRows(run.units, su)
	}
	rows := baselineRows(scope, run.decs, run.forced, func(kind string) map[string]bool {
		if kind == hub.BaselineKindGameIni {
			return freshIni
		}
		return fresh
	}, only != nil, time.Now().UnixMilli())
	if err := s.deps.Hub.AdvanceBaselines(ctx, rows); err != nil {
		report.BaselineError = err.Error()
	}
	return report, nil
}

// suRows 勾选轮的基线推进面：只含勾选单位（未勾选的冲突保持冻结）。
func suRows(units map[unitKey]*unitRow, only map[unitKey]bool) map[unitKey]*unitRow {
	out := map[unitKey]*unitRow{}
	for uk := range only {
		if row, ok := units[uk]; ok {
			out[uk] = row
		}
	}
	return out
}

// intersectionSkipCount 全局交集排除的 skip 计数（Pending.Unsupported 的
// 输入，§0-B3/G6）。
func intersectionSkipCount(rep deploy.Report) int {
	n := 0
	for _, sk := range rep.Skipped {
		if strings.HasPrefix(sk.Reason, deploy.IntersectionRejectPrefix) {
			n += sk.Count
		}
	}
	return n
}

// resolveSel 勾选处置装配（§0.4 手动处置的「以该端为准」路径）：每条带
// Winner 的勾选 → 覆盖单位 → 以胜者端当前值构造 forced 判定（writable
// = 全部目标端——显式覆盖保护/差异态）→ 子集装配复用全套兼容预检与
// 谱面集版本替换语义。tombstone/弱键门由 forced 降级为 Restore/
// NeedsConfirm 项。胜者端不持有（或持多版本）的勾选记拒绝原因。
// 返回 (目标端→计划项, 目标端→ini 决定值, 单位→forced 判定, 勾选键→拒绝原因)。
func (s *realSync) resolveSel(ctx context.Context, sel []Selection,
	outcome *decideOutcome) (map[string][]deploy.PlanItem, map[string]map[string]deploy.IniValue,
	map[unitKey]*unitDecision, map[string]string, error) {
	plans := map[string][]deploy.PlanItem{}
	ini := map[string]map[string]deploy.IniValue{}
	forced := map[unitKey]*unitDecision{}
	rejects := map[string]string{}
	if len(sel) == 0 {
		return plans, ini, forced, rejects, nil
	}
	in := outcome.in
	done := map[unitKey]bool{}
	for _, sl := range sel {
		key := string(sl.Kind) + "\x00" + sl.ContentKey
		uks, err := s.unitsForSelection(ctx, sl, outcome.units)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		// 单位按 member 排序：哨兵（member=""）先评估——收藏的「胜者
		// 端持有」校验在哨兵处定，成员缺席降级为移除语义而非拒绝。
		sorted := make([]unitKey, 0, len(uks))
		for uk := range uks {
			sorted = append(sorted, uk)
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].member < sorted[j].member })
		subUnits := map[unitKey]*unitRow{}
		subDecs := map[unitKey]*unitDecision{}
		reason := ""
		for _, uk := range sorted {
			row, ok := outcome.units[uk]
			if !ok {
				continue // 纯基线残留单位（无 presence）——处置面只认当前单位
			}
			wv, held := row.cur[sl.Winner]
			if !held {
				if uk.kind == string(hub.KindCollection) && uk.member != "" {
					wv = unitVal{fp: "0"} // 胜者端该成员缺席 → 以胜者为准即移除
				} else {
					reason = fmt.Sprintf("%s 端不持有该项，无法以其为准", sl.Winner)
					break
				}
			}
			if uk.kind == string(hub.KindBeatmapSet) {
				if hs := decodeSetFP(wv.fp); len(hs) != 1 {
					reason = fmt.Sprintf("%s 端持有该谱面集 %d 个版本，无法以其为准", sl.Winner, len(hs))
					break
				}
			}
			if done[uk] {
				continue // 同单位已被前序勾选处置（首条生效，确定性）
			}
			done[uk] = true
			subUnits[uk] = row
			writable := make(map[string]bool, len(in.targets))
			for _, t := range in.targets {
				writable[t] = true
			}
			subDecs[uk] = &unitDecision{
				decided: true, fp: wv.fp, winner: sl.Winner, forced: true,
				writable:  writable,
				protected: map[string]bool{},
				divergent: map[string]bool{},
			}
		}
		if reason != "" {
			rejects[key] = reason
			continue
		}
		sub := assemblePlans(in, subUnits, subDecs)
		for t, items := range sub.plans {
			plans[t] = append(plans[t], items...)
		}
		for t, m := range sub.ini {
			if ini[t] == nil {
				ini[t] = map[string]deploy.IniValue{}
			}
			for k, v := range m {
				ini[t][k] = v
			}
		}
		for uk, d := range subDecs {
			forced[uk] = d
		}
	}
	return plans, ini, forced, rejects, nil
}
