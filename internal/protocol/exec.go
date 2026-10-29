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
// The program runs with its working directory at the repository root and
// receives ERRATA_ROOT, ERRATA_CHECK_ID and ERRATA_SCOPE in its environment.
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

	// A program check's output has no bound yet: #191 decides it.
	p := run(ctx, root, c, c.Program().Run, 0)
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
	return res
}

// run starts argv for check c through program.Run, with root as its
// working directory, and returns how it ended. The program gets c's
// timeout, a timeout of 0 being none; the environment of errata with
// ERRATA_ROOT, ERRATA_CHECK_ID and ERRATA_SCOPE added; no stdin, so a
// program that reads it reads an empty file at once; and a process group
// of its own, which a timeout kills whole. A bound above 0 is the most
// bytes errata holds of stdout, and of stderr apart, and the byte after it
// stops the program; a bound of 0 holds all of both, as a program check's
// run does.
func run(ctx context.Context, root string, c check.Check, argv []string, bound int64) program.Ended {
	return program.Run(ctx, program.Spec{
		Argv: argv,
		Dir:  root,
		Env: []string{
			"ERRATA_ROOT=" + root,
			"ERRATA_CHECK_ID=" + c.ID,
			"ERRATA_SCOPE=" + string(c.Scope),
		},
		Timeout: c.Timeout,
		Bound:   bound,
	})
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
