// Package filestore 是实体文件仓 + 链接引擎（design.md §3.1 的 store 模块、
// §5.3）：SHA-256 内容寻址、两级哈希布局（与四端官方 files/ 完全同构），
// 以及仓 ↔ 游戏侧 files/ 的链接分级（符号链接 → 硬链接 → 复制兜底）与
// 收仓回链（HB4 四步序列）。
package filestore

import "errors"

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("filestore: 契约桩未实现")

// Store 是内容寻址仓（hub 目录布局下的 store/，§3.4）。
type Store struct {
	Root string
}

// Open 打开（不存在则创建）仓目录 root 及其两级哈希子目录骨架。
func Open(root string) (*Store, error) {
	return nil, ErrNotImplemented
}

// Path 返回哈希的仓内规范路径：<root>/<h[0]>/<h[0:2]>/<hash>（两级哈希，
// 与官方 GetStoragePath 同构——哈希 ↔ 路径零换算，直拷/校验工具通用）。
func (s *Store) Path(hash string) string {
	return ""
}

// Has 报告仓内是否已持有该哈希的有效副本。
func (s *Store) Has(hash string) (bool, error) {
	return false, ErrNotImplemented
}

// Put 把 srcPath 的字节复制入仓（仓已有同哈希则跳过复制）。
// 只负责入仓；调用方侧的链接/原子替换是 Engine 的职责。
func (s *Store) Put(srcPath string) error {
	return ErrNotImplemented
}

// Verify 读回校验仓内该哈希文件的内容与哈希一致。
func (s *Store) Verify(hash string) error {
	return ErrNotImplemented
}
