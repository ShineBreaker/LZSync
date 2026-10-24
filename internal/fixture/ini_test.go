package fixture

// game.ini 夹具单测：模板覆盖、越面键拒绝、落盘位置。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGameIni(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteGameIni(dir, map[string]string{"ShowFpsDisplay": "true"})
	if err != nil {
		t.Fatalf("WriteGameIni: %v", err)
	}
	if want := filepath.Join(dir, "game.ini"); path != want {
		t.Errorf("落盘位置 = %q，期望 %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "ShowFpsDisplay = true\n") {
		t.Errorf("覆盖键未生效:\n%s", got)
	}
	// 未覆盖键与全部判定分支行保持模板原样（红灯/Own/未知/恶意注入/注释）。
	for _, want := range []string{
		"Token = fixture.eyJhbGciOiJSUzI1NiJ9.fake.jwt.payload",
		"Username = fixture_player",
		"ReleaseStream = Lazer",
		"MenuCursorFromSkin = true",
		"CustomApiUrl = https://g0v0.top/api",
		"FutureKey = 42",
		"Weird Key With = Signs = value",
		"Injected=Key",
		";; legacy comment",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("夹具缺模板行 %q", want)
		}
	}
	// 越面键（模板没有的键）拒绝，防夹具静默自造键。
	if _, err := WriteGameIni(t.TempDir(), map[string]string{"NoSuchKey": "x"}); err == nil {
		t.Error("模板外键应报错")
	}
}
