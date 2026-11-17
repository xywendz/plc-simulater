package bridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

// UnmarshalJSON 在标准键名之外兼容常见的字段定义键名，避免导入外部文件时
// 因键名不匹配而丢失字段名称与类型。
func (f *ExportedField) UnmarshalJSON(data []byte) error {
	type plain ExportedField
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*f = ExportedField(p)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	firstString := func(keys ...string) string {
		for _, k := range keys {
			raw, ok := obj[k]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
		return ""
	}
	if f.Name == "" {
		f.Name = firstString("fieldName", "field_name", "field", "columnName", "column_name", "column")
	}
	if f.Type == "" {
		f.Type = firstString("dataType", "data_type", "fieldType", "field_type", "valueType", "value_type")
	}
	return nil
}

// ExportedDB 是导出文件中的单个数据块（定义 + 当前字节内容）。
type ExportedDB struct {
	Number int             `json:"number"`
	Name   string          `json:"name,omitempty"`
	Size   int             `json:"size"`
	Fields []ExportedField `json:"fields,omitempty"`
	Raw    string          `json:"raw,omitempty"` // 全块字节的十六进制表示
}

// UnmarshalJSON 在标准 fields 键之外兼容常见的字段列表键名。
func (e *ExportedDB) UnmarshalJSON(data []byte) error {
	type plain ExportedDB
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*e = ExportedDB(p)

	if len(e.Fields) > 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	for _, k := range []string{"columns", "Columns", "fieldDefinitions", "field_definitions"} {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		var fields []ExportedField
		if json.Unmarshal(raw, &fields) == nil && len(fields) > 0 {
			e.Fields = fields
			return nil
		}
	}
	return nil
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

// normalizeFieldType 规范化导入文件中的类型值：去除空白、统一小写，
// 并把常见别名映射到系统支持的类型名。
func normalizeFieldType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "boolean":
		return "bool"
	case "float", "float32", "single":
		return "real"
	case "int16":
		return "int"
	case "int32":
		return "dint"
	case "uint8":
		return "byte"
	case "uint16":
		return "word"
	case "uint32":
		return "dword"
	default:
		return t
	}
}

// supportedFieldType 判断类型是否为系统支持的字段类型。
func supportedFieldType(t string) bool {
	switch t {
	case "bool", "bit", "byte", "word", "int", "dword", "dint", "real", "string":
		return true
	}
	return false
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
	// 删除现有 DB 前先校验全部导入数据，避免校验失败时清空同号块。
	for i, f := range e.Fields {
		name := strings.TrimSpace(f.Name)
		typ := normalizeFieldType(f.Type)
		if name == "" || typ == "" {
			return fmt.Errorf("DB%d 第 %d 个字段缺少名称或类型，请检查导入文件中的 name/type 键", e.Number, i+1)
		}
		if !supportedFieldType(typ) {
			return fmt.Errorf("DB%d 第 %d 个字段类型 %q 不受支持", e.Number, i+1, typ)
		}
	}
	var raw []byte
	if e.Raw != "" {
		var err error
		raw, err = hex.DecodeString(e.Raw)
		if err != nil {
			return fmt.Errorf("DB%d 原始字节非法: %w", e.Number, err)
		}
	}
	if b.srv.DB(e.Number) != nil {
		if err := b.srv.DeleteDB(e.Number); err != nil {
			return fmt.Errorf("DB%d 已存在且无法替换: %w", e.Number, err)
		}
	}
	if err := b.srv.CreateDB(e.Number, e.Size, e.Name); err != nil {
		return err
	}

	// 导出包中的 raw 字节与字段 offset/bit 来自同一布局。若字段带有原始偏移
	// 且字节大小匹配，必须按原始偏移恢复字段元数据，否则重算的紧凑偏移会让
	// 字段读到错位字节，字符串类型表现为中文乱码。
	restoreRaw := len(raw) > 0 && len(raw) == e.Size
	hasLayout := false
	for _, f := range e.Fields {
		if f.Offset > 0 || f.Bit > 0 {
			hasLayout = true
			break
		}
	}
	if restoreRaw && hasLayout {
		metas := make([]store.FieldMeta, 0, len(e.Fields))
		for i, f := range e.Fields {
			metas = append(metas, store.FieldMeta{
				Name: strings.TrimSpace(f.Name),
				Type: normalizeFieldType(f.Type),
				Length: f.Length,
				Offset: f.Offset,
				Bit:    f.Bit,
				Ord:    i + 1,
				Value:  f.Value,
			})
		}
		if err := b.srv.RestoreFieldLayout(e.Number, metas); err != nil {
			return fmt.Errorf("DB%d 字段布局恢复失败: %w", e.Number, err)
		}
		db := b.srv.DB(e.Number)
		if db != nil {
			if err := db.Write(0, raw); err != nil {
				return err
			}
		}
		return nil
	}

	// 无原始布局信息（或字节大小不匹配）时，按类型重算偏移并写入字段值。
	for i, f := range e.Fields {
		name := strings.TrimSpace(f.Name)
		typ := normalizeFieldType(f.Type)
		if err := b.srv.UpsertField(e.Number, store.FieldMeta{
			Name:   name,
			Type:   typ,
			Length: f.Length,
			Ord:    i + 1,
			Value:  f.Value,
		}); err != nil {
			return fmt.Errorf("DB%d 字段 %s 恢复失败: %w", e.Number, name, err)
		}
	}
	if restoreRaw {
		db := b.srv.DB(e.Number)
		if db != nil {
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
