// Package cli turns arguments into a run and a run into an exit code. It holds
// no domain logic: it parses flags, wires config, engine and reporter
// together, and maps the outcome to the exit codes below.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
	"github.com/f-klement/errata/internal/engine"
	"github.com/f-klement/errata/internal/report"
)

// Exit codes. CI reads only these, so they are part of the public contract.
const (
	ExitOK     = 0 // every check that ran found its claim true
	ExitErrata = 1 // at least one erratum or unknown
	ExitUsage  = 2 // bad flags or a config that does not load
)

// Version is set at build time with -ldflags "-X .../cli.Version=v0.1.0".
var Version = "dev"

// errErrata marks a run that completed and failed on an erratum or an
// unknown, so Execute can tell it apart from a usage error.
var errErrata = errors.New("errata found")

// Execute runs the CLI with args and returns the process exit code. It takes
// its streams as arguments so tests drive it without a subprocess.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return execute(ctx, args, stdout, stderr, config.Load)
}

// loader reads the config at path under root into checks, as config.Load
// does.
type loader func(root, path string) ([]check.Check, error)

// execute is Execute with the loader as an argument. The testscripts pass
// one that wraps config.Load, to run a reader that the extract package does
// not have yet, such as one that marks a value not literal.
func execute(ctx context.Context, args []string, stdout, stderr io.Writer, load loader) int {
	root := newRoot(load)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.ExecuteContext(ctx)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, errErrata):
		return ExitErrata
	default:
		// A load error can quote the config file, such as a CEL or a regular
		// expression, so it reaches the terminal through the text output's
		// escapes.
		fmt.Fprintln(stderr, "errata:", report.QuoteControls(err.Error()))
		return ExitUsage
	}
}

func newRoot(load loader) *cobra.Command {
	root := &cobra.Command{
		Use:           "errata",
		Short:         "Check the claims a repository makes about itself",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newCheckCmd(load), newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the errata version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), resolveVersion(Version, debug.ReadBuildInfo))
			return err
		},
	}
}

// resolveVersion prefers the version a release build stamps with -ldflags.
// `go install module@vX.Y.Z` stamps nothing, but Go records the module
// version in the binary, so that is the fallback; a local build records
// "(devel)" and stays "dev".
func resolveVersion(stamped string, buildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "dev" {
		return stamped
	}
	if info, ok := buildInfo(); ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return stamped
}

func newCheckCmd(load loader) *cobra.Command {
	var (
		repoRoot   string
		configPath string
		format     string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run the checks in errata.yaml and print the errata",
		Long: "Run every check the available providers can reach, skip the rest with the reason,\n" +
			"and print the errata and the coverage. errata never applies anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			newReporter, ok := report.Formats[format]
			if !ok {
				return fmt.Errorf("unknown --format %q, want one of %s", format, strings.Join(report.FormatNames(), ", "))
			}
			reporter := newReporter(resolveVersion(Version, debug.ReadBuildInfo))
			absRoot, err := filepath.Abs(repoRoot)
			if err != nil {
				return err
			}
			if configPath == "" {
				configPath = filepath.Join(absRoot, config.DefaultFile)
			}
			checks, err := load(absRoot, configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", configPath, err)
			}

			rep := engine.New(absRoot, nil).Execute(cmd.Context(), checks)
			if err := reporter.Write(cmd.OutOrStdout(), rep); err != nil {
				return err
			}
			if rep.Failed() {
				return errErrata
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoRoot, "root", ".", "repository root; checks run with this as their working directory")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "config file (default <root>/"+config.DefaultFile+")")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: "+strings.Join(report.FormatNames(), ", "))
	return cmd
}
