// Package forks 承载四端 fork 的静态元数据注册表（ForkProfile）、库身份
// 识别与结构档案仓库（design.md §3.1、§4.3）。内核不内置任何具体 fork
// 知识——全部「Ez 怎样、g0v0 怎样」的差异只存在于本包的 Profile 数据与
// 运行时结构档案中：新增第五个 fork = 新增一份 Profile，不改内核。
package forks

import "errors"

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("forks: 契约桩未实现")

// SkinFingerprint 标识 fork 专有皮肤（读取投影跳过，§4.1）。
type SkinFingerprint struct {
	Name    string
	Creator string
}

// IniKeyPolicy 是 game.ini 键策略（S10 红灯排除 + M3 白名单同步）。
type IniKeyPolicy struct {
	Shared []string // 四端共有白名单键
	Own    []string // 本端专有键（只在本端管理）
	Locked []string // 红灯硬锁定键（Token/Username 等，永不同步、UI 不可勾选）
}

// ForkProfile 是单个 fork 的静态差异声明（design.md §4.3）。
type ForkProfile struct {
	ID           string // "lazer" | "delta" | "ez2lazer" | "g0v0"
	DataDirName  string // osu / deltalazer / osu-Ez2Lazer / g0v0
	RealmFilePat string // client.realm；Ez 为 client_<diskVersion>.realm + 前驱链
	ProcessNames []string
	// DecodeVersion 解码磁盘 schema 版本：Ez 为 v>=1000 ? v/1000, v%1000 : v, 0。
	DecodeVersion func(uint64) (upstream int, forkRev int)
	// ServerRealm 服务器命名空间指纹（"ppy" vs "g0v0.top"），S8 判定依据。
	ServerRealm string
	// ExtColumns 本 fork 在官方基线之外追加的列（表名 → 列名；Ez 的 10 列）。
	ExtColumns map[string][]string
	// ExtTables 本 fork 专有表（Ez 7 张技能表）。
	ExtTables []string
	// OwnRulesets 专有规则集 ShortName（Ez: bms；g0v0: OnlineID>=10 市场）。
	OwnRulesets []string
	// OwnSkins 专有皮肤指纹。
	OwnSkins []SkinFingerprint
	// IniKeys game.ini 键策略。
	IniKeys IniKeyPolicy
}

// All 返回全部已注册 Profile（四端：lazer / delta / ez2lazer / g0v0；
// 数据内容以 docs/research/ 摘录为准，C-1 不依赖 reference/）。
// 桩阶段返回 nil，四端静态数据由 worker 从调研摘录固化为包内数据。
func All() []ForkProfile {
	return nil
}

// Profile 按 ID 取 Profile；不存在返回 false。桩阶段恒 (零值, false)。
func Profile(id string) (ForkProfile, bool) {
	return ForkProfile{}, false
}
