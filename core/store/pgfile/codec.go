package pgfile

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jinzhongjia/postgo/core/model"
)

func kvEnts(kvs []model.KV) []ent {
	out := make([]ent, len(kvs))
	for i, kv := range kvs {
		out[i] = ent{Desc: kv.Desc, Disabled: kv.Disabled, Key: kv.Key, Value: kv.Value, HasKey: true}
	}
	return out
}

func entKVs(ps []pent) []model.KV {
	if len(ps) == 0 {
		return nil
	}
	out := make([]model.KV, len(ps))
	for i, p := range ps {
		out[i] = model.KV{Key: p.e.Key, Value: p.e.Value, Desc: p.e.Desc, Disabled: p.e.Disabled}
	}
	return out
}

// fields decodes the rows of a section naming fields: set is called for
// each enabled row; disabled rows and those set rejects as unknown are
// kept in the section's extras.
func (d *pdoc) fields(sec *psec, set func(key, value string) (known bool, err error)) {
	d.fieldsOf(sec, sec.entries, set)
}

func (d *pdoc) fieldsOf(sec *psec, ents []pent, set func(key, value string) (known bool, err error)) {
	seen := map[string]bool{}
	for _, p := range ents {
		if p.e.Disabled {
			sec.extras = append(sec.extras, p.raw)
			continue
		}
		if seen[p.e.Key] && !strings.Contains(p.e.Key, ".") {
			d.errorf(p.no, "%s is set twice", p.e.Key)
			continue
		}
		seen[p.e.Key] = true
		known, err := set(p.e.Key, p.e.Value)
		switch {
		case err != nil:
			d.errorf(p.no, "%s: %v", p.e.Key, err)
		case !known:
			d.warnf(p.no, "[%s] has no %s; it is kept as it is", strings.TrimSpace(sec.name+" "+sec.arg), p.e.Key)
			sec.extras = append(sec.extras, p.raw)
		}
	}
}

// fieldEnts makes the rows of fields, skipping empty values.
func fieldEnts(kv ...string) []ent {
	var out []ent
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			out = append(out, ent{Key: kv[i], Value: kv[i+1], HasKey: true})
		}
	}
	return out
}

func parseBool(v string) (bool, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("%q is not true or false", v)
}

// kebab returns the name of a field in files: AuthURL is auth-url,
// PKCEMethod pkce-method.
func kebab(name string) string {
	r := []rune(name)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 {
			prevLower := unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1])
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if prevLower || (unicode.IsUpper(r[i-1]) && nextLower) {
				b.WriteByte('-')
			}
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

var kvsType = reflect.TypeFor[[]model.KV]()

// structEnts makes the rows of a struct of auth fields: strings and bools
// by their kebab names, lists of KV as "name.key" rows, the name without
// its plural s.
func structEnts(v any) []ent {
	rv := reflect.ValueOf(v)
	var out []ent
	for i := 0; i < rv.NumField(); i++ {
		f, fv := rv.Type().Field(i), rv.Field(i)
		name := kebab(f.Name)
		switch {
		case fv.Kind() == reflect.String:
			if s := fv.String(); s != "" {
				out = append(out, ent{Key: name, Value: s, HasKey: true})
			}
		case fv.Kind() == reflect.Bool:
			if fv.Bool() {
				out = append(out, ent{Key: name, Value: "true", HasKey: true})
			}
		case f.Type == kvsType:
			prefix := strings.TrimSuffix(name, "s") + "."
			for _, kv := range fv.Interface().([]model.KV) {
				out = append(out, ent{Key: prefix + kv.Key, Value: kv.Value, Desc: kv.Desc, Disabled: kv.Disabled, HasKey: true})
			}
		}
	}
	return out
}

// decodeStruct sets the fields of the struct ptr points to from a
// section's rows, as structEnts writes them.
func (d *pdoc) decodeStruct(sec *psec, ptr any) {
	rv := reflect.ValueOf(ptr).Elem()
	byName := map[string]int{}
	lists := map[string]int{}
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if f.Type == kvsType {
			lists[strings.TrimSuffix(kebab(f.Name), "s")] = i
		} else {
			byName[kebab(f.Name)] = i
		}
	}
	// List rows keep their descriptions and disabled marks, so they are
	// taken before fields sees the rest.
	var rest []pent
	for _, p := range sec.entries {
		if prefix, key, ok := strings.Cut(p.e.Key, "."); ok {
			if i, isList := lists[prefix]; isList && key != "" {
				fv := rv.Field(i)
				fv.Set(reflect.Append(fv, reflect.ValueOf(model.KV{Key: key, Value: p.e.Value, Desc: p.e.Desc, Disabled: p.e.Disabled})))
				continue
			}
		}
		rest = append(rest, p)
	}
	d.fieldsOf(sec, rest, func(key, value string) (bool, error) {
		i, ok := byName[key]
		if !ok {
			return false, nil
		}
		fv := rv.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(value)
		case reflect.Bool:
			b, err := parseBool(value)
			if err != nil {
				return true, err
			}
			fv.SetBool(b)
		}
		return true, nil
	})
}

// authArgs maps an auth type to the field of model.Auth holding its own.
var authFields = map[model.AuthType]string{
	model.AuthBasic: "Basic", model.AuthBearer: "Bearer", model.AuthAPIKey: "APIKey", model.AuthDigest: "Digest",
	model.AuthOAuth1: "OAuth1", model.AuthOAuth2: "OAuth2", model.AuthAWSV4: "AWSV4", model.AuthNTLM: "NTLM", model.AuthJWT: "JWT",
}

func (d *pdoc) decodeAuth(sec *psec, a *model.Auth) {
	t := model.AuthType(sec.arg)
	if !validAuth(t) {
		d.errorf(sec.no, "%q is not an auth type", sec.arg)
		return
	}
	a.Type = t
	if field, ok := authFields[t]; ok {
		d.decodeStruct(sec, reflect.ValueOf(a).Elem().FieldByName(field).Addr().Interface())
	} else {
		d.fields(sec, func(string, string) (bool, error) { return false, nil })
	}
}

func validAuth(t model.AuthType) bool {
	for _, x := range model.AuthTypes {
		if x == t {
			return true
		}
	}
	return false
}

// authSpec is the section of an auth, or false for the default one.
func authSpec(a model.Auth, def model.AuthType) (spec, bool) {
	if a.Type == def {
		return spec{}, false
	}
	s := spec{name: "auth", arg: string(a.Type), mode: modeFields}
	if field, ok := authFields[a.Type]; ok {
		s.entries = structEnts(reflect.ValueOf(a).FieldByName(field).Interface())
	}
	return s, true
}

func (d *pdoc) decodeSettings(sec *psec, s *model.Settings, method *string) {
	d.fields(sec, func(key, v string) (bool, error) {
		var err error
		switch key {
		case "timeout":
			var t time.Duration
			if t, err = time.ParseDuration(v); err == nil {
				s.Timeout = model.Some(t)
			}
		case "follow-redirects":
			var b bool
			if b, err = parseBool(v); err == nil {
				s.FollowRedirects = model.Some(b)
			}
		case "max-redirects":
			var n int
			if n, err = strconv.Atoi(v); err == nil {
				s.MaxRedirects = model.Some(n)
			}
		case "verify-tls":
			var b bool
			if b, err = parseBool(v); err == nil {
				s.VerifyTLS = model.Some(b)
			}
		case "http-version":
			s.HTTPVersion = model.Some(model.HTTPVersion(v))
		case "encode-url":
			var b bool
			if b, err = parseBool(v); err == nil {
				s.EncodeURL = model.Some(b)
			}
		case "method":
			if method == nil {
				return false, nil
			}
			*method = v
		default:
			return false, nil
		}
		return true, err
	})
}

func optBool(o model.Opt[bool]) string {
	if !o.Set {
		return ""
	}
	return strconv.FormatBool(o.Value)
}

func settingsSpec(s model.Settings, method string) (spec, bool) {
	var timeout, redirects, version string
	if s.Timeout.Set {
		timeout = s.Timeout.Value.String()
	}
	if s.MaxRedirects.Set {
		redirects = strconv.Itoa(s.MaxRedirects.Value)
	}
	if s.HTTPVersion.Set {
		version = string(s.HTTPVersion.Value)
	}
	ents := fieldEnts("timeout", timeout, "follow-redirects", optBool(s.FollowRedirects), "max-redirects", redirects,
		"verify-tls", optBool(s.VerifyTLS), "http-version", version, "encode-url", optBool(s.EncodeURL), "method", method)
	if len(ents) == 0 {
		return spec{}, false
	}
	return spec{name: "settings", mode: modeFields, entries: ents}, true
}

func rawSpec(name, arg, info, content string) spec {
	return spec{name: name, arg: arg, mode: modeRaw, info: info, content: content, hasBlock: true}
}

func blockContent(sec *psec) string {
	if sec.block == nil {
		return ""
	}
	return sec.block.content
}

// scripts

func (d *pdoc) decodeScript(sec *psec, s *model.Scripts) {
	c := blockContent(sec)
	switch sec.arg {
	case "before-request":
		s.BeforeRequest = c
	case "after-response":
		s.AfterResponse = c
	case "tests":
		s.Tests = c
	}
}

func scriptSpecs(s model.Scripts) []spec {
	var out []spec
	for _, x := range []struct{ phase, code string }{
		{"before-request", s.BeforeRequest}, {"after-response", s.AfterResponse}, {"tests", s.Tests},
	} {
		if x.code != "" {
			out = append(out, rawSpec("script", x.phase, "js", x.code))
		}
	}
	return out
}

func kvSpec(name string, kvs []model.KV) (spec, bool) {
	if len(kvs) == 0 {
		return spec{}, false
	}
	return spec{name: name, mode: modeKV, entries: kvEnts(kvs)}, true
}
