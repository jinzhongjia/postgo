package vars

import (
	"crypto/rand"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"strings"
	"time"

	"github.com/jinzhongjia/postgo/core/model"
)

// SecretLookup returns the value of a secret variable by name.
type SecretLookup func(name string) (string, error)

// Target is where a resolved template goes.
type Target struct {
	// Host is the host the request goes to, known once its URL is
	// resolved; "" for the URL itself.
	Host string
	// Field is the request's field, as "url", "headers[0].value", "body".
	Field string
}

// Policy decides which secrets may be put in which targets.
type Policy struct {
	// AllowSecret reports whether the secret may go in the target. Nil
	// allows every secret everywhere, as for requests the user sends.
	AllowSecret func(name string, t Target) bool
}

// Options configure a Resolver.
type Options struct {
	Secrets  SecretLookup
	Policy   Policy
	Now      func() time.Time // time.Now when nil
	Rand     io.Reader        // crypto/rand when nil; seeds the random values
	MaxDepth int              // how deep values may refer to variables; 10 when 0
}

// Diagnostic codes.
const (
	CodeUndefined         = "undefined"          // no level defines the variable
	CodeUnknownDynamic    = "unknown-dynamic"    // no dynamic variable has the name
	CodeUnclosed          = "unclosed"           // {{ without }}
	CodeCycle             = "cycle"              // a value refers back to itself
	CodeTooDeep           = "too-deep"           // values refer to values deeper than MaxDepth
	CodeSecretDenied      = "secret-denied"      // the policy refused the secret for the target
	CodeSecretUnavailable = "secret-unavailable" // the secret could not be looked up
)

// Diag is a problem with a reference: bytes Start to End of the template.
// A problem inside the value of a variable is reported at the reference
// to that variable.
type Diag struct {
	Start, End int
	Severity   model.Severity
	Code       string
	Msg        string
}

// Span is a part of a result that a reference was resolved to: bytes
// Start to End of Result.Text. References inside the values of variables
// have spans of their own, inside that of the variable.
type Span struct {
	Start, End int
	Var        string // the variable, or the dynamic variable with its $
	Secret     bool   // the bytes are a secret's value
}

// Result is a resolved template.
type Result struct {
	Text  string
	Spans []Span
	Diags []Diag
}

// HasErrors reports whether a diagnostic is an error: then the result
// must not be sent.
func (r Result) HasErrors() bool {
	for _, d := range r.Diags {
		if d.Severity == model.Error {
			return true
		}
	}
	return false
}

// Value is what a variable is, for showing it.
type Value struct {
	// Text is the value as written, before resolving the references it
	// holds; "" for a secret.
	Text   string
	Source Layer // its Level and Name
	Secret bool
}

// Resolver resolves templates for one send of a request: dynamic
// variables keep their value across the request's templates, so the
// {{$uuid}} of the URL is the one of the body.
type Resolver struct {
	scope   *Scope
	opts    Options
	rng     *mrand.Rand
	dynamic map[string]string
	secrets map[string]string
}

// NewResolver returns a resolver of the scope.
func NewResolver(s *Scope, o Options) *Resolver {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 10
	}
	var seed [32]byte
	io.ReadFull(o.Rand, seed[:])
	return &Resolver{
		scope:   s,
		opts:    o,
		rng:     mrand.New(mrand.NewChaCha8(seed)),
		dynamic: map[string]string{},
		secrets: map[string]string{},
	}
}

// Lookup returns what a variable is, without resolving it.
func (r *Resolver) Lookup(name string) (Value, bool) {
	e, ok := r.scope.find(name)
	if !ok {
		return Value{}, false
	}
	v := Value{Source: Layer{Level: e.layer.Level, Name: e.layer.Name}, Secret: e.secret}
	if !e.secret {
		v.Text = e.value
	}
	return v, true
}

// Resolve resolves a template going to the target.
func (r *Resolver) Resolve(tmpl string, t Target) Result {
	var res Result
	var b strings.Builder
	r.expand(&b, &res, tmpl, t, nil, -1, -1)
	res.Text = b.String()
	return res
}

// expand writes the resolution of tmpl. stack holds the variables being
// expanded, outermost first; diagnostics go at bytes at..end of the
// top-level template when nested (at >= 0), else at the token.
func (r *Resolver) expand(b *strings.Builder, res *Result, tmpl string, t Target, stack []string, at, end int) {
	diag := func(tok Token, sev model.Severity, code, format string, args ...any) {
		d := Diag{Start: tok.Start, End: tok.End, Severity: sev, Code: code, Msg: fmt.Sprintf(format, args...)}
		if at >= 0 {
			d.Start, d.End = at, end
			d.Msg = strings.Join(stack, " → ") + ": " + d.Msg
		}
		res.Diags = append(res.Diags, d)
	}
	for _, tok := range Tokenize(tmpl) {
		src := tmpl[tok.Start:tok.End]
		switch tok.Kind {
		case Text:
			b.WriteString(src)
		case Escape:
			b.WriteString("{{")
		case Unclosed:
			b.WriteString(src)
			diag(tok, model.Warning, CodeUnclosed, "{{ is not closed by }}")
		case Dynamic:
			v, ok := r.dynamicValue(tok.Name)
			if !ok {
				b.WriteString(src)
				diag(tok, model.Warning, CodeUnknownDynamic, "there is no dynamic variable %s", tok.Name)
				continue
			}
			start := b.Len()
			b.WriteString(v)
			res.Spans = append(res.Spans, Span{Start: start, End: b.Len(), Var: tok.Name})
		case Var:
			r.variable(b, res, tok, src, t, stack, at, end, diag)
		}
	}
}

func (r *Resolver) variable(b *strings.Builder, res *Result, tok Token, src string, t Target, stack []string, at, end int,
	diag func(Token, model.Severity, string, string, ...any)) {
	name := tok.Name
	e, ok := r.scope.find(name)
	if !ok {
		b.WriteString(src)
		diag(tok, model.Warning, CodeUndefined, "%s is not defined", name)
		return
	}
	for _, s := range stack {
		if s == name {
			b.WriteString(src)
			diag(tok, model.Error, CodeCycle, "%s refers to itself", name)
			return
		}
	}
	if len(stack) >= r.opts.MaxDepth {
		b.WriteString(src)
		diag(tok, model.Error, CodeTooDeep, "values refer to values more than %d deep", r.opts.MaxDepth)
		return
	}
	start := b.Len()
	if e.secret {
		if p := r.opts.Policy.AllowSecret; p != nil && !p(name, t) {
			diag(tok, model.Error, CodeSecretDenied, "the secret %s may not be sent to %s", name, hostOrField(t))
			return
		}
		v := e.value
		if !e.set {
			var err error
			if v, err = r.secret(name); err != nil {
				diag(tok, model.Error, CodeSecretUnavailable, "the secret %s cannot be read: %v", name, err)
				return
			}
		}
		// A secret's value is taken as it is, never as a template.
		b.WriteString(v)
		res.Spans = append(res.Spans, Span{Start: start, End: b.Len(), Var: name, Secret: true})
		return
	}
	if at < 0 {
		at, end = tok.Start, tok.End
	}
	inner := len(res.Spans)
	res.Spans = append(res.Spans, Span{Var: name})
	r.expand(b, res, e.value, t, append(stack[:len(stack):len(stack)], name), at, end)
	res.Spans[inner].Start, res.Spans[inner].End = start, b.Len()
}

func hostOrField(t Target) string {
	if t.Host != "" {
		return t.Host
	}
	if t.Field != "" {
		return t.Field
	}
	return "this request"
}

// secret looks a secret up once per resolver.
func (r *Resolver) secret(name string) (string, error) {
	if v, ok := r.secrets[name]; ok {
		return v, nil
	}
	if r.opts.Secrets == nil {
		return "", fmt.Errorf("no secret store")
	}
	v, err := r.opts.Secrets(name)
	if err != nil {
		return "", err
	}
	r.secrets[name] = v
	return v, nil
}

func (r *Resolver) dynamicValue(name string) (string, bool) {
	if v, ok := r.dynamic[name]; ok {
		return v, true
	}
	d, ok := dynamics[name]
	if !ok {
		return "", false
	}
	v := d.gen(r)
	r.dynamic[name] = v
	return v, true
}
