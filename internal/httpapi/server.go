// Package httpapi 为 Web 模式提供 HTTP 入口：统一 RPC 门面、静态前端资源，
// 以及原有 REST 接口（保持向后兼容）。
package httpapi

import (
	"io/fs"
	"net/http"

	"plc-simulater/internal/bridge"
	"plc-simulater/s7"
)

// NewHandler 组装 Web 模式的 http.Handler。
// 路由优先级：/api/rpc/... > 原有 /api/... REST 接口 > 静态前端资源。
func NewHandler(b *bridge.Bridge, srv *s7.Server, assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/rpc/{method}", handleRPC(b))
	srv.RegisterHTTPRoutes(mux)
	mux.Handle("/", staticHandler(assets))
	return mux
}
