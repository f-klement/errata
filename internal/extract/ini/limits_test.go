package ini

import (
	"reflect"
	"testing"
)

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
	got := iniValues(t, "hosts", "[s]\nk = 1\n", Spec{File: "hosts", Dialect: "plain", Section: "s", Key: "k"})
	if got != "1@2" {
		t.Errorf("dialect: plain on hosts: got %q", got)
	}
}

// Describe names the dialect and the file's base name, and nothing for a
// dialect errata does not read.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
		ok   bool
	}{
		{Spec{File: "a/pytest.ini"}, "plain dialect on pytest.ini", true},
		{Spec{File: "conf/x", Dialect: "git"}, "git dialect on x", true},
		{Spec{File: "hosts"}, "ansible dialect on hosts", true},
		{Spec{File: "x.ini", Dialect: "toml"}, "", false},
	} {
		if got, ok := Describe(c.spec); got != c.want || ok != c.ok {
			t.Errorf("%+v: %q, %v; want %q, %v", c.spec, got, ok, c.want, c.ok)
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
		want.Dialect = c.want
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
