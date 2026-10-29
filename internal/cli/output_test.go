package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// programCheck is a repo check that runs run from the root.
func programCheck(id string, run ...string) check.Check {
	return check.Check{ID: id, Reason: "r", Scope: check.ScopeRepo, Kind: &check.Program{Run: run}, Timeout: 10 * time.Second}
}

// checkRun is what one errata check printed and exited with, and how many
// times it loaded the config.
type checkRun struct {
	code           int
	stdout, stderr string
	loads          int
}

// runCheck runs errata check with args from root, against a loader that
// answers with checks. The tests give --output absolute paths under root,
// since a relative one is taken from the test's working directory.
func runCheck(t *testing.T, root string, checks []check.Check, args ...string) checkRun {
	t.Helper()
	var calls []loadCall
	var out, errOut bytes.Buffer
	code := execute(context.Background(), append([]string{"check", "--root", root}, args...), &out, &errOut, recordingLoader(&calls, checks, nil))
	return checkRun{code: code, stdout: out.String(), stderr: errOut.String(), loads: len(calls)}
}

// junitTime matches the attribute that holds a check's duration, the one part
// of a report two runs do not share.
var junitTime = regexp.MustCompile(` time="[^"]*"`)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// leftovers returns the temporary files --output left in dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// Each --output file holds what --format of that name prints on stdout, and
// stdout keeps the text report, with the exit code of a run without --output,
// on a pass, an erratum and an unknown alike. The planted breaks: commitOutputs
// called only when the run passed, which leaves no file on an erratum or an
// unknown; and commitOutput writing the format of --format in place of its
// own, which puts text in errata.xml.
func TestOutputWritesEachFormatWhateverTheVerdict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check check.Check
		code  int
	}{
		{"pass", programCheck("good", "true"), ExitOK},
		{"erratum", programCheck("bad", "false"), ExitErrata},
		{"unknown", programCheck("broken", "errata-no-such-program"), ExitErrata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			checks := []check.Check{tc.check}
			want := map[string]string{}
			for _, format := range []string{"text", "junit", "sarif"} {
				r := runCheck(t, root, checks, "--format", format)
				if r.code != tc.code {
					t.Fatalf("--format %s: exit %d, want %d; stderr %q", format, r.code, tc.code, r.stderr)
				}
				want[format] = r.stdout
			}
			xml, sarif := filepath.Join(root, "errata.xml"), filepath.Join(root, "errata.sarif")
			r := runCheck(t, root, checks, "--output", "junit="+xml, "-o", "sarif="+sarif)
			if r.code != tc.code {
				t.Fatalf("exit %d, want %d; stderr %q", r.code, tc.code, r.stderr)
			}
			if r.stdout != want["text"] {
				t.Errorf("stdout:\n%s\nwant the text report:\n%s", r.stdout, want["text"])
			}
			if got := readFile(t, xml); junitTime.ReplaceAllString(got, "") != junitTime.ReplaceAllString(want["junit"], "") {
				t.Errorf("errata.xml:\n%s\nwant what --format junit prints:\n%s", got, want["junit"])
			}
			if got := readFile(t, sarif); got != want["sarif"] {
				t.Errorf("errata.sarif:\n%s\nwant what --format sarif prints:\n%s", got, want["sarif"])
			}
			if left := leftovers(t, root); len(left) != 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
}

// --format picks what goes to stdout and --output adds files, text among
// them.
func TestOutputBesideFormat(t *testing.T) {
	root := t.TempDir()
	checks := []check.Check{programCheck("bad", "false")}
	text := runCheck(t, root, checks, "--format", "text").stdout
	sarif := runCheck(t, root, checks, "--format", "sarif").stdout

	out := filepath.Join(root, "errata.txt")
	r := runCheck(t, root, checks, "--format", "sarif", "--output", "text="+out)
	if r.code != ExitErrata {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, ExitErrata, r.stderr)
	}
	if r.stdout != sarif {
		t.Errorf("stdout is not the SARIF --format asks for:\n%s", r.stdout)
	}
	if got := readFile(t, out); got != text {
		t.Errorf("errata.txt:\n%s\nwant the text report:\n%s", got, text)
	}
}

// A file at PATH is replaced by the report, whole. The planted break: the
// rename dropped, which leaves the old file.
func TestOutputReplacesAnExistingFile(t *testing.T) {
	root := t.TempDir()
	xml := filepath.Join(root, "errata.xml")
	old := strings.Repeat("an older and longer report\n", 100)
	if err := os.WriteFile(xml, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runCheck(t, root, []check.Check{programCheck("good", "true")}, "-o", "junit="+xml)
	if r.code != ExitOK {
		t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
	}
	got := readFile(t, xml)
	if !strings.HasPrefix(got, "<?xml") || strings.Contains(got, "older") {
		t.Errorf("errata.xml is not the new report alone:\n%s", got)
	}
}

// An output that names the config file is refused with exit 2 before
// anything runs, whether errata found the config in the root or was given it
// with --config, and spelled another way. Without the refusal a passing run
// would replace the config with its report and exit 0. The planted break:
// refuseConfigOutput's comparison made false.
func TestOutputRefusesTheConfigFile(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "errata.yaml")
	if err := os.WriteFile(cfg, []byte("checks: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other.yaml")
	for _, args := range [][]string{
		{"-o", "text=" + cfg},
		{"-o", "text=" + filepath.Join(root, ".", "errata.yaml")},
		{"-c", other, "-o", "junit=" + other},
	} {
		checks := []check.Check{programCheck("touch", "touch", "ran")}
		r := runCheck(t, root, checks, args...)
		if r.code != ExitUsage {
			t.Fatalf("%v: exit %d, want %d", args, r.code, ExitUsage)
		}
		value := args[len(args)-1]
		if want := fmt.Sprintf("errata: --output %q names the config file", value); !strings.Contains(r.stderr, want) {
			t.Errorf("%v: stderr %q, want %q", args, r.stderr, want)
		}
		if _, err := os.Stat(filepath.Join(root, "ran")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: the check ran: %v", args, err)
		}
		if got := readFile(t, cfg); got != "checks: []\n" {
			t.Errorf("%v: the config is now %q", args, got)
		}
	}
}

// A value of --output that names no file errata can write in a format it
// knows is a usage error: exit 2 with the reason, before the config is
// loaded, so nothing runs. The planted breaks, each condition that refuses
// a case made false: format == "", path == "" (which also refuses a value
// without "="), !known, dup, and the paths compared without filepath.Abs.
func TestOutputRefusesABadValue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"no equals sign", []string{"errata.xml"}, `--output "errata.xml": want FORMAT=PATH`},
		{"empty format", []string{"=errata.xml"}, `--output "=errata.xml": want FORMAT=PATH`},
		{"empty path", []string{"junit="}, `--output "junit=": want FORMAT=PATH`},
		{"unknown format", []string{"xml=errata.xml"}, `--output "xml=errata.xml": unknown format "xml", want one of junit, sarif, text`},
		{"same path twice", []string{"junit=errata.xml", "sarif=./errata.xml"}, `--output "junit=errata.xml" and --output "sarif=./errata.xml" name the same file`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			args := []string{}
			for _, v := range tc.values {
				args = append(args, "--output", v)
			}
			r := runCheck(t, root, []check.Check{programCheck("good", "true")}, args...)
			if r.code != ExitUsage {
				t.Fatalf("exit %d, want %d", r.code, ExitUsage)
			}
			if !strings.Contains(r.stderr, "errata: "+tc.want) {
				t.Errorf("stderr %q, want it to hold %q", r.stderr, tc.want)
			}
			if r.loads != 0 || r.stdout != "" {
				t.Errorf("the config was loaded %d times and stdout is %q, want neither", r.loads, r.stdout)
			}
		})
	}
}

// One format to two files is two outputs, not a duplicate.
func TestOutputOneFormatToTwoFiles(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.xml"), filepath.Join(root, "b.xml")
	r := runCheck(t, root, []check.Check{programCheck("good", "true")}, "-o", "junit="+a, "-o", "junit="+b)
	if r.code != ExitOK {
		t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
	}
	if readFile(t, a) == "" || readFile(t, b) == "" {
		t.Error("an output file is empty")
	}
}

// A path errata cannot create a file next to, or one that is a directory,
// stops the run with exit 2 before any check runs, and leaves no temporary
// file of an output opened before it. The check would create ran in the root.
// The planted breaks: openOutputs not removing the files it created, which
// leaves one in the root; and the IsDir test dropped, which runs the check.
func TestOutputThatCannotBeOpenedRunsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(root string) string
		want string
	}{
		{"missing directory", func(root string) string { return filepath.Join(root, "missing", "errata.xml") }, "no such file or directory"},
		{"a directory", func(root string) string { return root }, "is a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bad := tc.path(root)
			checks := []check.Check{programCheck("touch", "touch", "ran")}
			r := runCheck(t, root, checks, "-o", "sarif="+filepath.Join(root, "first.sarif"), "-o", "junit="+bad)
			if r.code != ExitUsage {
				t.Fatalf("exit %d, want %d", r.code, ExitUsage)
			}
			if want := fmt.Sprintf("errata: --output %q: ", "junit="+bad); !strings.Contains(r.stderr, want) || !strings.Contains(r.stderr, tc.want) {
				t.Errorf("stderr %q, want %q and %q", r.stderr, want, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "ran")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the check ran: %v", err)
			}
			if left := leftovers(t, root); len(left) != 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
}

// A file that cannot be written after the run, here because the check made a
// directory of its path, is exit 2 with the path and the reason, even on a
// run with an erratum. The text report still goes to stdout, the other
// output is still written, and no temporary file is left. The planted
// breaks: commitOutputs returning on the first failure, which leaves b.sarif
// unwritten; commitOutput not removing the temporary file on a failure; and
// the stdout report written only when the outputs succeed.
func TestOutputThatCannotBeWrittenFailsTheRunAndSparesTheRest(t *testing.T) {
	root := t.TempDir()
	xml, sarif := filepath.Join(root, "a.xml"), filepath.Join(root, "b.sarif")
	checks := []check.Check{programCheck("bad", "mkdir", xml), programCheck("bad2", "false")}
	r := runCheck(t, root, checks, "-o", "junit="+xml, "-o", "sarif="+sarif)
	if r.code != ExitUsage {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, ExitUsage, r.stderr)
	}
	if want := fmt.Sprintf("errata: --output %q: rename ", "junit="+xml); !strings.Contains(r.stderr, want) {
		t.Errorf("stderr %q, want it to hold %q", r.stderr, want)
	}
	if !strings.Contains(r.stdout, "FALSE bad2 (repo)") {
		t.Errorf("stdout %q, want the text report", r.stdout)
	}
	if !strings.Contains(readFile(t, sarif), `"ruleId": "bad2"`) {
		t.Errorf("b.sarif lacks the erratum")
	}
	if left := leftovers(t, root); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// failingWriter refuses every write, as a closed stdout does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

// A stdout that cannot be written is exit 2, as before --output, and the files
// are written all the same. The planted break: RunE returning on the stdout
// error, which writes no file.
func TestOutputIsWrittenWhenStdoutFails(t *testing.T) {
	root := t.TempDir()
	xml := filepath.Join(root, "a.xml")
	var calls []loadCall
	var errOut bytes.Buffer
	args := []string{"check", "--root", root, "-o", "junit=" + xml}
	code := execute(context.Background(), args, failingWriter{}, &errOut, recordingLoader(&calls, []check.Check{programCheck("good", "true")}, nil))
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut.String(), "errata: stdout closed") {
		t.Errorf("stderr %q", errOut.String())
	}
	if !strings.Contains(readFile(t, xml), `<testsuite name="errata" tests="1"`) {
		t.Error("a.xml lacks the report")
	}
}

// Two failures are named on one line after one "errata: ".
func TestJoinErrors(t *testing.T) {
	if err := joinErrors(nil); err != nil {
		t.Errorf("joinErrors(nil) = %v, want nil", err)
	}
	err := joinErrors([]error{errors.New("a"), errors.New("b")})
	if err == nil || err.Error() != "a; b" {
		t.Errorf("joinErrors = %v, want a; b", err)
	}
}
