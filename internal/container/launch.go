package container

// 客户端拉起（§7-M3-5 路线④）：生成到临时目录的容器经「系统默认打开
// 方式」交给目标 fork 客户端——lazer 注册 .osz/.osk/.osr 文件关联后，
// 打开容器即触发启动参数导入（OsuGame.Import(ImportTask[])，
// go-realm-options §5-1）。找不到打开器或扩展未关联时给人话提示并附
// 容器路径（用户可手动拖入客户端导入）。
//
// 本文件的真实拉起链路属人工验收项（§8 验收：拉起客户端不进自动化）。
//
// 安全说明（Mimosa 误报登记）：exec.Command 是参数列表调用（直接构造
// argv、不经 shell）；path 是本程序生成的容器路径，非用户输入。拉起
// 系统默认打开器正是本降级路径的指定机制（任务原文「xdg-open 等价
// 机制经 exec.Command」）。

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// OpenerCommand 返回指定平台的系统默认打开命令（纯函数，供测试与
// OpenWithDefaultApp 复用）。Windows 走 rundll32 的文件关联协议处理器
//（等价资源管理器双击），macOS 走 open，其余按 XDG 桌面约定走 xdg-open。
func OpenerCommand(goos string) []string {
	switch goos {
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler"}
	case "darwin":
		return []string{"open"}
	default:
		return []string{"xdg-open"}
	}
}

// OpenWithDefaultApp 以系统默认打开方式拉起 path（交给注册了容器扩展的
// 目标 fork 客户端）。失败返回人话错误并附容器路径——容器文件已生成，
// 用户总能手动导入，绝不因拉起失败丢失生成产物。
func OpenWithDefaultApp(path string) error {
	args := OpenerCommand(runtime.GOOS)
	if _, err := exec.LookPath(args[0]); err != nil {
		return fmt.Errorf("container: 系统缺少默认打开器 %q（%s）。容器已生成：%s——请手动把它拖入目标 fork 客户端导入",
			args[0], runtime.GOOS, path)
	}
	cmd := exec.Command(args[0], append(args[1:], path)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("container: 拉起默认程序失败（%s 未注册 .osz/.osk/.osr 关联？输出：%s）。容器已生成：%s——请手动把它拖入目标 fork 客户端导入",
			args[0], strings.TrimSpace(string(out)), path)
	}
	return nil
}
