package jsonc

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// ParseJSONC reads one JSONC value into a tree: JSON with // and /* */
// comments and a comma after the last member of an object or the last item
// of an array, as TypeScript reads tsconfig.json and VS Code its settings.
// Every other byte is read as JSON: strings in double quotes with JSON's
// escapes, and JSON's numbers, each keeping the text the file wrote.
func ParseJSONC(data []byte) (*core.Node, error) { return parse(data, false) }

// ParseJSON5 reads one JSON5 value into a tree, as the JSON5 specification
// 1.0.0 defines it: JSONC, and keys written as ECMAScript identifiers,
// strings in single quotes with the escapes of ECMAScript 5.1 and a
// backslash that continues a string on the next line, numbers with a sign,
// a leading or trailing point, in hexadecimal, Infinity and NaN, and the
// white space of Unicode's Zs category. A number keeps the text the file
// wrote, so 0x1F stays 0x1F.
func ParseJSON5(data []byte) (*core.Node, error) { return parse(data, true) }

// parse reads data in JSONC, or in JSON5 when json5 is set. An object that
// repeats a key is an error in both, as it is for the json reader, since
// readers disagree on which of the two counts; in JSON5 a key is the same
// key however it is quoted or escaped.
func parse(data []byte, json5 bool) (*core.Node, error) {
	r := &reader{data: data, json5: json5, lines: core.NewLineCursor(data)}
	if !json5 && bytes.HasPrefix(data, []byte(core.BOM)) {
		// JSON5 reads U+FEFF as white space anywhere; JSONC only a mark at
		// the start, as TypeScript drops it when it reads the file.
		r.i = len(core.BOM)
	}
	if err := r.space(); err != nil {
		return nil, err
	}
	if r.i == len(data) {
		return nil, r.fail("the file holds no value")
	}
	root, err := r.value(0)
	if err != nil {
		return nil, err
	}
	if err := r.space(); err != nil {
		return nil, err
	}
	if r.i < len(data) {
		return nil, r.fail("data after the top-level value")
	}
	return root, nil
}

type reader struct {
	data  []byte
	i     int
	json5 bool
	lines *core.LineCursor
	// quote is the offset of the quote that opens the string being read,
	// where a string the file does not close is refused.
	quote int
}

// unclosed is the refusal of a string the file ends inside, at the line
// its quote opens on, as an unclosed /* comment is refused at its own.
func (r *reader) unclosed() error {
	r.i = r.quote
	return r.fail("the string that opens here is not closed")
}

// fail is an error at the reader's offset, naming its line.
func (r *reader) fail(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", r.lines.At(r.i), fmt.Sprintf(format, args...))
}

// found names the character at the reader's offset in a message.
func (r *reader) found() string {
	if r.i >= len(r.data) {
		return "the end of the file"
	}
	c, _ := utf8.DecodeRune(r.data[r.i:])
	return fmt.Sprintf("%q", c)
}

// space skips white space and comments. A /* comment not closed is an
// error at the line it opens on.
func (r *reader) space() error {
	for r.i < len(r.data) {
		c := r.data[r.i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			r.i++
		case c == '/' && r.i+1 < len(r.data) && r.data[r.i+1] == '/':
			r.i += 2
			for r.i < len(r.data) && !r.lineEnd() {
				r.i++
			}
		case c == '/' && r.i+1 < len(r.data) && r.data[r.i+1] == '*':
			end := bytes.Index(r.data[r.i+2:], []byte("*/"))
			if end < 0 {
				return r.fail("the comment that opens here is not closed")
			}
			r.i += 2 + end + 2
		case r.json5 && (c == '\v' || c == '\f'):
			r.i++
		case r.json5 && c >= utf8.RuneSelf:
			ru, size := utf8.DecodeRune(r.data[r.i:])
			if ru != '\ufeff' && ru != '\u2028' && ru != '\u2029' && !unicode.Is(unicode.Zs, ru) {
				return nil
			}
			r.i += size
		default:
			return nil
		}
	}
	return nil
}

// lineEnd reports whether a line terminator starts at the reader's offset:
// \n or \r, and in JSON5 LS and PS too.
func (r *reader) lineEnd() bool {
	c := r.data[r.i]
	if c == '\n' || c == '\r' {
		return true
	}
	return r.json5 && c == 0xe2 && r.i+2 < len(r.data) && r.data[r.i+1] == 0x80 && (r.data[r.i+2] == 0xa8 || r.data[r.i+2] == 0xa9)
}

func (r *reader) value(depth int) (*core.Node, error) {
	line := r.lines.At(r.i)
	if depth > core.MaxDepth {
		return nil, core.TooDeep(line)
	}
	if r.i >= len(r.data) {
		return nil, r.fail("the file ends before the value is complete")
	}
	switch c := r.data[r.i]; {
	case c == '{':
		return r.object(line, depth)
	case c == '[':
		return r.array(line, depth)
	case c == '"' || (r.json5 && c == '\''):
		s, err := r.str()
		if err != nil {
			return nil, err
		}
		return &core.Node{Text: s, Line: line}, nil
	case c == '-' || (c >= '0' && c <= '9') || (r.json5 && (c == '+' || c == '.' || c == 'I' || c == 'N')):
		text, err := r.number()
		if err != nil {
			return nil, err
		}
		return &core.Node{Type: core.TypeNumber, Text: text, Line: line}, nil
	}
	for _, w := range []struct {
		word string
		typ  core.ScalarType
	}{{"true", core.TypeBool}, {"false", core.TypeBool}, {"null", core.TypeNull}} {
		if r.word(w.word) {
			return &core.Node{Type: w.typ, Text: w.word, Line: line}, nil
		}
	}
	return nil, r.fail("a value cannot start with %s", r.found())
}

// word reads w at the reader's offset when the file holds it there, and no
// character of a name follows it.
func (r *reader) word(w string) bool {
	end := r.i + len(w)
	if end > len(r.data) || string(r.data[r.i:end]) != w {
		return false
	}
	if end < len(r.data) {
		if c, _ := utf8.DecodeRune(r.data[end:]); identPart(c) || c == '\\' {
			return false
		}
	}
	r.i = end
	return true
}

func (r *reader) object(line, depth int) (*core.Node, error) {
	n := core.NewMapping(line)
	r.i++ // the {
	for {
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.i < len(r.data) && r.data[r.i] == '}' {
			r.i++
			return n, nil
		}
		keyLine := r.lines.At(r.i)
		key, err := r.key()
		if err != nil {
			return nil, err
		}
		if _, dup := n.Field[key]; dup {
			return nil, fmt.Errorf("line %d: key %s is repeated", keyLine, core.Quote(key))
		}
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.i >= len(r.data) || r.data[r.i] != ':' {
			return nil, r.fail("a key is followed by :, not %s", r.found())
		}
		r.i++
		if err := r.space(); err != nil {
			return nil, err
		}
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		n.Set(key, v)
		if done, err := r.next('}'); done || err != nil {
			return n, err
		}
	}
}

func (r *reader) array(line, depth int) (*core.Node, error) {
	n := &core.Node{Shape: core.Sequence, Line: line}
	r.i++ // the [
	for {
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.i < len(r.data) && r.data[r.i] == ']' {
			r.i++
			return n, nil
		}
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, v)
		if done, err := r.next(']'); done || err != nil {
			return n, err
		}
	}
}

// next reads what follows a member or an item: close, which ends the
// collection, or a comma, after which close may follow too.
func (r *reader) next(close byte) (bool, error) {
	if err := r.space(); err != nil {
		return false, err
	}
	switch {
	case r.i >= len(r.data):
		return false, r.fail("the file ends before the value is complete")
	case r.data[r.i] == close:
		r.i++
		return true, nil
	case r.data[r.i] == ',':
		r.i++
		return false, nil
	}
	return false, r.fail("expected , or %c, found %s", close, r.found())
}

// key reads an object's key: a string, or in JSON5 an identifier name.
func (r *reader) key() (string, error) {
	if r.i >= len(r.data) {
		return "", r.fail("the file ends before the value is complete")
	}
	if c := r.data[r.i]; c == '"' || (r.json5 && c == '\'') {
		return r.str()
	}
	if r.json5 {
		if name, ok, err := r.ident(); ok || err != nil {
			return name, err
		}
	}
	return "", r.fail("a key cannot start with %s", r.found())
}

// ident reads an ECMAScript 5.1 IdentifierName: a letter, $ or _, then
// those, digits, combining marks, connector punctuation, ZWNJ and ZWJ, any
// of them written as a \u escape. ok is false when none starts here.
func (r *reader) ident() (string, bool, error) {
	var b strings.Builder
	for first := true; r.i < len(r.data); first = false {
		c, size := utf8.DecodeRune(r.data[r.i:])
		at := r.i
		if c == '\\' {
			if r.i+1 >= len(r.data) || r.data[r.i+1] != 'u' {
				return "", false, r.fail("a name holds \\ only before u and four hex digits")
			}
			r.i += 2
			u, ok := r.hex(4)
			if !ok {
				return "", false, r.fail("\\u takes four hex digits")
			}
			c = u
			if !identStart(c) && (first || !identPart(c)) {
				r.i = at
				return "", false, r.fail("the escape \\u%04X is no character of a name", u)
			}
		} else {
			if (first && !identStart(c)) || (!first && !identPart(c)) {
				if first {
					return "", false, nil
				}
				break
			}
			r.i += size
		}
		b.WriteRune(c)
	}
	return b.String(), true, nil
}

func identStart(c rune) bool {
	return c == '$' || c == '_' || unicode.In(c, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Lm, unicode.Lo, unicode.Nl)
}

func identPart(c rune) bool {
	return identStart(c) || c == '\u200c' || c == '\u200d' || unicode.In(c, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc)
}

// hex reads n hex digits at the reader's offset.
func (r *reader) hex(n int) (rune, bool) {
	if r.i+n > len(r.data) {
		return 0, false
	}
	var v rune
	for _, c := range r.data[r.i : r.i+n] {
		d, ok := hexDigit(c)
		if !ok {
			return 0, false
		}
		v = v<<4 | d
	}
	r.i += n
	return v, true
}

func hexDigit(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

// str reads a string and returns its decoded text. A byte that is not
// UTF-8, and a \u escape of half a surrogate pair, read as U+FFFD, as
// encoding/json reads them for the json reader.
func (r *reader) str() (string, error) {
	quote := r.data[r.i]
	r.quote = r.i
	r.i++
	var b strings.Builder
	for {
		if r.i >= len(r.data) {
			return "", r.unclosed()
		}
		c := r.data[r.i]
		switch {
		case c == quote:
			r.i++
			return b.String(), nil
		case c == '\\':
			if err := r.escape(&b); err != nil {
				return "", err
			}
		case c == '\n' || c == '\r':
			return "", r.fail("a string does not hold a line end; write \\n")
		case c < 0x20 && !r.json5:
			return "", r.fail("a string does not hold the control character %q; write it as an escape", c)
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			r.i++
		default:
			ru, size := utf8.DecodeRune(r.data[r.i:])
			b.WriteRune(ru)
			r.i += size
		}
	}
}

// escape reads the escape at the reader's offset into b.
func (r *reader) escape(b *strings.Builder) error {
	r.i++ // the backslash
	if r.i >= len(r.data) {
		return r.unclosed()
	}
	c := r.data[r.i]
	if single, ok := singleEscape(c, r.json5); ok {
		b.WriteByte(single)
		r.i++
		return nil
	}
	switch {
	case c == 'u':
		r.i++
		u, ok := r.hex(4)
		if !ok {
			return r.fail("\\u takes four hex digits")
		}
		ru := u
		if utf16.IsSurrogate(ru) {
			ru = r.lowHalf(ru)
		}
		b.WriteRune(ru)
		return nil
	case !r.json5:
		return r.fail("\\%c is no escape of JSON", c)
	case c == 'x':
		r.i++
		u, ok := r.hex(2)
		if !ok {
			return r.fail("\\x takes two hex digits")
		}
		b.WriteRune(u)
		return nil
	case c == '0':
		r.i++
		if r.i < len(r.data) && r.data[r.i] >= '0' && r.data[r.i] <= '9' {
			return r.fail("\\0 is not followed by a digit")
		}
		b.WriteByte(0)
		return nil
	case c >= '1' && c <= '9':
		return r.fail("\\%c is no escape of JSON5", c)
	case r.lineEnd():
		// A line continuation: the line end is no part of the string.
		if c == '\r' && r.i+1 < len(r.data) && r.data[r.i+1] == '\n' {
			r.i++
		}
		_, size := utf8.DecodeRune(r.data[r.i:])
		r.i += size
		return nil
	}
	// Any other character escaped is itself.
	ru, size := utf8.DecodeRune(r.data[r.i:])
	b.WriteRune(ru)
	r.i += size
	return nil
}

// singleEscape is the character a one character escape stands for: JSON's,
// and in JSON5 \' and \v too. JSON5 has no \/ of its own, but a character
// escaped is itself there, so / reads the same.
func singleEscape(c byte, json5 bool) (byte, bool) {
	switch c {
	case '"', '\\', '/':
		return c, true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case '\'':
		return c, json5
	case 'v':
		return '\v', json5
	}
	return 0, false
}

// lowHalf is the character the surrogate hi and a \u escape of a low
// surrogate after it make, read past that escape, or U+FFFD when none
// follows.
func (r *reader) lowHalf(hi rune) rune {
	at := r.i
	if hi < 0xdc00 && r.i+1 < len(r.data) && r.data[r.i] == '\\' && r.data[r.i+1] == 'u' {
		r.i += 2
		if lo, ok := r.hex(4); ok {
			if ru := utf16.DecodeRune(hi, lo); ru != utf8.RuneError {
				return ru
			}
		}
		r.i = at
	}
	return utf8.RuneError
}

// number reads a number and returns the text the file wrote: JSON's
// grammar, or in JSON5 ECMAScript 5.1's numeric literals with a sign.
func (r *reader) number() (string, error) {
	start := r.i
	if c := r.data[r.i]; c == '-' || (r.json5 && c == '+') {
		r.i++
	}
	ok := false
	if r.json5 {
		ok = r.word("Infinity") || r.word("NaN") || r.hexNumber() || r.decimal5()
	} else {
		ok = r.decimal()
	}
	if !ok {
		return "", r.fail("a number cannot hold %s here", r.found())
	}
	if r.i < len(r.data) {
		if c, _ := utf8.DecodeRune(r.data[r.i:]); identPart(c) || c == '\\' || c == '.' {
			return "", r.fail("a number cannot be followed by %s", r.found())
		}
	}
	return string(r.data[start:r.i]), nil
}

// digits reads decimal digits and reports how many it read.
func (r *reader) digits() int {
	start := r.i
	for r.i < len(r.data) && r.data[r.i] >= '0' && r.data[r.i] <= '9' {
		r.i++
	}
	return r.i - start
}

// integer reads 0, or a digit other than 0 and the digits after it.
func (r *reader) integer() bool {
	if r.i < len(r.data) && r.data[r.i] == '0' {
		r.i++
		return true
	}
	return r.digits() > 0
}

// exponent reads an exponent when one starts here, and reports whether
// what it read is whole.
func (r *reader) exponent() bool {
	if r.i >= len(r.data) || (r.data[r.i] != 'e' && r.data[r.i] != 'E') {
		return true
	}
	r.i++
	if r.i < len(r.data) && (r.data[r.i] == '+' || r.data[r.i] == '-') {
		r.i++
	}
	return r.digits() > 0
}

// decimal reads JSON's number after its sign.
func (r *reader) decimal() bool {
	if !r.integer() {
		return false
	}
	if r.i < len(r.data) && r.data[r.i] == '.' {
		r.i++
		if r.digits() == 0 {
			return false
		}
	}
	return r.exponent()
}

// decimal5 reads ECMAScript 5.1's DecimalLiteral: JSON's, with a point
// that may lead or end it.
func (r *reader) decimal5() bool {
	whole := r.integer()
	if r.i < len(r.data) && r.data[r.i] == '.' {
		r.i++
		if r.digits() == 0 && !whole {
			return false
		}
	} else if !whole {
		return false
	}
	return r.exponent()
}

// hexNumber reads 0x and hex digits, and reads nothing when no digit
// follows the x.
func (r *reader) hexNumber() bool {
	if r.i+1 >= len(r.data) || r.data[r.i] != '0' || (r.data[r.i+1] != 'x' && r.data[r.i+1] != 'X') {
		return false
	}
	end := r.i + 2
	for end < len(r.data) {
		if _, ok := hexDigit(r.data[end]); !ok {
			break
		}
		end++
	}
	if end == r.i+2 {
		return false
	}
	r.i = end
	return true
}
