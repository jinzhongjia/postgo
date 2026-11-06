package pgfile

import (
	"strconv"
	"strings"

	"github.com/jinzhongjia/postgo/core/model"
)

var lineKinds = map[string]model.Kind{"GRAPHQL": model.KindGraphQL, "WS": model.KindWS, "SSE": model.KindSSE, "GRPC": model.KindGRPC}

// ReadRequest reads a request's file. id is the request's ID, its path in
// the collection. A file with errors returns a *ParseError.
func ReadRequest(id string, src []byte) (*model.Request, *Origin, error) {
	d := parse(src, requestFile)
	r := &model.Request{ID: id, Body: model.Body{Type: model.BodyNone}, Auth: model.Auth{Type: model.AuthInherit}}
	if d.head != nil {
		d.decodeHead(r)
		d.decodeRequest(r)
	}
	if err := d.err(); err != nil {
		return nil, nil, err
	}
	h, specs := requestSpecs(r)
	return r, newOrigin(d, h, specs), nil
}

// WriteRequest writes a request's file, keeping what it can of the file o
// was read from; o may be nil.
func WriteRequest(r *model.Request, o *Origin) []byte {
	h, specs := requestSpecs(r)
	return write(&h, specs, o)
}

func (d *pdoc) decodeHead(r *model.Request) {
	method, url, _ := strings.Cut(d.head.line, " ")
	r.URL = url
	if k, ok := lineKinds[strings.ToUpper(method)]; ok {
		r.Kind = k
		if k == model.KindGraphQL {
			r.Method = "POST"
			r.Body.Type = model.BodyGraphQL
		}
		if k == model.KindGRPC {
			r.Proto.Source = model.ProtoReflection
		}
	} else {
		r.Kind, r.Method = model.KindHTTP, method
	}
	r.Headers = entKVs(d.head.entries)
}

func requestLine(r *model.Request) string {
	switch r.Kind {
	case model.KindGraphQL:
		return "GRAPHQL " + r.URL
	case model.KindWS:
		return "WS " + r.URL
	case model.KindSSE:
		return "SSE " + r.URL
	case model.KindGRPC:
		return "GRPC " + r.URL
	}
	return r.Method + " " + r.URL
}

// checkFamilies reports sections a file has twice.
func (d *pdoc) checkFamilies() {
	seen := map[string]bool{}
	for _, sec := range d.sections {
		if !sec.known {
			continue
		}
		f := family(sec.name, sec.arg)
		if seen[f] {
			d.errorf(sec.no, "there is already a [%s] section", f)
		}
		seen[f] = true
	}
}

func (d *pdoc) unknown(sec *psec) {
	d.warnf(sec.no, "%s is not a section this version knows; it is kept as it is", header(sec.name, sec.arg))
}

func (d *pdoc) decodeRequest(r *model.Request) {
	d.checkFamilies()
	for _, sec := range d.sections {
		if !sec.known {
			d.unknown(sec)
			continue
		}
		switch sec.name {
		case "meta":
			d.fields(sec, func(k, v string) (bool, error) {
				switch k {
				case "name":
					r.Name = v
				case "tags":
					r.Tags = splitTags(v)
				default:
					return false, nil
				}
				return true, nil
			})
		case "query":
			r.Query = entKVs(sec.entries)
		case "path":
			r.Path = entKVs(sec.entries)
		case "body":
			d.decodeBody(sec, &r.Body)
		case "variables":
			r.Body.Variables = blockContent(sec)
		case "auth":
			d.decodeAuth(sec, &r.Auth)
		case "vars":
			r.Vars = entKVs(sec.entries)
		case "script":
			d.decodeScript(sec, &r.Scripts)
		case "assert":
			for _, p := range sec.entries {
				r.Asserts = append(r.Asserts, model.Assert{Expr: p.e.Value, Desc: p.e.Desc, Disabled: p.e.Disabled})
			}
		case "settings":
			var method *string
			if r.Kind == model.KindGraphQL {
				method = &r.Method
			}
			d.decodeSettings(sec, &r.Settings, method)
		case "message":
			r.Messages = append(r.Messages, model.Message{Name: sec.arg, Content: blockContent(sec)})
		case "metadata":
			r.Metadata = entKVs(sec.entries)
		case "proto":
			d.fields(sec, func(k, v string) (bool, error) {
				switch k {
				case "reflection":
					b, err := parseBool(v)
					if b {
						r.Proto = model.Proto{Source: model.ProtoReflection}
					}
					return true, err
				case "file":
					r.Proto = model.Proto{Source: model.ProtoFile, File: v}
				default:
					return false, nil
				}
				return true, nil
			})
		case "example":
			e := model.Example{Name: sec.arg, Body: blockContent(sec)}
			d.fields(sec, func(k, v string) (bool, error) {
				if k != "status" {
					return false, nil
				}
				n, err := strconv.Atoi(v)
				e.Status = n
				return true, err
			})
			r.Examples = append(r.Examples, e)
		case "docs":
			r.Docs = blockContent(sec)
		}
	}
}

// messageInfo labels the block of a message that looks like JSON, for
// editors to highlight.
func messageInfo(content string) string {
	if c := strings.TrimSpace(content); strings.HasPrefix(c, "{") || strings.HasPrefix(c, "[") {
		return "json"
	}
	return ""
}

func splitTags(v string) []string {
	var tags []string
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func (d *pdoc) decodeBody(sec *psec, b *model.Body) {
	b.Type = model.BodyType(sec.arg)
	switch b.Type {
	case model.BodyForm:
		b.Form = entKVs(sec.entries)
	case model.BodyMultipart:
		for _, p := range sec.entries {
			part := model.Part{KV: model.KV{Key: p.e.Key, Desc: p.e.Desc, Disabled: p.e.Disabled}}
			v := p.e.Value
			switch {
			case strings.HasPrefix(v, "@"):
				part.File, part.Value = true, v[1:]
			case strings.HasPrefix(v, `\@`), strings.HasPrefix(v, `\\`):
				part.Value = v[1:]
			default:
				part.Value = v
			}
			b.Parts = append(b.Parts, part)
		}
	case model.BodyFile:
		d.fields(sec, func(k, v string) (bool, error) {
			if k != "path" {
				return false, nil
			}
			b.File = v
			return true, nil
		})
	default:
		b.Raw = blockContent(sec)
	}
}

func partValue(p model.Part) string {
	switch {
	case p.File:
		return "@" + p.Value
	case strings.HasPrefix(p.Value, "@"), strings.HasPrefix(p.Value, `\`):
		return `\` + p.Value
	}
	return p.Value
}

func bodySpecs(b model.Body) []spec {
	arg := string(b.Type)
	var vars []spec
	if b.Variables != "" {
		vars = []spec{rawSpec("variables", "", "json", b.Variables)}
	}
	switch b.Type {
	case model.BodyNone, "":
		return vars
	case model.BodyForm:
		return append([]spec{{name: "body", arg: arg, mode: modeKV, entries: kvEnts(b.Form)}}, vars...)
	case model.BodyMultipart:
		ents := make([]ent, len(b.Parts))
		for i, p := range b.Parts {
			ents[i] = ent{Key: p.Key, Value: partValue(p), Desc: p.Desc, Disabled: p.Disabled, HasKey: true}
		}
		return append([]spec{{name: "body", arg: arg, mode: modeKV, entries: ents}}, vars...)
	case model.BodyFile:
		return append([]spec{{name: "body", arg: arg, mode: modeFields, entries: fieldEnts("path", b.File)}}, vars...)
	}
	return append([]spec{rawSpec("body", arg, arg, b.Raw)}, vars...)
}

// requestSpecs returns a request's first lines and its sections, in the
// order FORMAT.md gives them.
func requestSpecs(r *model.Request) (head, []spec) {
	h := head{line: requestLine(r), entries: kvEnts(r.Headers)}
	var specs []spec
	add := func(s spec, ok bool) {
		if ok {
			specs = append(specs, s)
		}
	}
	add(spec{name: "meta", mode: modeFields, entries: fieldEnts("name", r.Name, "tags", strings.Join(r.Tags, ", "))}, r.Name != "" || len(r.Tags) > 0)
	add(kvSpec("query", r.Query))
	add(kvSpec("path", r.Path))
	specs = append(specs, bodySpecs(r.Body)...)
	add(authSpec(r.Auth, model.AuthInherit))
	add(kvSpec("vars", r.Vars))
	specs = append(specs, scriptSpecs(r.Scripts)...)
	if len(r.Asserts) > 0 {
		ents := make([]ent, len(r.Asserts))
		for i, a := range r.Asserts {
			ents[i] = ent{Value: a.Expr, Desc: a.Desc, Disabled: a.Disabled}
		}
		specs = append(specs, spec{name: "assert", mode: modeList, entries: ents})
	}
	method := ""
	if r.Kind == model.KindGraphQL && r.Method != "POST" {
		method = r.Method
	}
	add(settingsSpec(r.Settings, method))
	for _, m := range r.Messages {
		specs = append(specs, rawSpec("message", m.Name, messageInfo(m.Content), m.Content))
	}
	add(kvSpec("metadata", r.Metadata))
	switch r.Proto.Source {
	case model.ProtoReflection:
		specs = append(specs, spec{name: "proto", mode: modeFields, entries: fieldEnts("reflection", "true")})
	case model.ProtoFile:
		specs = append(specs, spec{name: "proto", mode: modeFields, entries: fieldEnts("file", r.Proto.File)})
	}
	for _, e := range r.Examples {
		specs = append(specs, spec{name: "example", arg: e.Name, mode: modeExample,
			entries: fieldEnts("status", strconv.Itoa(e.Status)), content: e.Body, hasBlock: e.Body != ""})
	}
	add(rawSpec("docs", "", "md", r.Docs), r.Docs != "")
	return h, specs
}
