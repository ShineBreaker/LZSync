package filestore

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
func (e *Engine) Probe() (Capabilities, error) {
	return Capabilities{}, ErrNotImplemented
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
// （该状态会使仍引用此哈希的 realm 行读流失败，谱面不可用）。
func (e *Engine) AdoptFile(gameFile string) (LinkMode, error) {
	return "", ErrNotImplemented
}

// EnsureLinked 投放方向的「建链接先行」（S6 链接形态）：确保游戏侧
// gameFile 是指向仓内 hash 的链接或有效副本——必须先于任何引用该哈希的
// realm 行写入落地。同哈希已在游戏侧且有效（链接或哈希校验一致的实体）
// → 跳过不覆盖；悬空链接或校验不符 → 重建并计数（§5.3）。
func (e *Engine) EnsureLinked(hash, gameFile string) (LinkMode, error) {
	return "", ErrNotImplemented
}
