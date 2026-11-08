package ini

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// errEnvironSyntax is a word of Environment= that extract_first_word
// refuses: a quote never closed, a \ at the end, or an escape it does not
// read. systemd ignores the rest of the line from that word on.
var errEnvironSyntax = errors.New("invalid syntax")

// environWords calls f with each word of an Environment= value, as systemd
// 257's config_parse_environ splits it with extract_first_word and the
// flags EXTRACT_CUNESCAPE and EXTRACT_UNQUOTE: words are separated by
// spaces, tabs, \n and \r; a ' or a " anywhere in a word quotes up to the
// next of the same; a \ inside quotes or out reads one C escape (\a \b \f
// \n \r \t \v \\ \" \' \s, \xHH, \ooo, \uHHHH, \UHHHHHHHH), and any other
// is refused, as are an escape of NUL and a quote never closed. The words
// before a refused one are still visited, since systemd adds each as it
// reads it; the error is errEnvironSyntax. buf is reused for each word.
func environWords(v []byte, buf []byte, m *core.Meter, f func(word []byte) error) error {
	if err := m.Charge(len(v)); err != nil {
		return err
	}
	w := wordReader{v: v, word: buf}
	for {
		for w.i < len(v) && systemdSpace(v[w.i]) {
			w.i++
		}
		if w.i == len(v) {
			return nil
		}
		if err := w.read(); err != nil {
			return err
		}
		if err := f(w.word); err != nil {
			return err
		}
	}
}

// wordReader reads the words of an Environment= value.
type wordReader struct {
	v     []byte
	i     int
	word  []byte
	quote byte // the quote open, 0 for none
}

// read reads the word at i into word, up to a separator outside quotes or
// the end, or refuses it.
func (w *wordReader) read() error {
	w.word, w.quote = w.word[:0], 0
	for w.i < len(w.v) {
		end, err := w.step(w.v[w.i])
		if end || err != nil {
			return err
		}
	}
	if w.quote != 0 {
		return errEnvironSyntax
	}
	return nil
}

// step reads the byte c at i and moves past it, or past the escape it
// starts; end reports a separator that ends the word.
func (w *wordReader) step(c byte) (end bool, err error) {
	switch {
	case c == '\\':
		return false, w.escape()
	case w.quote != 0:
		w.quoted(c)
	case c == '\'' || c == '"':
		w.quote = c
	case systemdSpace(c):
		return true, nil
	default:
		w.word = append(w.word, c)
	}
	w.i++
	return false, nil
}

// quoted reads c inside quotes: the quote that opened them closes them.
func (w *wordReader) quoted(c byte) {
	if c == w.quote {
		w.quote = 0
		return
	}
	w.word = append(w.word, c)
}

// escape reads the escape the \ at i starts.
func (w *wordReader) escape() error {
	var n int
	w.word, n = cunescape(w.word, w.v[w.i+1:])
	if n == 0 {
		return errEnvironSyntax
	}
	w.i += 1 + n
	return nil
}

// simpleEscapes are the escapes of one character after the \.
var simpleEscapes = map[byte]byte{
	'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v',
	'\\': '\\', '"': '"', '\'': '\'', 's': ' ',
}

// numericEscape is an escape that writes a number: its digits after the
// letter, or the digits alone for octal, their base, and whether the
// number is a byte or a code point systemd writes in UTF-8.
type numericEscape struct {
	skip, digits int
	base         uint32
	codePoint    bool
	max          uint32
}

var numericEscapes = map[byte]numericEscape{
	'x': {1, 2, 16, false, 0xff},
	'u': {1, 4, 16, true, 0xffff},
	'U': {1, 8, 16, true, 0x10ffff},
}

// octalEscape is \ooo: three octal digits, a byte up to 255.
var octalEscape = numericEscape{0, 3, 8, false, 0xff}

// cunescape appends the character the C escape at the start of p, the text
// after a \, stands for, as cunescape_one reads it without NUL, and how
// many bytes of p it read; 0 for none it reads.
func cunescape(out, p []byte) ([]byte, int) {
	if len(p) == 0 {
		return out, 0
	}
	if c, ok := simpleEscapes[p[0]]; ok {
		return append(out, c), 1
	}
	e, ok := numericEscapes[p[0]]
	if '0' <= p[0] && p[0] <= '7' {
		e, ok = octalEscape, true
	}
	if !ok {
		return out, 0
	}
	return e.read(out, p)
}

// read is the escape's number at the start of p, refused when it is 0,
// past max, or for \U a code point systemd does not take.
func (e numericEscape) read(out, p []byte) ([]byte, int) {
	n, ok := digits(p[e.skip:], e.digits, e.base)
	if !ok || n == 0 || n > e.max || e.max == 0x10ffff && !unicharValid(n) {
		return out, 0
	}
	if e.codePoint {
		return appendUnichar(out, n), e.skip + e.digits
	}
	return append(out, byte(n)), e.skip + e.digits //nolint:gosec // G115: n is at most max, 0xff for a byte
}

// digits reads n digits of base from the start of p.
func digits(p []byte, n int, base uint32) (uint32, bool) {
	if len(p) < n {
		return 0, false
	}
	var v uint32
	for _, c := range p[:n] {
		d := hexValue(c)
		if d >= base {
			return 0, false
		}
		v = v*base + d
	}
	return v, true
}

// hexValue is the value of a hexadecimal digit, 16 for any other byte.
func hexValue(c byte) uint32 {
	switch {
	case '0' <= c && c <= '9':
		return uint32(c - '0')
	case 'a' <= c && c <= 'f':
		return uint32(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return uint32(c-'A') + 10
	}
	return 16
}

// unicharValid is systemd's unichar_is_valid: below U+110000, no
// surrogate, none of U+FDD0 to U+FDEF, and no U+FFFE or U+FFFF in any
// plane.
func unicharValid(c uint32) bool {
	return c < 0x110000 && (c < 0xd800 || c > 0xdfff) && (c < 0xfdd0 || c > 0xfdef) && c&0xfffe != 0xfffe
}

// systemdUTF8 is utf8_is_valid: UTF-8, each character one unicharValid
// takes, so systemd refuses a noncharacter that Go's utf8.Valid reads.
func systemdUTF8(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if !unicharValid(uint32(r)) { //nolint:gosec // G115: a rune of valid UTF-8 is not negative
			return false
		}
	}
	return true
}

// appendUnichar is utf8_encode_unichar, which writes a surrogate as its
// three bytes where Go would write U+FFFD; the value check then refuses
// the word, as systemd's does.
func appendUnichar(out []byte, c uint32) []byte {
	switch {
	case c < 0x80:
		return append(out, byte(c))
	case c < 0x800:
		return append(out, 0xc0|byte(c>>6), 0x80|byte(c&0x3f))
	case c < 0x10000:
		return append(out, 0xe0|byte(c>>12), 0x80|byte(c>>6&0x3f), 0x80|byte(c&0x3f))
	}
	return append(out, 0xf0|byte(c>>18), 0x80|byte(c>>12&0x3f), 0x80|byte(c>>6&0x3f), 0x80|byte(c&0x3f)) //nolint:gosec // G115: c is below U+110000, so c>>18 is below 8
}

// envName is env_name_is_valid: letters, digits and _, not starting with
// a digit.
func envName(name []byte) bool {
	if len(name) == 0 || isDigit(name[0]) {
		return false
	}
	for _, c := range name {
		if !isAlpha(c) && !isDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

// envAssignment splits a word of Environment= into the name and the value
// systemd sets (env_assignment_is_valid): a name envName takes, = and a
// value of UTF-8. ok is false for a word systemd ignores.
func envAssignment(word []byte) (name, value []byte, ok bool) {
	name, value, ok = bytes.Cut(word, []byte("="))
	return name, value, ok && envName(name) && systemdUTF8(value)
}

// readEnvironmentFile reads a file as systemd 257 reads one that
// EnvironmentFile= names (parse_env_file_internal and load_env_file_push),
// and visits every assignment it sets, at the line where its key starts:
//
//   - the file ends at its first NUL; a line ends at \n or \r;
//   - a line whose first character other than white space is # or ; is a
//     comment;
//   - KEY=value, the key without the white space around it, the value
//     without the white space before it and, unquoted, after it; a key
//     with no = is ignored;
//   - unquoted, \ keeps the character after it, and a \ before a line end
//     joins the next line; in '...' nothing is an escape; in "..." a \
//     keeps ", \, ` and $, joins a line, and stays before any other
//     character; what follows a closing quote joins the value;
//   - systemd substitutes nothing, and ignores an assignment whose name is
//     not letters, digits and _, or starts with a digit, such as x.y=3 or
//     export A=1;
//   - a key or a value that is not UTF-8 is refused, as systemd refuses
//     the file.
func readEnvironmentFile(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	data, _, _ = bytes.Cut(data, []byte{0})
	p := envParser{m: m, visit: visit, lines: core.NewLineCursor(data)}
	for i, c := range data {
		if err := envStates[p.state](&p, i, c); err != nil {
			return err
		}
	}
	if err := p.finish(len(data)); err != nil {
		return err
	}
	return m.Charge(len(data) - p.charged)
}

// The states of parse_env_file_internal.
const (
	preKey = iota
	inKey
	preValue
	inValue
	valueEscape
	singleQuoted
	doubleQuoted
	doubleEscape
	inComment
	commentEscape
)

// envParser is readEnvironmentFile's state between bytes.
type envParser struct {
	m          *core.Meter
	visit      func(*core.Entry) error
	lines      *core.LineCursor
	state      int
	key, value []byte
	keyAt      int // where the key starts
	charged    int // the bytes charged so far
	// kept is where the white space at the end of the value starts, which
	// systemd drops: past the last byte that is no white space, or that a
	// quote or a \ keeps.
	kept int
	e    core.Entry
}

// envStates are the steps of each state, by its number.
var envStates = [...]func(p *envParser, i int, c byte) error{
	preKey:        (*envParser).preKey,
	inKey:         (*envParser).inKey,
	preValue:      (*envParser).preValue,
	inValue:       (*envParser).inValue,
	valueEscape:   (*envParser).valueEscape,
	singleQuoted:  (*envParser).singleQuoted,
	doubleQuoted:  (*envParser).doubleQuoted,
	doubleEscape:  (*envParser).doubleEscape,
	inComment:     (*envParser).comment,
	commentEscape: (*envParser).commentEscape,
}

// envNewline is NEWLINE, the bytes that end a line of an environment file.
func envNewline(c byte) bool { return c == '\n' || c == '\r' }

func (p *envParser) preKey(i int, c byte) error {
	switch {
	case c == '#' || c == ';':
		p.state = inComment
	case !systemdSpace(c):
		p.state, p.keyAt = inKey, i
		p.key = append(p.key[:0], c)
	}
	return nil
}

func (p *envParser) inKey(_ int, c byte) error {
	switch {
	case envNewline(c):
		p.state = preKey
	case c == '=':
		p.state, p.value, p.kept = preValue, p.value[:0], 0
	default:
		p.key = append(p.key, c)
	}
	return nil
}

// keep appends c to the value, and keeps it from the white space dropped
// at the value's end.
func (p *envParser) keep(c byte) {
	p.value = append(p.value, c)
	p.kept = len(p.value)
}

func (p *envParser) preValue(i int, c byte) error {
	switch {
	case envNewline(c):
		p.state = preKey
		return p.push(i)
	case c == '\'':
		p.state = singleQuoted
	case c == '"':
		p.state = doubleQuoted
	case c == '\\':
		p.state = valueEscape
	case !systemdSpace(c):
		p.state = inValue
		p.keep(c)
	}
	return nil
}

func (p *envParser) inValue(i int, c byte) error {
	switch {
	case envNewline(c):
		p.state = preKey
		return p.push(i)
	case c == '\\':
		p.state, p.kept = valueEscape, len(p.value)
	case systemdSpace(c):
		p.value = append(p.value, c)
	default:
		p.keep(c)
	}
	return nil
}

func (p *envParser) valueEscape(_ int, c byte) error {
	p.state = inValue
	if !envNewline(c) {
		p.keep(c)
	}
	return nil
}

func (p *envParser) singleQuoted(_ int, c byte) error {
	if c == '\'' {
		p.state = preValue
		return nil
	}
	p.keep(c)
	return nil
}

func (p *envParser) doubleQuoted(_ int, c byte) error {
	switch c {
	case '"':
		p.state = preValue
	case '\\':
		p.state = doubleEscape
	default:
		p.keep(c)
	}
	return nil
}

// doubleEscape keeps the character after a \ in double quotes for ", \,
// ` and $, joins a line, and keeps the \ before any other.
func (p *envParser) doubleEscape(_ int, c byte) error {
	p.state = doubleQuoted
	switch c {
	case '"', '\\', '`', '$':
		p.keep(c)
	case '\n':
	default:
		p.value = append(p.value, '\\')
		p.keep(c)
	}
	return nil
}

func (p *envParser) comment(_ int, c byte) error {
	switch {
	case c == '\\':
		p.state = commentEscape
	case envNewline(c):
		p.state = preKey
	}
	return nil
}

// commentEscape ends the comment at a line end after a \, as systemd 254
// and later do.
func (p *envParser) commentEscape(_ int, c byte) error {
	p.state = inComment
	if envNewline(c) {
		p.state = preKey
	}
	return nil
}

// finish pushes the assignment the file ends inside, if any.
func (p *envParser) finish(at int) error {
	switch p.state {
	case preValue, inValue, valueEscape, singleQuoted, doubleQuoted, doubleEscape:
		return p.push(at)
	}
	return nil
}

// push visits the assignment read, once its key and the end of its value
// are stripped, unless systemd ignores its name; a key or a value that is
// not UTF-8 is refused. Only white space an unquoted value ends in is
// stripped (kept), as systemd strips it in that state alone.
func (p *envParser) push(at int) error {
	if err := p.m.Charge(core.LineWork + at - p.charged); err != nil {
		return err
	}
	p.charged = at
	k := bytes.TrimRight(p.key, " \t\n\r")
	p.value = p.value[:p.kept]
	line := p.lines.At(p.keyAt)
	if !systemdUTF8(k) {
		return core.LineError(line, "the key %s is not UTF-8, which systemd refuses", core.Quote(string(k)))
	}
	if !systemdUTF8(p.value) {
		return core.LineError(line, "the value of %s is not UTF-8, which systemd refuses", core.Quote(string(k)))
	}
	if !envName(k) {
		return nil
	}
	p.e = core.Entry{Key: k, Value: p.value, Line: line}
	return p.visit(&p.e)
}
