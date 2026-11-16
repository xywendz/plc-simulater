package model

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
)

func TestNewDB(t *testing.T) {
	fields := []Field{
		{Name: "TankLevel", Offset: 0, Type: "real", Value: 12.5},
		{Name: "PumpRunning", Offset: 4, Type: "bool", Bit: 0, Value: true},
		{Name: "Fault", Offset: 4, Type: "bool", Bit: 1, Value: false},
		{Name: "StatusWord", Offset: 8, Type: "word", Value: 0x01FF},
		{Name: "Counter", Offset: 12, Type: "dword", Value: 1000},
		{Name: "Temperature", Offset: 16, Type: "int", Value: -25},
		{Name: "ProductName", Offset: 32, Type: "string", Length: 20, Value: "Hello S7"},
	}
	db, err := NewDB(1, 128, "", fields)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	if db.Number != 1 {
		t.Errorf("Number = %d, want 1", db.Number)
	}
	if db.Size() != 128 {
		t.Errorf("Size = %d, want 128", db.Size())
	}
	if len(db.Fields()) != 7 {
		t.Errorf("len(Fields) = %d, want 7", len(db.Fields()))
	}

	// REAL @0
	buf, err := db.Read(0, 4)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := math.Float32frombits(binary.BigEndian.Uint32(buf)); got != 12.5 {
		t.Errorf("TankLevel = %v, want 12.5", got)
	}
	// BOOL @4.0 / @4.1
	if on, err := db.GetBit(4, 0); err != nil || !on {
		t.Errorf("PumpRunning = %v, %v; want true", on, err)
	}
	if on, err := db.GetBit(4, 1); err != nil || on {
		t.Errorf("Fault = %v, %v; want false", on, err)
	}
	// WORD @8
	buf, _ = db.Read(8, 2)
	if got := binary.BigEndian.Uint16(buf); got != 0x01FF {
		t.Errorf("StatusWord = 0x%04X, want 0x01FF", got)
	}
	// DWORD @12
	buf, _ = db.Read(12, 4)
	if got := binary.BigEndian.Uint32(buf); got != 1000 {
		t.Errorf("Counter = %d, want 1000", got)
	}
	// INT @16
	buf, _ = db.Read(16, 2)
	if got := int16(binary.BigEndian.Uint16(buf)); got != -25 {
		t.Errorf("Temperature = %d, want -25", got)
	}
	// STRING @32
	buf, _ = db.Read(32, 8)
	if string(buf) != "Hello S7" {
		t.Errorf("ProductName = %q, want %q", string(buf), "Hello S7")
	}
	// 字符串尾部补零
	buf, _ = db.Read(41, 11)
	for i, b := range buf {
		if b != 0 {
			t.Errorf("ProductName tail[%d] = 0x%02X, want 0", i, b)
		}
	}
}

func TestNewDBSizeFromFields(t *testing.T) {
	// 省略 size 时按字段自动扩展
	db, err := NewDB(5, 0, "", []Field{
		{Name: "A", Offset: 0, Type: "real", Value: 1.0},
		{Name: "B", Offset: 4, Type: "word", Value: 7},
	})
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	if db.Size() != 6 {
		t.Errorf("Size = %d, want 6", db.Size())
	}
	// 字段超出配置 size 时同样扩展
	db, err = NewDB(6, 4, "", []Field{
		{Name: "S", Offset: 0, Type: "string", Length: 10, Value: "abcdefghij"},
	})
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	if db.Size() != 10 {
		t.Errorf("Size = %d, want 10", db.Size())
	}
	buf, _ := db.Read(0, 10)
	if string(buf) != "abcdefghij" {
		t.Errorf("string = %q, want %q", string(buf), "abcdefghij")
	}
}

func TestNewDBErrors(t *testing.T) {
	cases := []struct {
		name   string
		number int
		size   int
		fields []Field
	}{
		{"bad number", 0, 16, nil},
		{"number too large", 0x10000, 16, nil},
		{"zero size no fields", 1, 0, nil},
		{"unsupported type", 1, 16, []Field{{Name: "X", Offset: 0, Type: "bogus"}}},
		{"string without length", 1, 16, []Field{{Name: "X", Offset: 0, Type: "string"}}},
		{"bad bool value", 1, 16, []Field{{Name: "X", Offset: 0, Type: "bool", Value: "maybe"}}},
		{"negative offset", 1, 16, []Field{{Name: "X", Offset: -1, Type: "byte"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDB(tc.number, tc.size, "", tc.fields); err == nil {
				t.Errorf("NewDB(%s) = nil error, want error", tc.name)
			}
		})
	}
}

func TestDBReadWrite(t *testing.T) {
	db, err := NewDB(1, 16, "", nil)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	if err := db.Write(4, []byte{0xDE, 0xAD, 0xBE, 0xEF}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf, err := db.Read(4, 4)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	for i, b := range buf {
		if b != want[i] {
			t.Errorf("buf[%d] = 0x%02X, want 0x%02X", i, b, want[i])
		}
	}
}

func TestDBReadBounds(t *testing.T) {
	db, _ := NewDB(1, 16, "", nil)
	cases := []struct {
		name   string
		offset int
		length int
	}{
		{"negative offset", -1, 4},
		{"negative length", 0, -4},
		{"offset at size", 16, 1},
		{"overflow", 12, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Read(tc.offset, tc.length); err == nil {
				t.Errorf("Read(%d, %d) = nil error, want error", tc.offset, tc.length)
			}
		})
	}
	// 边界合法读
	if _, err := db.Read(12, 4); err != nil {
		t.Errorf("Read(12, 4) = %v, want nil", err)
	}
	if _, err := db.Read(0, 0); err != nil {
		t.Errorf("Read(0, 0) = %v, want nil", err)
	}
}

func TestDBWriteBounds(t *testing.T) {
	db, _ := NewDB(1, 16, "", nil)
	cases := []struct {
		name   string
		offset int
		data   []byte
	}{
		{"negative offset", -1, []byte{1}},
		{"offset at size", 16, []byte{1}},
		{"overflow", 12, []byte{1, 2, 3, 4, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Write(tc.offset, tc.data); err == nil {
				t.Errorf("Write(%d, %v) = nil error, want error", tc.offset, tc.data)
			}
		})
	}
}

func TestDBBits(t *testing.T) {
	db, _ := NewDB(1, 8, "", nil)
	for bit := 0; bit < 8; bit++ {
		if err := db.SetBit(0, bit, true); err != nil {
			t.Fatalf("SetBit(%d): %v", bit, err)
		}
		on, err := db.GetBit(0, bit)
		if err != nil || !on {
			t.Errorf("GetBit(0, %d) = %v, %v; want true", bit, on, err)
		}
	}
	buf, _ := db.Read(0, 1)
	if buf[0] != 0xFF {
		t.Errorf("byte = 0x%02X, want 0xFF", buf[0])
	}
	// 清除单个位不影响其他位
	if err := db.SetBit(0, 3, false); err != nil {
		t.Fatalf("SetBit: %v", err)
	}
	if on, _ := db.GetBit(0, 3); on {
		t.Error("GetBit(0, 3) = true, want false")
	}
	if on, _ := db.GetBit(0, 7); !on {
		t.Error("GetBit(0, 7) = false, want true")
	}
	buf, _ = db.Read(0, 1)
	if buf[0] != 0xF7 {
		t.Errorf("byte = 0x%02X, want 0xF7", buf[0])
	}
	// 非法位号
	if err := db.SetBit(0, 8, true); err == nil {
		t.Error("SetBit(0, 8) = nil error, want error")
	}
	if _, err := db.GetBit(0, -1); err == nil {
		t.Error("GetBit(0, -1) = nil error, want error")
	}
	// 位越界
	if err := db.SetBit(8, 0, true); err == nil {
		t.Error("SetBit(8, 0) = nil error, want error")
	}
}

func TestDBReadIsolation(t *testing.T) {
	// Read 返回的切片必须是副本，修改不应影响 DB
	db, _ := NewDB(1, 8, "", nil)
	_ = db.Write(0, []byte{1, 2, 3, 4})
	buf, _ := db.Read(0, 4)
	buf[0] = 0xFF
	again, _ := db.Read(0, 1)
	if again[0] != 1 {
		t.Errorf("DB data mutated: got 0x%02X, want 0x01", again[0])
	}
}

func TestDBConcurrent(t *testing.T) {
	db, _ := NewDB(1, 64, "", nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = db.Write(int(seed%60), []byte{seed})
				_, _ = db.Read(0, 8)
				_ = db.SetBit(1, int(seed%8), seed%2 == 0)
				_, _ = db.GetBit(1, int(seed%8))
			}
		}(byte(i))
	}
	wg.Wait()
}

func TestElementSize(t *testing.T) {
	cases := []struct {
		typ    string
		length int
		want   int
	}{
		{"bool", 0, 1}, {"bit", 0, 1}, {"byte", 0, 1},
		{"word", 0, 2}, {"int", 0, 2},
		{"dword", 0, 4}, {"dint", 0, 4}, {"real", 0, 4},
		{"string", 20, 20}, {"STRING", 5, 5},
	}
	for _, tc := range cases {
		if got := elementSize(tc.typ, tc.length); got != tc.want {
			t.Errorf("elementSize(%q, %d) = %d, want %d", tc.typ, tc.length, got, tc.want)
		}
	}
}

func TestFieldValuesFromYAMLSemantics(t *testing.T) {
	// YAML 解析后 value 为 any：字符串数字与字符串布尔都应可转换
	db, err := NewDB(1, 8, "", []Field{
		{Name: "W", Offset: 0, Type: "word", Value: "0x01FF"},
		{Name: "R", Offset: 4, Type: "real", Value: "1.5"},
	})
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	buf, _ := db.Read(0, 2)
	if got := binary.BigEndian.Uint16(buf); got != 0x01FF {
		t.Errorf("word = 0x%04X, want 0x01FF", got)
	}
	buf, _ = db.Read(4, 4)
	if got := math.Float32frombits(binary.BigEndian.Uint32(buf)); got != 1.5 {
		t.Errorf("real = %v, want 1.5", got)
	}
}
