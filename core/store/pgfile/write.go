package pgfile

import (
	"slices"
	"strings"
)

// mode is how a section's body is written.
type mode int

const (
	modeUnknown    mode = iota
	modeKV              // key = value rows
	modeFields          // key = value rows naming fields
	modeHeaders         // Name: value rows after a request line, to the first blank line
	modeHeaderList      // Name: value rows of a [headers] section
	modeList            // one value per line
	modeRaw             // one block
	modeExample         // key = value rows and one block
)

// modeOf returns how a section of a kind of file is written, and whether
// the reader knows it.
func modeOf(kind fileKind, name, arg string) (mode, bool) {
	switch kind {
	case requestFile:
		switch name {
		case "meta", "settings", "proto", "auth":
			return modeFields, true
		case "query", "path", "vars", "metadata":
			return modeKV, true
		case "body":
			switch arg {
			case "json", "text", "xml", "html", "graphql":
				return modeRaw, true
			case "form", "multipart":
				return modeKV, true
			case "file":
				return modeFields, true
			}
		case "variables", "message", "docs":
			return modeRaw, true
		case "script":
			if slices.Contains(phases, arg) {
				return modeRaw, true
			}
		case "assert":
			return modeList, true
		case "example":
			return modeExample, true
		}
	case folderFile, collectionFile:
		switch name {
		case "postgo":
			if kind == collectionFile {
				return modeFields, true
			}
		case "auth", "settings":
			return modeFields, true
		case "headers":
			return modeHeaderList, true
		case "vars":
			return modeKV, true
		case "script":
			if slices.Contains(phases, arg) {
				return modeRaw, true
			}
		case "order":
			return modeList, true
		case "docs":
			return modeRaw, true
		}
	case envFile:
		switch name {
		case "meta":
			return modeFields, true
		case "vars":
			return modeKV, true
		case "secrets":
			return modeList, true
		}
	}
	return modeUnknown, false
}

var phases = []string{"before-request", "after-response", "tests"}

// multi reports whether a file may have several sections of the name,
// told apart by their arguments.
func multi(name string) bool { return name == "script" || name == "message" || name == "example" }

// family is what identifies a section for matching the file's sections
// with the model's: the name, and the argument of sections there may be
// several of.
func family(name, arg string) string {
	if multi(name) {
		return name + " " + arg
	}
	return name
}

// spec is a section as the model has it.
type spec struct {
	name, arg string
	mode      mode
	entries   []ent
	info      string // of the block
	content   string
	hasBlock  bool
}

func (s spec) family() string { return family(s.name, s.arg) }

func (s spec) equal(o spec) bool {
	return s.name == o.name && s.arg == o.arg && s.info == o.info && s.content == o.content &&
		s.hasBlock == o.hasBlock && slices.Equal(s.entries, o.entries)
}

// head is a request's first line and its headers, as the model has them.
type head struct {
	line    string
	entries []ent
}

func (h head) equal(o head) bool { return h.line == o.line && slices.Equal(h.entries, o.entries) }

// Origin is what a reader keeps of a file to write it back as it was.
type Origin struct {
	doc   *pdoc
	head  head
	specs map[string]spec // by family, as the file's model writes them
}

// Warnings returns the problems of the file that did not keep it from
// being read.
func (o *Origin) Warnings() []Problem {
	if o == nil {
		return nil
	}
	return slices.Clone(o.doc.probs)
}

func newOrigin(d *pdoc, h head, specs []spec) *Origin {
	o := &Origin{doc: d, head: h, specs: map[string]spec{}}
	for _, s := range specs {
		o.specs[s.family()] = s
	}
	return o
}

type writer struct {
	b  strings.Builder
	nl string
}

func (w *writer) raw(s string) { w.b.WriteString(s) }

// endLine ends the last line when it has no line ending, as a file
// without a final newline has.
func (w *writer) endLine() {
	if s := w.b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		w.b.WriteString(w.nl)
	}
}

func (w *writer) line(s string) {
	w.b.WriteString(s)
	w.b.WriteString(w.nl)
}

// fenceFor returns tildes that no line of content starts with as many of.
func fenceFor(content string) string {
	n := 3
	for _, l := range strings.Split(content, "\n") {
		run := 0
		for run < len(l) && l[run] == '~' {
			run++
		}
		n = max(n, run+1)
	}
	return strings.Repeat("~", n)
}

func (w *writer) block(fence, info, content string) {
	w.line(fence + info)
	if content != "" {
		for _, l := range strings.Split(content, "\n") {
			w.line(l)
		}
	}
	w.line(fence)
}

// escape escapes the start of an entry's line so that it reads back as the
// entry.
func escape(text string) string {
	if text != "" && (strings.IndexByte("#[~\\", text[0]) >= 0 || strings.HasPrefix(text, "//")) {
		return "\\" + text
	}
	return text
}

func needsFence(v string) bool {
	return strings.ContainsAny(v, "\r\n") || strings.TrimSpace(v) != v || isFence(v)
}

func (w *writer) entry(e ent, m mode) {
	if e.Desc != "" {
		for _, l := range strings.Split(e.Desc, "\n") {
			if l == "" {
				w.line("#")
			} else {
				w.line("# " + l)
			}
		}
	}
	prefix := ""
	if e.Disabled {
		prefix = "// "
	}
	switch {
	case !e.HasKey:
		w.line(prefix + escape(e.Value))
	case m == modeHeaders || m == modeHeaderList:
		if e.Value == "" {
			w.line(prefix + escape(e.Key) + ":")
		} else {
			w.line(prefix + escape(e.Key) + ": " + e.Value)
		}
	case e.Value == "":
		w.line(prefix + escape(e.Key) + " =")
	case needsFence(e.Value):
		f := fenceFor(e.Value)
		w.line(prefix + escape(e.Key) + " = " + f)
		if e.Value != "" {
			for _, l := range strings.Split(e.Value, "\n") {
				w.line(l)
			}
		}
		w.line(f)
	default:
		w.line(prefix + escape(e.Key) + " = " + e.Value)
	}
}

// entries writes the entries, reusing the text of the file's entries that
// are the same.
func (w *writer) entries(ents []ent, m mode, from []pent) {
	used := make([]bool, len(from))
	for _, e := range ents {
		reused := false
		for i, p := range from {
			if !used[i] && p.e == e {
				used[i], reused = true, true
				w.raw(p.raw)
				w.endLine()
				break
			}
		}
		if !reused {
			w.entry(e, m)
		}
	}
}

func header(name, arg string) string {
	if arg == "" {
		return "[" + name + "]"
	}
	return "[" + name + " " + arg + "]"
}

func (w *writer) section(s spec, from *psec) {
	if from != nil && from.name == s.name && from.arg == s.arg {
		// Keep the header as written, as [ meta ].
		w.raw(strings.SplitAfterN(from.raw, "\n", 2)[0])
		w.endLine()
	} else {
		w.line(header(s.name, s.arg))
	}
	var prev []pent
	if from != nil {
		prev = from.entries
	}
	switch s.mode {
	case modeRaw:
		w.block(fenceFor(s.content), s.info, s.content)
	default:
		w.entries(s.entries, s.mode, prev)
		if s.mode == modeExample && s.hasBlock {
			w.block(fenceFor(s.content), s.info, s.content)
		}
	}
	if from != nil {
		for _, x := range from.extras {
			w.raw(x)
			w.endLine()
		}
	}
}

// item is a section of the output: written as the file had it, or anew.
type item struct {
	rank  int // the spec's index, -1 for sections the model has nothing of
	gap   string
	fresh bool   // not in the file: written after one blank line
	raw   string // when kept
	spec  *spec
	from  *psec
}

// write writes a file of the head (nil for files without one) and the
// sections the model has, keeping what it can of the file o was read from.
func write(h *head, specs []spec, o *Origin) []byte {
	w := &writer{nl: "\n"}
	if o == nil {
		if h != nil {
			w.line(h.line)
			w.entries(h.entries, modeHeaders, nil)
		}
		for i := range specs {
			if w.b.Len() > 0 {
				w.line("")
			}
			w.section(specs[i], nil)
		}
		return []byte(w.b.String())
	}
	d := o.doc
	if d.crlf {
		w.nl = "\r\n"
	}
	w.raw(d.preamble)
	if h != nil {
		switch {
		case d.head != nil && h.equal(o.head):
			w.raw(d.head.raw)
		default:
			w.endLine()
			if d.head != nil && h.line == o.head.line {
				w.raw(d.head.lineRaw)
				w.endLine()
			} else {
				w.line(h.line)
			}
			var prev []pent
			if d.head != nil {
				prev = d.head.entries
			}
			w.entries(h.entries, modeHeaders, prev)
		}
	}

	byFamily := map[string]int{}
	for i, s := range specs {
		byFamily[s.family()] = i
	}
	var items []item
	placed := make([]bool, len(specs))
	for _, sec := range d.sections {
		f := family(sec.name, sec.arg)
		i, inModel := byFamily[f]
		switch {
		case !sec.known:
			items = append(items, item{rank: -1, gap: sec.gap, raw: sec.raw})
		case inModel:
			placed[i] = true
			old, had := o.specs[f]
			if had && old.equal(specs[i]) {
				items = append(items, item{rank: i, gap: sec.gap, raw: sec.raw})
			} else {
				items = append(items, item{rank: i, gap: sec.gap, spec: &specs[i], from: sec})
			}
		default:
			if _, had := o.specs[f]; !had {
				// The section meant nothing, as [auth inherit] or an empty
				// [query], and still does: keep it.
				items = append(items, item{rank: -1, gap: sec.gap, raw: sec.raw})
			}
		}
	}
	for i := range specs {
		if placed[i] {
			continue
		}
		if old, had := o.specs[specs[i].family()]; had && old.equal(specs[i]) {
			// The file never had the section: the reader gave the model
			// its default, as a gRPC request's reflection.
			continue
		}
		at := 0
		for j, it := range items {
			if it.rank >= 0 && it.rank < i {
				at = j + 1
			}
		}
		items = slices.Insert(items, at, item{rank: i, fresh: true, spec: &specs[i]})
	}
	freshBefore := false
	for _, it := range items {
		switch {
		case it.fresh && w.b.Len() == 0:
		case it.fresh:
			w.endLine()
			w.line("")
		case it.gap == "" && freshBefore:
			// The file's first section, which had nothing before it, now
			// follows another.
			w.endLine()
			w.line("")
		default:
			w.endLine()
			w.raw(it.gap)
		}
		if it.spec != nil {
			w.section(*it.spec, it.from)
		} else {
			w.raw(it.raw)
		}
		freshBefore = freshBefore || it.fresh
	}
	if d.tail != "" {
		w.endLine()
		w.raw(d.tail)
	}
	return []byte(w.b.String())
}
