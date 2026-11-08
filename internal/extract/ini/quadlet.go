package ini

import (
	"bytes"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// readQuadlet reads a Podman Quadlet file, or a drop-in of one, as Podman
// 5.4.0's UnitFile.Parse reads it (pkg/systemd/parser/unitfile.go), which
// differs from systemd's parser:
//
//   - a line ends at \n alone; a \r or a NUL is a byte of the line, and
//     white space at both ends of a line goes, as Go's strings.TrimSpace
//     takes it;
//   - an empty line and a line that starts with # or ; are comments, and
//     inside a value a \ continues, a comment line is skipped, so an empty
//     line does not end the value;
//   - a \ at the end of a line continues the value with no space for it,
//     even after another \, but on the last line, where it only goes;
//   - [name] is a group, with only spaces and tabs after the first ]; a
//     name that is empty or holds [ or a control character is refused, and
//     a quote or a \ in it is read;
//   - key=value at the first =, the key without the white space before the
//     =, the value without the white space after it; a line that is no
//     comment, group or key=value is refused, and so is a key before the
//     first group, as Quadlet refuses to generate the unit.
//
// The file is not checked for UTF-8, and has no line length limit, as
// Podman reads it whole. A key set to nothing is a reset, as LookupAll
// reads it.
func readQuadlet(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	r := quadletReader{visit: visit, lines: core.NewLineCursor(data)}
	text := bytes.TrimSuffix(data, []byte("\n"))
	for off := 0; ; {
		end := bytes.IndexByte(text[off:], '\n')
		last := end < 0
		if last {
			end = len(text) - off
		}
		if err := m.Charge(core.LineWork + end + 1); err != nil {
			return err
		}
		if err := r.line(off, text[off:off+end], last); err != nil || last {
			return err
		}
		off += end + 1
	}
}

// quadletReader is readQuadlet's state between lines.
type quadletReader struct {
	visit   func(*core.Entry) error
	lines   *core.LineCursor
	section []byte
	seq     int
	joined  []byte // what lines a \ ended hold, Parse's remaining
	at      int    // where the first line joined starts
	e       core.Entry
}

// line reads the line at off as Parse reads it.
func (r *quadletReader) line(off int, raw []byte, last bool) error {
	line := bytes.TrimSpace(raw)
	start := off + len(raw) - len(bytes.TrimLeftFunc(raw, unicode.IsSpace))
	if quadletComment(line) {
		return nil // a comment, also inside a value a \ continues
	}
	if cut, ok := bytes.CutSuffix(line, []byte(`\`)); ok && !last {
		if len(r.joined) == 0 {
			r.at = start
		}
		r.joined = append(r.joined, cut...)
		return nil
	} else if ok {
		line = cut
	}
	if len(r.joined) > 0 {
		line, start = append(r.joined, line...), r.at
		r.joined = r.joined[:0]
	}
	return r.parse(line, r.lines.At(start))
}

// quadletComment is lineIsComment: an empty line, or one that starts with
// # or ;.
func quadletComment(line []byte) bool {
	return len(line) == 0 || line[0] == '#' || line[0] == ';'
}

// parse is parseLine: a group, a key=value, or a refusal.
func (r *quadletReader) parse(line []byte, n int) error {
	if quadletComment(line) {
		return nil
	}
	if name, ok := quadletGroup(line); ok {
		return r.group(name, n)
	}
	eq := bytes.IndexByte(line, '=')
	switch {
	case eq <= 0:
		return core.LineError(n, "%s is no key=value, group or comment, which Quadlet refuses", core.Quote(string(line)))
	case r.section == nil:
		return core.LineError(n, "a key before the first group, which Quadlet refuses")
	}
	key := chompKey(line[:eq])
	if len(key) == 0 {
		return core.LineError(n, "the key of %s is white space alone, which Quadlet refuses", core.Quote(string(line)))
	}
	r.e = core.Entry{Section: r.section, SectionSeq: r.seq, Key: key, Value: chugValue(line[eq+1:]), Line: n}
	if len(r.e.Value) == 0 {
		r.e.Kind = core.EntryReset
	}
	r.e.Computed = systemdComputes(r.e.Key, r.e.Value)
	return r.visit(&r.e)
}

// chompKey is a key without the white space before its =, as
// parseKeyValuePair chomps it byte by byte.
func chompKey(key []byte) []byte {
	for len(key) > 0 && podmanSpace(key[len(key)-1]) {
		key = key[:len(key)-1]
	}
	return key
}

// chugValue is a value without the white space after its =.
func chugValue(value []byte) []byte {
	for len(value) > 0 && podmanSpace(value[0]) {
		value = value[1:]
	}
	return value
}

// podmanSpace is unicode.IsSpace of a byte read as a rune, as Parse
// chomps a key and a value byte by byte: \x85 and \xa0 are white space
// too.
func podmanSpace(c byte) bool {
	return unicode.IsSpace(rune(c))
}

// quadletGroup is lineIsGroup: the name between [ and the first ], when
// only spaces and tabs follow it.
func quadletGroup(line []byte) ([]byte, bool) {
	if len(line) == 0 || line[0] != '[' {
		return nil, false
	}
	end := bytes.IndexByte(line, ']')
	if end < 0 || len(bytes.Trim(line[end+1:], " \t")) > 0 {
		return nil, false
	}
	return line[1:end], true
}

// group starts the group name, or refuses it as groupNameIsValid does.
func (r *quadletReader) group(name []byte, n int) error {
	if len(name) == 0 || bytes.ContainsFunc(name, func(c rune) bool { return c == '[' || unicode.IsControl(c) }) {
		return core.LineError(n, "the group name %s is empty or holds [ or a control character, which Quadlet refuses", core.Quote(string(name)))
	}
	r.section = append(r.section[:0], name...)
	r.seq++
	return nil
}

// podmanWords calls f with each word of an Environment= value of a Quadlet
// group, as Podman 5.4.0's LookupAllKeyVal splits it (splitString with
// SplitRelax, SplitUnquote and SplitCUnescape, in split.go), which differs
// from systemd's: the value ends at a NUL; a quote never closed and a \ at
// the end are read; an escape writes a character, so \xff is U+00FF in
// UTF-8 and a surrogate is U+FFFD; an empty word ends the value, the
// words after it dropped; and an escape it does not read drops the whole
// value, so f sees no word of it.
func podmanWords(v []byte, m *core.Meter, f func(word []byte) error) error {
	if err := m.Charge(len(v)); err != nil {
		return err
	}
	v, _, _ = bytes.Cut(v, []byte{0})
	var words [][]byte
	w := podWordReader{v: v}
	for {
		word, ok := w.next()
		if w.bad {
			return nil
		}
		if !ok {
			break
		}
		words = append(words, word)
	}
	for _, word := range words {
		if err := f(word); err != nil {
			return err
		}
	}
	return nil
}

// podWordReader reads the words of a value as extractFirstWord does.
type podWordReader struct {
	v     []byte
	i     int
	quote byte
	bad   bool // an escape it does not read
}

// next is the next word, false at the end or at an empty word, which ends
// the value too.
func (w *podWordReader) next() ([]byte, bool) {
	for w.i < len(w.v) && systemdSpace(w.v[w.i]) {
		w.i++
	}
	var word []byte
	w.quote = 0
	for w.i < len(w.v) {
		var end bool
		if word, end = w.step(word); end || w.bad {
			break
		}
	}
	return word, len(word) > 0
}

// step reads the byte at i into word and moves past it; end reports a
// separator outside quotes.
func (w *podWordReader) step(word []byte) ([]byte, bool) {
	c := w.v[w.i]
	w.i++
	switch {
	case c == '\\':
		return w.escape(word), false
	case w.quote != 0 && c == w.quote:
		w.quote = 0
	case w.quote != 0:
		word = append(word, c)
	case c == '\'' || c == '"':
		w.quote = c
	case systemdSpace(c):
		return word, true
	default:
		word = append(word, c)
	}
	return word, false
}

// escape reads the escape after a \ at i, which a \ at the end of the
// value drops.
func (w *podWordReader) escape(word []byte) []byte {
	if w.i == len(w.v) {
		return word
	}
	r, n := podUnescape(w.v[w.i:])
	if n == 0 {
		w.bad = true
		return word
	}
	w.i += n
	return utf8.AppendRune(word, r)
}

// podUnescape is cUnescapeOne without NUL: the character the escape at the
// start of p stands for, and how many bytes it read, 0 for none it reads.
// Every escape is a character: \xHH and \ooo too.
func podUnescape(p []byte) (rune, int) {
	if c, ok := simpleEscapes[p[0]]; ok {
		return rune(c), 1
	}
	e, ok := numericEscapes[p[0]]
	if '0' <= p[0] && p[0] <= '7' {
		e, ok = octalEscape, true
	}
	if !ok {
		return 0, 0
	}
	n, ok := digits(p[e.skip:], e.digits, e.base)
	if !ok || n == 0 || n > e.max {
		return 0, 0
	}
	return rune(n), e.skip + e.digits //nolint:gosec // G115: n is at most U+10FFFF
}
