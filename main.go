// 兼容入口：go run . / go build . 默认以 Web 模式启动。
// 双模式设计下的正式入口位于 cmd/plc-simulater-server（Web）与
// cmd/plc-simulater-gui（Windows GUI）。
package main

import (
	"log"

	"plc-simulater/internal/bootstrap"
)

func main() {
	if err := bootstrap.RunWeb(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
