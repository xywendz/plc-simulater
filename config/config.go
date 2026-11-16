package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Field 描述数据块中的一个具名字段（元素）。
// 用于初始化数据，以及文档化块内布局。
type Field struct {
	Name   string `yaml:"name"`
	Offset int    `yaml:"offset"`
	Type   string `yaml:"type"`             // bool/bit, byte, word, dword, int, dint, real, string
	Bit    int    `yaml:"bit,omitempty"`    // bool/bit 类型的位号 0-7
	Length int    `yaml:"length,omitempty"` // string 类型需要
	Value  any    `yaml:"value,omitempty"`  // 初始值
}

// DB 描述一个数据块。
type DB struct {
	Number int     `yaml:"number"`
	Name   string  `yaml:"name,omitempty"` // 数据块名称（可选，便于辨识）
	Size   int     `yaml:"size,omitempty"` // 省略时根据 fields 自动计算
	Fields []Field `yaml:"fields,omitempty"`
}

// Config 为顶层配置。
type Config struct {
	Listen     string `yaml:"listen"`                 // 监听地址，如 0.0.0.0:102
	HTTPListen string `yaml:"http_listen,omitempty"`  // HTTP 管理界面监听地址，如 :8070
	DBPath     string `yaml:"db_path,omitempty"`      // SQLite 数据库文件路径（记录 DB 块与字段定义）
	MaxPDU     int    `yaml:"max_pdu_size,omitempty"` // 协商的 S7 PDU 最大长度
	DBs        []DB   `yaml:"dbs"`
}

// Default 返回内置默认配置。
// 当未提供配置文件、或显式使用默认时，系统以此配置独立启动，无需任何外部文件。
func Default() *Config {
	return &Config{
		Listen:     "0.0.0.0:102",
		HTTPListen: ":8070",
		DBPath:     "plc-simulater.db",
		MaxPDU:     240,
		DBs: []DB{
			{
				Number: 1,
				Name:   "过程数据",
				Size:   128,
				Fields: []Field{
					{Name: "TankLevel", Offset: 0, Type: "real", Value: 12.5},
					{Name: "PumpRunning", Offset: 4, Type: "bool", Bit: 0, Value: true},
					{Name: "Fault", Offset: 4, Type: "bool", Bit: 1, Value: false},
					{Name: "StatusWord", Offset: 8, Type: "word", Value: 0x01FF},
					{Name: "Counter", Offset: 12, Type: "dword", Value: 1000},
					{Name: "Temperature", Offset: 16, Type: "int", Value: -25},
					{Name: "Pressure", Offset: 24, Type: "real", Value: 1.013},
					{Name: "ProductName", Offset: 32, Type: "string", Length: 20, Value: "Hello S7"},
				},
			},
		},
	}
}

// Load 读取并解析配置文件。
// 若配置文件不存在（os.IsNotExist），返回内置 Default() 配置而不是报错，
// 以便系统在没有配置文件的情况下也能单独启动。
// 文件存在但内容非法（解析失败/校验失败）时仍返回错误。
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	applyDefaults(cfg)
	if err := validate(cfg, path); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults 为缺失字段填充默认值。
// 注意：HTTPListen 不设默认（留空即不启用 HTTP 服务），仅当使用内置默认配置时才会启用。
func applyDefaults(cfg *Config) {
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:102"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "plc-simulater.db"
	}
	if cfg.MaxPDU <= 0 {
		cfg.MaxPDU = 240
	}
}

// validate 校验 DB 配置完整性。
func validate(cfg *Config, path string) error {
	if len(cfg.DBs) == 0 {
		return fmt.Errorf("no db configured in %s", path)
	}
	seen := make(map[int]bool, len(cfg.DBs))
	for _, db := range cfg.DBs {
		if db.Number <= 0 || db.Number > 0xFFFF {
			return fmt.Errorf("invalid DB number %d in %s", db.Number, path)
		}
		if seen[db.Number] {
			return fmt.Errorf("duplicate DB number %d in %s", db.Number, path)
		}
		seen[db.Number] = true
	}
	return nil
}
