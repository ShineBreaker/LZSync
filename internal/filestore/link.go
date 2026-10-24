package filestore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LinkMode 链接分级形态（§5.3 投放链接分级策略）。
type LinkMode string

const (
	LinkSymlink  LinkMode = "symlink"  // 首选：跨卷可用；Windows 需管理员或开发者模式
	LinkHardlink LinkMode = "hardlink" // 次选：仅同卷（探测仓与目标卷号一致），无特权要求
	LinkCopy     LinkMode = "copy"     // 兜底：跨卷且无符号链接特权时纯字节复制
)

// Capabilities 是链接能力探测结果（每 fork 首次投放/收仓前探测一次并缓存；
// 探测失败自动逐级降级到复制——功能不缺失、仅形态退化，§5.3 / T10）。
type Capabilities struct {
	Symlink  bool // 符号链接可建
	Hardlink bool // 硬链接可建（仓与游戏数据目录同卷）
}

// Engine 是链接引擎：在仓与游戏侧 files/ 之间建立/维护链接形态。
type Engine struct {
	Store *Store
	Caps  Capabilities // Probe 后填充
}

// NewEngine 创建引擎；使用前须先 Probe。
func NewEngine(s *Store) *Engine {
	return &Engine{Store: s}
}

// Probe 探测当前环境的链接能力（符号链接试建 + 仓/目标卷号比对）。
// 卷号一致性无法在无目标目录时预判，跨卷（EXDEV）由实际建链时的试建
// 降级兜底——此处硬链接探测验证的是「本环境可建硬链接」。结果同步写入
// e.Caps。
func (e *Engine) Probe() (Capabilities, error) {
	if e.Store == nil {
		return Capabilities{}, ErrNoStore
	}
	dir, err := os.MkdirTemp(e.Store.Root, ".probe-*")
	if err != nil {
		return Capabilities{}, fmt.Errorf("filestore: 建探测目录: %w", err)
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, []byte("probe"), 0o644); err != nil {
		return Capabilities{}, fmt.Errorf("filestore: 探测写样本: %w", err)
	}
	caps := Capabilities{}
	if err := fsSymlink(target, filepath.Join(dir, "l")); err == nil {
		caps.Symlink = true
	}
	if err := fsHardlink(target, filepath.Join(dir, "h")); err == nil {
		caps.Hardlink = true
	}
	e.Caps = caps
	return caps, nil
}

// placeLinked 执行 HB4 步骤③④：在 finalPath 同目录以临时名按分级建出
// 指向仓内 hash 的项（符号 → 硬 → 副本），读回校验可读且内容正确后
// rename 原子入位 finalPath（同目录 = 同卷 = 原子）。失败清理临时产物，
// finalPath 原有内容不受影响。
func (e *Engine) placeLinked(hash, finalPath string) (LinkMode, error) {
	storePath := e.Store.Path(hash)
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := tempSibling(dir)
	var mode LinkMode
	// 分级降级：符号 → 硬 → 副本。Caps 跳过已知不可用的级别；试建失败
	// （如跨卷 EXDEV）继续降级，全失败才算步骤③ 失败。
	switch {
	case e.Caps.Symlink && fsSymlink(storePath, tmp) == nil:
		mode = LinkSymlink
	case e.Caps.Hardlink && fsHardlink(storePath, tmp) == nil:
		mode = LinkHardlink
	default:
		if err := fsCopy(tmp, storePath); err != nil {
			os.Remove(tmp)
			return "", fmt.Errorf("filestore: 副本回填失败: %w", err)
		}
		mode = LinkCopy
	}
	// 步骤④ 前置校验：临时项必须可读且哈希一致（lazer「建链后读回校验」
	// 纪律，§5.3）
	got, err := hashFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("filestore: 临时项不可读: %w", err)
	}
	if got != hash {
		os.Remove(tmp)
		return "", fmt.Errorf("filestore: 临时项校验不符: 期望 %s 实得 %s", hash, got)
	}
	if err := fsRename(tmp, finalPath); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("filestore: 原子替换失败: %w", err)
	}
	return mode, nil
}

// existingMode 判定 gameFile 现有项是否已是 hash 的有效投放形态。
// ok=true 时返回其形态（跳过不覆盖）；ok=false 表示悬空/校验不符/指向
// 别处，需要重建。
func (e *Engine) existingMode(hash, gameFile string, info os.FileInfo) (LinkMode, bool, error) {
	storePath := e.Store.Path(hash)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(gameFile)
		if err != nil {
			return "", false, err
		}
		if target == storePath {
			return LinkSymlink, true, nil
		}
		return "", false, nil // 指向别处（含悬空）→ 重建
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("filestore: 游戏侧路径非常规文件: %s", gameFile)
	}
	// 与仓内副本同一 inode → 硬链接形态已就位（regular 文件的 Lstat 与
	// Stat 等价，直接用 info）
	if stStore, err := os.Stat(storePath); err == nil && os.SameFile(info, stStore) {
		return LinkHardlink, true, nil
	}
	// 哈希校验一致的实体 → 有效副本，跳过不覆盖（§5.3）
	got, err := hashFile(gameFile)
	if err != nil {
		return "", false, nil // 不可读 → 走重建
	}
	if got == hash {
		return LinkCopy, true, nil
	}
	return "", false, nil // 校验不符 → 重建
}

// EnsureLinked 投放方向的「建链接先行」（S6 链接形态）：确保游戏侧
// gameFile 是指向仓内 hash 的链接或有效副本——必须先于任何引用该哈希的
// realm 行写入落地。同哈希已在游戏侧且有效（链接或哈希校验一致的实体）
// → 跳过不覆盖；悬空链接或校验不符 → 重建并计数（§5.3，重建以「临时名
// 建好 + rename 替换」原子完成）。
func (e *Engine) EnsureLinked(hash, gameFile string) (LinkMode, error) {
	if e.Store == nil {
		return "", ErrNoStore
	}
	if !validHash(hash) {
		return "", ErrInvalidHash
	}
	// 前置：仓内必须已有有效副本（仓是唯一权威副本，§5.3）
	if err := e.Store.Verify(hash); err != nil {
		return "", fmt.Errorf("%w（%s）: %v", ErrNotInStore, hash, err)
	}
	info, err := os.Lstat(gameFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// 不存在 → 直接建
	case err != nil:
		return "", err
	default:
		mode, ok, err := e.existingMode(hash, gameFile, info)
		if err != nil {
			return "", err
		}
		if ok {
			return mode, nil // 已是有效形态，跳过不覆盖
		}
		// 悬空链接 / 校验不符 → 重建（投放方向此时尚无 realm 行引用此
		// 哈希，Remove 与 rename 之间的空窗无害）
		if err := os.Remove(gameFile); err != nil {
			return "", fmt.Errorf("filestore: 移除待重建项: %w", err)
		}
	}
	return e.placeLinked(hash, gameFile)
}

// AdoptFile 收仓 + 回链（HB4 四步序列，§5.3 收集方向）：对游戏侧非链接的
// 实体文件执行——
//  1. 复制入仓（仓已有同哈希则跳过复制）；
//  2. 哈希校验仓副本；
//  3. 原文件同目录以临时名建链接（分级降级：符号 → 硬 → 复制回填）；
//  4. 校验临时项可读后 rename 原子替换原文件（同目录 = 同卷 = 原子）。
//
// 失败闭环：任一步失败即清理临时产物，游戏侧原文件完好且仍在原位（从不
// 移动）、仓内无半成品——绝不存在「实体已在仓、游戏侧缺失」的中间态
// （该状态会使仍引用此哈希的 realm 行读流失败，谱面不可用）。步骤③④
// 失败时仓内已通过校验的完整副本保留（非半成品，重跑可跳过复制）。
//
// 幂等：入参若已是指向仓内有效副本的符号链接，直接返回其形态（重跑
// 保护——「链接文件不再被收仓」）。
func (e *Engine) AdoptFile(gameFile string) (LinkMode, error) {
	if e.Store == nil {
		return "", ErrNoStore
	}
	info, err := os.Lstat(gameFile)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(gameFile); err == nil {
			if base := filepath.Base(target); validHash(base) && target == e.Store.Path(base) {
				if err := e.Store.Verify(base); err == nil {
					return LinkSymlink, nil
				}
			}
		}
		return "", fmt.Errorf("%w: %s", ErrIsLink, gameFile)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("filestore: 收仓对象非实体文件: %s", gameFile)
	}
	// 步骤①②：复制入仓 + 哈希校验（copyIntoVerified 内含读回复核）
	hash, err := hashFile(gameFile)
	if err != nil {
		return "", err
	}
	if err := e.Store.put(hash, gameFile); err != nil {
		return "", err
	}
	// 步骤③④：同目录临时名建链接 → 读回校验 → rename 原子替换
	return e.placeLinked(hash, gameFile)
}
