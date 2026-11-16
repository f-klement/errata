package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// built builds the command in this directory to a binary of its own.
func built(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testreach")
	if out, err := exec.CommandContext(t.Context(), goBin(t), "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building testreach: %v\n%s", err, out)
	}
	return bin
}

// The built binary, as scripts/mutation.sh runs it, exits and writes as
// the command line asks: 2 and the usage on a misuse, 1 and the error when
// index fails, 3 and the reason when the package gets no index, 0 when it
// does, and a run goes to go, here missing.
func TestTheBinaryExitsAsTheCommandLineAsks(t *testing.T) {
	bin := built(t)
	cc := casecoverBin(t)
	none := module(t, map[string]string{"p.go": "package p\n"})
	one := module(t, map[string]string{
		"p.go":      "package p\n",
		"p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) {}\n",
	})
	missing := filepath.Join(t.TempDir(), "go")
	usage := usageText + "\n"
	for _, tc := range []struct {
		args           []string
		rc             int
		stdout, stderr string
	}{
		{nil, 2, "", usage},
		{[]string{"bogus"}, 2, "", usage},
		{[]string{"run", "go", "root"}, 2, "", usage},
		{[]string{"redundant"}, 2, "", usage},
		{[]string{"index", goBin(t), cc, "", one, "p", t.TempDir(), "1"}, 2, "", usage},
		{[]string{"index", goBin(t), cc, "", one, "p", t.TempDir(), "0", "60"}, 2, "", usage},
		{[]string{"index", goBin(t), cc, "", one, "p", t.TempDir(), "1", "0"}, 2, "", usage},
		{[]string{"index", missing, cc, "", one, "p", t.TempDir(), "1", "60"}, 1, "", "testreach: go list ./p: "},
		{[]string{"index", goBin(t), cc, "", none, "p", t.TempDir(), "1", "60"}, 3, "it has no test files\n", ""},
		{[]string{"index", goBin(t), cc, "", one, "p", t.TempDir(), "1", "60"}, 0, "", ""},
		{[]string{"run", missing, "root", "idx"}, 2, "", "testreach: running go: "},
	} {
		cmd := exec.CommandContext(t.Context(), bin, tc.args...)
		var out, errs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errs
		rc := 0
		var exit *exec.ExitError
		if err := cmd.Run(); errors.As(err, &exit) {
			rc = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if rc != tc.rc || out.String() != tc.stdout || !strings.HasPrefix(errs.String(), tc.stderr) || (tc.stderr == "" && errs.Len() > 0) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", tc.args, rc, out.String(), errs.String())
		}
	}
}
