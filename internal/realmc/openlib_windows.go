//go:build windows

package realmc

import "syscall"

// openLibrary 经 LoadLibrary 加载 DLL；符号解析由 purego
// RegisterLibFunc 内部走 GetProcAddress。
func openLibrary(path string) (uintptr, error) {
	handle, err := syscall.LoadLibrary(path)
	return uintptr(handle), err
}
