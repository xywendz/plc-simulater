# PLC S7 协议模拟器

用 Go 实现的西门子 S7 通信（S7comm over ISO-on-TCP）**服务端模拟器**。
监听 TCP 端口（默认 102），接收客户端（TIA Portal、WinCC、snap7、libnodave、gos7、
plc4go 等）的**读写请求**，数据块（DB）通过 **YAML 配置文件**定义。

## 功能

- ISO-on-TCP 封装：TPKT + COTP（自动处理 TCP 粘包/拆包）
- COTP 连接握手（Connect Request / Confirm，引用与 TSAP 正确回显）与断开（DR/DC）
- S7 Setup Communication（0xF0）协商 PDU 长度
- **Read Var（0x04）**：按字节/字/双字/位读取，支持单请求多 item
- **Write Var（0x05）**：按字节/位写入，每 item 独立返回码
- **UserData（0x07）/ Read SZL**：应答模块标识（0x0011）、组件标识（0x001C）等系统状态列表请求
- 位寻址（DBXx.y）与字节寻址（DBB/DBW/DBD）
- 多客户端并发连接，DB 内存模型线程安全
- 通过配置文件初始化 DB 大小与初始值（bool/byte/word/dword/int/dint/real/string）

## 协议兼容性

实现参照 **apache/plc4x/plc4go** S7 客户端（`plc4go/internal/s7` 与
`plc4go/protocols/s7/readwrite/model`）并对齐 gos7、snap7 的报文习惯，要点：

- **数据项长度单位**（读响应/写请求共用结构）与 plc4x `DataTransportSize.SizeInBits()` 一致：
  BIT(0x03)/BYTE_WORD_DWORD(0x04)/INTEGER(0x05) 以**位**计，REAL(0x07)/OCTET_STRING(0x09) 以字节计。
- **填充字节**：非最后一项且数据长度为奇数时补一个 `0x00`（plc4x 的对齐规则）。
- **元素个数**：请求 item 的 length 是元素个数（WORD=2 字节/元素、REAL=4 字节/元素，同 gos7）。
- **错误码**（plc4x `DataTransportErrorCode`）：`0xFF` 成功、`0x0A` 对象不存在（DB 未配置/
  区域不支持）、`0x05` 地址越界、`0x06` 数据类型不支持。
- **UserData 参数块**：`parameterType(0x00) + numItems + item[itemLength + method(0x11/0x12)
  + type|group(0x44) + subfunction(0x01) + sequenceNumber + 可选字段]`，
  不校验 itemLength（plc4x 与真实 PLC 取值不同）。
- **COTP CC**：dstRef 回显 CR 的 srcRef（snap7 `IsoConfirmConnection` 的解析前提），
  TSAP 与请求对调，TPDU 大小 1024。
- **SZL 数据**：`LENTHDR(4) + N_DR(2) + 记录`，记录含 ASCII 订货号/型号名
  （plc4x `parseSzlProbeResponse` 扫描 "6ES7"/"6GK" 或 "CPU " 前缀）。

## 使用

```bash
# 构建
go build -o s7sim.exe .

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o plc-simulater

$env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="amd64";go build -o plc-simulater

# 运行（默认读取 config.yaml）
./s7sim.exe -config config.yaml

# 自测（另开终端）
go run ./cmd/s7test -addr 127.0.0.1:102
```

## 运行模式：Web 与 GUI 双模式

项目采用 **Wails 3**（参考 `d:\doit\db-fly`）改造为双模式：两个独立编译入口，共用同一套
Go 业务内核与同一套 TypeScript 前端组件，通过 Vite 编译期别名切换运行时实现。
两种模式都会启动 S7 TCP 监听，PLC 客户端访问方式完全一致。

| 模式 | Go 入口 | 前端产物 | 通信方式 |
|------|---------|----------|----------|
| Web / B-S | `cmd/plc-simulater-server` | `frontend/dist-web` → `internal/webui/dist`（`//go:embed`） | HTTP JSON-RPC `POST /api/rpc/{Method}` |
| Windows GUI | `cmd/plc-simulater-gui` | `frontend/dist` → `cmd/plc-simulater-gui/assets`（`//go:embed`） | Wails 3 绑定（`@wailsio/runtime`） |

前端通过 `@/runtime` 统一抽象层对接后端：`--mode web` 编译进 `runtime/web.ts`（fetch RPC），
`--mode gui` 编译进 `runtime/gui.ts`（Wails 绑定）。因此 Web 产物不含 Wails runtime，
GUI 产物不含 fetch RPC 代码；业务 API（`api/client.ts`）与 UI 组件完全一致。

### 构建与启动

```bash
# 前端开发（GUI 模板 / Web 模板，Vite HMR）
cd frontend && npm install
npm run dev          # GUI 模板，端口 34115（需配合 wails3 dev）
npm run dev:web      # Web 模板，/api 代理到 http://127.0.0.1:8070

# Web 服务（默认交叉编译 linux/amd64）
powershell -File ./build/web.ps1
$env:GOOS_TARGET='windows'; powershell -File ./build/web.ps1   # 编译 Windows 版
bash build/web.sh                                              # Linux/macOS

# Windows GUI 客户端（纯 Go，CGO_ENABLED=0，无需 gcc）
powershell -File ./build/gui.ps1

# 一键校验（Go build/vet/test + 前端类型检查 + 两套构建 + Linux 交叉编译）
powershell -File ./build/verify.ps1
```

产物位于 `dist/`：`plc-simulater-server`（或 `.exe`）与 `plc-simulater-gui.exe`。
也可使用 Task 入口：`task build`（GUI）、`task build:web`、`task build:all`、`task verify`。

应用图标以仓库根的 `logo.png` 为唯一来源，`build/gui.ps1` 每次构建时自动刷新：

- `frontend/public/logo.png`：页面 favicon 与头部展示（Vite 打包进两套前端产物）；
- `cmd/plc-simulater-gui/icon.png`：`//go:embed` 后作为 Wails 窗口/任务栏图标（`Options.Icon`）；
- `build/windows/icon.ico` + `cmd/plc-simulater-gui/rsrc_windows_*.syso`：
  由 `wails3 generate icons / syso` 生成，作为 exe 的资源管理器/任务栏图标与 DPI 清单。

Web 服务启动参数：

```bash
./dist/plc-simulater-server -config config.yaml -addr :8070
# -addr 覆盖配置文件中的 http_listen；http_listen 留空时 Web 模式默认 :8070
# 根入口 go run . / go build . 仍可用，等价于 Web 模式
```

GUI 从可执行文件同目录的 `config.yaml`（不存在则回落到工作目录）读取配置，
并在本机后台启动 S7 监听；窗口内嵌页面通过 Wails 绑定操作同一份数据。

### 新增业务方法时的同步清单

1. 在 `internal/bridge` 添加 JSON 可序列化的门面方法；
2. 在 `internal/httpapi/rpc.go` 的 `allowedMethods` 白名单中登记同名方法；
3. 在 `frontend/src/api/client.ts` 添加同名调用；
4. 执行 `npm run generate:bindings` 重新生成 GUI 绑定（不要手工编辑生成文件）。

### HTTP 管理界面

配置项 `http_listen` 指定 HTTP 服务监听地址（如 `:8080`，留空则不启用）。
配置项 `db_path` 指定内置 **SQLite** 数据库文件路径（默认 `plc-simulater.db`），
用于**持久化记录所有 DB 块及其字段定义（名称 / 偏移 / 类型 / 位号 / 长度 / 初始值）**，
重启后自动恢复，并作为字段定义的唯一真源。

启动后浏览器访问该地址即可打开内置静态页面，用于**浏览、查询、配置与修改 DB 块值**：

- **左侧列表**展示所有数据块（`DB号 · 名称 · 大小`）；点击列表项，右侧区域显示该 DB 的
  **字段名 / 类型 / 偏移量 / 位号 / 当前值**，并可直接修改字段值、增删字段、调整字段顺序；
- **左键点击**数据块：右侧加载并显示其字段信息；**右键点击**数据块：弹出菜单可
  **重命名 / 复制（克隆）副本 / 删除**该数据块；
- 未配置字段的 DB 展示原始字节十六进制，便于直接浏览；
- 提供「原始查询」面板，可按 DB 号 + 字节偏移 + 长度查询任意区域的十六进制；
- 「字段配置」面板可**动态设置 / 修改 DB 块的字段名称与类型**（bool/bit/byte/word/dword/int/dint/real/string），
  并支持字段**排序（↑/↓）与删除**；新建的 DB 与字段定义均持久化到 SQLite；
- 页面提供 **重新加载（Reload）** 功能：可单独重载某个 DB 或重载全部，
  从 SQLite 数据库重新读取数据块信息（字段元数据与初始值）并同步到内存；
- **字段偏移自动计算**：字段按「顺序 + 类型」进行位级打包，`bool`/`bit` 占 1 位并按位紧密排列，
  多字节类型（byte/word/dint/…）按字节对齐；**修改任意字段的类型 / 顺序 / 增删后，
  整块所有字段的偏移与位号会即时重新计算并持久化**，无需手工维护偏移；
- **字段名 / 类型 / 值三列均支持双击就地修改**：
  - 字段名：双击显示输入框，回车或失焦提交，Esc 取消；改名会保留原有顺序、类型、长度与值；
  - 类型：双击显示类型下拉框，选中即从预设类型中切换，偏移按新类型自动重算；
  - 值：`bool`/`bit` 显示复选框（勾选即提交），其余类型显示输入框（回车或失焦提交，Esc 取消）；
  修改会通过 S7 内存模型实时生效（同步写入内存 DB 块字节），与 S7 客户端读写共用同一份数据。
- **暗色 / 亮色双主题**：点击头部「主题」按钮切换，选择保存在浏览器/WebView 本地存储，
  下次打开自动恢复；页面加载前恢复主题以避免闪烁。
- **导出 / 导入**：
  - 左侧列表顶部「导出」按钮导出全部数据块；数据块右键菜单「导出此 DB」导出单个数据块；
    均下载为 JSON 文件（含 DB 号/名称/大小、字段定义与当前值的完整字节内容）；
  - 左侧列表顶部「导入」按钮选择 JSON 文件批量导入：兼容整体导出包
    （`{version, dbs:[...]}`）与单块导出文件（`{number, ...}`）；
    同号数据块会被覆盖（先删除再重建，随后恢复字段定义与字节内容）；
    任一块失败即中止并提示已导入数量，可用「↻ 重新加载全部」从 SQLite 恢复到导入前状态。

HTTP JSON 接口：

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/rpc/{Method}` | **Web 前端主通道**，请求体 `{"args":[...]}`，响应 `{"result":...}`；方法名见 `allowedMethods`（与 Wails 绑定同名） |
| GET  | `/api/dbs` | 所有 DB 视图（字段名/值/偏移，或原始十六进制） |
| GET  | `/api/db/{n}` | 单个 DB 视图 |
| GET  | `/api/db/{n}/fields` | 持久化的字段定义列表 |
| POST | `/api/db` | 动态创建 DB，请求体 `{"number":int,"size":int,"name"?}` |
| POST | `/api/db/clone` | 复制数据块，请求体 `{"src":int,"number":int,"name"?,"size"?}` |
| DELETE | `/api/db/{n}` | 删除数据块（含其字段定义） |
| POST | `/api/db/{n}/rename` | 修改 DB 名称，请求体 `{"name":string}` |
| POST | `/api/db/{n}/reload` | 从数据库重新加载该数据块到内存（恢复字段元数据与初始值） |
| POST | `/api/reload` | 从数据库重新加载全部数据块到内存 |
| POST | `/api/db/{n}/field` | 新增/修改字段，请求体 `{"name","type","length?","value?"}`（偏移自动计算） |
| DELETE | `/api/db/{n}/field/{name}` | 删除字段定义 |
| POST | `/api/db/{n}/move` | 调整字段顺序，请求体 `{"name":string,"dir":-1|1}` |
| GET  | `/api/raw?db={n}&offset={o}&len={l}` | 查询原始字节（十六进制） |
| POST | `/api/db/{n}/bit` | 修改位，请求体 `{"offset":int,"bit":int,"value":bool}` |
| POST | `/api/db/{n}/set` | 修改具名字段，请求体 `{"name":string,"value":any}` |

## 配置示例

```yaml
listen: "0.0.0.0:102"       # 监听地址
max_pdu_size: 240           # 协商的最大 PDU 长度

dbs:
  - number: 1
    name: "过程数据"        # 数据块名称（可选，便于辨识）
    size: 128               # 数据块字节数（省略时按字段自动计算）
    fields:                 # 可选：具名字段，用于初始化数据
      - { name: "TankLevel",   offset: 0,  type: real,  value: 12.5 }
      - { name: "PumpRunning", offset: 4,  type: bool,  bit: 0, value: true }
      - { name: "StatusWord",  offset: 8,  type: word,  value: 0x01FF }
      - { name: "Counter",     offset: 12, type: dword, value: 1000 }
      - { name: "Temperature", offset: 16, type: int,   value: -25 }
      - { name: "ProductName", offset: 32, type: string, length: 20, value: "Hello S7" }
```

支持的类型：`bool`/`bit`（配合 `bit` 号）、`byte`、`word`（uint16）、`dword`（uint32）、
`int`（int16）、`dint`（int32）、`real`（float32，大端 IEEE754）、`string`（需要 `length`）。
所有值均为大端序（与 S7/PLC 一致）。

## 客户端如何访问

| 客户端地址        | 说明                          |
|-------------------|-------------------------------|
| `DB1.DBB0`        | DB1 字节 0                    |
| `DB1.DBW8`        | DB1 字 8（2 字节，大端）      |
| `DB1.DBD12`       | DB1 双字 12（4 字节）         |
| `DB1.DBX4.0`      | DB1 字节 4 的位 0             |

> 当前仅支持 **DB 区（0x84）**。访问其他区域（I/Q/M/C/T）或未配置的 DB 会返回
> "对象不存在"（0x0A）错误，客户端表现为读/写失败。

## 协议说明

一条完整报文：

```
TPKT(4B) + COTP(3B) + S7 PDU
03 00 00 1F | 02 F0 80 | 32 01 ...
```

S7 PDU 头（10B）：`0x32` 协议 ID、ROSCTR、PDU 引用、参数长度、数据长度。
读请求 item（12B）：`12 0A 10 <TS> <长度> <DB号> <区域> <地址(3B)>`，
其中地址为大端 `(字节偏移 << 3) | 位号`，长度为**元素个数**。
读响应数据段每 item：`返回码(FF) + 传输尺寸 + 长度(2B) + 数据 [+ 填充]`。
写请求数据段每 item：`保留(00) + 传输尺寸 + 长度(2B) + 数据 [+ 填充]`。
写响应数据段：每 item 1 字节返回码（参数区为 `05 <item数>`）。

## 测试

```bash
# 运行全部单元测试
go test . ./config ./model ./s7 ./cmd/s7test

# 查看详细输出
go test -v ./s7

# 仅运行 plc4go 客户端集成测试
go test -run TestPlc4go -v ./s7
```

测试覆盖：

| 包 | 内容 |
|---|---|
| `config` | 配置加载、默认值、重复 DB 号/非法 DB 号/空 DB 列表等错误分支 |
| `model` | DB 创建与自动扩容、字段初始值（bool/位/字/双字/int/real/string）、读写边界、位操作、读隔离、并发安全 |
| `s7` | TPKT/COTP/S7 PDU 编解码（含 TCP 粘包）、S7ANY item 解析、传输尺寸映射、数据项长度单位与填充字节、Setup/Read/Write/UserData 处理、服务端集成测试（握手、类型化读写、多 item、错误码、SZL、并发客户端、断开连接、DB 长度边界） |

### plc4go 客户端集成测试

`s7/plc4go_client_test.go` 使用 **github.com/apache/plc4x/plc4go** 作为真实客户端
（`go.mod` 直接依赖，仅测试使用），在临时端口启动服务端后验证：

- `s7://127.0.0.1:<port>?remote-rack=0&remote-slot=2` 连接（含 COTP 握手、
  Setup Communication、`identifyRemote` 的 Read SZL 探测）
- 读取 `%DB1.DBD0:REAL`、`%DB1.DBW8:WORD`、`%DB1.DBX4.0:BOOL`、
  `%DB1.DBD12:DWORD`、`%DB1.DBW16:INT`、`%DB1.DBB0:BYTE` 等类型化访问
- 写入后读回（REAL/DWORD/INT/WORD/BOOL，含位写不影响同字节其他位）
- 单请求多 item（含奇数长度项，验证填充字节不影响 plc4go 顺序解析）
- 错误场景：未配置 DB / 越界地址 -> plc4x `INVALID_ADDRESS`
- 连接元数据：SZL 识别出的订货号/控制器类型/PDU 大小

连接成功后 `GetMetadata().GetConnectionAttributes()` 形如：

```
map[article-number:CPU 315-2 PN/DP controller-type:S7_300
    max-amq-callee:8 max-amq-caller:8 pdu-size:240]
```

## 项目结构

```
main.go                              兼容入口（等价于 Web 模式）
cmd/plc-simulater-server/main.go     Web/B-S 模式入口（纯 Go）
cmd/plc-simulater-gui/main.go        Windows GUI 入口（wails3，go:build windows）
cmd/plc-simulater-gui/stub.go        非 Windows 占位入口
cmd/s7test/                          自测客户端（原始报文）
internal/bridge/                     业务门面（GUI 绑定与 Web RPC 共用）
internal/bootstrap/                  配置加载、内核创建、S7 监听启动
internal/httpapi/                    Web RPC 分发 + 静态资源 + 旧 REST 兼容
internal/webui/                      Web 前端嵌入（//go:embed dist）
frontend/                            Vite + TypeScript 前端（双模板）
frontend/src/runtime/                gui.ts / web.ts 运行时抽象
frontend/src/api/client.ts           统一业务调用层
frontend/src/bindings/               wails3 生成的绑定（勿手改）
config/config.go                     YAML 配置解析
model/db.go                          内存 DB 块（线程安全）
store/store.go                       内置 SQLite 持久化
s7/pdu.go                            TPKT/COTP/S7 PDU 编解码
s7/server.go                         TCP 服务器、握手、SQLite 接入
s7/handler.go                        Setup/Read/Write/UserData 处理
s7/webui.go                          旧 REST 接口（Web 模式继续兼容）
build/                               gui.ps1 / web.ps1 / web.sh / verify.ps1
wails.json, Taskfile.yml             wails3 配置与任务入口
config.yaml                          示例配置
```

> 注：工作区中的 `pkg/` 目录是本地模块缓存副本，不属于本模块源码；其中放置了
> `pkg/go.mod` 作为嵌套模块边界，避免 `go build ./...` / `go mod tidy` 扫描缓存副本。
