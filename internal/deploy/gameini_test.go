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
		"delta", nil, nil, nil)
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
		"delta", store, nil, nil)
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
		"delta", store, nil, nil)
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
	// 自指进程名守卫：测试进程自身（go test）在名单内 → 拦截（S5）。
	g := guard.NewGuard([]string{selfProcName()})
	if _, err := SyncGameIni(forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()},
		"delta", store, nil, g); err == nil {
		t.Fatal("守卫拦截应报错")
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
		"delta", store, nil, nil)
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
