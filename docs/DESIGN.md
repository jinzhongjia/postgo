# PostGo 设计方案：基于 MyGo 的 API 客户端（对标 Postman）

> 调研对象：github.com/egoist/mygo @ `690dbf4`（v0.7.4，2026-11-01）
> 本机环境：Go 1.27.3（满足 MyGo 要求）

---

## 1. MyGo 调研结论

### 1.1 它是什么

纯 Go（**无 cgo**）的桌面框架，每个窗口可以是两种界面之一（同一个窗口里也能混用）：

| 模式 | 渲染 | 适合 |
|---|---|---|
| **Web 页面** | 系统 WebView（WKWebView / WebKitGTK / WebView2），Go ↔ TS 类型化 IPC（`mygo.Bind`，自动生成 TS 客户端） | 复用 Web 生态 |
| **Native UI**（`package ui`） | MyGo 自己用 GPU 绘制（Metal / D3D11 / OpenGL），即时模式风格的 Go API，不带 HTML/JS | 工具类、需要秒开和低内存的应用 |

产物是一个几 MB 的单二进制文件，任何机器都能交叉编译到所有平台；打包、签名、公证、自动更新（含增量更新）都内置了。

### 1.2 和 API 客户端直接相关的能力

| 需求 | MyGo 提供的能力 | 说明 |
|---|---|---|
| 请求体/响应体编辑器 | `ui/editor`（仿 Zed） | 已内置 JSON、GraphQL、XML、HTML、YAML 等语言高亮，支持多光标、折叠、查找替换、补全、诊断和 hover，**百万行文件也能流畅编辑** |
| 集合树 | `ui.Outline` / `ui.TreeBase` | 虚拟化，支持拖拽重排 |
| JSON 树状查看 | `ui.TreeBase`（文档里正好拿 JSON viewer 举例） | |
| Headers/Params 表格 | `ui.Table` | 列可排序、可拖宽度、可选单元格 |
| 布局 | `Split`、`Tabs`、`Sidebar`、`Toolbar`、`Dialog`、`Popover`、`Toast` | |
| 方法选择 / 变量补全 | `Select`、`Combobox`、`Autocomplete` | |
| HTML 响应预览 | Native UI 里嵌 `Window.NewWebView` | Linux 上需要 WebKitGTK，做成可选功能 |
| 响应 diff | `ui/editor` 的 diff viewer | 用于对比两次响应 |
| Markdown 文档 | `ui/markdown` | 集合/请求的说明文档 |
| 计时图表 | `ui/chart` | 用于 Runner 统计 |
| 系统代理 | `netproxy.Transport()` | 支持 PAC/WPAD 和环境变量 |
| 本地存储 | `plugins/sqlite`（纯 Go，通过 purego 加载） | |
| 凭据 | `mygo.Secrets`（Keychain / Secret Service / Credential Manager） | 单条上限 2560 字节 |
| 文件监听 | `mygo.Watch` / `plugins/watch` | 外部（git pull、编辑器）改了集合文件时自动重载 |
| OAuth 回调 | deep links | 自定义 URL scheme |
| 测试 | `ui.NewTester` | 不开窗口就能跑 UI 单测；`plugins/automation` 用于 E2E |

### 1.3 风险

- **非常年轻**：仓库 2026-09 才创建，目前 v0.7.x，有 101 个 open issue，API 还在变（已经有 `migration.md`）。
- **要求 Go ≥ 1.27.3**。
- Native UI 模式下没有现成的 Web 组件生态，所有界面都要用 Go 自己写。

**应对**：核心逻辑（模型、存储、HTTP 引擎、脚本）**完全不依赖 mygo**，UI 层只做薄薄一层。这样即使 MyGo 出问题，也能换成 WebView 前端或别的 GUI，CLI 也能复用同一套核心。

---

## 2. 关键决策

### 决策 1：选 Native UI，不走 WebView + React ✅

- 差异化定位：Postman 是 Electron，启动慢、内存占用常常 500MB 以上。"秒开、几十 MB 内存、单文件"是这个项目最有力的卖点。
- MyGo 的编辑器、表格、大纲正好覆盖 API 客户端 90% 的界面需求，大响应体的虚拟化渲染也比 WebView 里的 Monaco 更轻。
- 全栈都是 Go：网络、脚本、UI 用同一种语言，状态不需要跨 IPC 序列化。
- 代价：UI 迭代比 React 慢；少数场景（HTML 预览）要嵌 WebView。

### 决策 2：集合存成自定义的 `.postgo` 文本格式，用 git 协作，不做云同步 ✅（已确认）

- 磁盘格式是**自定义的 `.postgo` 文本格式**，不用 YAML、JSON 或 TOML，详见 3.3 和 [FORMAT.md](FORMAT.md)。一个请求一个文件，diff 友好，团队通过 git 协作。
- 暂时不做云同步、账号和团队空间。架构上不预留同步层，也不做相关设计。
- SQLite 只存**派生或私有数据**：历史记录、响应缓存、Cookie Jar、UI 状态。
- 敏感变量不落盘到集合目录：值存在 `mygo.Secrets`（过长的话用 Secrets 里的密钥加密后写入用户数据目录）。

### 决策 3：脚本用 goja（纯 Go 的 JS 引擎），API 由我们自己设计：`pg` / `req` / `res`

- 原生 API 自己设计，不被任何第三方的 API 绑住：`pg.env`、`pg.vars`、`pg.collection`、`pg.sendRequest`、`pg.setNextRequest`，加上 `req`、`res`、`test()` 和 `expect()`。同时提供 `.d.ts` 类型声明，脚本编辑器可以据此补全。
- 纯 Go，符合"无 cgo"约束。
- 沙箱：每次执行都用新的 runtime，设超时（`Interrupt`），不开放文件和网络（`pg.sendRequest` 走我们自己的引擎）。
- **Postman 兼容放在最后做**：`pm.*` API 适配层和 Postman 导入导出单独拆成模块 P1，排在所有模块之后（见 5.4）。核心模块不为 Postman 预留任何特殊逻辑。

### 决策 4：macOS、Linux、Windows 三平台同等支持 ✅（已确认）

- 三个平台都是一等公民，不存在"首发平台"：每个模块的 DoD 都要求在三平台的 CI 上通过。
- CI 矩阵：`macos-latest`、`ubuntu-latest`、`windows-latest`，跑 `go build` / `vet` / `test`，加上 `ui.NewTester` 视图测试。E2E 测试（`plugins/automation`）也在三个平台上各跑一遍。
- 平台差异只集中在三处：
  - 快捷键：macOS 用 Cmd，Linux 和 Windows 用 Ctrl，MyGo 会自动映射；
  - 凭据后端：Keychain、Secret Service、Credential Manager；
  - HTML 预览需要 WebView：Linux 依赖 WebKitGTK，缺失时用 `mygo.WebViewAvailable()` 判断，降级为显示源码。
- Linux 运行时只依赖 GTK 3。打包产出 deb、PKGBUILD、AppImage 和安装脚本；Windows 产出安装包；macOS 产出 dmg，并完成签名和公证。

### 决策 5：核心和 CLI 共享一套代码

`postgo-cli run ./collection -e staging` 可以在 CI 里跑测试，相当于对标 Newman，而且不需要额外工作量。

### 决策 6：通过 MCP 提供 AI agent 能力 ✅

完整设计见 [AGENT.md](AGENT.md)。要点：

- 基于官方 `modelcontextprotocol/go-sdk` 实现 MCP server。agent 那边只需配置 `postgo-cli mcp` 一行：GUI 在运行时转发给 GUI，共享状态、在 GUI 里审批；GUI 没开时在进程内以无界面模式运行。
- tool 覆盖浏览、编辑、执行、运行、导入、代码生成、GraphQL / gRPC 结构查询和流式消息。请求内容一律用 `.postgo` 源码读写。
- 安全模型：
  - secret 永远遮蔽，原文和编码形式都会被替换；
  - agent 发起的请求只在目标是可信主机时才替换 secret；
  - read / edit / execute 三档权限，execute 默认需要审批；
  - 权限只存在本机，不进集合文件；
  - 每次操作都记录日志。

---

## 3. 总体架构

```
┌──────────────────────────── cmd/postgo (GUI) ────────────────────────────┐
│  ui/   视图层（package ui，只读 AppState，产出 Action）                 │
│   ├─ shell/      窗口骨架：Sidebar | Tabs | Split                       │
│   ├─ request/    URL 栏、Params/Headers/Body/Auth/Scripts/Settings      │
│   ├─ response/   Body(Pretty/Raw/Tree/Preview)、Headers、Cookies、      │
│   │              Timeline、Tests                                         │
│   ├─ kv/         通用 KeyValue 表格编辑器（启用、Key、Value、描述）     │
│   ├─ env/        环境管理                                               │
│   └─ runner/     集合 Runner                                            │
│  app/   应用层：AppState、命令（Send/Save/Import…）、win.Update 桥接     │
└───────────────────────────────┬──────────────────────────────────────────┘
                                │ 只依赖 core 的接口
┌───────────────────────────────┴───── core/（不 import mygo）────────────┐
│ model/     Request, Folder, Collection, Environment, Auth, Body…        │
│ store/     文件格式读写、集合目录扫描、外部修改合并                     │
│ vars/      变量解析：{{var}}、{{$uuid}} 等，作用域链，未定义诊断        │
│ engine/    HTTP 执行：httptrace 计时、TLS、代理、重定向、Cookie、取消、 │
│            流式（SSE）、大响应落临时文件                                │
│ auth/      Basic/Bearer/APIKey/Digest/OAuth2(PKCE)/AWS SigV4            │
│ script/    goja 沙箱 + pg API（P1 再注入 pm）                           │
│ runner/    集合执行、迭代、数据文件（CSV/JSON）、报告                   │
│ importer/  OpenAPI 3、Swagger 2、cURL、HAR、Insomnia、Bruno、OpenCollection │
│ codegen/   cURL、Go、Python requests、JS fetch、HTTPie                  │
│ agent/     MCP tools / resources / prompts、权限、secret 遮蔽            │
│ storage/   SQLite（modernc.org/sqlite）：历史、Cookie Jar、响应缓存     │
└──────────────────────────────────────────────────────────────────────────┘
cmd/postgo-cli   复用 core，用于 CI（JUnit/JSON 报告），以及 `postgo-cli mcp`
```

### 3.1 线程模型（遵守 MyGo 的约束）

- `AppState` 只在**主线程**读写，view 函数每帧从状态构建界面。
- 发送请求：
  1. 主线程创建 `ctx, cancel`，把 tab 状态设为 `Sending`；
  2. `go engine.Do(ctx, resolvedReq)`；
  3. 中间进度（已收字节数、SSE 事件）用 `mygo.RunOnMain` 合并更新，并按约 30 fps 节流后 `Invalidate`；
  4. 完成后调用 `win.Update(func(){ tab.Response = resp })`。
- Cancel 按钮直接调用 `cancel()`。
- 文件写入、导入、脚本执行一律放在 goroutine 里，**绝不阻塞主线程**。

### 3.2 核心数据模型

定义在 `core/model`（C1），以代码为准：`Request`、`Body`、`Auth`（11 种认证方式各自的字段）、`Settings`（`Opt[T]`，未设置即继承）、`Config`、`Folder` / `Node`、`Collection`、`Environment`。另外提供 `Validate()`，返回带字段路径、严重级别（Error / Warning）和规则代码的问题列表；以及 `Clone()` 深拷贝。

变量作用域，后者覆盖前者：
`Global < Collection < Environment < Folder（链） < Data（Runner 迭代） < Local（脚本设置的）`

### 3.3 文件格式：自定义 `.postgo` 文本格式

完整规范见 [FORMAT.md](FORMAT.md)。要点：

- **像 HTTP 报文。** 文件开头是 `METHOD URL` 加 headers，后面跟 `[段落]`。请求行的 METHOD 也可以是 `GRAPHQL`、`WS`、`SSE`、`GRPC`。
- **正文和脚本原样保存。** 用 `~~~` 围栏包起来，不转义、不缩进。写入时自动选择不会和内容冲突的围栏长度。
- **每个条目自带开关和描述。** 行首 `//` 表示禁用，紧挨在上方的 `# ` 行是它的描述。
- **字节级往返，向前兼容。** 解析后再写回内容完全一致；不认识的段落原样保留；集合根文件里有 `[postgo] format = 1` 版本号。
- **排序**：顺序写在目录的 `_folder.postgo` 的 `[order]` 段里，调整顺序只改这一个文件。
- **不进 git 的内容。** Secret 变量只写名字，值存在系统钥匙串；`.postgo.local/` 存本机覆盖。

```
POST {{baseUrl}}/users
Content-Type: application/json
// X-Debug: 1

[meta]
name = Create user

[body json]
~~~json
{ "name": "{{$randomFirstName}}" }
~~~

[auth bearer]
token = {{accessToken}}

[assert]
res.status == 201
```


---

## 4. 界面设计

```
┌──────────────┬─────────────────────────────────────────────────────────────┐
│ 🔍 Search    │ [GET users ×] [POST create ●×] [+]            env: [dev ▾]  │
│──────────────│─────────────────────────────────────────────────────────────│
│ ▾ my-api     │ [POST▾] {{baseUrl}}/users                    [Send ⌘↵][▾]  │
│   ▾ users    │ Params  Headers(1)  Body●  Auth  Scripts  Settings  Docs   │
│     GET list │ ┌─────────────────────────────────────────────────────────┐ │
│     POST new │ │ { "name": "{{$randomName}}" }     ← ui/editor (JSON)    │ │
│ ▸ other-api  │ └─────────────────────────────────────────────────────────┘ │
│──────────────│═════════════════════ 可拖动 Split ══════════════════════════│
│ History      │ 201 Created · 132 ms · 1.2 KB              [Save] [Copy]    │
│ Environments │ Body  Headers(12)  Cookies  Timeline  Tests(2/2)           │
│              │ [Pretty|Raw|Tree|Preview]  🔎                               │
│              │ { "id": 42, ... }                  ← 只读 editor / TreeBase │
└──────────────┴─────────────────────────────────────────────────────────────┘
```

交互要点：

- **URL 栏**：`{{var}}` 高亮，已定义的是绿色、未定义的是红色；hover 显示解析后的值；输入 `{{` 时弹出变量补全（Autocomplete）。粘贴 cURL 命令时自动识别并导入。
- **KV 表格**：最后一行永远是空行，输入后自动追加新行；支持批量编辑模式（切成纯文本 `key: value`）。
- **响应体**：根据 Content-Type 选择编辑器语言；超过阈值（例如 20 MB）的响应默认落临时文件，只读打开并提示。Tree 视图用 TreeBase 懒展开。
- **Timeline**：基于 httptrace，画 DNS / TCP / TLS / TTFB / 下载的瀑布图，并展示实际发出的请求原文（变量解析后）。
- **快捷键**：⌘↵ 发送、⌘S 保存、⌘T 新建标签、⌘P 快速打开（模糊搜索请求）、⌘E 切换环境。
- 主题跟随系统深色/浅色模式和强调色。

---

## 5. 模块划分（按模块完整交付，不分阶段）

### 5.1 原则

- **一个模块一次做完。** 一个模块要么不动，要么把它的全部功能范围一次做完，不留"以后再补"的半成品。这样可以避免分阶段带来的问题：接口反复改、数据格式要迁移、临时代码变成永久代码。
- **先定接口，再写实现。** 每个模块开工前，先把它的对外接口（Go interface 和类型）以及文件格式写下来并评审，之后锁定不改。其他模块只依赖接口，不依赖实现细节。
- **"完成"的标准统一（DoD）**，四条都满足才算完成：
  1. 范围清单里的每一项都实现了；
  2. 单元测试和集成测试通过，core 模块的测试覆盖率 ≥ 80%；
  3. 有模块文档：接口说明和使用示例；
  4. 没有 TODO，也没有被跳过的测试。
- **模块之间只通过 `core/model` 里的类型和各自的接口通信**，不允许跨模块访问内部包（`internal/`）。
- 开发顺序完全由**依赖关系**决定（见 5.4）。没有依赖关系的模块可以并行开发。

### 5.2 Core 模块（不 import mygo）

| # | 模块 | 职责与完整范围 | 依赖 | 完成标准（除通用 DoD 外） |
|---|---|---|---|---|
| C1 | **model** | 内部领域模型（和磁盘格式解耦，由 C2 负责映射），全部领域类型：Request、Folder、Collection、Environment、Var、KV、Body（none / json / text / xml / form-urlencoded / multipart / binary / graphql）、Auth（全部类型）、Scripts、Settings，以及 WebSocket、SSE、gRPC 请求类型；校验函数；深拷贝 | 无 | 覆盖后续所有模块的需求，**定稿后冻结**，只允许新增字段 |
| C2 | **store** | `.postgo` 格式的解析器和写入器（手写，按 FORMAT.md 实现）：字节级往返、原始片段表、`format` 版本分派、未知段落原样保留、围栏自动选择；目录扫描、`[order]` 排序、新建/重命名/移动/删除（发出 Renamed 事件）、外部修改合并（通过 `Watcher` 接口，由 app 层注入）、冲突检测、`.postgo.local/` 覆盖层（只用于环境） | C1 | 往返测试：读入后原样写回，git diff 为零；fuzz 测试 `parse→write→parse` 结果一致且不会 panic；并发写入安全 |
| C3 | **vars** | `{{var}}` 解析、完整作用域链、动态变量（`$uuid`、`$timestamp`、`$isoTimestamp`、`$randomInt`、`$random*` 全集）、嵌套引用、循环引用检测、未定义变量诊断（附带位置信息，供 UI 高亮）；**调用方可以传入 secret 解析策略**（按目标主机决定是否替换 secret，供 A1 使用），解析结果标记出哪些片段来自 secret，供遮蔽使用 | C1 | 每个 token 都返回带位置的诊断信息 |
| C4 | **secrets** | 抽象接口 `SecretStore`。实现：mygo.Secrets 适配器（放在 app 层注入）、超过 2560 字节时用 AES-GCM 加密写文件、测试用的内存实现 | 无 | 接口在 core，具体实现由 app 层注入 |
| C5 | **storage**（SQLite） | 用 `modernc.org/sqlite`（纯 Go，无 cgo）。不能用 mygo 的 `plugins/sqlite`，它会把整个 mygo 依赖进来，违反 core 不依赖 mygo 的约束，CLI 也会因此变重。功能：请求历史（记录来源 user/agent 和客户端名称；按"集合 ID + 请求路径"索引，订阅 Renamed 事件更新）（可搜索、可清理）、Cookie Jar（实现 `http.CookieJar`，持久化，可按域名查看和编辑）、响应缓存、UI 状态（打开的 tab、分栏比例） | C1 | 有 schema 迁移机制；10 万条历史下查询 < 50 ms |
| C6 | **auth** | 全部认证方式：Inherit、Basic、Bearer、API Key（header 或 query）、Digest、OAuth 1.0、OAuth 2.0（授权码 + PKCE、Client Credentials、Password、Implicit、Refresh、自动续期、token 缓存）、AWS SigV4、NTLM、JWT 签发 | C1, C3, C4 | 每种方式都对照真实规范或测试服务器验证；OAuth 的回调通过 `Callback` 接口交给 app 层实现 |
| C7 | **engine**（HTTP） | 发送请求的完整流水线：变量解析 → 认证 → 构建请求 → 发送 → 读取响应。包括 httptrace 计时（DNS / TCP / TLS / TTFB / 下载）、HTTP/1.1 和 HTTP/2（HTTP/3 可选）、代理（系统代理、手动、按主机）、TLS（关闭校验、自定义 CA、mTLS 客户端证书）、重定向策略、超时、取消、Cookie、解压（gzip / br / zstd）、响应体流式读取、大响应落临时文件、记录实际发出的请求原文；每次发送都可以带上下文选项（secret 解析策略、请求来源 user/agent，写入历史记录） | C1, C3, C5, C6 | 用 `httptest` 覆盖所有分支；取消后不泄漏 goroutine |
| C8 | **realtime** | WebSocket（连接、收发、二进制、ping、子协议、自定义握手 header）、SSE（事件解析、自动重连、Last-Event-ID）、Socket.IO 可选；统一的消息流接口 `Stream` | C1, C3, C6 | 能稳定承受每秒 1 万条消息、不丢消息 |
| C9 | **graphql** | 请求构建、introspection 获取 schema、为编辑器提供补全和诊断数据、变量 JSON 校验 | C1, C7 | 用公开 schema 做黄金测试 |
| C10 | **grpc** | Server reflection、.proto 导入（protocompile）、unary 和三种流式调用、metadata、TLS、JSON ↔ protobuf 互转 | C1, C3, C6 | 四种调用模式都有测试 |
| C11 | **script** | goja 沙箱：超时中断、内存限制、禁止 IO；完整的 PostGo 原生 API：`pg`（env / vars / collection / globals / cookies / sendRequest / setNextRequest / skipRequest / interpolate / sleep）、`req`、`res`、`test`、`expect`（chai），以及 `.d.ts` 类型声明；全部脚本阶段（before-request / after-response / tests，与 FORMAT.md format 1 一致）；`[assert]` 声明式断言的求值器；常用库（lodash、moment、crypto-js、tv4/ajv、xml2js、atob/btoa 等）；console 输出捕获；脚本里的 `pg.sendRequest` 沿用调用方传入的 secret 策略和来源 | C1, C3, C7 | API 文档里的每个示例都有对应测试；API 的扩展点留给 P1 注入 `pm` 对象 |
| C12 | **runner** | 按集合或文件夹执行请求；迭代次数；CSV/JSON 数据文件；请求间延时；`setNextRequest` 控制流程；失败即停；并发；事件流（供 UI 实时展示）；报告（JSON / JUnit / HTML） | C2, C7, C11 | 运行确定性：同样的输入得到同样的执行顺序 |
| C13 | **importer / exporter** | 导入：OpenAPI 3.0/3.1、Swagger 2.0、cURL、HAR、Insomnia v4、Bruno（`.bru` 和 OpenCollection YAML，只转换请求结构；脚本原样保留并在报告中标记）。导出：OpenAPI 3.1、cURL、HAR、整个集合目录打包成 zip。Postman 部分不在这里，见 P1 | C1, C2 | 每种格式至少 3 个真实文件做 fixture；无法转换的部分生成报告 |
| A1 | **agent**（`core/agent`） | 实现 AGENT.md §3 列出的全部 tools、resources 和 prompts；三档权限的判定（审批通过 `Approver` 接口交给 GUI 或 elicitation 实现）；secret 遮蔽，覆盖原文及 URL 编码、base64、hex 形式；可信主机规则；并发、频率、迭代次数和输出大小的限制；审计日志写入 C5；响应 handle 管理和分页 | C2, C3, C4, C5, C7, C8, C9, C10, C12, C13, C14 | 每个 tool 至少有正常、无权限、校验失败三种用例；用 go-sdk 内存传输做端到端测试；secret 外传的对抗用例全部被拦下 |
| C14 | **codegen** | 把请求生成为代码：cURL、HTTPie、wget、Go、Python（requests / httpx）、JavaScript（fetch / axios）、Node、Java（OkHttp）、C#、PHP、Rust（reqwest）、Swift | C1, C3 | 每种语言生成的代码都实际执行验证（容器中运行） |

### 5.3 App / UI 模块（依赖 mygo）

| # | 模块 | 职责与完整范围 | 依赖 | 完成标准 |
|---|---|---|---|---|
| U1 | **app**（应用层） | AppState、命令总线（Send / Save / Import…）、主线程与 goroutine 的桥接（`win.Update` 加节流）、注入 core 需要的平台实现（C4 Secrets、C6 OAuth deep link 或本地回环回调、C2 的 `Watcher` 用 `mygo.Watch` 实现）、多窗口、单实例、最近打开的集合 | C1–C5 | 状态只在主线程修改，用 race 检测验证 |
| U2 | **shell** | 窗口骨架：Sidebar、Tabs（拖拽排序、关闭未保存时提示、固定）、上下或左右 Split（比例持久化）、工具栏、环境切换器、命令面板（⌘P / ⌘K）、全部快捷键、主题与深色模式、菜单栏 | U1 | Tester 覆盖每个快捷键 |
| U3 | **widgets**（通用组件） | KV 表格编辑器（启用、Key、Value、描述，自动追加空行，批量文本编辑，拖拽排序）、变量高亮输入框（hover 显示解析值、`{{` 补全）、文件选择器、键值对 diff；`.postgo` 语言的编辑器高亮（自定义 `editor.Language`，原始块内嵌对应语言的高亮），用于"以源码查看"请求文件 | U1, C3 | 后续 UI 模块只用这些组件，不再各自实现 |
| U4 | **collections**（侧边栏） | 集合和文件夹树（Outline）、增删改、拖拽移动、搜索过滤、右键菜单、历史列表、多个集合同时打开 | U2, U3, C2, C5 | 1 万个请求的集合滚动流畅 |
| U5 | **request editor** | URL 栏（方法、URL、Send / 取消、粘贴 cURL 自动识别）、Params（和 URL 双向同步）、Headers（含自动生成的头只读展示）、Body（全部类型，各自配编辑器）、Auth（全部类型的表单和 OAuth 流程）、Scripts（JS 编辑器 + `pg` API 补全）、Settings、Docs（Markdown） | U3, C6, C7 | 每个面板都有 Tester 用例 |
| U6 | **response viewer** | 状态、耗时、大小；Body 的 Pretty / Raw / Tree / Preview（HTML 嵌 WebView、图片、PDF、hex）；查找和 JSONPath 过滤；Headers；Cookies；Timeline 瀑布图 + 请求原文；Tests 结果；console 输出；保存到文件；与历史响应 diff | U3, C7, C11 | 打开 100 MB 的响应不卡顿 |
| U7 | **realtime / graphql / grpc 视图** | WebSocket 和 SSE 的消息流（筛选、格式化、发送框、保存的消息模板）；GraphQL 的 query 编辑器（schema 补全）、变量编辑、文档浏览器；gRPC 的方法选择、消息编辑、流式交互 | U5, U6, C8, C9, C10 | 三种协议都能端到端跑通 |
| U8 | **environments** | 环境列表和编辑、Secret 变量（遮罩显示，存钥匙串）、全局变量、当前生效值查看（脚本运行后实时更新） | U3, C3, C4 | — |
| U9 | **runner 视图** | 配置（范围、迭代、数据文件、延时）、实时进度、结果表、失败详情、图表统计、导出报告 | U3, C12 | — |
| U10 | **import / export / codegen 视图** | 导入向导（拖入文件、粘贴、URL）并展示转换报告；导出；代码生成面板（选择语言、复制） | U3, C13, C14 | — |
| U11 | **settings** | 代理、证书（CA、客户端证书按主机）、默认超时、字体和编辑器设置、快捷键、语言（中英）；agent 设置页面由 U12 提供，挂在这里 | U3, C7 | — |
| X1 | **cli** | `postgo-cli run` / `import` / `export` / `codegen`；所有命令支持 `--json` 输出，退出码有稳定含义；报告输出。**只链接 core，不链接 mygo**，所以在无图形界面的 Linux CI 上也能运行。Secret 变量从环境变量 `POSTGO_SECRET_<名字>` 或 `--secret name=value` 读取 | C2, C12, C13, C14 | 有 CI 示例（GitHub Actions）；`go list -deps` 检查确认不含 mygo |
| X2 | **distribution** | 三平台打包、签名、公证、自动更新、安装脚本、release 流水线；安装包同时把 `postgo-cli` 放进 PATH | U1 | 能从打 tag 开始，自动产出全部安装包 |
| X3 | **mcp 入口** | `postgo-cli mcp`（stdio）：检测 GUI 是否在运行，在就通过本地 IPC 转发（Unix socket / Windows 命名管道，令牌文件权限 0600），不在就启动进程内的无界面 host；`--collection`、`--allow-execute` 参数；无界面模式通过 MCP elicitation 审批；`postgo-cli agent-guide` | A1, X1 | GUI 运行和未运行两种情况、令牌错误都有测试，三个平台都通过 |
| U12 | **agent 集成**（GUI） | GUI 里的 MCP host 和 IPC 服务端；审批弹窗（展示解析后的完整请求，可以允许一次或本会话内允许该主机）；agent 活动面板（来源、参数、结果，被改的请求可以打开 diff）；权限和可信主机设置；"连接 agent"向导（为 Claude Code、Cursor、Codex 生成配置片段） | A1, X3, U2, U3, U6 | Tester 覆盖审批和活动面板 |

### 5.3.1 Postman 兼容模块（最后做）

| # | 模块 | 职责与完整范围 | 依赖 | 完成标准 |
|---|---|---|---|---|
| P1 | **postman-compat** | Postman v2.0/v2.1 集合和环境的导入导出、全局变量导入；`pm.*` 适配层（environment / variables / collectionVariables / globals / request / response / test / expect / sendRequest / cookies / info / iterationData / execution.setNextRequest / skipRequest）注入 C11；Postman 动态变量全集；导入时生成转换报告；UI 导入向导加 Postman 入口；CLI 支持 `postgo import --from postman` | C11, C13, U10, X1 | 一批真实的 Postman 公开集合导入后，脚本全部能跑通 |

P1 只能通过 C11 的扩展点和 C13 的 importer 注册表接入，**不修改任何已完成模块的内部实现**。

### 5.3.2 进度

| 模块 | 状态 | 代码位置 |
|---|---|---|
| C1 model | 已实现，测试覆盖率 99.4%，**已冻结**（2026-11-06），之后只允许新增字段 | `core/model` |
| C4 secrets | 已实现，测试覆盖率 94.4%（mygo.Secrets 适配器由 U1 提供） | `core/secrets` |
| — | 模块路径 `github.com/jinzhongjia/postgo` | `go.mod` |
| CI | 三平台测试 + 六个目标的交叉编译 + 依赖检查 | `.github/workflows/ci.yml`、`tools/checkdeps` |

### 5.4 依赖图与开发顺序

```
C1 model ──┬─ C2 store ──────────────┬─ C12 runner ─┬─ X1 cli
           ├─ C3 vars ─┬─ C6 auth ─┐ │              │
C4 secrets ┘           │           ├─ C7 engine ─┬─ C9 graphql
C5 storage ────────────┼───────────┘             ├─ C11 script ─┘
                       ├─ C8 realtime            └─ (U5/U6)
                       ├─ C10 grpc
                       └─ C14 codegen    C13 importer（依赖 C1、C2）

U1 app → U2 shell → U3 widgets → { U4, U5, U6, U8, U9, U10, U11 } → U7
X2 distribution（依赖 U1，可以随时并行）
C2…C14 ─→ A1 agent ─→ X3 mcp 入口（还依赖 X1）─→ U12 agent 集成
```

按依赖关系分批开工。同一批内的模块互不依赖，可以并行：

1. C1 model、C4 secrets（先做，接口定稿冻结）
2. C2 store、C3 vars、C5 storage
3. C6 auth、C13 importer、C14 codegen、U1 app
4. C7 engine、C8 realtime、C10 grpc、U2 shell、X2 distribution
5. C9 graphql、C11 script、U3 widgets
6. C12 runner、U4、U5、U6、U8、U10、U11
7. U7、U9、X1 cli、A1 agent
8. X3 mcp 入口
9. U12 agent 集成
10. **P1 postman-compat**（最后）

> 每一批里的模块都按完整范围交付，不存在"先做简版、下一阶段补全"。
> 一个模块通过 DoD 之后，它的接口就算稳定了，下游模块才能开工。

## 6. 工程规范

- **分层约束**：`core/...` 和 `cmd/postgo-cli` 禁止依赖 `github.com/egoist/mygo/...`。唯一的例外是 `github.com/egoist/mygo/netproxy`：它是独立的包，只依赖 purego。CI 里用 `go list -deps` 检查这条约束。
- **测试**：
  - core 层：单元测试 + `httptest.Server` 做黄金用例；导入器用真实的导出文件做 fixture；store 用 FORMAT.md 里的全部示例加上 fuzz 语料做往返测试
  - UI 层：`ui.NewTester` 写交互测试（点 Send → 出现 200）
  - E2E：`plugins/automation`
- **性能预算**：CI 里构建 release 产物并记录二进制大小；冷启动时间和大 JSON 渲染做基准测试。
- **MyGo 版本锁定**：`go.mod` 固定在小版本，升级前读 `docs/ui/migration.md`，然后跑全量 UI 测试。

---

## 7. 已确认的决策

| 问题 | 结论 |
|---|---|
| 集合文件格式 | 自定义的 `.postgo` 文本格式（不用 YAML），规范见 FORMAT.md，**format 1 已冻结** |
| 扩展名 | `.postgo` |
| 语法规则 | `//` 表示禁用、`#` 表示描述、URL 不带 query（参数统一放 `[query]`） |
| 云同步 | 暂时不做，只用 git 协作 |
| Postman 兼容 | 放到最后，作为独立模块 P1 |
| 平台 | macOS、Linux、Windows 三平台同等支持，每个模块都要在三平台的 CI 上通过 |

## 8. 仍需确认

暂无。
