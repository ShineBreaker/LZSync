package forks

import (
	"os"
	"path/filepath"
	"testing"

	"lzsync/internal/realmc"
)

// view 快速构造结构视图（class 与「class→列」二元组）。
func view(version uint64, classes []string, cols map[string][]string) structureView {
	v := structureView{version: version, classes: map[string]bool{}, props: map[string]map[string]bool{}}
	for _, c := range classes {
		v.classes[c] = true
		v.props[c] = map[string]bool{}
	}
	for c, cs := range cols {
		if v.props[c] == nil {
			v.props[c] = map[string]bool{}
		}
		for _, name := range cs {
			v.props[c][name] = true
		}
	}
	return v
}

// TestMatchFingerprint 四端身份判定表驱动：指纹优先、版本 + 目录消歧、
// 歧义与未知绝不猜（§6.1）。lazer 与 g0v0 结构全等（fork-g0v0 §2），
// 目录名是唯一区分信号。
func TestMatchFingerprint(t *testing.T) {
	officialClasses := []string{"BeatmapSet", "Beatmap", "Score", "Ruleset", "Skin", "File"}
	tests := []struct {
		name    string
		view    structureView
		dirName string
		want    string // 空串 = 不识别
	}{
		{
			name: "Ez专有表指纹命中(任意版本)",
			view: view(52, append([]string{"EzBeatmapSkillValue"}, officialClasses...), nil),
			want: "ez2lazer",
		},
		{
			name:    "Ez专有列指纹命中",
			view:    view(52, officialClasses, map[string][]string{"Score": {"JudgeMode", "TotalScore"}}),
			want:    "ez2lazer",
		},
		{
			name: "Ez编码版本即身份(无指纹)",
			view: view(52011, officialClasses, nil),
			want: "ez2lazer",
		},
		{
			name: "Ez编码版本不需目录佐证",
			view: view(52011, officialClasses, nil), dirName: "任意目录",
			want: "ez2lazer",
		},
		{
			name:    "v51+deltalazer目录=delta",
			view:    view(51, officialClasses, nil), dirName: "deltalazer",
			want: "delta",
		},
		{
			name:    "v51+development变体=delta",
			view:    view(51, officialClasses, nil), dirName: "deltalazer-development",
			want: "delta",
		},
		{
			name:    "v51+osu目录=不识别(可能是历史官方库)",
			view:    view(51, officialClasses, nil), dirName: "osu",
			want: "",
		},
		{
			name:    "v52+osu目录=lazer",
			view:    view(52, officialClasses, nil), dirName: "osu",
			want: "lazer",
		},
		{
			name:    "v52+g0v0目录=g0v0(结构全等靠目录消歧)",
			view:    view(52, officialClasses, nil), dirName: "g0v0",
			want: "g0v0",
		},
		{
			name:    "v52+development变体=lazer",
			view:    view(52, officialClasses, nil), dirName: "osu-development",
			want: "lazer",
		},
		{
			name:    "v52+陌生目录=不识别(lazer/g0v0歧义)",
			view:    view(52, officialClasses, nil), dirName: "my-custom-dir",
			want: "",
		},
		{
			name:    "未知版本=不识别",
			view:    view(49, officialClasses, nil), dirName: "osu",
			want: "",
		},
		{
			name:    "未来官方版本=不识别",
			view:    view(53, officialClasses, nil), dirName: "osu",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchProfile(tc.view, tc.dirName)
			if tc.want == "" {
				if ok {
					t.Errorf("matchProfile 误判为 %q，期望不识别", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("matchProfile = (%q, %v), 期望 %q", got, ok, tc.want)
			}
		})
	}
}

func TestParseVersionedRealmName(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"/x/y/client_52011.realm", 52011, true},
		{"/x/y/client_52.realm", 52, true},
		{"/x/y/client.realm", 0, false},
		{"/x/y/client_x.realm", 0, false},
		{"/x/y/client_52011.realm.bak", 0, false},
		{"/x/y/other_52011.realm", 0, false},
	}
	for _, tc := range tests {
		if got, ok := parseVersionedRealmName(tc.in); ok != tc.ok || got != tc.want {
			t.Errorf("parseVersionedRealmName(%q) = (%d, %v), 期望 (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// garbage 非法库内容：realm 只读打开对无效文件必失败（本机实证 errno=1021
// "file has an invalid size"），保证降级链测试的确定性（无论 native 库在否）。
func garbage(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("not a realm file, garbage bytes 0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscover 四端目录发现：目录身份 + 版本降级链（非 Ez 端垃圾库
// 开库失败 → unknown-running；Ez 版本化文件名 → filename 推断 + 解码）。
func TestDiscover(t *testing.T) {
	root := t.TempDir()
	mustMkRealm := func(dir, name string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, dir, name)
		garbage(t, p)
		return p
	}
	lazer := mustMkRealm("osu", "client.realm")
	delta := mustMkRealm("deltalazer", "client.realm")
	ez := mustMkRealm("osu-Ez2Lazer", "client_52011.realm")
	g0v0 := mustMkRealm("g0v0", "client.realm")
	// 前驱链遗留：旧号文件不干扰当前库定位（取版本最大者）。
	if err := os.WriteFile(filepath.Join(root, "osu-Ez2Lazer", "client_51011.realm"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// 无关目录与无库目录不产出候选。
	if err := os.MkdirAll(filepath.Join(root, "unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "osu-development"), 0o755); err != nil {
		t.Fatal(err)
	}

	refs, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(refs) != 4 {
		t.Fatalf("发现 %d 个候选, 期望 4: %+v", len(refs), refs)
	}
	byFork := map[string]LibraryRef{}
	for _, r := range refs {
		byFork[r.ForkID] = r
	}
	checks := []struct {
		fork  string
		path  string
		ver   uint64
		src   VersionSource
		up    int
		rev   int
		realm string
	}{
		{"lazer", filepath.Join(root, "osu"), 0, VersionUnknownRunning, 0, 0, lazer},
		{"delta", filepath.Join(root, "deltalazer"), 0, VersionUnknownRunning, 0, 0, delta},
		{"ez2lazer", filepath.Join(root, "osu-Ez2Lazer"), 52011, VersionFromFilename, 52, 11, ez},
		{"g0v0", filepath.Join(root, "g0v0"), 0, VersionUnknownRunning, 0, 0, g0v0},
	}
	for _, c := range checks {
		r, ok := byFork[c.fork]
		if !ok {
			t.Fatalf("未发现 %s 候选: %+v", c.fork, refs)
		}
		if r.DataDir != c.path || r.RealmPath != c.realm {
			t.Errorf("%s 路径 = (%s, %s), 期望 (%s, %s)", c.fork, r.DataDir, r.RealmPath, c.path, c.realm)
		}
		if r.DiskVersion != c.ver || r.VersionSource != c.src {
			t.Errorf("%s 版本 = (%d, %s), 期望 (%d, %s)", c.fork, r.DiskVersion, r.VersionSource, c.ver, c.src)
		}
		if r.UpstreamVersion != c.up || r.ForkRev != c.rev {
			t.Errorf("%s 解码 = (%d, %d), 期望 (%d, %d)", c.fork, r.UpstreamVersion, r.ForkRev, c.up, c.rev)
		}
	}

	// 数据根不存在 = 空集非错误。
	refs, err = Discover(filepath.Join(root, "no-such-root"))
	if err != nil || len(refs) != 0 {
		t.Errorf("不存在的数据根: refs=%v err=%v, 期望空集无错", refs, err)
	}
}

// TestDiscoverDevelopmentDir debug 构建目录变体同样命中（fork-comparison §1.1）。
func TestDiscoverDevelopmentDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "g0v0-development")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	garbage(t, filepath.Join(dir, "client.realm"))
	refs, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(refs) != 1 || refs[0].ForkID != "g0v0" {
		t.Fatalf("候选 = %+v, 期望单个 g0v0", refs)
	}
}

// TestIdentifyFilenameFallback Ez 版本化文件名推断（开库失败或无 native 库
// 时均可退，§3.2「仅 Ez 版本化文件名可退」）。
func TestIdentifyFilenameFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "client_52011.realm")
	garbage(t, p)
	ref, err := Identify(p)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if ref.ForkID != "ez2lazer" || ref.VersionSource != VersionFromFilename || ref.DiskVersion != 52011 {
		t.Errorf("ref = %+v, 期望 ez2lazer/filename/52011", ref)
	}
	if ref.UpstreamVersion != 52 || ref.ForkRev != 11 {
		t.Errorf("解码 = (%d, %d), 期望 (52, 11)", ref.UpstreamVersion, ref.ForkRev)
	}
	if ref.DataDir != dir {
		t.Errorf("DataDir = %s, 期望 %s", ref.DataDir, dir)
	}
}

// TestIdentifyUnidentified 开不了库、文件名无版本、无档案 → ErrUnidentified
// + unknown-running，原始信息供 UI 呈现（§6.1）。
func TestIdentifyUnidentified(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "client.realm")
	garbage(t, p)
	ref, err := Identify(p)
	if err == nil {
		t.Fatalf("期望 ErrUnidentified, got %+v", ref)
	}
	if ref.ForkID != "" || ref.VersionSource != VersionUnknownRunning {
		t.Errorf("ref = %+v, 期望空身份 + unknown-running", ref)
	}
}

// TestIdentifyCachedFromSnapshot 降级链第 ③ 段：开库失败且文件名不可推断
// 时按档案 SourcePath 反查 (fork, diskVersion)。
func TestIdentifyCachedFromSnapshot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "g0v0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "client.realm")
	garbage(t, p)

	store, err := OpenSnapshotStore(filepath.Join(root, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(p)
	if _, err := store.SaveIfAbsent(&StructureSnapshot{
		ForkID: "g0v0", DiskVersion: 52, SourcePath: abs,
	}); err != nil {
		t.Fatal(err)
	}

	// 无档案时失败；有档案时命中 snapshot-cache。
	if _, err := Identify(p); err == nil {
		t.Fatal("无档案的 Identify 不应成功")
	}
	ref, err := IdentifyCached(p, store)
	if err != nil {
		t.Fatalf("IdentifyCached: %v", err)
	}
	if ref.ForkID != "g0v0" || ref.DiskVersion != 52 || ref.VersionSource != VersionFromSnapshot {
		t.Errorf("ref = %+v, 期望 g0v0/52/snapshot-cache", ref)
	}
	if ref.UpstreamVersion != 52 || ref.ForkRev != 0 {
		t.Errorf("解码 = (%d, %d), 期望 (52, 0)", ref.UpstreamVersion, ref.ForkRev)
	}
}

// mustLoadNative 加载 realm-core；缺失时显式 SKIP（P-2 分发场景，
// 与 realmc PoC 测试同款纪律）。
func mustLoadNative(t *testing.T) *realmc.Library {
	t.Helper()
	lib, err := realmc.Load()
	if err != nil {
		t.Skipf("realm-core 库不可用，跳过真库链路: %v", err)
	}
	return lib
}

// TestIdentifyRealRealm 真库全链：显式 schema 建库 → 动态只读开库探测 →
// 指纹/版本判定（realm-open 来源）。
func TestIdentifyRealRealm(t *testing.T) {
	lib := mustLoadNative(t)

	build := func(t *testing.T, dir, name string, version uint64, classes []realmc.ClassDef) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		r, err := lib.OpenWithSchema(p, &realmc.Schema{Classes: classes}, version)
		if err != nil {
			t.Fatalf("建库 %s: %v", p, err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	beatmapSet := realmc.ClassDef{
		Name: "BeatmapSet", PrimaryKey: "ID",
		Properties: []realmc.PropertyDef{{Name: "ID", Type: realmc.PropTypeString, IsPrimaryKey: true}},
	}
	ezTable := realmc.ClassDef{
		Name: "EzBeatmapSkillValue", PrimaryKey: "ID",
		Properties: []realmc.PropertyDef{
			{Name: "ID", Type: realmc.PropTypeString, IsPrimaryKey: true},
			{Name: "Value", Type: realmc.PropTypeFloat},
		},
	}
	root := t.TempDir()

	t.Run("官方52库在osu目录=lazer/realm-open", func(t *testing.T) {
		p := build(t, filepath.Join(root, "osu"), "client.realm", 52, []realmc.ClassDef{beatmapSet})
		ref, err := Identify(p)
		if err != nil {
			t.Fatalf("Identify: %v", err)
		}
		if ref.ForkID != "lazer" || ref.VersionSource != VersionFromOpen || ref.DiskVersion != 52 {
			t.Errorf("ref = %+v", ref)
		}
	})
	t.Run("全等52库在g0v0目录=g0v0(目录消歧)", func(t *testing.T) {
		p := build(t, filepath.Join(root, "g0v0"), "client.realm", 52, []realmc.ClassDef{beatmapSet})
		ref, err := Identify(p)
		if err != nil {
			t.Fatalf("Identify: %v", err)
		}
		if ref.ForkID != "g0v0" || ref.DiskVersion != 52 {
			t.Errorf("ref = %+v", ref)
		}
	})
	t.Run("Ez指纹库=ez2lazer+编码解码", func(t *testing.T) {
		p := build(t, filepath.Join(root, "osu-Ez2Lazer"), "client_52011.realm", 52011,
			[]realmc.ClassDef{beatmapSet, ezTable})
		ref, err := Identify(p)
		if err != nil {
			t.Fatalf("Identify: %v", err)
		}
		if ref.ForkID != "ez2lazer" || ref.VersionSource != VersionFromOpen || ref.DiskVersion != 52011 {
			t.Errorf("ref = %+v", ref)
		}
		if ref.UpstreamVersion != 52 || ref.ForkRev != 11 {
			t.Errorf("解码 = (%d, %d), 期望 (52, 11)", ref.UpstreamVersion, ref.ForkRev)
		}
	})
	t.Run("全等52库在陌生目录=ErrUnidentified带原始版本", func(t *testing.T) {
		p := build(t, filepath.Join(root, "custom"), "client.realm", 52, []realmc.ClassDef{beatmapSet})
		ref, err := Identify(p)
		if err == nil || ref.ForkID != "" {
			t.Fatalf("期望不识别, got (%+v, %v)", ref, err)
		}
		if ref.DiskVersion != 52 || ref.VersionSource != VersionFromOpen {
			t.Errorf("未识别 ref = %+v, 期望带原始版本 52/realm-open", ref)
		}
	})
}

// TestLocateRealmFile 手动注册输入数据目录时的库文件定位：目录名命中
// Profile 走其规则（Ez 版本化名取最大版本、其余 client.realm），目录名
// 不命中时按通用规则（v>=1000 的版本化名优先，否则 client.realm）。
func TestLocateRealmFile(t *testing.T) {
	write := func(t *testing.T, dir, name string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("非Ez目录认client.realm", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "osu")
		write(t, dir, "client.realm")
		got, ok := LocateRealmFile(dir)
		if !ok || got != filepath.Join(dir, "client.realm") {
			t.Fatalf("got (%q, %v)", got, ok)
		}
	})
	t.Run("Ez目录取最大版本化库", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "osu-Ez2Lazer")
		write(t, dir, "client.realm")
		write(t, dir, "client_52010.realm")
		write(t, dir, "client_52011.realm")
		got, ok := LocateRealmFile(dir)
		if !ok || got != filepath.Join(dir, "client_52011.realm") {
			t.Fatalf("got (%q, %v)", got, ok)
		}
	})
	t.Run("陌生目录名按通用规则取版本化库", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "custom")
		write(t, dir, "client_52011.realm")
		got, ok := LocateRealmFile(dir)
		if !ok || got != filepath.Join(dir, "client_52011.realm") {
			t.Fatalf("got (%q, %v)", got, ok)
		}
	})
	t.Run("目录内无库文件定位失败", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "osu")
		write(t, dir, "collection.db")
		if got, ok := LocateRealmFile(dir); ok {
			t.Fatalf("期望定位失败, got %q", got)
		}
	})
}
