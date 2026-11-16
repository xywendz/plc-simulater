// Package webui 嵌入 Web 模式的前端构建产物（frontend/dist-web）。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS 返回以站点根目录为根的只读前端文件系统。
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: invalid embedded dist: " + err.Error())
	}
	return sub
}
