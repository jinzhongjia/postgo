// Package pgfile reads and writes the files of a collection: requests,
// folders, the collection's root and environments, in the .postgo format
// (docs/FORMAT.md).
//
// Reading a file returns the model and an Origin, which remembers the
// file's text. Writing with the Origin keeps every part of the text whose
// meaning did not change byte for byte, sections the reader does not know
// included: a file read and written back without changes is the same.
package pgfile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jinzhongjia/postgo/core/model"
)

// Problem is a problem of a file, at a line counted from 1.
type Problem struct {
	Line     int
	Severity model.Severity
	Msg      string
}

func (p Problem) String() string { return fmt.Sprintf("line %d: %s: %s", p.Line, p.Severity, p.Msg) }

// ParseError is the error of reading a file that has errors. Problems
// holds them all, warnings included.
type ParseError struct {
	Problems []Problem
}

func (e *ParseError) Error() string {
	var msgs []string
	for _, p := range e.Problems {
		if p.Severity == model.Error {
			msgs = append(msgs, p.String())
		}
	}
	return "pgfile: " + strings.Join(msgs, "; ")
}

// ErrNewerFormat is the error of reading a collection written in a newer
// format than this package knows.
var ErrNewerFormat = errors.New("pgfile: the collection is in a newer format")

// fileKind is the kind of a file, which decides its sections.
type fileKind int

const (
	requestFile fileKind = iota
	folderFile
	collectionFile
	envFile
)

type pline struct {
	text string // without its line ending
	raw  string // with it
	no   int
}

// ent is the text of an entry: an "key = value" row, a header, or a line
// of a list, with its description and whether it is disabled.
type ent struct {
	Desc     string
	Disabled bool
	Key      string
	Value    string
	HasKey   bool
}

// pent is an entry as the file has it.
type pent struct {
	e   ent
	raw string
	no  int
}

type pblock struct {
	info    string
	content string
	no      int
}

// psec is a section as the file has it.
type psec struct {
	gap       string // the blank lines before it
	name, arg string
	raw       string // its header and body, without the blank lines after
	no        int
	mode      mode
	known     bool
	entries   []pent
	block     *pblock
	extras    []string // raw entries kept as they are: unknown and disabled fields
}

type phead struct {
	line    string
	lineRaw string
	entries []pent
	raw     string
	no      int
}

// pdoc is a parsed file.
type pdoc struct {
	crlf     bool
	preamble string
	head     *phead
	sections []*psec
	tail     string
	probs    []Problem
}

func (d *pdoc) add(no int, sev model.Severity, format string, args ...any) {
	d.probs = append(d.probs, Problem{Line: no, Severity: sev, Msg: fmt.Sprintf(format, args...)})
}

func (d *pdoc) errorf(no int, format string, args ...any) { d.add(no, model.Error, format, args...) }

func (d *pdoc) warnf(no int, format string, args ...any) { d.add(no, model.Warning, format, args...) }

func (d *pdoc) err() error {
	for _, p := range d.probs {
		if p.Severity == model.Error {
			return &ParseError{Problems: d.probs}
		}
	}
	return nil
}

func splitLines(src string) []pline {
	var lines []pline
	for no := 1; src != ""; no++ {
		i := strings.IndexByte(src, '\n')
		raw := src
		if i >= 0 {
			raw = src[:i+1]
		}
		src = src[len(raw):]
		text := strings.TrimSuffix(strings.TrimSuffix(raw, "\r\n"), "\n")
		lines = append(lines, pline{text: text, raw: raw, no: no})
	}
	return lines
}

func blank(l pline) bool { return strings.TrimSpace(l.text) == "" }

func isDesc(text string) bool { return strings.HasPrefix(text, "#") }

// sectionHeader returns the name and argument of a section's header.
func sectionHeader(text string) (name, arg string, ok bool) {
	t := strings.TrimRight(text, " \t")
	if len(t) < 2 || t[0] != '[' || t[len(t)-1] != ']' {
		return "", "", false
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	name, arg, _ = strings.Cut(inner, " ")
	return name, strings.TrimSpace(arg), true
}

func isHeader(l pline) bool { _, _, ok := sectionHeader(l.text); return ok }

// fenceOpen returns the tildes and the info string of a line opening a
// block.
func fenceOpen(text string) (fence, info string, ok bool) {
	n := 0
	for n < len(text) && text[n] == '~' {
		n++
	}
	if n < 3 {
		return "", "", false
	}
	return text[:n], strings.TrimSpace(text[n:]), true
}

func isFence(s string) bool {
	if len(s) < 3 {
		return false
	}
	return strings.Trim(s, "~") == ""
}

// readBlock reads the lines of a block opened by fence at lines[i-1],
// returning its content and the index after its closing fence.
func (d *pdoc) readBlock(lines []pline, i int, fence string, openNo int) (string, int) {
	var content []string
	for ; i < len(lines); i++ {
		if lines[i].text == fence {
			return strings.Join(content, "\n"), i + 1
		}
		content = append(content, lines[i].text)
	}
	d.errorf(openNo, "the block opened here is not closed by %s", fence)
	return strings.Join(content, "\n"), len(lines)
}

func joinRaw(lines []pline, from, to int) string {
	var b strings.Builder
	for _, l := range lines[from:to] {
		b.WriteString(l.raw)
	}
	return b.String()
}

const escapable = "#/[~\\"

// entryText takes the disabled mark and the escape off an entry's line.
func entryText(text string) (string, bool) {
	disabled := false
	if strings.HasPrefix(text, "//") {
		disabled = true
		text = strings.TrimPrefix(text[2:], " ")
	}
	if len(text) > 1 && text[0] == '\\' && strings.IndexByte(escapable, text[1]) >= 0 {
		text = text[1:]
	}
	return text, disabled
}

// parseEntries reads the entries of a section, or of a request's headers,
// from lines[i] to the next section (or, for headers, the first blank
// line), and returns the index after them.
func (d *pdoc) parseEntries(lines []pline, i int, m mode) (ents []pent, block *pblock, end int) {
	var descFrom = -1
	var desc []string
	flushDesc := func() {
		if descFrom >= 0 {
			d.errorf(lines[descFrom].no, "a description must be right above an entry")
			descFrom, desc = -1, nil
		}
	}
	for i < len(lines) {
		l := lines[i]
		if isHeader(l) {
			break
		}
		if blank(l) {
			if m == modeHeaders {
				break
			}
			flushDesc()
			i++
			continue
		}
		if isDesc(l.text) {
			if descFrom < 0 {
				descFrom = i
			}
			desc = append(desc, strings.TrimPrefix(l.text[1:], " "))
			i++
			continue
		}
		if m == modeExample {
			if fence, info, ok := fenceOpen(l.text); ok {
				flushDesc()
				content, next := d.readBlock(lines, i+1, fence, l.no)
				if block != nil {
					d.errorf(l.no, "an example has one body")
				}
				block = &pblock{info: info, content: content, no: l.no}
				i = next
				continue
			}
		}
		start := i
		if descFrom >= 0 {
			start = descFrom
		}
		text, disabled := entryText(l.text)
		e := ent{Desc: strings.Join(desc, "\n"), Disabled: disabled}
		next := i + 1
		switch m {
		case modeList:
			e.Value = text
		case modeHeaders, modeHeaderList:
			e.HasKey = true
			if k, v, ok := strings.Cut(text, ": "); ok {
				e.Key, e.Value = k, v
			} else if strings.HasSuffix(text, ":") {
				e.Key = text[:len(text)-1]
			} else {
				d.errorf(l.no, "expected Name: value")
			}
		default:
			e.HasKey = true
			if k, v, ok := strings.Cut(text, " = "); ok {
				e.Key, e.Value = k, v
				if isFence(v) {
					e.Value, next = d.readBlock(lines, i+1, v, l.no)
				}
			} else if strings.HasSuffix(text, " =") {
				e.Key = text[:len(text)-2]
			} else {
				d.errorf(l.no, "expected key = value")
			}
		}
		ents = append(ents, pent{e: e, raw: joinRaw(lines, start, next), no: l.no})
		descFrom, desc = -1, nil
		i = next
	}
	flushDesc()
	return ents, block, i
}

// parseRaw reads the block of a section holding one, and returns the
// index of the next section.
func (d *pdoc) parseRaw(lines []pline, i int) (*pblock, int) {
	var block *pblock
	for i < len(lines) {
		l := lines[i]
		if isHeader(l) {
			break
		}
		if blank(l) {
			i++
			continue
		}
		fence, info, ok := fenceOpen(l.text)
		if !ok {
			d.errorf(l.no, "expected a block between ~~~ lines")
			i++
			continue
		}
		content, next := d.readBlock(lines, i+1, fence, l.no)
		if block != nil {
			d.errorf(l.no, "the section has one block")
		}
		block = &pblock{info: info, content: content, no: l.no}
		i = next
	}
	return block, i
}

// skipUnknown finds the end of a section the reader does not know, by the
// rules every section follows: blocks open at a line of tildes or at a
// value of tildes.
func (d *pdoc) skipUnknown(lines []pline, i int) int {
	for i < len(lines) {
		l := lines[i]
		if isHeader(l) {
			break
		}
		i++
		if isDesc(l.text) {
			continue
		}
		if fence, _, ok := fenceOpen(l.text); ok {
			_, i = d.readBlock(lines, i, fence, l.no)
		} else if text, _ := entryText(l.text); strings.Contains(text, " = ") {
			if _, v, _ := strings.Cut(text, " = "); isFence(v) {
				_, i = d.readBlock(lines, i, v, l.no)
			}
		}
	}
	return i
}

// parse reads a file into its parts; decoding them is the caller's.
func parse(src []byte, kind fileKind) *pdoc {
	s := string(src)
	d := &pdoc{crlf: strings.Contains(s, "\r\n")}
	lines := splitLines(s)
	i := 0
	for i < len(lines) && (blank(lines[i]) || isDesc(lines[i].text)) {
		i++
	}
	if kind == requestFile {
		if i == len(lines) || isHeader(lines[i]) {
			no := len(lines)
			if i < len(lines) {
				no = lines[i].no
			}
			d.errorf(max(no, 1), "a request starts with its method and URL, as GET https://example.com")
		} else {
			// The blank lines just before the request line are part of
			// the preamble too.
			d.preamble = joinRaw(lines, 0, i)
			h := &phead{line: lines[i].text, lineRaw: lines[i].raw, no: lines[i].no}
			start := i
			h.entries, _, i = d.parseEntries(lines, i+1, modeHeaders)
			h.raw = joinRaw(lines, start, i)
			d.head = h
		}
	} else {
		// Blank lines before the first section are its gap, not the
		// preamble.
		j := i
		for j > 0 && blank(lines[j-1]) {
			j--
		}
		d.preamble = joinRaw(lines, 0, j)
		i = j
	}
	for i < len(lines) {
		gapFrom := i
		for i < len(lines) && blank(lines[i]) {
			i++
		}
		if i == len(lines) {
			d.tail = joinRaw(lines, gapFrom, i)
			break
		}
		l := lines[i]
		name, arg, ok := sectionHeader(l.text)
		if !ok {
			d.errorf(l.no, "expected a section, as [query]")
			for i < len(lines) && !isHeader(lines[i]) {
				i++
			}
			continue
		}
		if name == "" {
			d.errorf(l.no, "a section needs a name")
		}
		sec := &psec{gap: joinRaw(lines, gapFrom, i), name: name, arg: arg, no: l.no}
		sec.mode, sec.known = modeOf(kind, name, arg)
		var end int
		switch sec.mode {
		case modeRaw:
			sec.block, end = d.parseRaw(lines, i+1)
		case modeUnknown:
			end = d.skipUnknown(lines, i+1)
		default:
			sec.entries, sec.block, end = d.parseEntries(lines, i+1, sec.mode)
		}
		for end > i+1 && blank(lines[end-1]) {
			end--
		}
		sec.raw = joinRaw(lines, i, end)
		d.sections = append(d.sections, sec)
		i = end
	}
	return d
}
