// Package program starts every program errata runs and waits for it: a
// program check's, a generated check's generator, and later a plugin's.
// Each starts through Run, so all of them get the same handling of the
// working directory, environment, stdin, timeout, bound on output and kill
// of the process group. What a program's exit and output mean is its
// caller's: the check protocol reads a verdict from them
// (internal/protocol), a generated check compares the output with a file.
package program

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// Spec is one program to run.
type Spec struct {
	// Argv is the program and its arguments, started as they are, never
	// through a shell.
	Argv []string
	// Dir is the working directory.
	Dir string
	// Env is added to errata's own environment, each entry KEY=value.
	Env []string
	// Stdin is what the program reads on stdin. nil is the null device, so
	// a program that reads stdin reads nothing and goes on, where a
	// terminal or errata's own stdin would hold it until the timeout.
	Stdin []byte
	// Timeout stops the program and its process group once it passes; 0 is
	// none.
	Timeout time.Duration
	// Bound is the most bytes errata holds of stdout, and of stderr apart:
	// the byte after it on either stops the program as a timeout does. 0
	// holds all of both.
	Bound int64
}

// Ended is how one run of a program ended: what it wrote, the error
// exec.Cmd.Run returned, whether errata stopped it for running past its
// timeout, and the stream, "stdout" or "stderr", it wrote past the bound
// on, "" when it wrote past none.
type Ended struct {
	Stdout, Stderr []byte
	Err            error
	TimedOut       bool
	OverBound      string
}

// Run starts s, waits for it and returns how it ended. The program runs in
// a process group of its own, which the timeout and the bound kill whole
// (isolate). A child left running after the program exits runs on. A Spec
// with no Argv starts nothing and ends with errNoArgv.
func Run(ctx context.Context, s Spec) Ended {
	if len(s.Argv) == 0 {
		return Ended{Err: errNoArgv}
	}
	timed := ctx
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		timed, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}
	// stop ends the program once it writes past the bound. It is a child
	// of the timeout's context, so timed.Err tells the two apart.
	stopped, stop := context.WithCancel(timed)
	defer stop()

	cmd := exec.CommandContext(stopped, s.Argv[0], s.Argv[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...)
	if s.Stdin != nil {
		cmd.Stdin = bytes.NewReader(s.Stdin)
	}
	isolate(cmd)
	// If something the program started still holds its output open after
	// the program is cancelled, stop waiting for it.
	cmd.WaitDelay = time.Second
	stdout := &bounded{limit: s.Bound, stop: stop}
	stderr := &bounded{limit: s.Bound, stop: stop}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	e := Ended{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Err: err}
	if stdout.over {
		e.OverBound = "stdout"
	}
	if stderr.over && !stdout.over {
		e.OverBound = "stderr"
	}
	e.TimedOut = err != nil && e.OverBound == "" && timed.Err() != nil
	return e
}

// errNoArgv is how a Spec with no program to start ends.
var errNoArgv = errors.New("program: the Spec names no program to start, its argv is empty")

// errOverBound is what bounded returns for the write that passes its limit,
// which makes exec stop copying the stream.
var errOverBound = errors.New("a stream is over its bound")

// bounded holds what a program writes to one stream up to limit bytes, and for
// a write that passes it keeps what fits, sets over and calls stop. A limit
// of 0 holds everything. exec writes to it from one goroutine and Run reads
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
