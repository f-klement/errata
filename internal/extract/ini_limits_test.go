package extract

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// iniKinds is each kind this file adds, with file f and the max_bytes n.
func iniKinds(f string, n int64) map[string]Spec {
	return map[string]Spec{
		"ini":        {INI: &INI{File: f, Key: "a", MaxBytes: n}},
		"properties": {Properties: &Properties{File: f, Key: "a", MaxBytes: n}},
	}
}

// The planted breaks of TestBuildRefusesFileOutsideRepository and
// TestBuildRefusesMaxBytesOutsideItsRange, for the ini and properties
// kinds: each refuses each one, under its own name.
func TestBuildRefusesINISourcesOutsideTheirRange(t *testing.T) {
	for _, f := range []string{"/etc/passwd", "../secret.ini", "a/../../secret.ini", ".."} {
		for kind, s := range iniKinds(f, 0) {
			_, err := Build(s)
			wantError(t, err, kind+": file \""+f+"\" is outside the repository")
		}
	}
	for _, n := range []int64{-1, maxMaxBytes + 1, math.MaxInt64} {
		for kind, s := range iniKinds("f", n) {
			_, err := Build(s)
			want := kind + ": max_bytes is " + strconv.FormatInt(n, 10) + "; write a number of bytes from 1 to 67108864, or leave it out for 33554432"
			if err == nil || err.Error() != want {
				t.Errorf("error %v, want %q", err, want)
			}
		}
	}
	for kind, s := range iniKinds("f", maxMaxBytes) {
		if _, err := Build(s); err != nil {
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
		got := mustRun(t, s, repo(t, map[string]string{"f": body}))
		if texts(got) != "1@2" {
			t.Errorf("%s: got %s", kind, texts(got))
		}
		_, err := run(t, s, repo(t, map[string]string{"f": " " + body}))
		want := "read f: the file is 1001 bytes, over the limit of 1000 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
	root := repo(t, nil)
	if err := os.WriteFile(filepath.Join(root, "huge"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, "huge"), defaultMaxBytes+1); err != nil {
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
	spec Spec
}{
	{"plain", func(n int) string { return strings.Repeat("a=1\n", n) }, ini("f", "", "a")},
	{"git", func(n int) string { return "[s]\n" + strings.Repeat("a\n", n) }, Spec{INI: &INI{File: "f", Dialect: "git", Section: "s", Key: "a"}}},
	{"ansible", func(n int) string { return strings.Repeat("h\n", n) }, Spec{INI: &INI{File: "f", Dialect: "ansible", Section: "ungrouped"}}},
	{"php", func(n int) string { return strings.Repeat("a=1\n", n) }, Spec{INI: &INI{File: "f", Dialect: "php", Key: "a"}}},
	{"properties", func(n int) string { return strings.Repeat("a\n", n) }, Spec{Properties: &Properties{File: "f", Key: "a"}}},
}

// checkValueLimit reads limit values and refuses one more, the planted
// break, with the value limit's message, through an extractor whose limit
// is limit.
func checkValueLimit(t *testing.T, limit int) {
	t.Helper()
	for _, c := range iniValueLimitCases {
		body := c.body
		e, err := Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(entryExtractor)
		if limit != maxValues {
			x.valueLimit = limit
		}
		if got, err := x.fromData([]byte(body(limit))); err != nil || len(got) != limit {
			t.Errorf("%s: %d values, %v, want %d", c.name, len(got), err, limit)
		}
		_, err = x.fromData([]byte(body(limit + 1)))
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
	checkValueLimit(t, maxValues)
}

// The value limit of an extractor errata builds is maxValues; a test that
// sets valueLimit is the only one with another.
func TestINIValueLimitIsTheConstant(t *testing.T) {
	e, err := Build(ini("f", "s", "k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.(entryExtractor).valuesAllowed(); got != maxValues {
		t.Errorf("limit %d, want %d", got, maxValues)
	}
	if got := (entryExtractor{valueLimit: 7}).valuesAllowed(); got != 7 {
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
		spec       Spec
		limit      int64
	}{
		{".gitconfig", "[a]\n" + strings.Repeat("k\n", 9998), gitSpec(".gitconfig", "a", nil, "k"), 20000},
		{"a.properties", "a=" + strings.Repeat("\xff", 998), props("a"), 1000},
	} {
		s := c.spec
		if s.INI != nil {
			x := *s.INI
			x.MaxBytes = c.limit
			s = Spec{INI: &x}
		} else {
			x := *s.Properties
			x.MaxBytes = c.limit
			s = Spec{Properties: &x}
		}
		_, err := run(t, s, repo(t, map[string]string{c.file: c.body}))
		want := c.file + ": the values the extractor selects hold more than " + strconv.FormatInt(c.limit, 10) + " bytes in all, the extractor's max_bytes"
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", c.file, err, want)
		}
	}
}

// A file name chooses the dialect, and dialect: overrides it. The planted
// breaks are names close to each rule's that it must not take.
func TestDialectForFileNames(t *testing.T) {
	for file, want := range map[string]string{
		".gitconfig": "git", "home/.gitconfig": "git", ".gitmodules": "git", "etc/gitconfig": "git", "work.gitconfig": "git",
		".git/config": "git", "x/.config/git/config": "git",
		"hosts": "ansible", "hosts.ini": "ansible", "deploy/inventory": "ansible", "inventory.ini": "ansible",
		"inventories/prod/web": "ansible", "inventory/group.ini": "ansible",
		"tox.ini": "tox", "a/tox.ini": "tox", "php.ini": "php", "etc/php/8.3/cli/php.ini": "php", "etc/php/8.3/fpm/conf.d/10-opcache.ini": "php",
		"setup.cfg": "plain", "tox.ini.bak": "plain", "php.ini-development": "php", "php.ini.dist": "php", "etc/php.ini-production": "php", "my-php.ini": "plain", "php.ini.j2": "plain", "php.ini.tftpl": "plain", "php.ini.tpl": "plain", "etc/php.ini-production.jinja2": "plain", "etc/conf.d/x.ini": "plain", "config": "plain", "gitconfig.bak": "plain",
		"git/config.ini": "plain", "hosts.txt": "plain", "inventory2/x": "plain", ".gitignore": "plain",
	} {
		if got := dialectFor(file).name; got != want {
			t.Errorf("%s: %s, want %s", file, got, want)
		}
	}
	got := iniValues(t, "hosts", "[s]\nk = 1\n", Spec{INI: &INI{File: "hosts", Dialect: "plain", Section: "s", Key: "k"}})
	if got != "1@2" {
		t.Errorf("dialect: plain on hosts: got %q", got)
	}
}

// Two specs that read one place have one Place: the file cleaned, the
// dialect chosen by name or written out, and a git name in any case or
// written as section.subsection. Specs that read different places differ.
func TestINIPlace(t *testing.T) {
	same := [][2]Spec{
		{ini("f.ini", "s", "k"), {INI: &INI{File: "./f.ini", Dialect: "plain", Section: "s", Key: "k", MaxBytes: 10}}},
		{gitSpec(".gitconfig", "Remote", ptr("origin"), "URL"), gitSpec("a/../.gitconfig", "remote.origin", nil, "url")},
		{{Properties: &Properties{File: "a.properties", Key: "k"}}, {Properties: &Properties{File: "./a.properties", Key: "k", MaxBytes: 5}}},
		{{INI: &INI{File: "hosts", Section: "web", Key: "k"}}, {INI: &INI{File: "hosts", Section: "web:hosts", Key: "k"}}},
		{ini("tox.ini", "testenv", "Deps"), ini("tox.ini", "testenv", "deps")},
	}
	for _, p := range same {
		if a, b := p[0].Place(), p[1].Place(); !reflect.DeepEqual(a, b) {
			t.Errorf("%+v and %+v: places differ", a, b)
		}
	}
	differ := [][2]Spec{
		{ini("f.ini", "s", "k"), ini("f.ini", "s", "K")},
		{gitSpec(".gitconfig", "remote", ptr("origin"), "url"), gitSpec(".gitconfig", "remote", ptr("Origin"), "url")},
		{ini("hosts", "s", "k"), {INI: &INI{File: "hosts", Dialect: "plain", Section: "s", Key: "k"}}},
		{{Properties: &Properties{File: "a.properties", Key: "k"}}, {Properties: &Properties{File: "a.properties", Key: "K"}}},
	}
	for _, p := range differ {
		if reflect.DeepEqual(p[0].Place(), p[1].Place()) {
			t.Errorf("%+v and %+v: one place", p[0], p[1])
		}
	}
}
