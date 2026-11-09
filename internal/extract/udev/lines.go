package udev

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// lineSize is UDEV_LINE_SIZE, the most bytes udev reads as one line: a rule
// joined over lines to this length or more is dropped, and a line this long
// stops udev's read of the file, which errata refuses.
const lineSize = 16384

// tokenWork is the fixed work of one key and value, besides its bytes.
const tokenWork = 32

// whitespace is systemd's WHITESPACE, which udev skips before a line, a key,
// an operator and a value.
const whitespace = " \t\n\r"

// logical is one rule as udev reads it: its lines joined, with a \ at the
// end of each but the last taken off, and where each line's text starts
// in it.
type logical struct {
	text  []byte
	parts []part
}

// part is where the text of one line starts in a logical line, and the
// line's number.
type part struct{ off, line int }

// lineOf is the number of the line that holds byte off of the logical
// line.
func (l *logical) lineOf(off int) int {
	n := l.parts[0].line
	for _, p := range l.parts {
		if p.off > off {
			break
		}
		n = p.line
	}
	return n
}

// joiner joins the lines of a rules file into rules, as
// udev_rules_parse_file does: white space before a line is skipped, a line
// that then starts with # is passed over, even between the lines of a
// rule, and a \ at the end of a line joins the next to it.
type joiner struct {
	cur    logical
	inRule bool // a line before ended in \
	ignore bool // the joined rule passed lineSize, so udev drops it
}

// eachRule calls f with each rule of data that udev reads. Lines end
// where core says they do. A line of lineSize bytes or more, and a NUL
// byte, which udev reads as a line end and errata does not, are refused.
func eachRule(data []byte, m *core.Meter, f func(*logical) error) error {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return core.LineError(core.LineOf(data, i), "holds a NUL byte, which udev reads as a line end; errata does not read such a file")
	}
	var j joiner
	for off, n := 0, 1; off < len(data); n++ {
		next := core.NextLine(data, off)
		if err := m.Charge(core.LineWork + next - off); err != nil {
			return err
		}
		line := bytes.TrimRight(data[off:next], "\r\n")
		if len(line) >= lineSize {
			return core.LineError(n, "is %d bytes long; udev stops reading a file at a line of %d bytes, so errata does not read it", len(line), lineSize)
		}
		if l := j.add(line, n); l != nil {
			if err := f(l); err != nil {
				return err
			}
		}
		off = next
	}
	return nil
}

// add takes line n and returns the rule it ends, nil when it ends none.
func (j *joiner) add(raw []byte, n int) *logical {
	line := bytes.TrimLeft(raw, whitespace)
	if len(line) > 0 && line[0] == '#' {
		return nil
	}
	line = j.extend(line, n)
	if len(line) > 0 && line[len(line)-1] == '\\' {
		j.open(line, n)
		return nil
	}
	return j.end(line, n)
}

// extend joins line n to the rule a line before it opened, and returns
// the rule joined so far; once the rule reaches lineSize, udev joins no
// more of it and goes on with line n alone.
func (j *joiner) extend(line []byte, n int) []byte {
	if !j.inRule || j.ignore {
		return line
	}
	j.ignore = len(j.cur.text)+len(line) >= lineSize
	j.cur.parts = append(j.cur.parts, part{len(j.cur.text), n})
	j.cur.text = append(j.cur.text, line...)
	if j.ignore {
		return line
	}
	return j.cur.text
}

// end is the rule line n ends, nil for one udev drops for its length and
// for an empty one, and starts the next.
func (j *joiner) end(line []byte, n int) *logical {
	l := j.close(line, n)
	drop := j.ignore || len(l.text) == 0
	j.cur, j.inRule, j.ignore = logical{}, false, false
	if drop {
		return nil
	}
	return &l
}

// open takes line n, which ends in \, as the start or the next line of a
// rule, the \ taken off. An empty line n after a line that ends in \\
// takes the \ that line kept, as udev does, and empty lines after a line
// that ends in more take one each, so the last part's start is held to the
// rule's end, and close holds each part to those after it.
func (j *joiner) open(line []byte, n int) {
	if j.ignore {
		return
	}
	if !j.inRule {
		j.cur = logical{text: append([]byte(nil), line[:len(line)-1]...), parts: []part{{0, n}}}
		j.inRule = true
		return
	}
	j.cur.text = j.cur.text[:len(j.cur.text)-1]
	last := &j.cur.parts[len(j.cur.parts)-1]
	last.off = min(last.off, len(j.cur.text))
}

// close is the rule that line n ends: the rule joined so far, or line n
// alone. A part starts no later than any part after it, since the \ an
// empty line takes may be one an earlier line wrote: one pass from the
// end, where a pass at each \ taken would be quadratic.
func (j *joiner) close(line []byte, n int) logical {
	if j.inRule {
		for i := len(j.cur.parts) - 2; i >= 0; i-- {
			j.cur.parts[i].off = min(j.cur.parts[i].off, j.cur.parts[i+1].off)
		}
		return j.cur
	}
	return logical{text: line, parts: []part{{0, n}}}
}

// op is an operator of a key, as parse_operator reads it.
type op uint8

const (
	opMatch   op = iota + 1 // ==
	opNoMatch               // !=
	opAdd                   // +=
	opRemove                // -=
	opAssign                // =
	opFinal                 // :=
)

var opText = map[op]string{opMatch: "==", opNoMatch: "!=", opAdd: "+=", opRemove: "-=", opAssign: "=", opFinal: ":="}

func (o op) String() string { return opText[o] }

// isMatch reports an operator of a match key.
func (o op) isMatch() bool { return o == opMatch || o == opNoMatch }

// parseOp is the operator s starts with, and its length; 0 for none.
func parseOp(s []byte) (op, int) {
	for _, o := range []op{opMatch, opNoMatch, opAdd, opRemove, opAssign, opFinal} {
		if bytes.HasPrefix(s, []byte(o.String())) {
			return o, len(o.String())
		}
	}
	return 0, 0
}

// token is one key and value of a rule, as parse_line reads it.
type token struct {
	key, attr string
	hasAttr   bool // the key is written with braces, empty or not
	op        op
	value     string
	fold      bool   // the value is written i"...", matched in any case
	written   []byte // the value as written, its prefix and quotes kept
	off       int    // where the key starts in the logical line
}

// errDrop is a rule udev drops, as it drops an invalid key and value, an
// unknown key and a key it refuses with that operator or attribute.
var errDrop = errors.New("udev drops the rule")

// eachToken calls f with each key and value of the rule l, in order. It
// returns errDrop where parse_line refuses the rest of the line, and udev
// drops the rule.
func eachToken(l *logical, m *core.Meter, f func(*token) error) error {
	for i := 0; ; {
		t, next, err := nextToken(l.text, i)
		if err != nil || t == nil {
			return err
		}
		if err := m.Charge(tokenWork + next - i); err != nil {
			return err
		}
		if err := f(t); err != nil {
			return err
		}
		i = next
	}
}

// nextToken is the key and value of s at i, and where the next starts; nil
// at the end of s.
func nextToken(s []byte, i int) (*token, int, error) {
	i = skip(s, i, whitespace+",")
	if i == len(s) {
		return nil, i, nil
	}
	t := &token{off: i}
	end, err := keyEnd(s, i)
	if err != nil {
		return nil, 0, err
	}
	t.key = string(s[i:end])
	at, err := t.readAttr(s, end)
	if err != nil {
		return nil, 0, err
	}
	at = skip(s, at, whitespace)
	o, n := parseOp(s[at:])
	if o == 0 {
		return nil, 0, errDrop
	}
	t.op = o
	next, err := t.readValue(s, skip(s, at+n, whitespace))
	return t, next, err
}

// keyEnd is where the key that starts at i ends: at white space, = or {,
// or at +, -, ! or : before =.
func keyEnd(s []byte, i int) (int, error) {
	for j := i; j < len(s); j++ {
		c := s[j]
		if bytes.IndexByte([]byte(whitespace+"={"), c) >= 0 {
			return j, nil
		}
		if bytes.IndexByte([]byte("+-!:"), c) >= 0 && j+1 < len(s) && s[j+1] == '=' {
			return j, nil
		}
	}
	return 0, errDrop
}

// readAttr reads the attribute in braces at end, where the key ends, and
// returns where the operator is looked for.
func (t *token) readAttr(s []byte, end int) (int, error) {
	if s[end] != '{' {
		return end, nil
	}
	close := bytes.IndexByte(s[end+1:], '}')
	if close < 0 {
		return 0, errDrop
	}
	t.attr, t.hasAttr = string(s[end+1:end+1+close]), true
	return end + 2 + close, nil
}

// prefixes reads the letters before a value's quote, e and i each at most
// once, in either order, as udev_rule_parse_value reads them, and returns
// whether e is one and where the quote is.
func (t *token) prefixes(s []byte, v int) (escaped bool, at int, err error) {
	for ; v < len(s) && s[v] != '"'; v++ {
		switch {
		case s[v] == 'e' && !escaped:
			escaped = true
		case s[v] == 'i' && !t.fold:
			t.fold = true
		default:
			return false, 0, errDrop
		}
	}
	if v == len(s) {
		return false, 0, errDrop
	}
	return escaped, v, nil
}

// readValue reads the value at v: in double quotes, after an e for C
// escapes, an i for a match in any case, or both, and returns where the
// line goes on.
func (t *token) readValue(s []byte, v int) (int, error) {
	start := v
	escaped, v, err := t.prefixes(s, v)
	if err != nil {
		return 0, err
	}
	var end int
	if escaped {
		end, err = t.escapedValue(s, v+1)
	} else {
		end, err = t.plainValue(s, v+1)
	}
	if err != nil {
		return 0, err
	}
	t.written = s[start : end+1]
	return end + 1, nil
}

// plainValue reads the value from i to its closing quote, a \" read as ",
// and returns where the quote is.
func (t *token) plainValue(s []byte, i int) (int, error) {
	var b []byte
	for ; i < len(s); i++ {
		if s[i] == '"' {
			t.value = string(b)
			return i, nil
		}
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '"' {
			i++
		}
		b = append(b, s[i])
	}
	return 0, errDrop
}

// escapedValue reads the value from i to its closing quote, read with C
// escapes as cunescape reads them, and returns where the quote is.
func (t *token) escapedValue(s []byte, i int) (int, error) {
	end := i
	for ; end < len(s) && s[end] != '"'; end++ {
		if s[end] == '\\' {
			end++
		}
	}
	if end >= len(s) {
		return 0, errDrop
	}
	v, err := unescape(s[i:end])
	t.value = v
	return end, err
}

// skip is the first index from i of s whose byte is not in set.
func skip(s []byte, i int, set string) int {
	for i < len(s) && bytes.IndexByte([]byte(set), s[i]) >= 0 {
		i++
	}
	return i
}

// simpleEscapes are the one letter escapes cunescape reads.
var simpleEscapes = map[byte]byte{'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v', '\\': '\\', '"': '"', '\'': '\'', 's': ' '}

// unescape is b with its C escapes read as systemd's cunescape reads them
// without flags: a NUL, an unknown escape and a code point no character
// has are refused, and udev drops the rule.
func unescape(b []byte) (string, error) {
	var out []byte
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			out = append(out, b[i])
			continue
		}
		n, add, err := unescapeOne(b[i+1:])
		if err != nil {
			return "", err
		}
		out = append(out, add...)
		i += n
	}
	return string(out), nil
}

// unescapeOne reads the escape b starts with, after its \: how many bytes
// it takes and what it stands for.
func unescapeOne(b []byte) (int, []byte, error) {
	if len(b) == 0 {
		return 0, nil, errDrop
	}
	if c, ok := simpleEscapes[b[0]]; ok {
		return 1, []byte{c}, nil
	}
	switch {
	case b[0] == 'x':
		return number(b[1:], 2, 16, asByte)
	case b[0] == 'u':
		return number(b[1:], 4, 16, asCode)
	case b[0] == 'U':
		return number(b[1:], 8, 16, asChar)
	case b[0] >= '0' && b[0] <= '3': // \400 and up are past a byte, which cunescape refuses
		n, add, err := number(b, 3, 8, asByte)
		return n - 1, add, err
	}
	return 0, nil, errDrop
}

// numberKind is what an escape's number stands for.
type numberKind uint8

const (
	asByte numberKind = iota // \x and octal: the byte itself
	asCode                   // \u: a code point, in UTF-8, whatever it is
	asChar                   // \U: a code point unichar_is_valid takes, in UTF-8
)

// number reads digits digits of base at the start of b, as kind says. It
// returns the bytes taken with the escape's letter and the bytes they
// stand for; NUL is refused for each.
func number(b []byte, digits, base int, kind numberKind) (int, []byte, error) {
	if len(b) < digits {
		return 0, nil, errDrop
	}
	v, err := strconv.ParseUint(string(b[:digits]), base, 32)
	switch {
	case err != nil || v == 0:
		return 0, nil, errDrop
	case kind == asByte:
		return digits + 1, []byte{byte(v & 0xff)}, nil
	case kind == asCode:
		return digits + 1, encode(uint16(v & 0xffff)), nil
	case v < 0x110000 && validChar(rune(v&0x1fffff)):
		return digits + 1, utf8.AppendRune(nil, rune(v&0x1fffff)), nil
	}
	return 0, nil, errDrop
}

// encode is c in UTF-8 as systemd's utf8_encode_unichar writes it: a
// surrogate or a noncharacter too, which Go's encoder would not write.
func encode(c uint16) []byte {
	switch {
	case c < 0x80:
		return []byte{byte(c)}
	case c < 0x800:
		return []byte{0xc0 | byte(c>>6), 0x80 | byte(c&0x3f)}
	}
	return []byte{0xe0 | byte(c>>12), 0x80 | byte(c>>6&0x3f), 0x80 | byte(c&0x3f)}
}

// validChar is systemd's unichar_is_valid for a code point below
// 0x110000: no surrogate and no noncharacter.
func validChar(c rune) bool {
	return c&^0x7ff != 0xd800 && (c < 0xfdd0 || c > 0xfdef) && c&0xfffe != 0xfffe
}

// String is t as a rule writes it, its value as the file writes it.
func (t *token) String() string {
	k := t.key
	if t.hasAttr {
		k += "{" + t.attr + "}"
	}
	return fmt.Sprintf("%s%s%s", k, t.op, t.written)
}
