//go:build windows

package deploy

// Windows 平台的占用探测占位：S5 规定 Windows 用独占打开探测（等价
// EZRealmSync 的 FileShare.None 开-即-释）。Go 标准库 os.OpenFile 无法表达
// FILE_SHARE_NONE，需 win32 CreateFileW（syscall）实现；本平台探测在 M1
// 未施工（开发环境为 linux），暂返回 nil（无信号），open/写事务的互斥仍
// 由 realm 自身保证。M2 链接引擎做 Windows 实测（T10）时一并补齐。

func probeOccupiedPlatform(realmPath string) error {
	return nil
}
