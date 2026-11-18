package bridge

import (
	"encoding/hex"
	"fmt"
	"sort"

	"plc-simulater/store"
)

// Info 返回应用元信息。
func (b *Bridge) Info() (AppInfo, error) {
	return AppInfo{
		Version: b.version,
		Mode:    "s7-simulator",
		Listen:  b.listen,
		MaxPDU:  b.maxPDU,
	}, nil
}

// ListDBs 返回全部数据块视图（按 DB 号升序）。
func (b *Bridge) ListDBs() ([]DBView, error) {
	dbs := b.srv.DBs()
	out := make([]DBView, 0, len(dbs))
	for _, db := range dbs {
		out = append(out, buildDBView(db))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// GetDB 返回单个数据块视图。
func (b *Bridge) GetDB(number int) (DBView, error) {
	db := b.srv.DB(number)
	if db == nil {
		return DBView{}, fmt.Errorf("DB%d 不存在", number)
	}
	return buildDBView(db), nil
}

// ListFields 返回指定 DB 持久化的字段定义列表。
func (b *Bridge) ListFields(number int) ([]FieldMeta, error) {
	if b.srv.DB(number) == nil {
		return nil, fmt.Errorf("DB%d 不存在", number)
	}
	stored, err := b.srv.Fields(number)
	if err != nil {
		return nil, err
	}
	out := make([]FieldMeta, 0, len(stored))
	for _, m := range stored {
		out = append(out, FieldMeta{
			Name: m.Name, Offset: m.Offset, Type: m.Type,
			Bit: m.Bit, Length: m.Length, Ord: m.Ord, Value: m.Value,
		})
	}
	return out, nil
}

// CreateDB 创建数据块。
func (b *Bridge) CreateDB(number, size int, name string) error {
	return b.srv.CreateDB(number, size, name)
}

// CloneDB 复制数据块（dst 为目标编号，size<=0 时沿用源大小）。
func (b *Bridge) CloneDB(src, dst int, name string, size int) error {
	return b.srv.CloneDB(src, dst, name, size)
}

// RenameDB 重命名数据块。
func (b *Bridge) RenameDB(number int, name string) error {
	return b.srv.RenameDB(number, name)
}

// DeleteDB 删除数据块。
func (b *Bridge) DeleteDB(number int) error {
	return b.srv.DeleteDB(number)
}

// ReloadDB 从 SQLite 重新加载单个数据块。
func (b *Bridge) ReloadDB(number int) error {
	return b.srv.ReloadDB(number)
}

// ReloadAll 从 SQLite 重新加载全部数据块。
func (b *Bridge) ReloadAll() error {
	return b.srv.ReloadAll()
}

// SetBit 修改指定字节的某一位（持久化到 SQLite）。
func (b *Bridge) SetBit(number, offset, bit int, value bool) error {
	return b.srv.SetBitValue(number, offset, bit, value)
}

// SetField 修改具名字段的值（持久化到 SQLite）。
func (b *Bridge) SetField(number int, name string, value any) error {
	return b.srv.SetFieldValue(number, name, value)
}

// UpsertField 新增或更新字段定义；偏移与位号由系统自动重算。
func (b *Bridge) UpsertField(number int, field FieldMeta) error {
	return b.srv.UpsertField(number, store.FieldMeta{
		Name:   field.Name,
		Offset: field.Offset,
		Type:   field.Type,
		Bit:    field.Bit,
		Length: field.Length,
		Ord:    field.Ord,
		Value:  field.Value,
	})
}

// DeleteField 删除字段定义。
func (b *Bridge) DeleteField(number int, name string) error {
	return b.srv.DeleteField(number, name)
}

// MoveField 调整字段顺序（dir=-1 上移，+1 下移）。
func (b *Bridge) MoveField(number int, name string, dir int) error {
	return b.srv.MoveField(number, name, dir)
}

// RawQuery 查询指定数据块的原始字节（十六进制）。
func (b *Bridge) RawQuery(number, offset, length int) (RawResult, error) {
	db := b.srv.DB(number)
	if db == nil {
		return RawResult{}, fmt.Errorf("DB%d 不存在", number)
	}
	if length <= 0 || length > 1024 {
		length = 16
	}
	if offset < 0 || offset+length > db.Size() {
		return RawResult{}, fmt.Errorf("查询范围超出 DB%d（大小 %d 字节）", number, db.Size())
	}
	buf, err := db.Read(offset, length)
	if err != nil {
		return RawResult{}, err
	}
	return RawResult{DB: number, Offset: offset, Hex: hex.EncodeToString(buf)}, nil
}
