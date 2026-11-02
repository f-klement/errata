package rule

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
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
//     instructions over renovateRegexDivisor (renovateWork.cost).
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

// regexCompileCost is what a regex's compile costs, once its text has
// passed scanRegex: renovateCompileWeight for each byte of the entry, as a
// glob pays; regexNamedWeight for each Unicode class it names, and when it
// may fold case, the code points its ranges cover over regexFoldDivisor,
// for what Go's parser does that the program does not show; one for each
// instruction of its program and each rune the program holds; and for a
// program Go tries to make onepass, its instructions times its instructions
// and runes over regexOnepassDivisor. Go's onepass pass merges the rune
// sets of the program at each instruction, so its time grows with the two:
// an anchored class and its complement repeated 330 times, 20 bytes, took
// up to 49 ms a compile on a loaded node. A program of about 1000
// instructions with no runes takes about 6 ms on its own, and an unanchored
// one of 2000 about 3 ms, which the instruction term covers. Fitted over
// 109 shapes, by the mean of compiles back to back on two nodes of the CI
// runner, the cost limit pays for at most 2.30 s of compiling on the slower
// node, for ^(?:\pL?){400}$, and 0.86 s on the other, for ^(?:\pL?){497}$;
// on a node loaded three to four times over, ^(?:\pL*){495}$ came to about
// 4.2 s. An ordinary entry such as /^apps/[^/]+/helmrelease\.yaml$/, 32
// bytes, names no class, folds no range, and costs 635. A regex refused
// after its parse costs its text's share alone, since no program was made.
func regexCompileCost(entry string, text regexText, prog *syntax.Prog) uint64 {
	cost := uint64(len(entry)) * renovateCompileWeight
	cost += uint64(text.named) * regexNamedWeight //nolint:gosec // a count, never below 0
	if text.folds {
		cost += uint64(text.folded) / regexFoldDivisor //nolint:gosec // a count, never below 0
	}
	if prog == nil {
		return cost
	}
	insts, runes := uint64(len(prog.Inst)), uint64(programRunes(prog)) //nolint:gosec // counts, never below 0
	cost += insts + runes
	if onepassTried(prog) {
		cost += insts * (insts + runes) / regexOnepassDivisor
	}
	return cost
}

// regexFoldDivisor scales the code points a regex's ranges cover, which
// Go's parser folds one at a time under case folding, into its compile
// cost (regexCompileCost).
const regexFoldDivisor = 4

// regexNamedWeight is the compile cost of each Unicode class a regex
// names, which Go's parser merges into the class around it, and folds
// under case folding: 16 of \pL in a class took up to 13.5 ms to parse
// without folding as well as with it (regexCompileCost).
const regexNamedWeight = 256

// regexOnepassDivisor scales a program's instructions times its
// instructions and runes into the cost of the onepass pass Go makes of it
// (regexCompileCost).
const regexOnepassDivisor = 256

// errRegexBound is a regex refused by one of the bounds above, before the
// step it bounds runs.
type errRegexBound struct{ msg string }

func (e errRegexBound) Error() string { return e.msg }

// regexPattern is a regex entry compiled: its matcher and the
// instructions of its program, for the cost, or why it is refused.
type regexPattern struct {
	entry string
	re    *regexp.Regexp
	insts int
	units int
	cost  uint64
	err   error
}

// match is s matched against the regex, with the instructions of the
// program that ran. It looks at ctx before the regex runs: Go's regexp
// takes no context, so a regex that has started runs to its end, at most
// about 120 ms (maxRegexInsts). A regex RE2 does not compile, and one past
// a bound, is a refusal.
func (p *regexPattern) match(ctx context.Context, s string) (bool, matchWork, error) {
	if p.err != nil {
		return false, matchWork{}, &refusal{form: "regex", entry: p.entry, s: s, reason: p.err}
	}
	if err := ctx.Err(); err != nil {
		return false, matchWork{}, fmt.Errorf("the evaluation was stopped before the regex %q ran: %w", p.entry, err)
	}
	return p.re.MatchString(s) != strings.HasPrefix(p.entry, "!"), matchWork{insts: p.insts}, nil
}

// size is the regex's units: three for each instruction of its program,
// one for each rune its program holds, and one for each rune of the
// onepass copy regexp.Compile may make of it (onepassRunes). An
// instruction holds 40 to 50 bytes, and up to about 185 for a capture in
// an onepass program; a rune holds 4, and about 6 with the onepass table
// that goes with it. The program shares the runes of a repeated class
// among its instructions, so each rune slice counts once there. A refused
// regex holds only its error.
func (p *regexPattern) size() int { return p.units }

// compileCost is regexCompileCost for a regex whose text scanRegex read,
// since its parse ran, and 0 for one it refused: the scan is one pass over
// at most MaxArgument bytes, which the call's own bytes pay for.
func (p *regexPattern) compileCost() uint64 { return p.cost }

// regexUnits is the units of prog, as regexPattern.size counts them.
func regexUnits(prog *syntax.Prog) int {
	return 3*len(prog.Inst) + programRunes(prog) + onepassRunes(prog)
}

// programRunes is the runes prog holds, each rune slice once: a program
// shares the runes of a repeated class among its instructions.
func programRunes(prog *syntax.Prog) int {
	runes := 0
	counted := map[*rune]bool{}
	for _, inst := range prog.Inst {
		if len(inst.Rune) == 0 || counted[&inst.Rune[0]] {
			continue
		}
		counted[&inst.Rune[0]] = true
		runes += len(inst.Rune)
	}
	return runes
}

// onepassTried is whether Go's makeOnePass tries a onepass copy of prog:
// a program under 1000 instructions that starts at an EmptyWidth
// instruction holding EmptyBeginText, such as an anchored regex.
func onepassTried(prog *syntax.Prog) bool {
	start := prog.Inst[prog.Start]
	begins := syntax.EmptyOp(start.Arg)&syntax.EmptyBeginText != 0 //nolint:gosec // an EmptyWidth instruction's Arg holds its EmptyOp, as regexp/syntax stores it
	return len(prog.Inst) < 1000 && start.Op == syntax.InstEmptyWidth && begins
}

// onepassRunes is at most the runes of the onepass copy regexp.Compile
// makes of prog, or 0 where it makes none. Go's makeOnePass tries one for
// a program under 1000 instructions that starts at an EmptyWidth
// instruction holding EmptyBeginText, such as an anchored regex. It gives
// each instruction a rune set: a consuming instruction its own runes
// (consumedRunes), a capture, an empty width or a nop instruction a copy
// of the set of the instruction after it, and an alternation the sets of
// both its legs merged. So an instruction's set holds at most the sets of
// the consuming instructions it reaches without consuming, each once: a
// merge that meets a rune in both legs fails, and Go then drops the copy.
// That sum is counted for each instruction. 109 empty captures before a
// class of four Unicode categories copy the class 110 times, where the
// program holds it once.
func onepassRunes(prog *syntax.Prog) int {
	if !onepassTried(prog) {
		return 0
	}
	// seen[pc] is the walk that last reached pc, so each walk counts a
	// consuming instruction once.
	seen := make([]int, len(prog.Inst))
	total := 0
	for from := range prog.Inst {
		walk := from + 1
		stack := []uint32{uint32(from)}
		for len(stack) > 0 {
			pc := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[pc] == walk {
				continue
			}
			seen[pc] = walk
			inst := &prog.Inst[pc]
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				stack = append(stack, inst.Out, inst.Arg)
			case syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstNop:
				stack = append(stack, inst.Out)
			default:
				total += consumedRunes(inst)
			}
		}
	}
	return total
}

// consumedRunes is the length of the rune set makeOnePass gives a
// consuming instruction: a class its ranges, a folded single rune a pair
// for each rune of its fold orbit, an unfolded one a pair, and any rune,
// or any rune but a newline, the ranges that stand for it. Match and Fail
// have none.
func consumedRunes(inst *syntax.Inst) int {
	folded := syntax.Flags(inst.Arg)&syntax.FoldCase != 0 //nolint:gosec // a rune instruction's Arg holds its Flags, as regexp/syntax stores them
	switch inst.Op {
	case syntax.InstRune, syntax.InstRune1:
		// syntax.Compile makes an unfolded single rune an InstRune1.
		switch {
		case len(inst.Rune) != 1:
			return len(inst.Rune)
		case !folded:
			return 2
		}
		n := 2
		for r := unicode.SimpleFold(inst.Rune[0]); r != inst.Rune[0]; r = unicode.SimpleFold(r) {
			n += 2
		}
		return n
	case syntax.InstRuneAny:
		return 2
	case syntax.InstRuneAnyNotNL:
		return 4
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstNop,
		syntax.InstMatch, syntax.InstFail:
	}
	return 0
}

// compileRegex compiles a regex entry as Renovate reads it, with each step
// bounded before it runs. regexp.Compile takes no parsed tree, so a regex
// that passes every bound is parsed once more inside it, within the same
// bound on its text.
func compileRegex(entry string) *regexPattern {
	re, prog, text, err := compileRegexBounded(entry)
	var scanned errScanned
	switch {
	case errors.As(err, &scanned):
		return &regexPattern{entry: entry, err: scanned.err}
	case err != nil:
		return &regexPattern{entry: entry, err: err, cost: regexCompileCost(entry, text, nil)}
	}
	return &regexPattern{entry: entry, re: re, insts: len(prog.Inst), units: regexUnits(prog), cost: regexCompileCost(entry, text, prog)}
}

// errScanned is a regex scanRegex refused, before any parse ran.
type errScanned struct{ err error }

func (e errScanned) Error() string { return e.err.Error() }

// compileRegexBounded is compileRegex's matcher and program, or why it is
// refused: an errRegexBound, or RE2's own error, which Renovate would meet
// too and then read the entry as a glob.
func compileRegexBounded(entry string) (*regexp.Regexp, *syntax.Prog, regexText, error) {
	// Renovate strips the start, then the end of what is left, so "/" is
	// the empty regex and "/i" the regex i with the i flag.
	body := regexEnd.ReplaceAllString(regexStart.ReplaceAllString(entry, ""), "")
	text, err := scanRegex(body, strings.HasSuffix(entry, "i"))
	if err != nil {
		return nil, nil, text, errScanned{err}
	}
	if strings.HasSuffix(entry, "i") {
		body = "(?i)" + body
	}
	parsed, err := syntax.Parse(body, syntax.Perl)
	if err != nil {
		return nil, nil, text, notCompiled(err)
	}
	if n := programSize(parsed); n > maxRegexInsts {
		return nil, nil, text, errRegexBound{fmt.Sprintf("its RE2 program would have about %d instructions; errata matches at most %d", n, maxRegexInsts)}
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil, nil, text, notCompiled(err)
	}
	if len(prog.Inst) > maxRegexInsts {
		return nil, nil, text, errRegexBound{fmt.Sprintf("its RE2 program has %d instructions; errata matches at most %d", len(prog.Inst), maxRegexInsts)}
	}
	re, err := regexp.Compile(body)
	if err != nil {
		return nil, nil, text, notCompiled(err)
	}
	return re, prog, text, nil
}

// notCompiled is RE2's error on a regex, as a refusal gives its reason.
func notCompiled(err error) error {
	return fmt.Errorf("RE2 does not compile the regex, which Renovate would then read as a glob: %w", err)
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
func scanRegex(body string, iflag bool) (regexText, error) {
	if len(body) > maxRegexText {
		return regexText{}, errRegexBound{fmt.Sprintf("its text is %d bytes; errata reads a regex of at most %d", len(body), maxRegexText)}
	}
	named, folded, refused := regexScan(body)
	text := regexText{named: named, folded: folded, folds: iflag || foldFlag.MatchString(body)}
	switch {
	case refused == `\Q`:
		return text, errRegexBound{`it quotes text with \Q, which errata does not read in a regex`}
	case refused != "":
		return text, errRegexBound{fmt.Sprintf("its escape %q is malformed", refused)}
	case named > maxRegexNamed:
		return text, errRegexBound{fmt.Sprintf("it names %d Unicode classes; errata reads at most %d", named, maxRegexNamed)}
	case folded > maxRegexFolded && text.folds:
		return text, errRegexBound{fmt.Sprintf("its ranges fold case over %d code points; errata folds at most %d", folded, maxRegexFolded)}
	}
	return text, nil
}

// regexText is what scanRegex read of a regex's text: the Unicode classes
// it names, the code points its ranges cover between minFold and maxFold,
// and whether it may fold case, which makes Go's parser fold each of those
// code points and each class one at a time.
type regexText struct {
	named  int
	folded int64
	folds  bool
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
