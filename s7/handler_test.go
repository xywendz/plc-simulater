package s7

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"math"
	"testing"

	"plc-simulater/config"
)

// testServer 构造一个带测试 DB 的 Server（不监听端口）。
func testServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		Listen: "127.0.0.1:0",
		MaxPDU: 240,
		DBs: []config.DB{
			{Number: 1, Size: 128, Fields: []config.Field{
				{Name: "TankLevel", Offset: 0, Type: "real", Value: 12.5},
				{Name: "PumpRunning", Offset: 4, Type: "bool", Bit: 0, Value: true},
				{Name: "Fault", Offset: 4, Type: "bool", Bit: 1, Value: false},
				{Name: "StatusWord", Offset: 8, Type: "word", Value: 0x01FF},
				{Name: "Counter", Offset: 12, Type: "dword", Value: 1000},
				{Name: "Temperature", Offset: 16, Type: "int", Value: -25},
				{Name: "ProductName", Offset: 32, Type: "string", Length: 20, Value: "Hello S7"},
			}},
			{Number: 2, Size: 256},
		},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.logger = log.New(io.Discard, "", 0)
	return s
}

// respDataItem 从响应帧中解析第一个数据项。
// 帧结构：TPKT(4) + COTP(3) + PDU[header(10) + err(2) + params + data]。
// 错误响应（数据区仅 1 字节返回码）时返回 rc 与空 data。
func respDataItem(t *testing.T, resp []byte) (rc, ts byte, data []byte, rest []byte) {
	t.Helper()
	if len(resp) < 7+14 {
		t.Fatalf("response too short: % X", resp)
	}
	h, err := ParsePDUHeader(resp[7:])
	if err != nil {
		t.Fatalf("ParsePDUHeader: %v", err)
	}
	dataSection := resp[19+h.ParamLen : 19+h.ParamLen+h.DataLen]
	if len(dataSection) < 1 {
		t.Fatalf("data section too short: % X", dataSection)
	}
	rc = dataSection[0]
	if rc != ResOK {
		return rc, 0, nil, nil
	}
	if len(dataSection) < 4 {
		t.Fatalf("data section too short: % X", dataSection)
	}
	ts = dataSection[1]
	length := int(binary.BigEndian.Uint16(dataSection[2:4]))
	n := dataItemBytes(ts, length)
	if len(dataSection) < 4+n {
		t.Fatalf("data item truncated: have %d want %d", len(dataSection), 4+n)
	}
	return rc, ts, dataSection[4 : 4+n], dataSection[4+n:]
}

// itemBytes 将 item 编码为 12 字节的 S7ANY 请求项（addr = offset<<3 | bit）。
func itemBytes(it *item) []byte {
	addr := (it.Offset << 3) | it.Bit
	return []byte{0x12, 0x0A, 0x10, it.TS,
		byte(it.Length >> 8), byte(it.Length),
		byte(it.DBNum >> 8), byte(it.DBNum), it.Area,
		byte(addr >> 16), byte(addr >> 8), byte(addr)}
}

// readParams 构造 Read Var 参数区：04 <count> + items。
func readParams(items ...*item) []byte {
	params := []byte{FuncRead, byte(len(items))}
	for _, it := range items {
		params = append(params, itemBytes(it)...)
	}
	return params
}

// writeParams 构造 Write Var 参数区：05 <count> + items。
func writeParams(items ...*item) []byte {
	params := []byte{FuncWrite, byte(len(items))}
	for _, it := range items {
		params = append(params, itemBytes(it)...)
	}
	return params
}

// writeData 构造 Write Var 数据区：每项 00 + ts + len(2) + data + [fill]。
// 长度单位与 plc4x 一致：BIT/BYTE_WORD_DWORD/INTEGER 以位计，其余以字节计。
// 每项输入格式为 []byte{ts, data...}。
func writeData(dataItems ...[]byte) []byte {
	var data []byte
	for i, di := range dataItems {
		ts := di[0]
		payload := di[1:]
		length := len(payload)
		if lengthInBits(ts) {
			length = len(payload) * 8
		}
		buf := []byte{0x00, ts, byte(length >> 8), byte(length)}
		buf = append(buf, payload...)
		if i < len(dataItems)-1 && len(payload)%2 == 1 {
			buf = append(buf, 0x00) // 非最后一项的奇数长度填充
		}
		data = append(data, buf...)
	}
	return data
}

func TestParseItem(t *testing.T) {
	// 构造 DB1.DBD0 的 item：TS=BYTE, len=4, db=1, area=DB, addr=0
	b := []byte{0x12, 0x0A, 0x10, TSByte, 0x00, 0x04, 0x00, 0x01, AreaDB, 0x00, 0x00, 0x00}
	it, err := parseItem(b)
	if err != nil {
		t.Fatalf("parseItem: %v", err)
	}
	if it.Area != AreaDB || it.DBNum != 1 || it.Offset != 0 || it.Bit != 0 || it.TS != TSByte || it.Length != 4 {
		t.Errorf("item = %+v", it)
	}

	// 位寻址 DB1.DBX5.3 -> addr = (5<<3)|3 = 0x2B
	b = []byte{0x12, 0x0A, 0x10, TSBit, 0x00, 0x01, 0x00, 0x01, AreaDB, 0x00, 0x00, 0x2B}
	it, err = parseItem(b)
	if err != nil {
		t.Fatalf("parseItem: %v", err)
	}
	if it.Offset != 5 || it.Bit != 3 || it.TS != TSBit {
		t.Errorf("item = %+v, want offset=5 bit=3", it)
	}
	if it.String() != "DB1 @5.3" {
		t.Errorf("String() = %q, want %q", it.String(), "DB1 @5.3")
	}

	// 非法 specifier
	if _, err := parseItem([]byte{0x11, 0x0A, 0x10, 0x02, 0, 1, 0, 1, AreaDB, 0, 0, 0}); err == nil {
		t.Error("parseItem(bad spec) = nil error, want error")
	}
	// 过短
	if _, err := parseItem(make([]byte, 11)); err == nil {
		t.Error("parseItem(short) = nil error, want error")
	}
}

func TestElementSizeAndTransportSizeMapping(t *testing.T) {
	// 每元素字节数（与 gos7 dataSizeByte 一致）
	sizes := map[byte]int{
		TSBit: 1, TSByte: 1, TSChar: 1,
		TSWord: 2, TSInt: 2, TSDate: 2, TSS5: 2,
		TSDWord: 4, TSDInt: 4, TSReal: 4, TSTod: 4, TSTime: 4,
	}
	for ts, want := range sizes {
		if got := elementSize(ts); got != want {
			t.Errorf("elementSize(0x%02X) = %d, want %d", ts, got, want)
		}
	}
	if elementSize(0xEE) != -1 {
		t.Errorf("elementSize(unknown) = %d, want -1", elementSize(0xEE))
	}

	// 请求 TS -> 响应 TS 映射（plc4x DataTransportSize）
	mapping := map[byte]byte{
		TSBit: TSResBit, TSByte: TSResByte, TSChar: TSResOctet,
		TSWord: TSResByte, TSDWord: TSResByte, TSInt: TSResInt, TSDInt: TSResInt,
		TSReal: TSResReal, TSDate: TSResByte, TSTod: TSResByte, TSTime: TSResByte,
	}
	for reqTS, wantResTS := range mapping {
		if got := respTransportSize(reqTS); got != wantResTS {
			t.Errorf("respTransportSize(0x%02X) = 0x%02X, want 0x%02X", reqTS, got, wantResTS)
		}
	}

	// 长度单位：BIT/BYTE_WORD_DWORD/INTEGER 以位计，REAL/OCTET_STRING 以字节计
	for _, ts := range []byte{TSResBit, TSResByte, TSResInt} {
		if !lengthInBits(ts) {
			t.Errorf("lengthInBits(0x%02X) = false, want true", ts)
		}
	}
	for _, ts := range []byte{TSResReal, TSResOctet, TSResDInt} {
		if lengthInBits(ts) {
			t.Errorf("lengthInBits(0x%02X) = true, want false", ts)
		}
	}
}

func TestDataItemBytes(t *testing.T) {
	cases := []struct {
		ts     byte
		length int
		want   int
	}{
		{TSResBit, 1, 1},      // 1 bit -> 1 字节
		{TSResByte, 8, 1},     // 8 bit -> 1 字节
		{TSResByte, 32, 4},    // 32 bit -> 4 字节
		{TSResByte, 520, 65},  // ceil
		{TSResInt, 16, 2},     // 16 bit -> 2 字节
		{TSResReal, 4, 4},     // 4 字节
		{TSResOctet, 30, 30},  // 30 字节
	}
	for _, tc := range cases {
		if got := dataItemBytes(tc.ts, tc.length); got != tc.want {
			t.Errorf("dataItemBytes(0x%02X, %d) = %d, want %d", tc.ts, tc.length, got, tc.want)
		}
	}
}

func TestBuildDataItem(t *testing.T) {
	// BYTE_WORD_DWORD：长度以位计（8×字节）
	di := buildDataItem(ResOK, TSResByte, []byte{1, 2, 3, 4}, false)
	want := []byte{ResOK, TSResByte, 0x00, 0x20, 1, 2, 3, 4}
	if !bytes.Equal(di, want) {
		t.Errorf("byte item = % X, want % X", di, want)
	}
	// REAL：长度以字节计
	di = buildDataItem(ResOK, TSResReal, []byte{1, 2, 3, 4}, false)
	want = []byte{ResOK, TSResReal, 0x00, 0x04, 1, 2, 3, 4}
	if !bytes.Equal(di, want) {
		t.Errorf("real item = % X, want % X", di, want)
	}
	// BIT：长度 = 位数（1）
	di = buildDataItem(ResOK, TSResBit, []byte{1}, false)
	want = []byte{ResOK, TSResBit, 0x00, 0x01, 1}
	if !bytes.Equal(di, want) {
		t.Errorf("bit item = % X, want % X", di, want)
	}
	// 奇数长度 + fill
	di = buildDataItem(ResOK, TSResByte, []byte{1, 2, 3}, true)
	want = []byte{ResOK, TSResByte, 0x00, 0x18, 1, 2, 3, 0x00}
	if !bytes.Equal(di, want) {
		t.Errorf("odd item = % X, want % X", di, want)
	}
	// 奇数长度 + 非最后一项的填充仅在 fill=true 时追加
	di = buildDataItem(ResOK, TSResByte, []byte{1, 2, 3}, false)
	if len(di) != 7 {
		t.Errorf("odd item without fill = % X, want 7 bytes", di)
	}
}

func TestParseDataItem(t *testing.T) {
	// 偶数长度项
	blob := buildDataItem(ResOK, TSResByte, []byte{1, 2, 3, 4}, false)
	di, next, err := parseDataItem(blob, 0)
	if err != nil {
		t.Fatalf("parseDataItem: %v", err)
	}
	if di.ts != TSResByte || !bytes.Equal(di.data, []byte{1, 2, 3, 4}) {
		t.Errorf("item = %+v", di)
	}
	if next != len(blob) {
		t.Errorf("next = %d, want %d", next, len(blob))
	}
	// 过短
	if _, _, err := parseDataItem(blob, len(blob)-2); err == nil {
		t.Error("parseDataItem(truncated) = nil error, want error")
	}
}

// makeReadJob 构造 Read Var 请求帧（ROSCTR=Job，单/多 item）。
func makeReadJob(ref uint16, items ...*item) []byte {
	return BuildFrame(BuildJob(ref, readParams(items...), nil))
}

// makeWriteJob 构造 Write Var 请求帧（ROSCTR=Job，单/多 item，数据项由调用方给出）。
func makeWriteJob(ref uint16, items []*item, dataItems [][]byte) []byte {
	return BuildFrame(BuildJob(ref, writeParams(items...), writeData(dataItems...)))
}

func TestHandleReadByte(t *testing.T) {
	s := testServer(t)
	resp := s.handleRead(PDUHeader{Ref: 1}, readParams(
		&item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSByte, Length: 2}), "test")
	rc, ts, data, _ := respDataItem(t, resp)
	if rc != ResOK {
		t.Fatalf("rc = 0x%02X, want 0xFF", rc)
	}
	if ts != TSResByte {
		t.Errorf("ts = 0x%02X, want 0x%02X", ts, TSResByte)
	}
	// DB1.DBB8 = StatusWord = 0x01FF
	if !bytes.Equal(data, []byte{0x01, 0xFF}) {
		t.Errorf("data = % X, want 01 FF", data)
	}
}

func TestHandleReadReal(t *testing.T) {
	s := testServer(t)
	// TS=REAL, numElements=1 -> 4 字节，响应 ts=REAL，长度以字节计
	resp := s.handleRead(PDUHeader{Ref: 2}, readParams(
		&item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSReal, Length: 1}), "test")
	rc, ts, data, _ := respDataItem(t, resp)
	if rc != ResOK || ts != TSResReal {
		t.Fatalf("rc=0x%02X ts=0x%02X, want FF / 0x%02X", rc, ts, TSResReal)
	}
	if len(data) != 4 {
		t.Fatalf("len(data) = %d, want 4", len(data))
	}
	f := math.Float32frombits(binary.BigEndian.Uint32(data))
	if f != 12.5 {
		t.Errorf("TankLevel = %v, want 12.5", f)
	}
}

func TestHandleReadWord(t *testing.T) {
	s := testServer(t)
	// TS=WORD, numElements=2 -> 4 字节（StatusWord + Counter 高 2 字节）
	resp := s.handleRead(PDUHeader{Ref: 3}, readParams(
		&item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSWord, Length: 2}), "test")
	rc, ts, data, _ := respDataItem(t, resp)
	if rc != ResOK || ts != TSResByte {
		t.Fatalf("rc=0x%02X ts=0x%02X", rc, ts)
	}
	want := []byte{0x01, 0xFF, 0x00, 0x00} // StatusWord=0x01FF, Counter=1000=0x000003E8
	if !bytes.Equal(data, want) {
		t.Errorf("data = % X, want % X", data, want)
	}
}

func TestHandleReadBit(t *testing.T) {
	s := testServer(t)
	// DB1.DBX4.0 = PumpRunning = true
	resp := s.handleRead(PDUHeader{Ref: 4}, []byte{FuncRead, 1,
		0x12, 0x0A, 0x10, TSBit, 0x00, 0x01, 0x00, 0x01, AreaDB, 0x00, 0x00, 0x20}, "test")
	rc, ts, data, _ := respDataItem(t, resp)
	if rc != ResOK || ts != TSResBit {
		t.Fatalf("rc=0x%02X ts=0x%02X", rc, ts)
	}
	if len(data) != 1 || data[0] != 1 {
		t.Errorf("data = % X, want 01", data)
	}
	// DB1.DBX4.1 = Fault = false
	resp = s.handleRead(PDUHeader{Ref: 5}, []byte{FuncRead, 1,
		0x12, 0x0A, 0x10, TSBit, 0x00, 0x01, 0x00, 0x01, AreaDB, 0x00, 0x00, 0x21}, "test")
	_, _, data, _ = respDataItem(t, resp)
	if len(data) != 1 || data[0] != 0 {
		t.Errorf("data = % X, want 00", data)
	}
}

func TestHandleReadErrors(t *testing.T) {
	s := testServer(t)
	cases := []struct {
		name string
		item *item
		rc   byte
	}{
		{"missing db", &item{Area: AreaDB, DBNum: 99, Offset: 0, TS: TSByte, Length: 4}, ResObjectNotFound},
		{"unsupported area", &item{Area: AreaMK, DBNum: 0, Offset: 0, TS: TSByte, Length: 4}, ResObjectNotFound},
		{"out of range", &item{Area: AreaDB, DBNum: 1, Offset: 200, TS: TSByte, Length: 4}, ResObjectNotAvailable},
		{"zero length", &item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSByte, Length: 0}, ResDataTypeNotsupp},
		{"bad ts", &item{Area: AreaDB, DBNum: 1, Offset: 0, TS: 0xEE, Length: 1}, ResDataTypeNotsupp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.handleRead(PDUHeader{Ref: 6}, readParams(tc.item), "test")
			rc, _, _, _ := respDataItem(t, resp)
			if rc != tc.rc {
				t.Errorf("rc = 0x%02X, want 0x%02X", rc, tc.rc)
			}
		})
	}
}

func TestHandleReadMultipleItemsWithFill(t *testing.T) {
	s := testServer(t)
	// 3 个 item：4 字节（偶）+ 3 字节（奇，需填充）+ 2 字节（偶）
	it1 := &item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSByte, Length: 4}
	it2 := &item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSByte, Length: 3}
	it3 := &item{Area: AreaDB, DBNum: 1, Offset: 12, TS: TSByte, Length: 2}
	resp := s.handleRead(PDUHeader{Ref: 7}, readParams(it1, it2, it3), "test")
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	h, _ := ParsePDUHeader(f.PDU)
	if h.ParamLen != 2 {
		t.Errorf("ParamLen = %d, want 2", h.ParamLen)
	}
	dataSection := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]

	// item1: FF 04 00 20 <4B>（偶数，无填充）
	// item2: FF 04 00 18 <3B> 00（非最后一项且奇数，补 0x00）
	// item3: FF 04 00 10 <2B>（最后一项，无填充）
	want := []byte{
		ResOK, TSResByte, 0x00, 0x20, 0x41, 0x48, 0x00, 0x00, // REAL 12.5
		ResOK, TSResByte, 0x00, 0x18, 0x01, 0xFF, 0x00, 0x00, // StatusWord + 1 字节 + 填充
		ResOK, TSResByte, 0x00, 0x10, 0x00, 0x00, // Counter 高 2 字节
	}
	if !bytes.Equal(dataSection, want) {
		t.Errorf("data section =\n% X\nwant\n% X", dataSection, want)
	}
}

// TestHandleReadBitItemFill 验证 BIT item（1 字节数据，奇数）在非最后位置时
// 同样需要填充字节（plc4x 的 2 字节对齐规则）。
func TestHandleReadBitItemFill(t *testing.T) {
	s := testServer(t)
	it1 := &item{Area: AreaDB, DBNum: 1, Offset: 4, Bit: 0, TS: TSBit, Length: 1}
	it2 := &item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSByte, Length: 2}
	resp := s.handleRead(PDUHeader{Ref: 13}, readParams(it1, it2), "test")
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	h, _ := ParsePDUHeader(f.PDU)
	dataSection := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	// BIT item 数据 1 字节（奇数），非最后一项 -> 补 0x00
	want := []byte{
		ResOK, TSResBit, 0x00, 0x01, 0x01, 0x00, // PumpRunning=true + 填充
		ResOK, TSResByte, 0x00, 0x10, 0x01, 0xFF, // StatusWord
	}
	if !bytes.Equal(dataSection, want) {
		t.Errorf("data section =\n% X\nwant\n% X", dataSection, want)
	}
}

// TestHandleReadMixedItems 验证成功项与错误项混合时：
// 错误项为完整 4 字节头（返回码 + NULL + 长度 0），plc4x 可顺序解析；
// 奇数长度成功项后仍有填充字节。
func TestHandleReadMixedItems(t *testing.T) {
	s := testServer(t)
	// item1：越界（错误）；item2：3 字节（奇数，非最后 -> 填充）；item3：DB99 不存在（错误）
	resp := s.handleRead(PDUHeader{Ref: 14}, readParams(
		&item{Area: AreaDB, DBNum: 1, Offset: 200, TS: TSByte, Length: 4},
		&item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSByte, Length: 3},
		&item{Area: AreaDB, DBNum: 99, TS: TSByte, Length: 4},
	), "test")
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	h, _ := ParsePDUHeader(f.PDU)
	if h.ParamLen != 2 {
		t.Fatalf("ParamLen = %d, want 2", h.ParamLen)
	}
	dataSection := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	want := []byte{
		ResObjectNotAvailable, TSResNull, 0x00, 0x00,       // 错误项：4 字节头，无数据
		ResOK, TSResByte, 0x00, 0x18, 0x01, 0xFF, 0x00, 0x00, // 3 字节 + 填充
		ResObjectNotFound, TSResNull, 0x00, 0x00,           // 错误项：4 字节头，无数据
	}
	if !bytes.Equal(dataSection, want) {
		t.Errorf("data section =\n% X\nwant\n% X", dataSection, want)
	}
}

func TestHandleSetup(t *testing.T) {
	s := testServer(t)
	params := []byte{FuncSetup, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0xF0}
	resp := s.handleSetup(PDUHeader{Ref: 8}, params)
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	h, _ := ParsePDUHeader(f.PDU)
	respParams := f.PDU[12 : 12+h.ParamLen]
	if respParams[0] != FuncSetup {
		t.Errorf("param[0] = 0x%02X, want 0xF0", respParams[0])
	}
	if got := binary.BigEndian.Uint16(respParams[6:8]); got != 240 {
		t.Errorf("negotiated PDU = %d, want 240", got)
	}
	// 过短参数不响应
	if s.handleSetup(PDUHeader{}, []byte{FuncSetup, 0x00}) != nil {
		t.Error("handleSetup(short) != nil")
	}
}

func TestHandleWrite(t *testing.T) {
	s := testServer(t)
	// 写 DB1.DBD40 = DE AD BE EF（数据项长度以位计：32）
	data := buildDataItem(0x00, TSResByte, []byte{0xDE, 0xAD, 0xBE, 0xEF}, false)
	resp := s.handleWrite(PDUHeader{Ref: 9},
		[]byte{FuncWrite, 1,
			0x12, 0x0A, 0x10, TSByte, 0x00, 0x04, 0x00, 0x01, AreaDB, 0x00, 0x01, 0x40},
		data, "test")
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	h, _ := ParsePDUHeader(f.PDU)
	respParams := f.PDU[12 : 12+h.ParamLen]
	respData := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	if respParams[0] != FuncWrite || respParams[1] != 1 {
		t.Errorf("params = % X, want 05 01", respParams)
	}
	if len(respData) != 1 || respData[0] != ResOK {
		t.Errorf("data = % X, want FF", respData)
	}
	// 验证 DB 内容
	buf, err := s.db(1).Read(40, 4)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("DB1[40:44] = % X, want DE AD BE EF", buf)
	}
}

func TestHandleWriteBit(t *testing.T) {
	s := testServer(t)
	// 写 DB1.DBX60.7 = 1
	data := buildDataItem(0x00, TSResBit, []byte{1}, false)
	resp := s.handleWrite(PDUHeader{Ref: 10},
		[]byte{FuncWrite, 1,
			0x12, 0x0A, 0x10, TSBit, 0x00, 0x01, 0x00, 0x01, AreaDB, 0x00, 0x01, 0xE7},
		data, "test")
	f, _ := ParseFrame(resp)
	h, _ := ParsePDUHeader(f.PDU)
	respData := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	if len(respData) != 1 || respData[0] != ResOK {
		t.Fatalf("write bit rc = % X, want FF", respData)
	}
	on, err := s.db(1).GetBit(60, 7)
	if err != nil || !on {
		t.Errorf("GetBit(60,7) = %v, %v; want true", on, err)
	}
}

func TestHandleWriteErrors(t *testing.T) {
	s := testServer(t)
	// 写不存在的 DB
	data := buildDataItem(0x00, TSResByte, []byte{1, 2, 3, 4}, false)
	resp := s.handleWrite(PDUHeader{Ref: 11},
		[]byte{FuncWrite, 1,
			0x12, 0x0A, 0x10, TSByte, 0x00, 0x04, 0x00, 0x63, AreaDB, 0x00, 0x00, 0x00},
		data, "test")
	f, _ := ParseFrame(resp)
	h, _ := ParsePDUHeader(f.PDU)
	respData := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	if len(respData) != 1 || respData[0] != ResObjectNotFound {
		t.Errorf("missing DB rc = % X, want 0A", respData)
	}
	// 写越界（offset 126 + 4 字节 > DB1 的 128 字节）
	resp = s.handleWrite(PDUHeader{Ref: 12},
		[]byte{FuncWrite, 1,
			0x12, 0x0A, 0x10, TSByte, 0x00, 0x04, 0x00, 0x01, AreaDB, 0x00, 0x03, 0xF0},
		data, "test")
	f, _ = ParseFrame(resp)
	h, _ = ParsePDUHeader(f.PDU)
	respData = f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	if len(respData) != 1 || respData[0] != ResObjectNotAvailable {
		t.Errorf("out of range rc = % X, want 05", respData)
	}
}

func TestHandleUserDataSZL(t *testing.T) {
	s := testServer(t)
	// 构造 Read SZL 请求：ROSCTR=0x07
	params := []byte{0x00, 0x01, UDItemTypeCPU, 0x08, UDMethodReadSZL, UDTypeGroupSZLReq, UDSubReadSZL, 0x00, 0x00, 0x00, 0x00, 0x00}
	data := []byte{ResOK, TSResOctet, 0x00, 0x04, 0x00, 0x1C, 0x00, 0x01}
	pdu := BuildUserData(0x0100, params, data)
	h, _ := ParsePDUHeader(pdu)
	resp := s.handleUserData(h, pdu, "test")
	if resp == nil {
		t.Fatal("handleUserData = nil, want SZL response")
	}
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	rh, _ := ParsePDUHeader(f.PDU)
	if rh.ROSCTR != ROSCTRUser {
		t.Errorf("rosctr = 0x%02X, want 0x07", rh.ROSCTR)
	}
	// UserData PDU 无错误类/码字节，参数区从 PDU 偏移 10 开始
	respParams := f.PDU[10 : 10+rh.ParamLen]
	respData := f.PDU[10+rh.ParamLen : 10+rh.ParamLen+rh.DataLen]
	// 方法 0x12（应答）、类型|组 0x84（类型 0x8=应答 | 组 0x4=SZL）
	// plc4x 据此将负载分派为 S7PayloadUserDataItemCpuFunctionReadSzlResponse
	if respParams[2] != UDItemTypeCPU || respParams[4] != UDMethodReadSZL+1 || respParams[5] != UDTypeGroupSZLRsp {
		t.Errorf("params = % X, want ... 12 08 12 84 01 ...", respParams)
	}
	if respData[0] != ResOK || respData[1] != TSResOctet {
		t.Errorf("data head = % X, want FF 09", respData[:2])
	}
	szl := respData[4:]
	// plc4x 旧版结构化格式：szlId(2) + szlIndex(2) + szlItemLength(2) + szlItemCount(2) + record(28)
	if len(szl) != 36 {
		t.Fatalf("len(szl) = %d, want 36", len(szl))
	}
	if binary.BigEndian.Uint16(szl[0:2]) != 0x001C {
		t.Errorf("szlId = % X, want 001C", szl[0:2])
	}
	if binary.BigEndian.Uint16(szl[2:4]) != 0x0001 {
		t.Errorf("szlIndex = % X, want 0001", szl[2:4])
	}
	if binary.BigEndian.Uint16(szl[4:6]) != 28 {
		t.Errorf("szlItemLength = %d, want 28", binary.BigEndian.Uint16(szl[4:6]))
	}
	if binary.BigEndian.Uint16(szl[6:8]) != 1 {
		t.Errorf("szlItemCount = %d, want 1", binary.BigEndian.Uint16(szl[6:8]))
	}
	// 记录含可读型号名（plc4x 据此识别 CPU 型号）
	if !bytes.Contains(szl, []byte("CPU 315-2 PN/DP")) {
		t.Errorf("SZL record missing model name: % X", szl)
	}
}

func TestHandleUserDataNonSZL(t *testing.T) {
	s := testServer(t)
	// 非 CPU 功能请求：不响应
	params := []byte{0x00, 0x01, 0x13, 0x08, 0x11, 0x44, 0x01, 0x00, 0, 0, 0, 0}
	pdu := BuildUserData(0x0101, params, nil)
	h, _ := ParsePDUHeader(pdu)
	if resp := s.handleUserData(h, pdu, "test"); resp != nil {
		t.Errorf("handleUserData(non-CPU) = % X, want nil", resp)
	}
	// 非 Read SZL 方法：不响应
	params = []byte{0x00, 0x01, UDItemTypeCPU, 0x08, 0x12, UDTypeGroupSZLReq, UDSubReadSZL, 0x00, 0, 0, 0, 0}
	pdu = BuildUserData(0x0102, params, nil)
	h, _ = ParsePDUHeader(pdu)
	if resp := s.handleUserData(h, pdu, "test"); resp != nil {
		t.Errorf("handleUserData(non-SZL) = % X, want nil", resp)
	}
}
