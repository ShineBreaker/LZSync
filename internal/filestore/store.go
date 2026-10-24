// Package filestore 是实体文件仓 + 链接引擎（design.md §3.1 的 store 模块、
// §5.3）：SHA-256 内容寻址、两级哈希布局（与四端官方 files/ 完全同构），
// 以及仓 ↔ 游戏侧 files/ 的链接分级（符号链接 → 硬链接 → 复制兜底）与
// 收仓回链（HB4 四步序列）。
package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// 哨兵错误。
var (
	// ErrInvalidHash 哈希不是 SHA-256 小写 hex 规范形态（64 位）。
	ErrInvalidHash = errors.New("filestore: 非法哈希（须 64 位小写 hex）")
	// ErrNotInStore 仓内不存在该哈希的有效副本（投放建链的前置失败）。
	ErrNotInStore = errors.New("filestore: 仓内无此哈希")
	// ErrIsLink 收仓对象本身是符号链接且不指向仓内有效副本（四步序列只
	// 接受非链接实体文件，§5.3）。
	ErrIsLink = errors.New("filestore: 收仓对象是符号链接")
	// ErrNoStore 引擎未绑定仓。
	ErrNoStore = errors.New("filestore: 引擎未绑定仓")
)

const hexChars = "0123456789abcdef"

// Store 是内容寻址仓（hub 目录布局下的 store/，§3.4）。
type Store struct {
	Root string
}

// validHash 报告 h 是否为 SHA-256 小写 hex（与官方 files/ 文件名规范同构）。
func validHash(h string) bool {
	if len(h) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hashFile 流式计算 path 内容的 SHA-256（小写 hex）；符号链接跟随读目标。
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Open 打开（不存在则创建）仓目录 root 及其两级哈希子目录骨架
// （<h0>/<h01> 共 16×16=256 个叶目录，与官方 files/ 预建形态一致）。
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("filestore: 仓根路径为空")
	}
	for i := 0; i < len(hexChars); i++ {
		for j := 0; j < len(hexChars); j++ {
			// 二级目录名 = 哈希前两位，其首字符即一级目录名
			sub := filepath.Join(root,
				string(hexChars[i]), string([]byte{hexChars[i], hexChars[j]}))
			if err := os.MkdirAll(sub, 0o755); err != nil {
				return nil, fmt.Errorf("filestore: 建仓目录 %s: %w", sub, err)
			}
		}
	}
	return &Store{Root: root}, nil
}

// Path 返回哈希的仓内规范路径：<root>/<h[0]>/<h[0:2]>/<hash>（两级哈希，
// 与官方 GetStoragePath 同构——哈希 ↔ 路径零换算，直拷/校验工具通用）。
// 非规范哈希（非 64 位小写 hex）返回空串。
func (s *Store) Path(hash string) string {
	if !validHash(hash) {
		return ""
	}
	return filepath.Join(s.Root, hash[:1], hash[:2], hash)
}

// Has 报告仓内是否已持有该哈希的有效副本：存在且读侧哈希复核通过才算
// 「有」（§5.3 哈希校验）；坏副本视同缺失，调用方走重写路径。
func (s *Store) Has(hash string) (bool, error) {
	if !validHash(hash) {
		return false, ErrInvalidHash
	}
	p := s.Path(hash)
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	got, err := hashFile(p)
	if err != nil {
		return false, err
	}
	return got == hash, nil
}

// Put 把 srcPath 的字节复制入仓（仓已有同哈希有效副本则跳过复制）。
// 只负责入仓；调用方侧的链接/原子替换是 Engine 的职责。
// 写入即校验：复制走「同目录临时名 → 读回哈希复核 → rename 入位」，
// 失败不留仓半成品。
func (s *Store) Put(srcPath string) error {
	h, err := hashFile(srcPath)
	if err != nil {
		return err
	}
	return s.put(h, srcPath)
}

// put 是 Put 的已知哈希形态（AdoptFile 复用：先算哈希再入仓）。
func (s *Store) put(hash, srcPath string) error {
	if !validHash(hash) {
		return ErrInvalidHash
	}
	ok, err := s.Has(hash)
	if err != nil {
		return err
	}
	if ok {
		return nil // 内容寻址幂等：已有有效副本，跳过
	}
	_, err = copyIntoVerified(s.Path(hash), srcPath)
	return err
}

// Verify 读回校验仓内该哈希文件的内容与哈希一致（读侧哈希复核）。
func (s *Store) Verify(hash string) error {
	if !validHash(hash) {
		return ErrInvalidHash
	}
	p := s.Path(hash)
	got, err := hashFile(p)
	if err != nil {
		return fmt.Errorf("filestore: 读仓文件 %s: %w", p, err)
	}
	if got != hash {
		return fmt.Errorf("filestore: 仓副本哈希不符 %s: 实得 %s", hash, got)
	}
	return nil
}
