package dockerfile

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

	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/plugin"
)

// linkedPkg is a package the reader links, as go list describes it.
type linkedPkg struct {
	ImportPath       string
	Standard         bool
	Imports          []string
	SFiles, CgoFiles []string
}

// readerLinks are the packages this package links on any platform errata
// ships for, by import path, each with the imports, assembly files and cgo
// files it has on any of them, as cmd/errata-ext-hcl's linked reads the
// plugin's.
func readerLinks(t *testing.T) map[string]*linkedPkg {
	t.Helper()
	out := map[string]*linkedPkg{}
	for _, p := range plugin.Platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		list := exec.CommandContext(t.Context(), "go", "list", "-deps", "-json=ImportPath,Standard,Imports,SFiles,CgoFiles", ".")
		list.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		data, err := list.Output()
		if err != nil {
			t.Fatalf("go list for %s: %v", p, err)
		}
		for d := json.NewDecoder(bytes.NewReader(data)); ; {
			var k linkedPkg
			if err := d.Decode(&k); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if o, ok := out[k.ImportPath]; ok {
				o.Imports, o.SFiles, o.CgoFiles = union(o.Imports, k.Imports), union(o.SFiles, k.SFiles), union(o.CgoFiles, k.CgoFiles)
			} else {
				out[k.ImportPath] = &k
			}
		}
	}
	return out
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

// fromBuildKit are the packages outside the standard library and errata
// that the reader's imports from outside errata link, those imports
// included: BuildKit's parser, shell and command and what they link.
func fromBuildKit(pkgs map[string]*linkedPkg) []*linkedPkg {
	const errata = "github.com/f-klement/errata/"
	var out []*linkedPkg
	seen := map[string]bool{}
	var walk func(path string)
	walk = func(path string) {
		p, ok := pkgs[path]
		if !ok || p.Standard || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, p)
		for _, imp := range p.Imports {
			walk(imp)
		}
	}
	for _, imp := range pkgs[errata+"internal/extract/dockerfile"].Imports {
		if !strings.HasPrefix(imp, errata) {
			walk(imp)
		}
	}
	return out
}

// buildKitOffline are the standard library packages a package BuildKit's
// parser links may import: those it imports today, none of which opens a
// file by name, starts a program or uses the network. A package joins only
// after a person has read what it can open and written its line.
var buildKitOffline = map[string]bool{
	"bufio": true, "bytes": true, "compress/gzip": true, "embed": true, "encoding/binary": true, "encoding/json": true,
	"errors": true, "fmt": true, "go/token": true, "hash/crc32": true, "hash/fnv": true, "io": true, "math": true,
	"math/bits": true, "path": true, "reflect": true, "regexp": true, "runtime": true, "slices": true, "sort": true,
	"strconv": true, "strings": true, "sync": true, "sync/atomic": true, "text/scanner": true, "unicode": true,
	"unicode/utf16": true, "unicode/utf8": true,
}

// buildKitExcused are the imports outside buildKitOffline a linked package
// may make, each read for what it uses. os: util/stack copies os.Args and
// os.Getpid into the stack it attaches to an error; protobuf's detrand
// opens the running executable, by os.Executable, to hash it;
// protoregistry reads GOLANG_PROTOBUF_REGISTRATION_CONFLICT and warns on
// stderr of a conflict; impl reads GOPROTODEBUG. unsafe: the pointer
// conversions of protobuf's runtime and generated code.
var buildKitExcused = map[string][]string{
	"github.com/moby/buildkit/util/stack":              {"os", "unsafe"},
	"google.golang.org/protobuf/internal/detrand":      {"os"},
	"google.golang.org/protobuf/reflect/protoregistry": {"os"},
	"google.golang.org/protobuf/internal/impl":         {"os", "unsafe"},
	"google.golang.org/protobuf/reflect/protoreflect":  {"unsafe"},
	"google.golang.org/protobuf/internal/strs":         {"unsafe"},
	"google.golang.org/protobuf/internal/protolazy":    {"unsafe"},
	"google.golang.org/protobuf/types/known/anypb":     {"unsafe"},
}

// Every package outside the standard library and errata that the reader
// links through BuildKit imports from the standard library only the
// packages of buildKitOffline, but for those buildKitExcused names, and
// has no assembly or cgo file, either of which could make a system call no
// import shows, on every platform errata ships for. A bump of
// github.com/moby/buildkit that brings in a file, a program or the network
// fails here until a person has read the new import. Planted breaks:
// util/stack's os excuse removed, detrand's, which only the walk past
// typeurl reaches, and the assembly check taking every package.
func TestBuildKitLinksNothingThatOpensAFileOrTheNetwork(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("go list reads the module graph, which no mutant of the reader changes")
	}
	pkgs := readerLinks(t)
	linked := fromBuildKit(pkgs)
	if len(linked) == 0 {
		t.Fatal("the reader links no package from outside errata; go list found none")
	}
	for _, p := range linked {
		if len(p.SFiles)+len(p.CgoFiles) > 0 {
			t.Errorf("%s has assembly or cgo files, %q and %q, which can make a system call no import shows", p.ImportPath, p.SFiles, p.CgoFiles)
		}
		for _, imp := range p.Imports {
			if d, ok := pkgs[imp]; ok && d.Standard && !buildKitOffline[imp] && !slices.Contains(buildKitExcused[p.ImportPath], imp) {
				t.Errorf("%s imports %s, which is not among the standard packages that cannot open a file, start a program or use the network", p.ImportPath, imp)
			}
		}
	}
}
