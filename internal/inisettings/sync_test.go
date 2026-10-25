package inisettings

// SyncFile / CollectFile / Store 验收（任务清单逐条）：
//   - 白名单外键（红灯/Own/未知/恶意注入行）零触碰；
//   - 写入后原文件其余内容逐字节不变（除白名单键行）；
//   - 备份存在且等于写前原件；
//   - 崩溃注入（rename 前中断）原文件一字未动、无半份正式文件；
//   - 目标文件不存在 → 跳过计数；
//   - 幂等重跑 no-op。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/forks"
	"lzsync/internal/guard"
)

// targetIni 目标端夹具：白名单键（值与源不同）、红灯键、目标端无的键
//（追加面）、delta 专有键、未知键、恶意注入行（键含空格与 =、值含 =、
// CRLF 行）、注释与空行——同步后除白名单键行外逐字节不变的断言面。
const targetIni = "[General]\n" +
	"// comment line\n" +
	";; legacy comment\n" +
	"Ruleset = osu\n" +
	"Token = target.eyJ.jwt\n" +
	"Username = target_player\n" +
	"ReleaseStream = Tachyon\n" +
	"ShowStoryboard = false\n" +
	"ShowFpsDisplay = false\r\n" +
	"MenuMusic = true\n" +
	"MenuCursorFromSkin = true\n" + // delta Own（对 lazer 目标 = 白名单外）
	"FutureKey = 42\n" +
	"Weird Key With = Signs = value\n" +
	"Injected=Key\r\n" +
	"Attempt = 1\n" +
	"\n"

// canonical 模拟 hub 快照的 canonical 键值（正常收集面 + 恶意塞入的
// 红灯/白名单外键——执行期复核防线）。
var canonical = map[string]string{
	"Ruleset":          "fruits",
	"ShowStoryboard":   "true", // 与目标不同 → 落地
	"ShowFpsDisplay":   "false",
	"MenuMusic":        "true", // 与目标一致 → 幂等跳过
	"NewInTarget":      "",     // 不在白名单（不是 OsuSetting）→ 拒
	"Skin":             "5df8f34c-204f-4d5f-b853-18b7f3e5b8d6", // 目标缺行 → 追加
	"Token":            "hijacked.jwt",  // 红灯注入 → 拒
	"Username":         "attacker",      // 红灯注入 → 拒
	"ReleaseStream":    "G0V0",          // 红灯注入 → 拒
	"MenuCursorFromSkin": "false",       // delta Own（对 lazer = 白名单外）→ 拒
}

func setupSync(t *testing.T) (path string, backups *guard.Backups) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "game.ini")
	if err := os.WriteFile(path, []byte(targetIni), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := guard.OpenBackups(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return path, b
}

func lazerProfile(t *testing.T) forks.ForkProfile {
	t.Helper()
	p, ok := forks.Profile("lazer")
	if !ok {
		t.Fatal("lazer Profile 缺失")
	}
	return p
}

func TestSyncFileWhitelistOnly(t *testing.T) {
	path, backups := setupSync(t)
	before, _ := os.ReadFile(path)

	res, err := SyncFile(lazerProfile(t), lazerProfile(t), path, canonical, backups)
	if err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	// 落地面：变化键 Ruleset/ShowStoryboard + 追加键 Skin；值一致键幂等跳过。
	if len(res.Applied) != 3 || res.Applied["Ruleset"] != "fruits" ||
		res.Applied["ShowStoryboard"] != "true" ||
		res.Applied["Skin"] != "5df8f34c-204f-4d5f-b853-18b7f3e5b8d6" {
		t.Fatalf("Applied = %v，期望恰 {Ruleset, ShowStoryboard, Skin}", res.Applied)
	}
	skipOf := func(sub string) int {
		n := 0
		for _, s := range res.Skipped {
			if strings.Contains(s.Reason, sub) {
				n += s.Count
			}
		}
		return n
	}
	if got := skipOf("红灯键"); got != 3 {
		t.Errorf("红灯键拒写计数 = %d，期望 3（Token/Username/ReleaseStream）", got)
	}
	if got := skipOf("白名单外键"); got != 2 {
		t.Errorf("白名单外键计数 = %d，期望 2（NewInTarget/MenuCursorFromSkin）", got)
	}
	if got := skipOf("幂等跳过"); got != 2 { // ShowFpsDisplay 与 MenuMusic
		t.Errorf("幂等跳过 = %d，期望 2", got)
	}

	after, _ := os.ReadFile(path)
	want := "[General]\n" +
		"// comment line\n" +
		";; legacy comment\n" +
		"Ruleset = fruits\n" + // 白名单键值替换（值区间之外字节不变）
		"Token = target.eyJ.jwt\n" +
		"Username = target_player\n" +
		"ReleaseStream = Tachyon\n" +
		"ShowStoryboard = true\n" +
		"ShowFpsDisplay = false\r\n" +
		"MenuMusic = true\n" +
		"MenuCursorFromSkin = true\n" +
		"FutureKey = 42\n" +
		"Weird Key With = Signs = value\n" +
		"Injected=Key\r\n" +
		"Attempt = 1\n" +
		"\n" +
		"Skin = 5df8f34c-204f-4d5f-b853-18b7f3e5b8d6\n"
	if !bytes.Equal(after, []byte(want)) {
		t.Fatalf("写后文件与期望不符:\n got %q\nwant %q", after, want)
	}
	// 除白名单键行（Ruleset/ShowStoryboard 值替换 + Skin 追加）外逐字节
	// 不变：内容行逐行比对原文（Append 落在内容行之后）。
	gotLines := strings.Split(string(after), "\n")
	wantLines := strings.Split(targetIni, "\n")
	changed := map[int]bool{3: true, 7: true} // Ruleset 与 ShowStoryboard 行（0 基）
	for i := 0; i < 16; i++ {                // targetIni 的 16 个内容行
		if changed[i] {
			continue
		}
		if gotLines[i] != wantLines[i] {
			t.Errorf("行 %d 被触碰: got %q want %q", i, gotLines[i], wantLines[i])
		}
	}
	// 备份存在且等于写前原件。
	if res.BackupPath == "" {
		t.Fatal("备份路径为空")
	}
	bk, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatalf("读备份: %v", err)
	}
	if !bytes.Equal(bk, before) {
		t.Error("备份内容 ≠ 写前原件")
	}
}

func TestSyncFileTargetMissingSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game.ini")
	res, err := SyncFile(lazerProfile(t), lazerProfile(t), path, canonical, nil)
	if err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "从未启动过客户端") ||
		res.Skipped[0].Count != 1 {
		t.Fatalf("Skipped = %+v，期望目标缺失跳过计 1", res.Skipped)
	}
	if len(res.Applied) != 0 {
		t.Errorf("缺失目标不应落地任何键，got %v", res.Applied)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("缺失目标不应被凭空创建")
	}
}

func TestSyncFileIdempotentNoop(t *testing.T) {
	path, backups := setupSync(t)
	// 全部值与目标一致 → no-op：不写、不备份。
	same := map[string]string{"ShowStoryboard": "false", "MenuMusic": "true"}
	res, err := SyncFile(lazerProfile(t), lazerProfile(t), path, same, backups)
	if err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	if len(res.Applied) != 0 || res.BackupPath != "" {
		t.Fatalf("no-op 期望零落地零备份，got applied=%v backup=%q", res.Applied, res.BackupPath)
	}
	if got := len(backupDirEntries(t, backups)); got != 0 {
		t.Errorf("no-op 后备份目录应有 0 项，got %d", got)
	}
	// 二次同步（有变化的）后再跑一次同值 → 幂等。
	res2, err := SyncFile(lazerProfile(t), lazerProfile(t), path, map[string]string{"ShowStoryboard": "true"}, backups)
	if err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	if res2.Applied["ShowStoryboard"] != "true" {
		t.Fatalf("首次应落地，got %v", res2.Applied)
	}
	res3, err := SyncFile(lazerProfile(t), lazerProfile(t), path, map[string]string{"ShowStoryboard": "true"}, backups)
	if err != nil {
		t.Fatalf("重跑: %v", err)
	}
	if len(res3.Applied) != 0 {
		t.Errorf("重跑应幂等零落地，got %v", res3.Applied)
	}
}

func TestSyncFileCrashBeforeRename(t *testing.T) {
	path, backups := setupSync(t)
	before, _ := os.ReadFile(path)

	beforeRenameHook = func() error { return errors.New("注入：rename 前崩溃") }
	defer func() { beforeRenameHook = nil }()

	_, err := SyncFile(lazerProfile(t), lazerProfile(t), path, map[string]string{"ShowStoryboard": "true"}, backups)
	if err == nil || !strings.Contains(err.Error(), "原文件未动") {
		t.Fatalf("崩溃注入应返回错误，got %v", err)
	}
	// 原文件逐字节未动（不存在半份正式文件：game.ini 要么旧要么新）。
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, before) {
		t.Fatal("rename 前中断后原文件被改动（S12 违背）")
	}
	// 临时文件已清理，无 .lzsync-ini-* 残留。
	for _, e := range dirEntries(t, filepath.Dir(path)) {
		if strings.HasPrefix(e, ".lzsync-ini-") {
			t.Errorf("临时文件残留: %s", e)
		}
	}
}

func TestCollectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game.ini")
	if err := os.WriteFile(path, []byte(targetIni), 0o644); err != nil {
		t.Fatal(err)
	}
	values, ok, err := CollectFile(path, lazerProfile(t))
	if err != nil || !ok {
		t.Fatalf("CollectFile = (%v,%v,%v)", values, ok, err)
	}
	for _, k := range []string{"Token", "Username", "ReleaseStream", "MenuCursorFromSkin",
		"FutureKey", "Weird Key With"} {
		if _, hit := values[k]; hit {
			t.Errorf("收集面混入非白名单键 %q", k)
		}
	}
	if values["ShowStoryboard"] != "false" || values["Ruleset"] != "osu" {
		t.Errorf("白名单键值收集不符: %v", values)
	}
	// 文件不存在 = 源端从未启动过客户端（正常态）。
	got, ok, err := CollectFile(filepath.Join(dir, "absent.ini"), lazerProfile(t))
	if err != nil || ok || got != nil {
		t.Fatalf("缺失源 = (%v,%v,%v)，期望 (nil,false,nil)", got, ok, err)
	}
}

func TestStoreRoundtrip(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "gameini"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := store.Load("lazer"); err != nil || ok {
		t.Fatalf("空仓 Load = ok:%v err:%v，期望 false nil", ok, err)
	}
	at := time.UnixMilli(1770000000000).UTC()
	keys := map[string]string{"Ruleset": "osu", "ShowStoryboard": "true"}
	if err := store.Save("lazer", keys, at); err != nil {
		t.Fatal(err)
	}
	got, ts, ok, err := store.Load("lazer")
	if err != nil || !ok {
		t.Fatalf("Load = %v %v %v", got, ok, err)
	}
	if got["Ruleset"] != "osu" || got["ShowStoryboard"] != "true" || !ts.Equal(at) {
		t.Errorf("快照往返不符: %v @%v", got, ts)
	}
	// 整份覆盖语义（HB3 的 ini 形态）：重收后旧键消失。
	if err := store.Save("lazer", map[string]string{"Ruleset": "fruits"}, at); err != nil {
		t.Fatal(err)
	}
	got2, _, _, _ := store.Load("lazer")
	if len(got2) != 1 || got2["Ruleset"] != "fruits" {
		t.Errorf("重收应整份覆盖，got %v", got2)
	}
	// 非法 fork id 拒绝（路径注入面）。
	if err := store.Save("../evil", nil, at); err == nil {
		t.Error("非法 fork id 应被拒绝")
	}
	// 快照写入同样原子：rename 前中断不破坏既有快照。
	if err := store.Save("lazer", keys, at); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Dir, "lazer.json"))
	beforeRenameHook = func() error { return errors.New("注入") }
	defer func() { beforeRenameHook = nil }()
	if err := store.Save("lazer", map[string]string{"x": "y"}, at); err == nil {
		t.Fatal("注入应报错")
	}
	after, _ := os.ReadFile(filepath.Join(store.Dir, "lazer.json"))
	if !bytes.Equal(before, after) {
		t.Error("快照原子写违背：中断后快照被改动")
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func backupDirEntries(t *testing.T, b *guard.Backups) []string {
	t.Helper()
	return dirEntries(t, b.Dir)
}


// TestSyncFileSourcePolicyRecheck 投放复核的源端面（SyncableKeys 语义）：
// 收集侧前置滤除依赖收集时的 Profile 数据，未来某端 Own/红灯名单扩充后
// 旧快照仍可漏出——SyncFile 必须独立复核源端 Own 与红灯。
func TestSyncFileSourcePolicyRecheck(t *testing.T) {
	path, backups := setupSync(t)
	// 构造 Profile 模拟「共有键被收编为源端 Own / 新增源端红灯」。
	src := forks.ForkProfile{
		ID: "test-src",
		IniKeys: forks.IniKeyPolicy{
			Own:    []string{"Ruleset"},        // 共有键 → 源端专有拒写
			Locked: []string{"ShowStoryboard"}, // 共有键 → 源端红灯拒写
		},
	}
	res, err := SyncFile(src, lazerProfile(t), path, map[string]string{
		"Ruleset":        "fruits",
		"ShowStoryboard": "true",
	}, backups)
	if err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Errorf("Applied = %v（期望空——源端 Own/红灯键全部拒写）", res.Applied)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("Skipped = %+v（期望源端专有+源端红灯各 1 条）", res.Skipped)
	}
	// sortedSkips 按字符串序：「源端专有键 …」<「红灯键 …」。
	if !strings.Contains(res.Skipped[0].Reason, "源端专有键 Ruleset") {
		t.Errorf("Skipped[0] = %q（期望源端专有键 Ruleset）", res.Skipped[0].Reason)
	}
	if !strings.Contains(res.Skipped[1].Reason, "红灯键 ShowStoryboard") {
		t.Errorf("Skipped[1] = %q（期望红灯键 ShowStoryboard）", res.Skipped[1].Reason)
	}
}
