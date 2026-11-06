package model

import (
	"strings"
	"testing"
	"time"
)

// validRequests are requests of every kind that have no issues.
func validRequests() map[string]*Request {
	h := NewRequest(KindHTTP)
	h.ID, h.Name, h.Method, h.URL = "users/create-user.postgo", "Create user", "POST", "{{baseUrl}}/users/:org"
	h.Tags = []string{"users", "write"}
	h.Query = []KV{{Key: "verbose", Value: "true"}, {Key: "dryRun", Value: "true", Disabled: true, Desc: "line 1\nline 2"}}
	h.Path = []KV{{Key: "org", Value: "{{org}}"}}
	h.Headers = []KV{{Key: "Content-Type", Value: "application/json"}, {Key: "X-Trace-Id", Value: "{{$uuid}}"}}
	h.Body = Body{Type: BodyJSON, Raw: "{\n  \"name\": \"{{name}}\"\n}"}
	h.Auth = Auth{Type: AuthBearer, Bearer: BearerAuth{Token: "{{accessToken}}"}}
	h.Vars = []KV{{Key: "user.id", Value: "1"}}
	h.Scripts = Scripts{AfterResponse: `pg.env.set("userId", res.json().id)`}
	h.Asserts = []Assert{{Expr: "res.status == 201"}}
	h.Settings = Settings{Timeout: Some(10 * time.Second), HTTPVersion: Some(HTTP2)}
	h.Examples = []Example{{Name: "Created user", Status: 201, Body: "{}"}}
	h.Docs = "Creates a user."

	g := NewRequest(KindGraphQL)
	g.URL = "https://api.example.com/graphql"
	g.Body.Raw, g.Body.Variables = "query { me { id } }", `{"id": 1}`

	ws := NewRequest(KindWS)
	ws.URL = "wss://{{host}}/ws"
	ws.Headers = []KV{{Key: "Sec-WebSocket-Protocol", Value: "chat"}}
	ws.Messages = []Message{{Name: "hello", Content: `{"type":"hello"}`}}

	sse := NewRequest(KindSSE)
	sse.URL = "{{baseUrl}}/events"

	rpc := NewRequest(KindGRPC)
	rpc.URL = "{{grpcHost}}/users.v1.UserService/GetUser"
	rpc.Metadata = []KV{{Key: "authorization", Value: "Bearer {{token}}"}}
	rpc.Messages = []Message{{Name: "default", Content: `{"id": "1"}`}}

	rpcFile := NewRequest(KindGRPC)
	rpcFile.URL = "localhost:50051/Greeter/SayHello"
	rpcFile.Proto = Proto{Source: ProtoFile, File: "protos/greeter.proto"}

	form := NewRequest(KindHTTP)
	form.Method, form.URL = "PUT", "http://localhost:8080/upload"
	form.Body = Body{Type: BodyMultipart, Parts: []Part{{KV: KV{Key: "avatar", Value: "fixtures/a.png"}, File: true}, {KV: KV{Key: "name", Value: "a"}}}}

	custom := NewRequest(KindHTTP)
	custom.Method, custom.URL = "PROPFIND", "https://dav.example.com/files"

	return map[string]*Request{"http": h, "graphql": g, "ws": ws, "sse": sse, "grpc": rpc, "grpc-file": rpcFile, "multipart": form, "custom-method": custom}
}

func TestValidRequests(t *testing.T) {
	for name, r := range validRequests() {
		if is := r.Validate(); len(is) > 0 {
			t.Errorf("%s: %v", name, is)
		}
	}
}

// hasIssue reports whether issues has one of the severity and code whose
// field starts with field.
func hasIssue(is Issues, sev Severity, code, field string) bool {
	for _, i := range is {
		if i.Severity == sev && i.Code == code && strings.HasPrefix(i.Field, field) {
			return true
		}
	}
	return false
}

func TestRequestIssues(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		edit  func(*Request)
		sev   Severity
		code  string
		field string
	}{
		{"no kind", "http", func(r *Request) { r.Kind = "" }, Error, CodeRequired, "kind"},
		{"bad kind", "http", func(r *Request) { r.Kind = "ftp" }, Error, CodeInvalid, "kind"},
		{"bad method", "http", func(r *Request) { r.Method = "GE T" }, Error, CodeInvalid, "method"},
		{"empty method", "http", func(r *Request) { r.Method = "" }, Error, CodeInvalid, "method"},
		{"reserved method", "http", func(r *Request) { r.Method = "grpc" }, Error, CodeReserved, "method"},
		{"graphql method", "graphql", func(r *Request) { r.Method = "PUT" }, Error, CodeInvalid, "method"},
		{"ws method", "ws", func(r *Request) { r.Method = "GET" }, Error, CodeNotApplicable, "method"},
		{"no url", "http", func(r *Request) { r.URL = "" }, Error, CodeRequired, "url"},
		{"url break", "http", func(r *Request) { r.URL = "http://a\n/b" }, Error, CodeSingleLine, "url"},
		{"url spaces", "http", func(r *Request) { r.URL = " http://a" }, Error, CodeUntrimmed, "url"},
		{"url inner space", "http", func(r *Request) { r.URL = "http://a/b c" }, Error, CodeInvalid, "url"},
		{"query in url", "http", func(r *Request) { r.URL = "http://a/b?c=1" }, Error, CodeInvalid, "url"},
		{"bad grpc url", "grpc", func(r *Request) { r.URL = "localhost:50051/GetUser" }, Error, CodeInvalid, "url"},
		{"grpc url no host", "grpc", func(r *Request) { r.URL = "/a.B/C" }, Error, CodeInvalid, "url"},
		{"ws http url", "ws", func(r *Request) { r.URL = "http://a/ws" }, Warning, CodeInvalid, "url"},
		{"http ws url", "http", func(r *Request) { r.URL = "ws://a/ws" }, Warning, CodeInvalid, "url"},
		{"name break", "http", func(r *Request) { r.Name = "a\nb" }, Error, CodeSingleLine, "name"},
		{"tag comma", "http", func(r *Request) { r.Tags = []string{"a,b"} }, Error, CodeInvalid, "tags[0]"},
		{"tag twice", "http", func(r *Request) { r.Tags = []string{"a", "a"} }, Error, CodeDuplicate, "tags[1]"},
		{"tag empty", "http", func(r *Request) { r.Tags = []string{""} }, Error, CodeRequired, "tags[0]"},
		{"query key empty", "http", func(r *Request) { r.Query[0].Key = "" }, Error, CodeRequired, "query[0].key"},
		{"query key sep", "http", func(r *Request) { r.Query[0].Key = "a = b" }, Error, CodeInvalid, "query[0].key"},
		{"query key trailing eq", "http", func(r *Request) { r.Query[0].Key = "a =" }, Error, CodeInvalid, "query[0].key"},
		{"query key var", "http", func(r *Request) { r.Query[0].Key = "{{k}}" }, Error, CodeInvalid, "query[0].key"},
		{"query key untrimmed", "http", func(r *Request) { r.Query[0].Key = "a " }, Error, CodeUntrimmed, "query[0].key"},
		{"header name", "http", func(r *Request) { r.Headers[0].Key = "Content Type" }, Error, CodeInvalid, "headers[0].key"},
		{"header colon", "http", func(r *Request) { r.Headers[0].Key = "A:B" }, Error, CodeInvalid, "headers[0].key"},
		{"header value break", "http", func(r *Request) { r.Headers[0].Value = "a\r\nb" }, Error, CodeSingleLine, "headers[0].value"},
		{"header value spaces", "http", func(r *Request) { r.Headers[0].Value = "a " }, Error, CodeUntrimmed, "headers[0].value"},
		{"path param missing", "http", func(r *Request) { r.Path[0].Key = "team" }, Warning, CodeInvalid, "path[0].key"},
		{"path on ws", "ws", func(r *Request) { r.Path = []KV{{Key: "a"}} }, Error, CodeNotApplicable, "path"},
		{"query on grpc", "grpc", func(r *Request) { r.Query = []KV{{Key: "a"}} }, Error, CodeNotApplicable, "query"},
		{"headers on grpc", "grpc", func(r *Request) { r.Headers = []KV{{Key: "a"}} }, Error, CodeNotApplicable, "headers"},
		{"messages on http", "http", func(r *Request) { r.Messages = []Message{{Name: "a"}} }, Error, CodeNotApplicable, "messages"},
		{"metadata on ws", "ws", func(r *Request) { r.Metadata = []KV{{Key: "a"}} }, Error, CodeNotApplicable, "metadata"},
		{"proto on http", "http", func(r *Request) { r.Proto = Proto{Source: ProtoReflection} }, Error, CodeNotApplicable, "proto"},
		{"examples on sse", "sse", func(r *Request) { r.Examples = []Example{{Name: "a", Status: 200}} }, Error, CodeNotApplicable, "examples"},
		{"no body type", "http", func(r *Request) { r.Body.Type = "" }, Error, CodeRequired, "body.type"},
		{"bad body type", "http", func(r *Request) { r.Body.Type = "yaml" }, Error, CodeInvalid, "body.type"},
		{"graphql json body", "graphql", func(r *Request) { r.Body.Type = BodyJSON }, Error, CodeInvalid, "body.type"},
		{"ws body", "ws", func(r *Request) { r.Body.Type = BodyJSON }, Error, CodeNotApplicable, "body.type"},
		{"form key", "http", func(r *Request) { r.Body = Body{Type: BodyForm, Form: []KV{{Key: ""}}} }, Error, CodeRequired, "body.form[0].key"},
		{"part path backslash", "multipart", func(r *Request) { r.Body.Parts[0].Value = `fixtures\a.png` }, Error, CodePath, "body.parts[0].value"},
		{"part path absolute", "multipart", func(r *Request) { r.Body.Parts[0].Value = "/tmp/a.png" }, Warning, CodePath, "body.parts[0].value"},
		{"part path windows", "multipart", func(r *Request) { r.Body.Parts[0].Value = "C:/a.png" }, Warning, CodePath, "body.parts[0].value"},
		{"part path unclean", "multipart", func(r *Request) { r.Body.Parts[0].Value = "./a.png" }, Error, CodePath, "body.parts[0].value"},
		{"part path outside", "multipart", func(r *Request) { r.Body.Parts[0].Value = "../a.png" }, Warning, CodePath, "body.parts[0].value"},
		{"part path empty", "multipart", func(r *Request) { r.Body.Parts[0].Value = "" }, Error, CodeRequired, "body.parts[0].value"},
		{"part key", "multipart", func(r *Request) { r.Body.Parts[1].Key = "" }, Error, CodeRequired, "body.parts[1].key"},
		{"file body", "http", func(r *Request) { r.Body = Body{Type: BodyFile} }, Error, CodeRequired, "body.file"},
		{"file body break", "http", func(r *Request) { r.Body = Body{Type: BodyFile, File: "a\nb"} }, Error, CodeSingleLine, "body.file"},
		{"var name", "http", func(r *Request) { r.Vars[0].Key = "1a" }, Error, CodeInvalid, "vars[0].key"},
		{"var twice", "http", func(r *Request) { r.Vars = []KV{{Key: "a"}, {Key: "a"}} }, Warning, CodeDuplicate, "vars[1].key"},
		{"assert empty", "http", func(r *Request) { r.Asserts[0].Expr = "" }, Error, CodeRequired, "asserts[0].expr"},
		{"assert break", "http", func(r *Request) { r.Asserts[0].Expr = "a\nb" }, Error, CodeSingleLine, "asserts[0].expr"},
		{"negative timeout", "http", func(r *Request) { r.Settings.Timeout = Some(-time.Second) }, Error, CodeInvalid, "settings.timeout"},
		{"negative redirects", "http", func(r *Request) { r.Settings.MaxRedirects = Some(-1) }, Error, CodeInvalid, "settings.maxRedirects"},
		{"bad http version", "http", func(r *Request) { r.Settings.HTTPVersion = Some(HTTPVersion("4")) }, Error, CodeInvalid, "settings.httpVersion"},
		{"message name", "ws", func(r *Request) { r.Messages[0].Name = "" }, Error, CodeRequired, "messages[0].name"},
		{"message twice", "ws", func(r *Request) { r.Messages = append(r.Messages, r.Messages[0]) }, Error, CodeDuplicate, "messages[1].name"},
		{"metadata key", "grpc", func(r *Request) { r.Metadata[0].Key = "a = b" }, Error, CodeInvalid, "metadata[0].key"},
		{"proto source", "grpc", func(r *Request) { r.Proto.Source = "" }, Error, CodeRequired, "proto.source"},
		{"proto file missing", "grpc", func(r *Request) { r.Proto = Proto{Source: ProtoFile} }, Error, CodeRequired, "proto.file"},
		{"proto file unused", "grpc", func(r *Request) { r.Proto.File = "a.proto" }, Error, CodeNotApplicable, "proto.file"},
		{"example status", "http", func(r *Request) { r.Examples[0].Status = 0 }, Error, CodeInvalid, "examples[0].status"},
		{"example name", "http", func(r *Request) { r.Examples[0].Name = " a" }, Error, CodeUntrimmed, "examples[0].name"},
		{"id ext", "http", func(r *Request) { r.ID = "users/a.txt" }, Error, CodeInvalid, "id"},
		{"id reserved", "http", func(r *Request) { r.ID = "users/_folder.postgo" }, Error, CodeReserved, "id"},
		{"id hidden", "http", func(r *Request) { r.ID = ".a.postgo" }, Error, CodeReserved, "id"},
		{"id envs", "http", func(r *Request) { r.ID = "environments/a.postgo" }, Error, CodeReserved, "id"},
		{"id absolute", "http", func(r *Request) { r.ID = "/a.postgo" }, Error, CodePath, "id"},
		{"id outside", "http", func(r *Request) { r.ID = "../a.postgo" }, Error, CodePath, "id"},
		{"id device", "http", func(r *Request) { r.ID = "con.postgo" }, Error, CodeReserved, "id"},
		{"id bad char", "http", func(r *Request) { r.ID = "a?b.postgo" }, Error, CodeInvalid, "id"},
		{"id unclean", "http", func(r *Request) { r.ID = "a//b.postgo" }, Error, CodePath, "id"},
	}
	for _, c := range cases {
		r := validRequests()[c.base]
		c.edit(r)
		is := r.Validate()
		if !hasIssue(is, c.sev, c.code, c.field) {
			t.Errorf("%s: want %s %s at %s, got %v", c.name, c.sev, c.code, c.field, is)
		}
		if c.sev == Warning && is.HasErrors() {
			t.Errorf("%s: want only warnings, got %v", c.name, is)
		}
	}
}

func authRequest(a Auth) *Request {
	r := NewRequest(KindHTTP)
	r.URL, r.Auth = "http://a", a
	return r
}

func TestValidAuths(t *testing.T) {
	auths := []Auth{
		{Type: AuthInherit},
		{Type: AuthNone},
		{Type: AuthBasic, Basic: BasicAuth{Username: "u", Password: "{{p}}"}},
		{Type: AuthBearer, Bearer: BearerAuth{Token: "{{t}}", Prefix: "Token"}},
		{Type: AuthAPIKey, APIKey: APIKeyAuth{Key: "X-Api-Key", Value: "{{k}}"}},
		{Type: AuthAPIKey, APIKey: APIKeyAuth{Key: "api key", Value: "{{k}}", In: InQuery}},
		{Type: AuthDigest, Digest: DigestAuth{Username: "u", Algorithm: "SHA-256", QOP: "auth"}},
		{Type: AuthOAuth1, OAuth1: OAuth1Auth{ConsumerKey: "k", SignatureMethod: "HMAC-SHA1", In: InBody}},
		{Type: AuthOAuth1, OAuth1: OAuth1Auth{ConsumerKey: "k", SignatureMethod: "RSA-SHA256", PrivateKey: "pem"}},
		{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantAuthorizationCode, AuthURL: "a", TokenURL: "t", ClientID: "c", PKCE: true, PKCEMethod: "S256", ClientAuth: "body", HeaderName: "X-Token", AuthParams: []KV{{Key: "prompt", Value: "consent"}}}},
		{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", In: InQuery}},
		{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantPassword, TokenURL: "t", ClientID: "c", Username: "u"}},
		{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantImplicit, AuthURL: "a", ClientID: "c"}},
		{Type: AuthAWSV4, AWSV4: AWSV4Auth{AccessKey: "a", SecretKey: "s", Region: "us-east-1", Service: "s3"}},
		{Type: AuthNTLM, NTLM: NTLMAuth{Username: "u", Domain: "d"}},
		{Type: AuthJWT, JWT: JWTAuth{Algorithm: "HS256", Secret: "s", Payload: `{"sub":"1"}`}},
		{Type: AuthJWT, JWT: JWTAuth{Algorithm: "ES256", PrivateKey: "pem", In: InQuery}},
	}
	for _, a := range auths {
		if is := authRequest(a).Validate(); len(is) > 0 {
			t.Errorf("%s: %v", a.Type, is)
		}
	}
}

func TestAuthIssues(t *testing.T) {
	cases := []struct {
		name  string
		auth  Auth
		sev   Severity
		code  string
		field string
	}{
		{"no type", Auth{}, Error, CodeRequired, "auth.type"},
		{"bad type", Auth{Type: "kerberos"}, Error, CodeInvalid, "auth.type"},
		{"basic user", Auth{Type: AuthBasic}, Warning, CodeRequired, "auth.basic.username"},
		{"bearer token", Auth{Type: AuthBearer}, Warning, CodeRequired, "auth.bearer.token"},
		{"bearer break", Auth{Type: AuthBearer, Bearer: BearerAuth{Token: "a\nb"}}, Error, CodeSingleLine, "auth.bearer.token"},
		{"bearer prefix break", Auth{Type: AuthBearer, Bearer: BearerAuth{Token: "a", Prefix: "a\n"}}, Error, CodeSingleLine, "auth.bearer.prefix"},
		{"apikey key", Auth{Type: AuthAPIKey}, Warning, CodeRequired, "auth.apikey.key"},
		{"apikey header", Auth{Type: AuthAPIKey, APIKey: APIKeyAuth{Key: "a b"}}, Error, CodeInvalid, "auth.apikey.key"},
		{"apikey in", Auth{Type: AuthAPIKey, APIKey: APIKeyAuth{Key: "a", In: InBody}}, Error, CodeInvalid, "auth.apikey.in"},
		{"apikey value", Auth{Type: AuthAPIKey, APIKey: APIKeyAuth{Key: "a", Value: "a\nb"}}, Error, CodeSingleLine, "auth.apikey.value"},
		{"digest user", Auth{Type: AuthDigest}, Warning, CodeRequired, "auth.digest.username"},
		{"digest alg", Auth{Type: AuthDigest, Digest: DigestAuth{Username: "u", Algorithm: "SHA-1"}}, Error, CodeInvalid, "auth.digest.algorithm"},
		{"digest qop", Auth{Type: AuthDigest, Digest: DigestAuth{Username: "u", QOP: "x"}}, Error, CodeInvalid, "auth.digest.qop"},
		{"oauth1 method", Auth{Type: AuthOAuth1, OAuth1: OAuth1Auth{ConsumerKey: "k"}}, Error, CodeRequired, "auth.oauth1.signatureMethod"},
		{"oauth1 key", Auth{Type: AuthOAuth1, OAuth1: OAuth1Auth{SignatureMethod: "PLAINTEXT"}}, Warning, CodeRequired, "auth.oauth1.consumerKey"},
		{"oauth1 rsa", Auth{Type: AuthOAuth1, OAuth1: OAuth1Auth{ConsumerKey: "k", SignatureMethod: "RSA-SHA1"}}, Warning, CodeRequired, "auth.oauth1.privateKey"},
		{"oauth1 in", Auth{Type: AuthOAuth1, OAuth1: OAuth1Auth{ConsumerKey: "k", SignatureMethod: "PLAINTEXT", In: "cookie"}}, Error, CodeInvalid, "auth.oauth1.in"},
		{"oauth2 grant", Auth{Type: AuthOAuth2}, Error, CodeRequired, "auth.oauth2.grant"},
		{"oauth2 auth url", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantImplicit, ClientID: "c"}}, Warning, CodeRequired, "auth.oauth2.authURL"},
		{"oauth2 token url", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, ClientID: "c"}}, Warning, CodeRequired, "auth.oauth2.tokenURL"},
		{"oauth2 username", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantPassword, TokenURL: "t", ClientID: "c"}}, Warning, CodeRequired, "auth.oauth2.username"},
		{"oauth2 client", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t"}}, Warning, CodeRequired, "auth.oauth2.clientID"},
		{"oauth2 pkce", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", PKCE: true}}, Error, CodeNotApplicable, "auth.oauth2.pkce"},
		{"oauth2 pkce method", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", PKCEMethod: "S512"}}, Error, CodeInvalid, "auth.oauth2.pkceMethod"},
		{"oauth2 client auth", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", ClientAuth: "jwt"}}, Error, CodeInvalid, "auth.oauth2.clientAuth"},
		{"oauth2 in", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", In: InBody}}, Error, CodeInvalid, "auth.oauth2.in"},
		{"oauth2 header", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", HeaderName: "a b"}}, Error, CodeInvalid, "auth.oauth2.headerName"},
		{"oauth2 params", Auth{Type: AuthOAuth2, OAuth2: OAuth2Auth{Grant: GrantClientCredentials, TokenURL: "t", ClientID: "c", TokenParams: []KV{{}}}}, Error, CodeRequired, "auth.oauth2.tokenParams[0].key"},
		{"awsv4", Auth{Type: AuthAWSV4}, Warning, CodeRequired, "auth.awsv4.region"},
		{"ntlm", Auth{Type: AuthNTLM}, Warning, CodeRequired, "auth.ntlm.username"},
		{"jwt alg", Auth{Type: AuthJWT}, Error, CodeRequired, "auth.jwt.algorithm"},
		{"jwt bad alg", Auth{Type: AuthJWT, JWT: JWTAuth{Algorithm: "none"}}, Error, CodeInvalid, "auth.jwt.algorithm"},
		{"jwt secret", Auth{Type: AuthJWT, JWT: JWTAuth{Algorithm: "HS512"}}, Warning, CodeRequired, "auth.jwt.secret"},
		{"jwt key", Auth{Type: AuthJWT, JWT: JWTAuth{Algorithm: "RS256"}}, Warning, CodeRequired, "auth.jwt.privateKey"},
		{"jwt in", Auth{Type: AuthJWT, JWT: JWTAuth{Algorithm: "HS256", Secret: "s", In: InBody}}, Error, CodeInvalid, "auth.jwt.in"},
	}
	for _, c := range cases {
		is := authRequest(c.auth).Validate()
		if !hasIssue(is, c.sev, c.code, c.field) {
			t.Errorf("%s: want %s %s at %s, got %v", c.name, c.sev, c.code, c.field, is)
		}
	}
}

func TestEnvironmentIssues(t *testing.T) {
	valid := func() *Environment {
		e := NewEnvironment("dev", "Development")
		e.Color = "green"
		e.Vars = []KV{{Key: "baseUrl", Value: "https://api.dev"}}
		e.Secrets = []Secret{{Name: "accessToken", Desc: "from the console"}}
		return e
	}
	if is := valid().Validate(); len(is) > 0 {
		t.Fatalf("valid environment: %v", is)
	}
	cases := []struct {
		name  string
		edit  func(*Environment)
		sev   Severity
		code  string
		field string
	}{
		{"no id", func(e *Environment) { e.ID = "" }, Error, CodeRequired, "id"},
		{"id slash", func(e *Environment) { e.ID = "a/b" }, Error, CodeInvalid, "id"},
		{"id hidden", func(e *Environment) { e.ID = ".dev" }, Error, CodeReserved, "id"},
		{"id underscore", func(e *Environment) { e.ID = "_dev" }, Error, CodeReserved, "id"},
		{"id device", func(e *Environment) { e.ID = "nul" }, Error, CodeReserved, "id"},
		{"id dot end", func(e *Environment) { e.ID = "dev." }, Error, CodeInvalid, "id"},
		{"name break", func(e *Environment) { e.Name = "a\nb" }, Error, CodeSingleLine, "name"},
		{"color break", func(e *Environment) { e.Color = "a\n" }, Error, CodeSingleLine, "color"},
		{"var name", func(e *Environment) { e.Vars[0].Key = "a b" }, Error, CodeInvalid, "vars[0].key"},
		{"secret name", func(e *Environment) { e.Secrets[0].Name = "$a" }, Error, CodeInvalid, "secrets[0].name"},
		{"secret twice", func(e *Environment) { e.Secrets = append(e.Secrets, e.Secrets[0]) }, Error, CodeDuplicate, "secrets[1].name"},
		{"secret and var", func(e *Environment) { e.Secrets[0].Name = "baseUrl" }, Warning, CodeDuplicate, "secrets[0].name"},
	}
	for _, c := range cases {
		e := valid()
		c.edit(e)
		if is := e.Validate(); !hasIssue(is, c.sev, c.code, c.field) {
			t.Errorf("%s: want %s %s at %s, got %v", c.name, c.sev, c.code, c.field, is)
		}
	}
}

func TestDisabledDuplicateVarsAreFine(t *testing.T) {
	e := NewEnvironment("dev", "")
	e.Vars = []KV{{Key: "a", Value: "1"}, {Key: "a", Value: "2", Disabled: true}}
	if is := e.Validate(); len(is) > 0 {
		t.Error(is)
	}
}

func TestCollectionIssues(t *testing.T) {
	if is := sampleCollection().Validate(); len(is) > 0 {
		t.Fatalf("valid collection: %v", is)
	}
	cases := []struct {
		name  string
		edit  func(*Collection)
		code  string
		field string
	}{
		{"id", func(c *Collection) { c.ID = "abc" }, CodeInvalid, "id"},
		{"name", func(c *Collection) { c.Name = "" }, CodeRequired, "name"},
		{"default env", func(c *Collection) { c.DefaultEnv = "prod" }, CodeInvalid, "defaultEnv"},
		{"nil env", func(c *Collection) { c.Envs = append(c.Envs, nil) }, CodeRequired, "envs[1]"},
		{"env twice", func(c *Collection) { c.Envs = append(c.Envs, NewEnvironment("DEV", "")) }, CodeDuplicate, "envs[1].id"},
		{"env invalid", func(c *Collection) { c.Envs[0].ID = "" }, CodeRequired, "envs[0].id"},
		{"nil root", func(c *Collection) { c.Root = nil }, CodeRequired, "root"},
		{"root path", func(c *Collection) { c.Root.Path = "x" }, CodeInvalid, "root.path"},
		{"root inherits", func(c *Collection) { c.Root.Config.Auth.Type = AuthInherit }, CodeInvalid, "root.config.auth.type"},
		{"root header", func(c *Collection) { c.Root.Config.Headers = []KV{{Key: "a b"}} }, CodeInvalid, "root.config.headers[0].key"},
		{"folder no auth", func(c *Collection) { c.Root.Children[0].Folder.Config.Auth.Type = "" }, CodeRequired, "root.children[0].config.auth.type"},
		{"folder settings", func(c *Collection) { c.Root.Children[0].Folder.Config.Settings.MaxRedirects = Some(-1) }, CodeInvalid, "root.children[0].config.settings"},
		{"folder vars", func(c *Collection) { c.Root.Children[0].Folder.Config.Vars = []KV{{Key: "-"}} }, CodeInvalid, "root.children[0].config.vars[0].key"},
		{"empty node", func(c *Collection) { c.Root.Children = append(c.Root.Children, Node{}) }, CodeInvalid, "root.children[2]"},
		{"both node", func(c *Collection) {
			c.Root.Children = append(c.Root.Children, Node{Folder: NewFolder("x"), Request: NewRequest(KindHTTP)})
		}, CodeInvalid, "root.children[2]"},
		{"folder outside", func(c *Collection) { c.Root.Children[0].Folder.Path = "a/users" }, CodePath, "root.children[0].path"},
		{"folder empty path", func(c *Collection) { c.Root.Children[0].Folder.Path = "" }, CodePath, "root.children[0].path"},
		{"folder envs", func(c *Collection) {
			c.Root.Children = append(c.Root.Children, Node{Folder: NewFolder("environments")})
		}, CodeReserved, "root.children[2].path"},
		{"folder underscore", func(c *Collection) {
			c.Root.Children = append(c.Root.Children, Node{Folder: NewFolder("_x")})
		}, CodeReserved, "root.children[2].path"},
		{"folder bad name", func(c *Collection) {
			c.Root.Children = append(c.Root.Children, Node{Folder: NewFolder("a:b")})
		}, CodeInvalid, "root.children[2].path"},
		{"request outside", func(c *Collection) { c.Root.Children[1].Request.ID = "users/health.postgo" }, CodePath, "root.children[1].id"},
		{"request no id", func(c *Collection) { c.Root.Children[1].Request.ID = "" }, CodeRequired, "root.children[1].id"},
		{"nested request", func(c *Collection) { c.Root.Children[0].Folder.Children[0].Request.URL = "" }, CodeRequired, "root.children[0].children[0].url"},
		{"case clash", func(c *Collection) {
			r := NewRequest(KindHTTP)
			r.ID, r.URL = "Health.postgo", "http://a"
			c.Root.Children = append(c.Root.Children, Node{Request: r})
		}, CodeDuplicate, "root.children[2]"},
	}
	for _, c := range cases {
		col := sampleCollection()
		c.edit(col)
		if is := col.Validate(); !hasIssue(is, Error, c.code, c.field) {
			t.Errorf("%s: want error %s at %s, got %v", c.name, c.code, c.field, is)
		}
	}
}

func TestIssues(t *testing.T) {
	var none Issues
	if none.HasErrors() || none.Err() != nil || none.Errors() != nil {
		t.Error("empty issues")
	}
	is := Issues{
		{Field: "a", Severity: Warning, Code: CodeRequired, Msg: "w"},
		{Field: "b", Severity: Error, Code: CodeInvalid, Msg: "e"},
	}
	if !is.HasErrors() || len(is.Errors()) != 1 {
		t.Error("HasErrors/Errors")
	}
	if err := is.Err(); err == nil || err.Error() != "invalid: error: b: e" {
		t.Errorf("Err = %v", err)
	}
	if is[0].String() != "warning: a: w" {
		t.Errorf("String = %q", is[0].String())
	}
}
