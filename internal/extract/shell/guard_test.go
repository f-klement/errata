package shell

import (
	"io"
	"runtime"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// deepestReader is data read with the depth of the stack counted at every
// read, the deepest kept.
type deepestReader struct {
	r       io.Reader
	pcs     []uintptr
	deepest int
}

func (d *deepestReader) Read(p []byte) (int, error) {
	d.deepest = max(d.deepest, runtime.Callers(0, d.pcs))
	return d.r.Read(p[:min(len(p), 64)])
}

// framesAt is how many calls the parser's stack reaches on src, above the
// read's start, and how deep its syntax tree is.
func framesAt(t *testing.T, src string) (frames, tree int) {
	t.Helper()
	d := &deepestReader{r: strings.NewReader(src), pcs: make([]uintptr, 1<<20)}
	base := runtime.Callers(0, d.pcs)
	for stmt, err := range syntax.NewParser().StmtsSeq(d) {
		if err != nil {
			t.Fatalf("%.40q: %v", src, err)
		}
		depth := 0
		syntax.Walk(stmt, func(n syntax.Node) bool {
			if n == nil {
				depth--
			} else {
				depth++
				tree = max(tree, depth)
			}
			return true
		})
	}
	return d.deepest - base, tree
}

// Each nesting the parser recurses on takes at most framesPerLevel calls a
// level, which the guard's message counts on: the stack of 2n levels less
// that of n, over n. Parentheses in (( )) are the costliest. And the deepest
// syntax tree the guard lets the parser build is under maxTreeDepth, so the
// walk refuses only what the parser builds in a loop: a pipeline, a list or
// a sum.
func TestGuardFramesPerLevel(t *testing.T) {
	const n = 300
	for _, c := range []struct{ name, open, inner, close string }{
		{"command substitution", "x=$(", "a", ")"},
		{"in quotes", "x=\"$(", "a", ")\""},
		{"subshell", "(", "a", ")"},
		{"group", "{ ", "a;", " }"},
		{"default", "x=${a:-", "b", "}"},
		{"if", "if a; then ", "b", "; fi"},
		{"while", "while a; do ", "b", "; done"},
		{"case", "case a in b) ", "c", ";; esac"},
		{"function", "f() { ", "a;", " }"},
		{"arithmetic", "x=$((", "1", "))"},
		// The same $(( )), with nest's case below putting the levels of
		// parentheses inside it.
		{"arithmetic parentheses", "x=$((", "1", "))"},
		{"arithmetic command", "(( ", "1", " ))"},
		{"pipe", "a | ", "a", ""},
		{"and", "a && ", "a", ""},
		{"process substitution", "cat <(", "a", ")"},
		{"array", "a=( $(", "b", ") )"},
		{"test parentheses", "[[ ", "a", " ]]"},
	} {
		nest := func(k int) string {
			switch c.name {
			case "arithmetic parentheses":
				return c.open + strings.Repeat("(", k) + c.inner + strings.Repeat(")", k) + c.close + "\n"
			case "test parentheses":
				return c.open + strings.Repeat("( ", k) + c.inner + strings.Repeat(" )", k) + c.close + "\n"
			}
			return strings.Repeat(c.open, k) + c.inner + strings.Repeat(c.close, k) + "\n"
		}
		k := n
		one, treeOne := framesAt(t, nest(k))
		two, treeTwo := framesAt(t, nest(2*k))
		per := float64(two-one) / float64(k)
		// The tree's depth at the most levels the guard allows.
		tree := float64(treeTwo-treeOne) / float64(k) * float64(maxFrames) / max(per, 1)
		t.Logf("%-24s %5d calls at %d levels, %5d at %d: %.2f a level; a tree %.0f deep at the limit", c.name, one, k, two, 2*k, per, tree)
		if per > framesPerLevel {
			t.Errorf("%s takes %.2f calls a level, over framesPerLevel, %d", c.name, per, framesPerLevel)
		}
		if per >= 1 && tree >= float64(maxTreeDepth) {
			t.Errorf("%s: the guard lets the parser build a tree %.0f deep, which the walk refuses", c.name, tree)
		}
	}
}

// The guard counts the whole stack of its caller, however deep, in a buffer
// that starts at 64 calls: one more than the caller's own count, the
// guard's frame. The planted break: the loop's test as <=, which cut the
// count at a stack of 64.
func TestGuardCountsTheCallersStack(t *testing.T) {
	big := make([]uintptr, 1<<12)
	var at func(depth int)
	at = func(depth int) {
		if depth != 0 {
			at(depth - 1)
			return
		}
		g := newGuard(nil, 0)
		if want := runtime.Callers(0, big) + 1; g.base != want {
			t.Errorf("base %d, want %d", g.base, want)
		}
	}
	for depth := range 300 {
		at(depth)
	}
}

//go:noinline
func guardHere() *guard { return newGuard(nil, 0) }

//go:noinline
func deepBy(g *guard, calls int) bool {
	if calls == 0 {
		return g.tooDeep()
	}
	return deepBy(g, calls-1)
}

// The stack is too deep at more than maxFrames calls above the guard's
// start, and not at maxFrames. The planted breaks: > as >=, and the base
// added, which each moved the refusal.
func TestGuardRefusesPastMaxFrames(t *testing.T) {
	frames := maxFrames
	maxFrames = 100
	t.Cleanup(func() { maxFrames = frames })
	g := guardHere()
	if deepBy(g, 100) {
		t.Errorf("refused at %d calls above the start", 100)
	}
	if !deepBy(g, 101) {
		t.Errorf("not refused at %d calls above the start", 101)
	}
}

// The guard looks at the stack once every checkEvery reads, which bounds
// its cost: 64 reads of 1 KiB take 4 looks. The planted breaks: the count
// going up, which never looked, and its test as !=, which looked 60 times.
func TestGuardChecksEveryFewReads(t *testing.T) {
	g := newGuard(make([]byte, 64<<10), 1<<30)
	buf := make([]byte, 1<<10)
	for {
		if _, err := g.Read(buf); err != nil {
			break
		}
	}
	if want := 64 / checkEvery; g.checks != want {
		t.Errorf("%d checks, want %d", g.checks, want)
	}
}
