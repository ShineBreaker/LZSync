//go:build !windows

package realmc

import "github.com/ebitengine/purego"

// openLibrary 经 dlopen 打开共享库。RTLD_GLOBAL 让库内符号对后续
// dlopen 可见（realm-core 内部子库间相互引用时需要）。
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
