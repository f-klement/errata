// Package protocol runs the programs a config names: a program check through
// the check protocol, and a generated check's generator (generated.go). Both
// start their program through one function, run, so the two kinds share
// the timeout, the environment, the working directory, the empty stdin and
// the kill of the process group.
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

	p := run(ctx, root, c, c.Run, 0)
	res.Message = message(p.stdout)

	var exitErr *exec.ExitError
	switch {
	case p.err == nil:
		res.Verdict = check.VerdictTrue
	case p.timedOut:
		res.Verdict = check.VerdictUnknown
		res.Message = fmt.Sprintf("did not finish within %s", c.Timeout)
	case errors.As(p.err, &exitErr) && exitErr.ExitCode() == 1:
		res.Verdict = check.VerdictFalse
	default:
		res.Verdict = check.VerdictUnknown
		res.Message = strings.TrimSpace(strings.Join([]string{p.err.Error(), res.Message, tail(string(p.stderr))}, "\n"))
	}
	return res
}

// ended is how one run of a program ended: what it wrote, the error
// exec.Cmd.Run returned, whether errata stopped it for running past the
// check's timeout, and the stream, "stdout" or "stderr", it wrote past the
// bound on, "" when it wrote past none.
type ended struct {
	stdout, stderr []byte
	err            error
	timedOut       bool
	overBound      string
}

// run starts argv for check c with root as its working directory, waits for
// it and returns how it ended. The program gets c's timeout, a timeout of 0
// being none; the environment of errata with ERRATA_ROOT, ERRATA_CHECK_ID
// and ERRATA_SCOPE added; no stdin, so a program that reads it reads an
// empty file at once; and a process group of its own, which a timeout
// kills whole (isolate). A bound above 0 is the most bytes errata holds of
// stdout, and of stderr apart: the byte after it on either stops the
// program as a timeout does, and overBound names the stream. A bound of 0
// holds all of both, as a program check's run does.
func run(ctx context.Context, root string, c check.Check, argv []string, bound int64) ended {
	timed := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		timed, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	// stop ends the program once it writes past the bound. It is a child
	// of the timeout's context, so timed.Err tells the two apart.
	stopped, stop := context.WithCancel(timed)
	defer stop()

	cmd := exec.CommandContext(stopped, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"ERRATA_ROOT="+root,
		"ERRATA_CHECK_ID="+c.ID,
		"ERRATA_SCOPE="+string(c.Scope),
	)
	// A nil Stdin is the null device: a program that reads stdin reads
	// nothing and goes on, where a terminal or errata's own stdin would
	// hold it until the timeout.
	cmd.Stdin = nil
	isolate(cmd)
	// If something the check started still holds its output open after the
	// check is cancelled, stop waiting for it.
	cmd.WaitDelay = time.Second
	stdout := &bounded{limit: bound, stop: stop}
	stderr := &bounded{limit: bound, stop: stop}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	e := ended{stdout: stdout.buf.Bytes(), stderr: stderr.buf.Bytes(), err: err}
	if stdout.over {
		e.overBound = "stdout"
	}
	if stderr.over && !stdout.over {
		e.overBound = "stderr"
	}
	e.timedOut = err != nil && e.overBound == "" && timed.Err() != nil
	return e
}

// errOverBound is what bounded returns for the write that passes its limit,
// which makes exec stop copying the stream.
var errOverBound = errors.New("a stream is over its bound")

// bounded holds what a program writes to one stream up to limit bytes, and for
// a write that passes it keeps what fits, sets over and calls stop. A limit
// of 0 holds everything. exec writes to it from one goroutine and run reads
// it only after Wait, so it needs no lock.
type bounded struct {
	buf   bytes.Buffer
	limit int64
	over  bool
	stop  context.CancelFunc
}

func (b *bounded) Write(p []byte) (int, error) {
	if room := b.limit - int64(b.buf.Len()); b.limit > 0 && int64(len(p)) > room {
		b.buf.Write(p[:room])
		b.over = true
		b.stop()
		return int(room), errOverBound
	}
	return b.buf.Write(p)
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
