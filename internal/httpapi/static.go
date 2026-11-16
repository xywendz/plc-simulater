package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHandler 提供 Vite 构建产物：静态文件直接返回，其余路径回落到 index.html
// 以支持单页应用的客户端路由。
func staticHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if clean == "." || clean == "/" {
			clean = "index.html"
		}
		if _, err := fs.Stat(fsys, clean); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 单页应用回落：直接把 index.html 作为响应，避免改变 URL 造成资源相对路径错乱。
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "前端资源缺失，请先执行 npm run build:web", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}
