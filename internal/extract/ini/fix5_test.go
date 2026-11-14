package ini

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// What a review of the CRAP refactor found no test pinned: grammar edges of
// the PHP reader, the last byte of a value in the Python literal reader,
// and three values the corpora never hold.

// An offset's ] is the bracket alone: the line break after it is no part
// of the token, unlike a section's, so key[x], a line break and =1 is a
// key with no = and then a line PHP's grammar refuses (TC_OFFSET
// option_offset ']' '=', with no END_OF_LINE between). The planted break
// is the offset's ] taking the line break as a section's does.
func TestPHPOffsetBracketTakesNoLineBreak(t *testing.T) {
	l := &phpLexer{data: []byte("]\n=1"), state: phpOffsetValue}
	l.next()
	if l.tok != phpRBracket || l.end != 1 {
		t.Errorf("token %d ending at %d, want a closing bracket ending at 1", l.tok, l.end)
	}
	got := iniError(t, "php.ini", "a[x]\n=1\n", ini("php.ini", "", "a"))
	if !strings.Contains(got, "PHP refuses this line") {
		t.Errorf("a[x], a line break and =1: error %q, want a refusal", got)
	}
}

// PHP's grammar takes a parenthesis that closes, and an expression around
// one, and errata returns each as the whole value as written, computed.
// The planted break is the closing parenthesis refused.
func TestPHPReadsAClosedParenthesis(t *testing.T) {
	body := "a = (1)\na = ~(E_ALL | 1) & 2\n"
	if got, want := notLiterals(t, "php.ini", body, ini("php.ini", "", "a")), "(1)!@1 ~(E_ALL | 1) & 2!@2"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Depth counts operators nested in one another, not operators side by
// side: more than core.MaxDepth of ~1 | joined in sequence nest no deeper
// than one, and the value is read. The planted break is a depth that only
// grows, which refuses the line as too deep.
func TestPHPDepthCountsNestingNotSequence(t *testing.T) {
	value := strings.Repeat("~1 | ", core.MaxDepth+1) + "1"
	if got, want := notLiterals(t, "php.ini", "a = "+value+"\n", ini("php.ini", "", "a")), value+"!@1"; got != want {
		t.Errorf("got %d bytes, want the %d bytes of the value marked", len(got), len(want))
	}
}

// Each of these values ends inside a literal at its last byte: a . with no
// digit after it, alone and in a list, a prefix letter with no quote, a \
// that ends a string, and an octal escape at the end of an open string.
// literal_eval refuses each, and ansible keeps the text, which errata
// returns unmarked. The planted breaks are each bounds check that guards
// the last byte dropped, which reads past the value and panics.
func TestAnsibleKeepsAValueThatEndsInsideALiteral(t *testing.T) {
	spec := Spec{File: "hosts", Section: "g:vars", Key: "a"}
	body := "[g:vars]\na=.\na=[.\na=[r\na='x\\\na='\\1\n"
	if got, want := notLiterals(t, "hosts", body, spec), `.@2 [.@3 [r@4 'x\@5 '\1@6`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A plain 0 is the text Python writes for it, and -0 is not, since str(-0)
// is 0. The planted break is a single 0 marked as a zero written with more
// digits.
func TestAnsibleLeavesAPlainZeroUnmarked(t *testing.T) {
	spec := Spec{File: "hosts", Section: "g:vars", Key: "a"}
	if got, want := notLiterals(t, "hosts", "[g:vars]\na=0\na=-0\n", spec), "0@2 -0!@3"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A brace group with no comma anywhere in the text, py{38}, is one group
// of one alternative, which tox's factor pattern takes. The planted break
// is a missing comma read as one before the brace, which reads py{38} as
// a part and refuses it.
func TestToxFactorsTakeABraceGroupWithNoComma(t *testing.T) {
	for _, s := range []string{"py{38}", "py{38}-lint"} {
		if !toxFactors(s) {
			t.Errorf("toxFactors(%q) is false, want true", s)
		}
	}
}
