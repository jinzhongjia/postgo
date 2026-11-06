# 第二批接口草案：C2 store、C3 vars、C5 storage

> 状态：**已冻结**（2026-11-06 评审通过）。
> 下面的 Go 代码是对外 API 的签名，不是实现。

---

## C3 vars（`core/vars`）

### 作用域

```go
// Level 是变量所在的层，后面的覆盖前面的。
type Level int

const (
    Global Level = iota
    Collection
    Environment
    Folder      // 可以有多层，外层在前
    Request     // 请求自己的 [vars]
    Data        // Runner 当前迭代的数据行
    Local       // 脚本运行时设置的值
)

type Layer struct {
    Level   Level
    Name    string     // 环境 ID、文件夹路径等，用于显示"值来自哪里"
    Vars    []model.KV // 只看启用的条目
    Secrets []string   // 值由 SecretLookup 提供的变量名
}

type Scope struct{ /* 不透明 */ }

func NewScope(layers ...Layer) *Scope

// Build 按 DESIGN 3.2 的顺序，从集合、请求所在的文件夹链、环境等构建作用域。
// 环境里的 .postgo.local 覆盖，由 C2 在加载时合并进 env。
func Build(in BuildInput) *Scope

type BuildInput struct {
    Globals    []model.KV
    Collection *model.Collection
    Parents    []*model.Folder // Collection.Find 返回的父文件夹链
    Request    *model.Request
    Env        *model.Environment // 可以为 nil
    Data       map[string]string
}

// Set 修改某一层里的值（脚本的 pg.env.set 等）。要不要写回磁盘由调用方决定，
// Changes 返回改了哪些。
func (s *Scope) Set(level Level, name, value string)
func (s *Scope) Changes() []Change // Change{Level, Name, Value}
```

### 解析

```go
type Options struct {
    Secrets  SecretLookup     // 按名字取 secret 的值，通常包一层 secrets.Store
    Policy   Policy
    Now      func() time.Time // 测试时注入，默认 time.Now
    Rand     io.Reader        // 测试时注入，默认 crypto/rand
    MaxDepth int              // 嵌套引用的最大深度，默认 10
}

type SecretLookup func(name string) (string, error)

// Policy 决定是否允许把 secret 替换进某个目标。AllowSecret 为 nil 时全部允许
// （用户自己发起的请求）。A1 用它实现可信主机规则。
type Policy struct {
    AllowSecret func(name string, t Target) bool
}

// Target 是模板最终要去的地方。
type Target struct {
    Host  string // 请求的目标主机，URL 解析完成后才知道
    Field string // 例如 "url"、"headers[0].value"、"body"
}

// Resolver 对应一次发送：同一个 Resolver 内，同名的动态变量（例如 $uuid、
// $timestamp）在整个请求里取同一个值。
func NewResolver(s *Scope, o Options) *Resolver

func (r *Resolver) Resolve(tmpl string, t Target) Result

type Result struct {
    Text  string
    Spans []Span // Text 里哪些片段是替换出来的，以及是否来自 secret（供遮蔽使用）
    Diags []Diag // 未定义、循环引用、嵌套过深、secret 被策略拒绝、未闭合的 {{、未知的动态变量
}

type Span struct {
    Start, End int // 在 Text 中的字节偏移
    Var        string
    Secret     bool
}

type Diag struct {
    Start, End int    // 在模板中的字节偏移，供 UI 下划线标记
    Severity   model.Severity
    Code, Msg  string
}

// Lookup 用于 hover：变量的值，以及它来自哪一层。secret 的值默认不返回。
func (r *Resolver) Lookup(name string) (Value, bool)

type Value struct {
    Text   string
    Source Layer // 只含 Level 和 Name
    Secret bool
}
```

### 词法（不需要作用域，供高亮使用）

```go
type TokenKind int // Text、Var、Dynamic、Escape、Unclosed

type Token struct {
    Kind       TokenKind
    Start, End int
    Name       string
}

func Tokenize(tmpl string) []Token

// Dynamics 列出全部动态变量，供补全和文档使用。
func Dynamics() []DynamicInfo // DynamicInfo{Name, Desc, Example}
```

**规则**

- `\{{` 输出为字面的 `{{`。
- 变量的值里还可以引用别的变量，会递归解析，最多 10 层，出现循环时报错。
- 未定义的变量原样保留为 `{{name}}`，并报一条 Warning。
- secret 被策略拒绝时，**该片段替换为空**，并报 Error。调用方看到 Error 就不发送请求。

---

## C2 store（`core/store`）

分两层：`core/store/pgfile` 只负责单个文件的读写；`core/store` 负责整个集合目录。

### pgfile：单个文件

```go
// Origin 保存解析时的原文信息（每个条目的源文本、不认识的段落、换行风格），
// 用于保证字节级往返。对调用方不透明。
type Origin struct{ /* 不透明 */ }

func ReadRequest(id string, src []byte) (*model.Request, *Origin, error)
func WriteRequest(r *model.Request, o *Origin) []byte // o 为 nil 时输出规范格式

func ReadFolder(src []byte) (cfg model.Config, order []string, o *Origin, err error)
func WriteFolder(cfg model.Config, order []string, o *Origin) []byte

func ReadCollection(src []byte) (CollectionInfo, *Origin, error) // [postgo] 段，加上根目录的 Config 和 order
func WriteCollection(ci CollectionInfo, o *Origin) []byte

func ReadEnvironment(id string, src []byte) (*model.Environment, *Origin, error)
func WriteEnvironment(e *model.Environment, o *Origin) []byte

// error 的类型是 *ParseError，一次报告全部问题，每个问题带行号和列号。
type ParseError struct{ Problems []Problem } // Problem{Line, Col int; Msg string; Severity model.Severity}
```

- 已知段落里出现不认识的 key 时：原样保留、写回，并报一条 Warning。这是为了向前兼容，不报 Error。
- `format` 比当前支持的版本新时，返回 `ErrNewerFormat`，调用方只读打开。

### Store：集合目录

```go
func Open(dir string, o Options) (*Store, error)
func Create(dir, name string) (*Store, error) // 生成 UUID，写入 _collection.postgo 和 .gitignore

type Options struct {
    Watcher Watcher // nil 表示不监听外部修改（CLI）
}

// Watcher 由 app 层用 mygo.Watch 实现。
type Watcher interface {
    Watch(dir string, changed func(paths []string)) (stop func(), err error)
}

// Collection 返回一份快照（深拷贝）。解析失败的文件不会出现在里面，而是出现在 Problems 里。
func (s *Store) Collection() *model.Collection
func (s *Store) Problems() []FileProblem // FileProblem{Path string; Err error}

// Version 是文件内容的哈希，用于乐观并发控制。
func (s *Store) Version(path string) string

// 写入操作：先过 model.Validate，有 Error 就拒绝；然后原子写入（临时文件 + rename）。
// base 是调用方读取时拿到的版本号；磁盘上的内容已经变了就返回 ErrConflict。
// 新建时 base 传 ""，此时文件必须不存在。
func (s *Store) SaveRequest(r *model.Request, base string) (version string, err error)
func (s *Store) DeleteRequest(id string) error
func (s *Store) MoveRequest(id, newID string) error // 发出 Renamed 事件
func (s *Store) CreateFolder(path string) error
func (s *Store) MoveFolder(path, newPath string) error
func (s *Store) DeleteFolder(path string) error // 文件夹必须是空的
func (s *Store) SaveFolder(path string, cfg model.Config, base string) (string, error)
func (s *Store) SetOrder(folder string, names []string) error
func (s *Store) SaveCollectionInfo(name, defaultEnv string, cfg model.Config, base string) (string, error)
func (s *Store) SaveEnvironment(e *model.Environment, base string) (string, error)
func (s *Store) DeleteEnvironment(id string) error
func (s *Store) SaveLocalOverrides(envID string, vars []model.KV) error // 写入 .postgo.local/

func (s *Store) Subscribe(fn func(Event)) (cancel func())

type Event struct {
    Kind     EventKind // Changed、Created、Deleted、Renamed、Problem
    Path     string
    OldPath  string    // 只用于 Renamed
    External bool      // true 表示由外部修改触发（git pull、编辑器、另一个 PostGo 进程）
}

func (s *Store) Close() error
```

- `Store` 的方法可以在任意 goroutine 中调用。
- **多进程**（GUI 和无界面的 MCP 同时打开同一个集合）：靠版本号的乐观检查，写入前会重新读取磁盘内容比对哈希。不使用文件锁。剩余的竞争窗口只有"比对之后、rename 之前"这一小段。

---

## C5 storage（`core/storage`，modernc.org/sqlite）

```go
func Open(path string) (*DB, error) // 自动执行 schema 迁移
func (d *DB) Close() error
```

### 历史与响应

```go
type Entry struct {
    ID           int64
    Time         time.Time
    CollectionID string
    RequestID    string // 临时请求为 ""
    Source       Source // SourceUser 或 SourceAgent
    Client       string // agent 的客户端名称
    Method, URL  string // URL 已解析变量，secret 已遮蔽
    Status       int
    Duration     time.Duration
    Size         int64
    Err          string
}

type StoredResponse struct {
    Request   []byte // 实际发出的请求原文（已遮蔽）
    Headers   []byte
    Body      []byte
    Truncated bool   // body 超过上限时为 true
    Timing    []byte // JSON
}

func (d *DB) AddHistory(e Entry, r *StoredResponse) (int64, error)
func (d *DB) History(q HistoryQuery) ([]Entry, error) // 可按集合、请求、关键字、来源、起始时间过滤；用 Before ID 做游标分页
func (d *DB) Response(id int64) (*StoredResponse, error)
func (d *DB) DeleteHistory(q HistoryQuery) (int, error)
func (d *DB) PruneHistory(keep int, olderThan time.Duration) (int, error)
func (d *DB) RenameRequest(collectionID, oldID, newID string) error // 订阅 C2 的 Renamed 事件
```

- 响应体存进 SQLite 的上限默认 10 MB，超出部分截断并设置 `Truncated`。

### Cookie

```go
// Jar 实现 http.CookieJar，遵循 RFC 6265，用 publicsuffix 判断公共后缀，并持久化到数据库。
// 每个集合一个 Jar，项目之间互不串扰。
func (d *DB) Jar(collectionID string) *Jar

func (j *Jar) SetCookies(u *url.URL, cs []*http.Cookie)
func (j *Jar) Cookies(u *url.URL) []*http.Cookie
func (j *Jar) All() []StoredCookie
func (j *Jar) Put(c StoredCookie) error     // 用户在 UI 里手动编辑
func (j *Jar) Delete(domain, path, name string) error
func (j *Jar) Clear(domain string) error    // domain 为 "" 时清空全部
```

### 审计、设置、UI 状态

```go
func (d *DB) AppendAudit(a AuditEntry) error // AuditEntry{Time, Client, Tool, Args, Result, CollectionID}
func (d *DB) Audit(q AuditQuery) ([]AuditEntry, error)

// 本机设置：agent 权限、可信主机、代理等。值是 JSON，由各模块自己定义类型。
func (d *DB) GetSetting(key string, v any) (bool, error)
func (d *DB) SetSetting(key string, v any) error

// UI 状态：打开的 tab、分栏比例等。
func (d *DB) GetState(key string, v any) (bool, error)
func (d *DB) SetState(key string, v any) error
```

---

## 需要评审的决定

1. **变量优先级多了一层 Request**，位置在 Folder 和 Data 之间，对应请求自己的 `[vars]` 段。DESIGN 3.2 原来的链条里漏了这一层。
2. **动态变量**在一次发送内同名取同一个值。比如 URL 和 body 里的 `{{$uuid}}` 是同一个值。Postman 是每处都生成新值，这一点和它不同。
3. **Cookie Jar 按集合隔离**，不是 Postman 那样全局共用一个。
4. **多进程同时写同一个集合**，只靠版本号做乐观检查，不加文件锁。
5. **响应体在 SQLite 里最多存 10 MB**。
6. **已知段落里不认识的 key**：原样保留，报 Warning。
