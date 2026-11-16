package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The built binary, as scripts/mutation.sh runs it, exits 2 with the usage
// on a misuse, 1 with the error when the run fails, and 0 when it appends
// the case blocks to the profile.
func TestTheBinaryExitsAsTheRunEnds(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "casecover")
	if out, err := exec.CommandContext(t.Context(), goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building casecover: %v\n%s", err, out)
	}
	dir := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":      "module example.com/p\n\ngo 1.21\n",
		"p.go":        sign,
		"profile.out": signProfile,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	profile, missing := filepath.Join(dir, "profile.out"), filepath.Join(dir, "missing.out")
	usage := "usage: casecover <module root> <profile>\n"
	for _, tc := range []struct {
		args   []string
		rc     int
		stderr string
	}{
		{nil, 2, usage},
		{[]string{dir, profile, "extra"}, 2, usage},
		{[]string{dir, missing}, 1, "casecover: open " + missing + ": no such file or directory\n"},
		{[]string{dir, profile}, 0, ""},
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
		if rc != tc.rc || out.Len() > 0 || errs.String() != tc.stderr {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", tc.args, rc, out.String(), errs.String())
		}
	}
	got, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got), "example.com/p/p.go:6.2,6.12 0 1\nexample.com/p/p.go:11.2,11.13 0 1\n") {
		t.Errorf("the profile reads\n%s", got)
	}
}
