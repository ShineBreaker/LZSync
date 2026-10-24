package filestore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newStore 开临时仓。
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedGameFile 在游戏侧 files/ 布局写入一个实体文件，返回 (路径, 哈希)。
func seedGameFile(t *testing.T, filesDir, content string) (string, string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "seed")
	writeFile(t, tmp, content)
	h := mustHash(t, tmp)
	dst := gamePath(filesDir, h)
	writeFile(t, dst, content)
	return dst, h
}

func TestProbe(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	caps, err := e.Probe()
	if err != nil {
		t.Fatal(err)
	}
	// Linux 临时目录：符号链接与硬链接均应可用
	if !caps.Symlink || !caps.Hardlink {
		t.Fatalf("Probe = %+v, want 双 true（Linux）", caps)
	}
	if e.Caps != caps {
		t.Fatal("Probe 结果未写入 e.Caps")
	}
	// 模拟「无符号链接特权」环境 → Symlink 降级为 false
	override(t, &fsSymlink, func(_, _ string) error {
		return errors.New("EPERM: 无特权")
	})
	caps, err = e.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if caps.Symlink || !caps.Hardlink {
		t.Fatalf("无特权环境 Probe = %+v, want Symlink=false Hardlink=true", caps)
	}
}

func TestEnsureLinkedCreatesSymlink(t *testing.T) {
	s := newStore(t)
	src := filepath.Join(t.TempDir(), "skin")
	writeFile(t, src, "skin-bytes")
	if err := s.Put(src); err != nil {
		t.Fatal(err)
	}
	h := mustHash(t, src)
	e := NewEngine(s)
	if _, err := e.Probe(); err != nil {
		t.Fatal(err)
	}
	filesDir := filepath.Join(t.TempDir(), "files")
	game := gamePath(filesDir, h)
	mode, err := e.EnsureLinked(h, game)
	if err != nil {
		t.Fatal(err)
	}
	if mode != LinkSymlink {
		t.Fatalf("mode = %q, want symlink", mode)
	}
	// 链接指向仓内绝对路径，读回内容一致（建链后读回校验）
	target, err := os.Readlink(game)
	if err != nil {
		t.Fatal(err)
	}
	if target != s.Path(h) {
		t.Fatalf("链接目标 = %q, want %q", target, s.Path(h))
	}
	got, err := os.ReadFile(game)
	if err != nil || string(got) != "skin-bytes" {
		t.Fatalf("读回不符: %q, %v", string(got), err)
	}
}

func TestEnsureLinkedSkipsValidExisting(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")

	// 三种形态的哈希都先入仓（仓是唯一权威副本，EnsureLinked 前置）
	for _, c := range []string{"one", "two", "three"} {
		src := filepath.Join(t.TempDir(), c)
		writeFile(t, src, c)
		if err := s.Put(src); err != nil {
			t.Fatal(err)
		}
	}

	// 形态一：有效符号链接 → 跳过
	p1, h1 := seedGameFile(t, filesDir, "one")
	if err := os.Remove(p1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(s.Path(h1), p1); err != nil {
		t.Fatal(err)
	}
	if mode, err := e.EnsureLinked(h1, p1); err != nil || mode != LinkSymlink {
		t.Fatalf("有效链接应跳过: %q, %v", mode, err)
	}

	// 形态二：哈希一致的实体副本 → 跳过不覆盖
	p2, h2 := seedGameFile(t, filesDir, "two")
	if mode, err := e.EnsureLinked(h2, p2); err != nil || mode != LinkCopy {
		t.Fatalf("一致实体应跳过: %q, %v", mode, err)
	}
	if info, err := os.Lstat(p2); err != nil || !info.Mode().IsRegular() {
		t.Fatal("一致实体不应被覆盖为链接")
	}

	// 形态三：与仓同 inode 的硬链接 → 跳过
	p3, h3 := seedGameFile(t, filesDir, "three")
	if err := os.Remove(p3); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(s.Path(h3), p3); err != nil {
		t.Fatal(err)
	}
	if mode, err := e.EnsureLinked(h3, p3); err != nil || mode != LinkHardlink {
		t.Fatalf("硬链接形态应跳过: %q, %v", mode, err)
	}
}

func TestEnsureLinkedRebuildsDangling(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	_, h := seedGameFile(t, filesDir, "dangling-target")
	game := gamePath(filesDir, h)
	// 悬空链接：指向仓内尚不存在的路径
	if err := os.Remove(game); err != nil {
		t.Fatal(err)
	}
	// 先把实体收进仓（不经 AdoptFile，直接 Put 源文件）
	src := filepath.Join(t.TempDir(), "src")
	writeFile(t, src, "dangling-target")
	if err := s.Put(src); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(s.Path(h), game); err != nil {
		t.Fatal(err)
	}
	mode, err := e.EnsureLinked(h, game)
	if err != nil {
		t.Fatal(err)
	}
	if mode != LinkSymlink {
		t.Fatalf("悬空重建后 mode = %q, want symlink", mode)
	}
	if got, err := os.ReadFile(game); err != nil || string(got) != "dangling-target" {
		t.Fatalf("重建后读回不符: %q, %v", string(got), err)
	}
}

func TestEnsureLinkedRebuildsMismatched(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	// 游戏侧实体内容与哈希不符（旧版本残留/损坏）
	_, h := seedGameFile(t, filesDir, "correct")
	src := filepath.Join(t.TempDir(), "src")
	writeFile(t, src, "correct")
	s.Put(src)
	writeFile(t, gamePath(filesDir, h), "stale-mismatch")
	mode, err := e.EnsureLinked(h, gamePath(filesDir, h))
	if err != nil {
		t.Fatal(err)
	}
	if mode != LinkSymlink {
		t.Fatalf("不符实体重建后 mode = %q, want symlink", mode)
	}
	if got, _ := os.ReadFile(gamePath(filesDir, h)); string(got) != "correct" {
		t.Fatalf("重建后内容不符: %q", string(got))
	}
}

func TestEnsureLinkedDegradation(t *testing.T) {
	s := newStore(t)
	src := filepath.Join(t.TempDir(), "f")
	writeFile(t, src, "degrade")
	s.Put(src)
	h := mustHash(t, src)

	// 符号链接不可用 → 降级硬链接
	e1 := NewEngine(s)
	override(t, &fsSymlink, func(_, _ string) error { return errors.New("EPERM") })
	e1.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	mode, err := e1.EnsureLinked(h, gamePath(filesDir, h))
	if err != nil || mode != LinkHardlink {
		t.Fatalf("降级硬链接: %q, %v", mode, err)
	}

	// 符号链接与硬链接都不可用 → 降级复制
	e2 := NewEngine(s)
	override(t, &fsSymlink, func(_, _ string) error { return errors.New("EPERM") })
	override(t, &fsHardlink, func(_, _ string) error { return errors.New("EXDEV 跨卷") })
	e2.Probe()
	filesDir2 := filepath.Join(t.TempDir(), "files2")
	mode, err = e2.EnsureLinked(h, gamePath(filesDir2, h))
	if err != nil || mode != LinkCopy {
		t.Fatalf("降级复制: %q, %v", mode, err)
	}
	if got, _ := os.ReadFile(gamePath(filesDir2, h)); string(got) != "degrade" {
		t.Fatalf("副本内容不符: %q", string(got))
	}
}

func TestEnsureLinkedRequiresStoreCopy(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	// 仓内没有该哈希 → 前置失败（仓是唯一权威副本）
	_, err := e.EnsureLinked(hashA, gamePath(t.TempDir(), hashA))
	if !errors.Is(err, ErrNotInStore) {
		t.Fatalf("err = %v, want ErrNotInStore", err)
	}
}

func TestAdoptFileFourSteps(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	if _, err := e.Probe(); err != nil {
		t.Fatal(err)
	}
	filesDir := filepath.Join(t.TempDir(), "files")
	game, h := seedGameFile(t, filesDir, "new-import")

	mode, err := e.AdoptFile(game)
	if err != nil {
		t.Fatal(err)
	}
	if mode != LinkSymlink {
		t.Fatalf("mode = %q, want symlink", mode)
	}
	// ①② 仓内已有校验一致的副本
	if err := s.Verify(h); err != nil {
		t.Fatalf("仓副本: %v", err)
	}
	// ③④ 原位被链接原子替换，读回内容一致
	if target, err := os.Readlink(game); err != nil || target != s.Path(h) {
		t.Fatalf("原位应为指向仓的链接: %q, %v", target, err)
	}
	if got, err := os.ReadFile(game); err != nil || string(got) != "new-import" {
		t.Fatalf("读回不符: %q, %v", string(got), err)
	}
	noTempResidue(t, filesDir)
	noTempResidue(t, s.Root)
}

func TestAdoptFileIdempotentOnLink(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	game, _ := seedGameFile(t, filesDir, "rerun")
	if _, err := e.AdoptFile(game); err != nil {
		t.Fatal(err)
	}
	// 二次收集：链接文件不再被收仓，直接返回既有形态
	mode, err := e.AdoptFile(game)
	if err != nil || mode != LinkSymlink {
		t.Fatalf("幂等重跑: %q, %v", mode, err)
	}
	if got := countFiles(t, s.Root); got != 1 {
		t.Fatalf("仓内文件数 = %d, want 1", got)
	}
}

func TestAdoptFileRejectsForeignLink(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	// 指向仓外目标的链接不属于收仓对象
	link := filepath.Join(filesDir, "foreign")
	writeFile(t, filepath.Join(filesDir, "real"), "x")
	if err := os.Symlink(filepath.Join(filesDir, "real"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AdoptFile(link); !errors.Is(err, ErrIsLink) {
		t.Fatalf("err = %v, want ErrIsLink", err)
	}
}

// --- HB4 四步序列失败注入点 ---

// 步骤① 复制入仓失败（如源不可读/磁盘满）→ 原文件完好原位、仓无半成品。
func TestAdoptFileStep1CopyFails(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	game, _ := seedGameFile(t, filesDir, "step1")
	override(t, &fsCopy, func(_, _ string) error {
		return errors.New("ENOSPC: 磁盘满")
	})
	_, err := e.AdoptFile(game)
	if err == nil {
		t.Fatal("复制失败应返回错误")
	}
	assertIntact(t, game, "step1")
	noTempResidue(t, s.Root)
	if got := countFiles(t, s.Root); got != 0 {
		t.Fatalf("仓内文件数 = %d, want 0（无半成品）", got)
	}
	noTempResidue(t, filesDir)
}

// 步骤② 哈希校验失败（复制过程损坏）→ 清理仓半成品、原文件未动。
func TestAdoptFileStep2VerifyFails(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	game, _ := seedGameFile(t, filesDir, "step2")
	override(t, &fsCopy, func(dst, src string) error {
		if err := copyFileBytes(dst, src); err != nil {
			return err
		}
		// 写出损坏字节但谎报成功
		return os.WriteFile(dst, []byte("bitrot"), 0o644)
	})
	_, err := e.AdoptFile(game)
	if err == nil {
		t.Fatal("损坏复制应被步骤② 校验拦截")
	}
	assertIntact(t, game, "step2")
	noTempResidue(t, s.Root)
	if got := countFiles(t, s.Root); got != 0 {
		t.Fatalf("仓内文件数 = %d, want 0（半成品已清理）", got)
	}
}

// 步骤③ 建链三级全失败（无特权 + 跨卷 + 副本回填也失败）→ 原文件完好、
// 临时产物清理；仓内已校验的完整副本保留（非半成品——游戏侧原文件仍在，
// 不构成「实体在仓、游戏侧缺失」）。
func TestAdoptFileStep3AllLinkFormsFail(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	game, h := seedGameFile(t, filesDir, "step3")
	override(t, &fsSymlink, func(_, _ string) error { return errors.New("EPERM") })
	override(t, &fsHardlink, func(_, _ string) error { return errors.New("EXDEV") })
	override(t, &fsCopy, func(dst, src string) error {
		if strings.Contains(dst, filesDir) { // 只让游戏侧副本回填失败
			return errors.New("EACCES: 目标不可写")
		}
		return copyFileBytes(dst, src) // 入仓复制不受影响
	})
	_, err := e.AdoptFile(game)
	if err == nil {
		t.Fatal("三级建链全失败应返回错误")
	}
	assertIntact(t, game, "step3")
	noTempResidue(t, filesDir)
	if err := s.Verify(h); err != nil {
		t.Fatalf("仓内完整副本应保留: %v", err)
	}
}

// 步骤③ 副本回填写出损坏字节 → 临时项读回校验拦截 → 原文件完好。
func TestAdoptFileStep3CopyGarbageRejected(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	game, _ := seedGameFile(t, filesDir, "step3g")
	override(t, &fsSymlink, func(_, _ string) error { return errors.New("EPERM") })
	override(t, &fsHardlink, func(_, _ string) error { return errors.New("EXDEV") })
	override(t, &fsCopy, func(dst, src string) error {
		if strings.Contains(dst, filesDir) {
			return os.WriteFile(dst, []byte("wrong-bytes"), 0o644)
		}
		return copyFileBytes(dst, src)
	})
	_, err := e.AdoptFile(game)
	if err == nil {
		t.Fatal("损坏的临时项应被读回校验拦截")
	}
	assertIntact(t, game, "step3g")
	noTempResidue(t, filesDir)
}

// 步骤④ rename 原子替换失败 → 原文件完好原位、临时项清理。
func TestAdoptFileStep4RenameFails(t *testing.T) {
	s := newStore(t)
	e := NewEngine(s)
	e.Probe()
	filesDir := filepath.Join(t.TempDir(), "files")
	game, _ := seedGameFile(t, filesDir, "step4")
	override(t, &fsRename, func(_, _ string) error {
		return errors.New("EIO: rename 失败")
	})
	_, err := e.AdoptFile(game)
	if err == nil {
		t.Fatal("rename 失败应返回错误")
	}
	assertIntact(t, game, "step4")
	noTempResidue(t, filesDir)
}
