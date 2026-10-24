package filestore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// 文件系统原语的注入缝（仅供单测失败注入，对应 HB4 四步序列的各步）：
// fsCopy 覆盖步骤① 复制入仓与步骤③ 副本回填；fsSymlink/fsHardlink 覆盖
// 步骤③ 建链；fsRename 覆盖步骤④ 原子替换。
var (
	fsCopy     = copyFileBytes
	fsSymlink  = os.Symlink
	fsHardlink = os.Link
	fsRename   = os.Rename
)

// copyFileBytes 把 src 的字节直接复制到 dst（不建临时名、不校验——校验与
// 原子性由 copyIntoVerified 负责）。
func copyFileBytes(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// tempSibling 在 dir 内生成唯一临时名（点前缀：游戏侧目录枚举不误认）。
func tempSibling(dir string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand 系统熵源失败不可恢复
	}
	return filepath.Join(dir, ".lzsync-tmp-"+hex.EncodeToString(b[:]))
}

// copyIntoVerified 把 src 的字节原子搬运到 dstPath：先写同目录临时名，
// 读回复核哈希与源一致后 rename 入位（写入即校验）。失败时清理临时产物，
// dstPath 不受影响——不留半成品（HB4）。
func copyIntoVerified(dstPath, srcPath string) (string, error) {
	// 期望哈希取自源内容：复制后临时文件须与之逐位一致（防复制损坏、
	// 以及源在复制窗口内被改写）
	want, err := hashFile(srcPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := tempSibling(dir)
	if err := fsCopy(tmp, srcPath); err != nil {
		os.Remove(tmp)
		return "", err
	}
	got, err := hashFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if got != want {
		os.Remove(tmp)
		return "", fmt.Errorf("filestore: 复制校验不符 %s: 期望 %s 实得 %s", srcPath, want, got)
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return want, nil
}
