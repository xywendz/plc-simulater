package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"

	"plc-simulater/internal/bridge"
)

// allowedMethods 是 Web 模式对外的接口契约：与 frontend/src/api/client.ts 的调用一一对应。
// 使用白名单而非全量反射，避免把构造器等辅助方法意外暴露为 HTTP 端点。
var allowedMethods = map[string]struct{}{
	"Info": {}, "ListDBs": {}, "GetDB": {}, "ListFields": {},
	"CreateDB": {}, "CloneDB": {}, "RenameDB": {}, "DeleteDB": {},
	"ReloadDB": {}, "ReloadAll": {},
	"SetBit": {}, "SetField": {}, "UpsertField": {}, "DeleteField": {},
	"MoveField": {}, "RawQuery": {},
	"ExportDB": {}, "ExportAll": {}, "ImportDB": {}, "ImportAll": {},
}

const maxRPCBody = 8 << 20 // 8 MiB（导入包可能包含多块 DB 的原始字节）

type rpcRequest struct {
	Args []json.RawMessage `json:"args"`
}

type rpcResponse struct {
	Result any `json:"result"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeRPCError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcError{Code: code, Message: message})
}

func writeRPCResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(rpcResponse{Result: result})
}

// handleRPC 把 POST /api/rpc/{Method} 转换为对 *bridge.Bridge 同名方法的一次反射调用。
// 参数形状与 Wails 绑定完全一致：{"args": [arg1, arg2, ...]}。
func handleRPC(b *bridge.Bridge) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("method")
		if _, ok := allowedMethods[name]; !ok {
			writeRPCError(w, http.StatusNotFound, "not_found", "未知的 API 方法: "+name)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRPCBody))
		if err != nil {
			writeRPCError(w, http.StatusRequestEntityTooLarge, "validation", "请求体过大或读取失败: "+err.Error())
			return
		}
		var req rpcRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeRPCError(w, http.StatusBadRequest, "validation", "请求参数格式错误: "+err.Error())
				return
			}
		}

		method := reflect.ValueOf(b).MethodByName(name)
		if !method.IsValid() {
			writeRPCError(w, http.StatusNotFound, "not_found", "方法未实现: "+name)
			return
		}
		mt := method.Type()
		if len(req.Args) > mt.NumIn() {
			writeRPCError(w, http.StatusBadRequest, "validation",
				fmt.Sprintf("方法 %s 最多接受 %d 个参数，收到 %d 个", name, mt.NumIn(), len(req.Args)))
			return
		}
		in := make([]reflect.Value, mt.NumIn())
		for i := range in {
			arg := reflect.New(mt.In(i))
			if i < len(req.Args) && string(req.Args[i]) != "null" {
				if err := json.Unmarshal(req.Args[i], arg.Interface()); err != nil {
					writeRPCError(w, http.StatusBadRequest, "validation",
						fmt.Sprintf("方法 %s 第 %d 个参数解析失败: %v", name, i+1, err))
					return
				}
			}
			in[i] = arg.Elem()
		}

		out := method.Call(in)
		last := out[len(out)-1]
		if !last.IsNil() {
			err, ok := last.Interface().(error)
			if !ok {
				writeRPCError(w, http.StatusInternalServerError, "internal", "方法返回值格式异常")
				return
			}
			writeRPCError(w, http.StatusBadRequest, "error", err.Error())
			return
		}
		var result any
		if len(out) > 1 {
			result = out[0].Interface()
		}
		writeRPCResult(w, result)
	}
}
