package program

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func sh(script string) Spec {
	return Spec{Argv: []string{"sh", "-c", script}, Dir: ".", Timeout: 5 * time.Second}
}

// Stdin is what the program reads: cat prints it back whole without a
// bound, and an empty or nil stdin reads as an empty file at once. Planted
// break: Stdin left nil whatever the Spec holds, which printed nothing.
func TestRunGivesTheProgramItsStdin(t *testing.T) {
	for _, in := range [][]byte{[]byte("request\n"), {}, nil} {
		s := Spec{Argv: []string{"cat"}, Dir: ".", Stdin: in, Timeout: 2 * time.Second}
		e := Run(context.Background(), s)
		if e.Err != nil || !bytes.Equal(e.Stdout, in) || e.OverBound != "" || e.TimedOut {
			t.Fatalf("cat of %q: %+v", in, e)
		}
	}
}

// A program that echoes its stdin past the bound is stopped at the byte
// after it, with its process group: the sleep its shell starts after cat
// would hold the run for 4 s. Planted breaks: bounded never calling stop,
// which ran the sleep out; the bound counted with >=, which stopped cat at
// exactly the bound.
func TestRunStopsAProgramThatEchoesStdinPastTheBound(t *testing.T) {
	in := bytes.Repeat([]byte("x"), 70000)
	s := sh("cat; sleep 4 & wait")
	s.Stdin, s.Bound, s.Timeout = in, 1000, 6*time.Second
	start := time.Now()
	e := Run(context.Background(), s)
	if e.OverBound != "stdout" || len(e.Stdout) != 1000 || e.TimedOut {
		t.Fatalf("over %q, %d bytes held, timed out %v", e.OverBound, len(e.Stdout), e.TimedOut)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the program ran %s past its bound", took)
	}

	s = Spec{Argv: []string{"cat"}, Dir: ".", Stdin: in[:1000], Bound: 1000, Timeout: 5 * time.Second}
	if e := Run(context.Background(), s); e.Err != nil || e.OverBound != "" || len(e.Stdout) != 1000 {
		t.Fatalf("exactly the bound: %v, over %q, %d bytes", e.Err, e.OverBound, len(e.Stdout))
	}
}

// Stderr has a bound of its own, and the stream past it is named. A bound
// of 0 holds all of both. Planted break: stderr
// made with no limit.
func TestRunBoundsEachStream(t *testing.T) {
	s := sh("head -c 5000 /dev/zero >&2")
	s.Bound = 100
	if e := Run(context.Background(), s); e.OverBound != "stderr" || len(e.Stderr) != 100 {
		t.Fatalf("stderr: over %q, %d bytes", e.OverBound, len(e.Stderr))
	}
	s = sh("head -c 70000 /dev/zero; head -c 70000 /dev/zero >&2")
	if e := Run(context.Background(), s); e.Err != nil || e.OverBound != "" || len(e.Stdout) != 70000 || len(e.Stderr) != 70000 {
		t.Fatalf("no bound: %v, over %q, %d and %d bytes", e.Err, e.OverBound, len(e.Stdout), len(e.Stderr))
	}
}

// A draining bound holds the first Bound bytes of each stream and drops the
// rest without stopping the program: the shell writes past the bound on
// both streams, more than a pipe holds, and still reaches the line that
// writes after.txt and exits 0. Planted breaks: the drain calling stop,
// which killed the shell before after.txt; the drain failing the write,
// which left the pipe full and the shell blocked until the timeout.
func TestRunDrainsPastTheBound(t *testing.T) {
	dir := t.TempDir()
	s := sh("head -c 70000 /dev/zero; head -c 70000 /dev/zero >&2; echo ran on > after.txt")
	s.Dir, s.Bound, s.Drain = dir, 1000, true
	e := Run(context.Background(), s)
	if e.Err != nil || e.TimedOut || e.OverBound != "" || len(e.Stdout) != 1000 || len(e.Stderr) != 1000 {
		t.Fatalf("%v, timed out %v, over %q, %d and %d bytes held", e.Err, e.TimedOut, e.OverBound, len(e.Stdout), len(e.Stderr))
	}
	if !slices.Equal(e.Cut, []string{"stdout", "stderr"}) {
		t.Fatalf("cut %q, want stdout and stderr", e.Cut)
	}
	if _, err := os.Stat(filepath.Join(dir, "after.txt")); err != nil {
		t.Fatalf("the program did not run on past the bound: %v", err)
	}
}

// Cut names only the streams written past the bound, and a stopping bound
// cuts none. Planted breaks: stderr named whatever it held; the stopping
// bound's stream listed in Cut as well.
func TestRunCutNamesEachStreamPastTheBound(t *testing.T) {
	s := sh("head -c 5000 /dev/zero >&2; echo short")
	s.Bound, s.Drain = 1000, true
	if e := Run(context.Background(), s); e.Err != nil || !slices.Equal(e.Cut, []string{"stderr"}) {
		t.Fatalf("%v: cut %q, want stderr alone", e.Err, e.Cut)
	}
	s = sh("echo short")
	s.Bound, s.Drain = 1000, true
	if e := Run(context.Background(), s); e.Err != nil || e.Cut != nil {
		t.Fatalf("%v: cut %q, want none", e.Err, e.Cut)
	}
	s = sh("head -c 5000 /dev/zero")
	s.Bound = 1000
	if e := Run(context.Background(), s); e.OverBound != "stdout" || e.Cut != nil {
		t.Fatalf("a stopping bound: over %q, cut %q", e.OverBound, e.Cut)
	}
}

// A bound of 0 holds every byte, as a program check's run needs.
func TestBoundedZeroHoldsEverything(t *testing.T) {
	b := &bounded{stop: func() { t.Fatal("stopped without a bound") }}
	for range 3 {
		if n, err := b.Write(make([]byte, 1000)); n != 1000 || err != nil {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if b.buf.Len() != 3000 || b.over {
		t.Fatalf("held %d bytes, over %v", b.buf.Len(), b.over)
	}
}

// The timeout stops the program, children included, and is told apart
// from an exit; a timeout of 0 is none. Planted break: the timeout's
// context dropped, which ran the sleep out.
func TestRunTimeout(t *testing.T) {
	s := sh("sleep 3")
	s.Timeout = 50 * time.Millisecond
	start := time.Now()
	e := Run(context.Background(), s)
	if !e.TimedOut || e.Err == nil {
		t.Fatalf("%+v, want timed out", e)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a 50ms timeout took %s to return", took)
	}
	s = sh("sleep 0.05; exit 3")
	s.Timeout = 0
	if e := Run(context.Background(), s); e.TimedOut || e.Err == nil {
		t.Fatalf("no timeout: %+v, want an exit that is no timeout", e)
	}
}

// The program runs in Dir with errata's environment and Env added.
func TestRunDirAndEnvironment(t *testing.T) {
	t.Setenv("ERRATA_PROGRAM_TEST", "kept")
	dir := t.TempDir()
	s := sh(`printf '%s %s %s' "$PWD" "$ERRATA_PROGRAM_TEST" "$ADDED"`)
	s.Dir, s.Env = dir, []string{"ADDED=yes"}
	e := Run(context.Background(), s)
	got, _ := filepath.EvalSymlinks(strings.Fields(string(e.Stdout) + " ")[0])
	want, _ := filepath.EvalSymlinks(dir)
	if e.Err != nil || got != want || !strings.HasSuffix(string(e.Stdout), " kept yes") {
		t.Fatalf("%v: printed %q, want %s kept yes", e.Err, e.Stdout, dir)
	}
}

// A Spec with no Argv ends with an error and starts nothing, where indexing
// Argv[0] panicked. Planted break: the check of Argv's length dropped.
func TestRunRefusesAnEmptyArgv(t *testing.T) {
	for _, argv := range [][]string{nil, {}} {
		if e := Run(context.Background(), Spec{Argv: argv, Dir: "."}); !errors.Is(e.Err, errNoArgv) {
			t.Fatalf("argv %q: %+v", argv, e)
		}
	}
}
