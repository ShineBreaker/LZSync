package filestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFilesDir(t *testing.T) {
	root := t.TempDir()

	// 一级：库旁 files/
	lib := filepath.Join(root, "client.realm")
	os.WriteFile(lib, nil, 0o644)
	want := filepath.Join(root, "files")
	os.MkdirAll(want, 0o755)
	if got := ResolveFilesDir(lib); got != want {
		t.Fatalf("库旁回退 = %q, want %q", got, want)
	}

	// 二级：data 目录的兄弟 files/（库旁 files/ 不存在时）
	root2 := t.TempDir()
	lib2 := filepath.Join(root2, "data", "client.realm")
	os.MkdirAll(filepath.Join(root2, "data"), 0o755)
	os.WriteFile(lib2, nil, 0o644)
	os.MkdirAll(filepath.Join(root2, "files"), 0o755)
	if got := ResolveFilesDir(lib2); got != filepath.Join(root2, "files") {
		t.Fatalf("data 兄弟回退 = %q, want %q", got, filepath.Join(root2, "files"))
	}

	// 三级：父目录 files/（data 兄弟与库旁都不存在时）
	root3 := t.TempDir()
	lib3 := filepath.Join(root3, "sub", "client.realm")
	os.MkdirAll(filepath.Join(root3, "sub"), 0o755)
	os.WriteFile(lib3, nil, 0o644)
	os.MkdirAll(filepath.Join(root3, "files"), 0o755)
	if got := ResolveFilesDir(lib3); got != filepath.Join(root3, "files") {
		t.Fatalf("父级回退 = %q, want %q", got, filepath.Join(root3, "files"))
	}

	// 全部不存在 → 返回首选候选（库旁 files/）
	root4 := t.TempDir()
	lib4 := filepath.Join(root4, "client.realm")
	os.WriteFile(lib4, nil, 0o644)
	if got := ResolveFilesDir(lib4); got != filepath.Join(root4, "files") {
		t.Fatalf("全缺失回退 = %q, want %q", got, filepath.Join(root4, "files"))
	}
}

func TestCopyMissing(t *testing.T) {
	src := newStore(t)
	dst, err := Open(filepath.Join(t.TempDir(), "dst-files"))
	if err != nil {
		t.Fatal(err)
	}
	// 造 3 个源实体
	var hashes []string
	contents := map[string]string{}
	for _, c := range []string{"alpha", "beta", "gamma"} {
		p := filepath.Join(t.TempDir(), c)
		writeFile(t, p, c)
		if err := src.Put(p); err != nil {
			t.Fatal(err)
		}
		h := mustHash(t, p)
		hashes = append(hashes, h)
		contents[h] = c
	}
	// 预置：目标已有 beta（跳过）、beta 位置换成悬空链接的另存场景由
	// beta 自身覆盖——先单独验证「已有跳过」
	n, err := CopyMissing(src.Root, dst.Root, hashes)
	if err != nil || n != 3 {
		t.Fatalf("首轮 CopyMissing = %d, %v; want 3, nil", n, err)
	}
	for h, c := range contents {
		if got, err := os.ReadFile(filepath.Join(dst.Root, h[:1], h[:2], h)); err != nil || string(got) != c {
			t.Fatalf("搬运内容不符 %s: %q, %v", h, string(got), err)
		}
	}
	// 二轮：全部已有 → 0
	if n, err := CopyMissing(src.Root, dst.Root, hashes); err != nil || n != 0 {
		t.Fatalf("二轮 CopyMissing = %d, %v; want 0, nil（已有跳过）", n, err)
	}
}

func TestCopyMissingSameRootNoOp(t *testing.T) {
	s := newStore(t)
	p := filepath.Join(t.TempDir(), "f")
	writeFile(t, p, "noop")
	s.Put(p)
	h := mustHash(t, p)
	n, err := CopyMissing(s.Root, s.Root, []string{h})
	if err != nil || n != 0 {
		t.Fatalf("同根 CopyMissing = %d, %v; want 0, nil", n, err)
	}
	// 路径形态不同但指向同一目录（尾斜杠/Clean 等价）同样视为同根
	n, err = CopyMissing(s.Root, s.Root+string(filepath.Separator), []string{h})
	if err != nil || n != 0 {
		t.Fatalf("等价路径同根 = %d, %v; want 0, nil", n, err)
	}
}

func TestCopyMissingReplacesDanglingLink(t *testing.T) {
	src := newStore(t)
	dst, _ := Open(filepath.Join(t.TempDir(), "dst"))
	p := filepath.Join(t.TempDir(), "f")
	writeFile(t, p, "real")
	if err := src.Put(p); err != nil {
		t.Fatal(err)
	}
	h := mustHash(t, p)
	// 目标位置是悬空链接（指向不存在的路径）→ 视同缺失，被原子替换
	dangling := filepath.Join(dst.Root, h[:1], h[:2], h)
	os.MkdirAll(filepath.Dir(dangling), 0o755)
	os.Symlink(filepath.Join(t.TempDir(), "nowhere"), dangling)
	n, err := CopyMissing(src.Root, dst.Root, []string{h})
	if err != nil || n != 1 {
		t.Fatalf("悬空替换 = %d, %v; want 1, nil", n, err)
	}
	if got, err := os.ReadFile(dangling); err != nil || string(got) != "real" {
		t.Fatalf("替换后内容不符: %q, %v", string(got), err)
	}
}

func TestCopyMissingMissingSource(t *testing.T) {
	src := newStore(t)
	dst, _ := Open(filepath.Join(t.TempDir(), "dst"))
	p := filepath.Join(t.TempDir(), "f")
	writeFile(t, p, "keep")
	src.Put(p)
	h := mustHash(t, p)
	ghost := "c" + h[1:] // 合法形态但仓内不存在
	n, err := CopyMissing(src.Root, dst.Root, []string{h, ghost})
	if err == nil {
		t.Fatal("源缺失应报错")
	}
	if n != 1 {
		t.Fatalf("中止前已复制 = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dst.Root, ghost[:1], ghost[:2], ghost)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("缺失哈希不应在目标留任何产物")
	}
}
