//go:build windows

// plc-simulater-gui 是桌面 GUI 模式的编译入口（wails3 + 内嵌前端）。
// Windows 后端为纯 Go（CGO_ENABLED=0 即可）；非 Windows 平台由 stub.go 提供占位入口，
// 保证 go build ./... 与 go test ./... 在任意平台均可通过。
package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	"plc-simulater/internal/bootstrap"
	"plc-simulater/internal/bridge"
)

// 前端产物由 build/gui.ps1 从 frontend/dist 复制到 assets/。
//
//go:embed all:assets
var assets embed.FS

// logo.png 的副本（由 build/gui.ps1 从仓库根 logo.png 同步），
// 用作窗口/任务栏/关于对话框的应用图标；Windows 下 exe 的
// Explorer 图标由 rsrc_windows_*.syso 中的 PE 资源提供。
//
//go:embed icon.png
var appIcon []byte

// assetFS 剥离 assets/ 前缀，使 index.html 位于文件系统根目录。
func assetFS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		log.Fatalf("资源嵌入异常: %v", err)
	}
	return sub
}

func main() {
	cfg, err := bootstrap.LoadConfig(bootstrap.ConfigPathForGUI(), false)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	srv, err := bootstrap.NewServer(cfg)
	if err != nil {
		log.Fatalf("初始化模拟器失败: %v", err)
	}
	defer srv.Close()

	// GUI 窗口内嵌的前端通过 Wails 绑定操作同一份 S7 内核；
	// TCP 监听仍在本机后台运行，PLC 客户端可照常连接。
	if _, err := bootstrap.StartS7(srv, cfg.Listen); err != nil {
		log.Fatalf("启动 S7 监听失败: %v", err)
	}

	svc := bridge.NewService(bridge.New(srv, cfg.Listen, cfg.MaxPDU))
	app := application.New(application.Options{
		Name:        "plc-simulater",
		Description: "PLC S7 协议模拟器（DB 块浏览器）",
		Icon:        appIcon,
		Services:    []application.Service{svc},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(assetFS()),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "PLC S7 模拟器",
		Width:     1280,
		Height:    820,
		MinWidth:  960,
		MinHeight: 640,
	})

	app.OnShutdown(func() { _ = srv.Close() })

	if err := app.Run(); err != nil {
		log.Fatalf("应用运行失败: %v", err)
	}
}
