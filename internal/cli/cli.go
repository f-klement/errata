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

	"github.com/f-klement/errata/internal/config"
	"github.com/f-klement/errata/internal/engine"
	"github.com/f-klement/errata/internal/report"
)

// Exit codes. CI reads only these, so they are part of the public contract.
const (
	ExitOK     = 0 // every check that ran found its claim true
	ExitErrata = 1 // at least one erratum or unknown, or a skip under --fail-on-skip
	ExitUsage  = 2 // bad flags, a config that does not load, or an --output file errata cannot write
)

// Version is set at build time with -ldflags "-X .../cli.Version=v0.1.0".
var Version = "dev"

// errErrata marks a run that completed and failed on an erratum, an unknown
// or a skip under --fail-on-skip, so Execute can tell it apart from a usage error.
var errErrata = errors.New("errata found")

// Execute runs the CLI with args and returns the process exit code. It takes
// its streams as arguments so tests drive it without a subprocess.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return execute(ctx, args, stdout, stderr, config.Load)
}

// loader reads the config at path under root, as config.Load does.
type loader func(root, path string) (config.Config, error)

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
	root.AddCommand(newCheckCmd(load), newListCmd(load), newVersionCmd())
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

// configFlags are the flags that say which config a command loads. check
// and list declare them with one function and load with loadConfig, so the
// two read one config the same way.
type configFlags struct {
	repoRoot   string
	configPath string
}

func (f *configFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.repoRoot, "root", ".", "repository root; checks run with this as their working directory")
	cmd.Flags().StringVarP(&f.configPath, "config", "c", "", "config file (default <root>/"+config.DefaultFile+")")
}

// file is the config file the flags name: --config, or errata.yaml in the
// absolute root.
func (f configFlags) file(absRoot string) string {
	if f.configPath == "" {
		return filepath.Join(absRoot, config.DefaultFile)
	}
	return f.configPath
}

// loadConfig is the one way a command reads its config: check runs the checks
// it returns, and list prints them. It returns the absolute root as well,
// which check runs the checks from. A config that does not load is an error
// that names the file, which Execute prints and exits 2 on; a config whose
// required list and checks differ is one, so check runs nothing.
func loadConfig(load loader, f configFlags) (string, config.Config, error) {
	absRoot, err := filepath.Abs(f.repoRoot)
	if err != nil {
		return "", config.Config{}, err
	}
	path := f.file(absRoot)
	cfg, err := load(absRoot, path)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("load %s: %w", path, err)
	}
	return absRoot, cfg, nil
}

func newCheckCmd(load loader) *cobra.Command {
	var (
		flags      configFlags
		format     string
		outputs    []string
		failOnSkip bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run the checks in errata.yaml and print the errata",
		Long: "Run every check the available providers can reach, skip the rest with the reason,\n" +
			"and print the errata and the coverage. errata never applies anything.\n" +
			"--format picks what goes to stdout; each --output FORMAT=PATH writes one more file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			newReporter, ok := report.Formats[format]
			if !ok {
				return fmt.Errorf("unknown --format %q, want one of %s", format, strings.Join(report.FormatNames(), ", "))
			}
			version := resolveVersion(Version, debug.ReadBuildInfo)
			outs, err := parseOutputs(outputs)
			if err != nil {
				return err
			}
			absRoot, cfg, err := loadConfig(load, flags)
			if err != nil {
				return err
			}
			if err := refuseConfigOutput(outs, flags.file(absRoot)); err != nil {
				return err
			}
			pending, err := openOutputs(outs)
			if err != nil {
				return err
			}
			defer discardOutputs(pending)

			e := engine.New(absRoot, nil)
			e.SkipsFail = failOnSkip
			rep := e.Execute(cmd.Context(), cfg.Checks)
			var errs []error
			if err := newReporter(version).Write(cmd.OutOrStdout(), rep); err != nil {
				errs = append(errs, err)
			}
			errs = append(errs, commitOutputs(pending, rep, version)...)
			if err := joinErrors(errs); err != nil {
				return err
			}
			if rep.Failed() {
				return errErrata
			}
			return nil
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: "+strings.Join(report.FormatNames(), ", "))
	cmd.Flags().StringArrayVarP(&outputs, "output", "o", nil, "also write the report in FORMAT to PATH, as FORMAT=PATH; repeatable")
	cmd.Flags().BoolVar(&failOnSkip, "fail-on-skip", false, "exit 1 when a check was skipped, as on an unknown")
	return cmd
}

func newListCmd(load loader) *cobra.Command {
	var (
		flags  configFlags
		format string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the checks errata.yaml holds, as errata check loads them",
		Long: "Print every check the config holds, with the values errata check would run it with,\n" +
			"defaults filled in. It runs nothing. A config that does not load exits 2, as with check.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lister, ok := report.ListFormats[format]
			if !ok {
				return fmt.Errorf("unknown --format %q, want one of %s", format, strings.Join(report.ListFormatNames(), ", "))
			}
			_, cfg, err := loadConfig(load, flags)
			if err != nil {
				return err
			}
			return lister.WriteList(cmd.OutOrStdout(), cfg.Checks, cfg.Required)
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: "+strings.Join(report.ListFormatNames(), ", "))
	return cmd
}
