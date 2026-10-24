// Package collect 是收集方向编排（fork → hub，design.md §3.2/§3.3）：
// 只读投影 → hub 单事务入仓（hub.ApplySnapshot）→ 收仓 + 回链。
// 幂等可重跑；游戏运行时整段被守卫拦截。
package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// ErrNotImplemented 是骨架期公共契约残留：编排层施工前 appsvc 等上层以
// 它做穿透断言。collect 已施工、本包不再返回此错误；保留导出直至上层
// 施工完成并移除引用（届时连同本注释一并删除）。
var ErrNotImplemented = errors.New("collect: 契约桩未实现")

// Deps 是 Runner 的协作依赖。
type Deps struct {
	Hub       *hub.Store
	Files     *filestore.Engine
	Snapshots *forks.SnapshotStore
	Guard     *guard.Guard
}

// Stage 收集进度阶段（§3.2 收集三步）。
type Stage string

const (
	StageProject Stage = "project" // 只读投影（对象计数）
	StageApply   Stage = "apply"   // 投影入 hub（实体/快照/tombstone）
	StageAdopt   Stage = "adopt"   // 收仓 + 回链（文件计数）
)

// ProgressFunc 进度回调（appsvc 层桥接为前端事件，M-2）。
type ProgressFunc func(stage Stage, current, total int)

// Report 收集执行报告。
type Report struct {
	Entities        map[hub.EntityKind]int     // 各类型入仓行数
	Skips           []hub.SkipStat             // 投影跳过（原因 + 计数）
	FilesAdopted    int                        // 收仓成功文件数
	FilesSkipped    int                        // 收仓跳过数（能力探测整体跳过等）
	AdoptModeCounts map[filestore.LinkMode]int // 收仓后各链接形态计数（降级统计）
}

// Runner 执行对单个 fork 的完整收集。
type Runner struct {
	deps Deps
}

// NewRunner 创建收集编排器。
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Collect 对 target 库执行收集：
//  1. 守卫拦截（游戏运行中直接报 guard.ErrGameRunning；呈现缓存快照的
//     回退策略属 appsvc，不在本层）——收集只读不惧并发，但 realm 开库
//     在游戏运行时会因进程锁失败（go-realm-options §1.3-7），守卫把
//     这一失败提前为友好提示；
//  2. 动态只读投影（跳过规则 §4.1；跳过项计数入 Report.Skips——不静默吞）；
//  3. hub.ApplySnapshot 单事务入仓（部分失败不产生 tombstone，HB2）；
//  4. 顺手采集结构档案（同版本只留首份，§4.3）；
//  5. 收仓 + 回链（HB4：能力探测 + 首文件试探失败 → 该 fork 整体跳过
//     收仓、文件留原地 + 计数——纯增益操作，跳过只损失磁盘节约）。
//
// 幂等：重跑 = 重写快照 + 补收文件，无部分完成风险（§3.3）。
func (r *Runner) Collect(ctx context.Context, target forks.LibraryRef, prog ProgressFunc) (Report, error) {
	report := Report{}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if target.ForkID == "" || target.RealmPath == "" {
		return report, fmt.Errorf("collect: 目标库未解析（ForkID/RealmPath 为空）")
	}
	if err := r.deps.Guard.EnsureClear(); err != nil {
		return report, err
	}
	lib, err := realmc.Load()
	if err != nil {
		return report, fmt.Errorf("collect: native 库不可用（P-2 降级由上层呈现）: %w", err)
	}

	// ② 只读投影：动态只读打开 + 单读事务内读全表。
	// 开库失败（游戏运行时的进程锁等）如实上抛——上层按「跳过该库 +
	// 提示」呈现（§3.2 收集段）。
	ro, err := lib.OpenDynamicReadOnly(target.RealmPath)
	if err != nil {
		return report, fmt.Errorf("collect: 开库失败（游戏可能正在运行，已跳过该库）: %w", err)
	}
	if err := ro.BeginRead(); err != nil {
		ro.Close()
		return report, fmt.Errorf("collect: 开读事务: %w", err)
	}
	snap, err := project(ro, target, prog)
	// 只读会话（RLM_SCHEMA_MODE_READ_ONLY）的读事务不做显式收尾：
	// commit 与 rollback 都会报 errno=2005「Can't perform transactions on
	// read-only Realms」（实测），Close 即正确结束——fixture 侧同款先例。
	if err != nil {
		ro.Close()
		return report, fmt.Errorf("collect: 投影 %s: %w", target.RealmPath, err)
	}
	if err := ro.Close(); err != nil {
		return report, fmt.Errorf("collect: 关闭只读会话: %w", err)
	}

	// ③ 单事务入 hub（HB2：到这步为止的任何失败都不落任何快照/tombstone）。
	if prog != nil {
		prog(StageApply, 0, len(snap.Collections)+len(snap.BeatmapSets)+len(snap.Skins)+len(snap.Scores))
	}
	applyRes, err := r.deps.Hub.ApplySnapshot(ctx, snap)
	if err != nil {
		return report, fmt.Errorf("collect: 入 hub: %w", err)
	}
	report.Entities = applyRes.EntitiesWritten
	report.Skips = snap.Skips

	// ④ 顺手采集结构档案（采集失败只记日志——坏文件按不存在处理，§4.3；
	// 失败不阻断收集主体）。
	if r.deps.Snapshots != nil {
		if structure, cerr := forks.Capture(lib, target.ForkID, target.RealmPath); cerr == nil {
			if _, serr := r.deps.Snapshots.SaveIfAbsent(structure); serr != nil {
				fmt.Fprintf(os.Stderr, "collect: 结构档案落盘失败（忽略）: %v\n", serr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "collect: 结构档案采集失败（忽略）: %v\n", cerr)
		}
	}

	// ⑤ 收仓 + 回链：files/ 不存在视为「无实体文件」（无文件 fork 的
	// 正常态），扫描失败如实上抛。
	if prog != nil {
		prog(StageAdopt, 0, 0)
	}
	if filesDir, ok := resolveFilesDir(target.RealmPath); ok {
		stats, aerr := r.deps.Files.AdoptDir(filesDir)
		if aerr != nil {
			return report, fmt.Errorf("collect: 收仓 %s: %w", filesDir, aerr)
		}
		report.FilesAdopted = stats.Adopted
		report.FilesSkipped = stats.Failed + stats.Skipped + stats.Aborted
		report.AdoptModeCounts = stats.Modes
	}
	return report, nil
}

// resolveFilesDir 解析 fork 的 files/ 目录（§5.3 三级回退）：库所在目录
// files/ → 所在目录名是 data 时取兄弟 files/（与父目录 files/ 同一表达
// 式，合为一条）→ 不存在返回 false（无实体文件，跳过收仓）。
func resolveFilesDir(realmPath string) (string, bool) {
	dir := filepath.Dir(realmPath)
	for _, cand := range []string{
		filepath.Join(dir, "files"),
		filepath.Join(filepath.Dir(dir), "files"),
	} {
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand, true
		}
	}
	return "", false
}
