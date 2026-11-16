package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemp 将内容写入临时文件并返回路径。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeTemp(t, `
listen: "127.0.0.1:1102"
max_pdu_size: 480
dbs:
  - number: 1
    size: 128
    fields:
      - { name: "TankLevel", offset: 0, type: real, value: 12.5 }
      - { name: "PumpRunning", offset: 4, type: bool, bit: 0, value: true }
      - { name: "ProductName", offset: 32, type: string, length: 20, value: "Hello S7" }
  - number: 20
    size: 256
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:1102" {
		t.Errorf("Listen = %q, want 127.0.0.1:1102", cfg.Listen)
	}
	if cfg.MaxPDU != 480 {
		t.Errorf("MaxPDU = %d, want 480", cfg.MaxPDU)
	}
	if len(cfg.DBs) != 2 {
		t.Fatalf("len(DBs) = %d, want 2", len(cfg.DBs))
	}
	db1 := cfg.DBs[0]
	if db1.Number != 1 || db1.Size != 128 {
		t.Errorf("DB1 = %+v, want number=1 size=128", db1)
	}
	if len(db1.Fields) != 3 {
		t.Errorf("len(DB1.Fields) = %d, want 3", len(db1.Fields))
	}
	if cfg.DBs[1].Number != 20 {
		t.Errorf("DBs[1].Number = %d, want 20", cfg.DBs[1].Number)
	}
}

func TestLoadDefaults(t *testing.T) {
	path := writeTemp(t, `
dbs:
  - number: 3
    size: 16
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "0.0.0.0:102" {
		t.Errorf("default Listen = %q, want 0.0.0.0:102", cfg.Listen)
	}
	if cfg.MaxPDU != 240 {
		t.Errorf("default MaxPDU = %d, want 240", cfg.MaxPDU)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"no dbs", "listen: \"0.0.0.0:102\"\n"},
		{"duplicate db", "dbs:\n  - number: 1\n    size: 16\n  - number: 1\n    size: 32\n"},
		{"bad db number", "dbs:\n  - number: 0\n    size: 16\n"},
		{"db number too large", "dbs:\n  - number: 70000\n    size: 16\n"},
		{"invalid yaml", "dbs: [oops\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, tc.content)); err == nil {
				t.Errorf("Load(%s) = nil error, want error", tc.name)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	// 缺失配置文件时，应回退到内置默认配置而非报错，使系统可独立启动。
	cfg, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("Load(missing file) = %v, want nil error (default config)", err)
	}
	if cfg.Listen != "0.0.0.0:102" {
		t.Errorf("default Listen = %q, want 0.0.0.0:102", cfg.Listen)
	}
	if cfg.MaxPDU != 240 {
		t.Errorf("default MaxPDU = %d, want 240", cfg.MaxPDU)
	}
	if len(cfg.DBs) == 0 {
		t.Error("default config has no DBs")
	}
	if cfg.DBs[0].Number != 1 {
		t.Errorf("default DBs[0].Number = %d, want 1", cfg.DBs[0].Number)
	}
}

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Listen != "0.0.0.0:102" || cfg.MaxPDU != 240 {
		t.Errorf("Default() = %+v, unexpected fields", cfg)
	}
	if len(cfg.DBs) == 0 {
		t.Fatal("Default() has no DBs")
	}
}

func TestLoadRealProjectConfig(t *testing.T) {
	// 项目根目录的示例配置必须可加载。
	cfg, err := Load("../../config.yaml")
	if err != nil {
		t.Skipf("skipped (config.yaml not reachable): %v", err)
	}
	if len(cfg.DBs) == 0 {
		t.Error("project config.yaml has no DBs")
	}
}
