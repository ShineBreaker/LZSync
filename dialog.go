// DialogService 系统文件对话框（DESIGN.md §9：选择文件夹注册客户端、
// 还原…选择备份文件）。挂在主窗口下；取消返回空串不报错。
package main

import "github.com/egoist/mygo"

// DialogService 是绑定给前端的系统对话框服务。
type DialogService struct{}

// PickFolder 选择目录（注册客户端「选择文件夹…」）。
func (DialogService) PickFolder(defaultPath string) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Directory:   true,
		DefaultPath: defaultPath,
		Parent:      mainWindow.Load(),
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// PickFile 选择文件（「还原…」选备份；defaultPath 开备份目录）。
func (DialogService) PickFile(defaultPath string) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		DefaultPath: defaultPath,
		Parent:      mainWindow.Load(),
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}
