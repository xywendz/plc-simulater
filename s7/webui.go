package s7

import (
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"plc-simulater/model"
	"plc-simulater/store"
)

//go:embed webui.html
var webuiFS embed.FS

// fieldView 是字段在 HTTP 响应中的视图。
type fieldView struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"`
	Type   string `json:"type"`
	Bit    int    `json:"bit,omitempty"`
	Length int    `json:"length,omitempty"`
	Value  any    `json:"value"`
}

// dbView 是数据块在 HTTP 响应中的视图。
// 若数据块配置了具名字段，则填充 fields（字段名/值/偏移量）；
// 否则提供 raw 原始十六进制，便于直接浏览。
type dbView struct {
	Number int         `json:"number"`
	Name   string      `json:"name"`
	Size   int         `json:"size"`
	Fields []fieldView `json:"fields"`
	Raw    string      `json:"raw,omitempty"`
}

// RegisterHTTPRoutes 将原有 REST 接口注册到给定 mux（使用 Go 1.22+ 的方法+路径模式）。
// Web 模式的新入口复用这些接口以保持向后兼容，同时叠加 /api/rpc 与新的前端资源。
func (s *Server) RegisterHTTPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dbs", s.handleDBs)
	mux.HandleFunc("GET /api/db/{number}", s.handleDBView)
	mux.HandleFunc("GET /api/db/{number}/fields", s.handleFields)
	mux.HandleFunc("POST /api/db", s.handleCreateDB)
	mux.HandleFunc("POST /api/db/clone", s.handleCloneDB)
	mux.HandleFunc("DELETE /api/db/{number}", s.handleDeleteDB)
	mux.HandleFunc("POST /api/db/{number}/reload", s.handleReloadDB)
	mux.HandleFunc("POST /api/reload", s.handleReloadAll)
	mux.HandleFunc("POST /api/db/{number}/rename", s.handleRenameDB)
	mux.HandleFunc("POST /api/db/{number}/bit", s.handleSetBit)
	mux.HandleFunc("POST /api/db/{number}/set", s.handleSetField)
	mux.HandleFunc("POST /api/db/{number}/field", s.handleUpsertField)
	mux.HandleFunc("DELETE /api/db/{number}/field/{name}", s.handleDeleteField)
	mux.HandleFunc("POST /api/db/{number}/move", s.handleMoveField)
	mux.HandleFunc("GET /api/raw", s.handleRaw)
}

// httpHandler 构建旧的独立 HTTP 服务（内置单文件页面 + REST 接口），供兼容场景使用。
func (s *Server) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	s.RegisterHTTPRoutes(mux)
	return mux
}

// handleIndex 返回内置静态页面。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	data, err := webuiFS.ReadFile("webui.html")
	if err != nil {
		http.Error(w, "internal: missing webui.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleDBs 返回所有数据块视图。
func (s *Server) handleDBs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dbs := s.DBs()
	views := make([]dbView, 0, len(dbs))
	for _, db := range dbs {
		views = append(views, s.buildDBView(db))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Number < views[j].Number })
	writeJSON(w, views)
}

// handleDBView 返回单个数据块视图。
func (s *Server) handleDBView(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	db := s.DB(num)
	if db == nil {
		http.Error(w, "db not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.buildDBView(db))
}

// handleFields 返回持久化的字段定义列表。
func (s *Server) handleFields(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	if s.DB(num) == nil {
		http.Error(w, "db not found", http.StatusNotFound)
		return
	}
	fields, err := s.Fields(num)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, fields)
}

// handleCreateDB 动态创建数据块。请求体 {number, size, name?}。
func (s *Server) handleCreateDB(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Number int    `json:"number"`
		Size   int    `json:"size"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.CreateDB(req.Number, req.Size, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "number": req.Number})
}

// handleRenameDB 修改数据块名称。请求体 {name}。
func (s *Server) handleRenameDB(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.RenameDB(num, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleCloneDB 复制数据块。请求体 {src, number, name?, size?}。
func (s *Server) handleCloneDB(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Src    int    `json:"src"`
		Number int    `json:"number"`
		Name   string `json:"name"`
		Size   int    `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.CloneDB(req.Src, req.Number, req.Name, req.Size); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "number": req.Number})
}

// handleDeleteDB 删除数据块。
func (s *Server) handleDeleteDB(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	if err := s.DeleteDB(num); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleReloadDB 从数据库重新加载单个数据块到内存。
func (s *Server) handleReloadDB(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	if err := s.ReloadDB(num); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleReloadAll 从数据库重新加载全部数据块到内存。
func (s *Server) handleReloadAll(w http.ResponseWriter, r *http.Request) {
	if err := s.ReloadAll(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleUpsertField 新增或更新字段定义。请求体含 name/offset/type/bit/length/value。
func (s *Server) handleUpsertField(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	var req store.FieldMeta
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.UpsertField(num, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleDeleteField 删除字段定义。
func (s *Server) handleDeleteField(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if err := s.DeleteField(num, name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleMoveField 调整字段顺序。请求体 {name, dir}。
func (s *Server) handleMoveField(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	var req struct {
		Name string `json:"name"`
		Dir  int    `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.MoveField(num, req.Name, req.Dir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleRaw 按字节偏移/长度查询原始数据（十六进制）。
// 查询参数：db、offset、len。
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	num, _ := strconv.Atoi(q.Get("db"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	length, _ := strconv.Atoi(q.Get("len"))
	if length <= 0 || length > 1024 {
		length = 16
	}
	db := s.DB(num)
	if db == nil {
		http.Error(w, "db not found", http.StatusNotFound)
		return
	}
	if offset < 0 || offset+length > db.Size() {
		http.Error(w, "out of range", http.StatusBadRequest)
		return
	}
	buf, err := db.Read(offset, length)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"db":     num,
		"offset": offset,
		"hex":    hex.EncodeToString(buf),
		"bytes":  buf,
	})
}

// handleSetBit 修改指定位：请求体 {offset, bit, value}。
func (s *Server) handleSetBit(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	db := s.DB(num)
	if db == nil {
		http.Error(w, "db not found", http.StatusNotFound)
		return
	}
	var req struct {
		Offset int  `json:"offset"`
		Bit    int  `json:"bit"`
		Value  bool `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.SetBitValue(num, req.Offset, req.Bit, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logger.Printf("HTTP 修改 DB%d 位 @%d.%d = %v", num, req.Offset, req.Bit, req.Value)
	writeJSON(w, map[string]any{"ok": true})
}

// handleSetField 修改具名字段值：请求体 {name, value}。
func (s *Server) handleSetField(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.Error(w, "invalid db number", http.StatusBadRequest)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	db := s.DB(num)
	if db == nil {
		http.Error(w, "db not found", http.StatusNotFound)
		return
	}
	if _, ok := db.Fields()[req.Name]; !ok {
		http.Error(w, "field not found: "+req.Name, http.StatusNotFound)
		return
	}
	if err := s.SetFieldValue(num, req.Name, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logger.Printf("HTTP 修改 DB%d 字段 %s = %v", num, req.Name, req.Value)
	writeJSON(w, map[string]any{"ok": true})
}

// buildDBView 由数据块构造 HTTP 视图。
func (s *Server) buildDBView(db *model.DB) dbView {
	v := dbView{Number: db.Number, Name: db.Name, Size: db.Size()}
	fields := db.Fields()
	if len(fields) == 0 {
		// 未配置具名字段：提供原始字节十六进制以便浏览
		if raw, err := db.Read(0, db.Size()); err == nil {
			v.Raw = hex.EncodeToString(raw)
		}
		return v
	}
	fs := make([]model.Field, 0, len(fields))
	for _, f := range fields {
		fs = append(fs, f)
	}
	// 按 offset 再 bit 排序，保证展示顺序稳定
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Offset != fs[j].Offset {
			return fs[i].Offset < fs[j].Offset
		}
		return fs[i].Bit < fs[j].Bit
	})
	for _, f := range fs {
		val, _ := db.FieldValue(f)
		v.Fields = append(v.Fields, fieldView{
			Name:   f.Name,
			Offset: f.Offset,
			Type:   f.Type,
			Bit:    f.Bit,
			Length: f.Length,
			Value:  val,
		})
	}
	return v
}

// writeJSON 以 JSON 响应。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
