// Package protocol runs a check written as a program: the check protocol.
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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// Run executes c with root as its working directory and returns its result.
func Run(ctx context.Context, root string, c check.Check) (res check.Result) {
	start := time.Now()
	res.Check = c
	// res is a named result so this assignment reaches the caller.
	defer func() { res.Duration = time.Since(start) }()

	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, c.Run[0], c.Run[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"ERRATA_ROOT="+root,
		"ERRATA_CHECK_ID="+c.ID,
		"ERRATA_SCOPE="+string(c.Scope),
	)
	isolate(cmd)
	// If something the check started still holds its output open after the
	// check is cancelled, stop waiting for it.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res.Message = message(stdout.Bytes())

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Verdict = check.VerdictTrue
	case ctx.Err() != nil:
		res.Verdict = check.VerdictUnknown
		res.Message = fmt.Sprintf("did not finish within %s", c.Timeout)
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		res.Verdict = check.VerdictFalse
	default:
		res.Verdict = check.VerdictUnknown
		res.Message = strings.TrimSpace(strings.Join([]string{err.Error(), res.Message, tail(stderr.String())}, "\n"))
	}
	return res
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
