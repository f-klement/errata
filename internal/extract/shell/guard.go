package shell

import (
	"errors"
	"io"
	"runtime"
)

// mvdan.cc/sh parses by recursive descent and has no limit on nesting, so
// a script of nothing but $( or "$(" grows the parser's stack until Go stops
// the process: 1 MiB of "$(" passed the 1 GB stack limit on 2026-10-24. A
// stack overflow is fatal, not an error a caller can recover, so the guard
// stops the parse first. The parser reads its input through the guard, at
// most 1 KiB a read and only from inside its recursion, and every
// checkEvery reads the guard counts the calls on the stack above the one
// that started the parse; past maxFrames it fails the read, and the parser
// stops with that error. Between two checks the parser reads at most
// checkEvery KiB, so the stack passes the limit by at most what that many
// bytes can nest.

// maxFrames is the deepest the parser's stack may grow, in calls above
// the read's. framesPerLevel bounds the calls one level of nesting takes:
// parentheses in (( )), the costliest measured, take 58, $(( )) 34, a
// subshell 28, "$( )" 14, $( ) 11, and if, case, a group or a default 6
// (TestGuardFramesPerLevel). So the limit allows maxFrames/framesPerLevel
// levels of the costliest, and more of every other; costliestPerLevel is
// the measured figure, which the message gives. Between two checks the
// parser reads at most 16 KiB, which can nest about 450000 calls deeper, at
// 27 calls for each ( inside $(( )), the most for one byte measured (a
// level of (( )) is three bytes). A check costs a walk of the stack, so at
// 4 KiB a script of 32 MiB held just under the limit took 6.5 s more than
// one that nests nothing.
const (
	framesPerLevel    = 64
	costliestPerLevel = 58
)

// The limits are variables only so that FuzzShell can lower them and reach
// both refusals with short inputs.
var (
	maxFrames  = 20000
	checkEvery = 16
)

// errTooDeep and errTooLong are the guard's refusals, which the parser
// returns.
var (
	errTooDeep = errors.New("the parser's stack is past its limit")
	errTooLong = errors.New("a statement is past its limit")
)

// guard is data read through a check on the depth of the stack.
type guard struct {
	data []byte
	off  int
	// untilCheck counts down the reads to the next check of the stack, and
	// checks counts the checks, which TestGuardChecksEveryFewReads pins.
	untilCheck, checks int
	pcs                []uintptr
	base               int // the calls on the stack when the parse started
	// mark is where the parser had read to when it gave the last statement,
	// and stmtLimit the most it may read past it for the next: the parser
	// holds a statement's syntax whole, about 200 bytes of memory for each
	// byte of a pipeline of a|a|a, so the limit bounds what one statement
	// costs.
	mark, stmtLimit int
	// stopped ends the input early, once the read has failed.
	stopped bool
}

func newGuard(data []byte, stmtLimit int) *guard {
	g := &guard{data: data, stmtLimit: stmtLimit, untilCheck: checkEvery}
	// The caller's own stack may be any depth; count it whole, in a buffer
	// doubled until the stack leaves room in it. The buffer a check needs
	// waits for the first check (tooDeep).
	for g.pcs = make([]uintptr, 64); ; g.pcs = append(g.pcs, g.pcs...) {
		if g.base = runtime.Callers(0, g.pcs); g.base < len(g.pcs) {
			break
		}
	}
	return g
}

// Read is data's next bytes, or errTooDeep once the stack is past the
// limit.
func (g *guard) Read(p []byte) (int, error) {
	if g.off == len(g.data) || g.stopped {
		return 0, io.EOF
	}
	if g.off-g.mark > g.stmtLimit {
		return 0, errTooLong
	}
	if g.untilCheck--; g.untilCheck == 0 {
		g.untilCheck = checkEvery
		g.checks++
		if g.tooDeep() {
			return 0, errTooDeep
		}
	}
	n := copy(p, g.data[g.off:])
	g.off += n
	return n, nil
}

// statementEnds marks where the parser has read to once it gives a
// statement.
func (g *guard) statementEnds() { g.mark = g.off }

// stop ends the input: the parser reads to the end of what it holds.
func (g *guard) stop() { g.stopped = true }

// tooDeep reports whether the stack holds more than maxFrames calls above
// the parse's start.
func (g *guard) tooDeep() bool {
	// The buffer holds one call more than the limit allows, 160 KB, so it
	// is made at the first check: a script the parser reads in fewer than
	// checkEvery reads, an empty one too, never pays for it. Made with the
	// guard, it was most of what env allocated for each empty script.
	if need := g.base + maxFrames + 1; len(g.pcs) < need {
		g.pcs = make([]uintptr, need)
	}
	return runtime.Callers(0, g.pcs)-g.base > maxFrames
}
