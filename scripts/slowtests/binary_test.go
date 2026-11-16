package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The built binary, as make slow runs it, prints the list and exits 0,
// exits 1 with the error when the tree holds a call the list would miss,
// and 2 with the usage on a misuse.
func TestTheBinaryExitsAsTheListEnds(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "slowtests")
	if out, err := exec.CommandContext(t.Context(), goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building slowtests: %v\n%s", err, out)
	}
	good := tree(t, map[string]string{"internal/p/p_test.go": "package p\n\nfunc TestB(t *testing.T) { slowOnly(t) }\n\nfunc TestA(t *testing.T) { slowOnly(t) }\n"})
	bad := tree(t, map[string]string{"internal/p/p_test.go": "package p\n\nfunc big(t *testing.T) { slowOnly(t) }\n"})
	usage := "usage: slowtests <module root>\n"
	for _, tc := range []struct {
		args           []string
		rc             int
		stdout, stderr string
	}{
		{nil, 2, "", usage},
		{[]string{good, good}, 2, "", usage},
		{[]string{good}, 0, "./internal/p:TestA\n./internal/p:TestB\n", ""},
		{[]string{bad}, 1, "", "slowtests: " + filepath.Join(bad, "internal/p/p_test.go") + ":3:26: big calls slowOnly"},
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
