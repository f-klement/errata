package core_test

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/properties"
)

// iniKinds is each kind this file adds, with file f and the max_bytes n.
func iniKinds(f string, n int64) map[string]extract.Spec {
	return map[string]extract.Spec{
		"ini":        extract.Of(ini.Kind, ini.Spec{File: f, Key: "a", MaxBytes: n}),
		"properties": extract.Of(properties.Kind, properties.Spec{File: f, Key: "a", MaxBytes: n}),
	}
}

// The planted breaks of TestBuildRefusesFileOutsideRepository and
// TestBuildRefusesMaxBytesOutsideItsRange, for the ini and properties
// kinds: each refuses each one, under its own name.
func TestBuildRefusesINISourcesOutsideTheirRange(t *testing.T) {
	for _, f := range []string{"/etc/passwd", "../secret.ini", "a/../../secret.ini", ".."} {
		for kind, s := range iniKinds(f, 0) {
			_, err := extract.Build(s)
			extracttest.WantError(t, err, kind+": file \""+f+"\" is outside the repository")
		}
	}
	for _, n := range []int64{-1, core.MaxMaxBytes + 1, math.MaxInt64} {
		for kind, s := range iniKinds("f", n) {
			_, err := extract.Build(s)
			want := kind + ": max_bytes is " + strconv.FormatInt(n, 10) + "; write a number of bytes from 1 to 67108864, or leave it out for 33554432"
			if err == nil || err.Error() != want {
				t.Errorf("error %v, want %q", err, want)
			}
		}
	}
	for kind, s := range iniKinds("f", core.MaxMaxBytes) {
		if _, err := extract.Build(s); err != nil {
			t.Errorf("%s: max_bytes at the ceiling: %v", kind, err)
		}
	}
}

// Each kind reads a file of exactly max_bytes, with its value at the end,
// and refuses one byte more, the planted break, before reading it; and one
// byte over the default without max_bytes.
func TestINIFileSizeLimits(t *testing.T) {
	const limit = 1000
	body := "#" + strings.Repeat(" ", limit-5) + "\na=1"
	for kind, s := range iniKinds("f", limit) {
		got := mustRun(t, s, extracttest.Repo(t, map[string]string{"f": body}))
		if extracttest.Texts(got) != "1@2" {
			t.Errorf("%s: got %s", kind, extracttest.Texts(got))
		}
		_, err := run(t, s, extracttest.Repo(t, map[string]string{"f": " " + body}))
		want := "read f: the file is 1001 bytes, over the limit of 1000 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
	root := extracttest.Repo(t, nil)
	if err := os.WriteFile(filepath.Join(root, "huge"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, "huge"), core.DefaultMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	for kind, s := range iniKinds("huge", 0) {
		_, err := run(t, s, root)
		want := "read huge: the file is 33554433 bytes, over the limit of 33554432 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
}

// iniValueLimitCases are a file of n values for each dialect and the
// properties reader, and the spec that selects them. tox refuses a key set
// twice, so a tox spec selects one value at most.
var iniValueLimitCases = []struct {
	name string
	body func(n int) string
	spec extract.Spec
}{
	{"plain", func(n int) string { return strings.Repeat("a=1\n", n) }, iniSpec("f", "", "a")},
	{"git", func(n int) string { return "[s]\n" + strings.Repeat("a\n", n) }, extract.Of(ini.Kind, ini.Spec{File: "f", Dialect: "git", Section: "s", Key: "a"})},
	{"ansible", func(n int) string { return strings.Repeat("h\n", n) }, extract.Of(ini.Kind, ini.Spec{File: "f", Dialect: "ansible", Section: "ungrouped"})},
	{"php", func(n int) string { return strings.Repeat("a=1\n", n) }, extract.Of(ini.Kind, ini.Spec{File: "f", Dialect: "php", Key: "a"})},
	{"properties", func(n int) string { return strings.Repeat("a\n", n) }, extract.Of(properties.Kind, properties.Spec{File: "f", Key: "a"})},
}

// checkValueLimit reads limit values and refuses one more, the planted
// break, with the value limit's message, through an extractor whose limit
// is limit.
func checkValueLimit(t *testing.T, limit int) {
	t.Helper()
	for _, c := range iniValueLimitCases {
		body := c.body
		e, err := extract.Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(core.EntryExtractor)
		if limit != core.MaxValues {
			x.ValueLimit = limit
		}
		if got, err := x.FromData([]byte(body(limit))); err != nil || len(got) != limit {
			t.Errorf("%s: %d values, %v, want %d", c.name, len(got), err, limit)
		}
		_, err = x.FromData([]byte(body(limit + 1)))
		want := "f: the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", c.name, err, want)
		}
	}
}

// Each dialect and the properties reader return as many values as the
// extractor's limit, and refuse one more: in make test at a limit of 1000.
func TestINIValueLimit(t *testing.T) { checkValueLimit(t, 1000) }

// The same at maxValues, the limit errata runs with.
func TestINIValueLimitAtItsSize(t *testing.T) {
	slowOnly(t)
	checkValueLimit(t, core.MaxValues)
}

// The value limit of an extractor errata builds is maxValues; a test that
// sets valueLimit is the only one with another.
func TestINIValueLimitIsTheConstant(t *testing.T) {
	e, err := extract.Build(iniSpec("f", "s", "k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.(core.EntryExtractor).ValuesAllowed(); got != core.MaxValues {
		t.Errorf("limit %d, want %d", got, core.MaxValues)
	}
	if got := (core.EntryExtractor{ValueLimit: 7}).ValuesAllowed(); got != 7 {
		t.Errorf("limit %d, want 7", got)
	}
}

// The values hold at most max_bytes in all, as every extractor's do. The
// planted breaks: a git file whose keys have no =, each of which reads as
// true, four bytes for its two, and a properties file of bytes that are not
// UTF-8, each of which reads as U+FFFD, three bytes for one; both read
// before, 399992 and 2997 bytes from 200000 and 1000, and are read here at a
// tenth of the git file.
func TestINIValueBytesLimit(t *testing.T) {
	for _, c := range []struct {
		file, body string
		spec       func(limit int64) extract.Spec
		limit      int64
	}{
		{".gitconfig", "[a]\n" + strings.Repeat("k\n", 9998), func(n int64) extract.Spec {
			return extract.Of(ini.Kind, ini.Spec{File: ".gitconfig", Section: "a", Key: "k", MaxBytes: n})
		}, 20000},
		{"a.properties", "a=" + strings.Repeat("\xff", 998), func(n int64) extract.Spec {
			return extract.Of(properties.Kind, properties.Spec{File: "a.properties", Key: "a", MaxBytes: n})
		}, 1000},
	} {
		s := c.spec(c.limit)
		_, err := run(t, s, extracttest.Repo(t, map[string]string{c.file: c.body}))
		want := c.file + ": the values the extractor selects hold more than " + strconv.FormatInt(c.limit, 10) + " bytes in all, the extractor's max_bytes"
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", c.file, err, want)
		}
	}
}
