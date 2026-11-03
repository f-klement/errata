// Package protocol runs the programs a config names: a program check through
// the check protocol, and a generated check's generator (generated.go). Both
// start their program through run, and so through program.Run, which every
// program errata runs starts through: the two kinds share the timeout, the
// environment, the working directory, the empty stdin and the kill of the
// process group.
//
// The contract, which a check in any language implements:
//
//   - exit 0: the claim is true
//   - exit 1: the claim is false
//   - any other exit, a crash, or a timeout: unknown, the check could not evaluate
//
// Stdout is the message. If stdout is a JSON object with a "message" string,
// errata uses that string instead, which leaves room to add fields later
// without breaking a check that prints plain text.
//
// errata holds at most check.DefaultMaxBytes of a program check's stdout,
// and of its stderr apart. It reads what the program writes past that and
// drops it, without stopping the program, and the message then says which
// stream it cut and at what bound. The verdict is still the exit code. A
// cut stdout is no longer a whole JSON object, so its message is the held
// text as it is, and an unknown quotes the last lines of the held part of
// stderr, not of all of it. A generator's bound is generated.max_bytes, and
// the byte past it stops the generator (generated.go).
//
// The program runs with its working directory at the repository root and
// receives ERRATA_ROOT, ERRATA_CHECK_ID and ERRATA_SCOPE in its environment.
// On a witness ERRATA_ROOT is the fixture, and the working directory stays
// the repository's root (ADR 0015), so a program reads the tree it is
// handed from ERRATA_ROOT.
package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/program"
)

// Run executes c with root as its working directory and returns its result.
func Run(ctx context.Context, root string, c check.Check) (res check.Result) {
	start := time.Now()
	res.Check = c
	// res is a named result so this assignment reaches the caller.
	defer func() { res.Duration = time.Since(start) }()

	// The bound drains: a check that prints past it is cut, never stopped
	// or made unknown for its output alone, since its exit is its verdict.
	bound := int64(check.DefaultMaxBytes)
	p := run(ctx, root, c, c.Program().Run, bound, true)
	res.Message = message(p.Stdout)

	var exitErr *exec.ExitError
	switch {
	case p.Err == nil:
		res.Verdict = check.VerdictTrue
	case p.TimedOut:
		res.Verdict = check.VerdictUnknown
		res.Message = fmt.Sprintf("did not finish within %s", c.Timeout)
	case errors.As(p.Err, &exitErr) && exitErr.ExitCode() == 1:
		res.Verdict = check.VerdictFalse
	default:
		res.Verdict = check.VerdictUnknown
		res.Message = strings.TrimSpace(strings.Join([]string{p.Err.Error(), res.Message, tail(string(p.Stderr))}, "\n"))
	}
	res.Message = withCut(res.Message, p.Cut, bound)
	return res
}

// run starts argv for check c through program.Run and returns how it
// ended. Its working directory is root, or the repository's root on a run
// of a witness, whose root is the fixture (check.WorkDir), so a relative
// argv resolves as on any run; ERRATA_ROOT is root. The program gets c's
// timeout, a timeout of 0 being none; the environment of errata with
// ERRATA_ROOT, ERRATA_CHECK_ID and ERRATA_SCOPE added; no stdin, so a
// program that reads it reads an empty file at once; and a process group
// of its own, which a timeout kills whole. bound is the most bytes errata
// holds of stdout, and of stderr apart. When drain is set errata drops what
// the program writes past it and lets it run on, as a program check's run
// does; otherwise the byte after it stops the program, as a generator's
// does.
func run(ctx context.Context, root string, c check.Check, argv []string, bound int64, drain bool) program.Ended {
	return program.Run(ctx, program.Spec{
		Argv: argv,
		Dir:  check.WorkDir(ctx, root),
		Env: []string{
			"ERRATA_ROOT=" + root,
			"ERRATA_CHECK_ID=" + c.ID,
			"ERRATA_SCOPE=" + string(c.Scope),
		},
		Timeout: c.Timeout,
		Bound:   bound,
		Drain:   drain,
	})
}

// withCut is msg followed by a line for each stream in cut, the streams
// whose bytes past bound, the bound the run held them to, errata dropped.
func withCut(msg string, cut []string, bound int64) string {
	var b strings.Builder
	b.WriteString(msg)
	for _, stream := range cut {
		fmt.Fprintf(&b, "\n(errata cut %s at %d bytes, the most it holds of a program check's stream, and dropped the rest)", stream, bound)
	}
	return strings.TrimSpace(b.String())
}

func message(stdout []byte) string {
	var obj struct {
		Message *string `json:"message"`
	}
	if json.Unmarshal(stdout, &obj) == nil && obj.Message != nil {
		return strings.TrimSpace(*obj.Message)
	}
	return strings.TrimSpace(string(stdout))
}

// tail keeps the last few lines of stderr, which is where a crashing program
// usually says why.
func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-5):], "\n")
}
