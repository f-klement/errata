package rule

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"sync"
	"unicode/utf8"
)

// A regex entry runs through four steps in Go's regexp: parse, simplify,
// compile and match. None of them stops early, and CEL charges a call only
// once it returns, so each step is bounded before it runs, by what the one
// before it gives:
//
//   - The parse, by the regex's text alone (scanRegex): no \Q, at most
//     maxRegexText bytes, maxRegexNamed Unicode classes, and under case
//     folding, ranges over at most maxRegexFolded code points. Go folds a
//     class range under (?i) one code point at a time, so (?i)[a-\x{10FFFF}]
//     84 times took 0.79 s to parse, and merges the ranges of each \pL, so
//     [^\pL] with 339 of them took 57 ms.
//   - The compile, by the program the parsed tree would make (programSize),
//     and again by the program it made: at most maxRegexInsts.
//   - The match, by the cost: the string's bytes times the program's
//     instructions over renovateRegexDivisor (renovateCallCost).
//
// The entries of k3s's renovate.json are at most 43 bytes between the
// slashes and errata's at most 20; (^|/)values\.ya?ml$ is 20 instructions.
const (
	maxRegexText   = 256
	maxRegexNamed  = 16
	maxRegexFolded = 4096
	maxRegexInsts  = 4096
)

// renovateRegexDivisor scales a regex call's string bytes times its
// program's instructions into its cost. RE2 matches in time proportional
// to the two. The slowest rate measured through the CI runner,
// (?:\pL*){680}b on its 4 core node, was 28.7 ns for each byte times
// instruction, so at 160 the cost limit pays for about 4.6 s of matching,
// and a call at maxRegexInsts against 1024 bytes takes about 120 ms.
const renovateRegexDivisor = 160

// errRegexBound is a regex refused by one of the bounds above, before the
// step it bounds runs.
type errRegexBound struct{ msg string }

func (e errRegexBound) Error() string { return e.msg }

// regexEntry is one regex entry compiled: its program, for the cost, and
// the matcher, or why it is refused.
type regexEntry struct {
	prog *syntax.Prog
	re   *regexp.Regexp
	err  error
}

// regexCache holds the entry of the call that just returned, which the
// cost tracker reads, so a call parses its regex once in errata, unless
// another evaluation runs at the same time and takes the entry first, and
// a call of the same pattern as the one before parses nothing. It holds no
// more: regexp.Compile gives an anchored regex under 1000 instructions a
// onepass program, which copies each instruction's runes, and
// (?i)^(?:[^\pL\pN]){990}$, 26 bytes, held 9.3 MiB, so 256 entries held
// 2.39 GiB.
var regexCache = struct {
	sync.Mutex
	entries map[string]regexEntry
}{entries: map[string]regexEntry{}}

const regexCacheSize = 1

// compiledRegex is the entry for pattern, a regex between slashes, from the
// cache or compiled now, and then the one the cache holds.
func compiledRegex(pattern string) regexEntry {
	regexCache.Lock()
	e, ok := regexCache.entries[pattern]
	regexCache.Unlock()
	if ok {
		return e
	}
	e = compileRegex(pattern)
	regexCache.Lock()
	if len(regexCache.entries) >= regexCacheSize {
		clear(regexCache.entries)
	}
	regexCache.entries[pattern] = e
	regexCache.Unlock()
	return e
}

// compileRegex compiles a regex entry as Renovate reads it, with each step
// bounded before it runs. regexp.Compile takes no parsed tree, so a regex
// that passes every bound is parsed once more inside it, within the same
// bound on its text.
func compileRegex(pattern string) regexEntry {
	// Renovate strips the start, then the end of what is left, so "/" is
	// the empty regex and "/i" the regex i with the i flag.
	body := regexEnd.ReplaceAllString(regexStart.ReplaceAllString(pattern, ""), "")
	if err := scanRegex(body, strings.HasSuffix(pattern, "i")); err != nil {
		return regexEntry{err: err}
	}
	if strings.HasSuffix(pattern, "i") {
		body = "(?i)" + body
	}
	parsed, err := syntax.Parse(body, syntax.Perl)
	if err != nil {
		return regexEntry{err: err}
	}
	if n := programSize(parsed); n > maxRegexInsts {
		return regexEntry{err: errRegexBound{fmt.Sprintf("its RE2 program would have about %d instructions; errata matches at most %d", n, maxRegexInsts)}}
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return regexEntry{err: err}
	}
	if len(prog.Inst) > maxRegexInsts {
		return regexEntry{err: errRegexBound{fmt.Sprintf("its RE2 program has %d instructions; errata matches at most %d", len(prog.Inst), maxRegexInsts)}}
	}
	re, err := regexp.Compile(body)
	if err != nil {
		return regexEntry{err: err}
	}
	return regexEntry{prog: prog, re: re}
}

// The code points Go's parser folds one at a time in a class range under
// case folding: minFold and maxFold in regexp/syntax.
const (
	minFold = 0x0041
	maxFold = 0x1E943
)

// foldFlag finds a flag group that may set i, such as (?i) or (?si:. It
// finds more than Go would read as one, such as \(?i, which only holds the
// regex's ranges to maxRegexFolded where it did not need to.
var foldFlag = regexp.MustCompile(`\(\?[A-Za-z-]*i`)

// scanRegex bounds what Go's parser can be made to do with body, from the
// text alone: its length, the Unicode classes it names, and under case
// folding, the code points its ranges cover between minFold and maxFold,
// where the parser folds each one.
func scanRegex(body string, iflag bool) error {
	if len(body) > maxRegexText {
		return errRegexBound{fmt.Sprintf("its text is %d bytes; errata reads a regex of at most %d", len(body), maxRegexText)}
	}
	named, folded, refused := regexScan(body)
	switch {
	case refused == `\Q`:
		return errRegexBound{`it quotes text with \Q, which errata does not read in a regex`}
	case refused != "":
		return errRegexBound{fmt.Sprintf("its escape %q is malformed", refused)}
	case named > maxRegexNamed:
		return errRegexBound{fmt.Sprintf("it names %d Unicode classes; errata reads at most %d", named, maxRegexNamed)}
	case folded > maxRegexFolded && (iflag || foldFlag.MatchString(body)):
		return errRegexBound{fmt.Sprintf("its ranges fold case over %d code points; errata folds at most %d", folded, maxRegexFolded)}
	}
	return nil
}

// regexScan reads body in one pass, a token at a time, and counts the
// Unicode classes it names and the code points its ranges cover between
// minFold and maxFold. A token is a character, an escape that stands for
// one, or a class escape. Each - that stands between two characters is
// counted as a range of the two, and counts as a character for the
// triple before it: Go reads [--H] as the range - to H, and every range
// Go reads in a class is such a triple. The scan reads only what it can
// read as Go does: it stops at \Q, after which Go reads the text as
// literals to \E, and at a \x{ or \p{ that Go would refuse, and returns
// the token as refused. TestRegexScanNeverCountsFewerThanGoFolds and
// FuzzRegexScan hold its count to Go's own parse over generated
// patterns. A single character, or a POSIX or Perl class such as \w, Go
// folds in at most 63 code points, which maxRegexText holds.
func regexScan(body string) (named int, folded int64, refused string) {
	// The two tokens before this one: a character's code point, whether
	// it is a character at all, and whether the last is a plain -.
	var twoBack, oneBack rune
	twoChar, oneChar, oneDash := false, false, false
	for i := 0; i < len(body); {
		c, n, class := regexToken(body[i:])
		if class == 'q' || class == 'm' {
			return named, folded, body[i : i+n]
		}
		// Every token is at least a byte, so no input needs the max. It is
		// there so a mutant that makes a token empty fails its tests
		// instead of looping past the mutation gate's limit (gate-06eae1e).
		i += max(n, 1)
		if class == 'p' {
			named++
		}
		char := class == 0
		if char && oneDash && twoChar {
			folded += foldedSpan(twoBack, c)
		}
		twoBack, twoChar = oneBack, oneChar
		oneBack, oneChar, oneDash = c, char, char && c == '-' && n == 1
	}
	return named, folded, ""
}

// foldedSpan is how many code points of the range lo-hi Go's parser folds
// one at a time: those between minFold and maxFold, or none when the range
// covers them all.
func foldedSpan(lo, hi rune) int64 {
	if lo <= minFold && hi >= maxFold {
		return 0
	}
	lo, hi = max(lo, minFold), min(hi, maxFold)
	return max(int64(hi)-int64(lo)+1, 0)
}

// regexToken reads one token at the start of s: the character it stands
// for and its length, or a class escape, with class 'p' for \p and \P,
// which name a Unicode class, and 'c' for \d, \s, \w and their negations.
// It gives 'q' for \Q, and 'm' for a \x{ or \p{ that does not close with
// } after hex digits or the bytes of a class's name, ending the token
// there. Go refuses both: it reads \x{ only to a } after hex digits, and
// \p{ to the next }, whose name then holds a byte no class's name has.
// An escaped letter that names no class, such as \t, or \0 and the
// octal digits after it, stands for a code point below minFold, which a
// range treats as 0 does.
func regexToken(s string) (r rune, n int, class byte) {
	if s[0] != '\\' || len(s) == 1 {
		r, n = utf8.DecodeRuneInString(s)
		return r, n, 0
	}
	switch e := s[1]; {
	case e == 'p' || e == 'P':
		if len(s) > 2 && s[2] == '{' {
			k := 3 + leading(s[3:], isClassName)
			if k > 3 && k < len(s) && s[k] == '}' {
				return 0, k + 1, 'p'
			}
			return 0, k, 'm'
		}
		return 0, min(3, len(s)), 'p'
	case e == 'Q':
		return 0, 2, 'q'
	case strings.IndexByte("dDsSwW", e) >= 0:
		return 0, 2, 'c'
	case e == 'x' && len(s) > 2 && s[2] == '{':
		k := 3 + leading(s[3:], isHexDigit)
		if k > 3 && k < len(s) && s[k] == '}' {
			return digitsValue(s[3:k], 16), k + 1, 0
		}
		return 0, k, 'm'
	case e == 'x':
		k := 2 + leading(s[2:min(4, len(s))], isHexDigit)
		return digitsValue(s[2:k], 16), k, 0
	case e >= '1' && e <= '7':
		k := 1 + leading(s[1:min(4, len(s))], isOctalDigit)
		return digitsValue(s[1:k], 8), k, 0
	case e < utf8.RuneSelf && digitValue(e, 36) == -1:
		// Punctuation stands for itself.
		return rune(e), 2, 0
	}
	_, size := utf8.DecodeRuneInString(s[1:])
	return 0, 1 + size, 0
}

// leading is how many bytes at the start of s ok holds for.
func leading(s string, ok func(byte) bool) int {
	k := 0
	for k < len(s) && ok(s[k]) {
		k++
	}
	return k
}

func isHexDigit(c byte) bool   { return digitValue(c, 16) != -1 }
func isOctalDigit(c byte) bool { return digitValue(c, 8) != -1 }

// isClassName is whether c can be in the name of a Unicode class, as in
// \p{Old_Italic} or \p{^Greek}. Go's canonicalName ignores _, - and
// spaces in a name, so \p{Old-Italic} and \p{ L } name classes too.
func isClassName(c byte) bool {
	return c == '_' || c == '-' || c == ' ' || c == '^' || digitValue(c, 36) != -1
}

// digitValue is c's value as a digit of base, up to 36, or -1.
func digitValue(c byte, base int) int {
	v := 36
	switch {
	case c >= '0' && c <= '9':
		v = int(c - '0')
	case c >= 'a' && c <= 'z':
		v = int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		v = int(c-'A') + 10
	}
	if v < base {
		return v
	}
	return -1
}

// digitsValue is the value of digits in base, held to utf8.MaxRune, past
// which Go refuses the escape anyway.
func digitsValue(digits string, base int) rune {
	var v int64
	for i := range len(digits) {
		v = min(v*int64(base)+int64(digitValue(digits[i], base)), utf8.MaxRune)
	}
	return rune(v)
}

// programSize estimates the instructions syntax.Compile makes of re, by
// the rules Go's parser uses for its own size limit (calcSize in
// regexp/syntax): a counted repetition multiplies its operand. Parse has
// held that estimate to about 3.3 million, so it does not overflow.
func programSize(re *syntax.Regexp) int64 {
	var size int64
	switch re.Op {
	case syntax.OpLiteral:
		size = int64(len(re.Rune))
	case syntax.OpCapture, syntax.OpStar:
		size = 2 + programSize(re.Sub[0])
	case syntax.OpPlus, syntax.OpQuest:
		size = 1 + programSize(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			size += programSize(sub)
		}
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			size += programSize(sub)
		}
		size += int64(max(len(re.Sub)-1, 0))
	case syntax.OpRepeat:
		sub := programSize(re.Sub[0])
		switch {
		case re.Max == -1 && re.Min == 0:
			size = 2 + sub
		case re.Max == -1:
			size = 1 + int64(re.Min)*sub
		default:
			size = int64(re.Max)*sub + int64(re.Max-re.Min)
		}
	default:
		// A class, an anchor or an empty match is one instruction.
	}
	return max(1, size)
}
