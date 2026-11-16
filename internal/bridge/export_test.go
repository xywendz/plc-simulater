package bridge

import (
	"testing"

	"plc-simulater/config"
	"plc-simulater/s7"
)

func newTestBridge(t *testing.T) *Bridge {
	t.Helper()
	cfg := &config.Config{
		DBPath: t.TempDir() + "/bridge-test.db",
		MaxPDU: 240,
		DBs: []config.DB{
			{
				Number: 1,
				Name:   "导出测试",
				Size:   32,
				// 偏移与 PackFields 的打包规则一致：bool 占 1 位，
				// 多字节类型对齐到字节边界（不是自身大小）。
				Fields: []config.Field{
					{Name: "PumpOn", Offset: 0, Type: "bool", Bit: 0, Value: true},
					{Name: "Pressure", Offset: 1, Type: "real", Value: 12.5},
					{Name: "ProductName", Offset: 5, Type: "string", Length: 8, Value: "hello"},
				},
			},
		},
	}
	srv, err := s7.New(cfg)
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return New(srv, "127.0.0.1:0", 240)
}

func fieldOf(db ExportedDB, name string) ExportedField {
	for _, f := range db.Fields {
		if f.Name == name {
			return f
		}
	}
	return ExportedField{}
}

func TestExportImportRoundTrip(t *testing.T) {
	b := newTestBridge(t)

	bundle, err := b.ExportDB(1)
	if err != nil {
		t.Fatalf("ExportDB: %v", err)
	}
	if len(bundle.DBs) != 1 {
		t.Fatalf("期望 1 个数据块, 得到 %d", len(bundle.DBs))
	}
	exported := bundle.DBs[0]
	if exported.Number != 1 || exported.Size != 32 || exported.Name != "导出测试" {
		t.Fatalf("导出内容不符: %+v", exported)
	}
	if len(exported.Fields) != 3 {
		t.Fatalf("期望 3 个字段, 得到 %d", len(exported.Fields))
	}
	// real 类型在内存中为 float32，导出值保持该类型；经 JSON 往返后为 float64。
	if v := fieldOf(exported, "Pressure").Value; toFloat(v) != 12.5 {
		t.Fatalf("Pressure 导出值 = %v, 期望 12.5", v)
	}
	if len(exported.Raw) != 64 { // 32 字节 -> 64 个十六进制字符
		t.Fatalf("Raw 长度 = %d, 期望 64", len(exported.Raw))
	}

	// 修改运行值后按导出包恢复，值应回到导出时的状态。
	if err := b.SetField(1, "Pressure", 99.5); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := b.ImportDB(exported); err != nil {
		t.Fatalf("ImportDB: %v", err)
	}
	view, err := b.GetDB(1)
	if err != nil {
		t.Fatalf("GetDB: %v", err)
	}
	for _, f := range view.Fields {
		if f.Name == "Pressure" && toFloat(f.Value) != 12.5 {
			t.Fatalf("导入后 Pressure = %v, 期望 12.5", f.Value)
		}
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float32:
		return float64(x)
	case float64:
		return x
	case int:
		return float64(x)
	default:
		return -1
	}
}

func TestImportAllCreatesMissing(t *testing.T) {
	b := newTestBridge(t)

	bundle := ExportBundle{Version: "test", DBs: []ExportedDB{
		{
			Number: 7,
			Name:   "导入新块",
			Size:   16,
			Fields: []ExportedField{
				{Name: "Flag", Type: "bool", Value: true},
				{Name: "Count", Type: "dint", Value: 42},
			},
			Raw: "",
		},
	}}
	n, err := b.ImportAll(bundle)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	if n != 1 {
		t.Fatalf("导入数量 = %d, 期望 1", n)
	}

	view, err := b.GetDB(7)
	if err != nil {
		t.Fatalf("导入的 DB7 不存在: %v", err)
	}
	if view.Name != "导入新块" || view.Size != 16 {
		t.Fatalf("DB7 元数据不符: %+v", view)
	}
	if len(view.Fields) != 2 {
		t.Fatalf("DB7 字段数 = %d, 期望 2", len(view.Fields))
	}

	// 覆盖导入：已存在的同号块被替换。
	bundle.DBs[0].Name = "覆盖后"
	if _, err := b.ImportAll(bundle); err != nil {
		t.Fatalf("覆盖导入: %v", err)
	}
	view, _ = b.GetDB(7)
	if view.Name != "覆盖后" {
		t.Fatalf("覆盖导入后名称 = %q", view.Name)
	}
}

func TestExportAllSortedAndRejectsInvalid(t *testing.T) {
	b := newTestBridge(t)
	if err := b.CreateDB(3, 8, "三号"); err != nil {
		t.Fatalf("CreateDB: %v", err)
	}
	bundle, err := b.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(bundle.DBs) != 2 || bundle.DBs[0].Number != 1 || bundle.DBs[1].Number != 3 {
		t.Fatalf("ExportAll 结果不符: %+v", bundle.DBs)
	}

	if err := b.ImportDB(ExportedDB{Number: 0, Size: 8}); err == nil {
		t.Fatal("DB 号 0 应被拒绝")
	}
	if err := b.ImportDB(ExportedDB{Number: 9, Size: 0}); err == nil {
		t.Fatal("大小 0 应被拒绝")
	}
}
