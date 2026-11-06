# 第三批接口草案：C6 auth、C13 importer、C14 codegen、U1 app

> 状态：**待评审**。评审通过后冻结，然后再写实现（DESIGN 5.1）。
>
> 约定：下面的 Go 代码是对外 API 的签名，不是实现。冻结后只允许新增，不允许改已有签名。

---

## C6 auth（`core/auth`）

### 分工

引擎（C7）决定发什么，auth 只负责把凭据放上去。多数方式一次就能算完；Digest 和 NTLM 必须先收到 401，再由引擎带着挑战重发，所以它们有两种用法：

- **Direct**：`Apply` 一次写完，不重发。
- **Challenge**：`Apply` 不写任何东西；收到 401 后引擎用 `Digest` / `NTLM` 会话算出下一个 `Authorization`，再发一次（NTLM 最多三轮）。

```go
package auth

// Resolve resolves a template field ({{...}}) to its value. C7 passes the
// resolver's function, so secrets keep their policy.
type Resolve func(template string) (string, error)

// Input is everything an authentication may need about one request.
type Input struct {
    Auth    *model.Auth
    Method  string
    URL     *url.URL      // no query parameters are added here by auth
    Body    []byte        // OAuth 1 body hash, Digest auth-int
    Headers http.Header   // OAuth 1 signature base
    Resolve Resolve
}

// Scheme tells the engine how to apply the authentication.
type Scheme int

const (
    Direct Scheme = iota
    Digest         // respond to WWW-Authenticate: Digest
    NTLM           // three-message handshake
)

// SchemeOf returns the scheme of a type, and whether auth knows it.
func SchemeOf(t model.AuthType) (Scheme, bool)

// Resolver holds the dependencies shared by all requests.
type Resolver struct {
    Tokens TokenStore    // nil: tokens live in memory of the resolver
    OAuth2 Callback      // authorization code and implicit grants
    HTTP   *http.Client  // token requests; http.DefaultClient when nil
    Clock  func() time.Time
    Rand   io.Reader
}

// Apply authenticates req for a Direct scheme. For Digest and NTLM it does
// nothing and returns nil; the engine calls Respond instead.
func (r *Resolver) Apply(ctx context.Context, req *http.Request, in Input) error

// Respond returns the Authorization value for a 401 challenge. challenge is
// the value of the WWW-Authenticate header. state must be kept between the
// calls of one exchange: NewChallenge then Respond once for Digest, and
// twice for NTLM (type 1, then type 3).
func (r *Resolver) NewChallenge(in Input) (*Challenge, error)
func (c *Challenge) Message(challenge string) (authorization string, done bool, err error)
```

`Challenge` is opaque; it keeps nonce counts and, for NTLM, the handshake state. The engine owns its lifetime (one send, including retries).

### OAuth 2.0

```go
// Token is an access token and what came with it.
type Token struct {
    AccessToken  string
    TokenType    string
    RefreshToken string
    Expiry       time.Time // zero: does not expire
    Scope        string
    Raw          map[string]any // the whole response, for display
}

// TokenStore caches tokens by the key Resolver computes from the grant and
// the client: two requests with the same client share a token.
type TokenStore interface {
    Load(ctx context.Context, key string) (Token, bool, error)
    Save(ctx context.Context, key string, t Token) error
    Delete(ctx context.Context, key string) error
}

// Callback is the browser flow, injected by the app layer (U1): it opens
// the authorization URL and waits for the redirect to redirectURI.
type Callback interface {
    Authorize(ctx context.Context, url, redirectURI string) (AuthorizeResult, error)
}

type AuthorizeResult struct {
    Code  string
    State string
    Error string // the provider's error, as "access_denied"
}

// AuthorizeURL builds the URL for the authorization code or implicit grant,
// and the state and PKCE verifier to keep until the callback.
func AuthorizeURL(a *model.OAuth2Auth, res Resolve) (url, state, verifier string, err error)

// Exchange turns a callback result into a token, checking state. For the
// implicit grant there is no exchange: the token comes in the fragment,
// which Callback puts in AuthorizeResult.Code.
func (r *Resolver) Exchange(ctx context.Context, a *model.OAuth2Auth, res AuthorizeResult, verifier string) (*Token, error)

// Authorize runs the whole browser flow: AuthorizeURL, Callback, Exchange,
// and Save.
func (r *Resolver) Authorize(ctx context.Context, a *model.OAuth2Auth, in Input) (*Token, error)

// Refresh asks for a new token with the refresh token.
func (r *Resolver) Refresh(ctx context.Context, a *model.OAuth2Auth, t Token) (*Token, error)

// Token returns the cached token of a, fetching or refreshing it when
// needed, for Client Credentials, Password, and the other grants.
func (r *Resolver) Token(ctx context.Context, a *model.OAuth2Auth, in Input) (*Token, error)
```

### 冻结前需要确认

1. **NTLM 用现成库**：`github.com/Azure/go-ntlmssp`（纯 Go，生成 type 1/3 消息）。握手仍由引擎驱动，不引入自定义 `RoundTripper`，这样重试和重定向仍由 C7 统一处理。
2. **Token 缓存的 key**：`grant + clientID + tokenURL + scope + 用户名`，不含 secret。secret 改变后旧 token 仍然可用，直到过期。
3. **OAuth 2.0 的隐式授权**也是浏览器流程，token 从重定向的 fragment 拿，`Callback` 负责把它解析进 `AuthorizeResult.Code`。
4. **`Apply` 不解析 URL 里的 query**：API Key 和 OAuth2 的 query 放置直接改 `req.URL`；不碰 `Input.URL`。这样引擎的“实际发出的请求”记录能拿到最终 URL。
5. **JWT 的 `Payload` 每次发送重新生成**：如果 `Payload` 里含 `iat` / `exp`，按当前时间替换；用户自己写死的值不覆盖。

---

## C14 codegen（`core/codegen`）

```go
package codegen

type Language string

const (
    Curl Language = "curl"
    HTTPie Language = "httpie"
    Wget Language = "wget"
    Go Language = "go"
    PythonRequests Language = "python-requests"
    PythonHttpx Language = "python-httpx"
    JavaScriptFetch Language = "javascript-fetch"
    JavaScriptAxios Language = "javascript-axios"
    Node Language = "node"
    JavaOkHTTP Language = "java-okhttp"
    CSharp Language = "csharp"
    PHP Language = "php"
    RustReqwest Language = "rust-reqwest"
    Swift Language = "swift"
)

// Languages lists every language, in the order the UI offers them.
func Languages() []Language

// Request is one resolved request: codegen never resolves variables or
// authentication itself.
type Request struct {
    Method  string
    URL     string            // with query
    Headers []model.KV        // what will be sent, secrets already resolved
    Body    []byte            // nil: no body
    // ContentType is separate so generators can choose a body encoding
    // (form fields, files) without parsing Headers.
    ContentType string
    // Parts describes a multipart body; nil for other bodies.
    Parts []Part
}

type Part struct {
    Name     string
    Value    string
    FileName string // "" for a text part
}

type Options struct {
    Indent string // "  " when empty
    Pretty bool   // format JSON bodies
}

// Generate returns code that makes the request. An unknown language is an
// error, never a fallback.
func Generate(lang Language, req Request, opts Options) (string, error)
```

### 冻结前需要确认

1. **入参是“已解析的请求”**，所以 C14 不依赖 C7。C7 负责把 `model.Request` 变成 `codegen.Request`，两处共用一份“实际发出什么”的逻辑，避免生成代码和真实请求不一致。
2. **只生成标准库或最常用库**：Go 用 `net/http`，Python 是 `requests` 和 `httpx`，JS 是 `fetch` 和 `axios`；不生成框架绑定。
3. **二进制 body 用占位符**：生成 `@/path/to/file`（cURL）或 `open(...)`（其他语言），并在末尾注释原始路径；不把二进制内容塞进代码。
4. **敏感值照常写入**：用户点了“生成代码”就是要复制走，这里不做遮蔽；调用方负责提示。

---

## C13 importer / exporter（`core/importer`）

```go
package importer

type Format string

const (
    OpenAPI3  Format = "openapi3"
    Swagger2  Format = "swagger2"
    Curl      Format = "curl"
    HAR       Format = "har"
    Insomnia  Format = "insomnia"
    Bruno     Format = "bruno"      // .bru 和 OpenCollection YAML
)

func Formats() []Format

// Result is what an import produced, in memory. Writing it belongs to C2,
// so an import can be previewed and edited before it touches the disk.
type Result struct {
    Collection *model.Collection
    // Files maps a collection-relative path to its file's source: the
    // requests and folders that came from the import. Paths already have
    // their final names.
    Files map[string][]byte
    Report []Issue
}

// Issue is a part of the input that was changed or dropped.
type Issue struct {
    Level   IssueLevel // Warning or Error
    Path    string     // where in the input
    Message string
}

type IssueLevel int
const (
    Warning IssueLevel = iota
    Error
)

type Options struct {
    Name    string // the collection's name when the input has none
    BaseURL string // for OpenAPI servers, when they are relative
    // Scripts keeps the scripts of formats that carry them (Bruno).
    // When false they are left out and reported.
    Scripts bool
}

// Detect guesses the format of data, by filename and content. It returns
// false when it recognizes nothing.
func Detect(filename string, data []byte) (Format, bool)

// Import parses data. It returns an error only when the input is not the
// format at all; everything else is in Report, with Level Error, and a
// partial Result.
func Import(format Format, data []byte, opts Options) (*Result, error)

// ImportAny detects and imports. An unknown format is an error listing
// the known ones.
func ImportAny(filename string, data []byte, opts Options) (*Result, error)
```

### 导出

```go
// ExportToOpenAPI writes the collection as an OpenAPI 3.1 document.
func ExportToOpenAPI(c *model.Collection, opts ExportOptions) ([]byte, error)

// ExportToHAR writes the leaves of the collection as a HAR 1.2 log. It
// makes no requests: only the request side is filled.
func ExportToHAR(c *model.Collection, opts ExportOptions) ([]byte, error)

type ExportOptions struct {
    Name    string
    BaseURL string
    // IncludeScripts puts scripts in extensions when the format allows.
    IncludeScripts bool
}

// Archive packs a collection directory (its files as C2 loaded them) into
// a zip, preserving relative paths and executable-free permissions.
func Archive(files map[string][]byte) ([]byte, error)
```

### 冻结前需要确认

1. **导入只产出内存中的模型和文件内容，不写盘。** 调用方先展示报告，用户确认后再由 C2 落盘，失败不会留下半个集合。
2. **cURL 导入放在这里，cURL 导出放在 C14。** 两者方向不同，避免 C13 依赖 C14。
3. **脚本原样保留**：Bruno 的脚本不改写、不翻译，原样进入 `Scripts` 字段，并在报告里注明“脚本 API 不是 PostGo 的，运行时可能失败”。`Options.Scripts=false` 时丢弃并报告。
4. **格式识别失败时报错，不猜**：`Detect` 认不出时 `ImportAny` 直接报错，列出支持的格式；不做“像 JSON 就当 Postman”这种猜测。
5. **OpenAPI 的 `servers` 变量**（`{baseUrl}`）作为普通变量导入，默认值放 `[vars]`；相对 URL 用 `Options.BaseURL` 补全。
6. **保留未知部分**：格式里有、模型里没有的字段（例如 OpenAPI 的 schema、Bruno 的断言）放不进模型就不放，逐条记进 `Report`，不静默丢弃。

---

## U1 app（`app`）

U1 依赖 mygo，是唯一允许 import `github.com/egoist/mygo` 的应用层包。它把 core 的能力接到窗口上，本身不含业务逻辑。

```go
package app

type Options struct {
    // DataDir is where the database, settings and local overrides live.
    // Empty: the OS's user data directory for "postgo".
    DataDir string
    // Open lists collection directories to open at start.
    Open []string
    // Headless runs without windows: for tests and the MCP host. It has
    // no UI thread; commands run inline.
    Headless bool
    // Secrets, Callback and Watcher are injected by main, so tests can
    // replace them.
    Secrets  secrets.Store
    OAuth    auth.Callback
    Watcher  store.Watcher
    Version  string
}

func New(opts Options) (*App, error)

// Run opens the windows and blocks until the last one closes. It owns the
// main thread.
func (a *App) Run() error

// Close saves state and closes the database. Run returns after it.
func (a *App) Close() error
```

### 状态与命令

```go
// State is what the UI draws. It is read on the main thread only.
type State struct {
    Collections []CollectionState // open collections
    Active      string            // the active collection's ID
    Env         map[string]string // collection ID → active environment ID
    Tabs        []Tab
    ActiveTab   int
    Layout      Layout            // sidebar width, split ratio
    Busy        int               // requests in flight
    Toasts      []Toast
}

// Command is one user action. Every change to the model goes through one,
// so undo, the CLI and MCP can share them.
type Command interface{ command() }

type SendRequest struct{ Tab int }
type CancelRequest struct{ Tab int }
// ... SaveRequest, OpenCollection, CloseCollection, SelectEnv, OpenTab,
// CloseTab, ReorderTabs, CreateRequest, RenameNode, MoveNode, DeleteNode,
// Import, Export, RunCollection, SetSetting, ...

// Do runs a command on the main thread and returns when its synchronous
// part is done. Long work (sending, importing, running) continues in a
// goroutine and reports back through Events.
func (a *App) Do(ctx context.Context, cmd Command) error

// Events is the stream of what happened, for the UI and for tests.
func (a *App) Events() <-chan Event

// State returns a snapshot, safe to read from any goroutine.
func (a *App) State() State
```

### 线程模型

- 主线程：所有 `State` 的修改，都在 `Do` 和事件回调里完成。
- 工作 goroutine：请求、导入、Runner 在后台跑，通过 `win.Update` 把结果送回主线程；进度按 30fps 合并。
- `App.Do` 从任意 goroutine 调用：内部投递到主线程并等待，不会死锁（主线程不反过来等工作 goroutine 同步返回）。
- Headless 模式没有主线程，`Do` 直接执行；GUI 和 CLI 因此走同一套命令。

### 冻结前需要确认

1. **命令是值类型，事件也是值类型**，两者都在 `app` 包里定义；UI 只 switch，不直接调 core。这样 UI 测试可以只发命令、收事件，不点按钮。
2. **`State` 用快照返回**，内部结构不暴露，避免 UI 持有指针后跨线程读写。
3. **平台实现由 `main` 注入**，`app` 只定义接口（Secrets、OAuth Callback、Watcher 已在 core 定义，这里只是组装）；没有构建标签以外的平台分支。
4. **单实例**：`Run` 启动时用 `mygo.SingleInstance`（若无此 API，用锁文件实现）把第二次启动的参数转给第一个实例。
5. **多窗口先不做**：`Options` 里保留位置，但 U1 只开一个窗口；多窗口等 U2 需要时再加，避免现在猜错 API。

---

## 请确认

1. C6 的 NTLM 库、Token 缓存 key、隐式授权处理、`Apply` 不碰 `Input.URL`、JWT 时间字段重算。
2. C14 的入参设计（已解析请求）和二进制占位符。
3. C13 的“只产出内存结果、不写盘”、脚本原样保留、格式识别失败即报错。
4. U1 的命令/事件总线、快照状态、单窗口。
5. 三个模块的实现顺序：C14 → C6 → C13 → U1（从依赖少、风险低的开始）。
