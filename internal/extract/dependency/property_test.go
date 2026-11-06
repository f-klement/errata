package dependency

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// pools are lines each reader's files are drawn from, some naming the
// package x and some not, in every form the reader reads.
var pools = map[string][]string{
	"go.mod": {"module m", "go 1.22", "require x v1.0.0", "require (", "\tx v1.2.0 // c", "\ty v2.0.0", ")",
		"replace x => ../x", "replace x v1.0.0 => x v1.0.1", "// x v9", "require x v1", "\tx v1.2", ""},
	"go.work": {"go 1.22", "use ./a", "replace x => x v1.0.1", "replace (", "\tx v1 => ./y", ")", ""},
	"requirements.txt": {"x==1.0", "X >= 2 ; python_version > '3'", "x[a]~=3.1 \\", "    --hash=sha256:0", "# x==9", "-r other.txt",
		"y==1 # x==8", "x @ https://h/x.zip", "", "  x<4  ", "x>=1\\\\", "# x \\", ".0 ; os_name == 'nt'"},
	"constraints.txt": {"x==1.0", "x\t>1", ""},
	"Gemfile": {`source "https://rubygems.org"`, `gem "x", "~> 1.0"`, `gem 'x', '>= 1', '< 2'`, `gem "x",`, `  "1.2" # c`,
		`gem "y", "1"`, `# gem "x", "9"`, `gem "x", require: false`, `gem("x", V)`, `x = "gem"; gem "x", "3"`, "=begin", "gem \"x\", \"7\"", "=end", "", `gem "x", # c`, "  V", `  "2.0"`},
	"Gemfile.lock": {"GEM", "  remote: https://rubygems.org/", "  specs:", "    x (1.0.0)", "      x (~> 2)", "    x (1.1.0-java)",
		"    y (2.0)", "PLATFORMS", "  ruby", "DEPENDENCIES", "  x (~> 1.0)", "    x (9.9)", ""},
	"yarn.lock": {"# yarn lockfile v1", "", `"x@^1", x@^1.1:`, `  version "1.2.0"`, "y@^2:", `  version "2.0.0"`,
		"x@workspace:.:", `  version "0.0.0"`, `    version "8"`},
}

// lineBreaks are the three line ends.
var lineBreaks = regexp.MustCompile(`\r\n|\r|\n`)

// textual are the readers whose value is text of the line it is at.
// A Gemfile's value is the text of the line its string or its name starts
// on, a bare version with the == the reader writes before it.
// A requirements file's value is text of the line it starts on with the
// lines a backslash joins to it, as pip joins them.
var textual = map[string]bool{"go.mod": true, "go.work": true, "Gemfile.lock": true, "yarn.lock": true, "constraints.txt": true, "requirements.txt": true, "Gemfile": true}

// joins are the readers that join a line ending in a backslash to the next.
var joins = map[string]bool{"constraints.txt": true, "requirements.txt": true}

// lineEnds are the line ends a file is drawn with: each ends a line.
var lineEnds = []string{"\n", "\r\n", "\r"}

// Every value of a file drawn from its reader's lines, with any of the
// three line ends, is at a line inside the file, and, for a reader whose
// value is text of its line, at a line that holds it. Planted break:
// eachLine reading \r\n as two line ends.
func TestPropertyValuesSitInTheirFile(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		file := rapid.SampledFrom(slices.Sorted(maps.Keys(pools))).Draw(t, "file")
		lines := rapid.SliceOfN(rapid.SampledFrom(pools[file]), 0, 12).Draw(t, "lines")
		var b strings.Builder
		for i, l := range lines {
			b.WriteString(l)
			if i < len(lines)-1 || rapid.Bool().Draw(t, "last end") {
				b.WriteString(rapid.SampledFrom(lineEnds).Draw(t, "end"))
			}
		}
		data := b.String()
		got, err := fromData(file, "x", data)
		if err != nil {
			return
		}
		checkLines(t, file, data, got)
	})
}

// fromData is what a spec of name reads from data, as the file named file.
func fromData(file, name, data string) ([]check.Value, error) {
	e, err := build(of(name, "d/"+file))
	if err != nil {
		return nil, err
	}
	x := e.(extractor)
	r, err := x.readerOf(file)
	if err != nil {
		return nil, err
	}
	return x.FromData(r, core.Source{File: "d/" + file, Limit: core.DefaultMaxBytes}, []byte(data))
}

// checkLines fails unless each value is a non-empty text at a line of data,
// and, for a textual reader, one its line holds.
func checkLines(t interface{ Fatalf(string, ...any) }, file, data string, got []check.Value) {
	// The lines as the rule of core's line functions splits them, by a
	// regex of its own, so a fault in eachLine does not hide here.
	split := lineBreaks.Split(data, -1)
	n := len(split)
	for _, v := range got {
		if v.At.File != "d/"+file || v.At.Line < 1 || v.At.Line > n {
			t.Fatalf("%s: %q at %s:%d, outside the file's %d lines\n%q", file, v.Text, v.At.File, v.At.Line, n, data)
		}
		if v.Text == "" {
			t.Fatalf("%s: an empty value at line %d", file, v.At.Line)
		}
		line := split[v.At.Line-1]
		if joins[file] {
			// pip strips every backslash at both ends of a line it
			// continues and joins the next with nothing between.
			var joined strings.Builder
			for i := v.At.Line - 1; i < n; i++ {
				if !strings.HasSuffix(split[i], `\`) {
					joined.WriteString(split[i])
					break
				}
				joined.WriteString(strings.Trim(split[i], `\`))
			}
			line = joined.String()
		}
		if textual[file] && !strings.Contains(line, strings.TrimPrefix(v.Text, "==")) {
			t.Fatalf("%s: %q at line %d, which is %q\n%q", file, v.Text, v.At.Line, line, data)
		}
	}
}

// A package.json's range, among other packages' and other sections', comes
// back at the line it is on, once for each section that names it.
func TestPropertyPackageJSONRangeSitsAtItsLine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var b strings.Builder
		b.WriteString("{\n")
		var want []string
		for i, section := range npmSections {
			b.WriteString("  \"" + section + "\": {")
			n := rapid.IntRange(0, 3).Draw(t, "deps")
			at := rapid.IntRange(-1, n-1).Draw(t, "x at")
			for j := range n {
				name := "y" + strconv.Itoa(j)
				if j == at {
					name = rapid.SampledFrom([]string{"x", "@s/x"}).Draw(t, "name")
				}
				v := rapid.StringMatching(`[~^]?[0-9]\.[0-9]`).Draw(t, "range")
				if j > 0 {
					b.WriteString(",")
				}
				b.WriteString("\n    \"" + name + "\": \"" + v + "\"")
				if name == "x" {
					want = append(want, v+"@"+strconv.Itoa(strings.Count(b.String(), "\n")+1))
				}
			}
			b.WriteString("\n  }")
			if i < len(npmSections)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("}\n")
		got, err := fromData("package.json", "x", b.String())
		if err != nil {
			t.Fatalf("%v\n%s", err, b.String())
		}
		var texts []string
		for _, v := range got {
			texts = append(texts, v.Text+"@"+strconv.Itoa(v.At.Line))
		}
		if strings.Join(texts, " ") != strings.Join(want, " ") {
			t.Fatalf("got %q, want %q\n%s", texts, want, b.String())
		}
	})
}
