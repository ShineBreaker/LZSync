package deploy

// game.ini 白名单键投放接线（design.md §3.1 inisettings / §5.1-M3、S12、
// §0.3 逐键基线三方判定）。
// 独立编排入口而非并入 Plan：PlanItem.Kind 是 hub.EntityKind（定义于
// hub 包，本块禁改），ini 键同步是文件级原子操作、无 realm 批事务语义
//（S12 的备份→临时→rename 即其原子单位）；appsvc 组合调用 Deploy 与
// SyncGameIniValues 完成一次投放。

import (
	"fmt"
	"path/filepath"

	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/inisettings"
)

// IniValue 是单个 ini 键的裁决决定值（§0.3：每个键的决定值来源端可以
// 不同——源端策略按决定值自己的来源端判定，不再假设单一 source）。
type IniValue struct {
	Value      string // 决定值（写入键值原文）
	SourceFork string // 决定值来源端（SyncableKeys 的源侧策略输入）
}

// GameIniResult 是 SyncGameIniValues 的执行摘要（SyncResult 的编排层
// 形态：补未配置/未注册两类跳过与报错）。
type GameIniResult struct {
	Applied    map[string]string
	Skipped    []inisettings.SkipStat
	BackupPath string
}

// SyncGameIniValues 把裁决好的逐键值写到 target 端 game.ini：
//   - 逐端守卫拦截（§0-B1/G3：只检测目标 fork 的进程）；
//   - 逐键全局交集（§0-B3/G6）：键须同时被其来源端与每个 peer 端的
//     SyncableKeys 接受——任一端不收的键所有端都不写（一轮同步的全部
//     目标写同一份 ini 形态），被排除键逐个计跳过（原因带
//     IntersectionRejectPrefix）。peers 须含目标端自身；
//   - 目标 game.ini 不存在 → 跳过计数（从未启动过客户端，绝不凭空
//     创建）；
//   - S12 原子写与白名单/红灯复核由 inisettings.SyncFile 承担（源侧
//     策略已逐键裁决完毕，执行期复核以目标策略为唯一防线）。
//
// values 为空 = 本轮无决定键值，no-op（不算写入、不备份）。
func SyncGameIniValues(target forks.LibraryRef, values map[string]IniValue, peers []string,
	backups *guard.Backups, g *guard.Guard) (GameIniResult, error) {
	res := GameIniResult{Applied: map[string]string{}}
	skip := func(reason string) {
		res.Skipped = append(res.Skipped, inisettings.SkipStat{Reason: reason, Count: 1})
	}
	if len(values) == 0 {
		return res, nil
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
	// 全局交集（§0-B3/G6）：写入键集 = 逐键来源端可出 ∩ 每个 peer 端可写。
	// 任一 peer 未注册 → 该键交集不可判定，保守不写（宁少勿错）。
	filtered := make(map[string]string, len(values))
	for k, iv := range values {
		srcProf, srcOK := forks.Profile(iv.SourceFork)
		if !srcOK {
			skip("键 " + k + " 的来源端 " + iv.SourceFork + " 未注册（策略无从取数，不写）")
			continue
		}
		rejected := false
		for _, p := range peers {
			pp, ok := forks.Profile(p)
			if !ok {
				skip(IntersectionRejectPrefix + p + " 不支持 — 该 fork 未注册，交集不可判定")
				rejected = true
				break
			}
			if !inisettings.SyncableKeys(srcProf, pp)[k] {
				skip(IntersectionRejectPrefix + p + " 不支持 — " + k)
				rejected = true
				break
			}
		}
		if !rejected {
			filtered[k] = iv.Value
		}
	}
	if len(filtered) == 0 {
		return res, nil
	}
	// 源侧策略已逐键裁决（filtered 只含全端交集通过的键）：执行期以目标
	// 策略单端复核（source=target 等价于只做目标侧 own/红灯检查）。
	sync, err := inisettings.SyncFile(tgtProf, tgtProf,
		filepath.Join(target.DataDir, inisettings.GameIniName), filtered, backups)
	if err != nil {
		return res, fmt.Errorf("deploy: 写 %s 端 game.ini: %w", target.ForkID, err)
	}
	res.Applied = sync.Applied
	res.Skipped = append(res.Skipped, sync.Skipped...)
	res.BackupPath = sync.BackupPath
	return res, nil
}
