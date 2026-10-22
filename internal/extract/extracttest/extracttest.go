// Package extracttest holds what the tests of internal/extract and of its
// readers share: a repository written from a map, the text of the values an
// extractor returns, inputs repeated by shape, and the switches that tell a
// test it runs under the race detector or under Gremlins. Only test files
// import it.
package extracttest

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/f-klement/errata/internal/check"
)

// Repo writes files into a new repository root and returns it.
func Repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Run builds s with build and extracts from root.
func Run[S any](t *testing.T, build func(S) (check.Extractor, error), s S, root string) ([]check.Value, error) {
	t.Helper()
	e, err := build(s)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return e.Extract(root)
}

// MustRun is Run for an extraction that must succeed.
func MustRun[S any](t *testing.T, build func(S) (check.Extractor, error), s S, root string) []check.Value {
	t.Helper()
	got, err := Run(t, build, s, root)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return got
}

// Texts lists the values' text and line as "text@line".
func Texts(vs []check.Value) string {
	var b strings.Builder
	for i, v := range vs {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(v.Text + "@" + strconv.Itoa(v.At.Line))
	}
	return b.String()
}

// WantError fails unless err is set and its message holds want.
func WantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// Symlink makes link, inside root, point at target.
func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// Nest wraps inner in n pairs of brackets.
func Nest(n int, inner string) string {
	return strings.Repeat("[", n) + inner + strings.Repeat("]", n)
}

// UTF16LE is s in UTF-16, little end first, after a byte order mark, which
// yaml.v3 reads as UTF-16.
func UTF16LE(s string) string {
	b := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

// RepeatLines repeats line(i) for i from 0 to n-1.
func RepeatLines(n int, line func(i int) string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(line(i))
	}
	return b.String()
}

// List is n copies of item, separated by a comma and a space.
func List(n int, item string) string {
	return strings.TrimSuffix(strings.Repeat(item+", ", n), ", ")
}

// KeyLines is n lines that each set a key of its own, k0 to k<n-1>, to 1,
// with sep between the key and the value.
func KeyLines(n int, sep string) string {
	return RepeatLines(n, func(i int) string { return "k" + strconv.Itoa(i) + sep + "1\n" })
}

// Allocated is the bytes f allocates.
func Allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
