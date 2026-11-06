package vars

import (
	"bytes"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jinzhongjia/postgo/core/model"
)

func TestTokenize(t *testing.T) {
	type tk struct {
		kind TokenKind
		src  string
		name string
	}
	cases := []struct {
		in   string
		want []tk
	}{
		{"", nil},
		{"plain", []tk{{Text, "plain", ""}}},
		{"{{a}}", []tk{{Var, "{{a}}", "a"}}},
		{"x{{ base.url }}/y", []tk{{Text, "x", ""}, {Var, "{{ base.url }}", "base.url"}, {Text, "/y", ""}}},
		{"{{$uuid}}", []tk{{Dynamic, "{{$uuid}}", "$uuid"}}},
		{`a\{{b}}`, []tk{{Text, "a", ""}, {Escape, `\{{`, ""}, {Text, "b}}", ""}}},
		{"{{ 1 + 2 }}z", []tk{{Text, "{{ 1 + 2 }}z", ""}}},
		{"a{{b", []tk{{Text, "a", ""}, {Unclosed, "{{b", ""}}},
		{"{{a}}{{b}}", []tk{{Var, "{{a}}", "a"}, {Var, "{{b}}", "b"}}},
		{"{{$1}}", []tk{{Text, "{{$1}}", ""}}},
		{"{{}}", []tk{{Text, "{{}}", ""}}},
	}
	for _, c := range cases {
		var got []tk
		end := 0
		for _, tok := range Tokenize(c.in) {
			if tok.Start != end {
				t.Errorf("%q: gap before %v", c.in, tok)
			}
			end = tok.End
			got = append(got, tk{tok.Kind, c.in[tok.Start:tok.End], tok.Name})
		}
		if end != len(c.in) {
			t.Errorf("%q: tokens end at %d", c.in, end)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for k, s := range map[TokenKind]string{Text: "text", Var: "var", Dynamic: "dynamic", Escape: "escape", Unclosed: "unclosed"} {
		if k.String() != s {
			t.Errorf("%d.String() = %q", k, k.String())
		}
	}
}

func kv(k, v string) model.KV { return model.KV{Key: k, Value: v} }

func fixed() Options {
	return Options{
		Now:  func() time.Time { return time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC) },
		Rand: bytes.NewReader(make([]byte, 64)),
	}
}

func TestPrecedence(t *testing.T) {
	s := NewScope(
		Layer{Level: Data, Vars: []model.KV{kv("d", "data")}},
		Layer{Level: Global, Vars: []model.KV{kv("a", "global"), kv("g", "g")}},
		Layer{Level: Environment, Name: "dev", Vars: []model.KV{kv("a", "env"), kv("e", "e")}},
		Layer{Level: Collection, Vars: []model.KV{kv("a", "collection"), kv("c", "c")}},
		Layer{Level: Folder, Name: "users", Vars: []model.KV{kv("a", "outer")}},
		Layer{Level: Folder, Name: "users/admin", Vars: []model.KV{kv("a", "inner")}},
		Layer{Level: Request, Vars: []model.KV{kv("r", "req"), {Key: "a", Value: "disabled", Disabled: true}}},
	)
	r := NewResolver(s, fixed())
	got := r.Resolve("{{a}} {{g}} {{c}} {{e}} {{r}} {{d}}", Target{})
	if got.Text != "inner g c e req data" || len(got.Diags) != 0 {
		t.Errorf("Resolve = %q %v", got.Text, got.Diags)
	}
	v, ok := r.Lookup("a")
	if !ok || v.Text != "inner" || v.Source.Level != Folder || v.Source.Name != "users/admin" {
		t.Errorf("Lookup = %+v", v)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Error("Lookup found a missing variable")
	}
	s.Set(Local, "a", "local")
	if got := NewResolver(s, fixed()).Resolve("{{a}}", Target{}).Text; got != "local" {
		t.Errorf("after Set = %q", got)
	}
	if n := s.Names(); !reflect.DeepEqual(n, []string{"a", "c", "d", "e", "g", "r"}) {
		t.Errorf("Names = %v", n)
	}
}

func TestLaterDuplicateWins(t *testing.T) {
	s := NewScope(Layer{Level: Global, Vars: []model.KV{kv("a", "1"), kv("a", "2")}})
	if got := NewResolver(s, fixed()).Resolve("{{a}}", Target{}).Text; got != "2" {
		t.Errorf("got %q", got)
	}
}

func TestNewScopeCopies(t *testing.T) {
	vars := []model.KV{kv("a", "1")}
	s := NewScope(Layer{Level: Global, Vars: vars})
	vars[0].Value = "changed"
	if got := NewResolver(s, fixed()).Resolve("{{a}}", Target{}).Text; got != "1" {
		t.Errorf("scope follows the caller's slice: %q", got)
	}
}

func TestNested(t *testing.T) {
	s := NewScope(Layer{Level: Global, Vars: []model.KV{
		kv("host", "api.example.com"), kv("base", "https://{{host}}/v1"), kv("url", "{{base}}/users"),
	}})
	got := NewResolver(s, fixed()).Resolve("GET {{url}}", Target{})
	if got.Text != "GET https://api.example.com/v1/users" || len(got.Diags) != 0 {
		t.Fatalf("Resolve = %q %v", got.Text, got.Diags)
	}
	want := []Span{{4, 36, "url", false}, {4, 30, "base", false}, {12, 27, "host", false}}
	if !reflect.DeepEqual(got.Spans, want) {
		t.Errorf("Spans = %v, want %v", got.Spans, want)
	}
}

func hasDiag(r Result, code string, sev model.Severity) bool {
	for _, d := range r.Diags {
		if d.Code == code && d.Severity == sev {
			return true
		}
	}
	return false
}

func TestDiagnostics(t *testing.T) {
	s := NewScope(Layer{Level: Global, Vars: []model.KV{
		kv("self", "{{self}}"), kv("a", "{{b}}"), kv("b", "{{a}}"), kv("bad", "x{{nope}}"),
	}})
	r := NewResolver(s, fixed())

	got := r.Resolve("x {{missing}} y", Target{})
	if got.Text != "x {{missing}} y" || !hasDiag(got, CodeUndefined, model.Warning) || got.HasErrors() {
		t.Errorf("undefined: %q %v", got.Text, got.Diags)
	}
	if d := got.Diags[0]; d.Start != 2 || d.End != 13 {
		t.Errorf("undefined at %d..%d", d.Start, d.End)
	}
	if got := r.Resolve("{{self}}", Target{}); !hasDiag(got, CodeCycle, model.Error) || !got.HasErrors() {
		t.Errorf("self cycle: %v", got.Diags)
	}
	if got := r.Resolve("z{{a}}", Target{}); !hasDiag(got, CodeCycle, model.Error) || got.Diags[0].Start != 1 || got.Diags[0].End != 6 ||
		!strings.Contains(got.Diags[0].Msg, "a → b") {
		t.Errorf("cycle: %v", got.Diags)
	}
	if got := r.Resolve("{{bad}}", Target{}); !hasDiag(got, CodeUndefined, model.Warning) || got.Text != "x{{nope}}" || got.Diags[0].Start != 0 {
		t.Errorf("nested undefined: %q %v", got.Text, got.Diags)
	}
	if got := r.Resolve("a {{b", Target{}); !hasDiag(got, CodeUnclosed, model.Warning) || got.Text != "a {{b" {
		t.Errorf("unclosed: %v", got)
	}
	if got := r.Resolve("{{$nope}}", Target{}); !hasDiag(got, CodeUnknownDynamic, model.Warning) || got.Text != "{{$nope}}" {
		t.Errorf("unknown dynamic: %v", got)
	}
	if got := r.Resolve(`\{{a}}`, Target{}); got.Text != "{{a}}" || len(got.Diags) != 0 {
		t.Errorf("escape: %v", got)
	}
}

func TestDepth(t *testing.T) {
	var vars []model.KV
	for i := range 12 {
		vars = append(vars, kv("v"+string(rune('a'+i)), "{{v"+string(rune('a'+i+1))+"}}"))
	}
	vars = append(vars, kv("vm", "end"))
	s := NewScope(Layer{Level: Global, Vars: vars})
	if got := NewResolver(s, fixed()).Resolve("{{va}}", Target{}); !hasDiag(got, CodeTooDeep, model.Error) {
		t.Errorf("deep: %v", got.Diags)
	}
	o := fixed()
	o.MaxDepth = 20
	if got := NewResolver(s, o).Resolve("{{va}}", Target{}); got.Text != "end" || len(got.Diags) != 0 {
		t.Errorf("MaxDepth 20: %v", got)
	}
}

func secretScope() *Scope {
	return NewScope(
		Layer{Level: Global, Vars: []model.KV{kv("token", "plain-global"), kv("auth", "Bearer {{token}}")}},
		Layer{Level: Environment, Name: "dev", Vars: []model.KV{kv("token", "shadowed")}, Secrets: []string{"token"}},
	)
}

func TestSecrets(t *testing.T) {
	calls := 0
	o := fixed()
	o.Secrets = func(name string) (string, error) {
		calls++
		if name == "token" {
			return "s3cr{{et}}", nil
		}
		return "", errors.New("no")
	}
	r := NewResolver(secretScope(), o)
	got := r.Resolve("{{auth}}|{{token}}", Target{Host: "api.example.com", Field: "headers[0].value"})
	if got.Text != "Bearer s3cr{{et}}|s3cr{{et}}" || len(got.Diags) != 0 {
		t.Fatalf("Resolve = %q %v", got.Text, got.Diags)
	}
	if calls != 1 {
		t.Errorf("looked the secret up %d times", calls)
	}
	var secret []Span
	for _, sp := range got.Spans {
		if sp.Secret {
			secret = append(secret, sp)
		}
	}
	if len(secret) != 2 || got.Text[secret[0].Start:secret[0].End] != "s3cr{{et}}" || got.Text[secret[1].Start:secret[1].End] != "s3cr{{et}}" {
		t.Errorf("secret spans = %v", secret)
	}
	if v, _ := r.Lookup("token"); !v.Secret || v.Text != "" || v.Source.Level != Environment {
		t.Errorf("Lookup secret = %+v", v)
	}
}

func TestSecretPolicy(t *testing.T) {
	o := fixed()
	o.Secrets = func(string) (string, error) { return "s3cret", nil }
	o.Policy.AllowSecret = func(name string, tg Target) bool { return tg.Host == "api.example.com" }
	r := NewResolver(secretScope(), o)
	if got := r.Resolve("{{token}}", Target{Host: "api.example.com"}); got.Text != "s3cret" || got.HasErrors() {
		t.Errorf("trusted host: %v", got)
	}
	got := r.Resolve("x={{auth}}", Target{Host: "evil.example"})
	if got.Text != "x=Bearer " || !hasDiag(got, CodeSecretDenied, model.Error) || strings.Contains(got.Text, "s3cret") {
		t.Errorf("untrusted host: %v", got)
	}
	if got := r.Resolve("{{token}}", Target{Field: "url"}); !strings.Contains(got.Diags[0].Msg, "url") {
		t.Errorf("message names the field: %v", got.Diags)
	}
	if got := r.Resolve("{{token}}", Target{}); !strings.Contains(got.Diags[0].Msg, "this request") {
		t.Errorf("message: %v", got.Diags)
	}
}

func TestSecretUnavailable(t *testing.T) {
	r := NewResolver(secretScope(), fixed())
	if got := r.Resolve("{{token}}", Target{}); !hasDiag(got, CodeSecretUnavailable, model.Error) || got.Text != "" {
		t.Errorf("no store: %v", got)
	}
	o := fixed()
	o.Secrets = func(string) (string, error) { return "", errors.New("locked") }
	if got := NewResolver(secretScope(), o).Resolve("{{token}}", Target{}); !hasDiag(got, CodeSecretUnavailable, model.Error) ||
		!strings.Contains(got.Diags[0].Msg, "locked") {
		t.Errorf("store error: %v", got)
	}
}

func TestSetSecret(t *testing.T) {
	s := secretScope()
	s.Set(Environment, "token", "fresh")
	s.Set(Environment, "token", "fresher")
	s.Set(Environment, "plain", "p")
	ch := s.Changes()
	want := []Change{{Environment, "dev", "token", "fresher", true}, {Environment, "dev", "plain", "p", false}}
	if !reflect.DeepEqual(ch, want) {
		t.Errorf("Changes = %v", ch)
	}
	o := fixed()
	o.Policy.AllowSecret = func(string, Target) bool { return false }
	// A secret set by a script is still a secret: the policy still holds,
	// and no lookup is needed.
	got := NewResolver(s, o).Resolve("{{token}}", Target{})
	if !hasDiag(got, CodeSecretDenied, model.Error) {
		t.Errorf("set secret ignored the policy: %v", got)
	}
	got = NewResolver(s, fixed()).Resolve("{{token}}", Target{})
	if got.Text != "fresher" || len(got.Diags) != 0 || !got.Spans[0].Secret {
		t.Errorf("set secret: %v", got)
	}
	// Set on a level without a layer makes one.
	s.Set(Data, "row", "1")
	if NewResolver(s, fixed()).Resolve("{{row}}", Target{}).Text != "1" {
		t.Error("Set on a new level")
	}
}

func TestBuild(t *testing.T) {
	c := model.NewCollection("3f6c2a1e-8d4b-4c1f-9a77-1b2e5d0c9e10", "API")
	c.Root.Config.Vars = []model.KV{kv("a", "root"), kv("root", "r")}
	f := model.NewFolder("users")
	f.Config.Vars = []model.KV{kv("a", "folder")}
	req := model.NewRequest(model.KindHTTP)
	req.ID = "users/x.postgo"
	req.Vars = []model.KV{kv("b", "request")}
	env := model.NewEnvironment("dev", "Dev")
	env.Vars = []model.KV{kv("a", "env"), kv("e", "e")}
	env.Secrets = []model.Secret{{Name: "tok"}, {Name: "off", Disabled: true}}
	s := Build(BuildInput{
		Globals:    []model.KV{kv("g", "global")},
		Collection: c,
		Parents:    []*model.Folder{c.Root, f},
		Request:    req,
		Env:        env,
		Data:       map[string]string{"z": "2", "b": "data"},
	})
	o := fixed()
	o.Secrets = func(string) (string, error) { return "T", nil }
	r := NewResolver(s, o)
	got := r.Resolve("{{a}} {{b}} {{g}} {{root}} {{e}} {{z}} {{tok}} {{off}}", Target{})
	if got.Text != "folder data global r e 2 T {{off}}" {
		t.Errorf("Build = %q %v", got.Text, got.Diags)
	}
	if v, _ := r.Lookup("e"); v.Source.Name != "dev" {
		t.Errorf("env layer name = %q", v.Source.Name)
	}
	if v, _ := r.Lookup("root"); v.Source.Level != Collection || v.Source.Name != c.ID {
		t.Errorf("collection layer = %+v", v.Source)
	}
	// Everything is optional.
	if Build(BuildInput{}).Names() != nil {
		t.Error("empty Build has names")
	}
}

func TestLevelString(t *testing.T) {
	if Global.String() != "global" || Local.String() != "local" || Folder.String() != "folder" {
		t.Error("Level.String")
	}
}

func TestDynamicsConsistentPerResolver(t *testing.T) {
	r := NewResolver(NewScope(), Options{})
	got := r.Resolve("{{$uuid}} {{$uuid}}", Target{})
	parts := strings.Split(got.Text, " ")
	if parts[0] != parts[1] {
		t.Errorf("$uuid differs within a resolver: %q", got.Text)
	}
	other := NewResolver(NewScope(), Options{}).Resolve("{{$uuid}}", Target{}).Text
	if other == parts[0] {
		t.Error("$uuid repeats across resolvers")
	}
	if got.Spans[0].Var != "$uuid" {
		t.Errorf("dynamic span = %v", got.Spans[0])
	}
}

func TestDynamicsDeterministicWithSeed(t *testing.T) {
	a := NewResolver(NewScope(), fixed()).Resolve("{{$randomInt}} {{$randomEmail}}", Target{}).Text
	b := NewResolver(NewScope(), fixed()).Resolve("{{$randomInt}} {{$randomEmail}}", Target{}).Text
	if a != b {
		t.Errorf("same seed, %q and %q", a, b)
	}
}

func TestEveryDynamic(t *testing.T) {
	formats := map[string]*regexp.Regexp{
		"$uuid":             regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`),
		"$timestamp":        regexp.MustCompile(`^1793952000$`),
		"$timestampMs":      regexp.MustCompile(`^1793952000000$`),
		"$isoTimestamp":     regexp.MustCompile(`^2026-11-06T08:00:00\.000Z$`),
		"$randomInt":        regexp.MustCompile(`^\d{1,4}$`),
		"$randomEmail":      regexp.MustCompile(`^[a-z.-]+@example\.(com|net|org)$`),
		"$randomIP":         regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`),
		"$randomHexColor":   regexp.MustCompile(`^#[0-9a-f]{6}$`),
		"$randomBoolean":    regexp.MustCompile(`^(true|false)$`),
		"$randomDateFuture": regexp.MustCompile(`^202[67]-`),
		"$randomDatePast":   regexp.MustCompile(`^202[56]-`),
	}
	infos := Dynamics()
	if len(infos) < 40 {
		t.Errorf("only %d dynamic variables", len(infos))
	}
	for i, d := range infos {
		if i > 0 && infos[i-1].Name >= d.Name {
			t.Errorf("Dynamics not sorted at %s", d.Name)
		}
		if d.Desc == "" || d.Example == "" || !dynamicName.MatchString(d.Name) {
			t.Errorf("%s: incomplete info %+v", d.Name, d)
		}
		for seed := byte(0); seed < 5; seed++ {
			o := fixed()
			o.Rand = bytes.NewReader(bytes.Repeat([]byte{seed}, 32))
			got := NewResolver(NewScope(), o).Resolve("{{"+d.Name+"}}", Target{})
			if got.Text == "" || len(got.Diags) != 0 || strings.Contains(got.Text, "{{") {
				t.Errorf("%s = %q %v", d.Name, got.Text, got.Diags)
			}
			if re := formats[d.Name]; re != nil && !re.MatchString(got.Text) {
				t.Errorf("%s = %q, want %s", d.Name, got.Text, re)
			}
		}
	}
}
