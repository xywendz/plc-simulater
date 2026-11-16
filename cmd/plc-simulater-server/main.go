// plc-simulater-server 是 Web/B-S 模式的独立编译入口：
// 纯 Go，无 Wails 依赖；HTTP 服务同时提供前端资源、RPC 门面与原有 REST 接口。
package main

import (
	"log"

	"plc-simulater/internal/bootstrap"
)

func main() {
	if err := bootstrap.RunWeb(); err != nil {
		log.Fatalf("Web 服务退出: %v", err)
	}
}
