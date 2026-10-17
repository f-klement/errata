// Command errata checks the claims a repository makes about itself.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/f-klement/errata/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
