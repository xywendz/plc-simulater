package s7

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// item 是一个解析后的 S7ANY 请求项。
type item struct {
	Area   byte
	DBNum  int
	Offset int // 字节偏移
	Bit    int // 位号 0-7（仅 BIT 访问有效）
	TS     byte // 请求传输尺寸（S7ANY transport size）
	Length int // 元素个数（BIT 访问时为位数）
}

// String 返回 item 的可读描述，用于日志。
func (it *item) String() string {
	area := "??"
	switch it.Area {
	case AreaDB:
		area = fmt.Sprintf("DB%d", it.DBNum)
	case AreaMK:
		area = "MK"
	case AreaPE:
		area = "PE"
	case AreaPA:
		area = "PA"
	case AreaCT:
		area = "CT"
	case AreaTM:
		area = "TM"
	}
	if it.TS == TSBit {
		return fmt.Sprintf("%s @%d.%d", area, it.Offset, it.Bit)
	}
	return fmt.Sprintf("%s @%d len=%d", area, it.Offset, it.Length)
}

// parseItem 解析 12 字节的 S7ANY item：
//
//	0     1     2    3    4-5     6-7    8     9-11
//	0x12  0x0A  0x10  TS   len     db     area  addr(3)
//
// addr 为 24 位大端，(addr>>3) 是字节偏移，(addr&7) 是位号。
// len 字段是"元素个数"（WORD=2 字节/元素、REAL=4 字节/元素），BIT 类型时表示位数。
// 参考 plc4x S7AddressAny 与 gos7 readArea。
func parseItem(b []byte) (*item, error) {
	if len(b) < 12 {
		return nil, errors.New("item too short")
	}
	if b[0] != 0x12 || b[1] != 0x0A || b[2] != 0x10 {
		return nil, errors.New("unsupported item specifier")
	}
	ts := b[3]
	length := int(binary.BigEndian.Uint16(b[4:6]))
	addr := int(b[9])<<16 | int(b[10])<<8 | int(b[11])
	return &item{
		Area:   b[8],
		DBNum:  int(binary.BigEndian.Uint16(b[6:8])),
		Offset: addr >> 3,
		Bit:    addr & 0x07,
		TS:     ts,
		Length: length,
	}, nil
}

// elementSize 返回请求传输尺寸下每个元素占用的字节数（与 gos7 dataSizeByte 一致）。
func elementSize(ts byte) int {
	switch ts {
	case TSBit, TSByte, TSChar:
		return 1
	case TSWord, TSInt, TSDate, TSS5:
		return 2
	case TSDWord, TSDInt, TSReal, TSTod, TSTime:
		return 4
	}
	return -1
}

// respTransportSize 将请求传输尺寸映射为响应数据项的传输尺寸（plc4x DataTransportSize）。
func respTransportSize(ts byte) byte {
	switch ts {
	case TSBit:
		return TSResBit
	case TSInt, TSDInt:
		return TSResInt
	case TSReal:
		return TSResReal
	case TSChar:
		return TSResOctet
	}
	// BYTE/WORD/DWORD/DATE/TOD/TIME/S5TIME 统一为 0x04
	return TSResByte
}

// lengthInBits 返回响应数据项长度字段是否以"位"为单位
// （plc4x DataTransportSize.SizeInBits() 为真的传输尺寸）。
func lengthInBits(ts byte) bool {
	switch ts {
	case TSResBit, TSResByte, TSResInt:
		return true
	}
	return false
}

// dataItemBytes 根据响应传输尺寸与长度字段计算数据字节数。
// 与 plc4x S7VarPayloadDataItem 解析逻辑一致：
// SizeInBits 为真时 nBytes = ceil(dataLength/8)，否则 nBytes = dataLength。
func dataItemBytes(ts byte, length int) int {
	if lengthInBits(ts) {
		return (length + 7) / 8
	}
	return length
}

// buildDataItem 构造一个数据项（读响应/写请求共用结构）：
//
//	returnCode(1) + transportSize(1) + length(2) + data + [fill byte]
//
// 长度单位与 plc4x 一致：BIT 为位数；BYTE_WORD_DWORD/INTEGER 为位（8×字节数）；
// REAL/DINTEGER/OCTET_STRING 为字节数。
// fill 为 true 且数据长度为奇数时追加一个 0x00 填充字节（plc4x 的奇数对齐规则）。
func buildDataItem(rc, ts byte, data []byte, fill bool) []byte {
	length := len(data)
	switch {
	case ts == TSResBit:
		length = len(data) // BIT：位数为 1（单 bit 时 data 为 1 字节）
	case lengthInBits(ts):
		length = len(data) * 8
	}
	out := make([]byte, 0, 4+len(data)+1)
	out = append(out, rc, ts, byte(length>>8), byte(length))
	out = append(out, data...)
	if fill && len(data)%2 == 1 {
		out = append(out, 0x00)
	}
	return out
}

// handleSetup 处理 Setup Communication (0xF0)，协商最大 PDU 长度。
// 参数格式：F0 00 <maxAmqCaller(2)> <maxAmqCallee(2)> <pduLength(2)>。
func (s *Server) handleSetup(h PDUHeader, params []byte) []byte {
	if len(params) < 8 {
		return nil
	}
	resp := make([]byte, 8)
	copy(resp, params[:8])
	resp[0] = FuncSetup
	binary.BigEndian.PutUint16(resp[6:], uint16(s.maxPDU))
	return BuildFrame(BuildAckData(h.Ref, resp, nil))
}

// handleRead 处理 Read Var (0x04)。
// 响应参数区为 04 <itemCount>，数据区为每个 item 的数据项拼接。
func (s *Server) handleRead(h PDUHeader, params []byte, remote string) []byte {
	if len(params) < 2 {
		return nil
	}
	count := int(params[1])
	if 2+count*12 > len(params) {
		return nil
	}
	items := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		it, err := parseItem(params[2+i*12:])
		if err != nil {
			s.logger.Printf("read <invalid item> from %s: %v", remote, err)
			items = append(items, errorItem(ResObjectNotFound))
			continue
		}
		s.logger.Printf("read %s from %s", it, remote)
		items = append(items, s.readItem(it))
	}
	// 拼接数据区：非最后一项且数据长度为奇数时补 0x00（plc4x 对齐规则）。
	var data []byte
	for i, it := range items {
		data = append(data, it...)
		if i < len(items)-1 && len(it) > 4 && (len(it)-4)%2 == 1 {
			data = append(data, 0x00)
		}
	}
	return BuildFrame(BuildAckData(h.Ref, []byte{FuncRead, byte(count)}, data))
}

// errorItem 构造错误数据项。
// plc4x 的 S7VarPayloadDataItem 解析对每个 item 都读取完整的 4 字节头
// （返回码 + 传输尺寸 + 长度），错误项也必须带头部（与真实 PLC 一致），
// 否则 plc4x 会因数据区错位而解析失败。
func errorItem(rc byte) []byte {
	return []byte{rc, TSResNull, 0x00, 0x00}
}

// readItem 读取单个 item，返回响应数据项内容（含返回码头）。
func (s *Server) readItem(it *item) []byte {
	if it.Area != AreaDB {
		return errorItem(ResObjectNotFound)
	}
	db := s.db(it.DBNum)
	if db == nil {
		return errorItem(ResObjectNotFound)
	}
	if it.TS == TSBit {
		// BIT 读：返回 1 字节，0/1 表示位值
		on, err := db.GetBit(it.Offset, it.Bit)
		if err != nil {
			return errorItem(ResObjectNotAvailable)
		}
		v := byte(0)
		if on {
			v = 1
		}
		return []byte{ResOK, TSResBit, 0x00, 0x01, v}
	}
	if it.Length <= 0 {
		return errorItem(ResDataTypeNotsupp)
	}
	es := elementSize(it.TS)
	if es < 0 {
		return errorItem(ResDataTypeNotsupp)
	}
	nBytes := it.Length * es
	buf, err := db.Read(it.Offset, nBytes)
	if err != nil {
		return errorItem(ResObjectNotAvailable)
	}
	return buildDataItem(ResOK, respTransportSize(it.TS), buf, false)
}

// handleWrite 处理 Write Var (0x05)。
// 请求数据区每 item 结构：0x00(保留) + 传输尺寸 + 长度(2) + 数据 + [fill byte]。
// 长度单位与读响应一致：BIT 为位数；BYTE_WORD_DWORD/INTEGER 为位；
// REAL/DINTEGER/OCTET_STRING 为字节。
// 响应参数区为 05 <itemCount>，数据区为每 item 1 字节返回码（plc4x 严格校验数量一致）。
func (s *Server) handleWrite(h PDUHeader, params, data []byte, remote string) []byte {
	if len(params) < 2 {
		return nil
	}
	count := int(params[1])
	if 2+count*12 > len(params) {
		return nil
	}
	rcs := make([]byte, 0, count)
	off := 0
	for i := 0; i < count; i++ {
		it, err := parseItem(params[2+i*12:])
		if err != nil {
			s.logger.Printf("write <invalid item> from %s: %v", remote, err)
			rcs = append(rcs, ResObjectNotFound)
			continue
		}
		di, next, err := parseDataItem(data, off)
		if err != nil {
			s.logger.Printf("write %s from %s: %v", it, remote, err)
			rcs = append(rcs, ResObjectNotFound)
			continue
		}
		off = next
		if err := s.writeItem(it, di.ts, di.data); err != nil {
			s.logger.Printf("write %s from %s: %v", it, remote, err)
			rcs = append(rcs, mapWriteError(err))
			continue
		}
		s.logger.Printf("write %s from %s", it, remote)
		rcs = append(rcs, ResOK)
	}
	return BuildFrame(BuildAckData(h.Ref, []byte{FuncWrite, byte(count)}, rcs))
}

// mapWriteError 将写入错误映射为 S7 返回码。
func mapWriteError(err error) byte {
	switch {
	case errors.Is(err, errDBNotFound):
		return ResObjectNotFound
	case errors.Is(err, errUnsupportedArea):
		return ResObjectNotFound
	default:
		return ResObjectNotAvailable
	}
}

// writeItem 写入单个 item。
func (s *Server) writeItem(it *item, dataTS byte, payload []byte) error {
	if it.Area != AreaDB {
		return fmt.Errorf("unsupported area 0x%02X: %w", it.Area, errUnsupportedArea)
	}
	db := s.db(it.DBNum)
	if db == nil {
		return fmt.Errorf("DB%d not found: %w", it.DBNum, errDBNotFound)
	}
	if it.TS == TSBit || dataTS == TSResBit {
		if len(payload) < 1 {
			return errors.New("missing bit value")
		}
		return db.SetBit(it.Offset, it.Bit, payload[0] != 0)
	}
	return db.Write(it.Offset, payload)
}

var (
	errDBNotFound      = errors.New("db not found")
	errUnsupportedArea = errors.New("unsupported area")
)

// dataItem 是解析后的数据项（读响应/写请求共用）。
type dataItem struct {
	rc   byte   // 返回码（写请求中为保留字节 0x00）
	ts   byte   // 传输尺寸
	data []byte // 数据负载
}

// parseDataItem 从 data[off:] 解析一个数据项，返回下一项的偏移。
// 结构：returnCode(1) + transportSize(1) + length(2) + data + [fill byte]。
// length 的单位与 plc4x S7VarPayloadDataItem 一致，见 dataItemBytes。
// 非最后一项且数据长度为奇数时后面跟一个 0x00 填充字节。
func parseDataItem(data []byte, off int) (*dataItem, int, error) {
	if off+4 > len(data) {
		return nil, off, errors.New("data item too short")
	}
	di := &dataItem{
		rc: data[off],
		ts: data[off+1],
	}
	length := int(binary.BigEndian.Uint16(data[off+2:]))
	n := dataItemBytes(di.ts, length)
	if n < 0 || off+4+n > len(data) {
		return nil, off, fmt.Errorf("data item length out of range: ts=0x%02X len=%d", di.ts, length)
	}
	di.data = data[off+4 : off+4+n]
	off += 4 + n
	// 奇数长度的数据项后面可能有一个填充字节：
	// 只有当剩余字节 >= 2（还有后续项）且数据长度为奇数时才消费。
	if n%2 == 1 && off < len(data) && len(data)-off >= 2 {
		off++
	}
	return di, off, nil
}

// handleUserData 处理 UserData (0x07) 报文。
// 目前支持 CPU 功能的 Read SZL（读取系统状态列表），其余功能忽略。
// 返回完整的 TPKT+COTP 响应帧，无法识别时返回 nil（不响应）。
//
// 参数区布局（plc4x S7ParameterUserData / S7ParameterUserDataItemCPUFunctions）：
//
//	parameterType(0x00) + numItems(>=1) + item
//	item = itemType(0x12) + itemLength + method + type|group + subfunction + sequenceNumber [+ 可选字段]
//
// 其中 method 0x11=请求/0x12=应答，type|group 高 4 位为 CPU 功能类型(0x4)、
// 低 4 位为功能组(0x4=SZL)。plc4x 与 snap7 发送的 itemLength 不同
// （无可选字段时为 3/4，真实 PLC 为 8），因此不校验 itemLength。
func (s *Server) handleUserData(h PDUHeader, pdu []byte, remote string) []byte {
	if h.ParamLen < 8 || 10+h.ParamLen > len(pdu) {
		return nil
	}
	params := pdu[10 : 10+h.ParamLen]
	var data []byte
	if 10+h.ParamLen+h.DataLen <= len(pdu) {
		data = pdu[10+h.ParamLen : 10+h.ParamLen+h.DataLen]
	}
	// UserData 参数块：功能组 0x00、项目数>=1、item 类型 0x12
	if params[0] != 0x00 || params[1] < 1 || params[2] != UDItemTypeCPU {
		return nil
	}
	// CPU 功能 item：仅支持 Read SZL —— 方法 0x11、类型|组 0x44、子功能 0x01
	if len(params) < 8 {
		return nil
	}
	if params[4] != UDMethodReadSZL || params[5] != UDTypeGroupSZLReq || params[6] != UDSubReadSZL {
		return nil
	}
	// 数据项：返回码 + 传输尺寸(OCTET_STRING) + 长度(4) + SZL ID(2) + SZL 索引(2)
	// 请求的返回码字节各客户端不一致（plc4x 用 0xFF，snap7 用 0x00），不校验。
	var szlID, szlIndex uint16
	if len(data) >= 8 && data[1] == TSResOctet {
		szlID = binary.BigEndian.Uint16(data[4:])
		szlIndex = binary.BigEndian.Uint16(data[6:])
	}
	szl := s.buildSZLData(szlID, szlIndex)
	// 响应参数区（12 字节，与真实 PLC 一致）：回显请求字段，
	// 方法改为应答(0x12)，类型|组改为 0x84（类型 0x8=应答 | 组 0x4=SZL）——
	// plc4x 据此将负载分派为 S7PayloadUserDataItemCpuFunctionReadSzlResponse；
	// 随后是数据单元引用/末包标记/错误码。
	respParams := []byte{0x00, 0x01, UDItemTypeCPU, 0x08,
		UDMethodReadSZL + 1, UDTypeGroupSZLRsp, UDSubReadSZL, params[7],
		0x00, 0x00, 0x00, 0x00}
	// 响应数据区：返回码 + 传输尺寸(OCTET_STRING) + 数据长度 + SZL 数据。
	respData := []byte{ResOK, TSResOctet, byte(len(szl) >> 8), byte(len(szl))}
	respData = append(respData, szl...)
	s.logger.Printf("read SZL 0x%04X[0x%04X] from %s -> %d bytes", szlID, szlIndex, remote, len(szl))
	return BuildFrame(BuildUserData(h.Ref, respParams, respData))
}

// szlRecordLen 是单条 SzlDataTreeItem 的字节长度（plc4x 定义）。
// plc4x 旧版 S7PayloadUserDataItemCpuFunctionReadSzlResponse 会按 const 0x1C(28)
// 校验 szlItemLength，并据此把每条记录解析为固定 szlRecordLen 字节，故此处必须保持一致。
const szlRecordLen = 28

// buildSZLData 构造 SZL 响应数据段（位于 data item 头部 ff 09 <len> 之后）。
// 字段顺序严格遵循 plc4x 旧版 S7PayloadUserDataItemCpuFunctionReadSzlResponse 解析：
//
//	szlId(2) + szlIndex(2) + szlItemLength(2, 固定 28) + szlItemCount(2) + [SzlDataTreeItem × N]
//
// 每条 SzlDataTreeItem 固定 szlRecordLen(28) 字节（plc4x 定义）：
//
//	itemIndex(2) + mlfb(20) + moduleTypeId(2) + ausbg(2) + ausbe(2)
//
// szlItemLength 必须为 28，否则 plc4x 的 const 0x1C 校验失败（正是本次报错的根因）；
// szlItemCount 为记录条数，plc4x 按 implicit 读取并据此解析后续数组。
func (s *Server) buildSZLData(id, index uint16) []byte {
	records := s.buildSZLRecords(id)
	n := len(records)
	out := make([]byte, 0, 8+szlRecordLen*n)
	out = append(out, byte(id>>8), byte(id))             // szlId（请求里的 SZL ID，大端）
	out = append(out, byte(index>>8), byte(index))       // szlIndex（请求里的索引，大端）
	out = append(out, byte(szlRecordLen>>8), byte(szlRecordLen)) // szlItemLength：固定 28
	out = append(out, byte(n>>8), byte(n))                // szlItemCount：记录条数
	for _, r := range records {
		out = append(out, r...)
	}
	return out
}

// buildSZLRecords 返回指定 SZL ID 的记录列表（每条固定 szlRecordLen 字节）。
func (s *Server) buildSZLRecords(id uint16) [][]byte {
	switch id {
	case SzlModuleIdentification:
		// SZL 0x0011 模块标识：mlfb 为 MLFB 订货号。
		return [][]byte{szlRecord(0x0001, "6ES7315-2EH14-0AB0")}
	case SzlComponentIdentification:
		// SZL 0x001C 组件标识：mlfb 为模块名称（plc4x 优先探测此 ID）。
		return [][]byte{szlRecord(0x0001, "CPU 315-2 PN/DP")}
	default:
		// 未知 SZL：返回空列表，客户端可正常解析（szlItemCount=0）。
		return nil
	}
}

// szlRecord 构造一条 szlRecordLen(28) 字节的 SZL 记录：
// itemIndex(2) + mlfb(20) + moduleTypeId(2) + ausbg(2) + ausbe(2)。
// mlfb 为最多 20 字节 ASCII（不足补 0x00）；其余标识字段保持 0x0000。
func szlRecord(itemIndex uint16, mlfb string) []byte {
	r := make([]byte, szlRecordLen)
	binary.BigEndian.PutUint16(r[0:], itemIndex)
	for i := 0; i < 20 && i < len(mlfb); i++ {
		r[2+i] = mlfb[i]
	}
	return r
}
