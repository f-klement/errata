package ini

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// What a scoped mutation run of this package found that no test pinned:
// the work meter's charges, which bound what one hostile file can cost,
// and the edges of the lexers' byte classes. Each charge test states the
// sum the code charges for its input, part by part, so a charge that
// changes by a unit fails it.

// parseWork is n up to the 18 digits int64 holds, and 32 a digit past
// them. The planted break is 18 digits charged as big.Int's.
func TestParseWorkAtTheDigitsInt64Holds(t *testing.T) {
	if got := parseWork(18); got != 18 {
		t.Errorf("18 digits: %d, want 18", got)
	}
	if got := parseWork(19); got != 32*19 {
		t.Errorf("19 digits: %d, want %d", got, 32*19)
	}
}

// checkRanges charges each range rangeWork, its bytes from where the walk
// stood to its ], and the parse of what the brackets hold: ab[1:3] is
// 256+6+4 and cd[x:y] after it 256+6+4 again, counted from cd, not from
// the line's start; a 20 digit bound is 32 a digit. The planted breaks
// are the range's bytes counted from the line's start, and the brackets'
// width taken as the sum of their offsets.
func TestCheckRangesChargesEachRangeExactly(t *testing.T) {
	for line, want := range map[string]int64{
		"ab[1:3]cd[x:y]":            2 * (rangeWork + 6 + 4),
		"h[0:99999999999999999999]": rangeWork + 24 + 32*23,
	} {
		m := &core.Meter{Budget: 1 << 40}
		if _, err := checkRanges(line, m); err != nil {
			t.Fatal(err)
		}
		if m.Spent != want {
			t.Errorf("%s: %d units, want %d", line, m.Spent, want)
		}
	}
}

// hostIn charges the name's length and 1, each range as checkRanges does,
// each position a literal is compared at (positionWork and the literal),
// each value a range tries (digitWork a digit and 1 for a small range,
// bigDigitWork for a big.Int one), and the tail at each position reached.
// h[1:3] against h2 is 3 + (256+5+4) + (8+1) + 16*2 + 8 = 317; with a tail
// x, 4 + 265 + 9 + 32 + 9 = 319. The longer figures are the same sum,
// pinned as measured. A letter range from c down to a holds no host and
// stops after its own charge. The planted breaks are each charge with
// its arithmetic turned, and a range compared the wrong way.
func TestHostInChargesExactly(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		in            bool
		want          int64
	}{
		{"h[1:3]", "h2", true, 3 + (rangeWork + 5 + 4) + (positionWork + 1) + digitWork*2 + positionWork},
		{"h[1:3]x", "h2x", true, 4 + (rangeWork + 5 + 4) + (positionWork + 1) + digitWork*2 + positionWork + 1},
		{"a[01:10:3]b[x:z]c", "a07byc", true, 636},
		{"h[-5:-1]", "h-3", true, 322},
		{"h[99999999999999999998:99999999999999999999]", "h99999999999999999999", true, 11762},
		{"h[c:a]", "hb", false, 268},
	} {
		m := &core.Meter{Budget: 1 << 40}
		in, err := hostIn([]byte(c.pattern), []byte(c.name), m)
		if err != nil || in != c.in || m.Spent != c.want {
			t.Errorf("%s against %s: %v, %v, %d units; want %v, %d units", c.pattern, c.name, in, err, m.Spent, c.in, c.want)
		}
	}
}

// An octal escape is one to three digits 0 to 7, as Python reads it:
// \101 is A whatever follows, / and 8 beside the digits end it, and \17
// takes the 7. The planted breaks are each end of the digit range and a
// fourth digit taken.
func TestPythonOctalEscapesTakeOneToThreeDigits(t *testing.T) {
	for v, want := range map[string]string{
		`'\101'`: "A", `'\1012'`: "A2", `'\1234'`: "S4", `'\10/'`: "\b/", `'\17'`: "\x0f", `'\18'`: "\x018", `b'\101'`: "A",
	} {
		got, _, err := pyValue([]byte(v))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", v, got, err, want)
		}
	}
}

// A PHP constant name starts with _ or a capital, A and Z included, and
// holds a _; @ and [ beside the capitals start none. The planted breaks
// are each end of the capitals.
func TestPHPConstantNamesStartWithACapitalOrUnderscore(t *testing.T) {
	for b, want := range map[string]bool{"A_B": true, "Z_B": true, "_A": true, "E_ALL": true, "@_A": false, "[_A": false, "A": false} {
		if got := isConstantName([]byte(b)); got != want {
			t.Errorf("%s: %v, want %v", b, got, want)
		}
	}
}

// number is PHP's [-]?[0-9]+ or [0-9]*.[0-9]+ or [0-9]+.[0-9]*, the
// longest; word is a boolean word and the blanks after it. The planted
// breaks are the sign, the point and either run of digits read wrong, and
// a word's first letter left out of the quick test.
func TestPHPNumbersAndWordsTakeTheirLongestMatch(t *testing.T) {
	for s, want := range map[string]int{"1": 1, "-1": 2, "-": 0, "1.": 2, ".5": 2, "1.5": 3, ".": 0, "-.5": 0, "12a": 2, "-12.5": 3} {
		if got := (&phpLexer{data: []byte(s)}).number(0); got != want {
			t.Errorf("number %q: %d, want %d", s, got, want)
		}
	}
	for _, c := range []struct {
		s    string
		want int
	}{{"yes", 3}, {"no", 2}, {"none", 4}, {"false", 5}, {"off", 3}, {"on", 2}, {"true", 4}, {"null", 4}, {"Yes ", 4}, {"x", 0}} {
		if got := (&phpLexer{data: []byte(c.s)}).word(0, "true", "on", "yes", "false", "off", "no", "none", "null"); got != c.want {
			t.Errorf("word %q: %d, want %d", c.s, got, c.want)
		}
	}
}

// Python's white space is \t to \r, the four separators U+001C to U+001F,
// the space, and Unicode's spaces such as U+0085 and U+00A0; U+001B and
// U+0008 are not. The planted breaks are each end of either range.
func TestPythonWhiteSpaceIsWhatStrStripTakes(t *testing.T) {
	space := map[rune]bool{'\t': true, '\n': true, '\v': true, '\f': true, '\r': true, 0x1c: true, 0x1d: true, 0x1e: true, 0x1f: true, ' ': true, 0x85: true, 0xa0: true}
	for r := rune(0); r < 0x100; r++ {
		if got := pySpace(r); got != space[r] {
			t.Errorf("pySpace(%U): %v", r, got)
		}
		if r < 0x80 {
			if got := asciiSpace(byte(r)); got != space[r] {
				t.Errorf("asciiSpace(%#x): %v", r, got)
			}
		}
	}
}

// A host is in a range as Python's range(begin, end+1, step) holds it,
// which ansible expands: h[b:d] is hb to hd, h[a:e:2] ha, hc and he,
// h[10:1:-4] h10 and h6, h[1:9:4] h1, h5 and h9, and a range of 20 digit
// bounds stepping down by 3 its begin, begin-3 and begin-6. The planted
// breaks are each bound of each kind of range taken one off, in or out.
func TestHostInHoldsWhatPythonsRangeHolds(t *testing.T) {
	big := "h[99999999999999999999:99999999999999999990:-3]"
	for pattern, names := range map[string]map[string]bool{
		"h[b:d]":     {"hb": true, "hc": true, "hd": true, "ha": false, "he": false},
		"h[a:e:2]":   {"ha": true, "hc": true, "he": true, "hb": false, "hd": false, "hf": false},
		"h[10:1:-4]": {"h10": true, "h6": true, "h2": false, "h3": false, "h11": false, "h9": false, "h14": false},
		"h[1:9:4]":   {"h1": true, "h5": true, "h9": true, "h0": false, "h3": false, "h13": false},
		big: {"h99999999999999999999": true, "h99999999999999999996": true, "h99999999999999999993": true,
			"h99999999999999999990": false, "h99999999999999999998": false, "h100000000000000000002": false},
	} {
		for name, want := range names {
			if got, err := hostIn([]byte(pattern), []byte(name), nil); got != want || err != nil {
				t.Errorf("%s in %s: %v, %v, want %v", name, pattern, got, err, want)
			}
		}
	}
}

// smallInt reads 1 to 17 digits, with a sign, and leaves the rest to
// big.Int. The planted breaks are 17 digits refused, 18 taken, and an
// empty bound read as 0.
func TestSmallIntTakesOneToSeventeenDigits(t *testing.T) {
	for s, want := range map[string]bool{"7": true, "-7": true, "12345678901234567": true, "-12345678901234567": true,
		"123456789012345678": false, "": false, "-": false, "1a": false} {
		if _, ok := smallInt(s); ok != want {
			t.Errorf("smallInt(%q): %v, want %v", s, ok, want)
		}
	}
}

// pyNumberLen is the length of Python's number token: 0x, 0o and 0b
// integers and their digits, a float with digits on either side of its
// point, an exponent with its sign, a j after any of them, and a decimal
// integer that starts with 0 taking only zeros. The planted breaks are the
// hex letters' ends, a prefix with no digit after it read as one, a point
// with no digit on either side read as a float, and the exponent's sign
// and digits read wrong.
func TestPyNumberLenIsPythonsToken(t *testing.T) {
	for s, want := range map[string]int{
		"0x1F": 4, "0xa": 3, "0xf": 3, "0xA": 3, "0xg": 1, "0x`": 1, "0o17": 4, "0o8": 1, "0b101": 5, "0b2": 1,
		"123": 3, "1.5": 3, "1.": 2, ".5": 2, ".": 0, "1e5": 3, "1e+5": 4, "1e-5": 4, "1e": 1, "1e+": 1, "1E5": 3,
		"1.5e3": 5, "1j": 2, "1.5J": 4, "0": 1, "00": 2, "007": 2, "1_000": 5, "1__0": 1, "x": 0,
	} {
		if got := pyNumberLen([]byte(s)); got != want {
			t.Errorf("pyNumberLen(%q): %d, want %d", s, got, want)
		}
	}
}

// In a value, a run of bytes is a number when number matches all of it, a
// boolean word when the word and its blanks are longest, and a constant
// otherwise; a boolean's text drops the blanks. The planted breaks are a
// tie lost by the later rule, and a boolean's text with its blanks.
func TestPHPValueWordsTakeTheirKind(t *testing.T) {
	for _, c := range []struct {
		s    string
		tok  phpTok
		text string
	}{
		{"1\n", phpNumber, "1"}, {"-1\n", phpNumber, "-1"}, {"1.5\n", phpNumber, "1.5"}, {"1a\n", phpString, "1a"},
		{"true \n", phpBool, "true"}, {"on\n", phpBool, "on"}, {"FOO\n", phpConstant, "FOO"}, {"a.b\n", phpString, "a.b"},
	} {
		l := &phpLexer{data: []byte(c.s), state: phpValue}
		l.next()
		if l.tok != c.tok || string(l.text) != c.text {
			t.Errorf("%q: token %d %q, want %d %q", c.s, l.tok, l.text, c.tok, c.text)
		}
	}
}

// Each line costs LineWork and its bytes with its line break, and what
// follows the last break costs LineWork and its bytes: ab\ncd is 67 + 66,
// and a \r\n is charged at its \r. eachPyLine breaks at U+2028 too, three
// bytes. The planted breaks are each line's bytes, and the last line's,
// counted with the sign turned.
func TestLineWalkersChargeEachLineExactly(t *testing.T) {
	noop := func(int, []byte) error { return nil }
	for s, want := range map[string]int64{"ab\ncd": 67 + 66, "ab\ncd\n": 67 + 67 + 64, "ab\r\ncd": 67 + 66} {
		m := &core.Meter{Budget: 1 << 40}
		if err := eachTextLine([]byte(s), m, noop); err != nil || m.Spent != want {
			t.Errorf("eachTextLine %q: %d units, %v; want %d", s, m.Spent, err, want)
		}
	}
	for s, want := range map[string]int64{"ab\ncd\u2028e": 67 + 69 + 65, "ab\r\ncd\u2028e": 68 + 69 + 65} {
		m := &core.Meter{Budget: 1 << 40}
		if err := eachPyLine([]byte(s), m, noop); err != nil || m.Spent != want {
			t.Errorf("eachPyLine %q: %d units, %v; want %d", s, m.Spent, err, want)
		}
	}
}

// A longer name costs its bytes once in each place that reads it: tox
// reads [section] and key = 1 as lines, as map entries (mapWork and the
// name) and through the matcher (the query's section and key), so 6 more
// bytes of section and 2 of key cost 3 times 8; an ansible host line is
// charged 3 times its bytes for the split, the range walk and pyValue, and
// once as a line, so a host 3 bytes longer costs 12. The planted breaks
// are each name charge with its sign turned, and the host line charged
// once.
func TestNamesCostTheirBytesOnceAPlace(t *testing.T) {
	spent := func(body string, s Spec) int64 {
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		return coretest.Spent(t, e.(core.EntryExtractor), []byte(body))
	}
	if d := spent("[section]\nkey = 1\n", ini("tox.ini", "section", "key")) - spent("[s]\nk = 1\n", ini("tox.ini", "s", "k")); d != 3*8 {
		t.Errorf("tox: 8 bytes more of names cost %d units, want %d", d, 3*8)
	}
	hosts := Spec{File: "hosts", Section: "g", Key: "k"}
	if d := spent("[g]\nhost k=1\n", hosts) - spent("[g]\nh k=1\n", hosts); d != 3*3+3 {
		t.Errorf("ansible: a host 3 bytes longer costs %d units, want %d", d, 3*3+3)
	}
}

// What ast.literal_eval takes in a value, the rest being the text written
// with its comment: a dict, a set, an empty dict with a trailing comma
// allowed, but no mix of the two nor a pair with no key; set() and no other
// call; two prefix letters to a string, rb or Rb, not bu; \x with two hex
// digits, \U up to U+10FFFF, and \N only as \N{name}. A string literal is
// its value; another literal is its text without the comment. The planted
// breaks are a dict and a set mixed, a prefix of any two letters, the
// edges of \x, \U and \N read one off.
func TestPythonLiteralsAsLiteralEvalTakesThem(t *testing.T) {
	for v, want := range map[string]string{
		"{1: 2} # c": "{1: 2}", "{1, 2} # c": "{1, 2}", "{} # c": "{}", "{1: 2,} # c": "{1: 2,}", "set() # c": "set()",
		"{1: 2, 3} # c": "{1: 2, 3} # c", "{1, 2: 3} # c": "{1, 2: 3} # c", "{:1} # c": "{:1} # c", "{1:2:3} # c": "{1:2:3} # c",
		`rb'\x'`: `\x`, `Rb'a'`: "a", `bu'x'`: `bu'x'`, `'\x41'`: "A", `'\x4'`: `'\x4'`,
		`'\U0010ffff'`: "\U0010ffff", `'\U00110000'`: `'\U00110000'`, `'\N'`: `'\N'`, `'\N{x'`: `'\N{x'`,
	} {
		got, _, err := pyValue([]byte(v))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", v, got, err, want)
		}
	}
	if _, _, err := pyValue([]byte(`'\N{DASH}'`)); err == nil {
		t.Errorf(`'\N{DASH}': no error, want the refusal of a string errata cannot decode`)
	}
}
