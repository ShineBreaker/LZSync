package forks

import "errors"

// ErrUnidentified 库身份识别失败（未知 schema / 识别歧义）：调用方须向用户
// 呈现原始版本号并要求指认 fork，绝不猜（§6.1）。
var ErrUnidentified = errors.New("forks: 无法识别库身份，需用户指认")

// VersionSource 是磁盘 schema 版本值的来源标注（§3.2 版本探测降级链）。
type VersionSource string

const (
	VersionFromOpen       VersionSource = "realm-open"      // 现场开库探测（最强）
	VersionFromFilename   VersionSource = "filename"        // 文件名推断（仅 Ez 版本化文件名可退）
	VersionFromSnapshot   VersionSource = "snapshot-cache"  // 结构档案缓存
	VersionUnknownRunning VersionSource = "unknown-running" // 游戏运行中，开库探测失败
)

// LibraryRef 是一个已注册（或候选）fork 库的解析结果。
type LibraryRef struct {
	ForkID          string        // 命中的 Profile ID；识别失败为 ""
	DataDir         string        // fork 数据目录绝对路径（files/ 的父目录）
	RealmPath       string        // 库文件绝对路径
	DiskVersion     uint64        // 磁盘 schema 版本（原始值）
	VersionSource   VersionSource // 版本值来源标注（UI 呈现，§6.1）
	UpstreamVersion int           // DecodeVersion 解码后的官方基线版本
	ForkRev         int           // fork 自身修订号（非 Ez 恒 0）
}

// Discover 扫描数据根目录，按 Profile 内置目录名识别四端候选（§3.2 发现；
// P-3 的平台数据根解析由调用方完成并传入）。Ez 侧需识别 client_52011.realm
// 及其前驱搜索链（fork-ez2lazer §2.2）。
func Discover(dataRoot string) ([]LibraryRef, error) {
	return nil, ErrNotImplemented
}

// Identify 对单个库文件做身份识别：版本探测（降级链见 VersionSource 常量）
// + fork 指纹判定（fork 身份 = 版本编码 + 结构指纹）。识别失败返回
// ErrUnidentified，此时返回的 LibraryRef 携带原始 DiskVersion 供 UI 呈现
// （ForkID 为空）。
func Identify(realmPath string) (LibraryRef, error) {
	return LibraryRef{}, ErrNotImplemented
}
