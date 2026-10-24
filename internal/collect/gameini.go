package collect

// game.ini 白名单键收集接线（design.md §3.1 inisettings / §5.1-M3）：
// 源端 game.ini 的白名单键值 → inisettings 快照仓（hub 目录自有文件，
// S10 红灯键在 CollectFile 内按 forks 注册表策略排除）。文件级操作，
// 不参与 realm 投影事务——调用序保证只在 ApplySnapshot 成功后执行
//（投影失败时 ini 快照同样不落）。

import (
	"path/filepath"
	"time"

	"lzsync/internal/forks"
	"lzsync/internal/inisettings"
)

// collectGameIni 读源端 game.ini 白名单键入快照仓。返回收集键数；
// 源端无 game.ini（从未启动过客户端）返回 -1（调用方按正常态计数）。
func collectGameIni(ref forks.LibraryRef, store *inisettings.Store) (int, error) {
	prof, _ := forks.Profile(ref.ForkID) // 未注册 fork 的零值策略自然放行
	values, found, err := inisettings.CollectFile(gameIniPath(ref), prof)
	if err != nil {
		return 0, err
	}
	if !found {
		return -1, nil
	}
	if err := store.Save(ref.ForkID, values, time.Now().UTC()); err != nil {
		return 0, err
	}
	return len(values), nil
}

func gameIniPath(ref forks.LibraryRef) string {
	return filepath.Join(ref.DataDir, inisettings.GameIniName)
}
