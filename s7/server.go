package s7

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"plc-simulater/config"
	"plc-simulater/model"
	"plc-simulater/store"
)

// errCOTPDisconnect 表示客户端主动断开 COTP 连接，属于正常关闭流程。
var errCOTPDisconnect = errors.New("s7: cotp disconnect")

// Server 是 S7 模拟器服务端。
type Server struct {
	cfg    *config.Config
	dbs    map[int]*model.DB
	store  *store.Store
	mu     sync.RWMutex
	maxPDU int
	logger *log.Logger
}

// New 依据配置创建服务端。
func New(cfg *config.Config) (*Server, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite(%s) 失败: %w", cfg.DBPath, err)
	}
	s := &Server{
		cfg:    cfg,
		dbs:    make(map[int]*model.DB, len(cfg.DBs)),
		store:  st,
		maxPDU: cfg.MaxPDU,
		logger: log.Default(),
	}
	for _, dbCfg := range cfg.DBs {
		// 用配置字段构造初始字节（首次运行时作为初始 raw）。
		fields := make([]model.Field, 0, len(dbCfg.Fields))
		for _, f := range dbCfg.Fields {
			fields = append(fields, model.Field{
				Name: f.Name, Offset: f.Offset, Type: f.Type,
				Bit: f.Bit, Length: f.Length, Value: f.Value,
			})
		}
		initDB, err := model.NewDB(dbCfg.Number, dbCfg.Size, dbCfg.Name, fields)
		if err != nil {
			_ = st.Close()
			return nil, err
		}

		// 读取已有元数据，UpsertDB 时保留已落库的 raw，避免重启时被清空。
		existingMeta, _ := s.store.GetDB(dbCfg.Number)
		metaRaw := []byte(nil)
		if existingMeta != nil {
			metaRaw = existingMeta.Raw
		}
		if err := s.store.UpsertDB(store.DBMeta{Number: dbCfg.Number, Size: initDB.Size(), Name: dbCfg.Name, Raw: metaRaw}); err != nil {
			_ = st.Close()
			return nil, err
		}
		if existing, _ := s.store.GetFields(dbCfg.Number); len(existing) == 0 {
			for i, f := range dbCfg.Fields {
				if err := s.store.UpsertField(store.FieldMeta{
					DBNumber: dbCfg.Number, Name: f.Name, Offset: f.Offset,
					Type: f.Type, Bit: f.Bit, Length: f.Length,
					Ord: i + 1, Value: f.Value,
				}); err != nil {
					_ = st.Close()
					return nil, err
				}
			}
		}

		stored, _ := s.store.GetFields(dbCfg.Number)
		meta, _ := s.store.GetDB(dbCfg.Number)
		var db *model.DB
		if meta != nil && len(meta.Raw) == meta.Size {
			// 库里已有整块原始字节（S7/HTTP 写入的结果）：以它为真相源重建。
			db, err = s.rebuildDBFromMeta(meta, stored)
			if err != nil {
				_ = st.Close()
				return nil, err
			}
		} else {
			// 首次运行或旧库无 raw：用配置初始字节建块，并落库为初始 raw。
			db = initDB
			if len(stored) > 0 {
				if err := applyStoredFields(db, stored); err != nil {
					_ = st.Close()
					return nil, err
				}
			}
			if err := s.persistDBRawFor(db); err != nil {
				_ = st.Close()
				return nil, err
			}
		}
		s.dbs[dbCfg.Number] = db
	}
	// 载入仅存在于 SQLite（通过 API 动态创建）的数据块，使重启后仍能恢复。
	storedDBs, _ := s.store.GetDBs()
	for _, m := range storedDBs {
		if _, exists := s.dbs[m.Number]; exists {
			continue
		}
		fields, _ := s.store.GetFields(m.Number)
		db, err := s.rebuildDBFromMeta(&m, fields)
		if err != nil {
			_ = st.Close()
			return nil, err
		}
		s.dbs[m.Number] = db
		s.logger.Printf("  从 SQLite 载入 DB%d: %d bytes", m.Number, db.Size())
	}
	return s, nil
}

// Close 释放资源（SQLite 连接）。
func (s *Server) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

// applyStoredFields 将持久化的字段定义应用到内存数据块。
func applyStoredFields(db *model.DB, stored []store.FieldMeta) error {
	for _, f := range stored {
		if err := db.AddField(model.Field{
			Name:   f.Name,
			Offset: f.Offset,
			Type:   f.Type,
			Bit:    f.Bit,
			Length: f.Length,
			Value:  f.Value,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ListenAndServe 监听并处理连接，阻塞运行。
func (s *Server) ListenAndServe() error {
	if s.cfg.HTTPListen != "" {
		if err := s.startHTTP(); err != nil {
			s.logger.Printf("HTTP 服务启动失败: %v", err)
		}
	}
	ln, err := net.Listen("tcp4", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve 在给定监听器上处理连接，阻塞运行。
// 与 ListenAndServe 分离便于单元测试使用临时端口。
func (s *Server) Serve(ln net.Listener) error {
	s.logger.Printf("S7 simulator listening on %s, max PDU = %d", ln.Addr(), s.maxPDU)
	for _, db := range s.dbs {
		s.logger.Printf("  DB%d: %d bytes", db.Number, db.Size())
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.logger.Printf("accept error: %v", err)
			continue
		}
		go s.handleConn(conn)
	}
}

// db 返回数据块，不存在返回 nil。
func (s *Server) db(number int) *model.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbs[number]
}

// DB 返回指定数据块（供 HTTP 接口等使用），不存在返回 nil。
func (s *Server) DB(number int) *model.DB {
	return s.db(number)
}

// DBs 返回所有数据块的快照（复制引用，避免外部持有内部 map）。
func (s *Server) DBs() map[int]*model.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int]*model.DB, len(s.dbs))
	for k, v := range s.dbs {
		out[k] = v
	}
	return out
}

// PersistDBRaw 将指定 DB 的整块内存字节持久化到 SQLite（供 bridge 导入等场景调用）。
func (s *Server) PersistDBRaw(number int) error {
	return s.persistDBRaw(number)
}

// persistDBRaw 将指定 DB 的整块内存字节持久化到 SQLite。
// 调用方不应持有 s.mu 写锁（内部取 DB 时用 RLock）；持锁场景请使用 persistDBRawFor。
func (s *Server) persistDBRaw(number int) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	return s.persistDBRawFor(db)
}

// persistDBRawLocked 将给定 DB 的整块内存字节持久化，不访问 s.dbs，可用于持锁场景。
func (s *Server) persistDBRawFor(db *model.DB) error {
	raw, err := db.Read(0, db.Size())
	if err != nil {
		return err
	}
	return s.store.UpdateDBRaw(db.Number, raw)
}

// CreateDB 动态创建一个新的数据块（持久化到 SQLite 并加入运行时）。
func (s *Server) CreateDB(number, size int, name string) error {
	if number <= 0 || number > 0xFFFF {
		return fmt.Errorf("invalid DB number %d", number)
	}
	if size <= 0 {
		size = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.dbs[number]; exists {
		return fmt.Errorf("DB%d already exists", number)
	}
	db, err := model.NewDB(number, size, name, nil)
	if err != nil {
		return err
	}
	s.dbs[number] = db
	raw, err := db.Read(0, db.Size())
	if err != nil {
		delete(s.dbs, number)
		return err
	}
	if err := s.store.UpsertDB(store.DBMeta{Number: number, Size: size, Name: name, Raw: raw}); err != nil {
		delete(s.dbs, number)
		return err
	}
	s.logger.Printf("创建 DB%d(%s): %d bytes", number, name, size)
	return nil
}

// RenameDB 修改数据块名称（持久化到 SQLite 并同步内存）。
func (s *Server) RenameDB(number int, name string) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	if err := s.store.UpsertDB(store.DBMeta{Number: number, Size: db.Size(), Name: name}); err != nil {
		return err
	}
	db.Name = name
	s.logger.Printf("DB%d 名称改为 %q", number, name)
	return nil
}

// DeleteDB 删除数据块（从 SQLite 与运行时一并移除）。
func (s *Server) DeleteDB(number int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.dbs[number]; !exists {
		return fmt.Errorf("DB%d not found", number)
	}
	if err := s.store.DeleteDB(number); err != nil {
		return err
	}
	delete(s.dbs, number)
	s.logger.Printf("删除 DB%d", number)
	return nil
}

// CloneDB 复制源数据块的结构与字段到新的数据块。
// dst 为目标编号，name 为目标名称（为空时自动加“副本”后缀），size 为目标大小（≤0 时沿用源大小）。
func (s *Server) CloneDB(src, dst int, name string, size int) error {
	if dst <= 0 || dst > 0xFFFF {
		return fmt.Errorf("invalid DB number %d", dst)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.dbs[dst]; exists {
		return fmt.Errorf("DB%d already exists", dst)
	}
	srcDB := s.dbs[src]
	if srcDB == nil {
		return fmt.Errorf("源 DB%d not found", src)
	}
	if size <= 0 {
		size = srcDB.Size()
	}
	if name == "" {
		if srcDB.Name != "" {
			name = srcDB.Name + "副本"
		} else {
			name = "DB" + fmt.Sprint(dst)
		}
	}
	db, err := model.NewDB(dst, size, name, nil)
	if err != nil {
		return err
	}
	// 复制源块的原始字节，使副本的值与源一致（目标更大时仅复制源大小范围）。
	copyLen := size
	if srcDB.Size() < copyLen {
		copyLen = srcDB.Size()
	}
	srcRaw, err := srcDB.Read(0, copyLen)
	if err != nil {
		return err
	}
	raw := make([]byte, size)
	copy(raw, srcRaw)
	if err := db.Write(0, raw); err != nil {
		return err
	}
	if err := s.store.UpsertDB(store.DBMeta{Number: dst, Size: size, Name: name, Raw: raw}); err != nil {
		return err
	}
	// 复制字段（保留顺序 ord 与初始值），随后重新打包计算偏移。
	srcFields, _ := s.store.GetFields(src)
	for _, f := range srcFields {
		nf := f
		nf.DBNumber = dst
		if err := s.store.UpsertField(nf); err != nil {
			return err
		}
	}
	packed, err := s.store.Recompute(dst)
	if err != nil {
		return err
	}
	mf := make([]model.Field, 0, len(packed))
	for _, p := range packed {
		mf = append(mf, model.Field{
			Name: p.Name, Offset: p.Offset, Type: p.Type,
			Bit: p.Bit, Length: p.Length, Value: p.Value,
		})
	}
	if err := db.SetFields(mf); err != nil {
		return err
	}
	s.dbs[dst] = db
	s.logger.Printf("复制 DB%d -> DB%d(%s)", src, dst, name)
	return nil
}

// ReloadDB 从 SQLite 重新加载指定数据块到内存（重建字段元数据并恢复初始值）。
func (s *Server) ReloadDB(number int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadDBLocked(number)
}

// ReloadAll 从 SQLite 重新加载全部数据块到内存。
func (s *Server) ReloadAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	metas, err := s.store.GetDBs()
	if err != nil {
		return err
	}
	for _, m := range metas {
		if err := s.reloadDBLocked(m.Number); err != nil {
			return err
		}
	}
	return nil
}

// rebuildDBFromMeta 依据持久化的元数据与字段定义重建内存数据块。
// 若保存了整块原始字节（Raw）则以其为字节真相源；否则（旧库）回退用字段初始值重建。
// 两种方式下字段元数据都只用于解释字节，不会覆盖已恢复的 raw。
func (s *Server) rebuildDBFromMeta(meta *store.DBMeta, fields []store.FieldMeta) (*model.DB, error) {
	db, err := model.NewDB(meta.Number, meta.Size, meta.Name, nil)
	if err != nil {
		return nil, err
	}
	if len(meta.Raw) == meta.Size {
		// 新格式：整块原始字节是真相源（含非字段区字节）。
		if err := db.Write(0, meta.Raw); err != nil {
			return nil, err
		}
		if err := db.SetFields(storedToModelFields(fields)); err != nil {
			return nil, err
		}
	} else {
		// 旧库兼容：没有整块 raw 时，用字段初始值重建字节。
		if err := applyStoredFields(db, fields); err != nil {
			return nil, err
		}
	}
	return db, nil
}

// storedToModelFields 只取字段元数据，不携带 Value（避免覆盖已恢复的原始字节）。
func storedToModelFields(stored []store.FieldMeta) []model.Field {
	out := make([]model.Field, 0, len(stored))
	for _, f := range stored {
		out = append(out, model.Field{
			Name: f.Name, Offset: f.Offset, Type: f.Type,
			Bit: f.Bit, Length: f.Length,
		})
	}
	return out
}

// reloadDBLocked 在持有锁的前提下从 SQLite 重建单个内存数据块。
func (s *Server) reloadDBLocked(number int) error {
	meta, err := s.store.GetDB(number)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("DB%d 在数据库中不存在", number)
	}
	fields, _ := s.store.GetFields(number)
	db, err := s.rebuildDBFromMeta(meta, fields)
	if err != nil {
		return err
	}
	s.dbs[number] = db
	s.logger.Printf("从数据库重新加载 DB%d", number)
	return nil
}

// UpsertField 新增或更新指定 DB 的具名字段定义（持久化并应用到运行时）。
// 保存后会按字段顺序与类型重新打包，重算整块所有字段的偏移与位号，
// 并同步更新内存中 DB 块的字段元数据（保留运行时字节值）。
func (s *Server) UpsertField(number int, f store.FieldMeta) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	if f.Name == "" {
		return fmt.Errorf("字段名称不能为空")
	}
	f.DBNumber = number

	// 计算排序号 ord：已存在的字段保留其顺序，新字段追加到末尾。
	existing, _ := s.store.GetFields(number)
	found := false
	for _, e := range existing {
		if e.Name == f.Name {
			found = true
			if f.Ord <= 0 {
				f.Ord = e.Ord
			}
		}
	}
	if !found {
		maxOrd := 0
		for _, e := range existing {
			if e.Ord > maxOrd {
				maxOrd = e.Ord
			}
		}
		if f.Ord <= 0 {
			f.Ord = maxOrd + 1
		}
	}

	if err := s.store.UpsertField(f); err != nil {
		return err
	}
	// 类型/长度/顺序变化后，按位打包重新计算整块偏移。
	packed, err := s.store.Recompute(number)
	if err != nil {
		return err
	}
	// 重建内存字段元数据（仅元数据，保留运行时字节值）。
	mf := make([]model.Field, 0, len(packed))
	for _, p := range packed {
		mf = append(mf, model.Field{Name: p.Name, Offset: p.Offset, Type: p.Type, Bit: p.Bit, Length: p.Length})
	}
	if err := db.SetFields(mf); err != nil {
		return err
	}
	// 若本次提供了初始值（新增字段或显式修改值），写入对应字节并落库。
	if f.Value != nil {
		for _, p := range packed {
			if p.Name == f.Name {
				_ = db.SetField(model.Field{Name: p.Name, Offset: p.Offset, Type: p.Type, Bit: p.Bit, Length: p.Length}, f.Value)
			}
		}
		if err := s.persistDBRaw(number); err != nil {
			return err
		}
	}
	s.logger.Printf("配置 DB%d 字段 %s (类型=%s 偏移=%d.%d)", number, f.Name, f.Type, f.Offset, f.Bit)
	return nil
}

// SetFieldValue 修改具名字段的值并持久化到 SQLite。
// 先写内存再持久化；持久化失败时回滚内存值，避免界面与重载结果不一致。
func (s *Server) SetFieldValue(number int, name string, value any) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	f, ok := db.Fields()[name]
	if !ok {
		return fmt.Errorf("字段不存在: %s", name)
	}
	old, _ := db.FieldValue(f)
	if err := db.SetField(f, value); err != nil {
		return err
	}
	// 整块原始字节是真相源，落库后重新加载/重启都能恢复该值。
	if err := s.persistDBRaw(number); err != nil {
		_ = db.SetField(f, old) // 回滚内存，保持与持久化状态一致
		return err
	}
	// 兼容旧库（无 raw）时仍按字段 value 恢复，保持两者一致。
	_ = s.store.UpdateFieldValue(number, name, value)
	return nil
}

// SetBitValue 修改指定字节的某一位并持久化整块原始字节到 SQLite。
// 位修改无论是否对应具名字段都会随整块 raw 落库，因此重新加载后均保持。
func (s *Server) SetBitValue(number, offset, bit int, value bool) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	if err := db.SetBit(offset, bit, value); err != nil {
		return err
	}
	if err := s.persistDBRaw(number); err != nil {
		_ = db.SetBit(offset, bit, !value) // 回滚内存
		return err
	}
	// 兼容旧库（无 raw）：若该位对应 bool/bit 字段，同步其 value。
	for _, f := range db.Fields() {
		t := strings.ToLower(f.Type)
		if (t == "bool" || t == "bit") && f.Offset == offset && f.Bit == bit {
			_ = s.store.UpdateFieldValue(number, f.Name, value)
			break
		}
	}
	return nil
}

// RestoreFieldLayout 按给定的偏移/位号恢复字段定义，不重新打包。
// 用于导入包含原始字节（raw）的数据块：字段偏移必须与原始字节布局一致，
// 否则读取字符串等类型时会因偏移错位而乱码。
func (s *Server) RestoreFieldLayout(number int, fields []store.FieldMeta) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	mf := make([]model.Field, 0, len(fields))
	for _, f := range fields {
		f.DBNumber = number
		if err := s.store.UpsertField(f); err != nil {
			return err
		}
		mf = append(mf, model.Field{
			Name: f.Name, Offset: f.Offset, Type: f.Type,
			Bit: f.Bit, Length: f.Length,
		})
	}
	if err := db.SetFields(mf); err != nil {
		return err
	}
	s.logger.Printf("恢复 DB%d 字段布局: %d 个字段（保留原始偏移）", number, len(fields))
	return nil
}

// DeleteField 删除指定 DB 的具名字段定义（从持久化与运行时移除）。
// 删除后会重新打包，使后续字段偏移前移。
func (s *Server) DeleteField(number int, name string) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	if err := s.store.DeleteField(number, name); err != nil {
		return err
	}
	packed, err := s.store.Recompute(number)
	if err != nil {
		return err
	}
	mf := make([]model.Field, 0, len(packed))
	for _, p := range packed {
		mf = append(mf, model.Field{Name: p.Name, Offset: p.Offset, Type: p.Type, Bit: p.Bit, Length: p.Length})
	}
	if err := db.SetFields(mf); err != nil {
		return err
	}
	s.logger.Printf("删除 DB%d 字段 %s", number, name)
	return nil
}

// MoveField 调整字段在顺序中的位置（dir=-1 上移，+1 下移），
// 随后重新打包以更新整块偏移。
func (s *Server) MoveField(number int, name string, dir int) error {
	db := s.DB(number)
	if db == nil {
		return fmt.Errorf("DB%d not found", number)
	}
	if err := s.store.MoveField(number, name, dir); err != nil {
		return err
	}
	packed, err := s.store.Recompute(number)
	if err != nil {
		return err
	}
	mf := make([]model.Field, 0, len(packed))
	for _, p := range packed {
		mf = append(mf, model.Field{Name: p.Name, Offset: p.Offset, Type: p.Type, Bit: p.Bit, Length: p.Length})
	}
	if err := db.SetFields(mf); err != nil {
		return err
	}
	s.logger.Printf("移动 DB%d 字段 %s (dir=%d)", number, name, dir)
	return nil
}

// Fields 返回指定 DB 持久化的字段定义列表。
func (s *Server) Fields(number int) ([]store.FieldMeta, error) {
	return s.store.GetFields(number)
}

// handleConn 处理单个 TCP 连接：COTP 握手 + S7 读写。
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	for {
		frame, err := ReadFrame(conn)
		if err != nil {
			if !isClosedErr(err) {
				s.logger.Printf("conn %s: %v", remote, err)
			}
			return
		}
		if err := s.processFrame(frame, conn, remote); err != nil {
			if !errors.Is(err, errCOTPDisconnect) {
				s.logger.Printf("conn %s: %v", remote, err)
			}
			return
		}
	}
}

// processFrame 处理一条报文。
func (s *Server) processFrame(frame []byte, conn net.Conn, remote string) error {
	// COTP Connect Request -> 应答 Connect Confirm
	// 按 PDU 类型码（frame[5]）判断；LI 因客户端携带的参数不同而变化，
	// 带 TSAP 参数的 CR 至少 11 字节（snap7/gos7/plc4go 均如此）。
	if len(frame) >= 11 && frame[5] == COTPCR {
		cc := buildConnectConfirm(frame)
		if _, err := conn.Write(cc); err != nil {
			return err
		}
		return nil
	}
	// COTP Disconnect Request -> 应答 Disconnect Confirm 并关闭连接
	if len(frame) >= 10 && frame[5] == COTPDR {
		dc := buildDisconnectConfirm(frame)
		if _, err := conn.Write(dc); err != nil {
			return err
		}
		return errCOTPDisconnect
	}
	f, err := ParseFrame(frame)
	if err != nil {
		return err
	}
	h, err := ParsePDUHeader(f.PDU)
	if err != nil {
		return err
	}
	var resp []byte
	switch h.ROSCTR {
	case ROSCTRJob:
		// 参数区起始于 PDU 偏移 10
		if h.ParamLen < 1 || 10+h.ParamLen > len(f.PDU) {
			return ErrBadPDU
		}
		params := f.PDU[10 : 10+h.ParamLen]
		data := []byte{}
		if 10+h.ParamLen+h.DataLen <= len(f.PDU) {
			data = f.PDU[10+h.ParamLen : 10+h.ParamLen+h.DataLen]
		}
		switch params[0] {
		case FuncSetup:
			resp = s.handleSetup(h, params)
		case FuncRead:
			resp = s.handleRead(h, params, remote)
		case FuncWrite:
			resp = s.handleWrite(h, params, data, remote)
		default:
			return nil // 未支持的功能码，忽略
		}
	case ROSCTRUser:
		resp = s.handleUserData(h, f.PDU, remote)
	default:
		return nil // 其他 ROSCTR 暂不处理
	}
	if resp != nil {
		if _, err := conn.Write(resp); err != nil {
			return err
		}
	}
	return nil
}

// COTP CR/CC 可选参数代码（ISO 8073 / TPKT over TCP）。
const (
	cotpParamTPDUSize    = 0xC0 // TPDU 大小（1 字节）
	cotpParamCallingTSAP = 0xC1 // 调用方（客户端）TSAP
	cotpParamCalledTSAP  = 0xC2 // 被叫方（服务端）TSAP
)

// cotpCRParams 从 COTP Connect Request 中解析出的可选参数。
type cotpCRParams struct {
	tpduSize    byte   // 客户端请求的 TPDU 大小编码（0x0A = 1024）
	callingTSAP []byte // 客户端本地 TSAP
	calledTSAP  []byte // 客户端眼中我们的 TSAP
}

// parseCOTPParams 按参数类型码解析 COTP CR 的可选参数。
// 各客户端的参数顺序不同（plc4go 为 C1,C2,C0；snap7/gos7 为 C0,C1,C2），
// 因此必须按类型码遍历，不能假定固定偏移。
// 帧结构：TPKT(4) + LI(1) + type(1) + dstRef(2) + srcRef(2) + classOpt(1) + params。
func parseCOTPParams(cr []byte) cotpCRParams {
	var p cotpCRParams
	p.tpduSize = 0x0A // 缺省 1024
	if len(cr) < 11 || cr[4] < 6 {
		return p // 无参数
	}
	end := 5 + int(cr[4]) // 4(TPKT) + 1(LI) + LI
	if end > len(cr) {
		end = len(cr)
	}
	for off := 11; off+2 <= end; {
		code := cr[off]
		length := int(cr[off+1])
		if length < 0 || off+2+length > end {
			break
		}
		value := cr[off+2 : off+2+length]
		switch code {
		case cotpParamTPDUSize:
			if length >= 1 {
				p.tpduSize = value[0]
			}
		case cotpParamCallingTSAP:
			if length >= 2 {
				p.callingTSAP = value[:2]
			}
		case cotpParamCalledTSAP:
			if length >= 2 {
				p.calledTSAP = value[:2]
			}
		}
		off += 2 + length
	}
	return p
}

// buildConnectConfirm 针对 COTP 连接请求构造连接确认。
//
//	03 00 <len:2> 11 D0 <dstRef:2> <srcRef:2> 00 C0 01 0A C1 02 <srcTSAP> C2 02 <dstTSAP>
//
// dstRef 回显请求的 srcRef（snap7 IsoConfirmConnection 即按此解析）；
// TSAP 对调：CC 的调用方 TSAP = 请求的被叫方 TSAP，CC 的被叫方 TSAP = 请求的调用方 TSAP；
// TPDU 大小回显客户端请求值（缺省 1024，即编码 0x0A）。
func buildConnectConfirm(cr []byte) []byte {
	p := parseCOTPParams(cr)
	cc := make([]byte, 22)
	cc[0], cc[1] = 0x03, 0x00
	binary.BigEndian.PutUint16(cc[2:], 22)
	cc[4] = 0x11
	cc[5] = COTPCC
	if len(cr) >= 10 {
		// dstRef = 请求的 srcRef，srcRef = 请求的 dstRef
		cc[6], cc[7] = cr[8], cr[9]
		cc[8], cc[9] = cr[6], cr[7]
	} else {
		cc[6], cc[7] = 0x00, 0x01
		cc[8], cc[9] = 0x00, 0x01
	}
	cc[10] = 0x00
	cc[11] = cotpParamTPDUSize
	cc[12], cc[13] = 0x01, p.tpduSize
	cc[14], cc[15] = cotpParamCallingTSAP, 0x02
	cc[18], cc[19] = cotpParamCalledTSAP, 0x02
	if len(p.calledTSAP) == 2 && len(p.callingTSAP) == 2 {
		// CC 调用方 = 请求被叫方，CC 被叫方 = 请求调用方
		cc[16], cc[17] = p.calledTSAP[0], p.calledTSAP[1]
		cc[20], cc[21] = p.callingTSAP[0], p.callingTSAP[1]
	} else {
		// 客户端未携带 TSAP 参数：使用常规值（rack0/slot1 = 0x0100，客户端 0x0102）
		cc[16], cc[17] = 0x01, 0x00
		cc[20], cc[21] = 0x01, 0x02
	}
	return cc
}

// buildDisconnectConfirm 针对 COTP 断开请求构造断开确认。
// 帧结构：TPKT(4) + LI(1) + 0xC0 + dstRef(2) + srcRef(2) = 10 字节。
func buildDisconnectConfirm(dr []byte) []byte {
	dc := make([]byte, 10)
	dc[0], dc[1] = 0x03, 0x00
	binary.BigEndian.PutUint16(dc[2:], 10)
	dc[4] = 0x05
	dc[5] = COTPDC
	if len(dr) >= 10 {
		// dstRef = 请求的 srcRef，srcRef = 请求的 dstRef
		dc[6], dc[7] = dr[8], dr[9]
		dc[8], dc[9] = dr[6], dr[7]
	}
	return dc
}

func isClosedErr(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// startHTTP 在后台启动 HTTP 管理界面（静态页面 + JSON API）。
// 非阻塞：在独立 goroutine 中运行，错误仅记录日志。
func (s *Server) startHTTP() error {
	ln, err := net.Listen("tcp4", s.cfg.HTTPListen)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: s.httpHandler()}
	go func() {
		s.logger.Printf("HTTP 管理界面已启动: http://%s", ln.Addr())
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Printf("HTTP 服务异常: %v", err)
		}
	}()
	return nil
}
