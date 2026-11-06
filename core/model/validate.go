package model

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Severity tells whether an issue keeps a value from being saved.
type Severity int

const (
	// Warning is a value that can be saved but is likely unfinished or
	// not portable, such as an OAuth 2.0 auth without a token URL.
	Warning Severity = iota
	// Error is a value the file format cannot hold, or that no request
	// could be built from.
	Error
)

func (s Severity) String() string {
	if s == Error {
		return "error"
	}
	return "warning"
}

// Issue is one problem Validate found.
type Issue struct {
	// Field is the path of the field, as "headers[2].key".
	Field    string
	Severity Severity
	// Code identifies the rule, for the UI to translate: one of the Code
	// constants.
	Code string
	Msg  string
}

func (i Issue) String() string { return fmt.Sprintf("%s: %s: %s", i.Severity, i.Field, i.Msg) }

// Issue codes.
const (
	CodeRequired      = "required"       // a field that must be set is empty
	CodeSingleLine    = "single-line"    // a field that must fit on one line has a line break
	CodeUntrimmed     = "untrimmed"      // a field has spaces at its start or end
	CodeInvalid       = "invalid"        // a value is not one the field allows
	CodeNotApplicable = "not-applicable" // a field is set where the request's kind does not use it
	CodeDuplicate     = "duplicate"      // a name is used twice where names must differ
	CodeReserved      = "reserved"       // a name the format keeps for itself
	CodePath          = "path"           // a path is not relative to the collection's root
)

// Issues are the issues of a value.
type Issues []Issue

// HasErrors reports whether any issue is an Error.
func (is Issues) HasErrors() bool {
	return slices.ContainsFunc(is, func(i Issue) bool { return i.Severity == Error })
}

// Errors returns the issues that are errors.
func (is Issues) Errors() Issues {
	var out Issues
	for _, i := range is {
		if i.Severity == Error {
			out = append(out, i)
		}
	}
	return out
}

// Err returns an error listing the errors, or nil when there are none.
func (is Issues) Err() error {
	errs := is.Errors()
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.String()
	}
	return fmt.Errorf("invalid: %s", strings.Join(msgs, "; "))
}

type validator struct {
	issues Issues
}

func (v *validator) add(sev Severity, field, code, format string, args ...any) {
	v.issues = append(v.issues, Issue{Field: field, Severity: sev, Code: code, Msg: fmt.Sprintf(format, args...)})
}

func join(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}

func at(prefix string, i int) string { return fmt.Sprintf("%s[%d]", prefix, i) }

func hasBreak(s string) bool { return strings.ContainsAny(s, "\r\n") }

// line checks a field that must fit on one line, without spaces around.
func (v *validator) line(field, s string, required bool) {
	switch {
	case s == "":
		if required {
			v.add(Error, field, CodeRequired, "must not be empty")
		}
	case hasBreak(s):
		v.add(Error, field, CodeSingleLine, "must not contain line breaks")
	case strings.TrimSpace(s) != s:
		v.add(Error, field, CodeUntrimmed, "must not start or end with spaces")
	}
}

func oneOf[T comparable](v *validator, field string, val T, allowed []T, allowEmpty bool) {
	var zero T
	if val == zero {
		if !allowEmpty {
			v.add(Error, field, CodeRequired, "must be set")
		}
		return
	}
	if !slices.Contains(allowed, val) {
		v.add(Error, field, CodeInvalid, "%v is not one of %v", val, allowed)
	}
}

// isTChar reports whether c may be in an HTTP token (RFC 9110 5.6.2).
func isTChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTChar(s[i]) {
			return false
		}
	}
	return true
}

// key checks the key of a "key = value" row.
func (v *validator) key(field, k string) {
	v.line(field, k, true)
	if strings.Contains(k, " = ") || strings.HasSuffix(k, " =") {
		v.add(Error, field, CodeInvalid, `must not contain " = " or end with " ="`)
	}
	if strings.Contains(k, "{{") {
		v.add(Error, field, CodeInvalid, "keys cannot hold variables")
	}
}

func (v *validator) kvs(prefix string, kvs []KV) {
	for i, kv := range kvs {
		v.key(join(at(prefix, i), "key"), kv.Key)
	}
}

func (v *validator) headers(prefix string, kvs []KV) {
	for i, kv := range kvs {
		f := at(prefix, i)
		if !isToken(kv.Key) {
			v.add(Error, join(f, "key"), CodeInvalid, "%q is not a valid header name", kv.Key)
		}
		if hasBreak(kv.Value) {
			v.add(Error, join(f, "value"), CodeSingleLine, "header values must not contain line breaks")
		} else if strings.TrimSpace(kv.Value) != kv.Value {
			v.add(Error, join(f, "value"), CodeUntrimmed, "header values must not start or end with spaces")
		}
	}
}

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// vars checks variables: their names must be names a template can refer
// to; a name used twice is a warning, the later one winning.
func (v *validator) vars(prefix string, kvs []KV) {
	seen := map[string]bool{}
	for i, kv := range kvs {
		f := join(at(prefix, i), "key")
		if !varName.MatchString(kv.Key) {
			v.add(Error, f, CodeInvalid, "%q is not a variable name (letters, digits, _ . -, not starting with a digit)", kv.Key)
			continue
		}
		if seen[kv.Key] && !kv.Disabled {
			v.add(Warning, f, CodeDuplicate, "%q is set twice; the later one wins", kv.Key)
		}
		if !kv.Disabled {
			seen[kv.Key] = true
		}
	}
}

// names checks the names of sections such as messages and examples: one
// line each, different from one another.
func (v *validator) names(prefix string, names []string) {
	seen := map[string]bool{}
	for i, n := range names {
		f := join(at(prefix, i), "name")
		v.line(f, n, true)
		if seen[n] {
			v.add(Error, f, CodeDuplicate, "%q is used twice", n)
		}
		seen[n] = true
	}
}

var drive = regexp.MustCompile(`^[A-Za-z]:`)

// relPath checks a path relative to the collection's root. Absolute paths
// are allowed but warned about, as other machines do not have them.
func (v *validator) relPath(field, p string, required bool) {
	switch {
	case p == "":
		if required {
			v.add(Error, field, CodeRequired, "must not be empty")
		}
		return
	case hasBreak(p):
		v.add(Error, field, CodeSingleLine, "must not contain line breaks")
		return
	case strings.HasPrefix(p, "/") || drive.MatchString(p):
		v.add(Warning, field, CodePath, "absolute paths do not work on other machines")
		return
	case strings.Contains(p, `\`):
		v.add(Error, field, CodePath, `use / to separate the path's elements, not \`)
		return
	}
	if c := path.Clean(p); c != p {
		v.add(Error, field, CodePath, "write the path as %q", c)
	} else if p == ".." || strings.HasPrefix(p, "../") {
		v.add(Warning, field, CodePath, "the path is outside the collection")
	}
}

// fileName checks a name that becomes a file or directory name on every
// system.
func (v *validator) fileName(field, name string) {
	if name == "" {
		v.add(Error, field, CodeRequired, "must not be empty")
		return
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\:*?\"<>|\x00\r\n\t") || strings.TrimSpace(name) != name ||
		strings.HasSuffix(name, ".") {
		v.add(Error, field, CodeInvalid, "%q cannot be a file name on every system", name)
		return
	}
	stem := strings.ToUpper(name)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		v.add(Error, field, CodeReserved, "%q is a device name on Windows", name)
	}
}

// Validate reports the request's issues.
func (r *Request) Validate() Issues {
	var v validator
	r.validate(&v, "")
	return v.issues
}

// Reserved methods: a request line starting with them is not HTTP.
var reservedMethods = []string{"GRAPHQL", "WS", "SSE", "GRPC"}

func (r *Request) validate(v *validator, p string) {
	if r.ID != "" { // unsaved requests have none; Collection.Validate requires it
		v.requestID(join(p, "id"), r.ID)
	}
	v.line(join(p, "name"), r.Name, false)
	seenTag := map[string]bool{}
	for i, t := range r.Tags {
		f := at(join(p, "tags"), i)
		v.line(f, t, true)
		if strings.Contains(t, ",") {
			v.add(Error, f, CodeInvalid, "tags must not contain commas")
		}
		if seenTag[t] {
			v.add(Error, f, CodeDuplicate, "%q is used twice", t)
		}
		seenTag[t] = true
	}

	oneOf(v, join(p, "kind"), r.Kind, Kinds, false)
	http := r.Kind == KindHTTP
	gql := r.Kind == KindGraphQL
	grpc := r.Kind == KindGRPC
	switch {
	case http:
		if !isToken(r.Method) {
			v.add(Error, join(p, "method"), CodeInvalid, "%q is not an HTTP method", r.Method)
		} else if slices.Contains(reservedMethods, strings.ToUpper(r.Method)) {
			v.add(Error, join(p, "method"), CodeReserved, "%q is the name of another kind of request", r.Method)
		}
	case gql:
		if r.Method != "POST" && r.Method != "GET" {
			v.add(Error, join(p, "method"), CodeInvalid, "GraphQL requests use POST or GET")
		}
	default:
		if r.Method != "" {
			v.add(Error, join(p, "method"), CodeNotApplicable, "only HTTP and GraphQL requests have a method")
		}
	}
	v.url(join(p, "url"), r)

	notFor := func(field string, set bool, allowed bool) {
		if set && !allowed {
			v.add(Error, join(p, field), CodeNotApplicable, "%s requests do not use %s", r.Kind, field)
		}
	}
	notFor("query", len(r.Query) > 0, !grpc)
	notFor("path", len(r.Path) > 0, http)
	notFor("headers", len(r.Headers) > 0, !grpc)
	notFor("messages", len(r.Messages) > 0, r.Kind == KindWS || grpc)
	notFor("metadata", len(r.Metadata) > 0, grpc)
	notFor("proto", r.Proto != Proto{}, grpc)
	notFor("examples", len(r.Examples) > 0, http)

	v.kvs(join(p, "query"), r.Query)
	v.kvs(join(p, "path"), r.Path)
	for i, kv := range r.Path {
		if kv.Key != "" && !hasPathParam(r.URL, kv.Key) {
			v.add(Warning, join(at(join(p, "path"), i), "key"), CodeInvalid, "the URL has no :%s", kv.Key)
		}
	}
	v.headers(join(p, "headers"), r.Headers)
	v.body(join(p, "body"), r)
	v.auth(join(p, "auth"), r.Auth, true)
	v.vars(join(p, "vars"), r.Vars)
	v.asserts(join(p, "asserts"), r.Asserts)
	v.settings(join(p, "settings"), r.Settings)

	names := make([]string, len(r.Messages))
	for i, m := range r.Messages {
		names[i] = m.Name
	}
	v.names(join(p, "messages"), names)
	v.kvs(join(p, "metadata"), r.Metadata)
	if grpc {
		oneOf(v, join(p, "proto.source"), r.Proto.Source, []ProtoSource{ProtoReflection, ProtoFile}, false)
		if r.Proto.Source == ProtoFile {
			v.relPath(join(p, "proto.file"), r.Proto.File, true)
		} else if r.Proto.File != "" {
			v.add(Error, join(p, "proto.file"), CodeNotApplicable, "a file is only used when the source is file")
		}
	}

	names = make([]string, len(r.Examples))
	for i, e := range r.Examples {
		names[i] = e.Name
		if e.Status < 100 || e.Status > 599 {
			v.add(Error, join(at(join(p, "examples"), i), "status"), CodeInvalid, "%d is not an HTTP status", e.Status)
		}
	}
	v.names(join(p, "examples"), names)
}

func (v *validator) requestID(field, id string) {
	v.relPath(field, id, true)
	if id == "" || strings.HasPrefix(id, "/") || drive.MatchString(id) || strings.HasPrefix(id, "..") {
		if id != "" {
			v.add(Error, field, CodePath, "request IDs are paths inside the collection")
		}
		return
	}
	if !strings.HasSuffix(id, ".postgo") {
		v.add(Error, field, CodeInvalid, "request files end in .postgo")
	}
	elems := strings.Split(id, "/")
	for _, e := range elems {
		v.fileName(field, e)
	}
	if base := elems[len(elems)-1]; strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".") {
		v.add(Error, field, CodeReserved, "file names starting with _ or . are not requests")
	}
	if elems[0] == "environments" || elems[0] == ".postgo.local" {
		v.add(Error, field, CodeReserved, "%s/ is not for requests", elems[0])
	}
}

// hasPathParam reports whether url has a segment ":name".
func hasPathParam(url, name string) bool {
	for _, seg := range strings.Split(url, "/") {
		if seg == ":"+name {
			return true
		}
	}
	return false
}

var (
	grpcMethod = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*/[A-Za-z_][A-Za-z0-9_]*$`)
	scheme     = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://`)
)

// outsideTemplates returns s with the {{…}} references taken out.
func outsideTemplates(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(s[i:], "}}")
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+j+2:]
	}
}

func (v *validator) url(field string, r *Request) {
	u := r.URL
	v.line(field, u, true)
	if u == "" || hasBreak(u) {
		return
	}
	bare := outsideTemplates(u)
	if strings.ContainsAny(bare, " \t") {
		v.add(Error, field, CodeInvalid, "URLs must not contain spaces outside {{variables}}")
	}
	if r.Kind != KindGRPC && strings.Contains(bare, "?") {
		v.add(Error, field, CodeInvalid, "put the query in the request's query parameters, not in the URL")
	}
	if r.Kind == KindGRPC {
		i := strings.Index(u, "/")
		if i <= 0 || !grpcMethod.MatchString(u[i+1:]) {
			v.add(Error, field, CodeInvalid, "gRPC URLs are host/package.Service/Method")
		}
		return
	}
	m := scheme.FindStringSubmatch(u)
	if m == nil {
		return // a template, as {{baseUrl}}/users, or a host the engine completes
	}
	want := []string{"http", "https"}
	if r.Kind == KindWS {
		want = []string{"ws", "wss"}
	}
	if !slices.Contains(want, strings.ToLower(m[1])) {
		v.add(Warning, field, CodeInvalid, "%s requests use %v URLs", r.Kind, want)
	}
}

func (v *validator) body(p string, r *Request) {
	b := r.Body
	oneOf(v, join(p, "type"), b.Type, BodyTypes, false)
	switch r.Kind {
	case KindHTTP:
	case KindGraphQL:
		if b.Type != BodyGraphQL {
			v.add(Error, join(p, "type"), CodeInvalid, "GraphQL requests have a GraphQL body")
		}
	default:
		if b.Type != BodyNone {
			v.add(Error, join(p, "type"), CodeNotApplicable, "%s requests have no body", r.Kind)
		}
	}
	switch b.Type {
	case BodyForm:
		v.kvs(join(p, "form"), b.Form)
	case BodyMultipart:
		for i, part := range b.Parts {
			f := at(join(p, "parts"), i)
			v.key(join(f, "key"), part.Key)
			if part.File {
				v.relPath(join(f, "value"), part.Value, true)
			}
		}
	case BodyFile:
		v.relPath(join(p, "file"), b.File, true)
	}
}

func (v *validator) asserts(p string, as []Assert) {
	for i, a := range as {
		v.line(join(at(p, i), "expr"), a.Expr, true)
	}
}

func (v *validator) settings(p string, s Settings) {
	if s.Timeout.Set && s.Timeout.Value < 0 {
		v.add(Error, join(p, "timeout"), CodeInvalid, "must not be negative")
	}
	if s.MaxRedirects.Set && s.MaxRedirects.Value < 0 {
		v.add(Error, join(p, "maxRedirects"), CodeInvalid, "must not be negative")
	}
	if s.HTTPVersion.Set {
		oneOf(v, join(p, "httpVersion"), s.HTTPVersion.Value, HTTPVersions, false)
	}
}

// auth checks the fields of the auth's type. Fields an auth cannot work
// without are warnings, so that an unfinished auth can be saved.
func (v *validator) auth(p string, a Auth, canInherit bool) {
	oneOf(v, join(p, "type"), a.Type, AuthTypes, false)
	if a.Type == AuthInherit && !canInherit {
		v.add(Error, join(p, "type"), CodeInvalid, "the collection has nothing to inherit from")
	}
	need := func(field, val string) {
		if val == "" {
			v.add(Warning, join(p, field), CodeRequired, "the auth needs it")
		}
	}
	single := func(field, val string) {
		if hasBreak(val) {
			v.add(Error, join(p, field), CodeSingleLine, "must not contain line breaks")
		}
	}
	placement := func(field string, val Placement, allowed ...Placement) {
		oneOf(v, join(p, field), val, allowed, true)
	}
	switch a.Type {
	case AuthBasic:
		need("basic.username", a.Basic.Username)
	case AuthBearer:
		need("bearer.token", a.Bearer.Token)
		single("bearer.token", a.Bearer.Token)
		single("bearer.prefix", a.Bearer.Prefix)
	case AuthAPIKey:
		k := a.APIKey
		placement("apikey.in", k.In, InHeader, InQuery)
		if k.Key == "" {
			need("apikey.key", "")
		} else if k.In != InQuery && !isToken(k.Key) {
			v.add(Error, join(p, "apikey.key"), CodeInvalid, "%q is not a valid header name", k.Key)
		}
		single("apikey.value", k.Value)
	case AuthDigest:
		d := a.Digest
		need("digest.username", d.Username)
		oneOf(v, join(p, "digest.algorithm"), d.Algorithm, DigestAlgorithms, true)
		oneOf(v, join(p, "digest.qop"), d.QOP, []string{"auth", "auth-int"}, true)
	case AuthOAuth1:
		o := a.OAuth1
		need("oauth1.consumerKey", o.ConsumerKey)
		oneOf(v, join(p, "oauth1.signatureMethod"), o.SignatureMethod, OAuth1SignatureMethods, false)
		if strings.HasPrefix(o.SignatureMethod, "RSA-") {
			need("oauth1.privateKey", o.PrivateKey)
		}
		placement("oauth1.in", o.In, InHeader, InQuery, InBody)
	case AuthOAuth2:
		o := a.OAuth2
		oneOf(v, join(p, "oauth2.grant"), o.Grant, OAuth2Grants, false)
		if o.Grant == GrantAuthorizationCode || o.Grant == GrantImplicit {
			need("oauth2.authURL", o.AuthURL)
		}
		if o.Grant != GrantImplicit {
			need("oauth2.tokenURL", o.TokenURL)
		}
		if o.Grant == GrantPassword {
			need("oauth2.username", o.Username)
		}
		need("oauth2.clientID", o.ClientID)
		if o.PKCE && o.Grant != GrantAuthorizationCode {
			v.add(Error, join(p, "oauth2.pkce"), CodeNotApplicable, "PKCE is part of the authorization code grant")
		}
		oneOf(v, join(p, "oauth2.pkceMethod"), o.PKCEMethod, []string{"S256", "plain"}, true)
		oneOf(v, join(p, "oauth2.clientAuth"), o.ClientAuth, []string{"header", "body"}, true)
		placement("oauth2.in", o.In, InHeader, InQuery)
		if o.HeaderName != "" && !isToken(o.HeaderName) {
			v.add(Error, join(p, "oauth2.headerName"), CodeInvalid, "%q is not a valid header name", o.HeaderName)
		}
		v.kvs(join(p, "oauth2.authParams"), o.AuthParams)
		v.kvs(join(p, "oauth2.tokenParams"), o.TokenParams)
	case AuthAWSV4:
		need("awsv4.accessKey", a.AWSV4.AccessKey)
		need("awsv4.secretKey", a.AWSV4.SecretKey)
		need("awsv4.region", a.AWSV4.Region)
		need("awsv4.service", a.AWSV4.Service)
	case AuthNTLM:
		need("ntlm.username", a.NTLM.Username)
	case AuthJWT:
		j := a.JWT
		oneOf(v, join(p, "jwt.algorithm"), j.Algorithm, JWTAlgorithms, false)
		if strings.HasPrefix(j.Algorithm, "HS") {
			need("jwt.secret", j.Secret)
		} else if j.Algorithm != "" {
			need("jwt.privateKey", j.PrivateKey)
		}
		placement("jwt.in", j.In, InHeader, InQuery)
	}
}

func (v *validator) config(p string, c Config, root bool) {
	v.headers(join(p, "headers"), c.Headers)
	v.auth(join(p, "auth"), c.Auth, !root)
	v.vars(join(p, "vars"), c.Vars)
	v.settings(join(p, "settings"), c.Settings)
}

// Validate reports the environment's issues.
func (e *Environment) Validate() Issues {
	var v validator
	e.validate(&v, "")
	return v.issues
}

func (e *Environment) validate(v *validator, p string) {
	v.fileName(join(p, "id"), e.ID)
	if strings.HasPrefix(e.ID, ".") || strings.HasPrefix(e.ID, "_") {
		v.add(Error, join(p, "id"), CodeReserved, "environment IDs must not start with . or _")
	}
	v.line(join(p, "name"), e.Name, false)
	v.line(join(p, "color"), e.Color, false)
	v.vars(join(p, "vars"), e.Vars)
	vars := map[string]bool{}
	for _, kv := range e.Vars {
		vars[kv.Key] = true
	}
	seen := map[string]bool{}
	for i, s := range e.Secrets {
		f := join(at(join(p, "secrets"), i), "name")
		if !varName.MatchString(s.Name) {
			v.add(Error, f, CodeInvalid, "%q is not a variable name", s.Name)
			continue
		}
		if seen[s.Name] {
			v.add(Error, f, CodeDuplicate, "%q is used twice", s.Name)
		}
		if vars[s.Name] {
			v.add(Warning, f, CodeDuplicate, "%q is also a plain variable; the secret wins", s.Name)
		}
		seen[s.Name] = true
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Validate reports the issues of the collection, its folders, requests
// and environments, including the structure: folder paths and request IDs
// that match where they are, names that differ on file systems that
// ignore case.
func (c *Collection) Validate() Issues {
	var v validator
	if !uuidRE.MatchString(c.ID) {
		v.add(Error, "id", CodeInvalid, "%q is not a UUID", c.ID)
	}
	v.line("name", c.Name, true)
	if c.DefaultEnv != "" && c.Env(c.DefaultEnv) == nil {
		v.add(Error, "defaultEnv", CodeInvalid, "there is no environment %q", c.DefaultEnv)
	}
	envs := map[string]bool{}
	for i, e := range c.Envs {
		p := at("envs", i)
		if e == nil {
			v.add(Error, p, CodeRequired, "must not be nil")
			continue
		}
		e.validate(&v, p)
		if k := strings.ToLower(e.ID); envs[k] {
			v.add(Error, join(p, "id"), CodeDuplicate, "%q is used twice (ignoring case)", e.ID)
		} else {
			envs[k] = true
		}
	}
	if c.Root == nil {
		v.add(Error, "root", CodeRequired, "must not be nil")
		return v.issues
	}
	if c.Root.Path != "" {
		v.add(Error, "root.path", CodeInvalid, "the root's path is empty")
	}
	v.folder("root", c.Root, true)
	return v.issues
}

func (v *validator) folder(p string, f *Folder, root bool) {
	v.config(join(p, "config"), f.Config, root)
	seen := map[string]bool{}
	for i, n := range f.Children {
		cp := at(join(p, "children"), i)
		var name, want string
		switch {
		case (n.Folder == nil) == (n.Request == nil):
			v.add(Error, cp, CodeInvalid, "a node is a folder or a request")
			continue
		case n.Folder != nil:
			name = path.Base(n.Folder.Path)
			want = path.Join(f.Path, name)
			if n.Folder.Path != want || n.Folder.Path == "" {
				v.add(Error, join(cp, "path"), CodePath, "%q is not inside %q", n.Folder.Path, f.Path)
			}
			v.fileName(join(cp, "path"), name)
			if root && (name == "environments" || name == ".postgo.local") {
				v.add(Error, join(cp, "path"), CodeReserved, "%s/ is not for requests", name)
			} else if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
				v.add(Error, join(cp, "path"), CodeReserved, "folder names must not start with _ or .")
			}
			v.folder(cp, n.Folder, false)
		default:
			if n.Request.ID == "" {
				v.add(Error, join(cp, "id"), CodeRequired, "requests in a collection have an ID")
				n.Request.validate(v, cp)
				continue
			}
			name = path.Base(n.Request.ID)
			if path.Join(f.Path, name) != n.Request.ID {
				v.add(Error, join(cp, "id"), CodePath, "%q is not inside %q", n.Request.ID, f.Path)
			}
			n.Request.validate(v, cp)
		}
		if k := strings.ToLower(name); seen[k] {
			v.add(Error, cp, CodeDuplicate, "%q is used twice in the folder (ignoring case)", name)
		} else {
			seen[k] = true
		}
	}
}
