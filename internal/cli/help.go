package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/f-klement/errata/internal/report"
)

// The help of each command: what `errata <command> --help` prints, and so
// what docs/cli.md holds, since make cli-doc writes that file from it. It is
// the one home of the command line reference. Every line fits 80 columns,
// and TestHelpIsComplete fails on a command or flag the help leaves out.

// releaseTag is the shape of a release's tag, as the pipeline's workflow
// rule accepts it: vX.Y.Z or vX.Y.Z-rc.N. A pseudo-version, which Go records
// for a build in a checkout or a go install at a commit, is no tag.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

// referenceURL is the address of docs/cli.md for the version that prints
// it: a release names its own tag, and any other build names main.
func referenceURL(version string) string {
	ref := "main"
	if releaseTag.MatchString(version) {
		ref = version
	}
	return "https://github.com/f-klement/errata/blob/" + ref + "/docs/cli.md"
}

// helpFooter is appended to every command's help. It reads the reference
// annotation of the root command, which Execute sets and make cli-doc
// leaves out, since docs/cli.md is the reference itself.
const helpFooter = `{{with .Root.Annotations.reference}}
The full command line reference is docs/cli.md:
{{.}}
{{end}}`

// orList joins names as a sentence does: "a, b or c".
func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

const rootLong = `errata checks the claims a repository makes about itself. Each check in the
config is a program errata runs, or a rule over values errata reads from the
repository's files. errata prints every claim shown false (an erratum) with
the reason the check was written, and says how many checks ran, how many it
skipped, and why it skipped each.

errata never applies anything. It reads files and runs the programs the
config names. The only files it writes itself are the ones a --output flag
of errata check names, each through a temporary file beside it.

The config
  errata check and errata list read errata.yaml in the repository root.
  --root names the root and defaults to the working directory. --config
  names another file in place of <root>/errata.yaml; errata looks for no
  other file and reads no environment variable to find one.

Exit codes
  0  the command did what it was asked. For errata check, every check that
     ran found its claim true.
  1  errata check found at least one erratum or unknown, or, with
     --fail-on-skip, skipped at least one check.
  2  bad flags or an unknown command, a config that does not load, or an
     --output file errata cannot write.

errata with no command prints this help and exits 0. errata help <command>
prints the help of one command, as errata <command> --help does, and exits
0 even for a command errata does not know.`

const rootExample = `  # Check the repository in the working directory.
  errata check

  # See which checks a config holds, with the defaults filled in.
  errata list --config ci/errata.yaml

  # Check the working directory with the image in place of a binary.
  docker run --rm -v "$PWD:/repo" -w /repo fklement/errata check`

func checkLong() string {
	return `Run every check in the config that errata can reach, skip the rest with the
reason, and print the errata and the coverage: how many checks ran, how many
it skipped, and the gap behind each skip.

A repo check runs on the files. A live or applied check needs a provider for
a running system, which errata does not have yet, so errata skips it and
prints a line naming the check, its scope and the gap, such as "skipped
pinned-image (live): no provider can read a live system". A program check
runs with the repository root as its working directory.

errata check never applies anything. It writes no file but the ones --output
names, and it prints the report whatever the run found.

Flags
  --root DIR (default .)
      The repository root. Checks run with it as their working directory,
      and errata reads <root>/errata.yaml unless --config names a file.
  --config FILE, -c FILE (default <root>/errata.yaml)
      The config file. A relative FILE is read from the working directory,
      not from --root, as a shell redirect is. --root still sets where the
      checks run, so --root repo --config ci/errata.yaml reads the config
      from ci/ and runs the checks in repo.
  --format NAME, -f NAME (default text)
      What goes to stdout: ` + orList(report.FormatNames()) + `.
  --output FORMAT=PATH, -o FORMAT=PATH (default none, repeatable)
      Also write the report in FORMAT, any name --format takes, to the file
      PATH. --format still picks stdout, so --output junit=errata.xml
      --output sarif=errata.sarif writes both files and prints the text.
  --fail-on-skip (default off)
      A skipped check fails the run with exit code 1, as an unknown does.

Exit codes
  0  every check that ran found its claim true.
  1  at least one erratum (a claim shown false) or unknown (a check that
     could not evaluate), or, with --fail-on-skip, at least one skip.
  2  bad flags, a config that does not load, or an --output file errata
     cannot write. errata runs no check when it exits 2 before the run.

--fail-on-skip
  A CI gate that must not pass on checks that never ran, such as live
  checks in a job with no cluster, runs errata check --fail-on-skip. A skip
  then fails the run as an unknown does: exit code 1, and no erratum, since
  the check showed no claim false. It stays a skip, with its gap under the
  coverage line, and every format marks it as a failure. The text coverage
  line ends "A skip fails this run.", JUnit writes the skip as an <error>
  whose message is "skipped: " and the gap, and SARIF writes its
  notification at level error and sets executionSuccessful to false.

--output
  The exit code and each format's content are the same as without it, and
  errata writes every file whatever the run found. PATH is relative to the
  working directory, as a redirect's is. errata writes each file to a
  temporary file in its directory and renames it into place, so PATH holds
  the old file or the whole report, never part of one. A run that is
  killed leaves its temporary file, .<name>.<random>.tmp, beside PATH. A new
  file's mode is 0666 less the umask, and a file that is there keeps its
  mode.

  These exit 2 before any check runs: a value without =, a format errata
  does not know, a PATH another --output names, a PATH that is the config,
  and a file errata cannot create. An output is the config when it is the
  file the config was read from, through the config's symbolic links or a
  hard link. An output that is itself a symbolic link to the config is
  replaced, and the config is left alone. A file errata cannot write after
  the run exits 2 too, in place of 1, and errata still prints the report
  and writes every other file.

Control characters
  The text report writes every control character a file or a program's
  output holds, such as ESC or CR, as its escape (\x1b, \r), and every byte
  that is not UTF-8 as \xNN; it drops a CR before a newline. The direction
  controls (U+061C, U+200E, U+200F, U+202A to U+202E and U+2066 to U+2069)
  and the separators U+2028 and U+2029 are escapes too (\u202e). An error
  on stderr, which can quote the config, takes the same escapes. No file in
  the repository can clear the terminal, overwrite a line, reorder one or
  set a link in it. JUnit and SARIF write DEL, the C1 controls and the
  direction controls as character references (&#x9B;) in JUnit and as JSON
  escapes (\u009b) in SARIF, and a reader gets the text back as it was.`
}

const checkExample = `  # Check the repository in the working directory.
  errata check

  # Check another checkout, with a config kept outside it.
  errata check --root ../infra --config infra-errata.yaml

  # In CI: print the text report, keep JUnit for the test report, and
  # fail on a check that was skipped.
  errata check --output junit=errata.xml --fail-on-skip

  # SARIF on stdout, for GitHub code scanning.
  errata check -f sarif > errata.sarif`

func listLong() string {
	return `Print every check the config holds, as errata check loads it, with the
values errata check would run it with and the defaults filled in. A program
that needs to know what errata will run reads errata list --format json
instead of parsing errata.yaml itself.

errata list runs nothing and reads none of the files the checks name. It
reads the config with the loader errata check uses, so a config that check
refuses, list refuses with the same message.

Flags
  --root DIR (default .)
      The repository root. errata reads <root>/errata.yaml unless --config
      names a file.
  --config FILE, -c FILE (default <root>/errata.yaml)
      The config file. A relative FILE is read from the working directory,
      not from --root.
  --format NAME, -f NAME (default text)
      What goes to stdout: ` + orList(report.ListFormatNames()) + `.

Exit codes
  0  the config loaded, and errata printed its checks.
  2  bad flags, or a config that does not load.

Control characters
  The text format writes control characters, bytes that are not UTF-8 and
  the direction controls as escapes, as the text report of errata check
  does. The JSON writes DEL, the C1 controls and the direction controls as
  JSON escapes (\u009b), and a reader gets the text back as it was.`
}

const listExample = `  # The checks in <root>/errata.yaml, as text.
  errata list

  # The checks of another config, as JSON for a program.
  errata list --root ../infra -c infra-errata.yaml --format json`

const versionLong = `Print the version of this errata binary on one line. A release build prints
its tag, such as v0.2.4, and make build prints what git describe gives, such
as v0.2.4-3-g8ade832 three commits after it. Any other build prints the
version Go records in the binary: the module version for go install at a
release, and a pseudo-version, such as v0.2.5-0.20261103212526-8ade832ca96d,
for a build in a git checkout or a go install at a commit, with +dirty when
the checkout has changes. A build without that record prints dev.

errata version reads no config, and takes no argument and no flag but
--help (-h).

Exit codes
  0  errata printed its version.
  2  an argument, or a flag other than --help.`

const versionExample = `  # The version of the binary on the PATH.
  errata version

  # The version of the image.
  docker run --rm fklement/errata version`

// helpWidth is the terminal width the help fits.
const helpWidth = 80

// wrapFlags is cobra's usage template with the flag tables wrapped to
// helpWidth, since a flag's usage line is longer than a terminal row.
func wrapFlags(tmpl string) string {
	for _, set := range []string{".LocalFlags", ".InheritedFlags"} {
		tmpl = strings.ReplaceAll(tmpl, set+".FlagUsages ", fmt.Sprintf("(%s.FlagUsagesWrapped %d) ", set, helpWidth))
	}
	return tmpl
}

// documentBuiltIns gives the help and completion commands cobra adds the
// same kind of help as errata's own: a Long text with the exit codes, and
// examples. root must hold both, as newRoot makes it.
func documentBuiltIns(root *cobra.Command) {
	for _, c := range root.Commands() {
		switch c.Name() {
		case "help":
			c.Short, c.Long, c.Example = "Print the help of a command", helpLong, helpExample
		case "completion":
			c.Short, c.Long, c.Example = "Print a shell's completion script for errata", completionLong, completionExample
			for _, sh := range c.Commands() {
				if s, ok := shells[sh.Name()]; ok {
					sh.Short, sh.Long, sh.Example = "Print the completion script for "+s.name, s.long(), s.example(sh.Name())
				}
			}
		}
	}
}

const helpLong = `Print the help of a command, as errata <command> --help does. errata help
with no command prints the help of errata, and a path of names, such as
errata help completion bash, names a command below another.

For a name errata does not know, errata help prints "Unknown help topic"
and the usage of errata, and still exits 0.

Exit codes
  0  errata printed help, for a name it does not know too.
  2  a flag other than --help.`

const helpExample = `  # The help of errata check.
  errata help check

  # The help of the bash completion script.
  errata help completion bash`

const completionLong = `Print a script that has a shell complete errata's commands and flags.
errata completion <shell> prints the script for one shell: bash, zsh, fish
or powershell. The script asks errata itself for the completions as you
type, so it follows the binary on the PATH. errata completion with no shell,
or with a name that is no shell, prints this help.

Exit codes
  0  errata printed the help, for a name that is no shell too.
  2  a flag other than --help.`

const completionExample = `  # Complete errata in this bash session.
  source <(errata completion bash)

  # Complete errata in every new fish session.
  errata completion fish > ~/.config/fish/completions/errata.fish`

// shell is the help of one shell's completion command, whose name is its
// key in shells.
type shell struct {
	name, needs, now string
	// short is now as the example writes it, where now with
	// --no-descriptions would pass 80 columns; empty means now.
	short  string
	always []string // each way to install the script for every session
}

var shells = map[string]shell{
	"bash": {
		name:  "bash",
		needs: "The script needs the bash-completion package.",
		now:   "source <(errata completion bash)",
		always: []string{
			"errata completion bash > /etc/bash_completion.d/errata",
			"errata completion bash > $(brew --prefix)/etc/bash_completion.d/errata",
		},
	},
	"zsh": {
		name: "zsh",
		needs: "The script needs completion turned on in zsh. If it is not, run once:\n" +
			"  echo \"autoload -U compinit; compinit\" >> ~/.zshrc",
		now: "source <(errata completion zsh)",
		always: []string{
			"errata completion zsh > \"${fpath[1]}/_errata\"",
			"errata completion zsh > $(brew --prefix)/share/zsh/site-functions/_errata",
		},
	},
	"fish": {
		name:   "fish",
		now:    "errata completion fish | source",
		always: []string{"errata completion fish > ~/.config/fish/completions/errata.fish"},
	},
	"powershell": {
		name:   "PowerShell",
		now:    "errata completion powershell | Out-String | Invoke-Expression",
		short:  "errata completion powershell | Out-String | iex",
		always: []string{"errata completion powershell >> $PROFILE"},
	},
}

func (s shell) long() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Print the completion script for %s on stdout.", s.name)
	if s.needs != "" {
		b.WriteString("\n\n" + s.needs)
	}
	fmt.Fprintf(&b, "\n\nTo complete errata in this session:\n  %s\n", s.now)
	b.WriteString("\nTo complete errata in every new session, run once, and start a new shell:\n")
	for _, a := range s.always {
		b.WriteString("  " + a + "\n")
	}
	if len(s.always) > 1 {
		b.WriteString("The first is for Linux, the second for macOS with Homebrew.\n")
	}
	b.WriteString(`
Flags
  --no-descriptions (default off)
      Complete the names alone, without the line that describes each.

Exit codes
  0  errata printed the script.
  2  an argument, or a flag errata does not know.`)
	return b.String()
}

// example is the help's examples for the shell named cmd.
func (s shell) example(cmd string) string {
	now := s.now
	if s.short != "" {
		now = s.short
	}
	return fmt.Sprintf("  # Complete errata in this %s session, the names alone.\n  %s\n\n"+
		"  # Complete errata in every new session.\n  %s",
		s.name, strings.Replace(now, "completion "+cmd, "completion "+cmd+" --no-descriptions", 1), s.always[0])
}
