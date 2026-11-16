// Package bridge 把 S7 模拟器的业务内核以 JSON 可序列化的门面方法暴露给前端。
//
// 设计原则（与 db-fly 参考项目一致）：
//   - 本包保持纯 Go，不直接引入 Wails；GUI 模式由 service_windows.go 包装为
//     Wails 服务并生成前端绑定，Web 模式由 internal/httpapi 通过反射分发调用
//     同一组方法，两端共用同一套 DTO。
//   - 所有方法签名只使用 JSON 可序列化的参数与返回值，错误统一以 error 返回，
//     由 Wails 转成 Promise reject，或由 HTTP 层转成结构化错误响应。
package bridge

import (
	"encoding/hex"
	"sort"

	"plc-simulater/model"
	"plc-simulater/s7"
)

// Version 由构建脚本经 -ldflags "-X plc-simulater/internal/bridge.Version=..." 注入。
var Version = "dev"

// Bridge 是暴露给前端的唯一业务门面。
type Bridge struct {
	srv     *s7.Server
	version string
	listen  string
	maxPDU  int
}

// New 构造 Bridge。GUI 与 Web 两个入口共用。
func New(srv *s7.Server, listen string, maxPDU int) *Bridge {
	return &Bridge{srv: srv, version: Version, listen: listen, maxPDU: maxPDU}
}

// FieldView 是字段在前端的展示视图。
type FieldView struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"`
	Type   string `json:"type"`
	Bit    int    `json:"bit"`
	Length int    `json:"length"`
	Value  any    `json:"value"`
}

// DBView 是数据块在前端的展示视图。
type DBView struct {
	Number int         `json:"number"`
	Name   string      `json:"name"`
	Size   int         `json:"size"`
	Fields []FieldView `json:"fields"`
	Raw    string      `json:"raw"`
}

// FieldMeta 是持久化字段定义的传输对象（与 store.FieldMeta 解耦）。
type FieldMeta struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"`
	Type   string `json:"type"`
	Bit    int    `json:"bit"`
	Length int    `json:"length"`
	Ord    int    `json:"ord"`
	Value  any    `json:"value,omitempty"`
}

// RawResult 是原始字节查询结果。
type RawResult struct {
	DB     int    `json:"db"`
	Offset int    `json:"offset"`
	Hex    string `json:"hex"`
}

// AppInfo 是应用元信息。
type AppInfo struct {
	Version string `json:"version"`
	Mode    string `json:"mode"`
	Listen  string `json:"listen"`
	MaxPDU  int    `json:"max_pdu"`
}

func buildDBView(db *model.DB) DBView {
	v := DBView{
		Number: db.Number,
		Name:   db.Name,
		Size:   db.Size(),
		Fields: []FieldView{},
	}
	fields := db.Fields()
	if len(fields) == 0 {
		if raw, err := db.Read(0, db.Size()); err == nil {
			v.Raw = hex.EncodeToString(raw)
		}
		return v
	}
	fs := make([]model.Field, 0, len(fields))
	for _, f := range fields {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Offset != fs[j].Offset {
			return fs[i].Offset < fs[j].Offset
		}
		return fs[i].Bit < fs[j].Bit
	})
	for _, f := range fs {
		val, _ := db.FieldValue(f)
		v.Fields = append(v.Fields, FieldView{
			Name: f.Name, Offset: f.Offset, Type: f.Type,
			Bit: f.Bit, Length: f.Length, Value: val,
		})
	}
	return v
}
