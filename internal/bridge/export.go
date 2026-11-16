package bridge

import (
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"plc-simulater/model"
	"plc-simulater/store"
)

// ExportedField 是导出文件中的字段定义与当前值。
// Offset/Bit 仅作参考信息，导入时由系统按字段顺序与类型重新计算。
type ExportedField struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Offset int    `json:"offset,omitempty"`
	Bit    int    `json:"bit,omitempty"`
	Length int    `json:"length,omitempty"`
	Value  any    `json:"value,omitempty"`
}

// ExportedDB 是导出文件中的单个数据块（定义 + 当前字节内容）。
type ExportedDB struct {
	Number int             `json:"number"`
	Name   string          `json:"name,omitempty"`
	Size   int             `json:"size"`
	Fields []ExportedField `json:"fields,omitempty"`
	Raw    string          `json:"raw,omitempty"` // 全块字节的十六进制表示
}

// ExportBundle 是导出文件的整体结构；单块导出时 DBs 仅含一项。
type ExportBundle struct {
	Version    string       `json:"version"`
	ExportedAt string       `json:"exportedAt,omitempty"`
	DBs        []ExportedDB `json:"dbs"`
}

// exportDBView 把运行中的数据块转为可导出结构。
// 字段按持久化定义的顺序（ord）输出，保证导入后按相同顺序重算偏移。
func (b *Bridge) exportDBView(db *model.DB) ExportedDB {
	e := ExportedDB{
		Number: db.Number,
		Name:   db.Name,
		Size:   db.Size(),
		Fields: []ExportedField{},
	}

	stored, err := b.srv.Fields(db.Number)
	if err == nil {
		runtime := db.Fields()
		for _, m := range stored {
			f := ExportedField{Name: m.Name, Type: m.Type, Length: m.Length}
			if rf, ok := runtime[m.Name]; ok {
				f.Offset = rf.Offset
				f.Bit = rf.Bit
				if v, err := db.FieldValue(rf); err == nil {
					f.Value = v
				}
			} else {
				f.Offset = m.Offset
				f.Bit = m.Bit
			}
			e.Fields = append(e.Fields, f)
		}
	}

	if raw, err := db.Read(0, db.Size()); err == nil {
		e.Raw = hex.EncodeToString(raw)
	}
	return e
}

// ExportDB 导出单个数据块。
func (b *Bridge) ExportDB(number int) (ExportBundle, error) {
	db := b.srv.DB(number)
	if db == nil {
		return ExportBundle{}, fmt.Errorf("DB%d 不存在", number)
	}
	return ExportBundle{
		Version:    b.version,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		DBs:        []ExportedDB{b.exportDBView(db)},
	}, nil
}

// ExportAll 导出全部数据块（按 DB 号升序）。
func (b *Bridge) ExportAll() (ExportBundle, error) {
	m := b.srv.DBs()
	dbs := make([]*model.DB, 0, len(m))
	for _, db := range m {
		dbs = append(dbs, db)
	}
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Number < dbs[j].Number })
	out := make([]ExportedDB, 0, len(dbs))
	for _, db := range dbs {
		out = append(out, b.exportDBView(db))
	}
	return ExportBundle{
		Version:    b.version,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		DBs:        out,
	}, nil
}

// ImportDB 导入（恢复）单个数据块：同号数据块已存在时先删除再重建，
// 随后按导出顺序恢复字段定义，并在大小一致时按原始字节还原当前值。
func (b *Bridge) ImportDB(e ExportedDB) error {
	if e.Number <= 0 || e.Number > 0xFFFF {
		return fmt.Errorf("无效的 DB 号: %d", e.Number)
	}
	if e.Size <= 0 {
		return fmt.Errorf("DB%d 缺少有效的大小", e.Number)
	}
	if b.srv.DB(e.Number) != nil {
		if err := b.srv.DeleteDB(e.Number); err != nil {
			return fmt.Errorf("DB%d 已存在且无法替换: %w", e.Number, err)
		}
	}
	if err := b.srv.CreateDB(e.Number, e.Size, e.Name); err != nil {
		return err
	}
	for i, f := range e.Fields {
		if f.Name == "" || f.Type == "" {
			continue
		}
		if err := b.srv.UpsertField(e.Number, store.FieldMeta{
			Name:   f.Name,
			Type:   f.Type,
			Length: f.Length,
			Ord:    i + 1,
			Value:  f.Value,
		}); err != nil {
			return fmt.Errorf("DB%d 字段 %s 恢复失败: %w", e.Number, f.Name, err)
		}
	}
	if e.Raw != "" {
		raw, err := hex.DecodeString(e.Raw)
		if err != nil {
			return fmt.Errorf("DB%d 原始字节非法: %w", e.Number, err)
		}
		db := b.srv.DB(e.Number)
		if db != nil && len(raw) == db.Size() {
			if err := db.Write(0, raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// ImportAll 批量导入；返回成功导入的块数，任一块失败即中止并返回错误。
func (b *Bridge) ImportAll(bundle ExportBundle) (int, error) {
	imported := 0
	for _, e := range bundle.DBs {
		if err := b.ImportDB(e); err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}
