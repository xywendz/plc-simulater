package model

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// Field 描述数据块中的一个具名字段。
type Field struct {
	Name   string
	Offset int
	Type   string
	Bit    int
	Length int
	Value  any
}

// DB 表示一个位于内存中的数据块，线程安全。
type DB struct {
	Number int
	Name   string
	mu     sync.RWMutex
	size   int
	data   []byte
	fields map[string]Field
}

// NewDB 创建数据块。size 会被扩展以容纳所有字段。
func NewDB(number, size int, name string, fields []Field) (*DB, error) {
	if number <= 0 || number > 0xFFFF {
		return nil, fmt.Errorf("invalid DB number %d", number)
	}
	db := &DB{
		Number: number,
		Name:   name,
		fields: make(map[string]Field, len(fields)),
	}
	if size < 0 {
		size = 0
	}
	for _, f := range fields {
		n := elementSize(f.Type, f.Length)
		if n <= 0 {
			return nil, fmt.Errorf("DB%d field %q: unsupported or incomplete type %q", number, f.Name, f.Type)
		}
		if f.Offset < 0 || f.Offset+n > size {
			size = f.Offset + n
		}
		if f.Name != "" {
			db.fields[f.Name] = f
		}
	}
	if size == 0 {
		return nil, fmt.Errorf("DB%d: size must be > 0", number)
	}
	db.size = size
	db.data = make([]byte, size)
	for _, f := range fields {
		if f.Value == nil {
			continue
		}
		if err := writeValue(db.data, f); err != nil {
			return nil, fmt.Errorf("DB%d field %q: %w", number, f.Name, err)
		}
	}
	return db, nil
}

// Size 返回数据块字节数。
func (d *DB) Size() int { return d.size }

// Fields 返回按名称索引的字段表。
func (d *DB) Fields() map[string]Field { return d.fields }

// Read 读取一段原始字节。
func (d *DB) Read(offset, length int) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if offset < 0 || length < 0 || offset+length > d.size {
		return nil, fmt.Errorf("DB%d: read out of range offset=%d len=%d size=%d", d.Number, offset, length, d.size)
	}
	out := make([]byte, length)
	copy(out, d.data[offset:offset+length])
	return out, nil
}

// Write 写入一段原始字节。
func (d *DB) Write(offset int, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if offset < 0 || offset+len(data) > d.size {
		return fmt.Errorf("DB%d: write out of range offset=%d len=%d size=%d", d.Number, offset, len(data), d.size)
	}
	copy(d.data[offset:], data)
	return nil
}

// GetBit 读取指定位。
func (d *DB) GetBit(offset, bit int) (bool, error) {
	if bit < 0 || bit > 7 {
		return false, fmt.Errorf("DB%d: invalid bit %d", d.Number, bit)
	}
	b, err := d.Read(offset, 1)
	if err != nil {
		return false, err
	}
	return b[0]&(1<<uint(bit)) != 0, nil
}

// SetBit 写入指定位。
func (d *DB) SetBit(offset, bit int, on bool) error {
	if bit < 0 || bit > 7 {
		return fmt.Errorf("DB%d: invalid bit %d", d.Number, bit)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if offset < 0 || offset >= d.size {
		return fmt.Errorf("DB%d: write out of range offset=%d size=%d", d.Number, offset, d.size)
	}
	if on {
		d.data[offset] |= 1 << uint(bit)
	} else {
		d.data[offset] &^= 1 << uint(bit)
	}
	return nil
}

// FieldValue 读取字段当前值并按其类型解码为 Go 值。
func (d *DB) FieldValue(f Field) (any, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if f.Offset < 0 || f.Offset >= len(d.data) {
		return nil, fmt.Errorf("DB%d: field %q offset out of range", d.Number, f.Name)
	}
	return decodeField(d.data, f)
}

// SetField 将字段写入给定值，覆盖该字段类型所占的字节区间。
func (d *DB) SetField(f Field, value any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := elementSize(f.Type, f.Length)
	if n <= 0 || f.Offset < 0 || f.Offset+n > len(d.data) {
		return fmt.Errorf("DB%d: field %q out of range", d.Number, f.Name)
	}
	fv := f
	fv.Value = value
	return writeValue(d.data, fv)
}

// SetFields 用给定字段集合替换具名字段表（仅更新元数据，不改写字节值）。
// 若字段偏移超出当前大小，则扩展底层缓冲区以容纳，并保留已有数据。
func (d *DB) SetFields(fields []Field) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	maxEnd := 0
	for _, f := range fields {
		n := elementSize(f.Type, f.Length)
		if n <= 0 {
			return fmt.Errorf("DB%d: unsupported type %q", d.Number, f.Type)
		}
		if f.Offset < 0 {
			return fmt.Errorf("DB%d: invalid offset %d", d.Number, f.Offset)
		}
		if end := f.Offset + n; end > maxEnd {
			maxEnd = end
		}
	}
	if maxEnd > d.size {
		grown := make([]byte, maxEnd)
		copy(grown, d.data)
		d.data = grown
		d.size = maxEnd
	}
	fs := make(map[string]Field, len(fields))
	for _, f := range fields {
		if f.Name != "" {
			fs[f.Name] = f
		}
	}
	d.fields = fs
	return nil
}

// AddField 新增或更新一个具名字段，并按初始值写入字节（若提供了 value）。
// 字段偏移超出当前大小时会自动扩展缓冲区。
func (d *DB) AddField(f Field) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := elementSize(f.Type, f.Length)
	if n <= 0 {
		return fmt.Errorf("DB%d: unsupported type %q", d.Number, f.Type)
	}
	if f.Offset < 0 {
		return fmt.Errorf("DB%d: invalid offset %d", d.Number, f.Offset)
	}
	if end := f.Offset + n; end > len(d.data) {
		grown := make([]byte, end)
		copy(grown, d.data)
		d.data = grown
		d.size = end
	}
	if f.Name != "" {
		d.fields[f.Name] = f
	}
	if f.Value != nil {
		fv := f
		return writeValue(d.data, fv)
	}
	return nil
}

// RemoveField 删除具名字段（仅移除元数据，不影响底层字节）。
func (d *DB) RemoveField(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.fields, name)
}

// decodeField 从缓冲区按字段类型解码出当前值。
func decodeField(buf []byte, f Field) (any, error) {
	switch strings.ToLower(f.Type) {
	case "bool", "bit":
		if f.Offset >= len(buf) {
			return nil, fmt.Errorf("offset out of range")
		}
		return buf[f.Offset]&(1<<uint(f.Bit)) != 0, nil
	case "byte":
		return buf[f.Offset], nil
	case "word":
		return binary.BigEndian.Uint16(buf[f.Offset:]), nil
	case "dword":
		return binary.BigEndian.Uint32(buf[f.Offset:]), nil
	case "int":
		return int16(binary.BigEndian.Uint16(buf[f.Offset:])), nil
	case "dint":
		return int32(binary.BigEndian.Uint32(buf[f.Offset:])), nil
	case "real":
		return math.Float32frombits(binary.BigEndian.Uint32(buf[f.Offset:])), nil
	case "string":
		end := f.Offset + f.Length
		if end > len(buf) {
			end = len(buf)
		}
		s := buf[f.Offset:end]
		// 去掉结尾的 0x00 填充
		for len(s) > 0 && s[len(s)-1] == 0 {
			s = s[:len(s)-1]
		}
		return string(s), nil
	}
	return nil, fmt.Errorf("unsupported type %q", f.Type)
}

// elementSize 返回字段占用的字节数。
func elementSize(typ string, length int) int {
	switch strings.ToLower(typ) {
	case "bool", "bit", "byte":
		return 1
	case "word", "int":
		return 2
	case "dword", "dint", "real":
		return 4
	case "string":
		return length
	}
	return -1
}

// writeValue 将字段初始值写入缓冲区。
func writeValue(buf []byte, f Field) error {
	switch strings.ToLower(f.Type) {
	case "bool", "bit":
		v, ok := toBool(f.Value)
		if !ok {
			return fmt.Errorf("invalid bool value %v", f.Value)
		}
		if v {
			buf[f.Offset] |= 1 << uint(f.Bit)
		} else {
			buf[f.Offset] &^= 1 << uint(f.Bit)
		}
	case "byte":
		buf[f.Offset] = byte(toInt(f.Value))
	case "word":
		binary.BigEndian.PutUint16(buf[f.Offset:], uint16(toInt(f.Value)))
	case "dword":
		binary.BigEndian.PutUint32(buf[f.Offset:], uint32(toInt(f.Value)))
	case "int":
		binary.BigEndian.PutUint16(buf[f.Offset:], uint16(int16(toInt(f.Value))))
	case "dint":
		binary.BigEndian.PutUint32(buf[f.Offset:], uint32(int32(toInt(f.Value))))
	case "real":
		binary.BigEndian.PutUint32(buf[f.Offset:], math.Float32bits(float32(toFloat(f.Value))))
	case "string":
		s := fmt.Sprint(f.Value)
		n := f.Length
		if n == 0 {
			n = len(s)
		}
		if f.Offset+n > len(buf) {
			return fmt.Errorf("string too long for buffer")
		}
		for i := 0; i < n; i++ {
			if i < len(s) {
				buf[f.Offset+i] = s[i]
			} else {
				buf[f.Offset+i] = 0
			}
		}
	default:
		return fmt.Errorf("unsupported type %q", f.Type)
	}
	return nil
}

func toBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "on", "yes":
			return true, true
		case "false", "0", "off", "no":
			return false, true
		}
	}
	return false, false
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		// 支持十进制与 0x 前缀的十六进制（YAML 中带引号的数值）
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 0, 64); err == nil {
			return n
		}
		var n int64
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
		var f float64
		fmt.Sscanf(x, "%g", &f)
		return f
	}
	return 0
}
