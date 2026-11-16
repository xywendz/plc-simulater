package s7

import (
	"encoding/binary"
	"errors"
)

var (
	ErrShortFrame    = errors.New("s7: frame too short")
	ErrBadTPKT       = errors.New("s7: bad TPKT header")
	ErrBadCOTP       = errors.New("s7: bad COTP header")
	ErrBadPDU        = errors.New("s7: bad S7 PDU header")
	ErrCOTPNotData   = errors.New("s7: COTP packet is not a data PDU")
	ErrFrameTooLarge = errors.New("s7: frame too large")
)

// 单条 TPKT 报文的长度上限。
const maxFrameLen = 2048

// Frame 表示一条完整的 ISO-on-TCP 报文。
type Frame struct {
	Raw []byte // 完整帧
	PDU []byte // S7 PDU（TPKT+COTP 之后的内容）
}

// ParseFrame 解析一条 TPKT+COTP+S7 报文。
func ParseFrame(raw []byte) (*Frame, error) {
	if len(raw) < 7 {
		return nil, ErrShortFrame
	}
	if raw[0] != 0x03 || raw[1] != 0x00 {
		return nil, ErrBadTPKT
	}
	n := int(binary.BigEndian.Uint16(raw[2:]))
	if n < 7 || n > len(raw) {
		return nil, ErrBadTPKT
	}
	// COTP 数据 PDU 有两种形式：
	//   LI=2: 02 F0 80           -> 数据从偏移 7 开始
	//   LI=4: 02 F0 80 00 <len2> -> 数据从偏移 9 开始
	var pdu []byte
	switch raw[4] {
	case 0x02:
		if n < 7 || raw[5] != COTPData {
			return nil, ErrBadCOTP
		}
		pdu = raw[7:n]
	case 0x04:
		if n < 9 {
			return nil, ErrBadCOTP
		}
		pdu = raw[9:n]
	default:
		return nil, ErrCOTPNotData
	}
	return &Frame{Raw: raw[:n], PDU: pdu}, nil
}

// ReadFrame 从连接上读取一条完整的 TPKT 报文（处理 TCP 粘包/拆包）。
func ReadFrame(conn interface{ Read([]byte) (int, error) }) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := ioReadFull(conn, hdr); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	if n < 7 {
		return nil, ErrBadTPKT
	}
	if n > maxFrameLen {
		return nil, ErrFrameTooLarge
	}
	frame := make([]byte, n)
	copy(frame, hdr)
	if _, err := ioReadFull(conn, frame[4:]); err != nil {
		return nil, err
	}
	return frame, nil
}

// ioReadFull 读取足够的字节。
func ioReadFull(conn interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// BuildFrame 将 S7 PDU 封装为 TPKT + COTP(02 F0 80) 报文。
func BuildFrame(pdu []byte) []byte {
	total := 7 + len(pdu)
	out := make([]byte, total)
	out[0], out[1] = 0x03, 0x00
	binary.BigEndian.PutUint16(out[2:], uint16(total))
	out[4], out[5], out[6] = 0x02, COTPData, 0x80
	copy(out[7:], pdu)
	return out
}

// PDUHeader 是 S7 PDU 头。
type PDUHeader struct {
	ROSCTR   byte
	Ref      uint16
	ParamLen int
	DataLen  int
}

// ParsePDUHeader 解析 S7 PDU 头（10 字节）。
func ParsePDUHeader(pdu []byte) (PDUHeader, error) {
	var h PDUHeader
	if len(pdu) < 10 || pdu[0] != 0x32 {
		return h, ErrBadPDU
	}
	h.ROSCTR = pdu[1]
	h.Ref = binary.BigEndian.Uint16(pdu[4:])
	h.ParamLen = int(binary.BigEndian.Uint16(pdu[6:]))
	h.DataLen = int(binary.BigEndian.Uint16(pdu[8:]))
	return h, nil
}

// BuildJob 构造 Job 请求 PDU：
// header(10) + 参数 + 数据（Job 无错误类/码字段，与 Ack-Data 的区别）。
func BuildJob(ref uint16, params, data []byte) []byte {
	pdu := make([]byte, 0, 10+len(params)+len(data))
	pdu = append(pdu, 0x32, ROSCTRJob, 0x00, 0x00)
	pdu = append(pdu, byte(ref>>8), byte(ref))
	pdu = append(pdu, byte(len(params)>>8), byte(len(params)))
	pdu = append(pdu, byte(len(data)>>8), byte(len(data)))
	pdu = append(pdu, params...)
	pdu = append(pdu, data...)
	return pdu
}

// BuildAckData 构造 Ack-Data 响应 PDU：
// header(10) + 错误类/码(2) + 参数 + 数据。
func BuildAckData(ref uint16, params, data []byte) []byte {
	pdu := make([]byte, 0, 10+2+len(params)+len(data))
	pdu = append(pdu, 0x32, ROSCTRAckData, 0x00, 0x00)
	pdu = append(pdu, byte(ref>>8), byte(ref))
	pdu = append(pdu, byte(len(params)>>8), byte(len(params)))
	pdu = append(pdu, byte(len(data)>>8), byte(len(data)))
	pdu = append(pdu, 0x00, 0x00) // 错误类/码：无错误
	pdu = append(pdu, params...)
	pdu = append(pdu, data...)
	return pdu
}

// BuildUserData 构造 UserData (0x07) 响应 PDU。
// 用于 CPU 功能（如 Read SZL）等 UserData 请求的应答；
// 参数区由调用方提供（通常为 12 字节，含序号/错误码字段）。
func BuildUserData(ref uint16, params, data []byte) []byte {
	pdu := make([]byte, 0, 10+len(params)+len(data))
	pdu = append(pdu, 0x32, ROSCTRUser, 0x00, 0x00)
	pdu = append(pdu, byte(ref>>8), byte(ref))
	pdu = append(pdu, byte(len(params)>>8), byte(len(params)))
	pdu = append(pdu, byte(len(data)>>8), byte(len(data)))
	pdu = append(pdu, params...)
	pdu = append(pdu, data...)
	return pdu
}
