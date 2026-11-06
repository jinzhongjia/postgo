# PostGo 的 AI agent 能力（MCP）

## 1. 目标

让 Claude Code、Cursor、Codex 这类 AI agent 像人一样使用 PostGo：浏览集合、读写请求、切换环境、发请求、看响应、跑测试、导入 OpenAPI、生成代码。同时满足三点：

- **不泄露 secret**：agent 永远看不到 secret 变量的值，也不能把它带到任意主机。
- **用户掌控**：编辑和执行都有权限开关；发请求默认需要审批；agent 做的每一件事都能在 GUI 里看到。
- **一套实现，两种用法**：GUI 开着时，agent 连到 GUI，共享打开的集合、当前环境和审批弹窗；GUI 没开时，在后台以无界面模式运行。agent 那边的配置始终只有一行。

## 2. 总体结构

```
 AI agent ──stdio──▶ postgo-cli mcp ──┬── GUI 在运行 ──▶ 本地 IPC ──▶ GUI 里的 MCP host（共享状态、审批弹窗、活动面板）
                                      └── GUI 未运行 ──▶ 进程内的无界面 host（CLI 配置、elicitation 审批）
                                                 │
                                    两者都调用 core/agent（A1）
                                                 │
                    store / vars / engine / runner / importer / codegen / storage …
```

- **A1 `core/agent`**：所有 tool、resource 和 prompt 的实现。不依赖传输方式，也不依赖 mygo。基于官方的 `github.com/modelcontextprotocol/go-sdk`。
- **X3 `postgo-cli mcp`**：stdio 入口。启动时检查 GUI 是否在运行，在运行就把 JSON-RPC 转发过去，否则在进程内启动无界面 host。
  - GUI 在用户数据目录下监听本地 IPC：Unix 上是 Unix socket，Windows 上是命名管道。连接时要出示一个令牌，令牌存在权限为 0600 的文件里，所以只有本用户能连。
- **U12 GUI 集成**：MCP host、审批弹窗、agent 活动面板、权限设置，以及"连接 agent"向导。

agent 那边只需要这一行配置：

```json
{ "mcpServers": { "postgo": { "command": "postgo-cli", "args": ["mcp"] } } }
```

无界面模式下指定集合目录：`postgo-cli mcp --collection ./my-api`。

## 3. Tools

所有 tool 的返回都是结构化 JSON（MCP structured output），并附带一段人能读懂的摘要文本。请求的内容一律用 `.postgo` 源码来读写：这个格式本来就是逐行的纯文本，agent 读写起来很自然。

### 3.1 读取（权限：read）

| Tool | 作用 |
|---|---|
| `list_collections` | 列出打开的或已知的集合：id、名称、目录 |
| `list_requests` | 集合的请求树（id、名称、种类、方法、URL），可按文件夹和关键字过滤 |
| `get_request` | 请求的 `.postgo` 源码、内容版本号（哈希）、校验问题 |
| `get_folder` | 文件夹或集合根的配置源码 |
| `list_environments` / `get_environment` | 环境里的变量。secret 只返回名字，以及"是否已设置" |
| `render_request` | 用指定环境解析变量，得到最终的原始 HTTP 请求，相当于 dry run。secret 显示为 `«secret:name»` |
| `validate` | 校验一段 `.postgo` 源码，返回问题列表 |
| `list_history` / `get_response` | 历史记录；按 handle 分页读取响应体（offset/limit、JSONPath 过滤），以及响应的 headers 和计时 |
| `graphql_schema` | 用 introspection 获取 schema，返回 SDL |
| `grpc_describe` | 列出 service、method 和消息结构 |
| `codegen` | 把请求生成为指定语言的代码 |

### 3.2 编辑（权限：edit）

| Tool | 作用 |
|---|---|
| `create_request` / `update_request` | 写入 `.postgo` 源码。`update_request` 要带上读取时拿到的版本号（乐观并发），版本对不上就拒绝写入，防止覆盖用户刚做的修改 |
| `delete_request` / `move_request` / `create_folder` / `update_folder` / `set_order` | 管理目录结构 |
| `set_variable` | 修改环境里的普通变量。**不能**写入 secret 变量 |
| `import` | 导入 OpenAPI、cURL、HAR、Insomnia 或 Bruno 到指定文件夹，返回转换报告 |

所有写入都先过 `model.Validate`，有 Error 就拒绝写入，并把问题列表返回给 agent。写入通过 C2 store 完成；GUI 会收到文件变化事件，自动刷新。

### 3.3 执行（权限：execute，默认需要审批）

| Tool | 作用 |
|---|---|
| `send_request` | 发送一个已保存的请求，可以临时覆盖 vars、headers、query 和 body。返回状态码、计时、headers、body 预览（默认最多 16 KB，超出部分通过 `get_response` 分页读取）、断言和测试结果、console 输出，以及一个响应 handle |
| `send_adhoc` | 发送一段 `.postgo` 源码或一条 cURL 命令，不需要先保存 |
| `run` | 运行集合或文件夹，返回汇总和失败详情 |
| `stream_open` / `stream_send` / `stream_read` / `stream_close` | WebSocket、SSE 和 gRPC 流式调用。`stream_read` 可以设置等待时长和最多读取的条数 |

### 3.4 Resources 与 Prompts

- **Resources**：
  - `postgo://format`：FORMAT.md 全文，agent 可以先读它来学习文件格式；
  - `postgo://collections/{cid}/requests/{path}`；
  - `postgo://collections/{cid}/environments/{env}`；
  - `postgo://responses/{handle}`。
- **Prompts**：
  - `write-tests`：根据请求和最近一次响应，起草 `[assert]` 和测试脚本；
  - `debug-failure`：分析失败的请求；
  - `from-openapi`：从 OpenAPI 文档生成集合。

## 4. 安全模型

| 风险 | 措施 |
|---|---|
| **agent 读到 secret** | 所有输出（渲染出的请求、响应的 headers 和 body、console、错误信息、runner 报告）都会把 secret 的值替换成 `«secret:name»`。替换方式是按值匹配，原文以及它的 URL 编码、base64、hex 形式都算，所以服务端把 token 回显出来（哪怕编码过）也会被遮住。OAuth 拿到的 token 同样处理 |
| **agent 把 secret 带到别处**，比如把 `{{accessToken}}` 塞进指向攻击者主机的 URL | 由 agent 发起的请求，secret 变量只在**目标主机属于该集合的可信主机列表**时才会被替换，否则直接拒绝发送。可信主机列表默认取自各环境里的 URL 类变量所指的主机，用户也可以在设置里增删。脚本里调用 `pg.sendRequest` 同样遵守这条规则 |
| **未经允许发请求** | 有三档权限：read、edit、execute。每一档可以设为"允许"、"需要审批"或"禁止"。默认值：read 允许，edit 允许（改动会出现在 git diff 里，可以检查），execute 需要审批。审批的方式：GUI 模式下弹窗展示解析后的完整请求，可以选择"允许一次"或"本会话内允许这个主机"；无界面模式下用 MCP elicitation，客户端不支持时就拒绝，除非启动时加了 `--allow-execute` |
| **仓库自己给自己授权** | 权限和可信主机都存在本机用户设置里（C5 storage），**不写进集合文件**。clone 下来的仓库无法给自己授予任何权限 |
| **提示注入**：响应体里藏着指令 | tool 的输出把响应内容明确标注为不可信的外部数据；任何操作都需要 agent 显式调用 tool，不会因为响应内容自动执行 |
| **失控**：死循环发请求、超大输出 | 并发上限、每分钟请求数上限、runner 迭代次数上限、超时和输出截断，都可以配置 |
| **事后无法追查** | 每次调用 tool 都写入历史记录，标记来源为 `agent`，带上客户端名称和参数摘要。GUI 的活动面板可以查看，被改过的请求可以打开 diff 查看 |

## 5. 不用 MCP 时

- `postgo-cli` 的所有命令都支持 `--json` 输出，退出码有稳定的含义，agent 可以直接调用命令行。
- `postgo-cli agent-guide` 输出一份面向 agent 的使用指南（Markdown），可以直接当作 Claude Code 的 skill 使用。
- MyGo 的 `plugins/automation` 是用来驱动 UI 的，只用于我们自己的 E2E 测试，不作为 agent 的 API 能力。agent 操作的是 API 领域的对象，不是界面上的像素。

## 6. 测试要求

- A1：每个 tool 至少有正常、无权限、校验失败三种用例；用 go-sdk 的内存传输做端到端测试；secret 遮蔽和可信主机规则有专门的对抗用例，例如 token 被服务端回显、被编码后回显、通过脚本外传。
- X3：GUI 运行和未运行两种情况都测；IPC 令牌错误时要拒绝连接；三个平台都要测，包括 Windows 的命名管道。
- U12：用 Tester 覆盖审批弹窗和活动面板。
