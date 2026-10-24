package appsvc

// NewMockBackend 构造四服务全 Mock 的后端（M-4：UI 在没有 native 库的
// 环境开发与冒烟，ezrealmsync-design 的 --ui-test 同款结构）。数据由
// 内存态承载，不触碰任何文件系统与 native 库。实现待施工。
func NewMockBackend() (Backend, error) {
	return nil, ErrNotImplemented
}
