package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/f-klement/errata/internal/config"
)

// commands is errata and each command below it that its help lists, as the
// arguments that name it.
func commands(t *testing.T) [][]string {
	t.Helper()
	var paths [][]string
	for _, c := range documented(newRoot(config.Load, io.Discard, io.Discard)) {
		paths = append(paths, argsOf(c))
	}
	if len(paths) < 10 {
		t.Fatalf("found %v, want errata, check, list, version, help, and completion with its four shells", paths)
	}
	return paths
}

// printedHelp is what `errata <path> --help` prints, as Execute runs it.
func printedHelp(t *testing.T, path []string) string {
	t.Helper()
	var out, errs bytes.Buffer
	if code := Execute(context.Background(), append(append([]string{}, path...), "--help"), &out, &errs); code != ExitOK || errs.Len() > 0 {
		t.Fatalf("errata %v --help: exit %d, stderr %q", path, code, errs.String())
	}
	return out.String()
}

// helpProblems is each way the help of root's tree falls short: a command
// without a Long text, exit codes or two examples, and a flag, persistent or
// not, without a line of its own in the Long text, without the flag's own
// default on that line, or without an example.
func helpProblems(root *cobra.Command) []string {
	var problems []string
	for _, c := range documented(root) {
		name := c.CommandPath()
		if strings.TrimSpace(c.Long) == "" {
			problems = append(problems, name+" has no Long text")
		}
		if !strings.Contains(c.Long, "\nExit codes\n") {
			problems = append(problems, name+" gives no exit codes")
		}
		if n := exampleCount(c.Example); n < 2 {
			problems = append(problems, fmt.Sprintf("%s has %d examples, want at least 2", name, n))
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			flag := "--" + f.Name
			stated, found := "", false
			for line := range strings.Lines(c.Long) {
				if strings.HasPrefix(line, "  "+flag+" ") {
					stated, found = statedDefault(line), true
				}
			}
			if !found {
				problems = append(problems, fmt.Sprintf("%s: %s has no line of its own in the Long text", name, flag))
			} else if want := spelledDefault(f); stated != want {
				problems = append(problems, fmt.Sprintf("%s: the Long text gives %s the default %q, and the flag has %q", name, flag, stated, want))
			}
			if !strings.Contains(c.Example, flag) && (f.Shorthand == "" || !strings.Contains(c.Example, " -"+f.Shorthand+" ")) {
				problems = append(problems, fmt.Sprintf("%s: %s is in no example", name, flag))
			}
		})
	}
	return problems
}

// exampleCount is how many commands an Example holds: its lines that are
// neither blank nor a comment.
func exampleCount(example string) int {
	n := 0
	for line := range strings.Lines(example) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

// statedDefault is the default a Long text's flag line gives, as in
// "--output FORMAT=PATH, -o FORMAT=PATH (default none, repeatable)": none.
// It is empty when the line gives none.
func statedDefault(line string) string {
	_, after, ok := strings.Cut(line, "(default ")
	if !ok {
		return ""
	}
	end := strings.IndexAny(after, ",)")
	if end < 0 {
		return ""
	}
	return after[:end]
}

// spelledDefault is f's default as a Long text spells it: off or on for a
// switch, none for an empty list, the default its usage names for an empty
// string whose default is worked out at run time, such as --config's, and
// none for any other empty string.
func spelledDefault(f *pflag.Flag) string {
	switch {
	case f.Value.Type() == "bool" && f.DefValue == "false":
		return "off"
	case f.Value.Type() == "bool":
		return "on"
	case f.DefValue == "[]":
		return "none"
	case f.DefValue == "":
		if d := statedDefault(f.Usage); d != "" {
			return d
		}
		return "none"
	}
	return f.DefValue
}

// Every command errata's help lists has a Long text and examples, and its
// Long text gives every flag on a line of its own with the flag's own
// default, and the exit codes; every flag is in an example. So a new command
// or flag without help, or a default changed in one place, fails on the push
// (#176). The planted breaks: list's Example emptied, and --fail-on-skip left
// out of check's Long.
func TestHelpIsComplete(t *testing.T) {
	for _, p := range helpProblems(newRoot(config.Load, io.Discard, io.Discard)) {
		t.Error(p)
	}
}

// helpProblems finds each gap in a planted tree: a persistent flag with no
// help, a command two levels down with no help, a default the Long text
// gives wrong, a list flag with no example, and a Long text without exit
// codes.
func TestHelpProblemsRefuses(t *testing.T) {
	documentedLong := "Does a thing.\n\nFlags\n  --x (default off)\n      X.\n\nExit codes\n  0  done.\n"
	twoExamples := "  errata a --x\n  errata a"
	root := &cobra.Command{Use: "errata", Long: documentedLong, Example: twoExamples}
	root.Flags().Bool("x", false, "x")
	root.PersistentFlags().String("colour", "auto", "when to colour")
	a := &cobra.Command{Use: "a", Long: documentedLong, Example: twoExamples, Run: func(*cobra.Command, []string) {}}
	a.Flags().Bool("x", false, "x")
	a.Flags().Int("n", 3, "n")
	a.Long = strings.Replace(a.Long, "  --x (default off)", "  --x (default off)\n  --n N (default 4)", 1)
	b := &cobra.Command{Use: "b", Long: "Does b.", Run: func(*cobra.Command, []string) {}}
	a.AddCommand(b)
	root.AddCommand(a)
	got := strings.Join(helpProblems(root), "\n")
	for _, want := range []string{
		"errata: --colour has no line of its own in the Long text",
		"errata: --colour is in no example",
		`errata a: the Long text gives --n the default "4", and the flag has "3"`,
		"errata a: --n is in no example",
		"errata a b gives no exit codes",
		"errata a b has 0 examples, want at least 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("helpProblems does not report %q; it reports:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--x") {
		t.Errorf("helpProblems reports the documented --x:\n%s", got)
	}
}

func TestSpelledDefault(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.Bool("off", false, "")
	fs.Bool("on", true, "")
	fs.StringArray("list", nil, "")
	fs.String("worked-out", "", "the `FILE` (default <root>/errata.yaml)")
	fs.String("empty", "", "nothing")
	fs.String("plain", "text", "")
	for name, want := range map[string]string{
		"off": "off", "on": "on", "list": "none", "worked-out": "<root>/errata.yaml", "empty": "none", "plain": "text",
	} {
		if got := spelledDefault(fs.Lookup(name)); got != want {
			t.Errorf("spelledDefault(--%s) = %q, want %q", name, got, want)
		}
	}
	for line, want := range map[string]string{
		"  --root DIR (default .)\n":           ".",
		"  --o X (default none, repeatable)\n": "none",
		"  --q (default open":                  "",
		"  --p\n":                              "",
	} {
		if got := statedDefault(line); got != want {
			t.Errorf("statedDefault(%q) = %q, want %q", line, got, want)
		}
	}
}

// The help of every command fits an 80 column terminal, is printable ASCII,
// and ends by naming docs/cli.md at the version's tag. The planted breaks:
// a line of check's Long made 81 columns, and the flag tables left unwrapped
// (wrapFlags returning its argument).
func TestHelpFitsTheTerminal(t *testing.T) {
	for _, p := range commands(t) {
		help := printedHelp(t, p)
		for line := range strings.Lines(help) {
			line = strings.TrimSuffix(line, "\n")
			if len(line) > helpWidth {
				t.Errorf("errata %v --help: a line of %d columns: %q", p, len(line), line)
			}
			for _, r := range line {
				if r < ' ' || r > '~' {
					t.Errorf("errata %v --help: %q holds %U", p, line, r)
				}
			}
		}
		lines := strings.Split(strings.TrimRight(help, "\n"), "\n")
		if last := lines[len(lines)-1]; last != referenceURL(resolveVersion(Version, debug.ReadBuildInfo)) || !strings.HasSuffix(last, "/docs/cli.md") {
			t.Errorf("errata %v --help ends %q, want the address of docs/cli.md", p, last)
		}
	}
}

// A release names its tag; a pseudo-version, which Go records for a build in
// a checkout or a go install at a commit, and git describe's form name no
// tag on the mirror, so they name main. The planted break: any version that
// starts with v taken as a tag, as before the review.
func TestReferenceURL(t *testing.T) {
	const main = "https://github.com/f-klement/errata/blob/main/docs/cli.md"
	for version, want := range map[string]string{
		"v0.2.4":                               "https://github.com/f-klement/errata/blob/v0.2.4/docs/cli.md",
		"v0.3.0-rc.1":                          "https://github.com/f-klement/errata/blob/v0.3.0-rc.1/docs/cli.md",
		"dev":                                  main,
		"v0.2.3-0.20261103212526-8ade832ca96d": main,
		"v0.2.3-0.20261103212526-8ade832ca96d+dirty": main,
		"v0.2.4-3-g8ade832":                          main,
		"v0.2.4-rc.1x":                               main,
		"xv0.2.4":                                    main,
	} {
		if got := referenceURL(version); got != want {
			t.Errorf("referenceURL(%q) = %q, want %q", version, got, want)
		}
	}
	if longest := referenceURL("v99.99.99-rc.99"); len(longest) > helpWidth {
		t.Errorf("the footer of a release, %q, is %d columns", longest, len(longest))
	}
}

func TestOrList(t *testing.T) {
	for want, names := range map[string][]string{
		"":             nil,
		"text":         {"text"},
		"json or text": {"json", "text"},
		"a, b or c":    {"a", "b", "c"},
		"a, b, c or d": {"a", "b", "c", "d"},
	} {
		if got := orList(names); got != want {
			t.Errorf("orList(%q) = %q, want %q", names, got, want)
		}
	}
}

// docs/cli.md: the first line names the target that writes it, then a table
// of contents with an anchor per command, then each command's help as errata
// prints it, under a heading the anchor names, without the line naming
// docs/cli.md. The planted break: the table of contents left out.
func TestReference(t *testing.T) {
	ref, err := Reference()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(ref)
	if first, _, _ := strings.Cut(doc, "\n"); !strings.Contains(first, "generated by `make cli-doc`") || len(first) > helpWidth {
		t.Errorf("the first line is %q, want it to say make cli-doc generates the file in %d columns", first, helpWidth)
	}
	if strings.Contains(doc, "/docs/cli.md") {
		t.Error("docs/cli.md names its own address, which the help gives only with a version")
	}
	for _, p := range commands(t) {
		title := strings.Join(append([]string{"errata"}, p...), " ")
		anchor := strings.ReplaceAll(title, " ", "-")
		if !strings.Contains(doc, "\n- ["+title+"](#"+anchor+")\n") {
			t.Errorf("the table of contents has no entry for %s", title)
		}
		help := printedHelp(t, p)
		help = help[:strings.LastIndex(help, "\nThe full command line reference is docs/cli.md:")]
		if strings.Contains(help, "```") {
			t.Errorf("the help of %s holds a code fence, which would end its block", title)
		}
		if !strings.Contains(doc, "\n## "+title+"\n\n```text\n"+help+"```\n") {
			t.Errorf("docs/cli.md does not hold the help of %s under its heading", title)
		}
	}
	if again, err := Reference(); err != nil || !bytes.Equal(again, ref) {
		t.Error("Reference is not the same twice")
	}
}

// A bool flag prints as --name, with no value, as it did up to pflag
// v1.0.10; pflag v1.1.0 prints --name[=true|false], and a later pflag
// that changes the form again fails here rather than changing the help
// and docs/cli.md unseen. --name alone still sets the flag and
// --name=false clears it. Planted break: plainBools not called, which
// printed every bool flag, --help included, with [=true|false].
func TestABoolFlagPrintsWithoutAValue(t *testing.T) {
	for _, p := range commands(t) {
		if help := printedHelp(t, p); strings.Contains(help, "[=") {
			t.Errorf("errata %v --help prints a flag with a value form:\n%s", p, help)
		}
	}
	check := printedHelp(t, []string{"check"})
	for _, line := range []string{
		"\n      --all-values           a false assert prints every value its rule\n",
		"\n      --fail-on-skip         exit 1 when a check was skipped, as on an unknown\n",
		"\n  -h, --help                 help for check\n",
	} {
		if !strings.Contains(check, line) {
			t.Errorf("errata check --help has no line %q:\n%s", line, check)
		}
	}
	root := newRoot(config.Load, io.Discard, io.Discard)
	cmd, _, err := root.Find([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]bool{"--all-values": true, "--all-values=false": false, "--all-values=true": true} {
		flags := cmd.Flags()
		if err := flags.Parse([]string{args}); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if got, err := flags.GetBool("all-values"); err != nil || got != want {
			t.Errorf("%s: all-values %v, %v; want %v", args, got, err, want)
		}
	}
}
