package itests

// M3 game.ini 白名单键全链验收（design.md §5.1-M3-4 / §7-M3-3、S10/S12）：
// 源端（lazer）game.ini 夹具 → collect 收白名单键入快照仓 → deploy
// .SyncGameIni 写目标端（delta）game.ini。逐条断言：白名单外键（红灯/
// Own/未知/恶意注入行）零触碰且逐字节不变、备份存在且等于写前原件、
// 幂等重跑 no-op、目标端从未启动过客户端（无 game.ini）跳过计数。

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/inisettings"
)

// iniSkip 按原因子串取 ini 跳过计数（不存在返回 -1）。
func iniSkip(skips []inisettings.SkipStat, sub string) int {
	n := 0
	found := false
	for _, s := range skips {
		if strings.Contains(s.Reason, sub) {
			n += s.Count
			found = true
		}
	}
	if !found {
		return -1
	}
	return n
}

// iniValsFromStore 快照仓 → 逐键决定值（来源端 = srcFork）。全链测试的
// 投放面输入——生产路径的逐键裁决在 appsvc 判定引擎（§0.3）。
func iniValsFromStore(t *testing.T, store *inisettings.Store, srcFork string) map[string]deploy.IniValue {
	t.Helper()
	vals, _, ok, err := store.Load(srcFork)
	if err != nil || !ok {
		t.Fatalf("快照 Load(%s) = ok:%v err:%v", srcFork, ok, err)
	}
	out := map[string]deploy.IniValue{}
	for k, v := range vals {
		out[k] = deploy.IniValue{Value: v, SourceFork: srcFork}
	}
	return out
}

// buildIniLib 造库 + 数据目录 game.ini 夹具（overrides 透传夹具）。
func buildIniLib(t *testing.T, forkID string, version uint64, overrides map[string]string) forks.LibraryRef {
	t.Helper()
	mustLib(t) // librealm 缺失时显式 SKIP（§8.1）
	dir := t.TempDir()
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID: forkID, SchemaVersion: version, DataDir: dir,
	})
	if err != nil {
		t.Fatalf("fixture.Build(%s): %v", forkID, err)
	}
	if _, err := fixture.WriteGameIni(dir, overrides); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestGameIniFullChain(t *testing.T) {
	env := newEnv(t)
	store, err := inisettings.OpenStore(filepath.Join(env.dir, "gameini"))
	if err != nil {
		t.Fatal(err)
	}
	// 源端 lazer：模板默认 + ShowFpsDisplay=true（目标端模板为 false，
	// 该键应经全链落地）。
	src := buildIniLib(t, "lazer", 52, map[string]string{"ShowFpsDisplay": "true"})
	// 目标端 delta：模板默认值 + 自己的 Token（红灯键不得被源端值覆盖）。
	dst := buildIniLib(t, "delta", 51, nil)

	// ① 收集：白名单键入快照仓，红灯/Own/未知/恶意键不入。
	rep := env.collectIntoWithIni(store, src)
	if rep.IniKeysCollected <= 0 {
		t.Fatalf("IniKeysCollected = %d，期望 > 0", rep.IniKeysCollected)
	}
	values, _, ok, err := store.Load("lazer")
	if err != nil || !ok {
		t.Fatalf("快照 Load = ok:%v err:%v", ok, err)
	}
	for _, k := range []string{"Token", "Username", "ReleaseStream", "FutureKey", "Weird Key With"} {
		if _, hit := values[k]; hit {
			t.Errorf("快照混入非白名单键 %q", k)
		}
	}
	// delta Own 键（MenuCursorFromSkin）不是四端共有键（lazer 无此键），
	// 不进 sharedKeys，收集面天然排除。
	if _, hit := values["MenuCursorFromSkin"]; hit {
		t.Error("delta 专有键不应被 lazer 源收集")
	}
	if values["ShowFpsDisplay"] != "true" {
		t.Fatalf("源端 ShowFpsDisplay = %q，期望 true", values["ShowFpsDisplay"])
	}

	// ② 投放：lazer canonical → delta 目标。
	targetIniPath := filepath.Join(dst.DataDir, "game.ini")
	beforeTarget, _ := os.ReadFile(targetIniPath)
	res, err := deploy.SyncGameIniValues(dst, iniValsFromStore(t, store, "lazer"),
		nil, env.backups, guard.NewGuard(nil))
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
	}
	if res.Applied["ShowFpsDisplay"] != "true" {
		t.Fatalf("Applied = %v，期望含 ShowFpsDisplay=true", res.Applied)
	}
	afterTarget, _ := os.ReadFile(targetIniPath)

	// 白名单外键零触碰 + 其余内容逐字节不变（模板行逐行比对；被替换的
	// 白名单键行只有 ShowFpsDisplay——夹具里源/目标唯一差异键）。
	gotLines := strings.Split(string(afterTarget), "\n")
	wantLines := strings.Split(string(beforeTarget), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("行数 %d ≠ %d（不应增删行，追加面无触发）", len(gotLines), len(wantLines))
	}
	for i, orig := range wantLines {
		if strings.HasPrefix(orig, "ShowFpsDisplay = ") {
			if gotLines[i] != "ShowFpsDisplay = true" {
				t.Errorf("ShowFpsDisplay 行 = %q，期望被源值覆盖为 true", gotLines[i])
			}
			continue
		}
		if gotLines[i] != orig {
			t.Errorf("行 %d 被触碰: got %q want %q", i, gotLines[i], orig)
		}
	}
	// 红灯键逐项：delta 目标自身值原样（源端 Token 绝不跨端）。
	doc := inisettings.Parse(afterTarget)
	if v, _ := doc.Value("Token"); v != "fixture.eyJhbGciOiJSUzI1NiJ9.fake.jwt.payload" {
		t.Errorf("目标 Token 被改写: %q", v)
	}
	if v, _ := doc.Value("Username"); v != "fixture_player" {
		t.Errorf("目标 Username 被改写: %q", v)
	}
	if v, _ := doc.Value("ReleaseStream"); v != "Lazer" {
		t.Errorf("目标 ReleaseStream 被改写: %q", v)
	}
	// delta Own 键原样（源端收集面就没有它）。
	if v, _ := doc.Value("MenuCursorFromSkin"); v != "true" {
		t.Errorf("目标 delta Own 键被触碰: %q", v)
	}

	// 备份存在且等于写前原件（S2/S12）。
	if res.BackupPath == "" {
		t.Fatal("备份路径为空")
	}
	bk, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatalf("读备份: %v", err)
	}
	if !bytes.Equal(bk, beforeTarget) {
		t.Error("备份内容 ≠ 写前原件")
	}

	// ③ 幂等重跑：值已一致 → no-op（零落地零新备份）。
	res2, err := deploy.SyncGameIniValues(dst, iniValsFromStore(t, store, "lazer"),
		nil, env.backups, guard.NewGuard(nil))
	if err != nil {
		t.Fatalf("重跑 SyncGameIniValues: %v", err)
	}
	if len(res2.Applied) != 0 {
		t.Errorf("重跑应幂等零落地，got %v", res2.Applied)
	}
	if got := iniSkip(res2.Skipped, "幂等跳过"); got < 0 {
		t.Errorf("重跑应计数幂等跳过，Skipped = %+v", res2.Skipped)
	}
	after2, _ := os.ReadFile(targetIniPath)
	if !bytes.Equal(after2, afterTarget) {
		t.Error("重跑后目标文件被改动")
	}
}

func TestGameIniTargetNeverLaunched(t *testing.T) {
	env := newEnv(t)
	store, err := inisettings.OpenStore(filepath.Join(env.dir, "gameini"))
	if err != nil {
		t.Fatal(err)
	}
	src := buildIniLib(t, "lazer", 52, nil)
	if rep := env.collectIntoWithIni(store, src); rep.IniKeysCollected <= 0 {
		t.Fatalf("IniKeysCollected = %d，期望 > 0", rep.IniKeysCollected)
	}
	// 目标端从未启动过客户端：数据目录存在（有库）但无 game.ini。
	mustLib(t)
	dir := t.TempDir()
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID: "delta", SchemaVersion: 51, DataDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := deploy.SyncGameIniValues(ref, iniValsFromStore(t, store, "lazer"),
		nil, env.backups, guard.NewGuard(nil))
	if err != nil {
		t.Fatalf("SyncGameIniValues: %v", err)
	}
	if got := iniSkip(res.Skipped, "从未启动过客户端"); got != 1 {
		t.Fatalf("目标缺失跳过计数 = %d，期望 1（Skipped=%+v）", got, res.Skipped)
	}
	if len(res.Applied) != 0 {
		t.Errorf("缺失目标不应落地，got %v", res.Applied)
	}
	if _, err := os.Stat(filepath.Join(dir, "game.ini")); !os.IsNotExist(err) {
		t.Error("缺失目标不应被凭空创建 game.ini")
	}
}

// collectIntoWithIni 真实收集一次（含 ini 面）并断言成功。
func (e *env) collectIntoWithIni(store *inisettings.Store, ref forks.LibraryRef) collect.Report {
	e.t.Helper()
	r := collect.NewRunner(collect.Deps{
		Hub: e.hub, Files: e.engine, Snapshots: e.snaps,
		Guard: guard.NewGuard(nil), GameIni: store,
	})
	rep, err := r.Collect(context.Background(), ref, nil)
	if err != nil {
		e.t.Fatalf("Collect(%s %s): %v", ref.ForkID, ref.RealmPath, err)
	}
	return rep
}
