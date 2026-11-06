// Package vars resolves the {{variables}} of templates: URLs, header
// values, bodies, auth fields.
//
// A Scope holds the variables of every level, from the globals to what
// scripts set; a Resolver resolves templates against a scope for one send
// of a request, with dynamic variables such as {{$uuid}}, secrets looked
// up when needed, and a Policy that may refuse a secret to a host.
package vars

import (
	"regexp"
	"strings"
)

// TokenKind is the kind of a Token.
type TokenKind int

const (
	Text     TokenKind = iota // text without references
	Var                       // {{name}}
	Dynamic                   // {{$name}}
	Escape                    // \{{, which stands for a literal {{
	Unclosed                  // {{ without a closing }}, to the end
)

func (k TokenKind) String() string {
	switch k {
	case Var:
		return "var"
	case Dynamic:
		return "dynamic"
	case Escape:
		return "escape"
	case Unclosed:
		return "unclosed"
	}
	return "text"
}

// Token is a piece of a template: bytes Start to End, and the name
// referred to, without braces or spaces.
type Token struct {
	Kind       TokenKind
	Start, End int
	Name       string
}

var (
	varName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	dynamicName = regexp.MustCompile(`^\$[A-Za-z][A-Za-z0-9]*$`)
)

// Tokenize splits a template into tokens, which cover it from its first
// byte to its last. Braces around something that is not a name, as
// {{ 1 + 2 }}, are text.
func Tokenize(tmpl string) []Token {
	var toks []Token
	text := func(start, end int) {
		if start == end {
			return
		}
		if n := len(toks); n > 0 && toks[n-1].Kind == Text && toks[n-1].End == start {
			toks[n-1].End = end
			return
		}
		toks = append(toks, Token{Kind: Text, Start: start, End: end})
	}
	i := 0
	for i < len(tmpl) {
		j := strings.Index(tmpl[i:], "{{")
		if j < 0 {
			text(i, len(tmpl))
			break
		}
		j += i
		if j > i && tmpl[j-1] == '\\' {
			text(i, j-1)
			toks = append(toks, Token{Kind: Escape, Start: j - 1, End: j + 2})
			i = j + 2
			continue
		}
		text(i, j)
		k := strings.Index(tmpl[j+2:], "}}")
		if k < 0 {
			toks = append(toks, Token{Kind: Unclosed, Start: j, End: len(tmpl)})
			break
		}
		end := j + 2 + k + 2
		name := strings.TrimSpace(tmpl[j+2 : j+2+k])
		switch {
		case varName.MatchString(name):
			toks = append(toks, Token{Kind: Var, Start: j, End: end, Name: name})
		case dynamicName.MatchString(name):
			toks = append(toks, Token{Kind: Dynamic, Start: j, End: end, Name: name})
		default:
			text(j, end)
		}
		i = end
	}
	return toks
}
