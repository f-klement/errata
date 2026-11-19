// Command errata checks the claims a repository makes about itself.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/f-klement/errata/internal/cli"
)

// stops are the signals that stop a run the way an interrupt does: the
// checks are cancelled and their copies removed (ADR 0019). SIGTERM is how
// CI cancels a job and timeout(1) stops a command, SIGHUP how a closed
// terminal ends one.
var stops = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), stops...)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
