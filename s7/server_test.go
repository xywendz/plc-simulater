package s7

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"math"
	"net"
	"testing"
	"time"

	"plc-simulater/config"
)

// startTestServer 在临时端口上启动服务端，返回地址与关闭函数。
func startTestServer(t *testing.T, cfg *config.Config) (string, func()) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.logger = log.New(io.Discard, "", 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go s.Serve(ln)
	return ln.Addr().String(), func() { ln.Close() }
}

// testConfig 返回集成测试用配置：DB1 带具名字段，DB2 为空，DB20 仅有长度。
func testConfig() *config.Config {
	return &config.Config{
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
			{Number: 20, Size: 256},
		},
	}
}

// s7Client 是测试用的原始 S7 客户端（COTP 握手 + 报文收发）。
type s7Client struct {
	t    *testing.T
	conn net.Conn
	ref  uint16
}

// dial 完成 COTP Connect + Setup Communication 握手，返回就绪的客户端。
func dial(t *testing.T, addr string) *s7Client {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	c := &s7Client{t: t, conn: conn}

	// COTP Connect Request（与 snap7/gos7 一致：C0 TPDU size、C1/C2 TSAP）
	cr := []byte{
		0x03, 0x00, 0x00, 0x16, 0x11, 0xE0, 0x00, 0x00, 0x00, 0x01, 0x00, 0xC0,
		0x01, 0x0A, 0xC1, 0x02, 0x01, 0x00, 0xC2, 0x02, 0x01, 0x02,
	}
	if _, err := conn.Write(cr); err != nil {
		t.Fatalf("write CR: %v", err)
	}
	cc, err := c.readFrame()
	if err != nil {
		t.Fatalf("read CC: %v", err)
	}
	if len(cc) < 6 || cc[4] != 0x11 || cc[5] != COTPCC {
		t.Fatalf("bad CC: % X", cc)
	}
	// dstRef 应回显请求的 srcRef（snap7 IsoConfirmConnection 的解析前提）
	if cc[6] != cr[8] || cc[7] != cr[9] {
		t.Errorf("CC dstRef = % X, want echo of CR srcRef % X", cc[6:8], cr[8:10])
	}
	// TSAP 对调：CC 的 src TSAP = CR 的 dst TSAP
	if cc[16] != cr[20] || cc[17] != cr[21] || cc[20] != cr[16] || cc[21] != cr[17] {
		t.Errorf("CC TSAPs = % X, want swapped CR TSAPs", cc[14:22])
	}
	// TPDU size 参数存在
	if cc[11] != 0xC0 {
		t.Errorf("CC TPDU size param code = 0x%02X, want 0xC0", cc[11])
	}

	// Setup Communication
	negotiate := []byte{
		0x03, 0x00, 0x00, 0x19, 0x02, 0xF0, 0x80, 0x32, 0x01, 0x00, 0x00, 0x04,
		0x00, 0x00, 0x08, 0x00, 0x00, 0xF0, 0x00, 0x00, 0x01, 0x00, 0x01, 0x01,
		0xE0,
	}
	if _, err := conn.Write(negotiate); err != nil {
		t.Fatalf("write negotiate: %v", err)
	}
	resp, err := c.readFrame()
	if err != nil {
		t.Fatalf("read negotiate resp: %v", err)
	}
	// 响应 PDU 长度在帧偏移 [25:27]（与 cmd/s7test 一致）
	if len(resp) < 27 {
		t.Fatalf("negotiate response too short: % X", resp)
	}
	if pduLen := int(binary.BigEndian.Uint16(resp[25:27])); pduLen != 240 {
		t.Errorf("negotiated PDU = %d, want 240", pduLen)
	}
	return c
}

func (c *s7Client) readFrame() ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	if n < 7 || n > 4096 {
		return nil, io.ErrUnexpectedEOF
	}
	frame := make([]byte, n)
	copy(frame, hdr)
	if _, err := io.ReadFull(c.conn, frame[4:]); err != nil {
		return nil, err
	}
	return frame, nil
}

func (c *s7Client) writeFrame(frame []byte) {
	c.t.Helper()
	if _, err := c.conn.Write(frame); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *s7Client) nextRef() uint16 { c.ref++; return c.ref }

// readVar 发送 Read Var 请求并返回每个数据项的原始字节。
func (c *s7Client) readVar(items ...*item) [][]byte {
	c.t.Helper()
	c.writeFrame(makeReadJob(c.nextRef(), items...))
	resp, err := c.readFrame()
	if err != nil {
		c.t.Fatalf("read frame: %v", err)
	}
	f, err := ParseFrame(resp)
	if err != nil {
		c.t.Fatalf("parse frame: %v", err)
	}
	h, err := ParsePDUHeader(f.PDU)
	if err != nil {
		c.t.Fatalf("parse pdu: %v", err)
	}
	if h.ROSCTR != ROSCTRAckData {
		c.t.Fatalf("rosctr = 0x%02X, want Ack-Data", h.ROSCTR)
	}
	params := f.PDU[12 : 12+h.ParamLen]
	if params[0] != FuncRead || int(params[1]) != len(items) {
		c.t.Fatalf("params = % X, want 04 %02X", params, len(items))
	}
	data := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	var out [][]byte
	off := 0
	for i := 0; i < len(items); i++ {
		if off >= len(data) {
			c.t.Fatalf("data section truncated: % X", data)
		}
		rc := data[off]
		if rc != ResOK {
			c.t.Fatalf("item %d rc = 0x%02X, want 0xFF", i, rc)
		}
		ts := data[off+1]
		length := int(binary.BigEndian.Uint16(data[off+2:]))
		n := dataItemBytes(ts, length)
		out = append(out, data[off+4:off+4+n])
		off += 4 + n
		// 非最后一项的奇数长度填充
		if n%2 == 1 && i < len(items)-1 {
			off++
		}
	}
	return out
}

// writeVar 发送 Write Var 请求并返回每个 item 的返回码。
func (c *s7Client) writeVar(items []*item, dataItems [][]byte) []byte {
	c.t.Helper()
	c.writeFrame(makeWriteJob(c.nextRef(), items, dataItems))
	resp, err := c.readFrame()
	if err != nil {
		c.t.Fatalf("read frame: %v", err)
	}
	f, err := ParseFrame(resp)
	if err != nil {
		c.t.Fatalf("parse frame: %v", err)
	}
	h, err := ParsePDUHeader(f.PDU)
	if err != nil {
		c.t.Fatalf("parse pdu: %v", err)
	}
	params := f.PDU[12 : 12+h.ParamLen]
	if params[0] != FuncWrite || int(params[1]) != len(items) {
		c.t.Fatalf("params = % X, want 05 %02X", params, len(items))
	}
	return f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
}

// readSZL 发送 Read SZL UserData 请求并返回 SZL 数据段。
func (c *s7Client) readSZL(id, index uint16) []byte {
	c.t.Helper()
	params := []byte{0x00, 0x01, UDItemTypeCPU, 0x08, UDMethodReadSZL, UDTypeGroupSZLReq, UDSubReadSZL, 0x00, 0x00, 0x00, 0x00, 0x00}
	data := []byte{0x00, TSResOctet, 0x00, 0x04, byte(id >> 8), byte(id), byte(index >> 8), byte(index)}
	c.writeFrame(BuildFrame(BuildUserData(c.nextRef(), params, data)))
	resp, err := c.readFrame()
	if err != nil {
		c.t.Fatalf("read frame: %v", err)
	}
	f, err := ParseFrame(resp)
	if err != nil {
		c.t.Fatalf("parse frame: %v", err)
	}
	h, err := ParsePDUHeader(f.PDU)
	if err != nil {
		c.t.Fatalf("parse pdu: %v", err)
	}
	if h.ROSCTR != ROSCTRUser {
		c.t.Fatalf("rosctr = 0x%02X, want UserData", h.ROSCTR)
	}
	respParams := f.PDU[10 : 10+h.ParamLen]
	if respParams[2] != UDItemTypeCPU || respParams[4] != UDMethodReadSZL+1 {
		c.t.Fatalf("resp params = % X", respParams)
	}
	respData := f.PDU[10+h.ParamLen : 10+h.ParamLen+h.DataLen]
	if respData[0] != ResOK || respData[1] != TSResOctet {
		c.t.Fatalf("resp data head = % X", respData[:2])
	}
	length := int(binary.BigEndian.Uint16(respData[2:4]))
	return respData[4 : 4+length]
}

func (c *s7Client) close() { c.conn.Close() }

func TestServerHandshake(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()
	// 握手报文不经过 nextRef，客户端引用计数仍为 0；
	// 之后的第一个请求引用为 1（由 readVar/writeVar 隐式验证）。
	if c.ref != 0 {
		t.Errorf("ref after handshake = %d, want 0", c.ref)
	}
}

func TestServerReadInitialValues(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// DB1 前 40 字节
	buf := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSByte, Length: 40})[0]
	if len(buf) != 40 {
		t.Fatalf("len = %d, want 40", len(buf))
	}
	if got := math.Float32frombits(binary.BigEndian.Uint32(buf[0:4])); got != 12.5 {
		t.Errorf("TankLevel = %v, want 12.5", got)
	}
	if buf[4]&0x01 == 0 {
		t.Error("PumpRunning(DBX4.0) = false, want true")
	}
	if buf[4]&0x02 != 0 {
		t.Error("Fault(DBX4.1) = true, want false")
	}
	if got := binary.BigEndian.Uint16(buf[8:10]); got != 0x01FF {
		t.Errorf("StatusWord = 0x%04X, want 0x01FF", got)
	}
	if got := binary.BigEndian.Uint32(buf[12:16]); got != 1000 {
		t.Errorf("Counter = %d, want 1000", got)
	}
	if got := int16(binary.BigEndian.Uint16(buf[16:18])); got != -25 {
		t.Errorf("Temperature = %d, want -25", got)
	}
	if string(buf[32:40]) != "Hello S7" {
		t.Errorf("ProductName = %q, want %q", string(buf[32:40]), "Hello S7")
	}
}

func TestServerReadTypedAccess(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// REAL 读：1 个元素 = 4 字节，响应 ts=REAL，长度以字节计
	real := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSReal, Length: 1})[0]
	if got := math.Float32frombits(binary.BigEndian.Uint32(real)); got != 12.5 {
		t.Errorf("REAL read = %v, want 12.5", got)
	}
	// WORD 读：2 个元素 = 4 字节
	words := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSWord, Length: 2})[0]
	if !bytes.Equal(words, []byte{0x01, 0xFF, 0x00, 0x00}) {
		t.Errorf("WORD read = % X, want 01 FF 00 00", words)
	}
	// INT 读
	intVal := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 16, TS: TSInt, Length: 1})[0]
	if got := int16(binary.BigEndian.Uint16(intVal)); got != -25 {
		t.Errorf("INT read = %d, want -25", got)
	}
	// DWORD 读
	dw := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 12, TS: TSDWord, Length: 1})[0]
	if got := binary.BigEndian.Uint32(dw); got != 1000 {
		t.Errorf("DWORD read = %d, want 1000", got)
	}
	// BIT 读
	bit := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 4, Bit: 0, TS: TSBit, Length: 1})[0]
	if len(bit) != 1 || bit[0] != 1 {
		t.Errorf("BIT read = % X, want 01", bit)
	}
	bit = c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 4, Bit: 1, TS: TSBit, Length: 1})[0]
	if len(bit) != 1 || bit[0] != 0 {
		t.Errorf("BIT read = % X, want 00", bit)
	}
}

func TestServerWriteReadBack(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// 字节写
	rcs := c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 1, Offset: 40, TS: TSByte, Length: 4}},
		[][]byte{{TSResByte, 0xDE, 0xAD, 0xBE, 0xEF}})
	if len(rcs) != 1 || rcs[0] != ResOK {
		t.Fatalf("write rc = % X, want FF", rcs)
	}
	got := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 40, TS: TSByte, Length: 4})[0]
	if !bytes.Equal(got, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("read back = % X, want DE AD BE EF", got)
	}

	// 位写：DB1.DBX60.7
	rcs = c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 1, Offset: 60, Bit: 7, TS: TSBit, Length: 1}},
		[][]byte{{TSResBit, 1}})
	if len(rcs) != 1 || rcs[0] != ResOK {
		t.Fatalf("bit write rc = % X, want FF", rcs)
	}
	bit := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 60, Bit: 7, TS: TSBit, Length: 1})[0]
	if bit[0] != 1 {
		t.Errorf("bit read back = %d, want 1", bit[0])
	}
	// 清位
	c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 1, Offset: 60, Bit: 7, TS: TSBit, Length: 1}},
		[][]byte{{TSResBit, 0}})
	bit = c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 60, Bit: 7, TS: TSBit, Length: 1})[0]
	if bit[0] != 0 {
		t.Errorf("bit read back = %d, want 0", bit[0])
	}

	// 写 DB2（无字段配置，仅长度）
	rcs = c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 2, Offset: 100, TS: TSByte, Length: 2}},
		[][]byte{{TSResByte, 0x12, 0x34}})
	if len(rcs) != 1 || rcs[0] != ResOK {
		t.Fatalf("DB2 write rc = % X, want FF", rcs)
	}
	got = c.readVar(&item{Area: AreaDB, DBNum: 2, Offset: 100, TS: TSByte, Length: 2})[0]
	if !bytes.Equal(got, []byte{0x12, 0x34}) {
		t.Errorf("DB2 read back = % X, want 12 34", got)
	}
}

func TestServerWriteReal(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// REAL 写：数据项 ts=REAL(0x07)，长度以字节计
	val := make([]byte, 4)
	binary.BigEndian.PutUint32(val, math.Float32bits(3.25))
	realItem := append([]byte{TSResReal}, val...)
	rcs := c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSReal, Length: 1}},
		[][]byte{realItem})
	if len(rcs) != 1 || rcs[0] != ResOK {
		t.Fatalf("real write rc = % X, want FF", rcs)
	}
	got := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSReal, Length: 1})[0]
	if f := math.Float32frombits(binary.BigEndian.Uint32(got)); f != 3.25 {
		t.Errorf("real read back = %v, want 3.25", f)
	}
}

func TestServerReadErrorsOverWire(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	cases := []struct {
		name string
		item *item
		rc   byte
	}{
		{"missing db", &item{Area: AreaDB, DBNum: 99, Offset: 0, TS: TSByte, Length: 4}, ResObjectNotFound},
		{"unsupported area", &item{Area: AreaMK, DBNum: 0, Offset: 0, TS: TSByte, Length: 4}, ResObjectNotFound},
		{"out of range", &item{Area: AreaDB, DBNum: 1, Offset: 200, TS: TSByte, Length: 4}, ResObjectNotAvailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c.writeFrame(makeReadJob(c.nextRef(), tc.item))
			resp, err := c.readFrame()
			if err != nil {
				t.Fatalf("read frame: %v", err)
			}
			f, err := ParseFrame(resp)
			if err != nil {
				t.Fatalf("parse frame: %v", err)
			}
			h, _ := ParsePDUHeader(f.PDU)
			data := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
			if len(data) < 1 || data[0] != tc.rc {
				t.Errorf("rc = % X, want 0x%02X", data, tc.rc)
			}
		})
	}
}

func TestServerWriteErrorsOverWire(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// 写不存在的 DB
	rcs := c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 99, Offset: 0, TS: TSByte, Length: 4}},
		[][]byte{{TSResByte, 1, 2, 3, 4}})
	if len(rcs) != 1 || rcs[0] != ResObjectNotFound {
		t.Errorf("missing DB rc = % X, want 0A", rcs)
	}
	// 写越界
	rcs = c.writeVar(
		[]*item{{Area: AreaDB, DBNum: 1, Offset: 126, TS: TSByte, Length: 4}},
		[][]byte{{TSResByte, 1, 2, 3, 4}})
	if len(rcs) != 1 || rcs[0] != ResObjectNotAvailable {
		t.Errorf("out of range rc = % X, want 05", rcs)
	}
	// 写不支持的区域
	rcs = c.writeVar(
		[]*item{{Area: AreaMK, DBNum: 0, Offset: 0, TS: TSByte, Length: 1}},
		[][]byte{{TSResByte, 0xAA}})
	if len(rcs) != 1 || rcs[0] != ResObjectNotFound {
		t.Errorf("unsupported area rc = % X, want 0A", rcs)
	}
}

// TestServerMultiItemReadFill 验证多 item 读响应中的奇数长度填充字节
// （plc4x S7VarPayloadDataItem 的对齐规则）。
func TestServerMultiItemReadFill(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	c.writeFrame(makeReadJob(c.nextRef(),
		&item{Area: AreaDB, DBNum: 1, Offset: 0, TS: TSByte, Length: 4},
		&item{Area: AreaDB, DBNum: 1, Offset: 8, TS: TSByte, Length: 3},
		&item{Area: AreaDB, DBNum: 1, Offset: 12, TS: TSByte, Length: 2}))
	resp, err := c.readFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	f, err := ParseFrame(resp)
	if err != nil {
		t.Fatalf("parse frame: %v", err)
	}
	h, _ := ParsePDUHeader(f.PDU)
	data := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
	want := []byte{
		ResOK, TSResByte, 0x00, 0x20, 0x41, 0x48, 0x00, 0x00, // REAL 12.5
		ResOK, TSResByte, 0x00, 0x18, 0x01, 0xFF, 0x00, 0x00, // 3 字节 + 填充
		ResOK, TSResByte, 0x00, 0x10, 0x00, 0x00, // 2 字节
	}
	if !bytes.Equal(data, want) {
		t.Errorf("data =\n% X\nwant\n% X", data, want)
	}
}

func TestServerMultiItemWrite(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// 两个 item 的写请求，数据区含奇数长度项与填充字节
	rcs := c.writeVar(
		[]*item{
			{Area: AreaDB, DBNum: 1, Offset: 40, TS: TSByte, Length: 4},
			{Area: AreaDB, DBNum: 2, Offset: 10, TS: TSByte, Length: 3},
		},
		[][]byte{
			{TSResByte, 0xDE, 0xAD, 0xBE, 0xEF},
			{TSResByte, 0x11, 0x22, 0x33},
		})
	if len(rcs) != 2 || rcs[0] != ResOK || rcs[1] != ResOK {
		t.Fatalf("rcs = % X, want FF FF", rcs)
	}
	got := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: 40, TS: TSByte, Length: 4})[0]
	if !bytes.Equal(got, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("DB1 = % X, want DE AD BE EF", got)
	}
	got = c.readVar(&item{Area: AreaDB, DBNum: 2, Offset: 10, TS: TSByte, Length: 3})[0]
	if !bytes.Equal(got, []byte{0x11, 0x22, 0x33}) {
		t.Errorf("DB2 = % X, want 11 22 33", got)
	}
}

func TestServerSZLIdentification(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// plc4x identifyRemote 优先探测 0x001C（组件标识）
	szl := c.readSZL(SzlComponentIdentification, 0x0001)
	if len(szl) < 8 {
		t.Fatalf("SZL 0x001C data too short: % X", szl)
	}
	if binary.BigEndian.Uint16(szl[0:2]) != SzlComponentIdentification {
		t.Errorf("szlId = % X, want % X", szl[0:2], SzlComponentIdentification)
	}
	// plc4x 旧版按 const 0x1C(28) 校验 szlItemLength
	if binary.BigEndian.Uint16(szl[4:6]) != 28 {
		t.Errorf("szlItemLength = %d, want 28", binary.BigEndian.Uint16(szl[4:6]))
	}
	if binary.BigEndian.Uint16(szl[6:8]) != 1 {
		t.Errorf("szlItemCount = %d, want 1", binary.BigEndian.Uint16(szl[6:8]))
	}
	// plc4x findCpuModelName 要求记录中含 "CPU " 前缀的型号名
	if !bytes.Contains(szl, []byte("CPU 315-2 PN/DP")) {
		t.Errorf("SZL 0x001C missing model name: % X", szl)
	}
	// 回退探测 0x0011（模块标识）：应含 "6ES7" 订货号
	szl = c.readSZL(SzlModuleIdentification, 0x0001)
	if !bytes.Contains(szl, []byte("6ES7315-2EH14-0AB0")) {
		t.Errorf("SZL 0x0011 missing order code: % X", szl)
	}
	// 未知 SZL：返回空列表（szlItemCount=0）
	szl = c.readSZL(0x9999, 0x0000)
	if binary.BigEndian.Uint16(szl[6:8]) != 0 {
		t.Errorf("unknown SZL szlItemCount = %d, want 0", binary.BigEndian.Uint16(szl[6:8]))
	}
}

func TestServerConcurrentClients(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()

	const n = 8
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(seed byte) {
			defer func() { done <- struct{}{} }()
			c := dial(t, addr)
			defer c.close()
			// 每个客户端使用独立的 4 字节区域（64 + seed*4），避免相互覆盖
			off := 64 + int(seed)*4
			for j := 0; j < 20; j++ {
				// 写
				rcs := c.writeVar(
					[]*item{{Area: AreaDB, DBNum: 1, Offset: off, TS: TSByte, Length: 4}},
					[][]byte{{TSResByte, seed, byte(j), 0x00, 0x00}})
				if rcs[0] != ResOK {
					t.Errorf("write rc = % X, want FF", rcs)
					return
				}
				// 读回验证
				got := c.readVar(&item{Area: AreaDB, DBNum: 1, Offset: off, TS: TSByte, Length: 2})[0]
				if !bytes.Equal(got, []byte{seed, byte(j)}) {
					t.Errorf("read back = % X, want % X", got, []byte{seed, byte(j)})
					return
				}
			}
		}(byte(i + 1))
	}
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("timeout waiting for concurrent clients")
		}
	}
}

func TestServerDisconnect(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// COTP Disconnect Request：TPKT(4) + LI(1) + 0x80 + dstRef(2) + srcRef(2)
	dr := []byte{0x03, 0x00, 0x00, 0x0A, 0x05, COTPDR, 0x00, 0x01, 0x00, 0x01}
	if _, err := c.conn.Write(dr); err != nil {
		t.Fatalf("write DR: %v", err)
	}
	dc, err := c.readFrame()
	if err != nil {
		t.Fatalf("read DC: %v", err)
	}
	if len(dc) < 6 || dc[5] != COTPDC {
		t.Errorf("bad DC: % X", dc)
	}
	// 连接应被服务端关闭
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.conn.Read(make([]byte, 1)); err == nil {
		t.Error("connection still open after DR, want closed")
	}
}

func TestServerDBLengthsFromConfig(t *testing.T) {
	addr, stop := startTestServer(t, testConfig())
	defer stop()
	c := dial(t, addr)
	defer c.close()

	// 各 DB 的配置长度边界：DB1=128、DB2=256、DB20=256
	bounds := []struct {
		db    int
		size  int
		valid bool
	}{
		{1, 128, true}, {1, 129, false},
		{2, 256, true}, {2, 257, false},
		{20, 256, true}, {20, 257, false},
	}
	for _, b := range bounds {
		it := &item{Area: AreaDB, DBNum: b.db, Offset: b.size - 4, TS: TSByte, Length: 4}
		c.writeFrame(makeReadJob(c.nextRef(), it))
		resp, err := c.readFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		f, _ := ParseFrame(resp)
		h, _ := ParsePDUHeader(f.PDU)
		data := f.PDU[12+h.ParamLen : 12+h.ParamLen+h.DataLen]
		ok := len(data) >= 4 && data[0] == ResOK
		if ok != b.valid {
			t.Errorf("DB%d read @%d: ok=%v, want %v (data=% X)", b.db, b.size, ok, b.valid, data)
		}
	}
}
