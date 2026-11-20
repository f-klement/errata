package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin"
)

// pkg is a package the plugin links, as go list describes it.
type pkg struct {
	ImportPath string
	Standard   bool
	Imports    []string
	SFiles     []string
	CgoFiles   []string
	Module     *struct {
		Path, Version, Dir string
		Main               bool
	}
}

// linked are the packages the plugin links on any platform errata ships
// for, each once, by import path, with the imports, assembly files and
// cgo files it has on any of them: a file of a package may build on one
// platform only, such as x_linux.go or x_arm64.s.
func linked(t *testing.T) []pkg {
	t.Helper()
	seen := map[string]int{}
	var out []pkg
	for _, p := range plugin.Platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		list := exec.CommandContext(t.Context(), "go", "list", "-deps", "-json=ImportPath,Standard,Imports,SFiles,CgoFiles,Module", ".")
		list.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		data, err := list.Output()
		if err != nil {
			t.Fatalf("go list for %s: %v", p, err)
		}
		for d := json.NewDecoder(bytes.NewReader(data)); ; {
			var k pkg
			if err := d.Decode(&k); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if at, ok := seen[k.ImportPath]; ok {
				out[at].merge(k)
			} else {
				seen[k.ImportPath] = len(out)
				out = append(out, k)
			}
		}
	}
	return out
}

// merge adds to p the imports, assembly files and cgo files of o, the
// same package on another platform, that p does not have yet.
func (p *pkg) merge(o pkg) {
	p.Imports = union(p.Imports, o.Imports)
	p.SFiles = union(p.SFiles, o.SFiles)
	p.CgoFiles = union(p.CgoFiles, o.CgoFiles)
}

// union is a with each string of b it does not hold appended.
func union(a, b []string) []string {
	for _, s := range b {
		if !slices.Contains(a, s) {
			a = append(a, s)
		}
	}
	return a
}

// offline are the standard library packages a package the plugin links
// from outside the standard library may import: those it imports today,
// none of which opens a file by name, starts a program or uses the
// network. time's LoadLocation reads the zone database, but takes no
// path and refuses one with "..". A package joins only after a person has
// read what it can open and written its line.
var offline = map[string]bool{
	"bufio": true, "bytes": true, "encoding/binary": true, "encoding/csv": true, "encoding/json": true,
	"errors": true, "fmt": true, "hash/crc32": true, "hash/crc64": true, "io": true, "iter": true,
	"maps": true, "math": true, "math/big": true, "reflect": true, "regexp": true, "regexp/syntax": true,
	"runtime": true, "runtime/debug": true, "slices": true, "sort": true, "strconv": true, "strings": true,
	"sync": true, "time": true, "unicode": true, "unicode/utf8": true,
}

// excused are the imports outside offline a linked package may make, each
// with what it uses: the main's stdin, stdout and exit status, and
// hclsyntax's filepath.Separator, which shortens a file name in a trace
// that only a debug build of hcl prints.
var excused = map[string]string{
	"github.com/f-klement/errata/cmd/errata-ext-hcl": "os",
	"github.com/hashicorp/hcl/v2/hclsyntax":          "path/filepath",
}

// Every package the plugin links from outside the standard library,
// hcl/v2's and its dependencies' included, imports from the standard
// library only the packages of offline, but for the two excused, and has
// no assembly or cgo file, either of which could make a system call no
// import shows. depguard's plugin-is-offline holds errata's own packages
// of the plugin to an allow list; it reads only this repository's files,
// so this test holds the modules the plugin links from elsewhere to
// another, on every platform errata ships for. Planted breaks, each in
// internal/ext/hcl: _ "text/template"; the same in a file that builds
// only on linux, or only on windows; an empty .s file, and an empty
// zz_arm64.s, which builds only on arm64; and hclsyntax's excuse removed.
func TestNoLinkedPackageCanOpenAFileOrTheNetwork(t *testing.T) {
	pkgs := linked(t)
	standard := map[string]bool{}
	for _, p := range pkgs {
		standard[p.ImportPath] = p.Standard
	}
	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		if len(p.SFiles)+len(p.CgoFiles) > 0 {
			t.Errorf("%s has assembly or cgo files, %q and %q, which can make a system call no import shows", p.ImportPath, p.SFiles, p.CgoFiles)
		}
		for _, imp := range p.Imports {
			if standard[imp] && !offline[imp] && excused[p.ImportPath] != imp {
				t.Errorf("%s imports %s, which is not among the standard packages that cannot open a file, start a program or use the network", p.ImportPath, imp)
			}
		}
	}
}

// licences are the licence of each module the plugin links besides
// errata's, as its licence file reads, by SPDX identifier. A module comes
// in only after a person has read its licence and written its row.
var licences = map[string]string{
	"github.com/agext/levenshtein":             "Apache-2.0",
	"github.com/apparentlymart/go-textseg/v17": "MIT",
	"github.com/hashicorp/hcl/v2":              "MPL-2.0",
	"github.com/mitchellh/go-wordwrap":         "MIT",
	"github.com/zclconf/go-cty":                "MIT",
	"golang.org/x/text":                        "BSD-3-Clause",
}

// noticeFile matches the files of a module's root that hold its licence
// or the notices it asks to be kept.
var noticeFile = regexp.MustCompile(`(?i)^(licen[cs]e|notice|copying|patents)(\.(md|txt))?$`)

// notices is the text of errata-ext-hcl_NOTICES.txt for modules: for each,
// its path and version, its licence, where its source is, and its licence
// and notice files as the module ships them.
func notices(t *testing.T, modules []module) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("errata-ext-hcl links the Go modules below, unmodified, besides errata's own code\n" +
		"(MIT, in errata's LICENSE) and Go's standard library. Each section names the module and\n" +
		"the version linked, its licence by SPDX identifier, and where its source is, and then\n" +
		"holds the module's licence and notice files as the module ships them.\n")
	for _, m := range modules {
		b.WriteString("\n" + strings.Repeat("=", 80) + "\n" + m.Path + " " + m.Version + "\nLicence: " + licences[m.Path] +
			"\nSource: https://proxy.golang.org/" + m.Path + "/@v/" + m.Version + ".zip\n")
		entries, err := os.ReadDir(m.Dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Type().IsRegular() && noticeFile.MatchString(e.Name()) {
				text, err := os.ReadFile(filepath.Join(m.Dir, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				b.WriteString("\n" + strings.Repeat("-", 80) + "\n" + e.Name() + "\n\n" + strings.TrimRight(string(text), "\n") + "\n")
			}
		}
	}
	return b.String()
}

// module is a module the plugin links.
type module struct{ Path, Version, Dir string }

// modules are the modules the plugin links besides errata's, by path.
func modules(t *testing.T) []module {
	t.Helper()
	var out []module
	for _, p := range linked(t) {
		if m := p.Module; m != nil && !m.Main && !slices.ContainsFunc(out, func(o module) bool { return o.Path == m.Path }) {
			out = append(out, module{m.Path, m.Version, m.Dir})
		}
	}
	slices.SortFunc(out, func(a, b module) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// NOTICES.txt is errata-ext-hcl_NOTICES.txt, which the plugin's release
// publishes beside its executables (ADR 0021): it names every module the
// plugin links on any platform, at the version linked, with its licence
// and the licence and notice files that module ships, so a bump of a
// module, a module more and a module less each fail here.
// ERRATA_WRITE_NOTICES=1 writes the file anew, to be read in the diff.
// Planted breaks: a module left out of the file; go-cty's licence row
// removed.
func TestNoticesNameEveryLinkedModule(t *testing.T) {
	ms := modules(t)
	for _, m := range ms {
		if licences[m.Path] == "" {
			t.Fatalf("the plugin links %s %s, whose licence has no row in licences; read its licence, add the row, and list it in NOTICES.txt", m.Path, m.Version)
		}
	}
	want := notices(t, ms)
	if os.Getenv("ERRATA_WRITE_NOTICES") == "1" {
		if err := os.WriteFile("NOTICES.txt", []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile("NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("NOTICES.txt is not the notices of the modules the plugin links; run ERRATA_WRITE_NOTICES=1 go test -run TestNoticesNameEveryLinkedModule ./cmd/errata-ext-hcl and read the diff")
	}
}
