package dependency

import (
	"testing"

	"github.com/f-klement/errata/internal/semver"
)

// Each reader hands semver the range its tool reads, so semver_satisfies
// answers as the package manager does: Cargo's bare version takes the ^ it
// implies, Composer's two number tilde runs to the next major, and a bare
// version in RubyGems, Composer and Poetry is that version alone. Each row reads
// the file through its reader. Planted breaks: cargoRange without the ^,
// composerAlternative leaving ~1.2 to npm, exactRange and composerAlternative
// leaving a bare version bare, poetryTable without exactRange,
// composerAlternative leaving @dev alone empty.
func TestDependencyRangesReadAsTheirTools(t *testing.T) {
	for _, c := range []struct {
		what, file, body, version string
		want                      bool
	}{
		{"Cargo, two numbers", "Cargo.toml", "[dependencies]\nx = \"1.38\"\n", "1.40.0", true},
		{"Cargo, three numbers", "Cargo.toml", "[dependencies]\nx = \"1.0.86\"\n", "1.0.89", true},
		{"Cargo, the next major", "Cargo.toml", "[dependencies]\nx = \"1.38\"\n", "2.0.0", false},
		{"Cargo, a table and two comparators", "Cargo.toml", "[dependencies]\nx = { version = \"1.2, <1.5\" }\n", "1.4.0", true},
		{"Cargo, a wildcard", "Cargo.toml", "[dependencies]\nx = \"1.*\"\n", "1.9.0", true},
		{"Composer, a two number tilde", "composer.json", `{"require": {"x": "~1.2"}}`, "1.5.0", true},
		{"Composer, a three number tilde", "composer.json", `{"require": {"x": "~1.2.3"}}`, "1.3.0", false},
		{"Composer, a bare version", "composer.json", `{"require": {"x": "7.1"}}`, "7.1.3", false},
		{"Composer, a bare version itself", "composer.json", `{"require": {"x": "7.1"}}`, "7.1.0", true},
		{"Composer, a stability flag", "composer.json", `{"require": {"x": "^3.0@dev"}}`, "3.1.0", true},
		{"Composer, an operator apart", "composer.json", `{"require": {"x": ">= 1.0, < 2.0"}}`, "1.9.0", true},
		{"Composer, a stability flag alone", "composer.json", `{"require": {"x": "@dev"}}`, "9.0.0", true},
		{"Poetry, a bare version", "pyproject.toml", "[tool.poetry.dependencies]\nx = \"1.2\"\n", "1.2.5", false},
		{"Poetry, a bare version itself", "pyproject.toml", "[tool.poetry.dependencies]\nx = { version = \"1.2\" }\n", "1.2.0", true},
		{"Poetry, a caret", "pyproject.toml", "[tool.poetry.dependencies]\nx = \"^1.2\"\n", "1.9.0", true},
		{"RubyGems, a bare version", "Gemfile", "gem \"x\", \"7.1\"\n", "7.1.3", false},
		{"RubyGems, = and a version", "Gemfile", "gem \"x\", \"= 7.1\"\n", "7.1.3", false},
		{"RubyGems, a bare version itself", "Gemfile", "gem \"x\", \"7.1\"\n", "7.1.0", true},
		{"RubyGems, pessimistic", "Gemfile", "gem \"x\", \"~> 7.1\"\n", "7.9.0", true},
	} {
		got, err := fromData(c.file, "x", c.body)
		if err != nil || len(got) != 1 {
			t.Errorf("%s: %v, %d values", c.what, err, len(got))
			continue
		}
		r, err := semver.ParseRange(got[0].Text)
		if err != nil {
			t.Errorf("%s: %q: %v", c.what, got[0].Text, err)
			continue
		}
		v, err := semver.ParseVersion(c.version)
		if err != nil {
			t.Fatal(err)
		}
		if semver.Satisfies(v, r) != c.want {
			t.Errorf("%s: %s satisfies %q (from %q): %v, want %v", c.what, c.version, got[0].Text, c.body, !c.want, c.want)
		}
	}
}

// The byte classes and npm's test for a range it reads from elsewhere hold
// at each edge of the classes they test.
func TestDependencyByteClassesAtTheirEdges(t *testing.T) {
	for _, c := range []struct {
		b            byte
		alnum, digit bool
	}{
		{'0', true, true}, {'9', true, true}, {'a', true, false}, {'z', true, false}, {'A', true, false}, {'Z', true, false},
		{'/', false, false}, {':', false, false}, {'@', false, false}, {'[', false, false}, {'`', false, false}, {'{', false, false},
	} {
		if isAlnum(c.b) != c.alnum || isDigit(c.b) != c.digit {
			t.Errorf("%q: alnum %v digit %v, want %v %v", c.b, isAlnum(c.b), isDigit(c.b), c.alnum, c.digit)
		}
	}
	for v, want := range map[string]bool{
		"a": true, "zed": true, "{1": false, "@1": false, "x": false, "x.1": false, "x 1": false, "xa": true,
		"v": true, "v0": false, "v9": false, "va": true, "v.": true, "1.0": false, "^1": false,
	} {
		if got := npmNotLiteral(v); got != want {
			t.Errorf("npmNotLiteral(%q) = %v, want %v", v, got, want)
		}
	}
	for line, want := range map[string]int{"#c": 0, "a\t#c": 2, "a #c": 2, "a#b": -1, "": -1} {
		if got := comment([]byte(line)); got != want {
			t.Errorf("comment(%q) = %d, want %d", line, got, want)
		}
	}
}
