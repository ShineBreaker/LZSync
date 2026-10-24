package filestore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// override 替换可注入原语并在测试结束后恢复（失败注入点，HB4 各步）。
func override(t *testing.T, dst *func(string, string) error, fn func(string, string) error) {
	t.Helper()
	old := *dst
	*dst = fn
	t.Cleanup(func() { *dst = old })
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	h, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile(%s): %v", path, err)
	}
	return h
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gamePath 计算哈希在游戏侧 files/ 布局中的规范路径（与仓同构）。
func gamePath(filesDir, hash string) string {
	return filepath.Join(filesDir, hash[:1], hash[:2], hash)
}

// noTempResidue 断言 root 下无临时产物残留（HB4「清理临时产物」）。
func noTempResidue(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(info.Name(), ".lzsync-tmp-") {
			t.Errorf("临时产物残留: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// countFiles 统计 root 下的普通文件数。
func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// assertIntact 断言游戏侧原文件字节完好、仍是原位实体文件（非链接）。
func assertIntact(t *testing.T, path, want string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("原文件丢失: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("原文件不再是实体文件: mode=%v", info.Mode())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("原文件字节损坏: 期望 %q 实得 %q", want, string(got))
	}
}

func TestPathLayout(t *testing.T) {
	s := &Store{Root: "/store"}
	want := filepath.Join("/store", hashA[:1], hashA[:2], hashA)
	if got := s.Path(hashA); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "ABC", strings.ToUpper(hashA), hashA[:63], hashA + "x"} {
		if got := s.Path(bad); got != "" {
			t.Fatalf("Path(%q) = %q, want 空串（非法哈希）", bad, got)
		}
	}
}

func TestOpenSkeleton(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root != root {
		t.Fatalf("Root = %q, want %q", s.Root, root)
	}
	// 16×16=256 个叶目录
	n := 0
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if info.IsDir() {
			n++
		}
		return nil
	})
	if n != 1+16+256 {
		t.Fatalf("骨架目录数 = %d, want 273（根+16+256）", n)
	}
}

func TestPutHasVerifyRoundtrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "beatmap.osu")
	writeFile(t, src, "hello lzsync")
	if err := s.Put(src); err != nil {
		t.Fatal(err)
	}
	h := mustHash(t, src)
	ok, err := s.Has(h)
	if err != nil || !ok {
		t.Fatalf("Has = %v, %v; want true, nil", ok, err)
	}
	if err := s.Verify(h); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	got, err := os.ReadFile(s.Path(h))
	if err != nil || string(got) != "hello lzsync" {
		t.Fatalf("仓内容不符: %q, %v", string(got), err)
	}
}

func TestPutSkipsExistingValidCopy(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store"))
	src := filepath.Join(t.TempDir(), "a")
	writeFile(t, src, "v1")
	if err := s.Put(src); err != nil {
		t.Fatal(err)
	}
	// 已有有效副本 → 同哈希再 Put 不触发复制（内容寻址幂等）
	calls := 0
	override(t, &fsCopy, func(dst, src string) error {
		calls++
		return copyFileBytes(dst, src)
	})
	if err := s.Put(src); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("已有有效副本应跳过复制，实际复制 %d 次", calls)
	}
}

func TestHasDetectsCorruption(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store"))
	src := filepath.Join(t.TempDir(), "f")
	writeFile(t, src, "integrity")
	s.Put(src)
	h := mustHash(t, src)
	// 直接篡改仓内字节（模拟存储腐烂）→ 读侧哈希复核视同缺失
	writeFile(t, s.Path(h), "corrupted!!")
	ok, err := s.Has(h)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("坏副本应被 Has 判为无效")
	}
	if err := s.Verify(h); err == nil {
		t.Fatal("Verify 应报告坏副本")
	}
	// 重写路径：Has=false 后 Put 应重写为有效副本
	writeFile(t, src, "integrity")
	if err := s.Put(src); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(h); err != nil {
		t.Fatalf("重写后 Verify: %v", err)
	}
}

func TestPutVerifiesWriteRejectsCorruptedCopy(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store"))
	src := filepath.Join(t.TempDir(), "f")
	writeFile(t, src, "payload")
	// 注入「复制成功返回但写出损坏字节」→ 写入即校验必须拦截
	override(t, &fsCopy, func(dst, src string) error {
		if err := copyFileBytes(dst, src); err != nil {
			return err
		}
		return os.WriteFile(dst, []byte("garbage-on-write"), 0o644)
	})
	err := s.Put(src)
	if err == nil {
		t.Fatal("损坏复制应被写入即校验拦截")
	}
	h := mustHash(t, src)
	if ok, _ := s.Has(h); ok {
		t.Fatal("仓内不应留下坏副本")
	}
	noTempResidue(t, s.Root)
	if got := countFiles(t, s.Root); got != 0 {
		t.Fatalf("仓内文件数 = %d, want 0（无半成品）", got)
	}
}

func TestInvalidHashRejected(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store"))
	if _, err := s.Has("not-a-hash"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Has 非法哈希: %v", err)
	}
	if err := s.Verify("NOT-A-HASH"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Verify 非法哈希: %v", err)
	}
	if err := s.put("short", "/tmp/x"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("put 非法哈希: %v", err)
	}
}

// hashA 是布局样例哈希（合法 64 位小写 hex，不必对应真实内容）。
var hashA = "a" + strings.Repeat("0", 62) + "1"
