package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/plugin"
	"github.com/f-klement/errata/internal/plugin/install"
)

// SourceEnv is the variable that sets the plugin source when --source is
// not given (ADR 0021).
const SourceEnv = plugin.SourceEnv

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plugin",
		Short:   "Install the plugins errata.lock pins",
		Long:    pluginLong,
		Example: pluginExample,
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(newPluginInstallCmd())
	return cmd
}

// installFlags are the flags of errata plugin install.
type installFlags struct {
	root string
	req  install.Request
}

func newPluginInstallCmd() *cobra.Command {
	var f installFlags
	cmd := &cobra.Command{
		Use:     "install [<name>[@<version>]...]",
		Short:   "Install plugins into the cache and pin them in errata.lock",
		Long:    pluginInstallLong,
		Example: pluginInstallExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := installer(f.root, cmd)
			if err != nil {
				return err
			}
			req := f.req
			req.Plugins = args
			if req.Source == "" && req.File == "" && req.URL == "" {
				req.Source, req.SourceFrom = os.Getenv(SourceEnv), SourceEnv
			}
			return in.Install(cmd.Context(), req)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.root, "root", ".", "the repository root `DIR`")
	fl.StringVar(&f.req.Source, "source", "", "a mirror of errata's releases, as an https or file `URL` (default $"+SourceEnv+", else errata's releases)")
	fl.BoolVar(&f.req.TrustSource, "trust-source", false, "pin a version the lock does not pin yet through the mirror")
	fl.BoolVar(&f.req.Vendor, "vendor", false, "also write every platform's build into .errata/plugins")
	fl.StringVar(&f.req.File, "file", "", "install the build at `PATH`, pinned as a file")
	fl.StringVar(&f.req.URL, "url", "", "install the build at the https `URL`, checked against --sha256")
	fl.StringVar(&f.req.SHA256, "sha256", "", "the sha256 of the build at --url, as 64 `HEX` digits")
	fl.StringVar(&f.req.Platform, "platform", "", "the platform `OS/ARCH` the build at --url is for (default the platform errata runs on)")
	fl.BoolVar(&f.req.Replace, "replace", false, "let --file or --url take the place of a release the lock pins")
	return cmd
}

// installer is the Installer of errata plugin install in the repository at
// root, printing to cmd's stdout.
func installer(root string, cmd *cobra.Command) (*install.Installer, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &install.Installer{
		Root:     absRoot,
		Cache:    cache,
		Token:    os.Getenv(plugin.TokenEnv),
		Builtin:  extract.Keys(),
		Platform: plugin.Platform(),
		Out:      cmd.OutOrStdout(),
	}, nil
}
