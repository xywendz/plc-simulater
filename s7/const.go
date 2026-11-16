package s7

// S7ANY 区域类型（S7comm 规范，读写 item 中的区域字段）。
const (
	AreaPE = 0x81 // 输入映像 I (Process Input)
	AreaPA = 0x82 // 输出映像 Q (Process Output)
	AreaMK = 0x83 // 位存储区 M (Flags/Markers)
	AreaDB = 0x84 // 数据块 DB
	AreaCT = 0x1C // 计数器 C
	AreaTM = 0x1D // 定时器 T
)

// 请求 item 中的传输尺寸（S7ANY transport size，gos7 s7wl*）。
// 这是客户端请求读写时使用的编码；每个元素占用的字节数见 elementSize()。
const (
	TSBit   = 0x01 // BIT：位寻址，长度为位数
	TSByte  = 0x02 // BYTE：每元素 1 字节
	TSChar  = 0x03 // CHAR：每元素 1 字节（plc4x 将 STRING 也归入此类）
	TSWord  = 0x04 // WORD：每元素 2 字节
	TSInt   = 0x05 // INT：每元素 2 字节
	TSDWord = 0x06 // DWORD：每元素 4 字节
	TSDInt  = 0x07 // DINT：每元素 4 字节
	TSReal  = 0x08 // REAL：每元素 4 字节
	TSDate  = 0x09 // DATE：每元素 2 字节
	TSTod   = 0x0A // TIME_OF_DAY：每元素 4 字节
	TSTime  = 0x0B // TIME：每元素 4 字节
	TSS5    = 0x0C // S5TIME：每元素 2 字节
)

// 响应数据区中的传输尺寸（plc4x DataTransportSize，gos7 tsRes*）。
// 与请求侧编码不同：例如 WORD/DWORD 请求的响应统一为 0x04 (BYTE_WORD_DWORD)。
const (
	TSResNull   = 0x00 // NULL：无数据（错误项）
	TSResBit    = 0x03 // BIT（长度以位计）
	TSResByte   = 0x04 // BYTE/WORD/DWORD（统一，长度以位计）
	TSResInt    = 0x05 // INT/DINT（统一，长度以位计）
	TSResDInt   = 0x06 // DINTEGER（长度以字节计）
	TSResReal   = 0x07 // REAL（长度以字节计）
	TSResOctet  = 0x09 // OCTET_STRING（COUNTER/TIMER/STRING 等，长度以字节计）
)

// 响应/请求数据项的返回码（plc4x DataTransportErrorCode）。
// 注意与旧实现的差异：0x0A 表示"对象不存在"（如 DB 号未配置），
// 0x05 表示"地址越界"，0x06 表示"数据类型不支持"。
const (
	ResOK                 = 0xFF // 成功
	ResAccessDenied       = 0x03 // 访问不允许
	ResObjectNotAvailable = 0x05 // 地址越界/无效地址
	ResDataTypeNotsupp    = 0x06 // 数据类型不支持
	ResObjectNotFound     = 0x0A // 对象不存在（如 DB 号未配置）
)

// ROSCTR。
const (
	ROSCTRJob     = 0x01 // Job（请求）
	ROSCTRACK     = 0x02 // Ack（无数据确认）
	ROSCTRAckData = 0x03 // Ack-Data（带数据确认/响应）
	ROSCTRUser    = 0x07 // UserData（CPU 功能，如 Read SZL）
)

// 功能码。
const (
	FuncRead  = 0x04 // Read Var
	FuncWrite = 0x05 // Write Var
	FuncSetup = 0xF0 // Setup Communication
)

// COTP 类型码（ISO 8073）。
const (
	COTPData = 0xF0 // Data (DT) TPDU
	COTPCR   = 0xE0 // Connect Request (CR)
	COTPCC   = 0xD0 // Connect Confirm (CC)
	COTPDR   = 0x80 // Disconnect Request (DR)
	COTPDC   = 0xC0 // Disconnect Confirm (DC)
)

// UserData（CPU 功能请求）参数区常量。
// type|group 字节的高 4 位为 CPU 功能类型（0x4=请求、0x8=应答），
// 低 4 位为功能组（0x4=SZL）；plc4x 按此字节将负载分派到请求/响应子类型。
const (
	UDItemTypeCPU     = 0x12 // item 类型：CPU 功能
	UDMethodReadSZL   = 0x11 // 方法：Read SZL（请求）
	UDSubReadSZL      = 0x01 // 子功能：Read SZL
	UDTypeGroupSZLReq = 0x44 // 请求：类型 0x4 | 组 0x4
	UDTypeGroupSZLRsp = 0x84 // 应答：类型 0x8 | 组 0x4
)

// 常见 SZL ID（系统状态列表标识）。
const (
	SzlModuleIdentification    = 0x0011 // 模块标识（旧版，plc4x 作为回退）
	SzlComponentIdentification = 0x001C // 组件标识（plc4x 优先探测）
)
