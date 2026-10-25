// Package main 真库全链测试驱动：不经 UI，直连 appsvc 真实后端走完整
// 功能面（扫描 → 收集 → 对比预演 → 计划 → 投放 → 收敛验证）。装配复刻
// main.go openDeps 的依赖布局（§3.4）。写目标库的投放必须显式传
// -confirm-write；只读阶段只写 LZSync 自有数据目录。
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lzsync/internal/appsvc"
	"lzsync/internal/deploy"
	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/inisettings"
)

func main() {
	home, _ := os.UserHomeDir()
	var (
		dataDir  = flag.String("data-dir", filepath.Join(home, ".local/share/lzsync-realdbtest"), "LZSync 自有数据目录（hub/store/snapshots/backups）")
		dataRoot = flag.String("data-root", filepath.Join(home, ".local/share"), "fork 数据根")
		phase    = flag.String("phase", "full", "scan|collect|compare|deploy|verify|full")
		forkList = flag.String("forks", "lazer,delta,g0v0", "收集顺序（逗号分隔）")
		deployTo = flag.String("deploy-to", "delta", "投放目标 fork")
		maxSets  = flag.Int("max-sets", 3, "计划中谱面集上限")
		maxSkins = flag.Int("max-skins", 1, "计划中皮肤上限")
		maxScore = flag.Int("max-scores", 3, "计划中成绩上限")
		confirm  = flag.Bool("confirm-write", false, "允许执行投放（写目标库）")
	)
	flag.Parse()

	b, closeHub, err := openBackend(*dataDir, *dataRoot)
	must(err)
	defer closeHub()

	switch *phase {
	case "scan":
		doScan(b)
	case "collect":
		doCollect(b, strings.Split(*forkList, ","))
	case "compare":
		doCompare(b, *deployTo, strings.Split(*forkList, ","))
	case "deploy":
		if !*confirm {
			must(errors.New("deploy 阶段需要 -confirm-write"))
		}
		doPlanDeploy(b, *deployTo, *maxSets, *maxSkins, *maxScore)
		doVerify(b, *deployTo)
	case "restore":
		must(doRestore(b, *deployTo, flag.Arg(0)))
	case "verify":
		doVerify(b, *deployTo)
	case "full":
		doScan(b)
		doCollect(b, strings.Split(*forkList, ","))
		doCompare(b, *deployTo, strings.Split(*forkList, ","))
		if *confirm {
			doPlanDeploy(b, *deployTo, *maxSets, *maxSkins, *maxScore)
			doVerify(b, *deployTo)
		} else {
			fmt.Println("[plan] 未传 -confirm-write，跳过投放（只读链路已完成）")
		}
	default:
		must(fmt.Errorf("未知 phase %q", *phase))
	}
}

// openBackend 复刻 main.go openDeps 的装配（LZSYNC_DATA_DIR 语义改为
// -data-dir 显式传入）。
func openBackend(dataDir, dataRoot string) (*appsvc.Backend, func(), error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	h, err := hub.Open(filepath.Join(dataDir, "hub.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("打开 hub: %w", err)
	}
	store, err := filestore.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		return nil, nil, fmt.Errorf("打开内容寻址仓: %w", err)
	}
	snaps, err := forks.OpenSnapshotStore(filepath.Join(dataDir, "snapshots"))
	if err != nil {
		return nil, nil, fmt.Errorf("打开结构档案目录: %w", err)
	}
	backups, err := guard.OpenBackups(filepath.Join(dataDir, "backups"))
	if err != nil {
		return nil, nil, fmt.Errorf("打开备份目录: %w", err)
	}
	gameIni, err := inisettings.OpenStore(filepath.Join(dataDir, "gameini"))
	if err != nil {
		return nil, nil, fmt.Errorf("打开 game.ini 快照仓: %w", err)
	}
	b, err := appsvc.NewRealBackend(appsvc.Deps{
		Hub:       h,
		Files:     filestore.NewEngine(store),
		Snapshots: snaps,
		Backups:   backups,
		Guard:     guard.NewGuard(builtinProcessNames()),
		DataRoot:  dataRoot,
		DataDir:   dataDir,
		GameIni:   gameIni,
	}, func(ev appsvc.ProgressEvent) {
		if ev.Total > 0 && ev.Current%100 == 0 && ev.Current != ev.Total {
			fmt.Printf("  [进度] %s %s %d/%d %s\n", ev.Service, ev.Stage, ev.Current, ev.Total, ev.Message)
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return b, func() { _ = h.Close() }, nil
}

func builtinProcessNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, p := range forks.All() {
		for _, n := range p.ProcessNames {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	return names
}

func doScan(b *appsvc.Backend) {
	fmt.Println("===== 1) 库扫描 =====")
	refs, err := b.Library.Scan()
	must(err)
	for _, r := range refs {
		fmt.Printf("[scan] %-8s realm=%s\n        ver=%d upstream=%d rev=%d source=%s\n",
			r.ForkID, r.RealmPath, r.DiskVersion, r.UpstreamVersion, r.ForkRev, r.VersionSource)
	}
	for _, c := range b.Library.List() {
		fmt.Printf("[card] %-8s %s realm=%d files=%d/%d running=%v(%v)\n",
			c.ForkID, c.VersionLabel, c.RealmSize, c.FilesSize, c.FilesCount, c.ProcessRunning, c.RunningNames)
	}
}

func doCollect(b *appsvc.Backend, forkIDs []string) {
	fmt.Println("===== 2) 收集 =====")
	for _, id := range forkIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		t0 := time.Now()
		rep, err := b.Sync.Collect(id)
		if err != nil {
			fmt.Printf("[collect:%s] 失败：%v\n", id, err)
			os.Exit(1)
		}
		var kinds []string
		for k, n := range rep.Entities {
			kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
		}
		sort.Strings(kinds)
		var skips []string
		for _, s := range rep.Skips {
			skips = append(skips, fmt.Sprintf("%s×%d", s.Reason, s.Count))
		}
		var modes []string
		for m, n := range rep.AdoptModeCounts {
			modes = append(modes, fmt.Sprintf("%s=%d", m, n))
		}
		sort.Strings(modes)
		fmt.Printf("[collect:%s] 耗时=%s 实体{%s} 跳过[%s] 文件收仓=%d(跳过%d) 形态{%s} ini键=%d\n",
			id, time.Since(t0).Round(time.Second), strings.Join(kinds, " "),
			strings.Join(skips, "; "), rep.FilesAdopted, rep.FilesSkipped,
			strings.Join(modes, " "), rep.IniKeysCollected)
	}
}

// rowCounts 按（类型,分类）聚合计数。
func rowCounts(rows []appsvc.DiffRow) map[string]int {
	m := map[string]int{}
	for _, r := range rows {
		m[string(r.Kind)+"|"+string(r.Category)]++
	}
	return m
}

func printRows(tag string, rows []appsvc.DiffRow) {
	m := rowCounts(rows)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("[%s] 共 %d 行：\n", tag, len(rows))
	for _, k := range keys {
		fmt.Printf("    %-40s %d\n", k, m[k])
	}
}

func doCompare(b *appsvc.Backend, target string, forkIDs []string) {
	fmt.Println("===== 3) 对比预演 =====")
	rows, err := b.Compare.PreviewDeploy(target)
	must(err)
	printRows("preview→"+target, rows)
	for i, r := range rows {
		if i >= 8 {
			fmt.Printf("    ……（其余省略）\n")
			break
		}
		fmt.Printf("    样例 %-11s %-18s %s%s\n", r.Kind, r.Category, r.Summary, skipNote(r))
	}
	del, err := b.Compare.DeletedEntries(target)
	must(err)
	fmt.Printf("[tombstone→%s] %d 行\n", target, len(del))
	for _, f := range forkIDs {
		f = strings.TrimSpace(f)
		if f == "" || f == target {
			continue
		}
		if _, err := b.Compare.CompareForks(f, target); err != nil {
			fmt.Printf("[fork↔fork %s→%s] 未接通：%v\n", f, target, err)
		}
	}
	st, err := b.Settings.Get()
	must(err)
	fmt.Printf("[settings] mode=%s nativeLib=%v symlink=%v hardlink=%v guard=%v 备份数=%d\n  hub=%s\n",
		st.Mode, st.NativeLibOK, st.LinkSymlink, st.LinkHardlink, st.GuardNames, st.BackupsCount, st.HubDBPath)
}

func skipNote(r appsvc.DiffRow) string {
	if r.SkipReason != "" {
		return "（" + r.SkipReason + "）"
	}
	return ""
}

// pickSelection 从预演行选计划勾选集：收藏夹全量 + 各类型限量 create 项。
func pickSelection(rows []appsvc.DiffRow, maxSets, maxSkins, maxScores int) []appsvc.Selection {
	var sel []appsvc.Selection
	caps := map[hub.EntityKind]int{
		hub.KindBeatmapSet: maxSets,
		hub.KindSkin:       maxSkins,
		hub.KindScore:      maxScores,
	}
	for _, r := range rows {
		if !r.DefaultChecked {
			continue
		}
		switch r.Kind {
		case hub.KindCollection:
			sel = append(sel, appsvc.Selection{Kind: r.Kind, ContentKey: r.ContentKey})
		case hub.KindBeatmapSet, hub.KindSkin, hub.KindScore:
			if caps[r.Kind] > 0 {
				caps[r.Kind]--
				sel = append(sel, appsvc.Selection{Kind: r.Kind, ContentKey: r.ContentKey})
			}
		}
	}
	return sel
}

func doPlanDeploy(b *appsvc.Backend, target string, maxSets, maxSkins, maxScores int) {
	fmt.Println("===== 4) 计划与投放（写目标库）=====")
	rows, err := b.Compare.PreviewDeploy(target)
	must(err)
	sel := pickSelection(rows, maxSets, maxSkins, maxScores)
	fmt.Printf("[select] 勾选 %d 项\n", len(sel))
	plan, err := b.Compare.BuildPlan(target, sel)
	must(err)
	for _, it := range plan.Items {
		fmt.Printf("  [plan] %-15s %-14s %s%s\n", it.Action, it.Kind, it.Summary, skipNotePlan(it))
	}
	t0 := time.Now()
	rep, err := b.Sync.DeployCurrentPlan()
	if err != nil {
		fmt.Printf("[deploy] 失败：%v\n", err)
		os.Exit(1)
	}
	cnt := map[string]int{}
	for _, a := range rep.Applied {
		cnt[string(a.Kind)+"|"+string(a.Action)]++
	}
	var applied []string
	for k, n := range cnt {
		applied = append(applied, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(applied)
	fmt.Printf("[deploy] 耗时=%s applied{%s} 备份=%s\n", time.Since(t0).Round(time.Second),
		strings.Join(applied, " "), rep.BackupPath)
	for _, s := range rep.Skipped {
		fmt.Printf("    skip %s×%d\n", s.Reason, s.Count)
	}
	for _, f := range rep.FailedBatches {
		fmt.Printf("    FAIL 批#%d：%s（%d 项）\n", f.BatchIndex, f.Reason, len(f.Items))
	}
	for _, r := range rep.Repointed {
		fmt.Printf("    repoint %s %s：%s\n", r.Item.Kind, r.Item.ContentKey, r.Reason)
	}
	for _, g := range rep.GimmickWarnings {
		fmt.Printf("    gimmick告警 %s %s %s\n", g.Kind, g.ContentKey, g.Summary)
	}
	for _, n := range rep.NotRun {
		fmt.Printf("    notRun %s %s\n", n.Kind, n.ContentKey)
	}
}

func skipNotePlan(it deploy.PlanItem) string {
	if it.SkipReason != "" {
		return "（" + it.SkipReason + "）"
	}
	return ""
}

func doVerify(b *appsvc.Backend, target string) {
	fmt.Println("===== 5) 收敛验证（重收集 + 复预演）=====")
	rep, err := b.Sync.Collect(target)
	must(err)
	var kinds []string
	for k, n := range rep.Entities {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(kinds)
	fmt.Printf("[recollect:%s] 实体{%s}\n", target, strings.Join(kinds, " "))
	rows, err := b.Compare.PreviewDeploy(target)
	must(err)
	printRows("复预演→"+target, rows)
	plan, err := b.Compare.CurrentPlan()
	if err != nil {
		return
	}
	applied := map[string]bool{}
	for _, it := range plan.Items {
		if it.Action == deploy.ActionSkip {
			continue
		}
		applied[string(it.Kind)+"\x00"+it.ContentKey] = true
	}
	var remain []string
	for _, r := range rows {
		if applied[string(r.Kind)+"\x00"+r.ContentKey] {
			remain = append(remain, string(r.Kind)+"|"+r.ContentKey)
		}
	}
	if len(remain) == 0 {
		fmt.Println("[converge] 已投放键全部收敛（预演不再出现）")
	} else {
		for _, k := range remain {
			fmt.Printf("    未收敛：%s\n", k)
		}
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "realdbtool: %v\n", err)
		os.Exit(1)
	}
}

// doRestore 从备份还原目标库（SyncService.RestoreBackup，§6.3 恢复入口）。
// 用法：-phase restore -deploy-to delta -- <备份路径>。还原后重收集报告
// 实体数，供与还原前对照。
func doRestore(b *appsvc.Backend, target, backupPath string) error {
	fmt.Printf("===== 6) 从备份还原 %s ← %s =====\n", target, backupPath)
	if backupPath == "" {
		return errors.New("restore 需要位置参数 <备份路径>（deploy 报告的 BackupPath）")
	}
	if err := b.Sync.RestoreBackup(target, backupPath); err != nil {
		return err
	}
	rep, err := b.Sync.Collect(target)
	if err != nil {
		return fmt.Errorf("还原后重收集: %w", err)
	}
	var kinds []string
	for k, n := range rep.Entities {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(kinds)
	fmt.Printf("[restore:%s] 还原完成，重收集实体{%s}\n", target, strings.Join(kinds, " "))
	return nil
}
