package deploy

// game.ini 白名单键投放接线（design.md §3.1 inisettings / §5.1-M3、S12）。
// 独立编排入口而非并入 Plan：PlanItem.Kind 是 hub.EntityKind（定义于
// hub 包，本块禁改），ini 键同步是文件级原子操作、无 realm 批事务语义
//（S12 的备份→临时→rename 即其原子单位）；appsvc 组合调用 Deploy 与
// SyncGameIni 完成一次投放。

import (
	"fmt"
	"path/filepath"

	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/inisettings"
)

// GameIniResult 是 SyncGameIni 的执行摘要（SyncResult 的编排层形态：
// 补源端快照缺失与未配置两类的跳过计数）。
type GameIniResult struct {
	Applied    map[string]string
	Skipped    []inisettings.SkipStat
	BackupPath string
}

// SyncGameIni 把快照仓中 sourceFork 的 game.ini 白名单键 canonical 值
// 写到 target 端 game.ini：
//   - 逐端守卫拦截（§0-B1/G3：只检测目标 fork 的进程）；
//   - 源端无快照（从未收集过 / 源端从未启动过客户端）→ 跳过计数；
//   - 目标 game.ini 不存在 → 跳过计数（从未启动过客户端，绝不凭空
//     创建）；
//   - S12 原子写与白名单/红灯复核由 inisettings.SyncFile 承担。
//
// peers 是全局交集端清单（§0-B3/G6）：写入键集 = 目标端策略允许 ∩ 每个
// peer 端策略允许——任一端不收的键所有端都不写（一轮同步的全部目标写
// 同一份 ini 形态）。被交集排除的键逐键计跳过（原因带
// IntersectionRejectPrefix）。peers 为空 = 仅目标端（旧行为）。
//
// store 为 nil（未配置 ini 面）按跳过计数返回，不报错。
func SyncGameIni(target forks.LibraryRef, sourceFork string, peers []string,
	store *inisettings.Store, backups *guard.Backups, g *guard.Guard) (GameIniResult, error) {
	res := GameIniResult{Applied: map[string]string{}}
	skip := func(reason string) {
		res.Skipped = append(res.Skipped, inisettings.SkipStat{Reason: reason, Count: 1})
	}
	if store == nil {
		skip("game.ini 快照仓未配置（本次不含 ini 面）")
		return res, nil
	}
	// 未注册 fork 拒绝（收/写两侧都以 Profile 命中为前提，先于快照
	// 读取的参数级校验）：零值 Profile 使 SyncFile 复核的排除面全空
	//（红灯/专有键可写入，S10 违反）——源端策略（Own/红灯面）同样
	// 是 SyncableKeys 的输入。
	srcProf, srcOK := forks.Profile(sourceFork)
	if !srcOK {
		return res, fmt.Errorf("deploy: 源端 fork %q 未注册（game.ini 同步策略无从取数，拒绝）", sourceFork)
	}
	tgtProf, tgtOK := forks.Profile(target.ForkID)
	if !tgtOK {
		return res, fmt.Errorf("deploy: 目标 fork %q 未注册（game.ini 同步策略无从取数，拒绝）", target.ForkID)
	}
	if g != nil {
		if err := g.EnsureClearFork(target.ForkID); err != nil {
			return res, err
		}
	}
	values, _, ok, err := store.Load(sourceFork)
	if err != nil {
		return res, fmt.Errorf("deploy: 读 %s 的 game.ini 快照: %w", sourceFork, err)
	}
	if !ok {
		skip("源端 " + sourceFork + " 无 game.ini 快照（未收集或源端从未启动过客户端）")
		return res, nil
	}
	// 全局交集（§0-B3/G6）：写入键集 = 目标端可写 ∩ 每个 peer 端可写。
	// 交集外的键所有目标都不写（含本轮因运行中跳过的端）。peer 未注册
	// → 交集不可判定，保守全部不写（宁少勿错）。
	filtered := make(map[string]string, len(values))
	for k, v := range values {
		filtered[k] = v
	}
	for _, p := range peers {
		if p == target.ForkID {
			continue // 目标端自身的键集已由 SyncFile 的策略复核承担
		}
		pp, ok := forks.Profile(p)
		if !ok {
			for k := range filtered {
				delete(filtered, k)
				skip(IntersectionRejectPrefix + p + " 不支持 — 该 fork 未注册，交集不可判定")
			}
			continue
		}
		allowed := inisettings.SyncableKeys(srcProf, pp)
		for k := range filtered {
			if !allowed[k] {
				delete(filtered, k)
				skip(IntersectionRejectPrefix + p + " 不支持 — " + k)
			}
		}
	}
	sync, err := inisettings.SyncFile(srcProf, tgtProf,
		filepath.Join(target.DataDir, inisettings.GameIniName), filtered, backups)
	if err != nil {
		return res, fmt.Errorf("deploy: 写 %s 端 game.ini: %w", target.ForkID, err)
	}
	res.Applied = sync.Applied
	res.Skipped = append(res.Skipped, sync.Skipped...)
	res.BackupPath = sync.BackupPath
	return res, nil
}
