package deploy

// SyncGameIniValues 接线单测（纯文件级）：空决定值 no-op、来源端未注册
// 跳过、目标缺失跳过、守卫拦截、逐键源策略与全局交集。（S12 原子写/
// 白名单复核/备份断言由 inisettings 包单测与 itests 全链覆盖。）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// iniVals 从源端目录的 game.ini 造逐键决定值（来源端 = srcFork）。
func iniVals(t *testing.T, srcFork, srcDir string, kv map[string]string) map[string]IniValue {
	t.Helper()
	if _, err := fixture.WriteGameIni(srcDir, kv); err != nil {
		t.Fatal(err)
	}
	values, _, err := inisettings.CollectFile(filepath.Join(srcDir, "game.ini"), mustProfile(t, srcFork))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]IniValue{}
	for k, v := range values {
		out[k] = IniValue{Value: v, SourceFork: srcFork}
	}
	return out
}

func TestSyncGameIniValuesEmptyNoop(t *testing.T) {
	res, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
	}
	if len(res.Applied) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("空决定值应 no-op，got %+v", res)
	}
}

func TestSyncGameIniValuesUnregisteredSourceSkips(t *testing.T) {
	// 决定值来源端未注册 → 逐键跳过（源侧策略无从取数）。
	res, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		map[string]IniValue{"ShowFpsDisplay": {Value: "true", SourceFork: "ghost"}},
		nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "未注册") {
		t.Fatalf("Skipped = %+v，期望来源端未注册跳过", res.Skipped)
	}
}

func TestSyncGameIniValuesTargetMissingSkips(t *testing.T) {
	vals := iniVals(t, "delta", t.TempDir(), nil)
	// 目标端目录无 game.ini → 跳过计数，绝不凭空创建。
	res, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		vals, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "从未启动过客户端") {
		t.Fatalf("Skipped = %+v，期望目标缺失跳过计数", res.Skipped)
	}
	if len(res.Applied) != 0 {
		t.Errorf("缺失目标不应落地，got %v", res.Applied)
	}
}

func TestSyncGameIniValuesGuardBlocks(t *testing.T) {
	vals := iniVals(t, "delta", t.TempDir(), nil)
	// 逐端守卫（§0-B1/G3）：名单内进程 = 测试进程自身——lazer 的
	// ProcessNames 不含它 → ini 写窗口不拦，继续走到目标缺失跳过
	//（守卫若误拦会返回错误而非跳过计数）。伪造已知 fork 进程名会与
	// 并行包的真实测试互相污染，不在测试里造。
	g := guard.NewGuard([]string{selfProcName()})
	res, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		vals, nil, nil, g)
	if err != nil {
		t.Fatalf("名单外进程不应拦截 lazer 的 ini 写窗口: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "从未启动过客户端") {
		t.Fatalf("Skipped = %+v，期望走到目标缺失跳过（守卫未拦）", res.Skipped)
	}
}

func TestSyncGameIniValuesAppliesWhitelist(t *testing.T) {
	vals := iniVals(t, "delta", t.TempDir(), map[string]string{"ShowFpsDisplay": "true"})
	// 目标 lazer：模板默认值 → ShowFpsDisplay 应被决定值覆盖为 true。
	dstDir := t.TempDir()
	if _, err := fixture.WriteGameIni(dstDir, nil); err != nil {
		t.Fatal(err)
	}
	res, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: dstDir},
		vals, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
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

func TestSyncGameIniValuesUnknownTargetRejected(t *testing.T) {
	// 目标端未注册：零值 Profile 会使 SyncFile 复核排除面全空（红灯键
	// 可写入，S10 违反）——以 Profile 命中为前提，拒绝。
	if _, err := SyncGameIniValues(forks.LibraryRef{ForkID: "ghost", DataDir: t.TempDir()},
		map[string]IniValue{"ShowFpsDisplay": {Value: "true", SourceFork: "lazer"}},
		nil, nil, nil); err == nil {
		t.Fatal("目标端未注册 fork 应拒绝")
	}
}

// TestSyncGameIniValuesPeersIntersection 全局交集（§0-B3/G6）：peers 含
// 未注册 fork → 交集不可判定，所有键保守不写且逐键记带前缀的跳过原因；
// peers 全注册时键集 = 目标允许 ∩ 各 peer 允许（四端共享键名录下与单端
// 一致，ShowFpsDisplay 等共享键照常落地）。
func TestSyncGameIniValuesPeersIntersection(t *testing.T) {
	vals := iniVals(t, "delta", t.TempDir(), map[string]string{"ShowFpsDisplay": "true"})
	dstDir := t.TempDir()
	if _, err := fixture.WriteGameIni(dstDir, nil); err != nil {
		t.Fatal(err)
	}
	// peers 含未注册 fork：交集不可判定 → 全部不写。
	res, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: dstDir},
		vals, []string{"lazer", "ghost"}, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
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
	if prefixed != len(vals) {
		t.Errorf("交集排除跳过计数 = %d，期望 %d（逐键带前缀）", prefixed, len(vals))
	}
	// peers 全注册（含 delta/g0v0/ez2lazer）：共享键照常落地。
	dstDir2 := t.TempDir()
	if _, err := fixture.WriteGameIni(dstDir2, nil); err != nil {
		t.Fatal(err)
	}
	res2, err := SyncGameIniValues(forks.LibraryRef{ForkID: "lazer", DataDir: dstDir2},
		vals, []string{"lazer", "g0v0", "ez2lazer"}, nil, nil)
	if err != nil {
		t.Fatalf("SyncGameIniValues(全注册 peers): %v", err)
	}
	if res2.Applied["ShowFpsDisplay"] != "true" {
		t.Fatalf("Applied = %v，期望含 ShowFpsDisplay=true", res2.Applied)
	}
}
