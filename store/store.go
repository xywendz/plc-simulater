// Package store 提供内置 SQLite 持久化，记录数据块（DB）及其具名字段定义
// （字段名 / 偏移量 / 类型 / 位号 / 长度 / 初始值）。
// 它作为字段配置的唯一真源：配置文件在首次运行时播种，之后可通过 HTTP 接口增改。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// DBMeta 描述一个数据块（仅元数据）。
type DBMeta struct {
	Number int    `json:"number"`
	Size   int    `json:"size"`
	Name   string `json:"name"`
}

// FieldMeta 描述数据块中的一个具名字段。
type FieldMeta struct {
	DBNumber int    `json:"db_number"`
	Name     string `json:"name"`
	Offset   int    `json:"offset"`
	Type     string `json:"type"`
	Bit      int    `json:"bit"`
	Length   int    `json:"length"`
	Ord      int    `json:"ord"` // 字段顺序（决定打包偏移）
	Value    any    `json:"value,omitempty"` // 初始值（可选）
}

// Store 是 SQLite 存储。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）SQLite 数据库并完成建表。
func Open(path string) (*Store, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// 单连接，避免并发写入竞争（本模拟器写入量很小）。
	sqldb.SetMaxOpenConns(1)
	s := &Store{db: sqldb}
	if err := s.init(); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭底层数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS dbs (
		number INTEGER PRIMARY KEY,
		size   INTEGER NOT NULL,
		name   TEXT    NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS fields (
		db_number INTEGER NOT NULL,
		name      TEXT    NOT NULL,
		offset    INTEGER NOT NULL,
		type      TEXT    NOT NULL,
		bit       INTEGER NOT NULL DEFAULT 0,
		length    INTEGER NOT NULL DEFAULT 0,
		ord       INTEGER NOT NULL DEFAULT 0,
		value     TEXT,
		PRIMARY KEY (db_number, name)
	);
	CREATE INDEX IF NOT EXISTS idx_fields_db ON fields(db_number);
	`)
	if err != nil {
		return err
	}
	// 兼容旧库：补齐新增列（已存在则忽略报错）
	_, _ = s.db.Exec(`ALTER TABLE fields ADD COLUMN ord INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE dbs ADD COLUMN name TEXT NOT NULL DEFAULT ''`)
	return nil
}

// UpsertDB 写入或更新数据块元数据。
func (s *Store) UpsertDB(m DBMeta) error {
	_, err := s.db.Exec(
		`INSERT INTO dbs(number, size, name) VALUES(?, ?, ?)
		 ON CONFLICT(number) DO UPDATE SET size=excluded.size, name=excluded.name`,
		m.Number, m.Size, m.Name)
	return err
}

// GetDBs 返回所有数据块元数据，按编号排序。
func (s *Store) GetDBs() ([]DBMeta, error) {
	rows, err := s.db.Query(`SELECT number, size, name FROM dbs ORDER BY number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DBMeta
	for rows.Next() {
		var m DBMeta
		if err := rows.Scan(&m.Number, &m.Size, &m.Name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetDB 返回单个数据块的元数据，不存在时返回 (nil, nil)。
func (s *Store) GetDB(number int) (*DBMeta, error) {
	row := s.db.QueryRow(`SELECT number, size, name FROM dbs WHERE number=?`, number)
	var m DBMeta
	err := row.Scan(&m.Number, &m.Size, &m.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteDB 删除数据块及其全部字段定义。
func (s *Store) DeleteDB(number int) error {
	if _, err := s.db.Exec(`DELETE FROM fields WHERE db_number=?`, number); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM dbs WHERE number=?`, number)
	return err
}

// UpsertField 写入或更新一个具名字段。
func (s *Store) UpsertField(f FieldMeta) error {
	if f.Name == "" {
		return fmt.Errorf("field name required")
	}
	if f.Type == "" {
		return fmt.Errorf("field type required")
	}
	v, _ := json.Marshal(f.Value)
	_, err := s.db.Exec(
		`INSERT INTO fields(db_number, name, offset, type, bit, length, ord, value)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(db_number, name) DO UPDATE SET
			offset=excluded.offset, type=excluded.type, bit=excluded.bit,
			length=excluded.length, ord=excluded.ord, value=excluded.value`,
		f.DBNumber, f.Name, f.Offset, strings.ToLower(f.Type), f.Bit, f.Length, f.Ord, string(v))
	return err
}

// GetFields 返回指定数据块的所有字段，按顺序（ord）、偏移、位号排序。
func (s *Store) GetFields(dbNumber int) ([]FieldMeta, error) {
	rows, err := s.db.Query(
		`SELECT name, offset, type, bit, length, ord, value
		 FROM fields WHERE db_number=? ORDER BY ord, offset, bit`, dbNumber)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FieldMeta
	for rows.Next() {
		var f FieldMeta
		var v string
		if err := rows.Scan(&f.Name, &f.Offset, &f.Type, &f.Bit, &f.Length, &f.Ord, &v); err != nil {
			return nil, err
		}
		f.DBNumber = dbNumber
		if v != "" {
			var val any
			if json.Unmarshal([]byte(v), &val) == nil {
				f.Value = val
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteField 删除指定数据块的具名字段。
func (s *Store) DeleteField(dbNumber int, name string) error {
	_, err := s.db.Exec(
		`DELETE FROM fields WHERE db_number=? AND name=?`, dbNumber, name)
	return err
}

// fieldBits 返回字段类型占用的位数（用于按位打包）。
func fieldBits(t string, length int) int {
	switch strings.ToLower(t) {
	case "bool", "bit":
		return 1
	case "byte":
		return 8
	case "word", "int":
		return 16
	case "dword", "dint", "real":
		return 32
	case "string":
		if length <= 0 {
			length = 1
		}
		return length * 8
	}
	return -1
}

// PackFields 按字段顺序进行位级打包，计算每个字段的字节偏移与位号。
// 规则：bool/bit 占 1 位并按位紧密排列；多字节类型（byte/word/dint/...）
// 在自身之前对齐到下一字节边界，确保解码与常见 PLC 数据布局一致。
func PackFields(fields []FieldMeta) ([]FieldMeta, error) {
	pos := 0 // 已占用位数（从 0 起）
	out := make([]FieldMeta, len(fields))
	for i, f := range fields {
		bits := fieldBits(f.Type, f.Length)
		if bits <= 0 {
			return nil, fmt.Errorf("字段 %q 不支持的类型 %q", f.Name, f.Type)
		}
		if bits > 1 && pos%8 != 0 {
			pos += 8 - (pos % 8) // 多字节类型对齐到字节边界
		}
		f.Offset = pos / 8
		f.Bit = pos % 8
		pos += bits
		out[i] = f
	}
	return out, nil
}

// Recompute 按字段顺序重新打包，并把计算后的偏移/位号写回数据库，返回打包结果。
func (s *Store) Recompute(dbNumber int) ([]FieldMeta, error) {
	fields, err := s.GetFields(dbNumber)
	if err != nil {
		return nil, err
	}
	packed, err := PackFields(fields)
	if err != nil {
		return nil, err
	}
	for _, f := range packed {
		if _, err := s.db.Exec(
			`UPDATE fields SET offset=?, bit=? WHERE db_number=? AND name=?`,
			f.Offset, f.Bit, dbNumber, f.Name); err != nil {
			return nil, err
		}
	}
	return packed, nil
}

// MoveField 将字段在顺序中上移（dir=-1）或下移（dir=+1），与相邻字段交换 ord。
func (s *Store) MoveField(dbNumber int, name string, dir int) error {
	fields, err := s.GetFields(dbNumber)
	if err != nil {
		return err
	}
	idx := -1
	for i, f := range fields {
		if f.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("字段 %q 不存在", name)
	}
	ni := idx + dir
	if ni < 0 || ni >= len(fields) {
		return nil // 已在边界，无需移动
	}
	fields[idx].Ord, fields[ni].Ord = fields[ni].Ord, fields[idx].Ord
	for _, f := range fields {
		if _, err := s.db.Exec(
			`UPDATE fields SET ord=? WHERE db_number=? AND name=?`,
			f.Ord, dbNumber, f.Name); err != nil {
			return err
		}
	}
	return nil
}
