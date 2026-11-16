// Package bootstrap 提供 GUI 与 Web 两个入口共用的装配逻辑：
// 配置加载、S7 内核创建、TCP 监听启动。
package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"plc-simulater/config"
	"plc-simulater/internal/bridge"
	"plc-simulater/internal/httpapi"
	"plc-simulater/internal/webui"
	"plc-simulater/s7"
)

// LoadConfig 加载配置：文件不存在时使用内置默认配置。
func LoadConfig(path string, useDefault bool) (*config.Config, error) {
	if useDefault {
		log.Printf("已指定 -default，使用内置默认配置启动")
		return config.Default(), nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			log.Printf("未找到配置文件 %q，使用内置默认配置启动", path)
			return config.Default(), nil
		}
		return nil, fmt.Errorf("访问配置文件失败: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	return cfg, nil
}

// ConfigPathForGUI 返回 GUI 模式下优先读取的配置文件路径：
// 可执行文件同目录的 config.yaml；不存在时回落到工作目录的 config.yaml。
func ConfigPathForGUI() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "config.yaml"
}

// NewServer 依据配置创建 S7 内核。
func NewServer(cfg *config.Config) (*s7.Server, error) {
	srv, err := s7.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("初始化模拟器失败: %w", err)
	}
	return srv, nil
}

// StartS7 在后台启动 S7 TCP 监听（ISO-on-TCP / 102 端口），不阻塞调用方。
func StartS7(srv *s7.Server, listen string) (net.Listener, error) {
	ln, err := net.Listen("tcp4", listen)
	if err != nil {
		return nil, fmt.Errorf("监听 S7 端口 %s 失败: %w", listen, err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Printf("S7 服务异常: %v", err)
		}
	}()
	return ln, nil
}

// RunWeb 是 Web/B-S 模式的完整启动流程。
func RunWeb() error {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径；文件不存在时自动使用内置默认配置")
	useDefault := flag.Bool("default", false, "强制使用内置默认配置，忽略配置文件")
	addr := flag.String("addr", "", "HTTP 管理界面监听地址，覆盖配置文件中的 http_listen（如 :8070）")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath, *useDefault)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.HTTPListen = *addr
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = ":8070"
	}

	srv, err := NewServer(cfg)
	if err != nil {
		return err
	}
	defer srv.Close()

	ln, err := StartS7(srv, cfg.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()

	b := bridge.New(srv, cfg.Listen, cfg.MaxPDU)
	httpServer := &http.Server{
		Addr:              cfg.HTTPListen,
		Handler:           httpapi.NewHandler(b, srv, webui.FS()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()

	log.Printf("S7 模拟器 Web 模式已启动: http://%s （S7 端口 %s）", cfg.HTTPListen, cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP 服务异常: %w", err)
	}
	return nil
}
