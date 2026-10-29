// Package properties is the properties extractor: every value of a key in a
// Java .properties file, read as java.util.Properties.load reads it, through
// core's entry extractor.
package properties

import (
	"bytes"
	"errors"
	"path/filepath"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value of Key in a Java .properties file. Key is
// the key after its escapes are read, matched as text and never parsed as a
// path, so server.port is the key server.port: the dots the yaml extractor
// reads as steps are characters of the key here.
type Spec struct {
	File     string `json:"file"`
	Key      string `json:"key"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// Kind is the properties kind, which a rule writes under the key properties.
var Kind = core.Kind[Spec]{Key: "properties", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is p with only what decides where it reads: the file cleaned, and
// no max_bytes, since a limit does not change what is read.
func Place(p Spec) Spec {
	p.File, p.MaxBytes = filepath.Clean(p.File), 0
	return p
}

// Resolve is p with its max_bytes written out, for errata list.
func Resolve(p Spec) Spec {
	p.MaxBytes = core.MaxBytes(p.MaxBytes)
	return p
}

// Describe is what p's extractor returns: flat values, not literal in a
// template file, as Build marks them.
func Describe(p Spec) check.Description {
	return check.Description{Label: "properties", NotLiteral: core.TemplateNotLiteral(p.File, nil) != nil}
}

// Build checks p and returns its extractor.
func Build(p Spec) (check.Extractor, error) {
	src, err := core.CheckSource("properties", p.File, p.MaxBytes)
	if err != nil {
		return nil, err
	}
	if p.Key == "" {
		return nil, errors.New("properties: key is required")
	}
	// Java reads every value as written; a template's engine does not.
	return core.EntryExtractor{Source: src, Query: core.Query{Key: []byte(p.Key), HasKey: true}, Read: readProperties, NotLiteral: core.TemplateNotLiteral(p.File, nil)}, nil
}

// readProperties reads a .properties file as java.util.Properties.load does
// (its LineReader and loadConvert, ported step for step), and visits every
// key and value in file order, at the line where the key starts:
//
//   - a line ends at \n, \r or \r\n; space, \t and \f are white space;
//   - a line whose first character other than white space is # or ! is a
//     comment, and so is one that starts a logical line after a \ ended the
//     line before it with nothing else on it;
//   - an odd number of \ at the end of a line joins the next line to it,
//     without the \, the line break and the white space the next line starts
//     with; a comment line is never joined;
//   - the key runs to the first =, : or white space no \ escapes; then
//     white space, at most one = or :, and white space are skipped, and the
//     rest of the line is the value;
//   - \t \n \r \f and \uXXXX are escapes, and \ before any other character
//     is dropped; a \u without four hex digits is an error, as in Java.
//
// Properties.load keeps the last value of a key set twice; errata returns
// every one, so an agree rule sees the file disagree with itself. A file is
// read as UTF-8 text, as load(Reader) reads it with a UTF-8 reader, each
// sequence that is not UTF-8 one U+FFFD as Java's decoder makes it
// (appendJava); load(InputStream) reads each byte as one ISO 8859-1
// character instead. A \uXXXX surrogate pair is its one
// character, and a surrogate on its own is U+FFFD, since UTF-8 cannot hold
// one. Lines are counted at \n, as every extractor counts them, so a file
// that ends its lines with a lone \r is one line long.
func readProperties(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	r := propertiesReader{data: data, line: 1}
	var e core.Entry
	// Each logical line is charged its bytes and lineWork for each line end
	// it took, the comment and blank lines skipped before it too, a lone \r
	// as much as a \n.
	last, lastEnds := 0, 0
	for ; r.logicalLine(); last, lastEnds = r.pos, r.ends {
		if err := m.Charge(core.LineWork*max(1, r.ends-lastEnds) + r.pos - last); err != nil {
			return err
		}
		// The escapes are read in place: none is shorter than what it
		// stands for, so each key and value is a part of buf.
		keyEnd, valueStart := splitProperty(r.buf)
		var err error
		if e.Key, err = unescape(r.buf[:0], r.buf[:keyEnd]); err != nil {
			return core.LineError(r.start, "the key: %v", err)
		}
		if e.Value, err = unescape(r.buf[valueStart:valueStart], r.buf[valueStart:]); err != nil {
			return core.LineError(r.start, "the value: %v", err)
		}
		e.Line = r.start
		if err := visit(&e); err != nil {
			return err
		}
	}
	return m.Charge(core.LineWork*(r.ends-lastEnds) + len(r.data) - last)
}

// propertiesReader is Java's LineReader over data: logicalLine reads the
// next logical line into buf, without its comments, continuations and line
// breaks, and start is the line of its first character.
type propertiesReader struct {
	data  []byte
	pos   int
	line  int // the line of the byte at pos
	ends  int // the line ends before pos: \n, \r\n and a lone \r
	buf   []byte
	start int
}

// end counts the line end c, read at pos-1, once: a \r before a \n is
// counted at the \n.
func (r *propertiesReader) end(c byte) {
	switch {
	case c == '\n':
		r.line++
		r.ends++
	case c == '\r' && (r.pos == len(r.data) || r.data[r.pos] != '\n'):
		r.ends++
	}
}

func isPropertiesSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\f' }

// logicalLine reports whether there is another logical line, and reads it.
func (r *propertiesReader) logicalLine() bool {
	r.buf = r.buf[:0]
	skipSpace, joined, backslash := true, false, false
	sized := false // buf has room for the rest of the physical line
	for {
		if r.pos >= len(r.data) {
			if len(r.buf) == 0 {
				return false
			}
			if backslash {
				r.buf = r.buf[:len(r.buf)-1]
			}
			return true
		}
		c, line := r.data[r.pos], r.line
		r.pos++
		r.end(c)
		if skipSpace {
			if isPropertiesSpace(c) || (!joined && (c == '\r' || c == '\n')) {
				continue
			}
			skipSpace, joined = false, false
		}
		if len(r.buf) == 0 && (c == '#' || c == '!') {
			r.skipComment()
			skipSpace = true
			continue
		}
		if c != '\n' && c != '\r' {
			if len(r.buf) == 0 {
				r.start = line
			}
			if !sized {
				// One allocation for the physical line as Java decodes it,
				// so a line of bytes that are not UTF-8, three bytes each
				// as U+FFFD, is not grown into by appends.
				sized = true
				rest := r.data[r.pos-1:]
				if i := bytes.IndexAny(rest, "\r\n"); i >= 0 {
					rest = rest[:i]
				}
				if need := javaLen(rest); cap(r.buf)-len(r.buf) < need {
					// At least doubled, for a value joined over many
					// lines; make and copy, not slices.Grow, whose make is
					// a second allocation under the race detector.
					grown := make([]byte, len(r.buf), max(len(r.buf)+need, 2*cap(r.buf)))
					copy(grown, r.buf)
					r.buf = grown
				}
			}
			if c < utf8.RuneSelf {
				r.buf = append(r.buf, c)
			} else {
				var n int
				r.buf, n = appendJava(r.buf, r.data[r.pos-1:])
				r.pos += n - 1
			}
			backslash = c == '\\' && !backslash
			continue
		}
		switch {
		case len(r.buf) == 0:
			skipSpace = true
		case r.pos >= len(r.data):
			if backslash {
				r.buf = r.buf[:len(r.buf)-1]
			}
			return true
		case backslash:
			r.buf = r.buf[:len(r.buf)-1]
			skipSpace, joined, backslash, sized = true, true, false, false
			if c == '\r' && r.data[r.pos] == '\n' {
				r.pos++
				r.end('\n')
			}
		default:
			return true
		}
	}
}

// skipComment reads to the end of the line, through its \r or \n.
func (r *propertiesReader) skipComment() {
	for r.pos < len(r.data) {
		c := r.data[r.pos]
		r.pos++
		r.end(c)
		if c == '\r' || c == '\n' {
			return
		}
	}
}

// splitProperty finds where the key of a logical line ends and its value
// starts, as Properties.load0 does.
func splitProperty(line []byte) (keyEnd, valueStart int) {
	valueStart = len(line)
	sep, backslash := false, false
	for keyEnd < len(line) {
		c := line[keyEnd]
		if !backslash && (c == '=' || c == ':') {
			valueStart, sep = keyEnd+1, true
			break
		}
		if !backslash && isPropertiesSpace(c) {
			valueStart = keyEnd + 1
			break
		}
		backslash = c == '\\' && !backslash
		keyEnd++
	}
	for valueStart < len(line) {
		c := line[valueStart]
		if !isPropertiesSpace(c) {
			if sep || (c != '=' && c != ':') {
				break
			}
			sep = true
		}
		valueStart++
	}
	return keyEnd, valueStart
}

var errUnicodeEscape = errors.New("a \\u is not followed by four hex digits, which Properties.load refuses")

// unescape appends in, with its escapes read as loadConvert reads them, to
// out.
func unescape(out, in []byte) ([]byte, error) {
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		// The line reader leaves no \ at the end of a key or a value
		// that escapes nothing; one there is dropped, as no escape.
		if i++; i == len(in) {
			break
		}
		switch c = in[i]; c {
		case 'u':
			u, ok := hex4(in[i+1:])
			if !ok {
				return out, errUnicodeEscape
			}
			i += 4
			r := rune(u)
			if utf16.IsSurrogate(r) {
				r = utf8.RuneError
				if low, ok := lowSurrogate(in[i+1:]); ok && u < 0xdc00 {
					r = utf16.DecodeRune(rune(u), rune(low))
					i += 6
				}
			}
			out = utf8.AppendRune(out, r)
			continue
		case 't':
			c = '\t'
		case 'r':
			c = '\r'
		case 'n':
			c = '\n'
		case 'f':
			c = '\f'
		}
		out = append(out, c)
	}
	return out, nil
}

// hex4 reads four hex digits at the start of b.
func hex4(b []byte) (uint16, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var u uint16
	for _, c := range b[:4] {
		var d byte
		switch {
		case '0' <= c && c <= '9':
			d = c - '0'
		case 'a' <= c && c <= 'f':
			d = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		u = u<<4 | uint16(d)
	}
	return u, true
}

// lowSurrogate reads a \uXXXX escape of a low surrogate at the start of b.
func lowSurrogate(b []byte) (uint16, bool) {
	if len(b) < 6 || b[0] != '\\' || b[1] != 'u' {
		return 0, false
	}
	u, ok := hex4(b[2:])
	return u, ok && 0xdc00 <= u && u <= 0xdfff
}

// Java's UTF-8 decoder reads each malformed sequence as one U+FFFD, as
// long as Java takes it (sun.nio.cs.UTF_8's malformedN, and at the end of
// the file every byte left): a lead byte and as many of its continuation
// bytes as could still begin a character, an encoded surrogate whole. No
// sequence holds \n or \r, so the reader decodes as it reads the lines,
// and the lines stay the file's.

// appendJava appends the character at the start of b, whose first byte is
// not ASCII, as Java decodes it, and returns how many bytes of b it took.
func appendJava(out, b []byte) ([]byte, int) {
	if r, w := utf8.DecodeRune(b); r != utf8.RuneError || w > 1 {
		return append(out, b[:w]...), w
	}
	return utf8.AppendRune(out, utf8.RuneError), javaMalformed(b)
}

// javaLen is the length of b as Java decodes it.
func javaLen(b []byte) int {
	n := 0
	for i := 0; i < len(b); {
		switch r, w := utf8.DecodeRune(b[i:]); {
		case r != utf8.RuneError || w > 1:
			n, i = n+w, i+w
		default:
			n, i = n+utf8.RuneLen(utf8.RuneError), i+javaMalformed(b[i:])
		}
	}
	return n
}

func isCont(b byte) bool { return b&0xc0 == 0x80 }

// javaMalformed is how many bytes of b, which starts with no valid UTF-8,
// Java's decoder reads as one malformed sequence.
func javaMalformed(b []byte) int {
	b1 := b[0]
	switch {
	case b1 >= 0xe0 && b1 <= 0xef:
		bad2 := func(b2 byte) bool { return (b1 == 0xe0 && b2&0xe0 == 0x80) || !isCont(b2) }
		switch {
		case len(b) < 3:
			if len(b) > 1 && bad2(b[1]) {
				return 1
			}
			return len(b) // cut by the end of the file
		case bad2(b[1]):
			return 1
		case !isCont(b[2]):
			return 2
		}
		return 3 // an encoded surrogate
	case b1 >= 0xf0 && b1 <= 0xf4:
		bad2 := func(b2 byte) bool {
			return (b1 == 0xf0 && (b2 < 0x90 || b2 > 0xbf)) || (b1 == 0xf4 && b2&0xf0 != 0x80) || !isCont(b2)
		}
		switch {
		case len(b) < 4:
			if len(b) > 1 && bad2(b[1]) {
				return 1
			}
			if len(b) > 2 && !isCont(b[2]) {
				return 2
			}
			return len(b)
		case bad2(b[1]):
			return 1
		case !isCont(b[2]):
			return 2
		}
		return 3 // the fourth byte is no continuation
	}
	return 1 // a byte no character starts with, or a two-byte lead cut short
}
