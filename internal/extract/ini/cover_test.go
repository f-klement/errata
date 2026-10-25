package ini

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// What PHP 8.4.26's parse_ini_string made of each body, run in php:8.4-cli:
// an indented key, a section named by a number, a file that ends in a
// comment with no line break after it, a line of blanks between two keys,
// and an offset with a blank before its ]. The planted break is reading a section's number with the bytes
// before it, which panics.
func TestPHPReadsTheEdgesOfAFile(t *testing.T) {
	for _, c := range []struct {
		name, body, section, key, want string
	}{
		{"an indented key", " key = 1\n", "", "key", "1@1"},
		{"a section named by a number", "[1]\na = x\n", "1", "a", "x@2"},
		{"a comment at the end with no line break", "a = 1\n; c", "", "a", "1@1"},
		{"a comment after the value at the end", "a = 1 ;c", "", "a", "1@1"},
		{"a line of blanks", "a = 1\n \t\nb = 2\n", "", "b", "2@3"},
		{"an offset with a blank before ]", "a[x ] = 1\n", "", "a", "1@1"},
	} {
		if got := iniValues(t, "php.ini", c.body, ini("php.ini", c.section, c.key)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Each body here is one parse_ini_string refuses (false in PHP 8.4.26): a
// reference the file ends inside, a $\ the file ends on, inside a word and
// at the start of a value, and a reference with no name. The planted break
// is cutting the name of a reference the file ends inside before its first
// byte, which panics instead of refusing.
func TestPHPRefusesAValueTheFileEndsInside(t *testing.T) {
	for _, body := range []string{"a = ${abc", "a = x$\\", "a = /$\\", "a = ${;x}"} {
		got := iniError(t, "php.ini", body, ini("php.ini", "", "a"))
		if want := "php.ini: line 1: PHP refuses this line: " + core.Quote(body); got != want {
			t.Errorf("%q: error %q, want %q", body, got, want)
		}
	}
}

// A line of blanks is one end of line token, as PHP's scanner reads
// {TABS_AND_SPACES}*{NEWLINE}, not blanks skipped and then the line break.
// The read of a file is the same either way; the work meter, which charges
// each token, is not. The planted break is the line break's length taken
// from the blanks', which skips them and reads the break alone.
func TestPHPLexerReadsABlankLineAsOneToken(t *testing.T) {
	l := &phpLexer{data: []byte(" \t\n")}
	l.next()
	if l.tok != phpEOL || l.start != 0 || l.end != 3 {
		t.Errorf("token %d from %d to %d, want an end of line from 0 to 3", l.tok, l.start, l.end)
	}
}

// run stops at the end of the file whatever its predicate takes, the NUL
// at gives past the end included, and takes the bytes the predicate takes
// before it. The planted break is the bound dropped from run's loop, which
// reads past the end and panics on the second case.
func TestRunStopsAtTheEnd(t *testing.T) {
	for _, c := range []struct {
		data string
		ok   func(byte) bool
		want int
	}{
		{"a \t\tb", isPHPBlank, 3},
		{"=abc", isPHPLabel, 3},
		{"=a\x00c", func(byte) bool { return true }, 3},
	} {
		l := &phpLexer{data: []byte(c.data)}
		if got := l.run(1, c.ok); got != c.want {
			t.Errorf("run over %q from 1 takes %d bytes, want %d", c.data, got, c.want)
		}
	}
}

// In the initial state no rule matches a NUL, and the lexer ends the file
// there; readPHP refuses a file that holds one before the lexer sees it, so
// only a lexer made here reaches it. The planted break is rule < 0 dropped,
// which leaves the lexer at the NUL with no token.
func TestPHPLexerEndsAtAByteNoRuleMatches(t *testing.T) {
	l := &phpLexer{data: []byte("\x00a")}
	l.next()
	if l.tok != phpEnd || l.pos != 3 {
		t.Errorf("token %d with the lexer at %d, want the end at 3", l.tok, l.pos)
	}
}

// Python's configparser reads a last line with no line break after it as it
// reads any other, so an entry there is found. The planted break is the
// line reader stopping, with no error, at the last line's charge.
func TestPythonDialectsReadALastLineWithoutALineBreak(t *testing.T) {
	for _, file := range []string{"setup.cfg", "tox.ini"} {
		if got, want := iniValues(t, file, "[s]\nk = 1", ini(file, "s", "k")), "1@2"; got != want {
			t.Errorf("%s: got %q, want %q", file, got, want)
		}
	}
}

// Blanks and then ] in an offset are one closing bracket token, as PHP's
// scanner reads {TABS_AND_SPACES}*"]" in ST_OFFSET, not blanks skipped and
// then the bracket. As with a blank line, the read of a file is the same
// either way and the work meter's charge is not. The planted break is the
// token's length one short of the blanks and the bracket, which leaves the
// blanks to the blank rule.
func TestPHPLexerReadsBlanksAndAnOffsetEndAsOneToken(t *testing.T) {
	l := &phpLexer{data: []byte(" \t]x"), state: phpOffsetValue}
	l.next()
	if l.tok != phpRBracket || l.start != 0 || l.end != 3 || l.state != phpInitial {
		t.Errorf("token %d from %d to %d in state %d, want a closing bracket from 0 to 3 in the initial state", l.tok, l.start, l.end, l.state)
	}
}

// Python writes 0o, 0b and the other prefixed integers in decimal, so
// ansible's value is not the text and errata marks it. Each case is
// ast.literal_eval in Python 3.13: 8, 15 and 2. The planted break is octal
// digits that stop before 0, which reads 0o10 as no literal.
func TestAnsibleMarksPrefixedIntegers(t *testing.T) {
	spec := Spec{File: "hosts", Section: "g:vars", Key: "a"}
	if got, want := notLiterals(t, "hosts", "[g:vars]\na=0o10\na=0o17\na=0b10\n", spec), "0o10!@2 0o17!@3 0b10!@4"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// literal_eval refuses each of these with a SyntaxError (Python 3.13): an
// imaginary number run into a letter, bytes beside text, and bytes that
// are not ASCII. ansible keeps the text written, which errata returns
// unmarked. The planted break is taking bytes beside text as one string,
// which reads b'a' 'b' as ab.
func TestAnsibleKeepsWhatLiteralEvalRefuses(t *testing.T) {
	spec := Spec{File: "hosts", Section: "g:vars", Key: "a"}
	body := "[g:vars]\na=1+2jx\na=b'a' 'b'\na=b'é'\n"
	if got, want := notLiterals(t, "hosts", body, spec), "1+2jx@2 b'a' 'b'@3 b'é'@4"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A step down over bounds that take the big.Int path (1_0 is no plain
// integer to smallInt, and is 10 to Python's int()): range(10, 11, -1) and
// range(11, 11, -1) hold no host, so their lines read none, and
// range(10, 2, -1) holds eight. Each is Python 3.13's range. The planted
// break is b < e for the empty step down, which reads h[1_0:1_0:-1].
func TestAnsibleEmptyStepDownPastInt64Path(t *testing.T) {
	body := "[g]\nh[1_0:1_0:-1]\nh[1_1:1_0:-1]\nh[1_0:1:-1]\nh2\n"
	if got, want := iniValues(t, "hosts", body, Spec{File: "hosts", Section: "g"}), "h[1_0:1:-1]@4 h2@5"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A host is one of a pattern's hosts as Python 3.13 expands it, on the
// int64 path and on the big.Int path, for steps down and negative values:
// h[1_0:1:-4] is h10 and h6, h[000:-05:-0_1] is h000, h-01, h-02 and h-03
// (zfill keeps the sign first), and h[-5:-1] is h-5 to h-1. The planted
// breaks are a step down that takes its begin as above it, zfill that
// pads a negative value to the wrong width, and a negative value read
// without its sign.
func TestHostInStepsDownAndNegatives(t *testing.T) {
	for pattern, names := range map[string]map[string]bool{
		"h[1_0:1:-4]":     {"h10": true, "h6": true, "h2": false, "h8": false, "h11": false, "h1": false},
		"h[000:-05:-0_1]": {"h000": true, "h-01": true, "h-03": true, "h-1": false, "h-04": false, "h0-01": false},
		"h[-5:-1]":        {"h-5": true, "h-3": true, "h-1": true, "h-6": false, "h-0": false, "h3": false},
	} {
		for name, want := range names {
			if got, err := hostIn([]byte(pattern), []byte(name), nil); got != want || err != nil {
				t.Errorf("%s in %s: %v, %v, want %v", name, pattern, got, err, want)
			}
		}
	}
}
