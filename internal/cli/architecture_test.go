package cli_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
)

// docs/architecture.md draws errata's packages and kinds, and a picture
// nobody checks drifts from the code. The tests below hold three parts of it
// to the code: the package graphs, the package table and the registry
// diagram. Each was seen failing on a break planted with ci/plant.sh: the
// arrow rule --> semver dropped, report --> check turned into report -->
// rule, the heapcap node dropped, readers --> core dropped, a reader's row
// dropped from the table, the kind "size" dropped from the registry and
// "json" moved before "yaml", a non-test file of rule importing
// extracttest, and the test-only box of the first graph given the id tests.

const errataModule = "github.com/f-klement/errata/"

// testOnlyID and testOnlyTitle are the id and the title of a subgraph whose
// packages only test files import. The test reads the id, and the doc's
// prose names the title, so either one without the other is refused.
const (
	testOnlyID    = "testonly"
	testOnlyTitle = "imported only by test files"
)

// archDoc is docs/architecture.md.
func archDoc(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../../docs/architecture.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(doc)
}

// Every mermaid block that holds a line "%% packages" is a package graph:
// its nodes are packages, each labelled with its path from the module's
// root, and an arrow is an import, from the importer. A subgraph stands for
// each package drawn in it, so an arrow from or to it is an arrow from or to
// every one of them. The graphs together must draw every package under cmd/
// and internal/ and every import between them that a non-test file makes,
// and nothing else. A package drawn in a subgraph whose id is testonly is
// one no non-test file imports.
func TestArchitectureDrawsEveryImport(t *testing.T) {
	drawn, err := archDrawn(archDoc(t))
	if err != nil {
		t.Fatal(err)
	}
	code, err := archCode("../..", "cmd", "internal")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range archDiff(drawn, code) {
		t.Error(d)
	}
}

// The table under Package dependencies has one row per package, or per few
// packages, each named in full in backquotes in its first cell. Together
// the rows name every package under cmd/ and internal/, each once.
func TestArchitectureTablesEveryPackage(t *testing.T) {
	rows, err := archTable(archDoc(t))
	if err != nil {
		t.Fatal(err)
	}
	code, err := archCode("../..", "cmd", "internal")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range archTableDiff(rows, code) {
		t.Error(d)
	}
}

// The registry diagram, the mermaid block that holds a line "%% kinds",
// draws one node per kind in its subgraph kinds, labelled with the kind's
// key, in the order of kinds in internal/extract/extract.go.
func TestArchitectureListsEveryKind(t *testing.T) {
	drawn, err := archKinds(archDoc(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range archKindsDiff(drawn, extract.Keys()) {
		t.Error(d)
	}
}

// archKindsDiff says what the registry diagram, which draws the kinds
// drawn, must change to draw want: each kind to add, with its node and the
// kind it goes after, each kind to remove, and the first place where the
// kinds both have stand in another order.
func archKindsDiff(drawn, want []string) []string {
	var out []string
	for i, k := range want {
		if slices.Contains(drawn, k) {
			continue
		}
		where := "first in its subgraph kinds"
		if i > 0 {
			where = fmt.Sprintf("in its subgraph kinds after %q", want[i-1])
		}
		out = append(out, fmt.Sprintf("the registry diagram of docs/architecture.md lacks the kind %q: draw %s %s", k, archKindNode(k), where))
	}
	for _, k := range drawn {
		if !slices.Contains(want, k) {
			out = append(out, fmt.Sprintf("the registry diagram of docs/architecture.md draws the kind %q, which kinds in internal/extract/extract.go does not list", k))
		}
	}
	both := func(of, in []string) []string {
		return slices.DeleteFunc(slices.Clone(of), func(k string) bool { return !slices.Contains(in, k) })
	}
	d, w := both(drawn, want), both(want, drawn)
	for i := range d {
		if d[i] != w[i] {
			out = append(out, fmt.Sprintf("the registry diagram of docs/architecture.md draws %q where kinds in internal/extract/extract.go has %q at position %d of the kinds both list: draw them in the order of kinds", d[i], w[i], i+1))
			break
		}
	}
	return out
}

// archKindNode is the node the registry diagram draws for the kind key.
func archKindNode(key string) string {
	return "k_" + strings.NewReplacer("-", "_", ".", "_").Replace(key) + `["` + key + `"]`
}

// archGraph is packages and the imports between them, each package by its
// path from the module's root, and the packages drawn as imported only by
// test files.
type archGraph struct {
	nodes    map[string]bool
	edges    map[[2]string]bool
	testOnly map[string]bool
}

func newArchGraph() archGraph {
	return archGraph{nodes: map[string]bool{}, edges: map[[2]string]bool{}, testOnly: map[string]bool{}}
}

var (
	packagesMarker = regexp.MustCompile(`^%%\s*packages\b`)
	kindsMarker    = regexp.MustCompile(`^%%\s*kinds\b`)
	graphHeader    = regexp.MustCompile(`^flowchart (TB|TD|LR|BT|RL)$`)
	nodeLine       = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)\["([^"]+)"\]$`)
	subgraphLine   = regexp.MustCompile(`^subgraph ([A-Za-z][A-Za-z0-9_]*) \["([^"]*)"\]$`)
	edgeLine       = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*) --> ([A-Za-z][A-Za-z0-9_]*)$`)
	backquoted     = regexp.MustCompile("`([^`]+)`")
)

// archDrawn reads the package graphs of doc. A line of a package graph that
// is none of the forms above, or a graph that does not open on a flowchart
// header, is an error, and so is a doc with no package graph, so a graph
// the test cannot read is never a graph it passes.
func archDrawn(doc string) (archGraph, error) {
	g := newArchGraph()
	labels := map[string]string{} // a node's id to its path, across every graph
	found := 0
	for _, b := range archBlocks(doc) {
		if !b.marked(packagesMarker) {
			continue
		}
		found++
		if len(b.lines) == 0 || !graphHeader.MatchString(strings.TrimSpace(b.lines[0])) {
			return g, fmt.Errorf("docs/architecture.md:%d: a package graph opens on a flowchart header, such as flowchart TB", b.line)
		}
		ids := map[string]string{}      // this graph's node ids to their paths
		groups := map[string][]string{} // this graph's subgraph ids to their members' paths
		var arrows [][2]string          // this graph's arrows, by id
		group := ""
		for i, raw := range b.lines[1:] {
			l := strings.TrimSpace(raw)
			at := fmt.Sprintf("docs/architecture.md:%d", b.line+1+i)
			node, sub, edge := nodeLine.FindStringSubmatch(l), subgraphLine.FindStringSubmatch(l), edgeLine.FindStringSubmatch(l)
			switch {
			case l == "", strings.HasPrefix(l, "%%"):
			case node != nil:
				id, path := node[1], node[2]
				if was, ok := labels[id]; ok && was != path {
					return g, fmt.Errorf("%s: the node %s is %q here and %q in another graph", at, id, path, was)
				}
				labels[id], ids[id] = path, path
				g.nodes[path] = true
				if group != "" {
					groups[group] = append(groups[group], path)
				}
				if group == testOnlyID {
					g.testOnly[path] = true
				}
			case sub != nil:
				if group != "" {
					return g, fmt.Errorf("%s: a subgraph inside the subgraph %s, which a package graph does not nest", at, group)
				}
				if (sub[1] == testOnlyID) != (sub[2] == testOnlyTitle) {
					return g, fmt.Errorf("%s: the subgraph %s is titled %q; a box of packages only test files import has the id %s and the title %q, both", at, sub[1], sub[2], testOnlyID, testOnlyTitle)
				}
				group = sub[1]
				groups[group] = nil
			case l == "end" && group != "":
				group = ""
			case edge != nil:
				arrows = append(arrows, [2]string{edge[1], edge[2]})
			default:
				return g, fmt.Errorf("%s: %q is no line a package graph holds: a node id[\"path\"], a subgraph id [\"title\"] and its end, an arrow a --> b, or a %%%% comment", at, l)
			}
		}
		if group != "" {
			return g, fmt.Errorf("docs/architecture.md:%d: the subgraph %s has no end", b.line, group)
		}
		for _, a := range arrows {
			from, err := archEnds(a[0], ids, groups)
			if err != nil {
				return g, err
			}
			to, err := archEnds(a[1], ids, groups)
			if err != nil {
				return g, err
			}
			for _, f := range from {
				for _, t := range to {
					g.edges[[2]string{f, t}] = true
				}
			}
		}
	}
	if found == 0 {
		return g, fmt.Errorf("docs/architecture.md has no mermaid block with a %q line, so no package graph to check", "%% packages")
	}
	return g, nil
}

// archEnds is the packages an arrow's end id stands for: the node's package,
// or each package of the subgraph.
func archEnds(id string, ids map[string]string, groups map[string][]string) ([]string, error) {
	if path, ok := ids[id]; ok {
		return []string{path}, nil
	}
	if members, ok := groups[id]; ok && len(members) > 0 {
		return members, nil
	}
	return nil, fmt.Errorf("docs/architecture.md: an arrow ends at %s, which is no node or subgraph of its graph with a package in it", id)
}

// archKinds is the keys the registry diagram draws in its subgraph kinds, in
// order.
func archKinds(doc string) ([]string, error) {
	for _, b := range archBlocks(doc) {
		if !b.marked(kindsMarker) {
			continue
		}
		var keys []string
		in, closed := false, false
		for _, raw := range b.lines {
			l := strings.TrimSpace(raw)
			switch sub := subgraphLine.FindStringSubmatch(l); {
			case sub != nil && sub[1] == "kinds":
				in = true
			case in && l == "end":
				in, closed = false, true
			case in && (l == "" || strings.HasPrefix(l, "%%")):
			case in:
				node := nodeLine.FindStringSubmatch(l)
				if node == nil {
					return nil, fmt.Errorf("docs/architecture.md: %q in the subgraph kinds is no node id[\"key\"]", l)
				}
				keys = append(keys, node[2])
			}
		}
		if !closed {
			return nil, fmt.Errorf("docs/architecture.md:%d: the registry diagram has no subgraph kinds with its end", b.line)
		}
		return keys, nil
	}
	return nil, fmt.Errorf("docs/architecture.md has no mermaid block with a %q line, so no registry diagram to check", "%% kinds")
}

// archTable is the packages each row of the package table names in its
// first cell, by row.
func archTable(doc string) ([][]string, error) {
	var rows [][]string
	in := false
	for _, l := range strings.Split(doc, "\n") {
		switch {
		case strings.HasPrefix(l, "| Package | Owns |"):
			in = true
		case in && strings.HasPrefix(l, "|---"):
		case in && strings.HasPrefix(l, "|"):
			cells := strings.Split(l, "|")
			var names []string
			for _, m := range backquoted.FindAllStringSubmatch(cells[1], -1) {
				names = append(names, m[1])
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("docs/architecture.md: the package table's row %q names no package in backquotes", strings.TrimSpace(cells[1]))
			}
			rows = append(rows, names)
		case in:
			return rows, nil
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("docs/architecture.md has no table under a header | Package | Owns |")
	}
	return rows, nil
}

func archTableDiff(rows [][]string, code archGraph) []string {
	var out []string
	named := map[string]bool{}
	for _, row := range rows {
		for _, p := range row {
			switch {
			case named[p]:
				out = append(out, fmt.Sprintf("the package table of docs/architecture.md names %s twice", p))
			case !code.nodes[p]:
				out = append(out, fmt.Sprintf("the package table of docs/architecture.md names %s, which the code does not have", p))
			}
			named[p] = true
		}
	}
	for p := range code.nodes {
		if !named[p] {
			out = append(out, fmt.Sprintf("the package %s has no row in the package table of docs/architecture.md", p))
		}
	}
	slices.Sort(out)
	return out
}

// archBlock is one mermaid block of a Markdown file: its lines, and the line
// number of the first.
type archBlock struct {
	line  int
	lines []string
}

func (b archBlock) marked(marker *regexp.Regexp) bool {
	return slices.ContainsFunc(b.lines, func(l string) bool { return marker.MatchString(strings.TrimSpace(l)) })
}

// archBlocks is the mermaid blocks of doc: each fence of three backquotes
// whose info string is mermaid. A fence of any other info string or length
// is passed over whole, so an example of a mermaid block inside a fence of
// four backquotes is no block.
func archBlocks(doc string) []archBlock {
	var (
		out   []archBlock
		cur   *archBlock
		fence string // the fence being passed over, or ""
	)
	for i, l := range strings.Split(doc, "\n") {
		tl := strings.TrimSpace(l)
		ticks := len(tl) - len(strings.TrimLeft(tl, "`"))
		switch {
		case cur != nil && tl == "```":
			out = append(out, *cur)
			cur = nil
		case cur != nil:
			cur.lines = append(cur.lines, l)
		case fence != "":
			if ticks >= len(fence) && strings.Trim(tl, "`") == "" {
				fence = ""
			}
		case ticks == 3 && tl == "```mermaid":
			cur = &archBlock{line: i + 2}
		case ticks >= 3:
			fence = tl[:ticks]
		}
	}
	return out
}

// archCode is the packages below each of dirs under root, and the imports
// between them that their non-test files make, on any OS: a file's build
// constraints are not read, so an import made only on Windows counts too.
// It passes over what go build does: testdata, a directory whose name starts
// with _ or ., and a file whose build constraint is ignore.
func archCode(root string, dirs ...string) (archGraph, error) {
	g := newArchGraph()
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly|parser.ParseComments)
			if err != nil {
				return err
			}
			for _, c := range f.Comments {
				if c.Pos() < f.Package && slices.ContainsFunc(c.List, func(l *ast.Comment) bool { return l.Text == "//go:build ignore" }) {
					return nil
				}
			}
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			pkg := filepath.ToSlash(rel)
			g.nodes[pkg] = true
			for _, im := range f.Imports {
				path, err := strconv.Unquote(im.Path.Value)
				if err != nil {
					return err
				}
				if dep, ok := strings.CutPrefix(path, errataModule); ok {
					g.edges[[2]string{pkg, dep}] = true
				}
			}
			return nil
		})
		if err != nil {
			return g, err
		}
	}
	return g, nil
}

// archDiff names each package and each import that one of drawn and code has
// and the other lacks, and each import of a package drawn as imported only
// by test files, sorted.
func archDiff(drawn, code archGraph) []string {
	var out []string
	for p := range code.nodes {
		if !drawn.nodes[p] {
			out = append(out, fmt.Sprintf("the package %s is in the code and in no package graph of docs/architecture.md", p))
		}
	}
	for p := range drawn.nodes {
		if !code.nodes[p] {
			out = append(out, fmt.Sprintf("docs/architecture.md draws the package %s, which the code does not have", p))
		}
	}
	for e := range code.edges {
		if !drawn.edges[e] {
			out = append(out, fmt.Sprintf("%s imports %s, and no package graph of docs/architecture.md draws the arrow", e[0], e[1]))
		}
		if drawn.testOnly[e[1]] {
			out = append(out, fmt.Sprintf("%s imports %s, which docs/architecture.md draws as imported only by test files", e[0], e[1]))
		}
	}
	for e := range drawn.edges {
		if !code.edges[e] {
			out = append(out, fmt.Sprintf("docs/architecture.md draws %s --> %s, and %s does not import %s", e[0], e[1], e[0], e[1]))
		}
	}
	slices.Sort(out)
	return out
}

// TestArchDrawnReadsAPackageGraph holds the reader of the doc's graphs to the
// forms it documents, so a graph the reader misreads cannot pass.
func TestArchDrawnReadsAPackageGraph(t *testing.T) {
	doc := "text\n````\n```mermaid\nnot a graph\n```\n````\n" +
		"```mermaid\nflowchart TB\n    %% packages\n    a[\"internal/a\"]\n" +
		"    subgraph g [\"two\"]\n        b[\"internal/b\"]\n        c[\"internal/c\"]\n    end\n" +
		"    subgraph testonly [\"imported only by test files\"]\n        d[\"internal/d\"]\n    end\n" +
		"    a --> g\n    g --> a\n```\n```mermaid\nflowchart LR\n    x[\"not a package graph\"] --> y\n```\n"
	g, err := archDrawn(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := newArchGraph()
	for _, p := range []string{"internal/a", "internal/b", "internal/c", "internal/d"} {
		want.nodes[p] = true
	}
	for _, e := range [][2]string{{"internal/a", "internal/b"}, {"internal/a", "internal/c"}, {"internal/b", "internal/a"}, {"internal/c", "internal/a"}} {
		want.edges[e] = true
	}
	if d := archDiff(g, want); len(d) > 0 {
		t.Errorf("read the graph wrong:\n%s", strings.Join(d, "\n"))
	}
	if !g.testOnly["internal/d"] || len(g.testOnly) != 1 {
		t.Errorf("testOnly = %v, want internal/d alone", g.testOnly)
	}
	want.edges[[2]string{"internal/a", "internal/d"}] = true
	if d := archDiff(g, want); !slices.ContainsFunc(d, func(s string) bool { return strings.Contains(s, "imported only by test files") }) {
		t.Errorf("an import of a test-only package passed: %v", d)
	}
}

func TestArchDrawnRefuses(t *testing.T) {
	head := "```mermaid\nflowchart TB\n%% packages\n"
	for doc, want := range map[string]string{
		"no graph": "no mermaid block",
		"```mermaid\n%% packages\na[\"internal/a\"]\n```":                   "opens on a flowchart header",
		head + "a[\"internal/a\"]\na -.-> a\n```":                           "is no line a package graph holds",
		head + "a[\"internal/a\"]\na --> b\n```":                            "an arrow ends at b",
		head + "subgraph g [\"t\"]\nend\na[\"internal/a\"]\na --> g\n```":   "an arrow ends at g",
		head + "subgraph g [\"t\"]\nsubgraph h [\"u\"]\nend\nend\n```":      "a subgraph inside the subgraph g",
		head + "subgraph g [\"t\"]\n```":                                    "the subgraph g has no end",
		head + "a[\"internal/a\"]\n```\n" + head + "a[\"internal/b\"]\n```": "the node a is",
	} {
		if _, err := archDrawn(doc); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("archDrawn(%q) = %v, want an error holding %q", doc, err, want)
		}
	}
}

func TestArchKindsAndTable(t *testing.T) {
	doc := "```mermaid\nflowchart LR\n%% kinds\nsubgraph kinds [\"k\"]\nk_a[\"a\"]\nk_b[\"b\"]\nend\nx --> kinds\n```\n" +
		"| Package | Owns |\n|---|---|\n| `internal/a`, `internal/b` | x |\n| `internal/c` | y |\n\nafter\n"
	keys, err := archKinds(doc)
	if err != nil || !slices.Equal(keys, []string{"a", "b"}) {
		t.Errorf("archKinds = %v, %v, want [a b]", keys, err)
	}
	rows, err := archTable(doc)
	if err != nil || len(rows) != 2 || !slices.Equal(rows[0], []string{"internal/a", "internal/b"}) {
		t.Errorf("archTable = %v, %v", rows, err)
	}
	code := newArchGraph()
	code.nodes["internal/a"], code.nodes["internal/c"], code.nodes["internal/d"] = true, true, true
	got := strings.Join(archTableDiff(append(rows, []string{"internal/a"}), code), "\n")
	for _, want := range []string{"names internal/a twice", "names internal/b, which the code", "internal/d has no row"} {
		if !strings.Contains(got, want) {
			t.Errorf("archTableDiff lacks %q:\n%s", want, got)
		}
	}
	for doc, want := range map[string]string{
		"none": "no mermaid block",
		"```mermaid\nflowchart LR\n%% kinds\nsubgraph kinds [\"k\"]\nk_a[\"a\"]\n```":   "no subgraph kinds with its end",
		"```mermaid\nflowchart LR\n%% kinds\nsubgraph kinds [\"k\"]\na --> b\nend\n```": "is no node",
	} {
		if _, err := archKinds(doc); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("archKinds(%q) = %v, want %q", doc, err, want)
		}
	}
	if _, err := archTable("| Package | Owns |\n|---|---|\n| none | x |\n"); err == nil || !strings.Contains(err.Error(), "names no package") {
		t.Errorf("archTable of a row without a package = %v", err)
	}
	if _, err := archTable("no table"); err == nil || !strings.Contains(err.Error(), "has no table under a header") {
		t.Errorf("archTable of a doc with no table = %v", err)
	}
}

func TestArchKindsDiff(t *testing.T) {
	if d := archKindsDiff([]string{"a", "b"}, []string{"a", "b"}); len(d) > 0 {
		t.Errorf("equal kinds gave %v", d)
	}
	got := strings.Join(archKindsDiff([]string{"b", "a", "x"}, []string{"a", "b", "gitlab-ci"}), "\n")
	for _, want := range []string{
		`lacks the kind "gitlab-ci": draw k_gitlab_ci["gitlab-ci"] in its subgraph kinds after "b"`,
		`draws the kind "x", which kinds`,
		`draws "b" where kinds in internal/extract/extract.go has "a" at position 1 of the kinds both list`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("archKindsDiff lacks %q:\n%s", want, got)
		}
	}
	if got := strings.Join(archKindsDiff(nil, []string{"a"}), ""); !strings.Contains(got, `k_a["a"] first in its subgraph kinds`) {
		t.Errorf("a first kind missing gave %q", got)
	}
}

func TestArchDrawnRefusesATestOnlyBoxNamedHalfway(t *testing.T) {
	head := "```mermaid\nflowchart TB\n%% packages\n"
	for _, sub := range []string{`subgraph tests ["imported only by test files"]`, `subgraph testonly ["helpers"]`} {
		doc := head + sub + "\na[\"internal/a\"]\nend\n```"
		if _, err := archDrawn(doc); err == nil || !strings.Contains(err.Error(), "has the id testonly and the title") {
			t.Errorf("archDrawn(%q) = %v, want the refusal of a test-only box named halfway", sub, err)
		}
	}
	if _, err := archKinds("```mermaid\nflowchart LR\n%% kinds\nsubgraph kinds [\"k\"]\n\n%% a comment\nk_a[\"a\"]\nend\n```"); err != nil {
		t.Errorf("archKinds refused a blank line or a comment in the subgraph kinds: %v", err)
	}
}
