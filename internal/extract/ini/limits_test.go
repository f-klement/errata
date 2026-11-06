package ini

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A file name chooses the dialect, and dialect: overrides it; a template's
// name without its suffix chooses it. The planted breaks are names close
// to each rule's that it must not take, and the suffix left on.
func TestDialectForFileNames(t *testing.T) {
	for file, want := range map[string]string{
		".gitconfig": "git", "home/.gitconfig": "git", ".gitmodules": "git", "etc/gitconfig": "git", "work.gitconfig": "git",
		".git/config": "git", "x/.config/git/config": "git",
		"hosts": "ansible", "hosts.ini": "ansible", "deploy/inventory": "ansible", "inventory.ini": "ansible",
		"inventories/prod/web": "ansible", "inventory/group.ini": "ansible",
		"tox.ini": "tox", "a/tox.ini": "tox", "php.ini": "php", "etc/php/8.3/cli/php.ini": "php", "etc/php/8.3/fpm/conf.d/10-opcache.ini": "php",
		"setup.cfg": "plain", "tox.ini.bak": "plain", "php.ini-development": "php", "php.ini.dist": "php", "etc/php.ini-production": "php", "my-php.ini": "plain", "php.ini.j2": "php", "php.ini.tftpl": "php", "php.ini.tpl": "php", "etc/php.ini-production.jinja2": "php",
		// #120: a template is read as the file it renders.
		"hosts.j2": "ansible", ".gitconfig.tmpl": "git", "tox.ini.erb": "tox", "etc/php/8.3/fpm/conf.d/10-opcache.ini.j2": "php",
		"setup.cfg.j2": "plain", ".j2": "plain", "x/.tpl": "plain", "hosts.j2.bak": "plain", "etc/conf.d/x.ini": "plain", "config": "plain", "gitconfig.bak": "plain",
		"git/config.ini": "plain", "hosts.txt": "plain", "inventory2/x": "plain", ".gitignore": "plain",
	} {
		if d, _ := chooseDialect("", file); d.name != want {
			t.Errorf("%s: %s, want %s", file, d.name, want)
		}
		if got := dialectFor(core.Untemplated(file)).name; got != want {
			t.Errorf("%s: %s, want %s", file, got, want)
		}
	}
	got := iniValues(t, "hosts", "[s]\nk = 1\n", Spec{File: "hosts", Dialect: "plain", Section: "s", Key: "k"})
	if got != "1@2" {
		t.Errorf("dialect: plain on hosts: got %q", got)
	}
}

// Describe labels the kind with the dialect and the file's base name, and
// gives nothing for a dialect errata does not read. A spec is not literal
// where its dialect's tool or a template engine computes values: git's
// dialect computes none, and plain only in setup.cfg. Planted break: the
// template suffix not asked.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec       Spec
		want       string
		notLiteral bool
	}{
		{Spec{File: "a/pytest.ini"}, "ini, plain dialect on pytest.ini", false},
		{Spec{File: "setup.cfg"}, "ini, plain dialect on setup.cfg", true},
		{Spec{File: "conf/x", Dialect: "git"}, "ini, git dialect on x", false},
		{Spec{File: "conf/x.j2", Dialect: "git"}, "ini, git dialect on x.j2", true},
		{Spec{File: "hosts"}, "ini, ansible dialect on hosts", true},
		{Spec{File: "x.ini", Dialect: "toml"}, "", false},
	} {
		if d := Describe(c.spec); d.Label != c.want || d.NotLiteral != c.notLiteral || d.Shape != check.ShapeFlat || d.Reads != nil {
			t.Errorf("%+v: %+v; want %q, not literal %v", c.spec, d, c.want, c.notLiteral)
		}
	}
}

// Resolve writes out the dialect the file's name chooses, keeps the one a
// spec names, and leaves a name errata does not read as it was, for Build to
// refuse. The planted break is its error check negated, which writes no
// dialect for the first two and stops on the third.
func TestResolve(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{File: "a/pytest.ini"}, "plain"},
		{Spec{File: "hosts"}, "ansible"},
		{Spec{File: "hosts", Dialect: "git"}, "git"},
		{Spec{File: "x.ini", Dialect: "toml"}, "toml"},
	} {
		got := Resolve(c.spec)
		want := c.spec
		want.Dialect, want.MaxBytes = c.want, core.DefaultMaxBytes
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Resolve(%+v) = %+v, want %+v", c.spec, got, want)
		}
	}
}

// Place cleans the file, drops max_bytes and writes the dialect out, and
// gives the names in the form the dialect compares them in; a spec that names
// no dialect errata reads, or that its dialect refuses, keeps its names. The
// planted breaks are two specs of one place that differ, and one place for
// two that differ. TestINIPlace in extract is the same through Spec.Place.
func TestPlace(t *testing.T) {
	for _, p := range [][2]Spec{
		{{File: "f.ini", Section: "s", Key: "k"}, {File: "./f.ini", Dialect: "plain", Section: "s", Key: "k", MaxBytes: 10}},
		{{File: ".gitconfig", Section: "Remote", Subsection: ptr("origin"), Key: "URL"}, {File: "a/../.gitconfig", Section: "remote.origin", Key: "url"}},
		{{File: "hosts", Section: "web", Key: "k"}, {File: "hosts", Section: "web:hosts", Key: "k"}},
		{{File: "tox.ini", Section: "testenv", Key: "Deps"}, {File: "tox.ini", Section: "testenv", Key: "deps"}},
	} {
		if a, b := Place(p[0]), Place(p[1]); !reflect.DeepEqual(a, b) {
			t.Errorf("%+v and %+v: places differ", a, b)
		}
	}
	for _, p := range [][2]Spec{
		{{File: "f.ini", Section: "s", Key: "k"}, {File: "f.ini", Section: "s", Key: "K"}},
		{{File: "hosts", Section: "s", Key: "k"}, {File: "hosts", Dialect: "plain", Section: "s", Key: "k"}},
	} {
		if reflect.DeepEqual(Place(p[0]), Place(p[1])) {
			t.Errorf("%+v and %+v: one place", p[0], p[1])
		}
	}
	for in, want := range map[Spec]Spec{
		{File: "./f.ini", Dialect: "toml", Key: "k", MaxBytes: 5}: {File: "f.ini", Dialect: "toml", Key: "k"},
		{File: "./f.ini", Section: "s", MaxBytes: 5}:              {File: "f.ini", Dialect: "plain", Section: "s"},
	} {
		if got := Place(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%+v: %+v, want %+v", in, got, want)
		}
	}
}

// An inventory of hosts with ports that are not ASCII reads within the
// work budget: the review's 96 KiB of wéb000123.exämple.org:22 lines, which
// the address patterns, 256 units a byte, refused, and plainHostname reads
// at one. Each host's port is taken off, so each is marked. Planted break:
// portTaken through addressOf for every host that is not ASCII.
func TestNonASCIIHostsWithPortsReadWithinTheBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("[web]\n")
	n := 0
	for ; b.Len() < 96<<10; n++ {
		fmt.Fprintf(&b, "wéb%06d.exämple.org:22\n", n)
	}
	values := mustRun(t, Spec{File: "inventory/hosts", Section: "web"}, extracttest.Repo(t, map[string]string{"inventory/hosts": b.String()}))
	if len(values) != n {
		t.Fatalf("%d values, want %d", len(values), n)
	}
	for _, v := range values {
		if !v.NotLiteral {
			t.Fatalf("%s: literal, want marked: ansible takes its port off", v.Text)
		}
	}
}
