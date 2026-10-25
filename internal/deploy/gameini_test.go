package deploy

// SyncGameIni 接线单测（纯文件级）：快照仓读取、源端缺失跳过、目标缺失
// 跳过、守卫拦截。（S12 原子写/白名单复核/备份断言由 inisettings 包
// 单测与 itests 全链覆盖。）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/inisettings"
)

func mustProfile(t *testing.T, id string) forks.ForkProfile {
	t.Helper()
	p, ok := forks.Profile(id)
	if !ok {
		t.Fatalf("forks.Profile(%s) 缺失", id)
	}
	return p
}

func selfProcName() string { return filepath.Base(os.Args[0]) }

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustStore(t *testing.T) *inisettings.Store {
	t.Helper()
	s, err := inisettings.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSyncGameIniNilStoreSkips(t *testing.T) {
	res, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		"delta", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIni: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "未配置") {
		t.Fatalf("Skipped = %+v，期望未配置跳过", res.Skipped)
	}
}

func TestSyncGameIniSourceSnapshotMissing(t *testing.T) {
	store := mustStore(t)
	res, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		"delta", nil, store, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIni: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "无 game.ini 快照") {
		t.Fatalf("Skipped = %+v，期望源端无快照跳过", res.Skipped)
	}
}

func TestSyncGameIniTargetMissingSkips(t *testing.T) {
	store := mustStore(t)
	// 源端快照在（delta 源），目标端目录无 game.ini。
	dir := t.TempDir()
	srcDir := t.TempDir()
	if _, err := fixture.WriteGameIni(srcDir, nil); err != nil {
		t.Fatal(err)
	}
	values, _, err := inisettings.CollectFile(filepath.Join(srcDir, "game.ini"), mustProfile(t, "delta"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("delta", values, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	res, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: dir},
		"delta", nil, store, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIni: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "从未启动过客户端") {
		t.Fatalf("Skipped = %+v，期望目标缺失跳过计数", res.Skipped)
	}
	if len(res.Applied) != 0 {
		t.Errorf("缺失目标不应落地，got %v", res.Applied)
	}
}

func TestSyncGameIniGuardBlocks(t *testing.T) {
	store := mustStore(t)
	srcDir := t.TempDir()
	if _, err := fixture.WriteGameIni(srcDir, nil); err != nil {
		t.Fatal(err)
	}
	values, _, err := inisettings.CollectFile(filepath.Join(srcDir, "game.ini"), mustProfile(t, "delta"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("delta", values, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// 逐端守卫（§0-B1/G3）：名单内进程 = 测试进程自身——lazer 的
	// ProcessNames 不含它 → ini 写窗口不拦，继续走到目标缺失跳过
	//（守卫若误拦会返回错误而非跳过计数）。伪造已知 fork 进程名会与
	// 并行包的真实测试互相污染，不在测试里造。
	g := guard.NewGuard([]string{selfProcName()})
	res, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		"delta", nil, store, nil, g)
	if err != nil {
		t.Fatalf("名单外进程不应拦截 lazer 的 ini 写窗口: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "从未启动过客户端") {
		t.Fatalf("Skipped = %+v，期望走到目标缺失跳过（守卫未拦）", res.Skipped)
	}
}

func TestSyncGameIniAppliesWhitelist(t *testing.T) {
	store := mustStore(t)
	srcDir := t.TempDir()
	if _, err := fixture.WriteGameIni(srcDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatal(err)
	}
	values, _, err := inisettings.CollectFile(filepath.Join(srcDir, "game.ini"), mustProfile(t, "delta"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("delta", values, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// 目标 lazer：模板默认值 → ShowFpsDisplay 应被 delta 快照覆盖为 true。
	dstDir := t.TempDir()
	if _, err := fixture.WriteGameIni(dstDir, nil); err != nil {
		t.Fatal(err)
	}
	res, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: dstDir},
		"delta", nil, store, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIni: %v", err)
	}
	if res.Applied["ShowFpsDisplay"] != "true" {
		t.Fatalf("Applied = %v，期望含 ShowFpsDisplay=true", res.Applied)
	}
	// delta Own 键与红灯键不在落地与目标文件。
	doc := inisettings.Parse(mustRead(t, filepath.Join(dstDir, "game.ini")))
	if v, ok := doc.Value("Token"); !ok || v != "fixture.eyJhbGciOiJSUzI1NiJ9.fake.jwt.payload" {
		t.Errorf("目标 Token 行应原样保留，got (%q,%v)", v, ok)
	}
	if v, _ := doc.Value("MenuCursorFromSkin"); v != "true" {
		t.Errorf("lazer 目标的 delta Own 键行应原样，got %q", v)
	}
}


func TestSyncGameIniUnknownForkRejected(t *testing.T) {
	store := mustStore(t)
	// 目标端未注册：零值 Profile 会使 SyncFile 复核排除面全空（红灯键
	// 可写入，S10 违反）——以 Profile 命中为前提，拒绝。
	if _, err := SyncGameIni(forks.LibraryRef{ForkID: "ghost", DataDir: t.TempDir()},
		"delta", nil, store, nil, nil); err == nil {
		t.Fatal("目标端未注册 fork 应拒绝")
	}
	// 源端未注册：同步策略（源端 Own/红灯面）无从取数，同样拒绝。
	if _, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		"ghost", nil, store, nil, nil); err == nil {
		t.Fatal("源端未注册 fork 应拒绝")
	}
}

// TestSyncGameIniPeersIntersection 全局交集（§0-B3/G6）：peers 含未注册
// fork → 交集不可判定，所有键保守不写且逐键记带前缀的跳过原因；peers
// 全注册时键集 = 目标允许 ∩ 各 peer 允许（四端共享键名录下与单端一致，
// ShowFpsDisplay 等共享键照常落地）。
func TestSyncGameIniPeersIntersection(t *testing.T) {
	store := mustStore(t)
	srcDir := t.TempDir()
	if _, err := fixture.WriteGameIni(srcDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatal(err)
	}
	values, _, err := inisettings.CollectFile(filepath.Join(srcDir, "game.ini"), mustProfile(t, "delta"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("delta", values, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	dstDir := t.TempDir()
	if _, err := fixture.WriteGameIni(dstDir, nil); err != nil {
		t.Fatal(err)
	}
	// peers 含未注册 fork：交集不可判定 → 全部不写。
	res, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: dstDir},
		"delta", []string{"lazer", "ghost"}, store, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIni: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("含未注册 peer 的交集应全部不写，got %v", res.Applied)
	}
	prefixed := 0
	for _, sk := range res.Skipped {
		if strings.HasPrefix(sk.Reason, IntersectionRejectPrefix) {
			prefixed += sk.Count
		}
	}
	if prefixed != len(values) {
		t.Errorf("交集排除跳过计数 = %d，期望 %d（逐键带前缀）", prefixed, len(values))
	}
	// peers 全注册（含 delta/g0v0/ez2lazer）：共享键照常落地。
	dstDir2 := t.TempDir()
	if _, err := fixture.WriteGameIni(dstDir2, nil); err != nil {
		t.Fatal(err)
	}
	res2, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: dstDir2},
		"delta", []string{"lazer", "g0v0", "ez2lazer"}, store, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIni(全注册 peers): %v", err)
	}
	if res2.Applied["ShowFpsDisplay"] != "true" {
		t.Fatalf("Applied = %v，期望含 ShowFpsDisplay=true", res2.Applied)
	}
}
