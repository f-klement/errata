package gomod

import (
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/f-klement/errata/internal/extract/core"
)

// A go.mod is refused before modfile parses it when it can hold more lines
// and tokens than its limit over 64, and read at the limit: "module m",
// "go 1.22" and their two line ends are seven with the start. The planted
// break: parseMod without the check.
func TestParseNodeLimit(t *testing.T) {
	data := []byte("module m\ngo 1.22\n")
	if mod, err := Parse(core.Source{File: "go.mod", Limit: 7 * 64}, data); err != nil || mod.Go.Version != "1.22" {
		t.Errorf("at the limit: %v", err)
	}
	_, err := Parse(core.Source{File: "go.mod", Limit: 7*64 - 1}, data)
	if err == nil || err.Error() != "go.mod: the file can hold up to 7 lines and tokens, over the limit of 6, one for every 64 bytes of max_bytes; set max_bytes: higher to read a file this dense" {
		t.Errorf("over the limit: %v", err)
	}
	_, err = ParseWork(core.Source{File: "go.work", Limit: 7*64 - 1}, []byte("go 1.22\nuse ./a\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "go.work: the file can hold up to 7 lines and tokens") {
		t.Errorf("go.work over the limit: %v", err)
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{"", 1}, {"a", 2}, {" a  b\t\r\n", 4}, {"a(b)\n(", 7}, {" x", 2},
		// Each byte modfile makes a token of, with no blank around it, and
		// a token right after one.
		{"x ,,,", 5}, {"[]{}", 5}, {`"a""b"`, 7}, {"`a`b", 5}, {"retract [v1.0.0, v1.1.0]", 7},
	} {
		if got := Nodes([]byte(c.body)); got != c.want {
			t.Errorf("%q: %d nodes, want %d", c.body, got, c.want)
		}
	}
}

// A file modfile refuses is refused with modfile's message, which names the
// file and the line.
func TestParseRefusesWhatModfileRefuses(t *testing.T) {
	_, err := Parse(core.Source{File: "d/go.mod", Limit: 1 << 20}, []byte("module m\nfrob x\n"))
	if err == nil || err.Error() != "d/go.mod:2: unknown directive: frob" {
		t.Errorf("go.mod: %v", err)
	}
	_, err = ParseWork(core.Source{File: "go.work", Limit: 1 << 20}, []byte("go 1.22\nrequire x v1\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "go.work:2: ") {
		t.Errorf("go.work: %v", err)
	}
}

// A panic in modfile is a refusal with the file named, not the end of the
// run. No input found makes it panic, so the parser is replaced.
func TestParsePanicIsARefusal(t *testing.T) {
	defer func(p func(string, []byte, modfile.VersionFixer) (*modfile.File, error)) { parseGoMod = p }(parseGoMod)
	parseGoMod = func(string, []byte, modfile.VersionFixer) (*modfile.File, error) { panic("index out of range") }
	_, err := Parse(core.Source{File: "go.mod", Limit: 1 << 20}, []byte("module m\n"))
	if err == nil || err.Error() != "go.mod: golang.org/x/mod/modfile stopped on the file: index out of range" {
		t.Errorf("got %v", err)
	}
}

// Nodes is at least the lines and tokens modfile builds from any bytes
// it parses, since the count runs before modfile parses.
func FuzzNodes(f *testing.F) {
	for _, body := range []string{
		"module m\n\ngo 1.22.0\ntoolchain go1.22.3\n",
		"module m\nretract [v1.0.0, v1.1.0]\n",
		"module m\ngo 1.22\nx ,,,,\n",
		"module m\nx []{}\"a\"\"b\"`c`d\n",
		"module m\nrequire (\n\tex.com/a v1.0.0 // c\n)\ntoolchain go1.22.3\n",
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, data string) {
		mod, err := modfile.ParseLax("go.mod", []byte(data), nil)
		if err != nil {
			return
		}
		built := 0
		for _, stmt := range mod.Syntax.Stmt {
			switch s := stmt.(type) {
			case *modfile.Line:
				built += 1 + len(s.Token)
			case *modfile.LineBlock:
				built += 2 + len(s.Token)
				for _, l := range s.Line {
					built += 1 + len(l.Token)
				}
			case *modfile.CommentBlock:
				built++
			}
		}
		if n := Nodes([]byte(data)); n < built {
			t.Fatalf("Nodes counts %d, modfile built %d\n%q", n, built, data)
		}
	})
}

// A directive's line is errata's, where modfile's is not: a lone CR before
// it ends a line, and modfile reads it as a blank. The planted break: Line
// returning p.Line.
func TestLineCountsALoneCR(t *testing.T) {
	data := []byte("module m\n\rgo 1.22\r\ntoolchain go1.22.3\n")
	mod, err := Parse(core.Source{File: "go.mod", Limit: 1 << 20}, data)
	if err != nil {
		t.Fatal(err)
	}
	if got := Line(data, mod.Go.Syntax.Start); got != 3 || mod.Go.Syntax.Start.Line != 2 {
		t.Errorf("go at line %d, modfile's %d; want 3 and 2", got, mod.Go.Syntax.Start.Line)
	}
	if got := Line(data, mod.Toolchain.Syntax.Start); got != 4 {
		t.Errorf("toolchain at line %d, want 4", got)
	}
}
