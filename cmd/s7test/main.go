// s7test 是一个针对 S7 模拟器的自测客户端。
// 用原始 S7 报文完成握手并执行字节/位读写，验证协议兼容性。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
)

func check(err error) {
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:102", "模拟器地址")
	flag.Parse()

	c, err := dialRaw(*addr)
	check(err)
	defer c.Close()
	fmt.Printf("已连接 %s，协商 PDU 长度: %d\n", *addr, c.PDULen)

	// ---- 读 DB1 前 32 字节 ----
	buf, err := c.readDB(1, 0, 32)
	check(err)
	fmt.Printf("DB1[0:32] = % X\n", buf)
	if len(buf) >= 4 {
		fmt.Printf("  TankLevel(REAL@0)  = %.2f\n", math.Float32frombits(binary.BigEndian.Uint32(buf[0:4])))
	}
	if len(buf) >= 8 {
		fmt.Printf("  PumpRunning(BOOL@4.0) = %v\n", buf[4]&0x01 != 0)
		fmt.Printf("  StatusWord(WORD@8)    = 0x%04X\n", binary.BigEndian.Uint16(buf[8:10]))
	}
	if len(buf) >= 14 {
		fmt.Printf("  Counter(DWORD@12)     = %d\n", binary.BigEndian.Uint32(buf[12:16]))
	}
	if len(buf) >= 18 {
		fmt.Printf("  Temperature(INT@16)   = %d\n", int16(binary.BigEndian.Uint16(buf[16:18])))
	}

	// ---- 写 DB1 偏移 40，4 字节并读回 ----
	check(c.writeDB(1, 40, []byte{0xDE, 0xAD, 0xBE, 0xEF}))
	buf2, err := c.readDB(1, 40, 4)
	check(err)
	fmt.Printf("写入并读回 DB1[40:44] = % X (期望 DE AD BE EF)\n", buf2)

	// ---- 写 MB 区（模拟器仅支持 DB，应失败）----
	if err := c.writeArea(areaMK, 0, 0, []byte{0xAA}); err != nil {
		fmt.Printf("写 MB 区被拒绝（预期行为）: %v\n", err)
	} else {
		fmt.Printf("警告: MB 区写意外成功\n")
	}

	// ---- 读不存在的 DB99，应失败 ----
	if _, err := c.readDB(99, 0, 4); err != nil {
		fmt.Printf("读 DB99 被拒绝（预期行为）: %v\n", err)
	} else {
		fmt.Printf("警告: DB99 读意外成功\n")
	}

	// ---- 位读写 DB1.DBX5.3 ----
	check(c.writeBit(1, 5, 3, true))
	v, err := c.readBit(1, 5, 3)
	check(err)
	fmt.Printf("DB1.DBX5.3 写 1 后读回 = %d\n", v)
	check(c.writeBit(1, 5, 3, false))
	v, err = c.readBit(1, 5, 3)
	check(err)
	fmt.Printf("DB1.DBX5.3 写 0 后读回 = %d\n", v)

	// ---- WORD / INT / DWORD / REAL 类型化读取 ----
	w, err := c.readDBTS(1, 8, tsWord, 2)
	check(err)
	fmt.Printf("DB1.DBW8 (WORD x2) = % X\n", w)
	i, err := c.readDBTS(1, 16, tsInt, 1)
	check(err)
	fmt.Printf("DB1.DBW16 (INT)    = %d\n", int16(binary.BigEndian.Uint16(i)))
	d, err := c.readDBTS(1, 12, tsDword, 1)
	check(err)
	fmt.Printf("DB1.DBD12 (DWORD)  = %d\n", binary.BigEndian.Uint32(d))
	r, err := c.readDBTS(1, 0, tsReal, 1)
	check(err)
	fmt.Printf("DB1.DBD0 (REAL)    = %.3f\n", math.Float32frombits(binary.BigEndian.Uint32(r)))

	// ---- 多 item 单请求读取（含奇数长度填充字节） ----
	multi, err := c.readMulti(
		readItemSpec{db: 1, offset: 0, ts: tsBy, length: 4},
		readItemSpec{db: 1, offset: 8, ts: tsBy, length: 3},
		readItemSpec{db: 1, offset: 12, ts: tsBy, length: 2},
	)
	check(err)
	fmt.Printf("多 item 读（4+3+2 字节）= % X\n", multi)

	// ---- UserData Read SZL：CPU 标识 ----
	szl, err := c.readSZL(0x001C, 0x0001)
	check(err)
	fmt.Printf("SZL 0x001C[0x0001] = % X\n", szl)

	// ---- 越界读，应失败 ----
	if _, err := c.readDB(1, 1000, 4); err != nil {
		fmt.Printf("越界读被拒绝（预期行为）: %v\n", err)
	}
}
