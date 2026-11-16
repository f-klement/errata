// Package properties is the properties extractor: every value of a key in a
// Java .properties file, read as java.util.Properties.load reads it, through
// core's entry extractor.
package properties

import (
	"bytes"
	"errors"
	"strings"
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
	core.OneFile
	Key string `json:"key"`
}

// Kind is the properties kind, which a rule writes under the key properties.
var Kind = core.Kind[Spec]{Key: "properties", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is p with only what decides where it reads: the file cleaned, and
// no max_bytes, since a limit does not change what is read.
func Place(p Spec) Spec {
	p.OneFile = p.Place()
	return p
}

// Resolve is p with its max_bytes written out, for errata list.
func Resolve(p Spec) Spec {
	p.OneFile = p.Resolved()
	return p
}

// Describe is what p's extractor returns: flat values, not literal in a
// file whose placeholders Spring or Log4j fill in and in a template file,
// as Build marks them.
func Describe(p Spec) check.Description {
	return check.Description{Label: "properties", NotLiteral: notLiteral(p.File) != nil}
}

// notLiteral is the test of a value of file that the tool reading it, or a
// template engine, computes: a ${ in a file of Spring Boot's
// (application.properties, bootstrap.properties and their -profile
// files) or of Log4j's (log4j2.properties, log4j2-test.properties and
// log4j.properties), which fill in ${name} from their own properties and
// the environment, and Log4j writes $${ as ${. A template is tested as
// the file it renders. Java reads any other value as written; a
// placeholder Maven's resource filtering fills in is not modelled, since
// the pom, not the file, turns it on.
func notLiteral(file string) func(*core.Entry) bool {
	_, base := core.FileParts(core.Untemplated(file))
	var test func(*core.Entry) bool
	if placeholderFile(base) {
		test = func(e *core.Entry) bool { return bytes.Contains(e.Value, []byte("${")) }
	}
	return core.TemplateNotLiteral(file, test)
}

// placeholderFile reports whether base names a properties file Spring Boot
// or Log4j reads and fills placeholders in: application, bootstrap,
// log4j2 and log4j, each alone or, but for log4j, with -something after
// it, then .properties.
func placeholderFile(base string) bool {
	stem, ok := strings.CutSuffix(base, ".properties")
	if !ok {
		return false
	}
	if stem == "log4j" {
		return true
	}
	for _, name := range []string{"application", "bootstrap", "log4j2"} {
		if stem == name || strings.HasPrefix(stem, name+"-") {
			return true
		}
	}
	return false
}

// Build checks p and returns its extractor.
func Build(p Spec) (check.Extractor, error) {
	src, err := p.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	if p.Key == "" {
		return nil, errors.New("properties: key is required")
	}
	// Java reads every value as written; Spring, Log4j and a template's
	// engine do not.
	return core.EntryExtractor{Source: src, Query: core.Query{Key: []byte(p.Key), HasKey: true}, Read: core.TemplateRead(p.File, readProperties), NotLiteral: notLiteral(p.File)}, nil
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
// one. Lines are numbered at the same line ends, as every extractor numbers
// them (core.LineCursor), so a file that ends its lines with a lone \r has as
// many lines as an editor shows.
func readProperties(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	r := propertiesReader{data: data, lines: core.NewLineCursor(data)}
	var e core.Entry
	// Each logical line is charged its bytes and lineWork for each line end
	// it took, the comment and blank lines skipped before it too, a lone \r
	// as much as a \n.
	last, lastLine := 0, 1
	for ; r.logicalLine(); last = r.pos {
		line := r.lines.At(r.pos)
		if err := m.Charge(core.LineWork*max(1, line-lastLine) + r.pos - last); err != nil {
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
		lastLine = line
	}
	return m.Charge(core.LineWork*(r.lines.At(len(r.data))-lastLine) + len(r.data) - last)
}

// propertiesReader is Java's LineReader over data: logicalLine reads the
// next logical line into buf, without its comments, continuations and line
// breaks, and start is the line of its first character.
type propertiesReader struct {
	data  []byte
	pos   int
	lines *core.LineCursor // the line of an offset, past the line ends before it: \n, \r\n and a lone \r
	buf   []byte
	start int
}

func isPropertiesSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\f' }

// lineState is where logicalLine is in the logical line it reads.
type lineState struct {
	skipSpace bool // space, and line breaks unless joined, are skipped before the next character
	joined    bool // a continuation joined the next physical line, whose line break ends the line
	backslash bool // the last character read is a \ that escapes the next
	sized     bool // buf has room for the rest of the physical line
}

// logicalLine reports whether there is another logical line, and reads it.
func (r *propertiesReader) logicalLine() bool {
	r.buf = r.buf[:0]
	st := lineState{skipSpace: true}
	for r.pos < len(r.data) {
		c := r.data[r.pos]
		r.pos++
		// The order of the cases matters: skip ends the skipping at the
		// first character it does not skip, and lineBreak moves pos and
		// the state, so each runs only when no case before it took c.
		switch {
		case st.skip(c):
		case r.commentStart(c):
			r.skipComment()
			st.skipSpace = true
		case c != '\n' && c != '\r':
			r.appendChar(c, &st)
		case r.lineBreak(c, &st):
			return true
		}
	}
	return r.lastLine(st.backslash)
}

// skip reports whether c is space to skip before the line's next
// character, and stops the skipping at the first character that is not.
func (st *lineState) skip(c byte) bool {
	if !st.skipSpace {
		return false
	}
	if isPropertiesSpace(c) || (!st.joined && (c == '\r' || c == '\n')) {
		return true
	}
	st.skipSpace, st.joined = false, false
	return false
}

// commentStart reports whether c, read where no logical line has begun,
// starts a comment.
func (r *propertiesReader) commentStart(c byte) bool {
	return len(r.buf) == 0 && (c == '#' || c == '!')
}

// appendChar appends c, the character read before pos and no line break, as
// Java decodes it.
func (r *propertiesReader) appendChar(c byte, st *lineState) {
	if len(r.buf) == 0 {
		r.start = r.lines.At(r.pos - 1)
	}
	if !st.sized {
		st.sized = true
		r.reserve()
	}
	if c < utf8.RuneSelf {
		r.buf = append(r.buf, c)
	} else {
		var n int
		r.buf, n = appendJava(r.buf, r.data[r.pos-1:])
		r.pos += n - 1
	}
	st.backslash = c == '\\' && !st.backslash
}

// reserve makes room in buf for the physical line from pos-1 on, as Java
// decodes it, in one allocation, so a line of bytes that are not UTF-8,
// three bytes each as U+FFFD, is not grown into by appends.
func (r *propertiesReader) reserve() {
	rest := r.data[r.pos-1:]
	if i := bytes.IndexAny(rest, "\r\n"); i >= 0 {
		rest = rest[:i]
	}
	if need := javaLen(rest); cap(r.buf)-len(r.buf) < need {
		// At least doubled, for a value joined over many lines; make and
		// copy, not slices.Grow, whose make is a second allocation under
		// the race detector.
		grown := make([]byte, len(r.buf), max(len(r.buf)+need, 2*cap(r.buf)))
		copy(grown, r.buf)
		r.buf = grown
	}
}

// lineBreak reads c, a line break, and reports whether it ends the logical
// line. A break after a \ joins the next physical line, and a break before
// the line's first character is skipped.
func (r *propertiesReader) lineBreak(c byte, st *lineState) bool {
	switch {
	case len(r.buf) == 0:
		st.skipSpace = true
		return false
	case r.pos >= len(r.data):
		return r.lastLine(st.backslash)
	case st.backslash:
		r.buf = r.buf[:len(r.buf)-1]
		*st = lineState{skipSpace: true, joined: true}
		if c == '\r' && r.data[r.pos] == '\n' {
			r.pos++
		}
		return false
	}
	return true
}

// lastLine reports whether the data's end leaves a logical line in buf, and
// drops a \ at its end, which escapes nothing.
func (r *propertiesReader) lastLine(backslash bool) bool {
	if len(r.buf) == 0 {
		return false
	}
	if backslash {
		r.buf = r.buf[:len(r.buf)-1]
	}
	return true
}

// skipComment reads to the end of the line, through its \r or \n.
func (r *propertiesReader) skipComment() {
	for r.pos < len(r.data) {
		c := r.data[r.pos]
		r.pos++
		if c == '\r' || c == '\n' {
			return
		}
	}
}

// splitProperty finds where the key of a logical line ends and its value
// starts, as Properties.load0 does.
func splitProperty(line []byte) (keyEnd, valueStart int) {
	keyEnd, valueStart, sep := keyBound(line)
	return keyEnd, skipSeparator(line, valueStart, sep)
}

// keyBound finds where the key of line ends: at the first = or : or space
// that no \ escapes. valueStart is just past it, and sep reports whether it
// is a separator rather than a space.
func keyBound(line []byte) (keyEnd, valueStart int, sep bool) {
	backslash := false
	for ; keyEnd < len(line); keyEnd++ {
		c := line[keyEnd]
		if !backslash && (c == '=' || c == ':') {
			return keyEnd, keyEnd + 1, true
		}
		if !backslash && isPropertiesSpace(c) {
			return keyEnd, keyEnd + 1, false
		}
		backslash = c == '\\' && !backslash
	}
	return keyEnd, len(line), false
}

// skipSeparator skips the space after a key from i, and the one = or : in
// it unless sep says the key ended at one, to where the value starts.
func skipSeparator(line []byte, i int, sep bool) int {
	for ; i < len(line); i++ {
		c := line[i]
		if isPropertiesSpace(c) {
			continue
		}
		if sep || (c != '=' && c != ':') {
			break
		}
		sep = true
	}
	return i
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
		if in[i] != 'u' {
			out = append(out, escaped(in[i]))
			continue
		}
		var err error
		if out, i, err = appendUnicode(out, in, i); err != nil {
			return out, err
		}
	}
	return out, nil
}

// escaped is the character that c, after a \ and no u, stands for.
func escaped(c byte) byte {
	switch c {
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case 'n':
		return '\n'
	case 'f':
		return '\f'
	}
	return c
}

// appendUnicode appends the \u escape whose u is in[i], and a low surrogate
// escape after it that completes a pair, to out, and returns the index of
// the escape's last byte.
func appendUnicode(out, in []byte, i int) ([]byte, int, error) {
	u, ok := hex4(in[i+1:])
	if !ok {
		return out, i, errUnicodeEscape
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
	return utf8.AppendRune(out, r), i, nil
}

// hex4 reads four hex digits at the start of b.
func hex4(b []byte) (uint16, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var u uint16
	for _, c := range b[:4] {
		d, ok := hexDigit(c)
		if !ok {
			return 0, false
		}
		u = u<<4 | uint16(d)
	}
	return u, true
}

// hexDigit is the value of the hex digit c.
func hexDigit(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
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
	switch b1 := b[0]; {
	case b1 >= 0xe0 && b1 <= 0xef:
		return malformed3(b)
	case b1 >= 0xf0 && b1 <= 0xf4:
		return malformed4(b)
	}
	return 1 // a byte no character starts with, or a two-byte lead cut short
}

// malformed3 is javaMalformed of b, which starts with the lead byte of a
// three-byte character.
func malformed3(b []byte) int {
	switch {
	case len(b) < 3:
		if len(b) > 1 && bad3Second(b[0], b[1]) {
			return 1
		}
		return len(b) // cut by the end of the file
	case bad3Second(b[0], b[1]):
		return 1
	case !isCont(b[2]):
		return 2
	}
	return 3 // an encoded surrogate
}

// bad3Second reports whether b2 cannot follow b1, the lead byte of a
// three-byte character, as its second byte.
func bad3Second(b1, b2 byte) bool { return (b1 == 0xe0 && b2&0xe0 == 0x80) || !isCont(b2) }

// malformed4 is javaMalformed of b, which starts with the lead byte of a
// four-byte character.
func malformed4(b []byte) int {
	switch {
	case len(b) < 4:
		if len(b) > 1 && bad4Second(b[0], b[1]) {
			return 1
		}
		if len(b) > 2 && !isCont(b[2]) {
			return 2
		}
		return len(b)
	case bad4Second(b[0], b[1]):
		return 1
	case !isCont(b[2]):
		return 2
	}
	return 3 // the fourth byte is no continuation
}

// bad4Second reports whether b2 cannot follow b1, the lead byte of a
// four-byte character, as its second byte.
func bad4Second(b1, b2 byte) bool {
	return (b1 == 0xf0 && (b2 < 0x90 || b2 > 0xbf)) || (b1 == 0xf4 && b2&0xf0 != 0x80) || !isCont(b2)
}
