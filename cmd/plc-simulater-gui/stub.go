//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "PLC S7 模拟器桌面程序仅支持 Windows（wails3 + WebView2）。")
	fmt.Fprintln(os.Stderr, "请使用 Web 模式：go run ./cmd/plc-simulater-server")
	os.Exit(1)
}
