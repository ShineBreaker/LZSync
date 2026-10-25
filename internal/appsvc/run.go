package appsvc

// 一轮同步编排（design.md §0.4）：PrepareRun/PrepareSelected 读同步范围 →
// 逐端守卫 → 按角色收集 → 全局交集预演 → CommitRun 逐目标端写入 →
// 重收集刷新快照。冲突/需确认/恢复项一律不进自动计划（§0-B4/B5），
// 在 Pending 计数中呈现供用户手动处置。

import (
	"context"
	"encoding/json"
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
	Conflicts    int // 冲突：多端取值不同（B5 无歧义检查拦下的 create 也在此计）
	NeedsConfirm int // 需确认：版本更新（P1 一律不自动执行）
	Restore      int // 恢复组（tombstone 命中的已删除项）
	Unsupported  int // 不支持：全局交集排除等兼容跳过（CommitRun 后累加）
}

// RunTargetPreview 单个目标端的预览（将写入的动作项数，不含 skip）。
type RunTargetPreview struct {
	ForkID string
	Items  int
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
}

// preparedRun 是 Prepare 与 Commit 之间的计划快照（只保留最新一轮）。
type preparedRun struct {
	plans          map[string]*deploy.Plan
	skipped        map[string]string
	modes          map[string]collect.Mode // 各端本轮收集模式（写后重收集复用）
	roles          map[string]ForkRole
	refs           map[string]forks.LibraryRef
	kinds          map[hub.EntityKind]bool
	gameIni        bool
	sources        []string // 成功收集的来源端
	intersection   []string // 全局交集端清单（全部 Target=true 端）
	collectReports map[string]*collect.Report
	pending        PendingSummary
	order          []string // 参与端排序（ForkID 序）
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
	wantGameIni := sel == nil && s.cfg.sectionEnabled(SectionGameIni) // 同步所选只处理勾选项，不带 game.ini
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
		skipped:        map[string]string{},
		modes:          map[string]collect.Mode{},
		roles:          map[string]ForkRole{},
		refs:           map[string]forks.LibraryRef{},
		kinds:          kinds,
		gameIni:        wantGameIni,
		intersection:   intersection,
		collectReports: map[string]*collect.Report{},
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
			GameIni: wantGameIni && p.role.Source,
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
		if p.role.Source {
			run.sources = append(run.sources, p.ref.ForkID)
		}
	}
	sort.Strings(run.order)
	sort.Strings(run.sources)

	// sel 过滤集（PrepareSelected）。
	var selSet map[string]bool
	if sel != nil {
		selSet = make(map[string]bool, len(sel))
		for _, sl := range sel {
			selSet[string(sl.Kind)+"\x00"+sl.ContentKey] = true
		}
	}
	sum := newSummarizer(s.deps.Hub)
	conflictSeen := map[string]bool{}
	confirmSeen := map[string]bool{}
	restoreSeen := map[string]bool{}

	// §0.4 步骤 3-4：逐目标端预演（create 非恢复 + B5 无歧义检查）。
	// 歧义检查的来源集合 = config 中全部 Source 角色端（含本轮被跳过者——
	// 其 presence 是最后已知取值，纳入更保守）；run.sources 只用于 ini 源。
	sourceIDs := s.cfg.sourceForkIDs()
targetLoop:
	for _, p := range parts {
		if !p.role.Target {
			continue
		}
		if _, dead := run.skipped[p.ref.ForkID]; dead {
			continue
		}
		items, err := s.cmp.allReconcileItems(ctx, p.ref.ForkID)
		if err != nil {
			run.skipped[p.ref.ForkID] = err.Error()
			continue
		}
		var planItems []deploy.PlanItem
		for _, it := range items {
			if !kinds[it.Kind] {
				continue // 板块关闭
			}
			if selSet != nil && !selSet[string(it.Kind)+"\x00"+it.ContentKey] {
				continue
			}
			key := string(it.Kind) + "\x00" + it.ContentKey
			switch it.Category {
			case hub.ReconcileCreate:
				if it.Restorable {
					if selSet != nil {
						planItems = append(planItems, planItemFrom(it, sum))
						continue
					}
					if !restoreSeen[key] {
						restoreSeen[key] = true
						run.pending.Restore++
					}
					continue
				}
				amb, aerr := s.ambiguousValue(ctx, it, p.ref.ForkID, sourceIDs)
				if aerr != nil {
					// fail-closed：取值集读不全 = 无法判歧义，本端不写。
					run.skipped[p.ref.ForkID] = fmt.Sprintf("歧义检查失败：%v", aerr)
					continue targetLoop
				}
				if amb {
					if !conflictSeen[key] {
						conflictSeen[key] = true
						run.pending.Conflicts++
					}
					continue // §0-B5：取值无歧义才可自动写
				}
				planItems = append(planItems, planItemFrom(it, sum))
			case hub.ReconcileConflict:
				if selSet != nil {
					planItems = append(planItems, planItemFrom(it, sum)) // 显式勾选 = 推 hub 变体
					continue
				}
				if !conflictSeen[key] {
					conflictSeen[key] = true
					run.pending.Conflicts++
				}
			case hub.ReconcileVersionUpdate:
				if selSet != nil {
					planItems = append(planItems, planItemFrom(it, sum))
					continue
				}
				if !confirmSeen[key] {
					confirmSeen[key] = true
					run.pending.NeedsConfirm++
				}
			}
		}
		if selSet != nil {
			// 勾选但不在本端预演中的项：逐端如实记 skip（快照可能已过期）。
			inPlan := map[string]bool{}
			for _, it := range items {
				inPlan[string(it.Kind)+"\x00"+it.ContentKey] = true
			}
			for _, sl := range sel {
				key := string(sl.Kind) + "\x00" + sl.ContentKey
				if inPlan[key] {
					continue
				}
				planItems = append(planItems, deploy.PlanItem{
					Kind: sl.Kind, ContentKey: sl.ContentKey, Action: deploy.ActionSkip,
					Summary:    sum.summary(sl.Kind, sl.ContentKey),
					SkipReason: "预演数据中不存在该键（快照可能已过期，请刷新预览）",
				})
			}
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

	// 预览：将写入的动作项数（不含 skip）。
	targets := []RunTargetPreview{}
	for _, fid := range run.order {
		plan, ok := run.plans[fid]
		if !ok {
			continue
		}
		n := 0
		for _, it := range plan.Items {
			if it.Action != deploy.ActionSkip {
				n++
			}
		}
		targets = append(targets, RunTargetPreview{ForkID: fid, Items: n})
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
				// realm 投放失败 → 本端不做 ini、不重收集。
				fr.Error = derr.Error()
				report.Forks = append(report.Forks, fr)
				continue
			}
			realmOK = true
			report.Pending.Unsupported += intersectionSkipCount(rep)
		}
		// game.ini 白名单键同步（板块开 + 未跳过 Target 即跑——即使本轮
		// 该端 realm 无可执行项；「只有 ini 不同」必须能同步）。源只从
		// Source 端中挑快照最新者，peers=全部 Target 端交集。
		if role.Target && run.gameIni && s.deps.GameIni != nil {
			if src := s.gameIniSourceForkFrom(run.sources, fid); src != "" {
				if gi, gerr := deploy.SyncGameIni(run.refs[fid], src, run.intersection,
					s.deps.GameIni, s.deps.Backups, s.deps.Guard); gerr == nil {
					if fr.Deploy != nil {
						fr.Deploy.GameIni = &gi
					} else {
						fr.Deploy = &deploy.Report{GameIni: &gi}
					}
				} else {
					fr.Error = gerr.Error()
				}
			}
		}
		if realmOK {
			// 重收集：写入成功的目标端按原 Mode 刷新快照/presence。
			opts := collect.Options{
				Mode:    run.modes[fid],
				Kinds:   run.kinds,
				GameIni: run.gameIni && role.Source,
			}
			if cr, cerr := s.cRunner().CollectWith(ctx, run.refs[fid], opts, nil); cerr == nil {
				fr.Collect = &cr
			} else if fr.Error == "" {
				fr.Error = fmt.Sprintf("投放成功但重收集失败：%s", cerr.Error())
			}
		}
		report.Forks = append(report.Forks, fr)
	}
	return report, nil
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

// gameIniSourceForkFrom 在候选来源端中选 game.ini 快照最新者（快照时间
// LastWrite-wins 保守近似；无快照返回空串——调用方跳过 ini 段）。
func (s *realSync) gameIniSourceForkFrom(candidates []string, target string) string {
	best, bestAt := "", time.Time{}
	for _, fid := range candidates {
		if fid == target {
			continue
		}
		_, at, ok, err := s.deps.GameIni.Load(fid)
		if err != nil || !ok {
			continue
		}
		if best == "" || at.After(bestAt) {
			best, bestAt = fid, at
		}
	}
	return best
}

// --- §0-B5 取值无歧义检查（create 前，fail-closed） ---

// ambiguousValue 报告 create 项在各来源端间取值是否有歧义（有歧义 = 不
// 入计划、计 Conflicts；hub 首见即定的 canonical 只是先到的端值，自动
// 写出去等于替用户拍板）。查询/扫描错误一律上抛——取值集合读不全时
// 按「无法判歧义」放行等于把不确定当确定，fail-closed 才对。
//
//   - collection / ruleset_setting / mod_preset：多个来源端 presence
//     对同一内容键的同步值字段不一致 → 歧义；
//   - key_binding：目标端 presence 已有同 (ruleset, variant, action) 但
//     不同 key_combination 的行 → 歧义（同动作多键位布局冲突）；
//   - beatmap_set：同一谱面身份键下来源端 presence 持有 ≥2 个不同版本
//     （identity_version 配对键）→ 歧义；
//   - skin / score：内容键即文件哈希，无取值歧义可能。
func (s *realSync) ambiguousValue(ctx context.Context, it hub.ReconcileItem, targetFork string, sources []string) (bool, error) {
	switch it.Kind {
	case hub.KindCollection, hub.KindRulesetSetting, hub.KindModPreset:
		vals, err := s.presenceValues(ctx, it.Kind, it.ContentKey, sources)
		if err != nil {
			return false, err
		}
		return len(vals) >= 2, nil
	case hub.KindKeyBinding:
		return s.keyBindingAmbiguous(ctx, it.ContentKey, targetFork)
	case hub.KindBeatmapSet:
		return s.beatmapSetAmbiguous(ctx, it.ContentKey, sources)
	default:
		return false, nil
	}
}

// presenceValues 取给定 (kind, key) 在各来源端 presence 中的同步值指纹
// 去重集合（local_values 是 fork 侧投影 DTO JSON——按同步值比较，不比
// 端本地字段）。
func (s *realSync) presenceValues(ctx context.Context, kind hub.EntityKind, contentKey string, sources []string) (map[string]bool, error) {
	vals := map[string]bool{}
	if len(sources) == 0 {
		return vals, nil
	}
	rows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT fork_id, local_values FROM presence WHERE kind=? AND content_key=? AND fork_id IN (`+
			placeholders(len(sources))+`)`, presenceArgs(kind, contentKey, sources)...)
	if err != nil {
		return nil, fmt.Errorf("读 presence 取值集: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fid, lv string
		if err := rows.Scan(&fid, &lv); err != nil {
			return nil, fmt.Errorf("扫 presence 行: %w", err)
		}
		if v := syncValueFingerprint(kind, lv); v != "" {
			vals[v] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 presence 行: %w", err)
	}
	return vals, nil
}

func placeholders(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	return b.String()
}

func presenceArgs(kind hub.EntityKind, contentKey string, sources []string) []any {
	args := make([]any, 0, len(sources)+2)
	args = append(args, string(kind), contentKey)
	for _, f := range sources {
		args = append(args, f)
	}
	return args
}

// syncValueFingerprint 从 presence.local_values（投影 DTO JSON）提取
// 「同步值」指纹——只比要同步的字段，端本地字段不参与比较。
func syncValueFingerprint(kind hub.EntityKind, localValues string) string {
	switch kind {
	case hub.KindCollection:
		var c hub.Collection
		if err := json.Unmarshal([]byte(localValues), &c); err != nil {
			return ""
		}
		hashes := append([]string(nil), c.BeatmapMD5Hashes...)
		sort.Strings(hashes)
		return strings.Join(hashes, "|")
	case hub.KindRulesetSetting:
		var rs hub.RulesetSetting
		if err := json.Unmarshal([]byte(localValues), &rs); err != nil {
			return ""
		}
		return rs.Value
	case hub.KindModPreset:
		var mp hub.ModPreset
		if err := json.Unmarshal([]byte(localValues), &mp); err != nil {
			return ""
		}
		return mp.Description + "\x00" + mp.ModsJson
	default:
		return ""
	}
}

// keyBindingAmbiguous 目标端 presence 已有同 (ruleset, variant, action)
// 但不同 key_combination 的行 → 歧义（一条 action 多行多键的布局差异，
// 自动写入会替用户拍板）。
func (s *realSync) keyBindingAmbiguous(ctx context.Context, contentKey, targetFork string) (bool, error) {
	rn, v, ai, kc, err := decodeKeyBindingKey(contentKey)
	if err != nil {
		return false, fmt.Errorf("键位内容键解码 %q: %w", contentKey, err)
	}
	rows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT local_values FROM presence WHERE fork_id=? AND kind=?`,
		targetFork, string(hub.KindKeyBinding))
	if err != nil {
		return false, fmt.Errorf("读目标键位 presence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var lv string
		if err := rows.Scan(&lv); err != nil {
			return false, fmt.Errorf("扫目标键位 presence 行: %w", err)
		}
		var kb hub.KeyBinding
		if err := json.Unmarshal([]byte(lv), &kb); err != nil {
			continue // local_values 单条解析失败不挡路（同键匹配已在外层查全）
		}
		sameRuleset := (kb.RulesetName == nil && rn == nil) ||
			(kb.RulesetName != nil && rn != nil && *kb.RulesetName == *rn)
		sameVariant := (kb.Variant == nil && v == nil) ||
			(kb.Variant != nil && v != nil && *kb.Variant == *v)
		if sameRuleset && sameVariant && kb.ActionInt == ai && kb.KeyCombination != kc {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("遍历目标键位 presence: %w", err)
	}
	return false, nil
}

// decodeKeyBindingKey 键位组合内容键的解析（hub 编码 JSON 数组四段）。
func decodeKeyBindingKey(key string) (rulesetName *string, variant *int, actionInt int, keyCombination string, err error) {
	var parts [4]json.RawMessage
	if err = json.Unmarshal([]byte(key), &parts); err != nil {
		return nil, nil, 0, "", err
	}
	err = json.Unmarshal(parts[0], &rulesetName)
	if err != nil {
		return nil, nil, 0, "", err
	}
	err = json.Unmarshal(parts[1], &variant)
	if err != nil {
		return nil, nil, 0, "", err
	}
	err = json.Unmarshal(parts[2], &actionInt)
	if err != nil {
		return nil, nil, 0, "", err
	}
	err = json.Unmarshal(parts[3], &keyCombination)
	return
}

// beatmapSetAmbiguous 同一谱面身份键下来源端 presence 持有 ≥2 个不同
// 版本 → 歧义（hub 链尾只是先到的值，没有基线裁决不该自动写）。一个
// content_hash 可配多个 identity_key（online: 与 meta: 共存），逐个查。
func (s *realSync) beatmapSetAmbiguous(ctx context.Context, contentKey string, sources []string) (bool, error) {
	if len(sources) == 0 {
		return false, nil
	}
	idRows, err := s.deps.Hub.DB().QueryContext(ctx,
		`SELECT DISTINCT identity_key FROM identity_version WHERE content_hash=?`, contentKey)
	if err != nil {
		return false, fmt.Errorf("读谱面集 %s 的身份键: %w", contentKey, err)
	}
	var identityKeys []string
	for idRows.Next() {
		var ik string
		if err := idRows.Scan(&ik); err != nil {
			idRows.Close()
			return false, fmt.Errorf("扫身份键行: %w", err)
		}
		identityKeys = append(identityKeys, ik)
	}
	if err := idRows.Err(); err != nil {
		idRows.Close()
		return false, fmt.Errorf("遍历身份键: %w", err)
	}
	idRows.Close()
	for _, identityKey := range identityKeys {
		args := make([]any, 0, len(sources)+2)
		args = append(args, identityKey, string(hub.KindBeatmapSet))
		for _, f := range sources {
			args = append(args, f)
		}
		var n int
		if err := s.deps.Hub.DB().QueryRowContext(ctx,
			`SELECT COUNT(DISTINCT p.content_key) FROM presence p
JOIN identity_version iv ON iv.content_hash = p.content_key
WHERE iv.identity_key=? AND p.kind=? AND p.fork_id IN (`+placeholders(len(sources))+`)`,
			args...).Scan(&n); err != nil {
			return false, fmt.Errorf("数身份键 %s 的来源端版本: %w", identityKey, err)
		}
		if n >= 2 {
			return true, nil
		}
	}
	return false, nil
}
