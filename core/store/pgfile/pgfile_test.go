package pgfile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jinzhongjia/postgo/core/model"
)

// The examples of docs/FORMAT.md.
const formatExample = `POST {{baseUrl}}/users
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
创建用户。成功返回 **201**，body 里带新用户的 ` + "`id`" + `。
~~~
`

const graphqlExample = `GRAPHQL {{baseUrl}}/graphql

[body graphql]
~~~graphql
query User($id: ID!) { user(id: $id) { id name } }
~~~

[variables]
~~~json
{ "id": "{{userId}}" }
~~~
`

const grpcExample = `GRPC {{grpcHost}}/users.v1.UserService/GetUser

[message default]
~~~json
{ "id": "{{userId}}" }
~~~

[metadata]
authorization = Bearer {{accessToken}}

[proto]
reflection = true
`

const wsExample = `WS wss://{{host}}/ws
Sec-WebSocket-Protocol: chat

[message hello]
~~~json
{ "type": "hello" }
~~~
`

const collectionExample = `[postgo]
format = 1
id = 3f6c2a1e-8d4b-4c1f-9a77-1b2e5d0c9e10
name = My API
default-env = dev

[order]
auth
users
health.postgo
`

const envExample = `[meta]
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
`

func mustRequest(t *testing.T, src string) (*model.Request, *Origin) {
	t.Helper()
	r, o, err := ReadRequest("users/create-user.postgo", []byte(src))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	return r, o
}

func TestFormatExample(t *testing.T) {
	r, o := mustRequest(t, formatExample)
	want := &model.Request{
		ID: "users/create-user.postgo", Name: "Create user", Tags: []string{"users", "write"},
		Kind: model.KindHTTP, Method: "POST", URL: "{{baseUrl}}/users",
		Headers: []model.KV{
			{Key: "Content-Type", Value: "application/json"},
			{Key: "X-Trace-Id", Value: "{{$uuid}}", Desc: "链路追踪 id，网关会透传"},
			{Key: "X-Debug", Value: "1", Disabled: true},
		},
		Query:    []model.KV{{Key: "verbose", Value: "true", Desc: "返回完整对象"}, {Key: "dryRun", Value: "true", Disabled: true}},
		Body:     model.Body{Type: model.BodyJSON, Raw: "{\n  \"name\": \"{{$randomFirstName}}\",\n  \"email\": \"{{$randomEmail}}\"\n}"},
		Auth:     model.Auth{Type: model.AuthBearer, Bearer: model.BearerAuth{Token: "{{accessToken}}"}},
		Scripts:  model.Scripts{AfterResponse: `pg.env.set("userId", res.json().id)`},
		Asserts:  []model.Assert{{Expr: "res.status == 201"}, {Expr: "res.json().id != null"}, {Expr: "res.time < 500"}},
		Settings: model.Settings{Timeout: model.Some(10 * time.Second)},
		Docs:     "创建用户。成功返回 **201**，body 里带新用户的 `id`。",
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("ReadRequest =\n%+v\nwant\n%+v", r, want)
	}
	if len(o.Warnings()) != 0 {
		t.Errorf("warnings: %v", o.Warnings())
	}
	if got := string(WriteRequest(r, o)); got != formatExample {
		t.Errorf("round trip:\n%s", got)
	}
	// The example is canonical: written anew, it is the same.
	if got := string(WriteRequest(r, nil)); got != formatExample {
		t.Errorf("canonical:\n%s", got)
	}
	if is := r.Validate(); is.HasErrors() {
		t.Errorf("the example is invalid: %v", is)
	}
}

func TestOtherKinds(t *testing.T) {
	cases := []struct {
		src  string
		want func(*model.Request) bool
	}{
		{graphqlExample, func(r *model.Request) bool {
			return r.Kind == model.KindGraphQL && r.Method == "POST" && r.Body.Type == model.BodyGraphQL &&
				strings.HasPrefix(r.Body.Raw, "query User") && r.Body.Variables == `{ "id": "{{userId}}" }`
		}},
		{grpcExample, func(r *model.Request) bool {
			return r.Kind == model.KindGRPC && r.Method == "" && r.Proto.Source == model.ProtoReflection &&
				r.Metadata[0].Value == "Bearer {{accessToken}}" && r.Messages[0].Name == "default"
		}},
		{wsExample, func(r *model.Request) bool {
			return r.Kind == model.KindWS && r.Headers[0].Key == "Sec-WebSocket-Protocol" && r.Messages[0].Content == `{ "type": "hello" }`
		}},
	}
	for _, c := range cases {
		r, o := mustRequest(t, c.src)
		if !c.want(r) {
			t.Errorf("%s: %+v", strings.SplitN(c.src, "\n", 2)[0], r)
		}
		if got := string(WriteRequest(r, o)); got != c.src {
			t.Errorf("round trip:\n%s", got)
		}
		if got := string(WriteRequest(r, nil)); got != c.src {
			t.Errorf("canonical:\n%s", got)
		}
	}
}

func TestCollectionAndEnvironment(t *testing.T) {
	ci, o, err := ReadCollection([]byte(collectionExample))
	if err != nil {
		t.Fatal(err)
	}
	if ci.Format != 1 || ci.ID != "3f6c2a1e-8d4b-4c1f-9a77-1b2e5d0c9e10" || ci.Name != "My API" || ci.DefaultEnv != "dev" ||
		!reflect.DeepEqual(ci.Order, []string{"auth", "users", "health.postgo"}) || ci.Config.Auth.Type != model.AuthNone {
		t.Errorf("ReadCollection = %+v", ci)
	}
	if got := string(WriteCollection(ci, o)); got != collectionExample {
		t.Errorf("round trip:\n%s", got)
	}
	if got := string(WriteCollection(ci, nil)); got != collectionExample {
		t.Errorf("canonical:\n%s", got)
	}

	e, eo, err := ReadEnvironment("dev", []byte(envExample))
	if err != nil {
		t.Fatal(err)
	}
	want := &model.Environment{ID: "dev", Name: "Development", Color: "green",
		Vars:    []model.KV{{Key: "baseUrl", Value: "https://api.dev.example.com"}, {Key: "username", Value: "tester", Desc: "测试账号"}},
		Secrets: []model.Secret{{Name: "accessToken", Desc: "值存在系统钥匙串里，文件里只有名字"}, {Name: "clientSecret"}}}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("ReadEnvironment = %+v", e)
	}
	if got := string(WriteEnvironment(e, eo)); got != envExample {
		t.Errorf("round trip:\n%s", got)
	}
	if got := string(WriteEnvironment(e, nil)); got != envExample {
		t.Errorf("canonical:\n%s", got)
	}
}

func TestFolder(t *testing.T) {
	src := `[headers]
X-Team: core
// X-Debug:

[auth apikey]
key = X-Api-Key
value = {{apiKey}}
in = query

[vars]
prefix = /v2

[script before-request]
~~~js
console.log("folder")
~~~

[settings]
verify-tls = false
max-redirects = 3
follow-redirects = true
encode-url = false
http-version = 1.1

[order]
list.postgo
admin
`
	cfg, order, o, err := ReadFolder([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Config{
		Headers:  []model.KV{{Key: "X-Team", Value: "core"}, {Key: "X-Debug", Disabled: true}},
		Auth:     model.Auth{Type: model.AuthAPIKey, APIKey: model.APIKeyAuth{Key: "X-Api-Key", Value: "{{apiKey}}", In: model.InQuery}},
		Vars:     []model.KV{{Key: "prefix", Value: "/v2"}},
		Scripts:  model.Scripts{BeforeRequest: `console.log("folder")`},
		Settings: model.Settings{VerifyTLS: model.Some(false), MaxRedirects: model.Some(3), FollowRedirects: model.Some(true), EncodeURL: model.Some(false), HTTPVersion: model.Some(model.HTTP11)},
	}
	if !reflect.DeepEqual(cfg, want) || !reflect.DeepEqual(order, []string{"list.postgo", "admin"}) {
		t.Errorf("ReadFolder = %+v %v", cfg, order)
	}
	if got := string(WriteFolder(cfg, order, o)); got != src {
		t.Errorf("round trip:\n%s", got)
	}
	// Written anew, settings come in the order of FORMAT.md.
	again, order2, _, err := ReadFolder(WriteFolder(cfg, order, nil))
	if err != nil || !reflect.DeepEqual(again, cfg) || !reflect.DeepEqual(order2, order) {
		t.Errorf("canonical read back = %+v %v %v", again, order2, err)
	}
	// An empty folder file is a folder that inherits.
	cfg, order, _, err = ReadFolder(nil)
	if err != nil || cfg.Auth.Type != model.AuthInherit || order != nil {
		t.Errorf("empty folder = %+v %v %v", cfg, order, err)
	}
	if got := WriteFolder(cfg, nil, nil); len(got) != 0 {
		t.Errorf("empty folder writes %q", got)
	}
}

func TestEditsKeepTheRest(t *testing.T) {
	src := `# A hand-written note above the request.
POST   {{baseUrl}}/users
Content-Type: application/json
#   odd spacing kept
X-Trace-Id: {{$uuid}}

[ meta ]
name = Create user

[query]
a = 1
b = 2


[future-section]
whatever = ~~~
[not a header]
~~~

[settings]
timeout = 10000ms
// max-redirects = 1
retries = 3
`
	r, o := mustRequest(t, src)
	if len(o.Warnings()) != 2 {
		t.Errorf("warnings = %v", o.Warnings())
	}
	if r.Method != "POST" || r.URL != "  {{baseUrl}}/users" {
		// The request line is split at its first space.
		t.Logf("request line read as %q %q", r.Method, r.URL)
	}
	if got := string(WriteRequest(r, o)); got != src {
		t.Fatalf("round trip:\n%s", got)
	}

	// Change one query parameter: only its line changes; the unknown
	// section, the note and the odd spacing stay.
	r.Query[1].Value = "3"
	got := string(WriteRequest(r, o))
	want := strings.Replace(src, "b = 2", "b = 3", 1)
	if got != want {
		t.Errorf("after editing a query parameter:\n%s", got)
	}

	// Change the timeout: the settings are written anew, keeping the
	// fields this version does not know and the disabled ones.
	r.Settings.Timeout = model.Some(5 * time.Second)
	got = string(WriteRequest(r, o))
	if !strings.Contains(got, "[settings]\ntimeout = 5s\n// max-redirects = 1\nretries = 3\n") {
		t.Errorf("after editing the timeout:\n%s", got)
	}
	if !strings.Contains(got, "[future-section]\nwhatever = ~~~\n[not a header]\n~~~\n") {
		t.Errorf("the unknown section changed:\n%s", got)
	}

	// Add a header and a body; remove the query.
	r.Headers = append(r.Headers, model.KV{Key: "Accept", Value: "*/*"})
	r.Body = model.Body{Type: model.BodyText, Raw: "hello"}
	r.Query = nil
	got = string(WriteRequest(r, o))
	wantPrefix := "# A hand-written note above the request.\nPOST   {{baseUrl}}/users\nContent-Type: application/json\n#   odd spacing kept\nX-Trace-Id: {{$uuid}}\nAccept: */*\n\n[ meta ]\nname = Create user\n\n[body text]\n~~~text\nhello\n~~~\n\n\n[future-section]"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("after adding and removing:\n%s", got)
	}
	r2, _ := mustRequest(t, got)
	if r2.Body.Raw != "hello" || r2.Query != nil || len(r2.Headers) != 3 {
		t.Errorf("read back: %+v", r2)
	}
}

func TestDefaultSectionsKept(t *testing.T) {
	src := "GET https://a\n\n[auth inherit]\n\n[query]\n"
	r, o := mustRequest(t, src)
	if got := string(WriteRequest(r, o)); got != src {
		t.Errorf("round trip:\n%q", got)
	}
	// Choosing another auth replaces the section.
	r.Auth = model.Auth{Type: model.AuthBasic, Basic: model.BasicAuth{Username: "u"}}
	got := string(WriteRequest(r, o))
	if got != "GET https://a\n\n[query]\n\n[auth basic]\nusername = u\n" && got != "GET https://a\n\n[auth basic]\nusername = u\n\n[query]\n" {
		t.Errorf("after choosing basic:\n%q", got)
	}
	if strings.Contains(got, "inherit") {
		t.Error("[auth inherit] kept next to [auth basic]")
	}
}

func TestEscapesAndFences(t *testing.T) {
	r := model.NewRequest(model.KindHTTP)
	r.ID, r.URL = "a.postgo", "https://a"
	r.Query = []model.KV{
		{Key: "#hash", Value: "1"},
		{Key: "[bracket]", Value: "2"},
		{Key: "~tilde", Value: "3"},
		{Key: `\back`, Value: "4"},
		{Key: "//slashes", Value: "5"},
		{Key: "/one", Value: "6"},
		{Key: "#off", Value: "7", Disabled: true},
		{Key: "empty", Value: ""},
		{Key: "spaces", Value: "  padded  "},
		{Key: "multi", Value: "line 1\nline 2"},
		{Key: "fence", Value: "~~~"},
		{Key: "eq", Value: "a = b ="},
		{Key: "described", Value: "x", Desc: "first\n\nthird"},
	}
	r.Body = model.Body{Type: model.BodyJSON, Raw: "~~~\n~~~~ inside\n[not a section]\n# not a description"}
	r.Scripts.Tests = ""
	r.Asserts = []model.Assert{{Expr: "#1"}, {Expr: "//x", Disabled: true}}
	out := WriteRequest(r, nil)
	got, o, err := ReadRequest("a.postgo", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("read back\n%+v\nwant\n%+v\n%s", got, r, out)
	}
	if !strings.Contains(string(out), "~~~~~json\n") {
		t.Errorf("the body's fence is not longer than its content's:\n%s", out)
	}
	if string(WriteRequest(got, o)) != string(out) {
		t.Error("round trip")
	}
}

func TestMultipartAndFileBodies(t *testing.T) {
	r := model.NewRequest(model.KindHTTP)
	r.ID, r.Method, r.URL = "a.postgo", "POST", "https://a"
	r.Body = model.Body{Type: model.BodyMultipart, Parts: []model.Part{
		{KV: model.KV{Key: "avatar", Value: "fixtures/a.png"}, File: true},
		{KV: model.KV{Key: "at", Value: "@literal"}},
		{KV: model.KV{Key: "back", Value: `\@x`}},
		{KV: model.KV{Key: "plain", Value: "text", Desc: "d", Disabled: true}},
		{KV: model.KV{Key: "multi", Value: "@a\nb"}},
	}}
	out := WriteRequest(r, nil)
	if !strings.Contains(string(out), "avatar = @fixtures/a.png\nat = \\@literal\n") {
		t.Errorf("multipart:\n%s", out)
	}
	got, _, err := ReadRequest("a.postgo", out)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Errorf("multipart read back = %+v %v\n%s", got, err, out)
	}
	r.Body = model.Body{Type: model.BodyFile, File: "fixtures/payload.bin"}
	out = WriteRequest(r, nil)
	got, _, err = ReadRequest("a.postgo", out)
	if err != nil || !reflect.DeepEqual(got.Body, r.Body) || !strings.Contains(string(out), "[body file]\npath = fixtures/payload.bin\n") {
		t.Errorf("file body:\n%s", out)
	}
	r.Body = model.Body{Type: model.BodyForm, Form: []model.KV{{Key: "a", Value: "1"}}}
	got, _, _ = ReadRequest("a.postgo", WriteRequest(r, nil))
	if !reflect.DeepEqual(got.Body, r.Body) {
		t.Errorf("form = %+v", got.Body)
	}
	r.Body = model.Body{Type: model.BodyForm}
	got, _, _ = ReadRequest("a.postgo", WriteRequest(r, nil))
	if got.Body.Type != model.BodyForm {
		t.Errorf("empty form = %+v", got.Body)
	}
}

func everyAuth() []model.Auth {
	return []model.Auth{
		{Type: model.AuthNone},
		{Type: model.AuthBasic, Basic: model.BasicAuth{Username: "u", Password: "{{p}}"}},
		{Type: model.AuthBearer, Bearer: model.BearerAuth{Token: "t", Prefix: "Token"}},
		{Type: model.AuthAPIKey, APIKey: model.APIKeyAuth{Key: "k", Value: "v", In: model.InHeader}},
		{Type: model.AuthDigest, Digest: model.DigestAuth{Username: "u", Password: "p", Realm: "r", Nonce: "n", Algorithm: "SHA-256", QOP: "auth", NonceCount: "1", ClientNonce: "c", Opaque: "o"}},
		{Type: model.AuthOAuth1, OAuth1: model.OAuth1Auth{ConsumerKey: "ck", ConsumerSecret: "cs", Token: "t", TokenSecret: "ts", SignatureMethod: "RSA-SHA256",
			PrivateKey: "-----BEGIN KEY-----\nabc\n-----END KEY-----", Realm: "r", Callback: "c", Verifier: "v", Nonce: "n", Timestamp: "1", Version: "1.0", In: model.InBody, IncludeBodyHash: true, AddEmptyParams: true}},
		{Type: model.AuthOAuth2, OAuth2: model.OAuth2Auth{Grant: model.GrantAuthorizationCode, AuthURL: "a", TokenURL: "t", RefreshURL: "r", ClientID: "id", ClientSecret: "{{s}}",
			Scope: "read write", Audience: "aud", Resource: "res", RedirectURI: "http://localhost/cb", State: "st", Username: "u", Password: "p", PKCE: true, PKCEMethod: "S256",
			CodeVerifier: "cv", ClientAuth: "body", TokenPrefix: "Bearer", In: model.InQuery, HeaderName: "X-Token", QueryParam: "tok", AutoRefresh: true,
			AuthParams: []model.KV{{Key: "prompt", Value: "consent"}, {Key: "x", Value: "y", Disabled: true, Desc: "d"}}, TokenParams: []model.KV{{Key: "a.b", Value: "c"}}}},
		{Type: model.AuthAWSV4, AWSV4: model.AWSV4Auth{AccessKey: "a", SecretKey: "s", SessionToken: "t", Region: "r", Service: "s3"}},
		{Type: model.AuthNTLM, NTLM: model.NTLMAuth{Username: "u", Password: "p", Domain: "d", Workstation: "w"}},
		{Type: model.AuthJWT, JWT: model.JWTAuth{Algorithm: "HS256", Secret: "s", SecretBase64: true, PrivateKey: "k", Payload: "{\n  \"sub\": 1\n}", Header: `{"kid":"1"}`, In: model.InQuery, Prefix: "Bearer", QueryParam: "jwt"}},
	}
}

func TestEveryAuth(t *testing.T) {
	for _, a := range everyAuth() {
		r := model.NewRequest(model.KindHTTP)
		r.URL, r.Auth = "https://a", a
		out := WriteRequest(r, nil)
		got, _, err := ReadRequest("", out)
		if err != nil || !reflect.DeepEqual(got.Auth, a) {
			t.Errorf("%s: read back %+v %v\n%s", a.Type, got.Auth, err, out)
		}
	}
	r := model.NewRequest(model.KindHTTP)
	r.URL = "https://a"
	r.Auth = everyAuth()[6]
	out := string(WriteRequest(r, nil))
	for _, key := range []string{"auth-url = a", "client-id = id", "pkce = true", "pkce-method = S256", "redirect-uri = ", "auth-param.prompt = consent", "// auth-param.x = y", "token-param.a.b = c", "auto-refresh = true"} {
		if !strings.Contains(out, key) {
			t.Errorf("OAuth 2.0 has no %q:\n%s", key, out)
		}
	}
}

func TestKebab(t *testing.T) {
	for in, want := range map[string]string{"AuthURL": "auth-url", "PKCEMethod": "pkce-method", "ClientID": "client-id", "QOP": "qop",
		"SecretBase64": "secret-base64", "In": "in", "IncludeBodyHash": "include-body-hash", "AuthParams": "auth-params", "RedirectURI": "redirect-uri"} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEverything(t *testing.T) {
	r := model.NewRequest(model.KindHTTP)
	r.ID, r.Name, r.Tags, r.Method, r.URL = "x.postgo", "All", []string{"a"}, "PATCH", "https://a/:id"
	r.Path = []model.KV{{Key: "id", Value: "1"}}
	r.Vars = []model.KV{{Key: "v", Value: "1"}}
	r.Scripts = model.Scripts{BeforeRequest: "a()", AfterResponse: "b()", Tests: "c()"}
	r.Settings = model.Settings{Timeout: model.Some(90 * time.Second), FollowRedirects: model.Some(false), MaxRedirects: model.Some(0),
		VerifyTLS: model.Some(true), HTTPVersion: model.Some(model.HTTP2), EncodeURL: model.Some(true)}
	r.Examples = []model.Example{{Name: "Created user", Status: 201, Body: "{}"}, {Name: "Empty", Status: 204}}
	r.Docs = "# Title\n\ntext"
	out := WriteRequest(r, nil)
	got, o, err := ReadRequest("x.postgo", out)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("read back %+v %v\n%s", got, err, out)
	}
	if string(WriteRequest(got, o)) != string(out) {
		t.Error("round trip")
	}

	g := model.NewRequest(model.KindGraphQL)
	g.URL, g.Method = "https://a/graphql", "GET"
	out = WriteRequest(g, nil)
	got, _, err = ReadRequest("", out)
	if err != nil || got.Method != "GET" || !strings.Contains(string(out), "method = GET") {
		t.Errorf("GraphQL GET:\n%s", out)
	}
	p := model.NewRequest(model.KindGRPC)
	p.URL, p.Proto = "h/a.B/C", model.Proto{Source: model.ProtoFile, File: "protos/a.proto"}
	got, _, err = ReadRequest("", WriteRequest(p, nil))
	if err != nil || got.Proto != p.Proto {
		t.Errorf("proto file = %+v %v", got.Proto, err)
	}
	s := model.NewRequest(model.KindSSE)
	s.URL = "https://a/events"
	got, _, err = ReadRequest("", WriteRequest(s, nil))
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Errorf("SSE = %+v %v", got, err)
	}
}

func TestCRLFAndNoFinalNewline(t *testing.T) {
	src := "GET https://a\r\nAccept: */*\r\n\r\n[query]\r\na = 1\r\n"
	r, o := mustRequest(t, src)
	if got := string(WriteRequest(r, o)); got != src {
		t.Errorf("CRLF round trip %q", got)
	}
	r.Query = append(r.Query, model.KV{Key: "b", Value: "2"})
	if got := string(WriteRequest(r, o)); got != "GET https://a\r\nAccept: */*\r\n\r\n[query]\r\na = 1\r\nb = 2\r\n" {
		t.Errorf("CRLF edit %q", got)
	}
	r.Docs = "d"
	if got := string(WriteRequest(r, o)); !strings.HasSuffix(got, "b = 2\r\n\r\n[docs]\r\n~~~md\r\nd\r\n~~~\r\n") {
		t.Errorf("CRLF new section %q", got)
	}

	src = "GET https://a\n\n[query]\na = 1"
	r, o = mustRequest(t, src)
	if got := string(WriteRequest(r, o)); got != src {
		t.Errorf("no final newline %q", got)
	}
	r.Docs = "d"
	if got := string(WriteRequest(r, o)); got != src+"\n\n[docs]\n~~~md\nd\n~~~\n" {
		t.Errorf("appending to a file without a final newline %q", got)
	}
	src = "GET https://a"
	r, o = mustRequest(t, src)
	r.Headers = []model.KV{{Key: "A", Value: "1"}}
	if got := string(WriteRequest(r, o)); got != "GET https://a\nA: 1\n" {
		t.Errorf("header after a line without a newline %q", got)
	}
}

func TestTrailingBlankLines(t *testing.T) {
	src := "GET https://a\n\n[query]\na = 1\n\n\n"
	r, o := mustRequest(t, src)
	if got := string(WriteRequest(r, o)); got != src {
		t.Errorf("%q", got)
	}
	r.Query[0].Value = "2"
	if got := string(WriteRequest(r, o)); got != "GET https://a\n\n[query]\na = 2\n\n\n" {
		t.Errorf("%q", got)
	}
}

func problems(err error) []Problem {
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe.Problems
	}
	return nil
}

func TestErrors(t *testing.T) {
	cases := []struct {
		src  string
		line int
		msg  string
	}{
		{"", 1, "starts with its method"},
		{"# only a note\n", 1, "starts with its method"},
		{"[query]\na = 1\n", 1, "starts with its method"},
		{"GET x\nBadHeader\n", 2, "Name: value"},
		{"GET x\n\n[query]\nno separator\n", 4, "key = value"},
		{"GET x\n\n[query]\n# lonely\n\na = 1\n", 4, "right above an entry"},
		{"GET x\n\n[query]\n# lonely\n", 4, "right above an entry"},
		{"GET x\n\nstray\n", 3, "expected a section"},
		{"GET x\n\n[]\n", 3, "needs a name"},
		{"GET x\n\n[body json]\nnot a block\n", 4, "between ~~~"},
		{"GET x\n\n[body json]\n~~~\nopen\n", 4, "not closed"},
		{"GET x\n\n[body json]\n~~~\n~~~\n~~~\n~~~\n", 6, "one block"},
		{"GET x\n\n[query]\na = ~~~\nb\n", 4, "not closed"},
		{"GET x\n\n[query]\n\n[query]\n", 5, "already"},
		{"GET x\n\n[body json]\n~~~\n~~~\n\n[body text]\n~~~\n~~~\n", 7, "already"},
		{"GET x\n\n[auth kerberos]\n", 3, "not an auth type"},
		{"GET x\n\n[settings]\ntimeout = soon\n", 4, "timeout"},
		{"GET x\n\n[settings]\nverify-tls = yes\n", 4, "true or false"},
		{"GET x\n\n[settings]\nmax-redirects = many\n", 4, "max-redirects"},
		{"GET x\n\n[settings]\ntimeout = 1s\ntimeout = 2s\n", 5, "twice"},
		{"GET x\n\n[auth basic]\nusername = a\nusername = b\n", 5, "twice"},
		{"GET x\n\n[auth bearer]\n// token = a\ntoken = b\n", 0, ""},
		{"GET x\n\n[example a]\nstatus = x\n", 4, "status"},
		{"GET x\n\n[example a]\n~~~\n~~~\n~~~\n~~~\n", 6, "one body"},
		{"GRPC h/a.B/C\n\n[proto]\nreflection = maybe\n", 4, "true or false"},
		{"GET x\n\n[auth oauth2]\npkce = sure\n", 4, "true or false"},
	}
	for _, c := range cases {
		_, _, err := ReadRequest("", []byte(c.src))
		if c.msg == "" {
			if err != nil {
				t.Errorf("%q: %v", c.src, err)
			}
			continue
		}
		ps := problems(err)
		found := false
		for _, p := range ps {
			if p.Severity == model.Error && p.Line == c.line && strings.Contains(p.Msg, c.msg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want an error at line %d with %q, got %v", c.src, c.line, c.msg, err)
		}
	}
	if err := (&ParseError{Problems: []Problem{{Line: 2, Severity: model.Error, Msg: "bad"}, {Line: 3, Msg: "meh"}}}).Error(); err != "pgfile: line 2: error: bad" {
		t.Errorf("Error() = %q", err)
	}
}

func TestCollectionErrors(t *testing.T) {
	if _, _, err := ReadCollection([]byte("[order]\na\n")); err == nil || !strings.Contains(err.Error(), "[postgo]") {
		t.Errorf("no [postgo]: %v", err)
	}
	if _, _, err := ReadCollection([]byte("[postgo]\nname = x\n")); err == nil || !strings.Contains(err.Error(), "format") {
		t.Errorf("no format: %v", err)
	}
	if _, _, err := ReadCollection([]byte("[postgo]\nformat = 2\nnewfield = x\n")); err != ErrNewerFormat {
		t.Errorf("newer format: %v", err)
	}
	if _, _, err := ReadCollection([]byte("[postgo]\nformat = one\n")); err == nil {
		t.Error("bad format number")
	}
	if _, _, _, err := ReadFolder([]byte("stray\n")); err == nil {
		t.Error("folder with content outside sections")
	}
	if _, _, err := ReadEnvironment("dev", []byte("[vars]\nbad\n")); err == nil {
		t.Error("environment with a bad row")
	}
	// Sections of other kinds of files are unknown here, and kept.
	src := "[postgo]\nformat = 1\nid = x\nname = n\n\n[meta]\nname = m\n"
	ci, o, err := ReadCollection([]byte(src))
	if err != nil || len(o.Warnings()) != 1 || string(WriteCollection(ci, o)) != src {
		t.Errorf("unknown section in a collection: %v %v", err, o.Warnings())
	}
	_, eo, err := ReadEnvironment("dev", []byte("[headers]\nA: b\n"))
	if err != nil || len(eo.Warnings()) != 1 {
		t.Errorf("unknown section in an environment: %v", err)
	}
	if (*Origin)(nil).Warnings() != nil {
		t.Error("Warnings of nil")
	}
}

func TestCollectionRootAuth(t *testing.T) {
	ci := CollectionInfo{ID: "i", Name: "n", Config: model.Config{Auth: model.Auth{Type: model.AuthInherit}}}
	// The root's default is none, so an inherit (invalid) is written out
	// rather than lost.
	out := WriteCollection(ci, nil)
	got, _, err := ReadCollection(out)
	if err != nil || got.Config.Auth.Type != model.AuthInherit || got.Format != 1 {
		t.Errorf("%+v %v\n%s", got, err, out)
	}
}

func TestUnknownFieldsWarn(t *testing.T) {
	src := "GET x\n\n[meta]\nname = a\ncolour = blue\n\n[auth none]\nfoo = bar\n\n[body file]\npath = a\nmode = 1\n\n[example e]\nstatus = 200\nx = 1\n"
	r, o := mustRequest(t, src)
	if len(o.Warnings()) != 4 {
		t.Errorf("warnings = %v", o.Warnings())
	}
	if r.Name != "a" || r.Body.File != "a" || r.Examples[0].Status != 200 {
		t.Errorf("%+v", r)
	}
	if string(WriteRequest(r, o)) != src {
		t.Error("round trip")
	}
	r.Name = "b"
	if got := string(WriteRequest(r, o)); !strings.Contains(got, "[meta]\nname = b\ncolour = blue\n") {
		t.Errorf("unknown field lost:\n%s", got)
	}
}

func TestCustomMethodsAndKinds(t *testing.T) {
	for line, kind := range map[string]model.Kind{"graphql x": model.KindGraphQL, "Ws x": model.KindWS, "PROPFIND x": model.KindHTTP} {
		r, _ := mustRequest(t, line)
		if r.Kind != kind {
			t.Errorf("%q: kind %s", line, r.Kind)
		}
	}
	r, _ := mustRequest(t, "GET")
	if r.Method != "GET" || r.URL != "" {
		t.Errorf("no URL: %+v", r)
	}
}

func bareCR(src []byte) bool {
	return strings.Contains(strings.ReplaceAll(string(src), "\r\n", ""), "\r")
}

func FuzzRequest(f *testing.F) {
	for _, s := range []string{formatExample, graphqlExample, grpcExample, wsExample, "GET x\n\n[query]\na = ~~~\nb\n~~~\n",
		"GET x\r\n\r\n[unknown]\r\nz = ~~~~\r\n~~~\r\n~~~~\r\n", "POST x\n# d\n// H: v\n\n[body multipart]\nf = @a\nt = \\@b\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		r, o, err := ReadRequest("f.postgo", src)
		if err != nil {
			return
		}
		if out := WriteRequest(r, o); string(out) != string(src) {
			t.Fatalf("round trip of %q gave %q", src, out)
		}
		if bareCR(src) {
			return // a CR that ends no line cannot be written anew (FORMAT.md 3)
		}
		canon := WriteRequest(r, nil)
		r2, o2, err := ReadRequest("f.postgo", canon)
		if err != nil {
			t.Fatalf("canonical %q of %q does not read: %v", canon, src, err)
		}
		if !reflect.DeepEqual(r, r2) {
			t.Fatalf("canonical %q of %q reads as\n%+v\nnot\n%+v", canon, src, r2, r)
		}
		if out := WriteRequest(r2, o2); string(out) != string(canon) {
			t.Fatalf("canonical round trip %q gave %q", canon, out)
		}
		// Edited and written over the file, the request reads back as
		// edited.
		for i, edit := range edits {
			e := r.Clone()
			edit(e)
			out := WriteRequest(e, o)
			got, _, err := ReadRequest("f.postgo", out)
			if err != nil {
				t.Fatalf("edit %d of %q wrote %q, which does not read: %v", i, src, out, err)
			}
			if !reflect.DeepEqual(got, e) {
				t.Fatalf("edit %d of %q wrote %q, which reads as\n%+v\nnot\n%+v", i, src, out, got, e)
			}
		}
	})
}

// edits change requests in the ways the UI does.
var edits = []func(*model.Request){
	func(r *model.Request) { r.Headers = append(r.Headers, model.KV{Key: "X-New", Value: "1"}) },
	func(r *model.Request) { r.Headers = nil },
	func(r *model.Request) { r.Name = "Renamed" },
	func(r *model.Request) { r.Name, r.Tags = "", nil },
	func(r *model.Request) { r.Docs = "new docs" },
	func(r *model.Request) { r.Query = append([]model.KV{{Key: "first", Value: "1"}}, r.Query...) },
	func(r *model.Request) { r.Query, r.Path, r.Vars, r.Metadata = nil, nil, nil, nil },
	func(r *model.Request) { r.Body = model.Body{Type: model.BodyText, Raw: "~~~\nbody"} },
	func(r *model.Request) {
		r.Auth = model.Auth{Type: model.AuthBasic, Basic: model.BasicAuth{Username: "u"}}
	},
	func(r *model.Request) { r.Auth = model.Auth{Type: model.AuthInherit} },
	func(r *model.Request) { r.Settings = model.Settings{Timeout: model.Some(time.Second)} },
	func(r *model.Request) { r.Scripts = model.Scripts{Tests: "test()"} },
	func(r *model.Request) { r.Asserts = append(r.Asserts, model.Assert{Expr: "res.status == 200"}) },
	func(r *model.Request) { r.Examples = nil; r.Messages = nil },
}

func FuzzEnvironment(f *testing.F) {
	f.Add([]byte(envExample))
	f.Add([]byte(collectionExample))
	f.Fuzz(func(t *testing.T, src []byte) {
		e, o, err := ReadEnvironment("e", src)
		if err != nil {
			return
		}
		if out := WriteEnvironment(e, o); string(out) != string(src) {
			t.Fatalf("round trip of %q gave %q", src, out)
		}
		if bareCR(src) {
			return
		}
		e2, _, err := ReadEnvironment("e", WriteEnvironment(e, nil))
		if err != nil || !reflect.DeepEqual(e, e2) {
			t.Fatalf("canonical of %q: %v %+v", src, err, e2)
		}
	})
}

func FuzzFolder(f *testing.F) {
	f.Add([]byte(collectionExample))
	f.Add([]byte("[headers]\nA: 1\n// B:\n\n[auth bearer]\ntoken = t\n\n[order]\na\n\\#b\n"))
	f.Fuzz(func(t *testing.T, src []byte) {
		if cfg, order, o, err := ReadFolder(src); err == nil {
			if out := WriteFolder(cfg, order, o); string(out) != string(src) {
				t.Fatalf("folder round trip of %q gave %q", src, out)
			}
			if !bareCR(src) {
				cfg2, order2, _, err := ReadFolder(WriteFolder(cfg, order, nil))
				if err != nil || !reflect.DeepEqual(cfg, cfg2) || !reflect.DeepEqual(order, order2) {
					t.Fatalf("canonical folder of %q: %v", src, err)
				}
			}
		}
		if ci, o, err := ReadCollection(src); err == nil {
			if out := WriteCollection(ci, o); string(out) != string(src) && ci.Format == model.FormatVersion {
				t.Fatalf("collection round trip of %q gave %q", src, out)
			}
		}
	})
}
