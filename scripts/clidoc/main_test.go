package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reference() ([]byte, error) { return []byte("the help\n"), nil }

// clidoc writes the reference to the file it names, and --check passes on
// that file.
func TestWriteThenCheck(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cli.md")
	var out, errs bytes.Buffer
	if code := run([]string{file}, &out, &errs, reference); code != 0 {
		t.Fatalf("write: exit %d, stderr %q", code, errs.String())
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "the help\n" {
		t.Fatalf("wrote %q, %v", got, err)
	}
	if code := run([]string{"--check", file}, &out, &errs, reference); code != 0 {
		t.Fatalf("check of the written file: exit %d, stderr %q", code, errs.String())
	}
}

// --check refuses a file that differs from the reference, and one that is
// not there, and names the target that rewrites it. The planted break: the
// comparison dropped, so every file passed.
func TestCheckRefuses(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "cli.md")
	if err := os.WriteFile(stale, []byte("the help of the last release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{stale, filepath.Join(dir, "missing.md")} {
		var out, errs bytes.Buffer
		code := run([]string{"--check", file}, &out, &errs, reference)
		if code != 1 || !strings.Contains(errs.String(), "REFUSED: "+file) || !strings.Contains(errs.String(), "Run make cli-doc") ||
			!strings.Contains(errs.String(), "delete the tag and set it again") {
			t.Errorf("--check %s: exit %d, stderr %q, want 1 and REFUSED naming make cli-doc", file, code, errs.String())
		}
	}
}

func TestUsageAndFailures(t *testing.T) {
	broken := func() ([]byte, error) { return nil, errors.New("no help") }
	file := filepath.Join(t.TempDir(), "cli.md")
	for _, tc := range []struct {
		args []string
		ref  func() ([]byte, error)
		want int
		msg  string
	}{
		{nil, reference, 2, "usage:"},
		{[]string{"a", "b"}, reference, 2, "usage:"},
		{[]string{"--check"}, reference, 2, "usage:"},
		{[]string{"-x"}, reference, 2, "usage:"},
		{[]string{file}, broken, 1, "no help"},
		{[]string{"--check", file}, broken, 1, "no help"},
		{[]string{filepath.Join(file, "under-a-file")}, reference, 1, "clidoc:"},
	} {
		var out, errs bytes.Buffer
		if code := run(tc.args, &out, &errs, tc.ref); code != tc.want || !strings.Contains(errs.String(), tc.msg) {
			t.Errorf("clidoc %q: exit %d, stderr %q, want %d and %q", tc.args, code, errs.String(), tc.want, tc.msg)
		}
	}
}
