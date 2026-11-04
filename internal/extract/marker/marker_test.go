package marker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// scanned is "rule=text@line" for each marker Scan finds in src read as
// file, or the error.
func scanned(t *testing.T, file, src string) string {
	t.Helper()
	found, err := Scan(file, []byte(src), 1<<20)
	if err != nil {
		return "error: " + err.Error()
	}
	var out []string
	for _, f := range found {
		out = append(out, fmt.Sprintf("%s=%s@%d", f.Rule, f.Value.Text, f.Value.At.Line))
	}
	return strings.Join(out, " ")
}

// A Markdown marker is an HTML comment goldmark reads as HTML, inline or
// as a comment block, and its value the prose up to its closer, in one
// block, read as one line across a wrap. Planted breaks: the prose walk
// starting at the opener's <!--; clean keeping a line feed.
func TestMarkdownMarkers(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"inline", "The API listens on port <!-- errata: api-port -->8080<!-- /errata -->.\n", "api-port=8080@1"},
		{"a block at the start of the line", "<!-- errata: a -->8080<!-- /errata -->\n", "a=8080@1"},
		{"on lines of their own", "# T\n\n<!-- errata: a -->\n8080\n<!-- /errata -->\n", "a=8080@4"},
		{"wrapped", "x <!-- errata: a -->runs on\n  node *20*<!-- /errata --> y\n", "a=runs on node 20@1"},
		{"wrapped in a quote", "> q <!-- errata: a -->1\n> 2<!-- /errata -->\n", "a=1 2@1"},
		{"value on the next line", "x <!-- errata: a -->\n2<!-- /errata -->\n", "a=2@2"},
		{"empty", "x <!--errata:a--><!-- /errata --> y\n", "a=@1"},
		{"two in a file", "<!-- errata: a -->1<!-- /errata --> and\n<!-- errata: b -->2<!-- /errata -->\n", "a=1@1 b=2@2"},
		{"a heading", "# Port <!-- errata: a -->80<!-- /errata -->\n", "a=80@1"},
		{"no marker", "<!-- a note -->\n", ""},
		{"a fenced code block", "```\n<!-- errata: a -->1<!-- /errata -->\n```\n", ""},
		{"an indented code block", "    <!-- errata: a -->1<!-- /errata -->\n", ""},
		{"a code span", "a `<!-- errata: a -->` b `<!-- /errata -->`\n", ""},
		{"a script", "<script>\n<!-- errata: a -->1<!-- /errata -->\n</script>\n", ""},
		{"crlf", "a <!-- errata: a -->1\r\n2<!-- /errata -->\r\n", "a=1 2@1"},
		{"a block with no blank before -->", "<!-- errata: a-->1<!-- /errata-->\n", "a=1@1"},
		{"a pipe in prose", "a <!-- errata: a -->x | y<!-- /errata -->\n", "a=x | y@1"},
		{"a pipe in a table cell", "| h |\n|---|\n| <!-- errata: a -->x \\| y<!-- /errata --> |\n", "a=x \\| y@3"},
	} {
		if got := scanned(t, "a.md", c.src); got != c.want {
			t.Errorf("%s: %q gives %q, want %q", c.name, c.src, got, c.want)
		}
	}
}

// A marker the walk cannot read whole is refused, never read as a value or
// passed over. Planted breaks, one per case: pairs ending without checking
// an open marker; the nesting check removed; the closer check removed;
// classify taking any id; the block check removed; blockMarks reading every
// HTML block; the Text check removed.
func TestMarkdownRefusals(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"unclosed", "a <!-- errata: a -->1\n", "line 1: the marker a opens and no <!-- /errata --> after it closes it"},
		{"nested", "<!-- errata: a -->1 <!-- errata: b -->2<!-- /errata -->\n", "the marker a opens, and nothing closes it before the marker b opens at line 1"},
		{"stray closer", "\n1<!-- /errata -->\n", "line 2: <!-- /errata --> closes no marker"},
		{"bad id", "<!-- errata: API_port -->1<!-- /errata -->\n", `the marker "errata: API_port" names no id`},
		{"bad closer", "<!-- errata: a -->1<!-- /errata: a -->\n", "a marker closes with <!-- /errata --> and nothing more"},
		{"across blocks", "<!-- errata: a -->1\n\n2<!-- /errata -->\n", "runs from one block into the next"},
		{"across table cells", "| h | i |\n|---|---|\n| <!-- errata: a -->a | b<!-- /errata --> |\n", "line 3: the value of the marker a runs from one table cell into the next"},
		{"in a div", "<div>\n<!-- errata: a -->1<!-- /errata -->\n</div>\n", "line 2: a marker inside an HTML block other than a comment"},
		{"in a pre", "<pre\n<!-- errata: a -->1<!-- /errata -->\n</pre>\n", "line 2: a marker inside an HTML block other than a comment"},
		{"a cell's value starting with the separator", "| h | i |\n|---|---|\n| x <!-- errata: a --> | b<!-- /errata --> |\n", "line 3: the value of the marker a runs from one table cell into the next"},
		{"opener with no end", "a <!-- errata: a 1\n", "line 1: a marker's <!-- that no --> closes"},
		{"block with no end", "<!-- errata: a\n1\n", "line 1: a marker's <!-- that no --> closes"},
		{"inside a comment", "x <!-- a <!-- errata: a --> y\n", "line 1: a marker's <!-- that no --> closes, or that stands inside another comment"},
		{"block inside a comment", "<!-- a <!-- errata: a -->\n", "line 1: a marker's <!-- that no --> closes, or that stands inside another comment"},
	} {
		got := scanned(t, "a.md", c.src)
		if !strings.HasPrefix(got, "error: marker: a.md: ") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q gives %q, want a refusal holding %q", c.name, c.src, got, c.want)
		}
	}
}

// A code marker stands inside a comment of the file's language; one in a
// string literal is none. A value runs on through comments with only blanks
// between them, and a block comment's * decoration is left out. Planted
// breaks: scanCode reading string spans; clean keeping the *; the gap check
// removed.
func TestCodeMarkers(t *testing.T) {
	for _, c := range []struct{ name, file, src, want string }{
		{"line comment", "a.go", "// port <!-- errata: p -->8080<!-- /errata -->\n", "p=8080@1"},
		{"string literal", "a.go", "s := \"<!-- errata: p -->1<!-- /errata -->\"\n", ""},
		{"wrapped over line comments", "a.go", "x := 1\n// port <!-- errata: p -->80\n//   81<!-- /errata -->\n", "p=80 81@2"},
		{"block comment", "a.go", "/* <!-- errata: p -->\n * 8080\n * <!-- /errata --> */\n", "p=8080@2"},
		{"hash comment", "a.py", "s = '# <!-- errata: p -->1<!-- /errata -->'\n# <!-- errata: p -->2<!-- /errata -->\n", "p=2@2"},
		{"yaml", "a.yaml", "port: 8080 # <!-- errata: p -->8080<!-- /errata -->\n", "p=8080@1"},
		{"crlf", "a.go", "// a\r\n// <!-- errata: p -->1\r\n// 2<!-- /errata -->\r\n", "p=1 2@2"},
		{"no marker", "a.go", "// errata is a tool\n", ""},
		{"no blank after //", "a.go", "//<!-- errata: p -->1<!-- /errata -->\n", "p=1@1"},
		{"wrapped with no blanks", "a.go", "//a <!-- errata: p -->80\n//81<!-- /errata -->\n", "p=80 81@1"},
		{"a value starting in the next comment", "a.go", "x := 1\n// <!-- errata: p -->\n// 8080<!-- /errata -->\n", "p=8080@3"},
	} {
		if got := scanned(t, c.file, c.src); got != c.want {
			t.Errorf("%s: %q gives %q, want %q", c.name, c.src, got, c.want)
		}
	}
	for _, c := range []struct{ name, file, src, want string }{
		{"into code", "a.go", "/* <!-- errata: p --> */ x := 1 /* <!-- /errata --> */\n", "line 1: the value of the marker p runs out of its comment into code at line 1"},
		{"no -->", "a.go", "// <!-- errata: p\n// -->1<!-- /errata -->\n", "line 1: a marker's <!-- opens here, and its comment ends before a --> closes it"},
		{"unclosed", "a.go", "\n// <!-- errata: p -->1\n", "line 2: the marker p opens and no <!-- /errata -->"},
		{"unclosed in a later comment", "a.go", "// a\n// b\n// <!-- errata: p -->1\n", "line 3: the marker p opens and no <!-- /errata -->"},
		{"no --> in a later comment", "a.go", "x := 1\n// a\n// <!-- errata: p\n", "line 3: a marker's <!-- opens here, and its comment ends before a --> closes it"},
		{"bad id in a later comment", "a.go", "x := 1\n// a\n// <!-- errata: P -->1<!-- /errata -->\n", "line 3: the marker \"errata: P\" names no id"},
		{"lost place", "a.go", "// <!-- errata: p -->1<!-- /errata -->\ns := `\n", "the lexer lost its place"},
		{"bad id", "a.go", "// <!-- errata: P -->1<!-- /errata -->\n", "names no id"},
	} {
		got := scanned(t, c.file, c.src)
		if !strings.HasPrefix(got, "error: marker: ") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q gives %q, want a refusal holding %q", c.name, c.src, got, c.want)
		}
	}
}

// In HTML and XML a marker is a comment of the file, and its value the text
// up to its closer. Planted break: scanMarkup reading every span.
func TestMarkupMarkers(t *testing.T) {
	for _, c := range []struct{ name, file, src, want string }{
		{"html", "a.html", "<p>port <!-- errata: p -->8080<!-- /errata --></p>\n", "p=8080@1"},
		{"attribute", "a.html", "<a title=\"<!-- errata: p -->\">x</a>\n", ""},
		{"xml", "a.xml", "<port>\n  <!-- errata: p -->\n  8080\n  <!-- /errata -->\n</port>\n", "p=8080@3"},
		{"a value right after its opener", "a.xml", "<a/>\n<!-- errata: p -->8080<!-- /errata -->\n", "p=8080@2"},
	} {
		if got := scanned(t, c.file, c.src); got != c.want {
			t.Errorf("%s: %q gives %q, want %q", c.name, c.src, got, c.want)
		}
	}
	got := scanned(t, "a.html", "<!-- x <!-- errata: p -->\n")
	if !strings.Contains(got, "line 1: a marker's <!-- that no --> closes, or that stands inside another comment") {
		t.Errorf("a marker inside a comment gives %q", got)
	}
}

// A file no reader of the walk reads is passed over unless it holds a
// marker, which is refused: the walk cannot tell its comments. Planted
// break: Scan passing over every such file.
func TestFileNoReaderReads(t *testing.T) {
	if got := scanned(t, "notes.txt", "errata is a tool\n<!-- a note -->\n"); got != "" {
		t.Errorf("a text file without a marker gives %q", got)
	}
	got := scanned(t, "notes.txt", "x\n<!--  errata: a -->1<!-- /errata -->\n")
	if !strings.Contains(got, "marker: notes.txt: line 2: a marker, in a file errata reads no markers in") {
		t.Errorf("a text file with a marker gives %q", got)
	}
	got = scanned(t, "notes.txt", "<!-- errata: a -->1<!-- /errata -->\n")
	if !strings.Contains(got, "marker: notes.txt: line 1: a marker, in a file errata reads no markers in") {
		t.Errorf("a text file that starts with a marker gives %q", got)
	}
}

// clean reads blanks as one space, trims both ends, and leaves out a block
// comment's decoration only when asked.
func TestClean(t *testing.T) {
	for _, c := range []struct {
		in    string
		stars bool
		want  string
		first int
	}{
		{"  a \t b\n", false, "a b", 2},
		{"\n * a\n * b", true, "a b", 4},
		{"\n * a\n * b", false, "* a * b", 2},
		{"\n*a", true, "*a", 1},
		{" \n ", true, "", -1},
		{"a\n *", true, "a", 0},
	} {
		if got, first := clean([]byte(c.in), c.stars); got != c.want || first != c.first {
			t.Errorf("clean(%q, %t) = %q, %d; want %q, %d", c.in, c.stars, got, first, c.want, c.first)
		}
	}
}

// build is the extractor of rule over files.
func build(t *testing.T, rule string, files ...string) extractor {
	t.Helper()
	x, err := Build(Spec{Files: files}.ForRule(rule))
	if err != nil {
		t.Fatal(err)
	}
	return x.(extractor)
}

// texts is the values as "file:line=text".
func texts(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%s=%s", v.At, v.Text))
	}
	return strings.Join(out, " ")
}

// The extractor walks every file git tracks, or the files files: names,
// and returns the values of the markers that name its rule, each at its
// file and line; a file git does not track is not read, nor a tracked
// symlink, whose target is read under its own name. Planted breaks: Files
// keeping links; Extract keeping every rule's markers.
func TestExtractWalksTrackedFiles(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"README.md":     "Port <!-- errata: api-port -->8080<!-- /errata -->.\n",
		"docs/ops.md":   "# Ops\n\nThe API is on <!-- errata: api-port -->8081<!-- /errata -->,\n<!-- errata: other -->x<!-- /errata -->\n",
		"cmd/main.go":   "package main\n\n// Listens on <!-- errata: api-port -->8080<!-- /errata -->.\n",
		"plain.txt":     "no marker here\n",
		"big/notes.bin": "\x00\x01",
	})
	// Written after git add: untracked.
	if err := os.WriteFile(filepath.Join(root, "untracked.md"), []byte("<!-- errata: api-port -->1<!-- /errata -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := build(t, "api-port").Extract(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if want := "README.md:1=8080 cmd/main.go:3=8080 docs/ops.md:3=8081"; texts(got) != want {
		t.Errorf("every file: %s, want %s", texts(got), want)
	}
	got, err = build(t, "api-port", "docs/**").Extract(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if want := "docs/ops.md:3=8081"; texts(got) != want {
		t.Errorf("docs/**: %s, want %s", texts(got), want)
	}
	got, err = build(t, "none").Extract(t.Context(), root)
	if err != nil || len(got) != 0 || got == nil {
		t.Errorf("a rule no marker names: %v %v, want an empty list", got, err)
	}
}

// A walk passes over a symlink git tracks that a glob matched, so a link
// such as AGENTS.md to CLAUDE.md leaves the default walk reading the
// target's markers once, under its own name, and a link that leads out of
// the repository is no file of it. A link files: names outright is refused,
// naming its target: read under the link's name, its marker would be bound
// to a file the built-in check reads under the target's. Planted breaks:
// Files keeping a glob's links; readFile without its symlink check.
func TestExtractLinks(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"CLAUDE.md": "<!-- errata: r -->1<!-- /errata -->\n"})
	extracttest.Symlink(t, "CLAUDE.md", filepath.Join(root, "AGENTS.md"))
	extracttest.Symlink(t, "/etc/hostname", filepath.Join(root, "out.md"))
	root = gitAdd(t, root)
	for _, files := range [][]string{nil, {"*.md"}} {
		got, err := build(t, "r", files...).Extract(t.Context(), root)
		if err != nil || texts(got) != "CLAUDE.md:1=1" {
			t.Errorf("files %v: %s %v, want CLAUDE.md:1=1", files, texts(got), err)
		}
	}
	_, err := build(t, "r", "AGENTS.md").Extract(t.Context(), root)
	extracttest.WantError(t, err, "marker: AGENTS.md is a symlink git tracks, to CLAUDE.md, and a marker walk reads each file under its own name; list CLAUDE.md in files instead")
	_, err = build(t, "r", "AGENTS.md", "*.md").Extract(t.Context(), root)
	extracttest.WantError(t, err, "marker: AGENTS.md is a symlink git tracks")
	_, err = build(t, "r", "out.md").Extract(t.Context(), root)
	extracttest.WantError(t, err, "marker: read out.md")
}

// A file reached through a directory link git tracks is refused as a link
// at the name is, and the refusal says the link is on the way, since git
// tracks the directory and not the file (markers delta review 2, E3). The
// planted break: the parent directory's resolve dropped, so the message
// says the file is a symlink.
func TestExtractThroughADirectoryLink(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"docs/a.md": "<!-- errata: r -->1<!-- /errata -->\n"})
	extracttest.Symlink(t, "docs", filepath.Join(root, "d"))
	root = gitAdd(t, root)
	_, err := build(t, "r", "d/a.md").Extract(t.Context(), root)
	extracttest.WantError(t, err, "marker: d/a.md is reached through a symlink git tracks, to docs/a.md, and a marker walk reads each file under its own name; list docs/a.md in files instead")
}

// gitAdd makes root a git repository that tracks every file in it.
func gitAdd(t *testing.T, root string) string {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// A file over max_bytes is read in pieces for the bytes errata, and refused
// when it holds them, across a piece's edge too; without them it is passed
// over. Planted breaks: contains keeping no bytes of the piece before; read
// passing over every large file.
func TestLargeFile(t *testing.T) {
	// The first piece read holds chunk+len(token) bytes, and ends inside the
	// token.
	pad := strings.Repeat("x", chunk+len(token)-3)
	root := extracttest.Tracked(t, map[string]string{
		"a.md":     "<!-- errata: r -->1<!-- /errata -->\n",
		"big.bin":  strings.Repeat("y", 3*chunk),
		"edge.txt": pad + "errata" + pad,
	})
	x, err := Build(Spec{Files: []string{"a.md", "big.bin"}, MaxBytes: 1024}.ForRule("r"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := x.Extract(t.Context(), root)
	if err != nil || texts(got) != "a.md:1=1" {
		t.Errorf("a large file without errata: %s %v", texts(got), err)
	}
	x, _ = Build(Spec{Files: []string{"edge.txt"}, MaxBytes: 1024}.ForRule("r"))
	_, err = x.Extract(t.Context(), root)
	extracttest.WantError(t, err, fmt.Sprintf("marker: edge.txt is %d bytes, over the limit of 1024, and holds the bytes errata", 2*len(pad)+6))
}

// The walk's bounds, each witnessed lowered: the files it opens, the bytes
// it reads and the markers it finds; each refusal names the real limit.
// Planted breaks, one per bound: its check removed.
func TestWalkBounds(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.md": "<!-- errata: r -->1<!-- /errata --> <!-- errata: r -->2<!-- /errata -->\n",
		"b.md": "<!-- errata: r -->3<!-- /errata -->\n",
	})
	w, err := NewWalk(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := w.Markers(t.Context(), root); err != nil || len(found) != 3 {
		t.Fatalf("unbounded: %v %v", found, err)
	}
	files := w
	files.files.FileLimit = 1
	_, err = files.Markers(t.Context(), root)
	extracttest.WantError(t, err, "marker: files names more than 100000 files, the limit")
	exact := w
	exact.Bytes, exact.Count = 108, 3 // the files' bytes and markers, at the limits
	if _, err := exact.Markers(t.Context(), root); err != nil {
		t.Errorf("at the limits: %v", err)
	}
	bytes := w
	bytes.Bytes = 80
	_, err = bytes.Markers(t.Context(), root)
	extracttest.WantError(t, err, "marker: b.md: the files read hold more than 2147483648 bytes, the limit of one walk")
	markers := w
	markers.Count = 2
	_, err = markers.Markers(t.Context(), root)
	extracttest.WantError(t, err, "marker: b.md: the files hold more than 100000 markers, the limit")
}

// A file over the walk's bound on bytes is refused before it is read, even
// one over max_bytes that would be read in pieces; one that alone holds more
// markers than the walk's bound is refused when it is read, so a run keeps
// the refusal and not its markers. Planted breaks: readFile's two checks
// removed, one at a time.
func TestReadFileBounds(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"big.txt": strings.Repeat("x", 90) + "errata",
		"a.md":    "<!-- errata: r -->1<!-- /errata --> <!-- errata: r -->2<!-- /errata -->\n",
	})
	r, err := core.OpenReader(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = readFile(r, "big.txt", 10, Bounds{Bytes: 50, Count: 10})
	extracttest.WantError(t, err, "marker: big.txt: the files read hold more than 2147483648 bytes, the limit of one walk")
	_, err = readFile(r, "a.md", 1<<20, Bounds{Bytes: 1 << 20, Count: 1})
	extracttest.WantError(t, err, "marker: a.md: the files hold more than 100000 markers, the limit")
	if m, err := readFile(r, "a.md", 1<<20, Bounds{Bytes: 1 << 20, Count: 2}); err != nil || len(m.found) != 2 {
		t.Errorf("at the bound: %v %v", m, err)
	}
}

// A walk stats each file against what it has left of its bound on bytes
// before the shared read, so it reads no file that crosses the bound
// (markers delta review 2, E1): b.md, whose unclosed marker a read would
// refuse, is refused for the walk's bytes, unread. The planted break: the
// stat in Read removed, so b.md is read and refused for its marker.
func TestReadStatsBeforeTheSharedRead(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.md": strings.Repeat("a", 40) + "\n",
		"b.md": "<!-- errata: r -->" + strings.Repeat("b", 22) + "\n",
	})
	names := []Name{{File: "a.md", Limit: 1 << 20}, {File: "b.md", Limit: 1 << 20}}
	_, err := Read(t.Context(), root, names, Bounds{Bytes: 60, Count: 10})
	extracttest.WantError(t, err, "marker: b.md: the files read hold more than 2147483648 bytes, the limit of one walk")
	if _, err := Read(t.Context(), root, names[:1], Bounds{Bytes: 41, Count: 10}); err != nil {
		t.Errorf("a file exactly at the bound: %v", err)
	}
}

// The extractor needs its rule, which config gives it, and a files: of no
// entry does not load. Planted breaks: Build's rule check removed; NewWalk
// reading an empty list as every file.
func TestBuildRefuses(t *testing.T) {
	_, err := Build(Spec{})
	extracttest.WantError(t, err, "marker: the extractor reads the markers that name its rule, and was built outside one")
	_, err = Build(Spec{Files: []string{}}.ForRule("r"))
	extracttest.WantError(t, err, "marker: files lists no entry")
	_, err = Build(Spec{Files: []string{"../x"}}.ForRule("r"))
	extracttest.WantError(t, err, "outside the repository")
}

// A sparse checkout that leaves a file out makes the walk unknown, as for
// any glob (ADR 0008), since a marker there would go unread.
func TestSparseCheckout(t *testing.T) {
	root := extracttest.Sparse(t, map[string]string{"a.md": "x\n", "docs/b.md": "<!-- errata: r -->1<!-- /errata -->\n"}, false)
	_, err := build(t, "r").Extract(t.Context(), root)
	extracttest.WantError(t, err, "sparse checkout")
}

// Place writes every file git tracks out and drops max_bytes; Resolve
// writes both out and shares no slice.
func TestPlaceAndResolve(t *testing.T) {
	if p := Place(Spec{MaxBytes: 9}); fmt.Sprint(p.Files) != "[**]" || p.MaxBytes != 0 {
		t.Errorf("Place: %+v", p)
	}
	if p := Place(Spec{Files: []string{"b/../c", "a"}}); fmt.Sprint(p.Files) != "[a c]" {
		t.Errorf("Place: %+v", p)
	}
	given := Spec{Files: []string{"a"}}
	r := Resolve(given)
	r.Files[0] = "changed"
	if given.Files[0] != "a" || r.MaxBytes != 32<<20 || fmt.Sprint(Resolve(Spec{}).Files) != "[**]" {
		t.Errorf("Resolve: %+v from %+v", r, given)
	}
}

// This package's own source holds no marker and nothing a walk refuses, so
// errata's repository can walk it: its comments describe the syntax in
// words. Planted break: the package comment quoting a marker whole.
func TestOwnSourceReadsClean(t *testing.T) {
	data, err := os.ReadFile("marker.go")
	if err != nil {
		t.Fatal(err)
	}
	if found, err := Scan("internal/extract/marker/marker.go", data, 1<<30); err != nil || len(found) != 0 {
		t.Errorf("marker.go: %v %v", found, err)
	}
}

// In one run a walk reads each file once: a file changed after the first
// walk gives the next walk of the run the first read's markers, which a
// new run reads again. The second walk opens no file. Planted breaks: Read
// outside the run's results; Read closing a reader it never opened.
func TestReadOncePerRun(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"a.md": "<!-- errata: r -->1<!-- /errata -->\n"})
	x := build(t, "r")
	ctx := check.WithRun(t.Context())
	if got, err := x.Extract(ctx, root); err != nil || texts(got) != "a.md:1=1" {
		t.Fatalf("first: %s %v", texts(got), err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("<!-- errata: r -->2<!-- /errata -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := x.Extract(ctx, root); err != nil || texts(got) != "a.md:1=1" {
		t.Errorf("the same run: %s %v, want the first read", texts(got), err)
	}
	if got, err := x.Extract(check.WithRun(t.Context()), root); err != nil || texts(got) != "a.md:1=2" {
		t.Errorf("a new run: %s %v, want a.md:1=2", texts(got), err)
	}
}

// A file of exactly max_bytes is parsed; one byte more is read in pieces.
// Planted break: the size check one byte early.
func TestFileAtTheLimit(t *testing.T) {
	src := "<!-- errata: r -->1<!-- /errata -->\n"
	root := extracttest.Tracked(t, map[string]string{"a.md": src})
	x, err := Build(Spec{MaxBytes: int64(len(src))}.ForRule("r"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := x.Extract(t.Context(), root); err != nil || texts(got) != "a.md:1=1" {
		t.Errorf("a file of max_bytes: %s %v", texts(got), err)
	}
	x, _ = Build(Spec{MaxBytes: int64(len(src)) - 1}.ForRule("r"))
	_, err = x.Extract(t.Context(), root)
	extracttest.WantError(t, err, "over the limit of")
}
