# PostGo 集合文件格式（`.postgo`）format 1

> 状态：**已冻结**（2026-11-06 评审通过）。以后的改动只能以 format 2 的形式发布，解析器要同时支持新旧两个版本。

## 1. 设计目标

1. **请求看起来就像一条 HTTP 报文。** 开头是请求行加 headers，从浏览器 DevTools 或抓包工具复制出来的原始报文，几乎不用改就能用。
2. **正文原样保存，不做转义，不加缩进。** JSON、XML、GraphQL、JS 脚本写进文件和粘贴到编辑器里一模一样。这是放弃 YAML / JSON / TOML 的主要原因：
   - YAML：缩进敏感，有 `no` 被解析成 false 这类隐式类型陷阱，多行内容必须缩进；
   - JSON：正文和脚本都要转义成一行字符串，diff 没法读；
   - TOML：嵌套结构写起来啰嗦，数组表冗长。
3. **git 友好。** 一个请求一个文件；写回时字段顺序固定，写出的内容是确定的；改一个 header 只产生一行 diff。
4. **能被无损往返。** 解析后再写回，得到的字节完全相同。GUI 编辑不会悄悄改掉用户手写的内容。
5. **向前兼容。** 不认识的段落原样保留并写回；格式版本号写在集合根文件里。
6. **简单，可以手写解析器。** 语法是逐行的，没有嵌套，解析器只需几百行 Go 代码，并且可以做 fuzz 测试。

## 2. 目录结构

```
my-api/
├── _collection.postgo          # 集合根：格式版本、集合级 headers/auth/vars/scripts/settings
├── environments/
│   ├── dev.postgo
│   └── prod.postgo
├── users/
│   ├── _folder.postgo          # 文件夹级配置（可选）
│   ├── list-users.postgo
│   └── create-user.postgo
├── .postgo.local/              # 本机私有覆盖，默认写进 .gitignore
└── .gitignore
```

- **身份**：请求的 ID 是它相对于集合根的路径（例如 `users/create-user.postgo`），显示名写在 `[meta] name` 里。重命名或移动时，store 会发出 `Renamed(old, new)` 事件，历史记录等引用据此更新。
- **集合 ID**：写在 `_collection.postgo` 的 `[postgo] id`，是一个 UUID，新建集合时生成。集合目录换位置、被 clone 到别处，ID 都不变。
- 以 `_` 开头的文件名保留给 `_collection` 和 `_folder`。
- **排序**：由所在目录的 `_folder.postgo` 或 `_collection.postgo` 里的 `[order]` 段决定，每行一个文件名或子目录名。不在列表里的条目按名称排在后面。调整顺序只改这一个文件，不会牵连其他请求文件；需要时 `_folder.postgo` 会按需自动创建。
- **路径**：文件里出现的所有路径（multipart 文件、`[body file]`、`[proto] file`、证书）都**相对于集合根**，这样移动请求文件不会让路径失效。也可以写绝对路径，但 UI 会提示"无法跨机器共享"。

## 3. 词法规则

| 规则 | 说明 |
|---|---|
| 编码 | UTF-8，无 BOM，换行符 LF（读取时兼容 CRLF，写回时保留文件原来的换行风格），文件以换行结尾 |
| 段落头 | 单独一行、从第 0 列开始的 `[名称]` 或 `[名称 参数]`，例如 `[body json]`、`[auth bearer]`、`[example Created user]`。名称是 `]` 之前的第一个词；参数是其余部分，去掉首尾空白，可以含空格。同一个文件里"名称 + 参数"不能重复，重复视为解析错误 |
| 键值行 | `key = value`，分隔符是**第一个** ` = `（等号两侧各一个空格）；值为空时写作 `key =`。headers 写作 `Name: value`，分隔符是第一个 `: `，值为空时写作 `Name:`。值的其余部分原样保留，不需要引号。key 中不能含 ` = `，header 名不能含 `:`，这由 C1 的校验保证 |
| 值的类型 | 文件里所有的值都是字符串。类型（数字、布尔、时长 `10s`）由读取它的模块按字段解释 |
| 禁用条目 | 行首加 `//`，例如 `// X-Debug: 1`。条目保留但不生效，对应 UI 里没勾选的复选框 |
| 描述 | 紧挨在条目上方、以 `# ` 开头的行，是这个条目的描述（可以多行）。没有游离的注释：所有 `#` 行都归属于它下面的条目，因此往返不会丢失 |
| 原始块 | 用 `~~~` 围栏包起来，可带信息串，例如 `~~~json`。闭合围栏的 `~` 数量必须与开头相同。块内容一字不改。写入时自动选择比内容里最长的行首 `~` 串更长的围栏，所以**永远不会冲突** |
| 多行值 | 键值的值如果含换行，或者有首尾空白，就写成 `key = ~~~`，后面跟原始块 |
| 转义 | 文件格式层的转义只有两种位置：**行首**的 `\#`、`\//`、`\[`、`\~`、`\\`，以及 multipart 值开头的 `\@`。写入器会自动加转义；原始块内部**从不**转义 |
| 空行 | 段落之间用一个空行分隔（写入器统一这样写）；读取时忽略多余的空行 |

## 4. 请求文件

### 4.1 请求行与 headers

文件的第一个非空、非描述行是请求行：

```
<METHOD> <URL>
```

| METHOD | 含义 |
|---|---|
| `GET` `POST` `PUT` `PATCH` `DELETE` `HEAD` `OPTIONS` `TRACE` 及任意自定义 token | HTTP |
| `GRAPHQL` | GraphQL over HTTP（默认用 POST，可在 `[settings] method` 里改） |
| `WS` | WebSocket |
| `SSE` | Server-Sent Events |
| `GRPC` | gRPC，URL 写作 `{{host}}/package.Service/Method` |

请求行之后、第一个空行或段落头之前的 `Name: value` 行都是 headers。

**URL 不带 query。** query 参数统一写在 `[query]` 段里，参数只有这一个来源，所以禁用状态和描述不会丢。UI 里的 URL 输入框会实时把两者合并显示，并双向同步。

### 4.2 段落一览（写入器按下表顺序输出）

| 段落 | 内容 | 适用于 |
|---|---|---|
| `[meta]` | `name`、`tags`（逗号分隔） | 所有 |
| `[query]` | 键值 | HTTP / GRAPHQL / WS / SSE |
| `[path]` | 路径参数，对应 URL 里的 `:id` | HTTP |
| `[body <type>]` | 类型：`json` `text` `xml` `html` `graphql` 用原始块；`form` `multipart` 用键值；`file` 写文件路径 | HTTP / GRAPHQL |
| `[variables]` | GraphQL 的 variables，JSON 原始块 | GRAPHQL |
| `[auth <type>]` | `inherit`（默认，可省略）`none` `basic` `bearer` `apikey` `digest` `oauth1` `oauth2` `awsv4` `ntlm` `jwt`，各自的键值 | 所有 |
| `[vars]` | 请求级变量 | 所有 |
| `[script <phase>]` | `before-request` / `after-response` / `tests`，JS 原始块 | 所有 |
| `[assert]` | 声明式断言，每行一条：`res.status == 201` | 所有 |
| `[settings]` | `timeout`、`follow-redirects`、`max-redirects`、`verify-tls`、`http-version`、`encode-url`；GRAPHQL 另有 `method`（`POST` 或 `GET`） | 所有 |
| `[message <name>]` | 保存的消息模板，原始块 | WS / GRPC |
| `[metadata]` | gRPC 的 metadata | GRPC |
| `[proto]` | `reflection = true` 或 `file = protos/user.proto`（相对于集合根） | GRPC |
| `[example <name>]` | 示例响应：`status = 201` 加上 body 原始块 | HTTP |
| `[docs]` | Markdown 原始块 | 所有 |

`multipart` 段里，值以 `@` 开头表示文件，路径相对于集合根，例如 `avatar = @fixtures/a.png`；字面的 `@` 写作 `\@`。

段落内容的格式只有两种：原始块类（body 的 raw 类型、`[variables]`、`[script …]`、`[message …]`、`[docs]`）的内容就是一个原始块；其余段落都是逐行的键值或列表。

### 4.3 完整示例

```
POST {{baseUrl}}/users
Content-Type: application/json
# 链路追踪 id，网关会透传
X-Trace-Id: {{$uuid}}
// X-Debug: 1

[meta]
name = Create user
tags = users, write

[query]
# 返回完整对象
verbose = true
// dryRun = true

[body json]
~~~json
{
  "name": "{{$randomFirstName}}",
  "email": "{{$randomEmail}}"
}
~~~

[auth bearer]
token = {{accessToken}}

[script after-response]
~~~js
pg.env.set("userId", res.json().id)
~~~

[assert]
res.status == 201
res.json().id != null
res.time < 500

[settings]
timeout = 10s

[docs]
~~~md
创建用户。成功返回 **201**，body 里带新用户的 `id`。
~~~
```

### 4.4 其他协议示例

```
GRAPHQL {{baseUrl}}/graphql

[body graphql]
~~~graphql
query User($id: ID!) { user(id: $id) { id name } }
~~~

[variables]
~~~json
{ "id": "{{userId}}" }
~~~
```

```
GRPC {{grpcHost}}/users.v1.UserService/GetUser

[message default]
~~~json
{ "id": "{{userId}}" }
~~~

[metadata]
authorization = Bearer {{accessToken}}

[proto]
reflection = true
```

```
WS wss://{{host}}/ws
Sec-WebSocket-Protocol: chat

[message hello]
~~~json
{ "type": "hello" }
~~~
```

## 5. 集合根与文件夹

`_collection.postgo` 和 `_folder.postgo` 没有请求行，所以 headers 写在 `[headers]` 段里（只有这两种文件用这个段），每行的写法和请求里的 headers 一样，是 `Name: value`。其他可用的段：`[auth …]`、`[vars]`、`[script …]`、`[settings]`、`[order]`、`[docs]`。这些配置由下层继承：headers 和 vars 逐层合并，下层覆盖上层；auth 和 settings 由下层覆盖；scripts 按 集合 → 文件夹 → 请求 的顺序依次执行。

集合根文件额外有一个必需的段落：

```
[postgo]
format = 1
id = 3f6c2a1e-8d4b-4c1f-9a77-1b2e5d0c9e10
name = My API
default-env = dev

[order]
auth
users
health.postgo
```

- `format` 是格式版本号。解析器按它分派到对应的解析逻辑。
- 遇到比自己新的版本时，只读打开并提示用户升级 PostGo。

## 6. 环境文件

```
[meta]
name = Development
color = green

[vars]
baseUrl = https://api.dev.example.com
# 测试账号
username = tester

[secrets]
# 值存在系统钥匙串里，文件里只有名字
accessToken
clientSecret
```

- `[secrets]` 段里只写变量名，每行一个。
- 值存在系统钥匙串，key 为 `postgo:<集合 id>:<环境文件名>:<名字>`。超过钥匙串大小上限的值，加密后写进用户数据目录。
- 本机覆盖：format 1 中 `.postgo.local/` 只支持环境文件。`.postgo.local/environments/dev.postgo` 里的同名变量覆盖共享值，并且不进 git。

## 7. 变量引用

- `{{name}}`：按作用域链解析，链条见 DESIGN 3.2。
- `{{$uuid}}`、`{{$timestamp}}`、`{{$isoTimestamp}}`、`{{$randomInt}}`、`{{$random*}}`：动态变量。
- 字面的 `{{` 写作 `\{{`。这是**变量插值层**（C3）的转义，不是文件格式层的：文件里原样存储 `\{{`（原始块里也一样），发送时由 C3 把它还原成 `{{`。
- 变量引用可以出现在 URL、header 值、键值的值、原始块和认证字段里；**不能出现在段落名和键名里**。

## 8. 实现要求（C2 store）

- 解析器输出 C1 的模型，同时保留一份"原始片段表"，记录不认识的段落和每个条目的源文本。写回时没被修改的条目直接输出原文，保证字节级往返。
- 写入器是确定性的：同样的模型永远写出同样的字节。
- 测试要求：
  - 黄金文件测试；
  - `go test -fuzz`：对任意输入，满足 `parse → write → parse` 前后两次结果相等，且解析不会 panic；
  - 往返测试：读入再写回，字节完全一致。
- C2 位于 core 层，不包含任何 UI 代码。`.postgo` 的编辑器高亮由 U3 实现：用 `editor.Language{Highlighter: …}` 自定义语言，`TokenizerFunc` 带有逐行的 `uint64` 状态，可以用它记录"是否在围栏内、围栏长度和信息串"。原始块内部按信息串切换到对应语言的 tokenizer。
- 文件监听由 C2 定义 `Watcher` 接口，app 层注入 `mygo.Watch` 的实现；CLI 不需要监听。
