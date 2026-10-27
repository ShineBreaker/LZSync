// LZSync 应用装配（design.md §3.1/§3.4/§1.4）：解析平台数据目录（P-3）→
// 装配 hub/store/snapshots/backups/guard 协作依赖 → 构造 appsvc 五服务并
// BindAs 暴露给前端（M-1）→ 真实/Mock 后端切换（M-4：native 库不可用或
// LZSYNC_MOCK=1 时降级 Mock，P-2 不闪退）→ 同步进度经 mygo 事件推送（M-2）。
package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"

	"github.com/egoist/mygo"

	"lzsync/internal/appsvc"
	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/inisettings"
)

//go:embed resources/icon.png
var iconPNG []byte

// SyncProgress 同步长任务进度事件（M-2：收集/投放执行不阻塞 UI，批级
// 进度实时推送，main.go:36 的 Tick 先例的同款机制）。
var SyncProgress = mygo.NewEvent[appsvc.ProgressEvent]("sync-progress")

// 主窗口指针：进度事件按窗口定向投递（WhenReady 前的早期事件安全丢弃）。
var mainWindow atomic.Pointer[mygo.Window]

func main() {
	backend := assembleBackend()

	mygo.BindAs("LibraryService", backend.Library)
	mygo.BindAs("CompareService", backend.Compare)
	mygo.BindAs("SyncService", backend.Sync)
	mygo.BindAs("ExportService", backend.Export)
	mygo.BindAs("SettingsService", backend.Settings)
	mygo.BindAs("DialogService", DialogService{})

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "LZSync",
			Width:           1080,
			Height:          720,
			MinWidth:        720,
			MinHeight:       480,
			BackgroundColor: "light-dark(#f6f7f9, #0f1115)", // src/style.css 的 --bg
			StateKey:        "main",
			URL:             "/",
		})
		if err := win.SetIcon(iconPNG); err != nil {
			log.Printf("设置窗口图标失败: %v", err)
		}
		mainWindow.Store(win)
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// assembleBackend 装配依赖并按可用性选择真实/Mock 后端（M-4）。
func assembleBackend() *appsvc.Backend {
	prog := func(ev appsvc.ProgressEvent) {
		if w := mainWindow.Load(); w != nil {
			if err := SyncProgress.Emit(w, ev); err != nil {
				log.Printf("进度事件投递失败: %v", err)
			}
		}
	}

	if os.Getenv("LZSYNC_MOCK") != "" {
		return appsvc.NewMockBackend("mock", "LZSYNC_MOCK=1 强制 Mock 后端（M-4 开发模式）", prog)
	}

	deps, err := openDeps()
	if err != nil {
		return appsvc.NewMockBackend("mock-fallback",
			fmt.Sprintf("数据目录装配失败，已降级 Mock：%s", err), prog)
	}
	backend, err := appsvc.NewRealBackend(deps, prog)
	if err != nil {
		return appsvc.NewMockBackend("mock-fallback",
			fmt.Sprintf("真实后端不可用（P-2 降级）：%s", err), prog)
	}
	return backend
}

// openDeps 解析平台数据目录（P-3）并按 §3.4 hub 目录布局装配协作依赖。
func openDeps() (appsvc.Deps, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return appsvc.Deps{}, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return appsvc.Deps{}, fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	h, err := hub.Open(filepath.Join(dataDir, "hub.db"))
	if err != nil {
		return appsvc.Deps{}, fmt.Errorf("打开 hub: %w", err)
	}
	store, err := filestore.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		return appsvc.Deps{}, fmt.Errorf("打开内容寻址仓: %w", err)
	}
	snaps, err := forks.OpenSnapshotStore(filepath.Join(dataDir, "snapshots"))
	if err != nil {
		return appsvc.Deps{}, fmt.Errorf("打开结构档案目录: %w", err)
	}
	backupsDir := filepath.Join(dataDir, "backups")
	backups, err := guard.OpenBackups(backupsDir)
	if err != nil {
		return appsvc.Deps{}, fmt.Errorf("打开备份目录: %w", err)
	}
	// game.ini 快照仓（§5.1-M3 收集面）：开仓失败不拦主链路（ini 面是
	// 辅助面，collect 报 IniKeysCollected=0 如实呈现），只留日志。
	gameIni, giErr := inisettings.OpenStore(filepath.Join(dataDir, "gameini"))
	if giErr != nil {
		log.Printf("game.ini 快照仓不可用（本会话不含 ini 收集面）: %v", giErr)
		gameIni = nil
	}
	return appsvc.Deps{
		Hub:       h,
		Files:     filestore.NewEngine(store),
		Snapshots: snaps,
		Backups:   backups,
		Guard:     guard.NewGuard(builtinProcessNames()),
		DataRoot:  resolveDataRoot(),
		DataDir:   dataDir,
		GameIni:   gameIni,
	}, nil
}

// resolveDataDir 解析 LZSync 自有数据目录（§3.4；LZSYNC_DATA_DIR 覆盖，
// 供开发与测试）。
func resolveDataDir() (string, error) {
	if dir := os.Getenv("LZSYNC_DATA_DIR"); dir != "" {
		return dir, nil
	}
	switch runtime.GOOS {
	case "windows":
		base, err := os.UserConfigDir() // %AppData%
		if err != nil {
			return "", fmt.Errorf("解析 %%AppData%%: %w", err)
		}
		return filepath.Join(base, "lzsync"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析家目录: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "lzsync"), nil
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("解析家目录: %w", err)
			}
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "lzsync"), nil
	}
}

// resolveDataRoot 解析 fork 数据根（P-3：与游戏数据目录同规则）。
func resolveDataRoot() string {
	if root := os.Getenv("LZSYNC_DATA_ROOT"); root != "" {
		return root
	}
	switch runtime.GOOS {
	case "windows":
		if base, err := os.UserConfigDir(); err == nil {
			return base
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support")
		}
	default:
		if base := os.Getenv("XDG_DATA_HOME"); base != "" {
			return base
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share")
		}
	}
	return "."
}

// builtinProcessNames 汇总四端 ForkProfile 的内置守卫名单（§6.4 用户可
// 扩展；delta 进程名未记录 T2，待运行时枚举补充）。
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
	sort.Strings(names)
	return names
}
