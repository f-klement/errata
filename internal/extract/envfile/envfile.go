// Package envfile is the envfile extractor: the values of a key in a dotenv
// file, read as docker compose reads its .env file and a service's
// env_file, through core's entry extractor.
package envfile

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value of Name in an env file, or every value of
// every key when Name is left out, each at the line of its assignment.
type Spec struct {
	core.OneFile
	Name string `json:"name,omitempty"`
}

// kindKey is the kind's key, which Kind and Build both use. Build names
// the constant rather than Kind.Key because an env reader's Read calls
// Build, and Build reading Kind would make an initialization cycle.
const kindKey = "envfile"

// Kind is the envfile kind, which a rule writes under the key envfile. It
// is an env reader, the first env lists.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe,
	Env: &check.EnvChoice{Order: 10, Chooses: chooses}, EnvRead: envRead}

// chooses takes, for the env kind, a name that starts with .env or ends in
// .env, such as prod.env, which compose's env_file: and systemd's
// EnvironmentFile= name. It leaves .envrc, direnv's shell script, and a
// name that ends in .sh to the shell reader.
func chooses(file string) bool {
	base := path.Base(file)
	if base == ".envrc" || strings.HasSuffix(base, ".sh") {
		return false
	}
	return strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env")
}

// envRead is the env kind's reader of the variable name in file: Build's.
func envRead(file, name string, maxBytes int64, _ core.Parsers) (check.Extractor, error) {
	return Build(Spec{File: file, Name: name, MaxBytes: maxBytes})
}

// Place is s with only what decides where it reads: the file cleaned, and
// no max_bytes, since a limit does not change what is read.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	return s
}

// Resolve is s with its max_bytes written out, for errata list.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since compose computes a value from a key the file does not set.
func Describe(Spec) check.Description {
	return check.Description{Label: "envfile", NotLiteral: true}
}

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	src, err := s.Check(kindKey)
	if err != nil {
		return nil, err
	}
	// A value compose computes from a key the file does not set is not
	// literal, and in a template file so is one whose text as written the
	// engine renders, and every substitution of it.
	rendered := core.TemplateNotLiteral(s.File, nil)
	read := func(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
		return readFile(data, m, rendered, visit)
	}
	x := core.EntryExtractor{Source: src, Read: read, NotLiteral: computed, Keyed: true}
	if s.Name == "" {
		x.Read = func(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
			return everyKey(data, m, rendered, visit)
		}
		return x, nil
	}
	if !isKey(s.Name) {
		return nil, fmt.Errorf("envfile: name %s is no key an env file can set; compose reads a name of letters, digits, _, ., -, [ and ]", core.Quote(s.Name))
	}
	x.Query = core.Query{Key: []byte(s.Name), HasKey: true}
	return x, nil
}

// computed is the test of a value readEnv marked as one compose computes.
func computed(e *core.Entry) bool { return e.Computed }

// everyKey is readEnv with every entry of kind EntryName, which a query
// with no key wants: a spec without a name selects every key's value.
func everyKey(data []byte, m *core.Meter, rendered func(*core.Entry) bool, visit func(*core.Entry) error) error {
	return readFile(data, m, rendered, func(e *core.Entry) error {
		e.Kind = core.EntryName
		return visit(e)
	})
}

// isKey reports whether name is a key some line of an env file sets: the
// key readEnv reads from the line name=.
func isKey(name string) bool {
	n := 0
	err := readEnv([]byte(name+"=\n"), func(e *core.Entry) error {
		if n++; string(e.Key) != name {
			return errStop
		}
		return nil
	})
	return err == nil && n == 1
}

var errStop = errors.New("stopped")

// readEnv reads an env file as compose-go's dotenv parser reads it (its
// parser.go and template.go, v2.16.2, ported step for step), with an empty
// environment, and visits every key and value in file order, at the line
// where the key starts:
//
//   - a UTF-8 byte order mark at the start is skipped; white space,
//     Unicode's included, separates statements, and a statement that
//     starts with # is a comment to the end of its line;
//   - export and white space before a key are dropped;
//   - the key runs to the first =, : or line break, and holds letters,
//     digits, _, ., -, [ and ], with white space other than a line break
//     between them; a space left inside it once its end is trimmed, or any
//     other character, is refused;
//   - a key the line break ends takes its value from the environment;
//   - after the separator, white space is skipped. A value in ' or " runs
//     to the next quote of its kind that no \ escapes, across lines, and
//     what follows it on the line is read as the next statement. In ' it
//     is as written but for \' and in " the escapes \a \b \f \n \r \t \v
//     \" \\ \$ and \0 with three octal digits are read, and then the
//     substitutions. An unquoted value runs to the end of the line, is cut
//     at the first " #", trimmed of white space at its end, and
//     substituted;
//   - a substitution is $NAME, ${NAME} or ${NAME} with :-, -, :+, +, :?
//     or ? and a word, as compose's template package reads them, and $$
//     is $; a ${ that is none of these is refused;
//   - a file whose last line holds no =, : or line break sets the empty
//     key to that line, as compose's loop ends without a separator.
//
// compose keeps the last value of a key set twice; errata returns every
// one, so an agree rule sees the file disagree with itself. A name is
// resolved from the keys set before it in the file. A substitution that
// asks for a name the file has not set, or one set from the environment,
// depends on the environment errata does not read, so the value is
// returned as the file wrote it, between its quotes, and marked computed;
// a key that takes its value from the environment is an empty computed
// value. The environment would also take precedence over a key the file
// sets, which errata does not model. Lines are counted at \n.
//
// readEnv reads with no meter, for isKey, whose input is a spec's name, and
// for the tests that hold the reader to compose. An extractor reads a file
// through readFile, which charges its meter.
func readEnv(data []byte, visit func(*core.Entry) error) error {
	return readFile(data, nil, nil, visit)
}

// readFile is readEnv charged to m, for a template file whose engine renders
// a value written as rendered reports, which may be nil: such a value is
// returned as written and marked computed, so a substitution of its key is
// computed too.
func readFile(data []byte, m *core.Meter, rendered func(*core.Entry) bool, visit func(*core.Entry) error) error {
	r := envReader{data: data, m: m, lines: core.NewLineCursor(data), vars: map[string]binding{}}
	r.pos = len(data) - len(core.DropBOM(data))
	var e core.Entry
	for {
		more, err := r.statement()
		if err != nil {
			return err
		}
		if !more {
			return r.m.Charge(len(r.data) - r.charged)
		}
		r.raw = nil
		if err := r.entry(&e); err != nil {
			return err
		}
		// One entry for the test, reused: a new one for each statement
		// escaped to the heap, 144 bytes a statement.
		r.probe.Value = r.raw
		if rendered != nil && !e.Computed && rendered(&r.probe) {
			e.Value, e.Computed = r.raw, true
		}
		if err := r.m.Charge(core.LineWork + r.pos - r.charged); err != nil {
			return err
		}
		r.charged = r.pos
		r.vars[string(e.Key)] = binding{value: string(e.Value), known: !e.Computed}
		if err := visit(&e); err != nil {
			return err
		}
	}
}

// binding is what the file set a key to: known is false when the value
// depends on the environment.
type binding struct {
	value string
	known bool
}

type envReader struct {
	data    []byte
	pos     int
	charged int // the bytes before it are charged
	m       *core.Meter
	lines   *core.LineCursor
	vars    map[string]binding
	chars   []byte // a quoted value, its quotes and its \ before a quote read
	escaped []byte // chars with its escapes read
	out     []byte // the value substituted
	raw     []byte // the value as written, between its quotes
	probe   core.Entry
}

// statement skips white space and comments to the start of the next
// statement, as getStatementStart does, and reports whether there is one.
func (r *envReader) statement() (bool, error) {
	for {
		i := bytes.IndexFunc(r.data[r.pos:], notSpace)
		if i == -1 {
			r.pos = len(r.data)
			return false, nil
		}
		r.pos += i
		if r.data[r.pos] != '#' {
			return true, nil
		}
		_, rest, found := bytes.Cut(r.data[r.pos:], []byte("\n"))
		if !found {
			r.pos = len(r.data)
			return false, nil
		}
		r.pos = len(r.data) - len(rest)
		if err := r.m.Charge(core.LineWork); err != nil {
			return false, err
		}
	}
}

// notSpace is the test of a character unicode.IsSpace does not read as
// white space, which separates statements.
func notSpace(c rune) bool { return !unicode.IsSpace(c) }

// isSpace is dotenv's white space, which is unicode.IsSpace's but for the
// line breaks.
func isSpace(c rune) bool {
	switch c {
	case '\t', '\v', '\f', '\r', ' ', 0x85, 0xA0:
		return true
	}
	return false
}

// skipSpace moves pos past dotenv's white space.
func (r *envReader) skipSpace() {
	i := bytes.IndexFunc(r.data[r.pos:], func(c rune) bool { return !isSpace(c) })
	if i == -1 {
		r.pos = len(r.data)
		return
	}
	r.pos += i
}

// isExport reports whether b starts with export and white space as RE2's
// \s reads it.
func isExport(b []byte) bool {
	if !bytes.HasPrefix(b, []byte("export")) || len(b) == len("export") {
		return false
	}
	switch b[len("export")] {
	case '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

// isSeparator reports whether c ends a name: =, : or a line break.
func isSeparator(c rune) bool { return c == '=' || c == ':' || c == '\n' }

// inName reports whether c may stand in a name, as locateKeyName lets it:
// letters, digits, _ . - [ ], and dotenv's white space.
func inName(c rune) bool {
	return isSpace(c) || c == '_' || c == '.' || c == '-' || c == '[' || c == ']' || unicode.IsLetter(c) || unicode.IsNumber(c)
}

// entry reads the statement at pos into e, as locateKeyName and
// extractVarValue do, and leaves pos after it.
func (r *envReader) entry(e *core.Entry) error {
	if isExport(r.data[r.pos:]) {
		r.pos += len("export")
		r.skipSpace()
		if r.pos == len(r.data) {
			return core.LineError(r.lines.At(r.pos), "export is followed by no name, which compose refuses")
		}
	}
	start := r.pos
	e.Line = r.lines.At(start)
	var keyEnd int
	var sep byte
	for i := start; i < len(r.data); {
		c, w := utf8.DecodeRune(r.data[i:])
		if isSeparator(c) {
			keyEnd, sep = i, r.data[i] // =, : or \n, one byte each
			break
		}
		if !inName(c) {
			return core.LineError(e.Line, "the name holds %s, which compose refuses in a name", core.Quote(string(r.data[i:i+w])))
		}
		i += w
	}
	e.Key, e.Computed = nil, false
	// When the file ends without a separator, compose's loop leaves the key
	// empty and reads the whole statement, from start, as its value.
	if sep != 0 {
		e.Key = bytes.TrimRightFunc(r.data[start:keyEnd], unicode.IsSpace)
		r.pos = keyEnd + 1
	}
	if bytes.Contains(e.Key, []byte(" ")) {
		return core.LineError(e.Line, "the name %s holds a space, which compose refuses", core.Quote(string(e.Key)))
	}
	r.skipSpace()
	if sep == '\n' {
		e.Value, e.Computed = nil, true
		return nil
	}
	return r.value(e)
}

// value reads the value at pos into e.
func (r *envReader) value(e *core.Entry) error {
	src := r.data[r.pos:]
	if len(src) == 0 || (src[0] != '\'' && src[0] != '"') {
		raw, _, _ := bytes.Cut(src, []byte("\n"))
		r.pos += min(len(raw)+1, len(src))
		raw, _, _ = bytes.Cut(raw, []byte(" #"))
		raw = bytes.TrimRightFunc(raw, unicode.IsSpace)
		r.raw = raw
		return r.substituteValue(e, raw, raw)
	}
	quote := src[0]
	r.chars = r.chars[:0]
	escape := false
	for i := 1; i < len(src); i++ {
		c := src[i]
		if c != quote {
			if !escape && c == '\\' {
				escape = true
				continue
			}
			if escape {
				escape = false
				r.chars = append(r.chars, '\\')
			}
			r.chars = append(r.chars, c)
			continue
		}
		if escape {
			escape = false
			r.chars = append(r.chars, c)
			continue
		}
		raw := src[1:i]
		r.raw = raw
		r.pos += i + 1
		if quote == '\'' {
			e.Value = r.chars
			return nil
		}
		if err := r.m.Charge(len(r.chars)); err != nil {
			return err
		}
		r.escaped = expandEscapes(r.escaped[:0], r.chars)
		return r.substituteValue(e, r.escaped, raw)
	}
	return core.LineError(e.Line, "the value's %c is not closed, which compose refuses", quote)
}

// substituteValue sets e's value to v substituted, or to raw, marked
// computed, when that depends on the environment.
func (r *envReader) substituteValue(e *core.Entry, v, raw []byte) error {
	out, unknown, err := r.substitute(r.out[:0], v, e.Line, 0)
	r.out = out
	if err != nil {
		return err
	}
	if unknown {
		e.Value, e.Computed = raw, true
	} else {
		e.Value = out
	}
	return nil
}

// expandEscapes appends s with the escapes of a " value read, as
// compose's expandEscapes reads them: the regular expression
// \\(?:[abcfnrtv$"\\]|0\d{0,3}) from left to right, \$ as $$ for the
// substitution to read as $, and each match through strconv.UnquoteChar,
// with \0 rewritten as \ first. A match UnquoteChar refuses is kept as
// rewritten: \c stays \c, and \012 becomes \12.
func expandEscapes(out, s []byte) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			out = append(out, s[i])
			continue
		}
		c := s[i+1]
		var match string
		switch c {
		case '$':
			out = append(out, "$$"...)
			i++
			continue
		case '0':
			// The regular expression's \d{0,3} takes 8 and 9 too, but a match
			// with either is one UnquoteChar refuses and is kept as written,
			// as the shorter match of octal digits here is with the rest
			// after it: the bytes come out the same.
			n := 0
			for n < 3 && i+2+n < len(s) && isOctal(s[i+2+n]) {
				n++
			}
			match = `\` + string(s[i+2:i+2+n])
			i += 1 + n
		case 'a', 'b', 'c', 'f', 'n', 'r', 't', 'v', '"', '\\':
			match = string(s[i : i+2])
			i++
		default:
			out = append(out, '\\')
			continue
		}
		v, _, _, err := strconv.UnquoteChar(match, '"')
		if err != nil {
			out = append(out, match...)
			continue
		}
		out = utf8.AppendRune(out, v)
	}
	return out
}

// isOctal reports whether b is an octal digit.
func isOctal(b byte) bool { return '0' <= b && b <= '7' }

// copyWork is the work of each byte a substitution copies from a key's
// value, so a chain of keys that each repeat the one before is refused for
// work before its values fill memory. It bounds the bytes copied, not time:
// a read copies at most a 32nd of its budget, 134 MB at 32 MiB. The memory
// is about three times that, since the value grows by append and is copied
// once more into the bindings: 32 MiB of ${A} for a value of 4 KiB is
// refused after 733 MiB allocated, anon 395 MiB above its base on a 2 core worker.
// At 8 the same shape allocated 2.8 GB.
const copyWork = 32

// substitute appends s with its substitutions made, as compose's
// template.Substitute makes them with the keys set before as the mapping,
// and reports whether the result depends on the environment. A match of
// ${NAME and an operator runs, in compose's regular expression, to the
// last } of its line, and compose substitutes again what follows the }
// that closes it: here the scan goes on after that }, which reads the same.
// depth counts the words of operators substitute is inside.
func (r *envReader) substitute(out, s []byte, line, depth int) ([]byte, bool, error) {
	if depth > core.MaxDepth {
		return out, false, core.TooDeep(line)
	}
	if err := r.m.Charge(len(s)); err != nil {
		return out, false, err
	}
	unknown := false
	// The last } of the line, for the ${ from p on, until nextLine.
	var lastBrace int
	nextLine := 0
	for i := 0; ; {
		j := bytes.IndexByte(s[i:], '$')
		if j < 0 {
			out = append(out, s[i:]...)
			break
		}
		p := i + j
		out = append(out, s[i:p]...)
		if p+1 == len(s) {
			out = append(out, '$')
			break
		}
		if s[p+1] == '$' {
			out = append(out, '$')
			i = p + 2
			continue
		}
		if s[p+1] != '{' {
			n := nameLen(s[p+1:])
			if n == 0 {
				out = append(out, '$')
				i = p + 1
				continue
			}
			var known bool
			var err error
			if out, known, err = r.lookup(out, s[p+1:p+1+n]); err != nil {
				return out, false, err
			}
			unknown = unknown || !known
			i = p + 1 + n
			continue
		}
		n := nameLen(s[p+2:])
		if n == 0 {
			return out, false, invalid(line, s[p:])
		}
		name, q := s[p+2:p+2+n], p+2+n
		if q < len(s) && s[q] == '}' {
			var known bool
			var err error
			if out, known, err = r.lookup(out, name); err != nil {
				return out, false, err
			}
			unknown = unknown || !known
			i = q + 1
			continue
		}
		opEnd := q
		if opEnd < len(s) && s[opEnd] == ':' {
			opEnd++
		}
		if opEnd == len(s) || bytes.IndexByte([]byte("-+?"), s[opEnd]) < 0 {
			return out, false, invalid(line, s[p:])
		}
		opEnd++
		if p >= nextLine {
			lineEnd := len(s)
			if e := bytes.IndexByte(s[p:], '\n'); e != -1 {
				lineEnd = p + e
			}
			lastBrace = p + bytes.LastIndexByte(s[p:lineEnd], '}')
			nextLine = lineEnd + 1
		}
		if lastBrace < opEnd {
			return out, false, invalid(line, s[p:])
		}
		end := closingBrace(s[:lastBrace+1], p)
		var known bool
		var err error
		out, known, err = r.operator(out, name, string(s[q:opEnd]), s[opEnd:end], line, depth)
		if err != nil {
			return out, false, err
		}
		unknown = unknown || !known
		i = end + 1
	}
	return out, unknown, nil
}

// closingBrace is the } of s that closes the ${ at p, as
// getFirstBraceClosingIndex finds it: each { opens one and hides the byte
// after it, and each } closes one. With none, it is the last byte of s.
func closingBrace(s []byte, p int) int {
	open := 0
	for k := p; k < len(s); k++ {
		switch s[k] {
		case '}':
			if open--; open == 0 {
				return k
			}
		case '{':
			open++
			k++
		}
	}
	return len(s) - 1
}

// operator appends what ${name op word} gives, and reports whether that is
// known. The word is substituted only where compose substitutes it, so it
// is never read for a name the file leaves to the environment.
func (r *envReader) operator(out, name []byte, op string, word []byte, line, depth int) ([]byte, bool, error) {
	b, set := r.vars[string(name)]
	if !set || !b.known {
		// compose would ask the environment for the name.
		return out, false, nil
	}
	empty := b.value == ""
	switch op {
	case ":-", "-":
		if op == ":-" && empty {
			return r.word(out, word, line, depth)
		}
	case ":+", "+":
		if op == "+" || !empty {
			return r.word(out, word, line, depth)
		}
	default: // :? and ?
		if op == ":?" && empty {
			return out, false, core.LineError(line, "${%s:?} needs a value that is not empty and the file sets it empty, which compose refuses", core.Clip(string(name)))
		}
	}
	out, err := r.copyValue(out, b.value)
	return out, true, err
}

// word appends an operator's word substituted, and reports whether that is
// known.
func (r *envReader) word(out, word []byte, line, depth int) ([]byte, bool, error) {
	out, unknown, err := r.substitute(out, word, line, depth+1)
	return out, !unknown, err
}

// lookup appends the value of name the file set before, and reports
// whether it is known.
func (r *envReader) lookup(out, name []byte) ([]byte, bool, error) {
	b, ok := r.vars[string(name)]
	if !ok || !b.known {
		return out, false, nil
	}
	out, err := r.copyValue(out, b.value)
	return out, true, err
}

// copyValue appends v, charged copyWork a byte.
func (r *envReader) copyValue(out []byte, v string) ([]byte, error) {
	if err := r.m.Charge(copyWork * len(v)); err != nil {
		return out, err
	}
	return append(out, v...), nil
}

// invalid refuses a ${ that starts no substitution.
func invalid(line int, s []byte) error {
	return core.LineError(line, "%s starts no substitution compose reads; write $${ for a literal ${", core.Quote(string(s)))
}

// kelvin and longS are the two characters other than ASCII letters that
// compose's case-insensitive [_a-z] matches, since Go's regexp folds k and
// s to them.
const (
	kelvin = '\u212A'
	longS  = '\u017F'
)

// nameLen is the length of the name at the start of s, as compose's
// (?i)[_a-z][_a-z0-9]* matches it: 0 for none. It decodes s in place:
// ranging over string(s) would copy the rest of the value at each name.
func nameLen(s []byte) int {
	c, w := utf8.DecodeRune(s)
	if !isNameStart(c) {
		return 0
	}
	n := bytes.IndexFunc(s[w:], notInName)
	if n == -1 {
		return len(s)
	}
	return w + n
}

// notInName reports whether c ends a name after its first character.
func notInName(c rune) bool { return !isNameStart(c) && !isDigit(c) }

// isNameStart reports whether c may start a name: _, an ASCII letter, or
// one of the two characters Go's regexp folds k and s to.
func isNameStart(c rune) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == kelvin || c == longS
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c rune) bool { return '0' <= c && c <= '9' }
