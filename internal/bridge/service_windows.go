//go:build windows

package bridge

import "github.com/wailsapp/wails/v3/pkg/application"

// NewService 把 Bridge 包装为 Wails 服务。
//
// application.NewService 的调用必须保留在本包内：wails3 generate bindings 的
// 静态分析器通过它推断可绑定类型（*bridge.Bridge）并生成前端绑定。
// 该文件仅在 Windows 编译，避免 Web/Linux 入口链接 Wails 运行时。
func NewService(b *Bridge) application.Service {
	return application.NewService(b)
}
