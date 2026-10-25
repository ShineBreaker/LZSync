package filestore

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AdoptStats 收仓批量结果（HB4 计数口径；collect.Report 文件侧的输入）。
type AdoptStats struct {
	Adopted     int              // 成功收仓数
	Failed      int              // 单文件四步失败数（原文件完好留原地）
	Skipped     int              // 符号链接跳过数（幂等重跑的正常路径）
	Aborted     int              // 首文件试探失败后整体跳过的实体文件数
	AbortReason string           // 试探失败原因（空 = 未中止）
	Modes       map[LinkMode]int // 成功收仓的最终形态分布（降级统计）
}

// AdoptDir 扫描 filesDir（两级哈希布局）收仓全部非链接实体文件（§3.2 收集
// 方向③）：先 Probe 链接能力（决定建链分级起点；探测出的能力只影响形态，
// 全不可用时自然降级到复制），再以首个实体文件做试探——试探失败（覆盖
// 「复制也失败」的降级环境：磁盘满/权限缺失，§5.3 前置跳过）→ 剩余实体
// 文件整体跳过并计数（文件全部留原地，收仓是纯增益操作，跳过只损失链接
// 形态的磁盘节约，不损失数据）。试探成功后逐文件执行四步序列，单文件
// 失败只计数不中止（每文件独立原子）。
func (e *Engine) AdoptDir(filesDir string) (AdoptStats, error) {
	var stats AdoptStats
	if e.Store == nil {
		return stats, ErrNoStore
	}
	if _, err := e.Probe(); err != nil {
		return stats, fmt.Errorf("filestore: 收仓前能力探测: %w", err)
	}
	// 只收两级哈希布局深度的规范名文件（<h0>/<h01>/<hash>）；符号链接
	// 跳过（幂等：已收仓的文件呈链接形态，不再被收仓）
	var physical []string
	err := filepath.WalkDir(filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filesDir, p)
		if err != nil {
			return err
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 || !validHash(parts[2]) || parts[1] != parts[2][:2] || parts[0] != parts[2][:1] {
			return nil // 非规范布局项：不属于本引擎管辖，忽略
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			stats.Skipped++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil // 套接字/FIFO 等：不收
		}
		physical = append(physical, p)
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("filestore: 扫描 files/ %s: %w", filesDir, err)
	}
	stats.Modes = map[LinkMode]int{}
	for i, p := range physical {
		mode, err := e.AdoptFile(p)
		if err != nil {
			if i == 0 {
				// 首文件试探失败：降级环境 → 整体跳过并计数（§5.3）
				stats.Aborted = len(physical)
				stats.AbortReason = err.Error()
				return stats, nil
			}
			stats.Failed++
			continue
		}
		stats.Adopted++
		stats.Modes[mode]++
	}
	return stats, nil
}

// IngestStats 入仓批量结果（collect.ModeSourceOnly 的文件侧口径，与
// AdoptStats 同风格）。
type IngestStats struct {
	Ingested int // 新入仓文件数
	Existing int // 仓内已有有效副本（跳过复制）
	Failed   int // 单文件失败数（原文件不动）
	Skipped  int // 符号链接跳过数（已收仓形态）
}

// IngestDir 把 filesDir（两级哈希布局）内全部非链接实体文件复制进仓，
// 绝不改动 fork 侧文件（§0 角色模型「仅来源」端：fork 的 files/ 保持普通
// 文件且字节不变——与 AdoptDir 的回链写原地不同，本函数只入仓）。
// 符号链接跳过（已收仓形态）；单文件失败只计数不中止。复制走 Store 的
// 「同目录临时名 → 读回哈希复核 → rename」纪律，仓内不留半成品。
func (e *Engine) IngestDir(filesDir string) (IngestStats, error) {
	var stats IngestStats
	if e.Store == nil {
		return stats, ErrNoStore
	}
	var physical []string
	err := filepath.WalkDir(filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filesDir, p)
		if err != nil {
			return err
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 || !validHash(parts[2]) || parts[1] != parts[2][:2] || parts[0] != parts[2][:1] {
			return nil // 非规范布局项：不属于本引擎管辖，忽略
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			stats.Skipped++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil // 套接字/FIFO 等：不收
		}
		physical = append(physical, p)
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("filestore: 扫描 files/ %s: %w", filesDir, err)
	}
	for _, p := range physical {
		h, err := hashFile(p)
		if err != nil {
			stats.Failed++
			continue
		}
		// 已有有效副本 → 跳过复制；坏副本/缺副本 → 写入即校验的复制。
		exists := false
		if _, err := os.Stat(e.Store.Path(h)); err == nil {
			exists = true
		}
		if err := e.Store.put(h, p); err != nil {
			stats.Failed++
			continue
		}
		if exists {
			stats.Existing++
		} else {
			stats.Ingested++
		}
	}
	return stats, nil
}
