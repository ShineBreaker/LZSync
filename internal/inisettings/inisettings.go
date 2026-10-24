// Package inisettings 承载 game.ini 白名单键同步（design.md §3.1）：
// 共有键拷贝（白名单机制）、红灯键硬锁定（S10：Token/Username 凭据、
// ReleaseStream 身份配置等）、S12 原子写（写前备份原件 → 临时文件 →
// rename 原子替换，绝不就地覆写）。
//
// 数据分层：共有键白名单是四端结构事实集（固化自 lazer-baseline §4.2
// 全量摘录，C-1 自包含）；per-fork 差异（红灯 Locked / 专有 Own）数据化
// 于 forks 注册表的 ForkProfile.IniKeys，本包不内置任何具体 fork 名单。
//
// hub 承载形态：白名单键快照落 hub 目录自有文件（store.go，forks
// SnapshotStore 的同款模式），不入 hub.db——hub.db 承载需扩 hub 包
// （实体表 + ApplySnapshot 载荷面），属独立施工块；本包以「hub 目录 =
// 中枢」的既有先例落地最小方案。
package inisettings
