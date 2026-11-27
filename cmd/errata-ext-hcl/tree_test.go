package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
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
		Path, Version string
		Main          bool
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
