package appsvc

// 容器导出服务测试：真实后端用 hub 直填 + 内存 filestore 仓（不依赖
// native 库——realExport 本身不触碰 realmc，registry 手工装配）；拉起
// 函数注入 stub，自动化绝不真的调 xdg-open（真实拉起属人工项，§8）。
// Mock 后端覆盖 UI 呈现分支。

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
)

// seedForExport hub 直填 + 收仓：返回种子数据与临时 hub 目录。
func seedForExport(t *testing.T, dir string) (*hub.Store, *filestore.Engine, fixture.Seed) {
	t.Helper()
	h, err := hub.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatalf("hub.Open: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	store, err := filestore.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	seed, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	// 皮肤种子（StandardSeed 不带皮肤，deploy 侧同款先例）。
	skin, err := fixture.MaterializeSkin("Export Skin", "creator", []fixture.SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:Export Skin\n")},
		{Filename: "hit.png", Bytes: []byte("png-bytes")},
	}, seed.Files)
	if err != nil {
		t.Fatalf("MaterializeSkin: %v", err)
	}
	seed.Skins = append(seed.Skins, skin)
	if _, err := h.ApplySnapshot(context.Background(), hub.Snapshot{
		ForkID: "lazer", CollectedAt: time.UnixMilli(1770000000000),
		BeatmapSets: seed.BeatmapSets, Skins: toHubSkins(seed.Skins), Scores: toHubScores(seed.Scores),
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	// 字节入仓（Store.Put 从路径收，先落盘再收）。
	for hash, b := range seed.Files {
		p := filepath.Join(t.TempDir(), hash)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("写字节 %s: %v", shortKey(hash), err)
		}
		if err := store.Put(p); err != nil {
			t.Fatalf("入仓 %s: %v", shortKey(hash), err)
		}
	}
	return h, filestore.NewEngine(store), seed
}

// toHubSkins/toHubScores fixture 种子 → hub 直填形态（Files = usage 列表）。
func toHubSkins(skins []fixture.SeedSkin) []hub.Skin {
	out := make([]hub.Skin, 0, len(skins))
	for _, s := range skins {
		out = append(out, hub.Skin{
			Name: s.Name, Creator: s.Creator, Hash: s.Hash,
			InstantiationInfo: s.InstantiationInfo, Files: s.Files,
		})
	}
	return out
}

func toHubScores(scores []fixture.SeedScore) []hub.Score {
	out := make([]hub.Score, 0, len(scores))
	for _, s := range scores {
		files := make([]hub.NamedFile, 0, len(s.ReplayHashes))
		for _, h := range s.ReplayHashes {
			files = append(files, hub.NamedFile{Hash: h, Filename: "replay.osr"})
		}
		out = append(out, hub.Score{
			BeatmapHash: s.BeatmapHash, Hash: s.Hash, RulesetShortName: s.RulesetShortName,
			ClientVersion: s.ClientVersion, TotalScore: s.TotalScore, Date: s.Date,
			User: s.User, Files: files,
		})
	}
	return out
}

// newExportForTest 手工装配 realExport（拉起 stub 化）。
func newExportForTest(t *testing.T, launch func(string) error) (*realExport, fixture.Seed, string) {
	t.Helper()
	dir := t.TempDir()
	h, engine, seed := seedForExport(t, dir)
	reg := &registry{byID: map[string]forks.LibraryRef{}}
	reg.put(forks.LibraryRef{ForkID: "lazer"})
	if launch == nil {
		launch = func(string) error { return nil }
	}
	return &realExport{deps: Deps{Hub: h, Files: engine, DataDir: dir}, reg: reg, open: launch}, seed, dir
}

func readZip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读容器 %s: %v", path, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip 打开 %s: %v", path, err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("zip 条目 %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close() //nolint:errcheck
		if err != nil {
			t.Fatalf("zip 条目 %s 读取: %v", f.Name, err)
		}
		out[f.Name] = b
	}
	return out
}

func TestRealExportOSZ(t *testing.T) {
	exp, seed, _ := newExportForTest(t, nil)
	set := seed.BeatmapSets[0]
	res, err := exp.ExportContainers("lazer", hub.KindBeatmapSet, []string{set.Hash})
	if err != nil {
		t.Fatalf("ExportContainers: %v", err)
	}
	if len(res.Paths) != 1 || res.Note != "" {
		t.Fatalf("结果 = (%v, %q)，期望 1 路径无拉起提示", res.Paths, res.Note)
	}
	if filepath.Ext(res.Paths[0]) != ".osz" {
		t.Fatalf("扩展名 = %s，期望 .osz", filepath.Ext(res.Paths[0]))
	}
	if !strings.Contains(filepath.Base(res.Paths[0]), "Fixture Title") {
		t.Fatalf("文件名应携带谱面标题人话：%s", res.Paths[0])
	}
	got := readZip(t, res.Paths[0])
	if len(got) != len(set.Files) {
		t.Fatalf("条目数 = %d，期望 %d", len(got), len(set.Files))
	}
	for _, f := range set.Files {
		if !bytes.Equal(got[f.Filename], seed.Files[f.Hash]) {
			t.Fatalf("条目 %s 字节与仓不符", f.Filename)
		}
	}
}

func TestRealExportOSKAndOSR(t *testing.T) {
	exp, seed, _ := newExportForTest(t, nil)
	skin := seed.Skins[0]
	res, err := exp.ExportContainers("lazer", hub.KindSkin, []string{skin.Hash})
	if err != nil {
		t.Fatalf("ExportContainers(skin): %v", err)
	}
	got := readZip(t, res.Paths[0])
	if len(got) != len(skin.Files) {
		t.Fatalf("皮肤条目数 = %d，期望 %d", len(got), len(skin.Files))
	}
	for _, f := range skin.Files {
		if !bytes.Equal(got[f.Filename], seed.Files[f.Hash]) {
			t.Fatalf("皮肤条目 %s 字节与仓不符", f.Filename)
		}
	}

	score := seed.Scores[0]
	key := hub.ScoreContentKey(score.BeatmapHash, score.Hash)
	res, err = exp.ExportContainers("lazer", hub.KindScore, []string{key})
	if err != nil {
		t.Fatalf("ExportContainers(score): %v", err)
	}
	if filepath.Ext(res.Paths[0]) != ".osr" {
		t.Fatalf("扩展名 = %s，期望 .osr", filepath.Ext(res.Paths[0]))
	}
	raw, err := os.ReadFile(res.Paths[0])
	if err != nil {
		t.Fatalf("读 .osr: %v", err)
	}
	if !bytes.Equal(raw, seed.Files[score.ReplayHashes[0]]) {
		t.Fatalf(".osr 字节与仓不符")
	}
}

func TestRealExportLaunchFailureKeepsPaths(t *testing.T) {
	exp, seed, _ := newExportForTest(t, func(string) error {
		return errFakeLaunch
	})
	set := seed.BeatmapSets[0]
	res, err := exp.ExportContainers("lazer", hub.KindBeatmapSet, []string{set.Hash})
	if err != nil {
		t.Fatalf("拉起失败不是导出失败: %v", err)
	}
	if len(res.Paths) != 1 || !strings.Contains(res.Note, "拉起失败") {
		t.Fatalf("拉起失败应进 Note 且保留路径: %+v", res)
	}
}

var errFakeLaunch = &launchError{}

type launchError struct{}

func (*launchError) Error() string { return "拉起失败（stub）" }

func TestRealExportPreconditions(t *testing.T) {
	exp, seed, _ := newExportForTest(t, nil)
	set := seed.BeatmapSets[0]
	if _, err := exp.ExportContainers("nope", hub.KindBeatmapSet, []string{set.Hash}); err == nil {
		t.Fatalf("未注册 fork 应报错")
	}
	if _, err := exp.ExportContainers("lazer", hub.KindCollection, []string{"X"}); err == nil {
		t.Fatalf("收藏夹无容器形态应报错")
	}
	if _, err := exp.ExportContainers("lazer", hub.KindBeatmapSet, nil); err == nil {
		t.Fatalf("空内容键应报错")
	}
	if _, err := exp.ExportContainers("lazer", hub.KindBeatmapSet, []string{"不存在"}); err == nil {
		t.Fatalf("hub 缺键应报错")
	}
	// 同批重名消歧：同键导两次 → 第二个文件名追加序号，互不覆盖。
	res, err := exp.ExportContainers("lazer", hub.KindBeatmapSet, []string{set.Hash, set.Hash})
	if err != nil {
		t.Fatalf("同键双导: %v", err)
	}
	if res.Paths[0] == res.Paths[1] {
		t.Fatalf("同批同名应消歧: %v", res.Paths)
	}
	for _, p := range res.Paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("容器 %s 应存在: %v", p, err)
		}
	}
}

func TestMockExport(t *testing.T) {
	b := NewMockBackend("mock", "测试", nil)
	if _, err := b.Export.ExportContainers("", hub.KindSkin, []string{"k"}); err == nil {
		t.Fatalf("空 fork 应报错")
	}
	if _, err := b.Export.ExportContainers("lazer", hub.KindModPreset, []string{"k"}); err == nil {
		t.Fatalf("无容器形态应报错")
	}
	res, err := b.Export.ExportContainers("lazer", hub.KindBeatmapSet, []string{"k1", "k2"})
	if err != nil || len(res.Paths) != 2 || res.Note == "" {
		t.Fatalf("Mock 导出 = (%+v, %v)", res, err)
	}
}
