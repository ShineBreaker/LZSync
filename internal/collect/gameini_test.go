package collect

// game.ini 收集接线单测（纯文件级）：白名单键入快照仓、红灯/Own/未知/
// 恶意键排除、源端缺失的正常态。

import (
	"testing"

	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/inisettings"
)

func TestCollectGameIni(t *testing.T) {
	dir := t.TempDir()
	if _, err := fixture.WriteGameIni(dir, nil); err != nil {
		t.Fatal(err)
	}
	store, err := inisettings.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := forks.LibraryRef{ForkID: "lazer", DataDir: dir}

	n, err := collectGameIni(ref, store)
	if err != nil {
		t.Fatalf("collectGameIni: %v", err)
	}
	if n == 0 {
		t.Fatal("应收集到非零键数")
	}
	values, _, ok, err := store.Load("lazer")
	if err != nil || !ok {
		t.Fatalf("Load = ok:%v err:%v", ok, err)
	}
	// 红灯/Own/未知/恶意注入键绝不入快照。
	for _, k := range []string{"Token", "Username", "ReleaseStream",
		"MenuCursorFromSkin", "CustomApiUrl", "FutureKey", "Weird Key With", "Injected"} {
		if _, hit := values[k]; hit {
			t.Errorf("快照混入非白名单键 %q", k)
		}
	}
	if values["ShowStoryboard"] != "true" || values["Ruleset"] != "osu" {
		t.Errorf("白名单键值不符: %v", values)
	}
	if len(values) != n {
		t.Errorf("快照键数 %d ≠ 返回值 %d", len(values), n)
	}
}

func TestCollectGameIniSourceMissing(t *testing.T) {
	store, err := inisettings.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := forks.LibraryRef{ForkID: "lazer", DataDir: t.TempDir()}
	n, err := collectGameIni(ref, store)
	if err != nil {
		t.Fatalf("collectGameIni: %v", err)
	}
	if n != -1 {
		t.Fatalf("缺失源返回 %d，期望 -1", n)
	}
	if _, _, ok, _ := store.Load("lazer"); ok {
		t.Error("缺失源不应落快照")
	}
}

func TestCollectGameIniDeltaOwnExcluded(t *testing.T) {
	// delta 源：MenuCursorFromSkin 属 delta Own → 不进收集面。
	dir := t.TempDir()
	if _, err := fixture.WriteGameIni(dir, nil); err != nil {
		t.Fatal(err)
	}
	store, err := inisettings.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collectGameIni(forks.LibraryRef{ForkID: "delta", DataDir: dir}, store); err != nil {
		t.Fatal(err)
	}
	values, _, _, _ := store.Load("delta")
	if _, hit := values["MenuCursorFromSkin"]; hit {
		t.Error("delta Own 键不应被 delta 源收集")
	}
}


func TestCollectGameIniUnknownForkRejected(t *testing.T) {
	// 未注册 fork 的零值 Profile 使 CollectableKeys 排除面全空——红灯
	// 键会原样入快照（S10 违反）；收集以 Profile 命中为前提，拒绝而非
	// 降级放行。
	store, err := inisettings.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collectGameIni(forks.LibraryRef{ForkID: "ghost", DataDir: t.TempDir()}, store); err == nil {
		t.Fatal("未注册 fork 应拒绝收集")
	}
	if _, _, ok, _ := store.Load("ghost"); ok {
		t.Error("拒绝的收集不应落任何快照")
	}
}
