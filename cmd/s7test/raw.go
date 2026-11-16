package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// 原始 S7 报文客户端：完成 COTP 握手后直接构造读写请求。

// S7ANY 区域。
const (
	areaMK = 0x83
	areaDB = 0x84
)

// 传输尺寸（S7ANY transport size）。
const (
	tsBit   = 0x01
	tsBy    = 0x02
	tsWord  = 0x04
	tsInt   = 0x05
	tsDword = 0x06
	tsReal  = 0x08
	// 响应数据区/写数据区的传输尺寸。
	tsResBit  = 0x03
	tsResByte = 0x04
	tsResInt  = 0x05
)

var isoCR = []byte{
	0x03, 0x00, 0x00, 0x16, 0x11, 0xE0, 0x00, 0x00, 0x00, 0x01, 0x00, 0xC0,
	0x01, 0x0A, 0xC1, 0x02, 0x01, 0x00, 0xC2, 0x02, 0x01, 0x02,
}

var isoNegotiate = []byte{
	0x03, 0x00, 0x00, 0x19, 0x02, 0xF0, 0x80, 0x32, 0x01, 0x00, 0x00, 0x04,
	0x00, 0x00, 0x08, 0x00, 0x00, 0xF0, 0x00, 0x00, 0x01, 0x00, 0x01, 0x01,
	0xE0,
}

// rawClient 封装连接与 PDU 引用计数。
type rawClient struct {
	conn  net.Conn
	ref   uint16
	PDULen int
}

func dialRaw(addr string) (*rawClient, error) {
	c := &rawClient{}
	var err error
	c.conn, err = net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	// COTP Connect Request -> Confirm
	if _, err := c.conn.Write(isoCR); err != nil {
		c.conn.Close()
		return nil, err
	}
	if _, err := readRawFrame(c.conn); err != nil {
		c.conn.Close()
		return nil, err
	}
	// Setup Communication -> 协商 PDU 长度
	if _, err := c.conn.Write(isoNegotiate); err != nil {
		c.conn.Close()
		return nil, err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		c.conn.Close()
		return nil, err
	}
	// 响应帧: TPKT(4)+COTP(3)+PDU(20)=27 字节, PDU 长度在帧偏移 [25:27]
	if len(resp) < 27 {
		c.conn.Close()
		return nil, fmt.Errorf("negotiate 响应过短: %d", len(resp))
	}
	c.PDULen = int(binary.BigEndian.Uint16(resp[25:27]))
	return c, nil
}

func (c *rawClient) Close() { c.conn.Close() }

func (c *rawClient) nextRef() uint16 { c.ref++; return c.ref }

func readRawFrame(conn net.Conn) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	frame := make([]byte, n)
	copy(frame, hdr)
	if _, err := io.ReadFull(conn, frame[4:]); err != nil {
		return nil, err
	}
	return frame, nil
}

// buildVarJob 构造读(0x04)/写(0x05) Job 请求。
// 参数: 功能码、区域、DB 号、字节偏移、位号、请求传输尺寸、字节/位数、写数据段尺寸、写数据。
func buildVarJob(fn byte, ref uint16, area byte, dbNo, offset, bit, ts, length int, dataTS byte, writeData []byte) []byte {
	addr := (offset << 3) | bit
	pdu := make([]byte, 0, 64)
	pdu = append(pdu, 0x32, 0x01, 0x00, 0x00)
	pdu = append(pdu, byte(ref>>8), byte(ref))
	dataLen := 0
	if fn == 0x05 {
		dataLen = 4 + len(writeData)
	}
	pdu = append(pdu, 0x00, 0x0E, byte(dataLen>>8), byte(dataLen))
	pdu = append(pdu, fn, 0x01)
	pdu = append(pdu, 0x12, 0x0A, 0x10, byte(ts))
	pdu = append(pdu, byte(length>>8), byte(length))
	pdu = append(pdu, byte(dbNo>>8), byte(dbNo))
	pdu = append(pdu, area)
	pdu = append(pdu, byte(addr>>16), byte(addr>>8), byte(addr))
	if fn == 0x05 {
		// 数据项长度单位与 plc4x DataTransportSize 一致：
		// BIT(0x03)/BYTE_WORD_DWORD(0x04)/INTEGER(0x05) 以位计，其余以字节计。
		length := len(writeData)
		if dataTS == tsResBit || dataTS == tsResByte || dataTS == tsResInt {
			length = len(writeData) * 8
			if dataTS == tsResBit {
				length = len(writeData) // BIT：位数为 1
			}
		}
		pdu = append(pdu, 0x00, dataTS, byte(length>>8), byte(length))
		pdu = append(pdu, writeData...)
	}
	total := 7 + len(pdu)
	frame := make([]byte, 0, total)
	frame = append(frame, 0x03, 0x00, byte(total>>8), byte(total), 0x02, 0xF0, 0x80)
	return append(frame, pdu...)
}

// readDB 按字节读取 DB 区域。
func (c *rawClient) readDB(dbNo, offset, size int) ([]byte, error) {
	req := buildVarJob(0x04, c.nextRef(), areaDB, dbNo, offset, 0, tsBy, size, 0, nil)
	if _, err := c.conn.Write(req); err != nil {
		return nil, err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return nil, err
	}
	return parseReadResp(resp)
}

// writeDB 按字节写入 DB 区域。
func (c *rawClient) writeDB(dbNo, offset int, data []byte) error {
	req := buildVarJob(0x05, c.nextRef(), areaDB, dbNo, offset, 0, tsBy, len(data), tsResByte, data)
	if _, err := c.conn.Write(req); err != nil {
		return err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return err
	}
	// PDU: header(10) + 错误(2) + params(05 00) + 返回码(1) @ frame[7+14]
	if len(resp) < 7+15 || resp[7+14] != 0xFF {
		return fmt.Errorf("写失败, 返回码 0x%02X, 响应: % X", resp[7+14], resp)
	}
	return nil
}

// writeArea 向任意区域写字节（用于测试不支持的区域）。
func (c *rawClient) writeArea(area byte, dbNo, offset int, data []byte) error {
	req := buildVarJob(0x05, c.nextRef(), area, dbNo, offset, 0, tsBy, len(data), tsResByte, data)
	if _, err := c.conn.Write(req); err != nil {
		return err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return err
	}
	if len(resp) < 7+15 || resp[7+14] != 0xFF {
		return fmt.Errorf("写失败, 返回码 0x%02X", resp[7+14])
	}
	return nil
}

func (c *rawClient) writeBit(dbNo, offset, bit int, on bool) error {
	v := byte(0)
	if on {
		v = 1
	}
	req := buildVarJob(0x05, c.nextRef(), areaDB, dbNo, offset, bit, tsBit, 1, tsResBit, []byte{v})
	if _, err := c.conn.Write(req); err != nil {
		return err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return err
	}
	if len(resp) < 7+15 || resp[7+14] != 0xFF {
		return fmt.Errorf("位写失败, 返回码 0x%02X, 响应: % X", resp[7+14], resp)
	}
	return nil
}

func (c *rawClient) readBit(dbNo, offset, bit int) (int, error) {
	req := buildVarJob(0x04, c.nextRef(), areaDB, dbNo, offset, bit, tsBit, 1, 0, nil)
	if _, err := c.conn.Write(req); err != nil {
		return 0, err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return 0, err
	}
	data, err := parseReadResp(resp)
	if err != nil {
		return 0, err
	}
	if len(data) < 1 {
		return 0, fmt.Errorf("位读无数据")
	}
	return int(data[0]), nil
}

// readDBTS 按指定传输尺寸读取 DB 区域（如 WORD/INT/DWORD/REAL）。
func (c *rawClient) readDBTS(dbNo, offset, ts, elements int) ([]byte, error) {
	req := buildVarJob(0x04, c.nextRef(), areaDB, dbNo, offset, 0, ts, elements, 0, nil)
	if _, err := c.conn.Write(req); err != nil {
		return nil, err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return nil, err
	}
	return parseReadResp(resp)
}

// readItemSpec 描述多 item 读请求中的一项。
type readItemSpec struct {
	db     int
	offset int
	ts     int
	length int
}

// readMulti 在单个 Read Var 请求中读取多个 item，返回拼接后的数据
// （自动处理非最后一项的奇数长度填充字节）。
func (c *rawClient) readMulti(items ...readItemSpec) ([]byte, error) {
	params := []byte{0x04, byte(len(items))}
	for _, it := range items {
		addr := it.offset << 3
		params = append(params, 0x12, 0x0A, 0x10, byte(it.ts),
			byte(it.length>>8), byte(it.length),
			byte(it.db>>8), byte(it.db), areaDB,
			byte(addr>>16), byte(addr>>8), byte(addr))
	}
	pdu := make([]byte, 0, 16)
	pdu = append(pdu, 0x32, 0x01, 0x00, 0x00)
	pdu = append(pdu, byte(c.nextRef()>>8), byte(c.ref))
	pdu = append(pdu, byte(len(params)>>8), byte(len(params)), 0x00, 0x00)
	pdu = append(pdu, params...)
	frame := append([]byte{0x03, 0x00, byte((7 + len(pdu)) >> 8), byte(7 + len(pdu)), 0x02, 0xF0, 0x80}, pdu...)
	if _, err := c.conn.Write(frame); err != nil {
		return nil, err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return nil, err
	}
	return parseReadMultiResp(resp, len(items))
}

// readSZL 发送 UserData Read SZL 请求，返回 SZL 数据段。
func (c *rawClient) readSZL(id, index uint16) ([]byte, error) {
	params := []byte{0x00, 0x01, 0x12, 0x08, 0x11, 0x44, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00}
	data := []byte{0x00, 0x09, 0x00, 0x04, byte(id >> 8), byte(id), byte(index >> 8), byte(index)}
	pdu := make([]byte, 0, 32)
	pdu = append(pdu, 0x32, 0x07, 0x00, 0x00)
	pdu = append(pdu, byte(c.nextRef()>>8), byte(c.ref))
	pdu = append(pdu, byte(len(params)>>8), byte(len(params)))
	pdu = append(pdu, byte(len(data)>>8), byte(len(data)))
	pdu = append(pdu, params...)
	pdu = append(pdu, data...)
	frame := append([]byte{0x03, 0x00, byte((7 + len(pdu)) >> 8), byte(7 + len(pdu)), 0x02, 0xF0, 0x80}, pdu...)
	if _, err := c.conn.Write(frame); err != nil {
		return nil, err
	}
	resp, err := readRawFrame(c.conn)
	if err != nil {
		return nil, err
	}
	// 响应帧: TPKT(4)+COTP(3)+PDU[header(10)+params(12)+data(FF 09 len:2 + szl)]
	if len(resp) < 7+10+12+8 {
		return nil, fmt.Errorf("SZL 响应过短: %d", len(resp))
	}
	off := 7 + 10 + 12
	if resp[off] != 0xFF || resp[off+1] != 0x09 {
		return nil, fmt.Errorf("SZL 数据项头错误: % X", resp[off:off+2])
	}
	n := int(binary.BigEndian.Uint16(resp[off+2:]))
	if len(resp) < off+4+n {
		return nil, fmt.Errorf("SZL 数据不完整: %d < %d", len(resp), off+4+n)
	}
	return resp[off+4 : off+4+n], nil
}

// parseReadResp 解析读响应，返回数据。
// PDU: header(10) + 错误(2) + params(04 01) + item[FF, ts, len(2), data...]
// 长度单位与 plc4x DataTransportSize 一致：
// BIT(0x03)/BYTE_WORD_DWORD(0x04)/INTEGER(0x05) 以位计，其余以字节计。
func parseReadResp(resp []byte) ([]byte, error) {
	if len(resp) < 7+15 {
		return nil, fmt.Errorf("读响应过短: %d", len(resp))
	}
	rc := resp[7+14]
	if rc != 0xFF {
		return nil, fmt.Errorf("读失败, 返回码 0x%02X", rc)
	}
	if len(resp) < 7+18 {
		return nil, fmt.Errorf("读响应过短: %d", len(resp))
	}
	ts := resp[7+15]
	n := int(binary.BigEndian.Uint16(resp[7+16 : 7+18]))
	switch ts {
	case tsResBit, tsResByte, tsResInt:
		n = (n + 7) / 8 // 以位计
	}
	if len(resp) < 7+18+n {
		return nil, fmt.Errorf("读响应数据不完整: %d < %d", len(resp), 7+18+n)
	}
	return resp[7+18 : 7+18+n], nil
}

// parseReadMultiResp 解析多 item 读响应，返回拼接后的数据
// （自动跳过非最后一项的奇数长度填充字节）。
func parseReadMultiResp(resp []byte, count int) ([]byte, error) {
	if len(resp) < 7+14 {
		return nil, fmt.Errorf("读响应过短: %d", len(resp))
	}
	if resp[7+10] != 0x00 || resp[7+11] != 0x00 {
		return nil, fmt.Errorf("PDU 错误类/码: % X", resp[7+10:7+12])
	}
	off := 7 + 14 // 第一个数据项（返回码）
	var out []byte
	for i := 0; i < count; i++ {
		if off+4 > len(resp) {
			return nil, fmt.Errorf("数据项 %d 头不完整", i)
		}
		if resp[off] != 0xFF {
			return nil, fmt.Errorf("item %d 返回码 0x%02X", i, resp[off])
		}
		ts := resp[off+1]
		n := int(binary.BigEndian.Uint16(resp[off+2:]))
		switch ts {
		case tsResBit, tsResByte, tsResInt:
			n = (n + 7) / 8 // 以位计
		}
		if off+4+n > len(resp) {
			return nil, fmt.Errorf("数据项 %d 数据不完整", i)
		}
		out = append(out, resp[off+4:off+4+n]...)
		off += 4 + n
		// 非最后一项的奇数长度填充
		if n%2 == 1 && i < count-1 {
			off++
		}
	}
	return out, nil
}
