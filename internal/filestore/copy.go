package filestore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResolveFilesDir 按 §5.3 三级回退解析 fork 库的 files/ 目录（ezrealmsync-
// design §2.6）：库所在目录 files/ → 所在目录名是 data 时取兄弟 files/ →
// 父目录 files/。返回第一个实际存在的候选；全部不存在时返回首选候选
// （库旁 files/，由调用方按需创建）。
func ResolveFilesDir(realmPath string) string {
	libDir := filepath.Dir(realmPath)
	parentFiles := filepath.Join(filepath.Dir(libDir), "files")
	first := filepath.Join(libDir, "files")
	candidates := []string{first, parentFiles}
	for i, c := range candidates {
		if i > 0 && c == candidates[0] {
			continue // 去重：data 情形下兄弟候选与父级候选同路
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return first
}

// CopyMissing 按哈希集合把 srcRoot（两级哈希布局）中的实体搬运到 dstRoot：
// 同根（srcRoot == dstRoot）整体 no-op；目标已存在该哈希跳过（悬空链接
// 视同缺失，会被复制的原子入位替换）；否则「同目录临时名 → 读回校验 →
// rename」原子复制入位（ezrealmsync-design §2.6 CopyMissing 模式）。
// 返回本次实际复制的文件数；源缺失对应哈希时中止并返回错误。
func CopyMissing(srcRoot, dstRoot string, hashes []string) (int, error) {
	srcRoot, dstRoot = filepath.Clean(srcRoot), filepath.Clean(dstRoot)
	if srcRoot == dstRoot {
		return 0, nil // 同根 no-op
	}
	copied := 0
	for _, h := range hashes {
		if !validHash(h) {
			return copied, ErrInvalidHash
		}
		dst := filepath.Join(dstRoot, h[:1], h[:2], h)
		if _, err := os.Stat(dst); err == nil {
			continue // 目标已有即跳过
		} else if !errors.Is(err, os.ErrNotExist) {
			return copied, err
		}
		src := filepath.Join(srcRoot, h[:1], h[:2], h)
		if _, err := copyIntoVerified(dst, src); err != nil {
			return copied, fmt.Errorf("filestore: 搬运 %s: %w", h, err)
		}
		copied++
	}
	return copied, nil
}
