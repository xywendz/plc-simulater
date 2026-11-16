package s7

import (
	"context"
	"testing"
	"time"

	plc4go "github.com/apache/plc4x/plc4go/pkg/api"
	"github.com/apache/plc4x/plc4go/pkg/api/drivers"
	apiModel "github.com/apache/plc4x/plc4go/pkg/api/model"
	apiValues "github.com/apache/plc4x/plc4go/pkg/api/values"
)

// connectPlc4x 启动服务端并用 plc4go S7 驱动连接，返回就绪的连接与清理函数。
// 连接过程包含 COTP 握手、Setup Communication 与 SZL 设备识别（identifyRemote）。
func connectPlc4x(t *testing.T) (plc4go.PlcConnection, func()) {
	t.Helper()
	addr, stop := startTestServer(t, testConfig())
	driverManager := plc4go.NewPlcDriverManager()
	drivers.RegisterS7Driver(driverManager)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 此版本 plc4go 的 GetConnection 内部即完成连接（握手 + Setup + SZL 识别）
	conn, err := driverManager.GetConnection(ctx, "s7://"+addr+"?remote-rack=0&remote-slot=2")
	if err != nil {
		stop()
		t.Fatalf("plc4go connect: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		stop()
	}
}

// readValue 读取单个 tag 并返回其值。
func readValue(t *testing.T, conn plc4go.PlcConnection, name, tag string) apiValues.PlcValue {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := conn.ReadRequestBuilder().AddTagAddress(name, tag).Build()
	if err != nil {
		t.Fatalf("build read request: %v", err)
	}
	result := <-req.Execute(ctx)
	if err := result.GetErr(); err != nil {
		t.Fatalf("read %s (%s): %v", name, tag, err)
	}
	resp := result.GetResponse()
	if code := resp.GetResponseCode(name); code != apiModel.PlcResponseCode_OK {
		t.Fatalf("read %s (%s): response code %v", name, tag, code)
	}
	return resp.GetValue(name)
}

// writePlc4x 写入单个 tag。
func writePlc4x(t *testing.T, conn plc4go.PlcConnection, name, tag string, value any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := conn.WriteRequestBuilder().AddTagAddress(name, tag, value).Build()
	if err != nil {
		t.Fatalf("build write request: %v", err)
	}
	result := <-req.Execute(ctx)
	if err := result.GetErr(); err != nil {
		t.Fatalf("write %s (%s): %v", name, tag, err)
	}
	resp := result.GetResponse()
	if code := resp.GetResponseCode(name); code != apiModel.PlcResponseCode_OK {
		t.Fatalf("write %s (%s): response code %v", name, tag, code)
	}
}

// TestPlc4goConnectAndRead 验证 plc4go 客户端能连接并读取各种类型。
func TestPlc4goConnectAndRead(t *testing.T) {
	conn, stop := connectPlc4x(t)
	defer stop()

	// REAL：TankLevel = 12.5
	if v := readValue(t, conn, "tank", "%DB1.DBD0:REAL"); v.GetFloat32() != 12.5 {
		t.Errorf("TankLevel = %v, want 12.5", v)
	}
	// BOOL：PumpRunning(DBX4.0) = true
	if v := readValue(t, conn, "pump", "%DB1.DBX4.0:BOOL"); !v.GetBool() {
		t.Errorf("PumpRunning = %v, want true", v)
	}
	// BOOL：Fault(DBX4.1) = false
	if v := readValue(t, conn, "fault", "%DB1.DBX4.1:BOOL"); v.GetBool() {
		t.Errorf("Fault = %v, want false", v)
	}
	// WORD：StatusWord = 0x01FF
	if v := readValue(t, conn, "status", "%DB1.DBW8:WORD"); v.GetUint16() != 0x01FF {
		t.Errorf("StatusWord = 0x%04X, want 0x01FF", v.GetUint16())
	}
	// DWORD：Counter = 1000
	if v := readValue(t, conn, "counter", "%DB1.DBD12:DWORD"); v.GetUint32() != 1000 {
		t.Errorf("Counter = %d, want 1000", v.GetUint32())
	}
	// INT：Temperature = -25
	if v := readValue(t, conn, "temp", "%DB1.DBW16:INT"); v.GetInt16() != -25 {
		t.Errorf("Temperature = %d, want -25", v.GetInt16())
	}
	// BYTE：DBB0 = 0x41（REAL 12.5 的首字节）
	if v := readValue(t, conn, "byte0", "%DB1.DBB0:BYTE"); v.GetUint8() != 0x41 {
		t.Errorf("DBB0 = 0x%02X, want 0x41", v.GetUint8())
	}
}

// TestPlc4goWriteAndReadBack 验证 plc4go 客户端写入后能读回。
func TestPlc4goWriteAndReadBack(t *testing.T) {
	conn, stop := connectPlc4x(t)
	defer stop()

	// REAL 写读回
	writePlc4x(t, conn, "sp", "%DB1.DBD40:REAL", float32(3.25))
	if v := readValue(t, conn, "sp", "%DB1.DBD40:REAL"); v.GetFloat32() != 3.25 {
		t.Errorf("SetPoint = %v, want 3.25", v)
	}
	// DWORD 写读回
	writePlc4x(t, conn, "recipe", "%DB1.DBD44:DWORD", uint32(4242))
	if v := readValue(t, conn, "recipe", "%DB1.DBD44:DWORD"); v.GetUint32() != 4242 {
		t.Errorf("RecipeID = %d, want 4242", v.GetUint32())
	}
	// INT 写读回
	writePlc4x(t, conn, "int16", "%DB1.DBW48:INT", int16(-1234))
	if v := readValue(t, conn, "int16", "%DB1.DBW48:INT"); v.GetInt16() != -1234 {
		t.Errorf("INT16 = %d, want -1234", v.GetInt16())
	}
	// WORD 写读回
	writePlc4x(t, conn, "word", "%DB1.DBW50:WORD", uint16(0xBEEF))
	if v := readValue(t, conn, "word", "%DB1.DBW50:WORD"); v.GetUint16() != 0xBEEF {
		t.Errorf("WORD = 0x%04X, want 0xBEEF", v.GetUint16())
	}
	// BOOL 写读回（置位与清位）
	writePlc4x(t, conn, "flag", "%DB1.DBX60.7:BOOL", true)
	if v := readValue(t, conn, "flag", "%DB1.DBX60.7:BOOL"); !v.GetBool() {
		t.Error("DBX60.7 = false, want true")
	}
	writePlc4x(t, conn, "flag", "%DB1.DBX60.7:BOOL", false)
	if v := readValue(t, conn, "flag", "%DB1.DBX60.7:BOOL"); v.GetBool() {
		t.Error("DBX60.7 = true, want false")
	}
	// 位写不影响同字节其他位
	writePlc4x(t, conn, "flag2", "%DB1.DBX60.6:BOOL", true)
	if v := readValue(t, conn, "flag2", "%DB1.DBX60.6:BOOL"); !v.GetBool() {
		t.Error("DBX60.6 = false, want true")
	}
	if v := readValue(t, conn, "flag", "%DB1.DBX60.7:BOOL"); v.GetBool() {
		t.Error("DBX60.7 changed, want still false")
	}
}

// TestPlc4goMultiItemRead 验证单请求多 item（含奇数长度项，验证填充字节
// 不影响 plc4go 的顺序解析）。
func TestPlc4goMultiItemRead(t *testing.T) {
	conn, stop := connectPlc4x(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := conn.ReadRequestBuilder().
		AddTagAddress("real", "%DB1.DBD0:REAL").
		AddTagAddress("word", "%DB1.DBW8:WORD").
		AddTagAddress("odd", "%DB1.DBB32[0..2]:BYTE").
		AddTagAddress("dword", "%DB1.DBD12:DWORD").
		Build()
	if err != nil {
		t.Fatalf("build multi read: %v", err)
	}
	result := <-req.Execute(ctx)
	if err := result.GetErr(); err != nil {
		t.Fatalf("multi read: %v", err)
	}
	resp := result.GetResponse()
	for _, name := range []string{"real", "word", "odd", "dword"} {
		if code := resp.GetResponseCode(name); code != apiModel.PlcResponseCode_OK {
			t.Errorf("%s: response code %v", name, code)
		}
	}
	if v := resp.GetValue("real"); v.GetFloat32() != 12.5 {
		t.Errorf("real = %v, want 12.5", v)
	}
	if v := resp.GetValue("word"); v.GetUint16() != 0x01FF {
		t.Errorf("word = 0x%04X, want 0x01FF", v.GetUint16())
	}
	if v := resp.GetValue("odd"); len(v.GetRaw()) != 3 {
		t.Errorf("odd = % X, want 3 bytes", v.GetRaw())
	}
	if v := resp.GetValue("dword"); v.GetUint32() != 1000 {
		t.Errorf("dword = %d, want 1000", v.GetUint32())
	}
}

// TestPlc4goReadErrors 验证错误场景下 plc4go 能收到正确的响应码。
func TestPlc4goReadErrors(t *testing.T) {
	conn, stop := connectPlc4x(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 读取未配置的 DB99：服务端返回 0x0A -> plc4x INVALID_ADDRESS
	req, err := conn.ReadRequestBuilder().AddTagAddress("missing", "%DB99.DBD0:REAL").Build()
	if err != nil {
		t.Fatalf("build read: %v", err)
	}
	result := <-req.Execute(ctx)
	if err := result.GetErr(); err != nil {
		t.Fatalf("read missing DB: %v", err)
	}
	if code := result.GetResponse().GetResponseCode("missing"); code != apiModel.PlcResponseCode_INVALID_ADDRESS {
		t.Errorf("missing DB code = %v, want INVALID_ADDRESS", code)
	}

	// 读取越界地址：DB1 仅 128 字节
	req, err = conn.ReadRequestBuilder().AddTagAddress("oob", "%DB1.DBD1000:REAL").Build()
	if err != nil {
		t.Fatalf("build read: %v", err)
	}
	result = <-req.Execute(ctx)
	if err := result.GetErr(); err != nil {
		t.Fatalf("read out of range: %v", err)
	}
	if code := result.GetResponse().GetResponseCode("oob"); code != apiModel.PlcResponseCode_INVALID_ADDRESS {
		t.Errorf("out of range code = %v, want INVALID_ADDRESS", code)
	}
}

// TestPlc4goOtherDBs 验证仅配置长度的 DB 块可读写，且长度边界生效。
func TestPlc4goOtherDBs(t *testing.T) {
	conn, stop := connectPlc4x(t)
	defer stop()

	// DB2（无字段，仅长度 256）写读回
	writePlc4x(t, conn, "v", "%DB2.DBD100:DWORD", uint32(0xDEADBEEF))
	if v := readValue(t, conn, "v", "%DB2.DBD100:DWORD"); v.GetUint32() != 0xDEADBEEF {
		t.Errorf("DB2 value = 0x%08X, want 0xDEADBEEF", v.GetUint32())
	}
	// DB20 越界（size=256，读取偏移 256 起 4 字节）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := conn.ReadRequestBuilder().AddTagAddress("oob", "%DB20.DBD256:DWORD").Build()
	if err != nil {
		t.Fatalf("build read: %v", err)
	}
	result := <-req.Execute(ctx)
	if err := result.GetErr(); err != nil {
		t.Fatalf("read DB20 oob: %v", err)
	}
	if code := result.GetResponse().GetResponseCode("oob"); code != apiModel.PlcResponseCode_INVALID_ADDRESS {
		t.Errorf("DB20 oob code = %v, want INVALID_ADDRESS", code)
	}
}

// TestPlc4goConnectionMetadata 验证连接元数据：identifyRemote 在 Connect 内部
// 通过 Read SZL 识别设备，plc4go 会把订货号/型号/PDU 大小写入连接属性。
func TestPlc4goConnectionMetadata(t *testing.T) {
	conn, stop := connectPlc4x(t)
	defer stop()

	md := conn.GetMetadata()
	if md == nil {
		t.Fatal("GetMetadata = nil")
	}
	attrs := md.GetConnectionAttributes()
	if attrs == nil {
		t.Fatal("connection attributes = nil")
	}
	t.Logf("plc4go connection attributes: %v", attrs)
	if attrs["pdu-size"] == "" {
		t.Errorf("pdu-size attribute missing: %v", attrs)
	}
	if attrs["article-number"] == "" && attrs["controller-type"] == "" {
		t.Errorf("SZL identification failed (article-number/controller-type empty): %v", attrs)
	}
}
