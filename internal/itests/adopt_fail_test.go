package itests

// 收仓四步失败注入（design §7-M1 收仓闭环 / §3.2 HB4 / §5.3）：单文件
// 失败 → 游戏侧原文件字节完好且仍在原位、仓内无半成品（已入仓的完整副本
// 非半成品——重跑可跳过复制）；整体跳过环境（首文件试探失败）→ 全部实体
// 文件留原地 + 计数（Aborted/FilesSkipped），投影快照不受影响。
//
// 注入手段用 OS 权限（非 mock）：后遍历文件的父目录去写权限 → 步骤③
// 临时名建链失败；首文件去读权限 → 试探即败。root 忽略 DAC，此类环境
// 显式跳过。

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

// skipIfRoot 权限注入对 root 无效（CAP_DAC_OVERRIDE）。
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root 环境忽略 DAC 权限，权限注入失效（§8.2-6 破坏性场景需非 root）")
	}
}

// physFile 一个游戏侧实体文件（路径 + 内容——路径序与内容序无关，断言以
// 内容为锚避免路径排序误配）。
type physFile struct {
	Path    string
	Content string
}

// seedPhysicalFiles 在 filesDir 按两级哈希布局落 N 个实体文件，返回
// WalkDir 字典序（AdoptDir 的处理序）。
func seedPhysicalFiles(t *testing.T, filesDir string, contents ...string) []physFile {
	t.Helper()
	var out []physFile
	for _, c := range contents {
		h := sha256Hex(c)
		p := filepath.Join(filesDir, h[:1], h[:2], h)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建布局目录: %v", err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatalf("落盘实体文件: %v", err)
		}
		out = append(out, physFile{Path: p, Content: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// assertNoTempResidue 断言目录树内无收仓临时名残留（.lzsync-tmp-*）。
func assertNoTempResidue(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".lzsync-tmp-") {
			t.Errorf("临时产物残留: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描临时残留 %s: %v", root, err)
	}
}

// assertStoreClean 断言仓无半成品：仓内全部常规文件（文件名即哈希）读回
// 校验通过、无临时名残留。
func assertStoreClean(t *testing.T, s *filestore.Store) {
	t.Helper()
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".lzsync-tmp-") {
			t.Errorf("仓内临时残留: %s", p)
			return nil
		}
		if err := s.Verify(d.Name()); err != nil {
			t.Errorf("仓内半成品/坏副本 %s: %v", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓 %s: %v", s.Root, err)
	}
}

// assertIntact 原文件完好断言：仍是实体文件（未被链接替换）且字节一致。
func assertIntact(t *testing.T, path, wantContent string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("%s 已被替换为链接（原文件必须留在原位）", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	if string(b) != wantContent {
		t.Errorf("%s 字节已改变: %q，期望 %q", path, string(b), wantContent)
	}
}

// TestAdoptSingleFileFailure 单文件失败：首文件收仓成功、次文件（父目录
// 只读 → 步骤③ 临时名建链失败）计入 Failed，原文件完好原位；仓内无半成品
//（次文件的完整副本按 HB4 语义保留——非半成品）。
func TestAdoptSingleFileFailure(t *testing.T) {
	skipIfRoot(t)
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	engine := filestore.NewEngine(store)
	filesDir := filepath.Join(t.TempDir(), "files")
	files := seedPhysicalFiles(t, filesDir, "first-file-bytes", "second-file-bytes")

	// 后遍历文件的父目录去写权限 → 步骤③ 无法在其目录内建临时名。
	last := files[len(files)-1]
	lastDir := filepath.Dir(last.Path)
	if err := os.Chmod(lastDir, 0o500); err != nil {
		t.Fatalf("chmod 只读: %v", err)
	}
	t.Cleanup(func() { os.Chmod(lastDir, 0o755) }) //nolint:errcheck // TempDir 清理需要

	stats, err := engine.AdoptDir(filesDir)
	if err != nil {
		t.Fatalf("AdoptDir: %v", err)
	}
	if stats.Adopted != 1 || stats.Failed != 1 {
		t.Errorf("AdoptStats = adopted %d failed %d，期望 1/1（首成功、次失败只计数不中止）",
			stats.Adopted, stats.Failed)
	}
	// 失败文件原样：实体、字节一致、仍在原位。
	assertIntact(t, last.Path, last.Content)
	// 首文件收仓成功：原位为指向仓的链接。
	first := files[0]
	if target, err := os.Readlink(first.Path); err != nil || target != store.Path(sha256Hex(first.Content)) {
		t.Errorf("首文件收仓后应呈指向仓的链接: got %v err %v want %s",
			target, err, store.Path(sha256Hex(first.Content)))
	}
	// 仓无半成品：两个哈希的仓内副本（次文件的完整副本合法保留）都通过校验。
	assertStoreClean(t, store)
	assertNoTempResidue(t, filesDir)
	assertNoTempResidue(t, store.Root)
}

// TestAdoptAbortOnProbeFailure 整体跳过环境：首文件试探失败（读权限被夺）
// → 剩余实体文件整体跳过计数（Aborted）、全部留原地、Adopted=0。
func TestAdoptAbortOnProbeFailure(t *testing.T) {
	skipIfRoot(t)
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	engine := filestore.NewEngine(store)
	filesDir := filepath.Join(t.TempDir(), "files")
	files := seedPhysicalFiles(t, filesDir, "probe-file-bytes", "tail-file-bytes")

	// 首文件（遍历序）去读权限 → 试探即败（§5.3「复制也失败」的降级环境）。
	first := files[0]
	if err := os.Chmod(first.Path, 0o000); err != nil {
		t.Fatalf("chmod 不可读: %v", err)
	}

	stats, err := engine.AdoptDir(filesDir)
	if err != nil {
		t.Fatalf("AdoptDir: %v", err)
	}
	if stats.Adopted != 0 || stats.Aborted != 2 || stats.AbortReason == "" {
		t.Errorf("AdoptStats = adopted %d aborted %d reason %q，期望 0/2 且带原因（整体跳过 + 计数）",
			stats.Adopted, stats.Aborted, stats.AbortReason)
	}
	// 全部留原地：恢复读权限后断言两文件仍实体完好、字节未变。
	if err := os.Chmod(first.Path, 0o644); err != nil {
		t.Fatalf("恢复读权限: %v", err)
	}
	assertIntact(t, first.Path, first.Content)
	assertIntact(t, files[1].Path, files[1].Content)
	assertNoTempResidue(t, filesDir)
	assertNoTempResidue(t, store.Root)
}

// TestCollectAdoptAbortCounted 经 collect.Runner 的整体跳过计数：收仓整体
// 跳过时投影快照不受影响（§3.2——收仓是纯增益操作，跳过只损失磁盘节约）。
func TestCollectAdoptAbortCounted(t *testing.T) {
	skipIfRoot(t)
	mustLib(t)
	e := newEnv(t)

	seed, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	ref := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, seed)

	// 全部游戏侧实体文件去读权限 → 首试探失败 → 整体跳过。
	nPhysical := 0
	err = filepath.WalkDir(filepath.Join(ref.DataDir, "files"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		nPhysical++
		return os.Chmod(p, 0o000)
	})
	if err != nil {
		t.Fatalf("chmod 注入: %v", err)
	}
	if nPhysical == 0 {
		t.Fatal("布置失败：files/ 无实体文件")
	}
	t.Cleanup(func() {
		filepath.WalkDir(filepath.Join(ref.DataDir, "files"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				os.Chmod(p, 0o644) //nolint:errcheck // TempDir 清理需要
			}
			return nil
		})
	})

	rep := e.collectInto(ref)
	if rep.FilesAdopted != 0 {
		t.Errorf("收仓成功数 = %d，期望 0（整体跳过）", rep.FilesAdopted)
	}
	if rep.FilesSkipped != nPhysical {
		t.Errorf("收仓跳过计数 = %d，期望 %d（全部实体文件整体跳过）", rep.FilesSkipped, nPhysical)
	}
	// 投影快照不受影响：实体与 presence 照常落库。
	if got := rep.Entities[hub.KindBeatmapSet]; got != 1 {
		t.Errorf("谱面集入仓 = %d，期望 1（投影不受收仓失败影响）", got)
	}
	if got := rep.Entities[hub.KindCollection]; got != 1 {
		t.Errorf("收藏入仓 = %d，期望 1", got)
	}
}
