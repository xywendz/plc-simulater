package s7

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestParseFrame(t *testing.T) {
	pdu := []byte{0x32, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x02, 0x00, 0x00}
	frame := BuildFrame(pdu)
	f, err := ParseFrame(frame)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if !bytes.Equal(f.PDU, pdu) {
		t.Errorf("PDU = % X, want % X", f.PDU, pdu)
	}
	if !bytes.Equal(f.Raw, frame) {
		t.Errorf("Raw mismatch")
	}
	if len(f.Raw) != 7+len(pdu) {
		t.Errorf("len(Raw) = %d, want %d", len(f.Raw), 7+len(pdu))
	}
}

func TestParseFrameCOTPLengthVariants(t *testing.T) {
	// LI=4（02 F0 80 00 <len:2>）形式：PDU 从偏移 9 开始
	pdu := []byte{0x32, 0x07, 0, 0, 0, 1, 0, 0, 0, 0}
	frame := append([]byte{0x03, 0x00, 0x00, byte(9 + len(pdu)), 0x04, 0xF0, 0x80, 0x00, 0x00}, pdu...)
	f, err := ParseFrame(frame)
	if err != nil {
		t.Fatalf("ParseFrame(LI=4): %v", err)
	}
	if !bytes.Equal(f.PDU, pdu) {
		t.Errorf("PDU = % X, want % X", f.PDU, pdu)
	}
}

func TestParseFrameErrors(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
	}{
		{"too short", []byte{0x03, 0x00}},
		{"bad version", []byte{0x02, 0x00, 0x00, 0x0B, 0x02, 0xF0, 0x80, 0x32, 0x03, 0, 0}},
		{"length below min", []byte{0x03, 0x00, 0x00, 0x06, 0x02, 0xF0, 0x80}},
		{"length beyond buffer", []byte{0x03, 0x00, 0x00, 0x64, 0x02, 0xF0, 0x80, 0x32, 0x03, 0, 0}},
		{"bad cotp type", []byte{0x03, 0x00, 0x00, 0x07, 0x02, 0xE0, 0x80}},
		{"bad cotp data", []byte{0x03, 0x00, 0x00, 0x07, 0x02, 0xF1, 0x80}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseFrame(tc.frame); err == nil {
				t.Errorf("ParseFrame(%s) = nil error, want error", tc.name)
			}
		})
	}
}

func TestBuildFrameRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 7, 100, 500} {
		pdu := bytes.Repeat([]byte{0xAB}, n)
		frame := BuildFrame(pdu)
		f, err := ParseFrame(frame)
		if err != nil {
			t.Fatalf("n=%d ParseFrame: %v", n, err)
		}
		if !bytes.Equal(f.PDU, pdu) {
			t.Errorf("n=%d PDU mismatch", n)
		}
		if int(binary.BigEndian.Uint16(frame[2:])) != len(frame) {
			t.Errorf("n=%d TPKT length = %d, want %d", n, binary.BigEndian.Uint16(frame[2:]), len(frame))
		}
	}
}

// TestReadFrameStickyPackets 验证 TCP 粘包场景：数据分多段到达，
// ReadFrame 必须能逐条取出完整报文。
func TestReadFrameStickyPackets(t *testing.T) {
	pdu1 := []byte{0x32, 0x03, 0, 0, 0, 1, 0, 0, 0, 0}
	pdu2 := []byte{0x32, 0x07, 0, 0, 0, 2, 0, 0, 0, 0}
	blob := append(BuildFrame(pdu1), BuildFrame(pdu2)...)

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		// 分两段写入，模拟 TCP 拆包
		client.Write(blob[:10])
		client.Write(blob[10:])
	}()

	f1, err := ReadFrame(server)
	if err != nil {
		t.Fatalf("ReadFrame 1: %v", err)
	}
	if !bytes.Equal(f1, BuildFrame(pdu1)) {
		t.Errorf("frame 1 = % X, want % X", f1, BuildFrame(pdu1))
	}
	f2, err := ReadFrame(server)
	if err != nil {
		t.Fatalf("ReadFrame 2: %v", err)
	}
	if !bytes.Equal(f2, BuildFrame(pdu2)) {
		t.Errorf("frame 2 = % X, want % X", f2, BuildFrame(pdu2))
	}
}

func TestReadFrameErrors(t *testing.T) {
	t.Run("bad tpkt length", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		go client.Write([]byte{0x03, 0x00, 0x00, 0x03, 0x02, 0xF0, 0x80})
		if _, err := ReadFrame(server); err == nil {
			t.Error("ReadFrame = nil error, want error")
		}
	})
	t.Run("frame too large", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		go client.Write([]byte{0x03, 0x00, 0xFF, 0xFF, 0x02, 0xF0, 0x80})
		if _, err := ReadFrame(server); err != ErrFrameTooLarge {
			t.Errorf("ReadFrame err = %v, want ErrFrameTooLarge", err)
		}
	})
}

func TestPDUHeader(t *testing.T) {
	pdu := []byte{0x32, 0x01, 0x00, 0x00, 0x12, 0x34, 0x00, 0x0E, 0x00, 0x04, 0x04, 0x01}
	h, err := ParsePDUHeader(pdu)
	if err != nil {
		t.Fatalf("ParsePDUHeader: %v", err)
	}
	if h.ROSCTR != ROSCTRJob || h.Ref != 0x1234 || h.ParamLen != 14 || h.DataLen != 4 {
		t.Errorf("header = %+v", h)
	}
	if _, err := ParsePDUHeader(pdu[:9]); err != ErrBadPDU {
		t.Errorf("short PDU err = %v, want ErrBadPDU", err)
	}
	if _, err := ParsePDUHeader([]byte{0x31, 0x01, 0, 0, 0, 1, 0, 0, 0, 0}); err != ErrBadPDU {
		t.Errorf("bad protocol id err = %v, want ErrBadPDU", err)
	}
}

func TestBuildAckData(t *testing.T) {
	params := []byte{FuncRead, 0x02}
	data := []byte{0xFF, 0x04, 0x00, 0x08, 1, 2, 3, 4}
	pdu := BuildAckData(0x0007, params, data)
	if pdu[0] != 0x32 || pdu[1] != ROSCTRAckData {
		t.Errorf("rosctr bytes = % X", pdu[:2])
	}
	if binary.BigEndian.Uint16(pdu[4:]) != 0x0007 {
		t.Errorf("ref = 0x%04X, want 0x0007", binary.BigEndian.Uint16(pdu[4:]))
	}
	if int(binary.BigEndian.Uint16(pdu[6:])) != len(params) {
		t.Errorf("param len = %d, want %d", binary.BigEndian.Uint16(pdu[6:]), len(params))
	}
	if int(binary.BigEndian.Uint16(pdu[8:])) != len(data) {
		t.Errorf("data len = %d, want %d", binary.BigEndian.Uint16(pdu[8:]), len(data))
	}
	if pdu[10] != 0 || pdu[11] != 0 {
		t.Errorf("error class/code = % X, want 00 00", pdu[10:12])
	}
	if !bytes.Equal(pdu[12:], append(append([]byte{}, params...), data...)) {
		t.Errorf("payload mismatch: % X", pdu[12:])
	}
}

func TestBuildUserData(t *testing.T) {
	params := []byte{0x00, 0x01, 0x12, 0x08, 0x12, 0x44, 0x01, 0x00, 0, 0, 0, 0}
	data := []byte{0xFF, 0x09, 0x00, 0x04, 1, 2, 3, 4}
	pdu := BuildUserData(0x0100, params, data)
	if pdu[1] != ROSCTRUser {
		t.Errorf("rosctr = 0x%02X, want 0x%02X", pdu[1], ROSCTRUser)
	}
	if int(binary.BigEndian.Uint16(pdu[6:])) != len(params) {
		t.Errorf("param len = %d, want %d", binary.BigEndian.Uint16(pdu[6:]), len(params))
	}
	if int(binary.BigEndian.Uint16(pdu[8:])) != len(data) {
		t.Errorf("data len = %d, want %d", binary.BigEndian.Uint16(pdu[8:]), len(data))
	}
}
