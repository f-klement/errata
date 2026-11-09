package envfile

import (
	"bytes"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// dialect is the reader whose reading of a file envfile keeps to.
type dialect int

const (
	// compose is docker compose's .env reading, readEnv's.
	compose dialect = iota
	// sourced is a file in etc/default, which a shell script sources and
	// systemd's EnvironmentFile= may read too: readEnv's reading, with a
	// value marked computed where either reads it otherwise.
	sourced
	// pamEnv is etc/environment, which pam_env reads: readPam's reading.
	pamEnv
)

// etcDialect is the dialect file's name chooses, a template suffix taken
// off: sourced for a file directly in a default directory beneath etc,
// pamEnv for a file named environment directly beneath etc, and compose for
// any other.
func etcDialect(file string) dialect {
	dirs, base := core.FileParts(core.Untemplated(file))
	n := len(dirs) // one at least: FileParts gives "." for no directory
	switch {
	case n > 1 && dirs[n-2] == "etc" && dirs[n-1] == "default":
		return sourced
	case dirs[n-1] == "etc" && base == "environment":
		return pamEnv
	}
	return compose
}

// marks are a template's tests of a value the engine may render to
// anything: none for a file that is no template.
type marks struct {
	rendered func(*core.Entry) bool
	blocks   *core.Blocks
	// One entry for the test, reused: a new one for each statement
	// escaped to the heap, 144 bytes a statement.
	probe core.Entry
}

// newMarks is file's marks, its blocks found in data and charged to m.
func newMarks(file string, data []byte, m *core.Meter) (*marks, error) {
	blocks, err := core.TemplateBlocks(file, data, m)
	return &marks{rendered: core.TemplateNotLiteral(file, nil), blocks: blocks}, err
}

// notLiteral reports whether a value written raw, set on line, is one the
// engine renders or one inside a block of the engine's.
func (t *marks) notLiteral(raw []byte, line int) (bool, error) {
	in, err := t.blocks.Holds(line)
	t.probe.Value = raw
	return in || t.rendered != nil && t.rendered(&t.probe), err
}

// markSourced returns e as written and marked computed unless
// sourcedReads takes its statement and no statement before is tainted. A
// statement that is not plain taints every one after it when a shell may
// read the lines after it otherwise than compose, or stop before them, as
// carriesPast finds.
func (r *envReader) markSourced(e *core.Entry) {
	if !r.tainted && sourcedReads(r.data, r.start, e) {
		return
	}
	if !e.Computed {
		e.Value, e.Computed = r.raw, true
	}
	r.tainted = r.tainted || r.carriesPast()
}

// carriesPast reports whether the statement may change how a shell reads
// the lines after it: when what comes before its value on its line, export
// and any statement before it there included, or its value, once
// withoutSpan takes out a span closed on its line, holds a byte
// carriesState names, or when a shell runs a command there, as
// startsCommand finds.
func (r *envReader) carriesPast() bool {
	if carriesState(r.data[core.LineStart(r.data, r.start):r.opened]) {
		return true
	}
	v := r.withoutSpan(r.data[r.opened:r.pos])
	return carriesState(v) || r.startsCommand(v)
}

// carriesState reports whether text holds a byte that may carry a shell's
// state past a line end, which compose ends the statement at, or stop the
// source there: an open quote, a \ that joins lines, a backtick or $(
// command, a ( or ) out of place, a < or > redirect or heredoc, which is a
// syntax error at a line end, a | or & that continues a pipe or a list,
// and a ; that lets a keyword such as if, or a { group, start mid line.
func carriesState(text []byte) bool {
	return bytes.ContainsAny(text, "'\"\\`()<>$|&;")
}

// withoutSpan is v with its first span, a backtick or $( command or a
// quote, written as the one byte x, when the span closes on the line it
// opens and holds nothing that may reach past its close; else v. A shell
// reads such a span to its close and carries nothing past it: a command
// runs in a subshell, which cannot set the sourcing shell's variables, and
// an assignment does not read its output again. A command that fails
// under set -e, or does not parse, still stops the source; the reference
// says errata reads the file as one whose commands parse and succeed. What
// remains is tested as any value is, so a second span, or a byte that
// carries after the first, still taints.
func (r *envReader) withoutSpan(v []byte) []byte {
	i, ok := spanStart(v)
	if !ok {
		return v
	}
	end, ok := spanEnd(v, i)
	if !ok {
		return v
	}
	r.span = append(append(append(r.span[:0], v[:i]...), 'x'), v[end:]...)
	return r.span
}

// spanStart is the offset of the first ', ", backtick or $ in v, and false
// when there is none or that $ starts no $( command: a $ outside a span
// carries, whatever follows.
func spanStart(v []byte) (int, bool) {
	i := bytes.IndexAny(v, "'\"`$")
	if i < 0 || v[i] == '$' && !bytes.HasPrefix(v[i:], []byte("$(")) {
		return 0, false
	}
	return i, true
}

// spanEnd is the offset past the close of the span at i, on its line, and
// false when it does not close there or holds what may reach past its
// close: a \ that may escape the close, a quote that may open a span of
// its own, what expands names in a " value, and in a backtick command a #
// that may hide a ) or ( and ) that do not balance, since dash parses it
// with the file. $(( is arithmetic, not a command: it runs in the sourcing
// shell and may assign. A ' value holds anything but a line end.
func spanEnd(v []byte, i int) (int, bool) {
	switch v[i] {
	case '\'':
		return closes(v, i+1, '\'', "\n")
	case '"':
		end, ok := closes(v, i+1, '"', "\\`\n")
		return end, ok && !expands(v[i:end])
	case '`':
		end, ok := closes(v, i+1, '`', "\\'\"\n#")
		return end, ok && balanced(v[i:end])
	}
	if bytes.HasPrefix(v[i:], []byte("$((")) {
		return 0, false
	}
	return closesParen(v, i+2)
}

// expands reports whether a " value holds a $(, ${ or $[: a command, a
// quote inside braces that opens one of its own, or bash's arithmetic,
// which runs in the sourcing shell and may assign.
func expands(b []byte) bool {
	for _, x := range []string{"$(", "${", "$["} {
		if bytes.Contains(b, []byte(x)) {
			return true
		}
	}
	return false
}

// balanced reports whether every ) in b closes a ( before it and every (
// is closed.
func balanced(b []byte) bool {
	depth := 0
	for _, c := range b {
		switch c {
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// closes is the offset past the first q in v from start, and false when
// there is none or a byte of stops comes before it.
func closes(v []byte, start int, q byte, stops string) (int, bool) {
	k := bytes.IndexByte(v[start:], q)
	if k < 0 {
		return 0, false
	}
	return start + k + 1, !bytes.ContainsAny(v[start:start+k], stops)
}

// closesParen is the offset past the ) that closes a $( whose text starts
// at start, ( and ) nested, and false when a line end, a quote, a \ or a
// backtick comes first, or the value ends. A # comes first too, since it
// may start a comment that hides the ), and so does a <, since a
// here-document in the command reads its body from the lines after.
func closesParen(v []byte, start int) (int, bool) {
	depth := 1
	for k := start; k < len(v); k++ {
		switch v[k] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return k + 1, true
			}
		case '\n', '\'', '"', '\\', '`', '#', '<':
			return 0, false
		}
	}
	return 0, false
}

// startsCommand reports whether a shell runs a command in the statement,
// whose value is v, which may return, exit or stop the source: a key
// alone, such as return or fi, a statement with no separator, a key with
// white space before its = or :, as in exit =1, which a shell runs with
// the rest as its arguments, and a word after white space following the
// =, as in A=x exit or A= y. export A=1, whose space comes before the key,
// and A=x # c, a comment, run none; nor does A: x, which runs the command
// A: whose arguments change nothing.
func (r *envReader) startsCommand(v []byte) bool {
	if r.sep != '=' && r.sep != ':' {
		return true
	}
	if k := r.data[r.start : r.opened-1]; len(k) > 0 && (k[len(k)-1] == ' ' || k[len(k)-1] == '\t') {
		return true
	}
	if r.sep == ':' {
		return false
	}
	line, _, _ := bytes.Cut(v, []byte("\n"))
	return bytes.ContainsAny(bytes.TrimRight(shellCode(line), " \t\r"), " \t")
}

// shellCode is line up to a shell comment: a # after a space or a tab.
func shellCode(line []byte) []byte {
	for i := 1; i < len(line); i++ {
		if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

// sourcedReads reports whether a shell that sources data and systemd's
// EnvironmentFile= both read the statement at start as compose read it into
// e: the statement is the whole of its line, which no lone CR starts, and
// plainAssignment reads that line into e's key and value. Any other
// statement, such as export A=1, A="a\tb" or A=x y, one of them reads
// otherwise or refuses.
func sourcedReads(data []byte, start int, e *core.Entry) bool {
	ls := core.LineStart(data, start)
	if ls > 0 && data[ls-1] != '\n' {
		return false
	}
	line := bytes.TrimSuffix(data[ls:core.NextLine(data, start)], []byte("\n"))
	key, value, ok := plainAssignment(line)
	return ok && bytes.Equal(key, e.Key) && bytes.Equal(value, e.Value)
}

// plainAssignment reads line as NAME=value, white space before it, where
// NAME is a shell variable's name and value is a word of safe bytes, or one
// in quotes that holds no \, $, `, its own quote or line end: the line all
// three readers read alike.
func plainAssignment(line []byte) (key, value []byte, ok bool) {
	line = bytes.TrimLeft(line, " \t")
	n := shellName(line)
	if n == 0 || n == len(line) || line[n] != '=' {
		return nil, nil, false
	}
	value, ok = plainValue(line[n+1:])
	return line[:n], value, ok
}

// shellName is the length of the shell variable name, [A-Za-z_][A-Za-z0-9_]*,
// at the start of b: 0 for none.
func shellName(b []byte) int {
	for i, c := range b {
		if c != '_' && !isASCIILetter(c) && (i == 0 || !isASCIIDigit(c)) {
			return i
		}
	}
	return len(b)
}

// plainValue is the value of what follows the = of a plain assignment, and
// whether it is plain. White space at the end of an unquoted word is
// dropped, as all three readers drop it; after a closing quote none may
// follow.
func plainValue(rest []byte) ([]byte, bool) {
	if len(rest) == 0 || (rest[0] != '"' && rest[0] != '\'') {
		rest = bytes.TrimRight(rest, " \t")
		return rest, bytes.IndexFunc(rest, notSafe) < 0
	}
	q := rest[0]
	if len(rest) < 2 || rest[len(rest)-1] != q {
		return nil, false
	}
	// A quote of its own kind inside needs no test: compose's value ends
	// there, shorter than inner, so sourcedReads finds the two differ.
	inner := rest[1 : len(rest)-1]
	return inner, !bytes.ContainsAny(inner, "\\$`\r\n\x00")
}

// notSafe reports whether c may read otherwise to a shell, systemd or
// compose in an unquoted word: anything but safeInWord.
func notSafe(c rune) bool { return !strings.ContainsRune(safeInWord, c) }

// safeInWord are the bytes notSafe lets stand in an unquoted word.
const safeInWord = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-./:,+@%="

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isASCIIDigit(c byte) bool { return '0' <= c && c <= '9' }

// readPam reads data as pam_env reads etc/environment (Linux-PAM 1.7,
// _parse_env_file over _pam_line_assemble, built without libeconf), and
// visits every key and value in file order at the line of its key:
//
//   - a line ends at \n alone; space and tab before a statement are
//     skipped, and # anywhere starts a comment to the end of the line;
//   - export and one space before a key are dropped;
//   - the key is ASCII letters, digits and _ up to the first =; a line
//     whose key holds anything else, or that starts with =, is skipped;
//   - a key alone asks pam_env to unset the variable: an empty computed
//     value;
//   - the value runs to the comment or the end of the line, white space
//     at its end kept, and nothing in it is substituted or escaped; a value
//     that starts with ' or " loses that quote, and its last byte when that
//     is ' or ";
//   - a line that a \ ends, before space and tab, and that holds no #,
//     joins the next. The join, which Linux-PAM releases have changed, is
//     not modelled: the value is the first line as written, marked
//     computed, and so is one that holds a NUL byte, where C cuts it.
func readPam(data []byte, m *core.Meter, t *marks, visit func(*core.Entry) error) error {
	lines := core.NewLineCursor(data)
	var e core.Entry
	for pos := 0; pos < len(data); {
		end, joined := pamStatement(data, pos)
		if err := m.Charge(core.LineWork + end - pos); err != nil {
			return err
		}
		line, _, _ := bytes.Cut(data[pos:end], []byte("\n"))
		off, raw, ok := pamEntry(line, &e)
		if ok {
			e.Line = lines.At(pos + off)
			if err := pamMark(&e, raw, t, joined || bytes.ContainsRune(line, 0)); err != nil {
				return err
			}
			if err := visit(&e); err != nil {
				return err
			}
		}
		pos = end
	}
	return nil
}

// pamStatement is the end of the statement at pos, its \n included, and
// whether it joins more than one line: a line joins the next when it holds
// no # and a \ ends it, before space, tab and its \n.
func pamStatement(data []byte, pos int) (end int, joined bool) {
	for {
		end = len(data)
		if i := bytes.IndexByte(data[pos:], '\n'); i >= 0 {
			end = pos + i + 1
		}
		line := data[pos:end]
		if bytes.IndexByte(line, '#') >= 0 || !bytes.HasSuffix(bytes.TrimRight(line, " \t\n"), []byte(`\`)) {
			return end, joined
		}
		// A \ on the last line is uncertain too: Linux-PAM replaces it
		// with a NUL, which ends the value before it.
		if joined = true; end == len(data) {
			return end, joined
		}
		pos = end
	}
}

// pamEntry reads line, a statement's first line without its \n, into e's
// key and value as pam_env does, and returns the key's offset in line, the
// value as written, and false for a line pam_env skips: a blank line, a
// comment, a line that starts with =, a key with a byte other than an ASCII
// letter, a digit or _, and a key alone that is empty.
func pamEntry(line []byte, e *core.Entry) (off int, raw []byte, ok bool) {
	line, _, _ = bytes.Cut(line, []byte("#"))
	s := bytes.TrimLeft(line, " \t")
	s = bytes.TrimPrefix(s, []byte("export "))
	off = len(line) - len(s)
	n := pamKey(s)
	switch {
	case n == len(s):
		e.Key, e.Value, e.Computed = s, nil, true
		return off, nil, n > 0
	case n == 0 || s[n] != '=':
		return 0, nil, false
	}
	raw = s[n+1:]
	e.Key, e.Value, e.Computed = s[:n], unquotePam(raw), false
	return off, raw, true
}

// pamKey is the length of the run of ASCII letters, digits and _ at the
// start of s.
func pamKey(s []byte) int {
	for i, c := range s {
		if c != '_' && !isASCIILetter(c) && !isASCIIDigit(c) {
			return i
		}
	}
	return len(s)
}

// unquotePam is v as pam_env takes its quotes off: a v that starts with '
// or " loses that byte, and its last byte when that is ' or "; any other
// quote stays.
func unquotePam(v []byte) []byte {
	if len(v) == 0 || (v[0] != '"' && v[0] != '\'') {
		return v
	}
	v = v[1:]
	if n := len(v); n > 0 && (v[n-1] == '"' || v[n-1] == '\'') {
		v = v[:n-1]
	}
	return v
}

// pamMark returns e's value as written, raw, and marks it computed when
// the statement is uncertain, joining lines or holding a NUL byte, or when
// t marks it in a template.
func pamMark(e *core.Entry, raw []byte, t *marks, uncertain bool) error {
	not, err := t.notLiteral(raw, e.Line)
	if (uncertain || not) && !e.Computed {
		e.Value, e.Computed = raw, true
	}
	return err
}
