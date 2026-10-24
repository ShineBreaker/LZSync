package filestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptDir(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	filesDir := filepath.Join(t.TempDir(), "files")

	// 3 个实体文件 + 1 个预先建好的链接（模拟上轮已收仓）
	type seeded struct {
		path, hash, content string
	}
	var seeds []seeded
	for _, c := range []string{"f1", "f2", "f3"} {
		p, h := seedGameFile(t, filesDir, c)
		seeds = append(seeds, seeded{p, h, c})
	}
	linked, linkedHash := seedGameFile(t, filesDir, "linked-already")
	os.Remove(linked)
	pre := filepath.Join(t.TempDir(), "pre")
	writeFile(t, pre, "linked-already")
	if err := s.Put(pre); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(s.Path(linkedHash), linked); err != nil {
		t.Fatal(err)
	}

	stats, err := e.AdoptDir(filesDir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Adopted != 3 || stats.Skipped != 1 || stats.Failed != 0 || stats.Aborted != 0 {
		t.Fatalf("stats = %+v, want Adopted=3 Skipped=1", stats)
	}
	if stats.Modes[LinkSymlink] != 3 {
		t.Fatalf("Modes = %v, want symlink×3", stats.Modes)
	}
	for _, sd := range seeds {
		if err := s.Verify(sd.hash); err != nil {
			t.Fatalf("仓副本 %s: %v", sd.hash, err)
		}
		if target, err := os.Readlink(sd.path); err != nil || target != s.Path(sd.hash) {
			t.Fatalf("原位应为链接: %q, %v", target, err)
		}
	}
	noTempResidue(t, filesDir)
	noTempResidue(t, s.Root)

	// 幂等重跑：上轮产物全为链接 → 全部 Skipped、仓不增长
	stats2, err := e.AdoptDir(filesDir)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Adopted != 0 || stats2.Skipped != 4 {
		t.Fatalf("重跑 stats = %+v, want Adopted=0 Skipped=4", stats2)
	}
	if got := countFiles(t, s.Root); got != 4 {
		t.Fatalf("仓内文件数 = %d, want 4", got)
	}
}

// 能力探测失败环境（模拟无特权 + 跨卷 + 磁盘满）→ 首文件试探失败 →
// 整体跳过并计数，文件全部留原地（§5.3 前置跳过 / §8.1-1）。
func TestAdoptDirAbortOnProbeFailureEnv(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	filesDir := filepath.Join(t.TempDir(), "files")
	contents := []string{"a", "b", "c"}
	var paths []string
	for _, c := range contents {
		p, _ := seedGameFile(t, filesDir, c)
		paths = append(paths, p)
	}
	// 三级形态全部不可用
	override(t, &fsSymlink, func(_, _ string) error { return errors.New("EPERM") })
	override(t, &fsHardlink, func(_, _ string) error { return errors.New("EXDEV") })
	override(t, &fsCopy, func(_, _ string) error { return errors.New("ENOSPC") })

	stats, err := e.AdoptDir(filesDir)
	if err != nil {
		t.Fatalf("整体跳过是合法终态，不应返回错误: %v", err)
	}
	if stats.Adopted != 0 || stats.Aborted != 3 || stats.AbortReason == "" {
		t.Fatalf("stats = %+v, want Adopted=0 Aborted=3 带原因", stats)
	}
	for i, p := range paths {
		assertIntact(t, p, contents[i])
	}
	if got := countFiles(t, s.Root); got != 0 {
		t.Fatalf("仓内文件数 = %d, want 0", got)
	}
	noTempResidue(t, filesDir)
}

// 非首文件的实体失败：计数 Failed 并继续收其余（每文件独立原子）。
// WalkDir 按字典序枚举 physical 文件——victim 是否恰好排首决定期望终态。
func TestAdoptDirSingleFailureContinues(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	filesDir := filepath.Join(t.TempDir(), "files")
	victim, victimHash := seedGameFile(t, filesDir, "victim")
	other, otherHash := seedGameFile(t, filesDir, "other")
	// 注入只让 victim 的入仓复制失败
	override(t, &fsCopy, func(dst, src string) error {
		if src == victim {
			return errors.New("EACCES")
		}
		return copyFileBytes(dst, src)
	})
	stats, err := e.AdoptDir(filesDir)
	if err != nil {
		t.Fatal(err)
	}
	// victim 完好留原地且仍为实体（两种终态下都必须成立）
	assertIntact(t, victim, "victim")
	if ok, _ := s.Has(victimHash); ok {
		t.Fatal("victim 不应入仓")
	}
	if victim < other {
		// victim 是首文件 → 试探失败 → 整体跳过
		if stats.Aborted != 2 || stats.Adopted != 0 {
			t.Fatalf("victim 首位: %+v, want Aborted=2 Adopted=0", stats)
		}
		return
	}
	// other 先收成功，victim 随后失败
	if stats.Adopted != 1 || stats.Failed != 1 || stats.Aborted != 0 {
		t.Fatalf("victim 次位: %+v, want Adopted=1 Failed=1", stats)
	}
	if target, err := os.Readlink(other); err != nil || target != s.Path(otherHash) {
		t.Fatalf("other 应已收仓: %q, %v", target, err)
	}
	noTempResidue(t, filesDir)
}

// 非规范布局项（乱放文件）不收、不计数、不报错。
func TestAdoptDirIgnoresNonCanonicalLayout(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	filesDir := filepath.Join(t.TempDir(), "files")
	writeFile(t, filepath.Join(filesDir, "loose-file.txt"), "junk")
	writeFile(t, filepath.Join(filesDir, "x", "not-hash"), "junk")
	stats, err := e.AdoptDir(filesDir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Adopted+stats.Skipped+stats.Failed+stats.Aborted != 0 {
		t.Fatalf("非规范项应被忽略: %+v", stats)
	}
}

// filesDir 不存在 → 扫描错误显式上报（调用方路径给错不应静默）。
func TestAdoptDirMissingDir(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	if _, err := e.AdoptDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("目录不存在应报错")
	}
}
