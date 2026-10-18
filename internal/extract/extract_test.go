package extract

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/f-klement/errata/internal/check"
)

// repo writes files into a new repository root and returns it.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	e, err := Build(s)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return e.Extract(root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	got, err := run(t, s, root)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return got
}

// texts lists the values' text and line as "text@line".
func texts(vs []check.Value) string {
	var b strings.Builder
	for i, v := range vs {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(v.Text + "@" + strconv.Itoa(v.At.Line))
	}
	return b.String()
}

// wantError fails unless err is set and its message holds want.
func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// each kind with its file set to f and a valid path or pattern.
func eachKind(f string) map[string]Spec {
	return map[string]Spec{
		"yaml":  {YAML: &YAML{File: f, Path: "a"}},
		"json":  {JSON: &JSON{File: f, Path: "a"}},
		"toml":  {TOML: &TOML{File: f, Path: "a"}},
		"regex": {Regex: &Regex{File: f, Pattern: "(a)"}},
		"text":  {Text: &Text{File: f, Pattern: "(a)"}},
	}
}

func TestBuildRefusesNoKindAndTwoKinds(t *testing.T) {
	for name, s := range map[string]Spec{
		"none": {},
		"two":  {YAML: &YAML{File: "a", Path: "a"}, JSON: &JSON{File: "a", Path: "a"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(s)
			wantError(t, err, "an extractor sets exactly one of yaml, json, toml, regex")
		})
	}
}

// The planted breaks: an absolute path, a .. that leaves the root, and one
// that leaves it after entering a directory. Each kind refuses each one.
func TestBuildRefusesFileOutsideRepository(t *testing.T) {
	for _, f := range []string{"/etc/passwd", "../secret.yaml", "a/../../secret.yaml", ".."} {
		for kind, s := range eachKind(f) {
			_, err := Build(s)
			wantError(t, err, kind+": file \""+f+"\" is outside the repository")
		}
	}
	for kind, s := range eachKind("") {
		_, err := Build(s)
		wantError(t, err, kind+": file is required")
	}
}

// The planted break: a kind with every field empty, as {yaml: {}} decodes,
// and then each required field empty on its own. Build names the field.
func TestBuildRefusesEmptyField(t *testing.T) {
	for want, s := range map[string]Spec{
		"yaml: file is required":                                 {YAML: &YAML{}},
		"json: file is required":                                 {JSON: &JSON{}},
		"toml: file is required":                                 {TOML: &TOML{}},
		"regex: file is required":                                {Regex: &Regex{}},
		"yaml: path is required; write $ for the whole document": {YAML: &YAML{File: "f"}},
		"json: path is required; write $ for the whole document": {JSON: &JSON{File: "f"}},
		"toml: path is required; write $ for the whole document": {TOML: &TOML{File: "f"}},
		"regex: pattern is required":                             {Regex: &Regex{File: "f"}},
	} {
		_, err := Build(s)
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

func TestBuildAcceptsDotDotThatStaysInside(t *testing.T) {
	for kind, s := range eachKind("a/../b.txt") {
		if _, err := Build(s); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// symlink makes link, inside root, point at target.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// A symlink that resolves outside the repository is only knowable when the
// file is read, so the refusal comes from Extract and the rule is unknown.
// The planted breaks: a relative link out, an absolute link out, a link to a
// directory outside in the middle of the path, links to a file outside that
// does not exist, directly and through that directory, a link to / with the
// rest of the path to the file outside after it, and a link that goes out
// through a directory outside and back into the repository, which ends
// inside but leaves by a directory that is not one of the root's parents.
// Every refusal is the same message, which names neither where the link
// points nor whether that exists.
func TestExtractRefusesSymlinkOutOfRepository(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := repo(t, nil)
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(rel, "secret"), filepath.Join(root, "relative.txt"))
	symlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "absolute.txt"))
	symlink(t, rel, filepath.Join(root, "middle"))
	symlink(t, filepath.Join(rel, "nope"), filepath.Join(root, "dangling.txt"))
	symlink(t, "/", filepath.Join(root, "slash"))
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Join would clean the route out and back away.
	symlink(t, rel+"/../"+filepath.Base(root)+"/a", filepath.Join(root, "outthenback.yaml"))
	if kernel, err := os.ReadFile(filepath.Join(root, "outthenback.yaml")); err != nil || string(kernel) != "a: 1\n" {
		t.Fatalf("the kernel reads %q, %v", kernel, err)
	}
	for _, f := range []string{"relative.txt", "absolute.txt", "middle/secret", "dangling.txt", "middle/nope", "slash" + outside + "/secret", "slash" + outside + "/nope", "outthenback.yaml"} {
		for kind, s := range eachKind(f) {
			_, err := run(t, s, root)
			want := "read " + f + ": a symlink leads outside the repository"
			if err == nil || err.Error() != want {
				t.Errorf("%s: error %v, want %q", kind, err, want)
			}
		}
	}
}

// A symlink that resolves inside the repository is read, whether it is
// relative, absolute, or relative by a route that goes up through the root's
// own parents and comes back. Each location names the file as the spec wrote
// it.
func TestExtractReadsSymlinkThatStaysInside(t *testing.T) {
	root := repo(t, map[string]string{"real/f.yaml": "a: 1\n"})
	symlink(t, "real/f.yaml", filepath.Join(root, "relative.yaml"))
	symlink(t, filepath.Join(root, "real", "f.yaml"), filepath.Join(root, "absolute.yaml"))
	symlink(t, filepath.Join("..", filepath.Base(root), "real", "f.yaml"), filepath.Join(root, "back.yaml"))
	symlink(t, "../real", filepath.Join(root, "dir", "link"))
	symlink(t, "relative.yaml", filepath.Join(root, "chain.yaml"))
	for _, f := range []string{"relative.yaml", "absolute.yaml", "back.yaml", "dir/link/f.yaml", "chain.yaml"} {
		got, err := run(t, Spec{YAML: &YAML{File: f, Path: "a"}}, root)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if texts(got) != "1@1" || got[0].At.File != f {
			t.Errorf("%s: got %s in %q", f, texts(got), got[0].At.File)
		}
	}
}

// A .. in a link's target goes up from where the link points, as the kernel
// reads it, while a .. in file is resolved by name before any link. The
// planted break is cleaning the target by name, which would read
// real/f.yaml for link.yaml instead of deep/real/f.yaml.
func TestExtractResolvesDotDotAsTheKernelDoes(t *testing.T) {
	root := repo(t, map[string]string{"real/f.yaml": "a: top\n", "deep/real/f.yaml": "a: deep\n", "deep/dir/x": ""})
	symlink(t, "deep/dir", filepath.Join(root, "sub"))
	symlink(t, "sub/../real/f.yaml", filepath.Join(root, "link.yaml"))
	kernel, err := os.ReadFile(filepath.Join(root, "link.yaml"))
	if err != nil || string(kernel) != "a: deep\n" {
		t.Fatalf("the kernel reads %q, %v", kernel, err)
	}
	for f, want := range map[string]string{"link.yaml": "deep@1", "sub/../real/f.yaml": "top@1"} {
		if got := texts(mustRun(t, Spec{YAML: &YAML{File: f, Path: "a"}}, root)); got != want {
			t.Errorf("%s: got %q, want %q", f, got, want)
		}
	}
}

// A file that cannot be read is named as the spec wrote it, with the reason
// and no absolute path: a missing file, a dangling link inside, a file used
// as a directory, a directory, and a loop of links.
func TestExtractUnreadableFileIsAnError(t *testing.T) {
	root := repo(t, map[string]string{"real/f.txt": "a"})
	symlink(t, "nowhere.txt", filepath.Join(root, "dangling.txt"))
	symlink(t, "loop2.txt", filepath.Join(root, "loop1.txt"))
	symlink(t, "loop1.txt", filepath.Join(root, "loop2.txt"))
	for f, want := range map[string]string{
		"absent.txt":   "read absent.txt: no such file or directory",
		"dangling.txt": "read dangling.txt: no such file or directory",
		"real/f.txt/x": "read real/f.txt/x: not a directory",
		"real":         "read real: is a directory",
		"loop1.txt":    "read loop1.txt: too many levels of symbolic links",
	} {
		for kind, s := range eachKind(f) {
			_, err := run(t, s, root)
			if err == nil || err.Error() != want {
				t.Errorf("%s: error %v, want %q", kind, err, want)
			}
		}
	}
}

func TestExtractMissingRootIsAnError(t *testing.T) {
	for kind, s := range eachKind("a.txt") {
		_, err := run(t, s, filepath.Join(t.TempDir(), "gone"))
		want := "open the repository root: no such file or directory"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
}

func TestExtractFileThatDoesNotParseIsAnError(t *testing.T) {
	root := repo(t, map[string]string{"bad": "a: [\n"})
	for want, s := range map[string]Spec{
		"bad: yaml: line 1: did not find expected node content":     {YAML: &YAML{File: "bad", Path: "a"}},
		"bad: invalid character 'a' looking for beginning of value": {JSON: &JSON{File: "bad", Path: "a"}},
		"bad: toml: line 1: expected '=' after key":                 {TOML: &TOML{File: "bad", Path: "a"}},
	} {
		_, err := run(t, s, root)
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

func TestLineAt(t *testing.T) {
	data := []byte("ab\ncd\n\nx")
	l := lineIndex(data)
	for off, want := range map[int]int{0: 1, 2: 1, 3: 2, 5: 2, 6: 3, 7: 4, 8: 4} {
		if got := l.at(off); got != want {
			t.Errorf("at(%d) = %d, want %d", off, got, want)
		}
	}
	// The cursor gives the index's line for offsets forward, the same
	// offset again, and back.
	c := newLineCursor(data)
	for _, off := range []int{0, 3, 3, 7, 8, 2, 6, 0, 8} {
		if got, want := c.at(off), l.at(off); got != want {
			t.Errorf("cursor at(%d) = %d, want %d", off, got, want)
		}
	}
}

// A read follows at most maxLinks symlinks, as Linux does: a chain of that
// many reaches the file, and one more is a loop.
func TestExtractFollowsAtMostMaxLinks(t *testing.T) {
	root := repo(t, map[string]string{"f.txt": "v=1"})
	prev := "f.txt"
	for i := range maxLinks + 1 {
		name := fmt.Sprintf("l%d", i)
		symlink(t, prev, filepath.Join(root, name))
		prev = name
	}
	if got := texts(mustRun(t, Spec{Regex: &Regex{File: fmt.Sprintf("l%d", maxLinks-1), Pattern: `v=(\d)`}}, root)); got != "1@1" {
		t.Fatalf("a chain of %d links: got %q", maxLinks, got)
	}
	_, err := run(t, Spec{Regex: &Regex{File: fmt.Sprintf("l%d", maxLinks), Pattern: `v=(\d)`}}, root)
	if want := fmt.Sprintf("read l%d: too many levels of symbolic links", maxLinks); err == nil || err.Error() != want {
		t.Fatalf("a chain of %d links: error %v, want %q", maxLinks+1, err, want)
	}
}

// nest wraps inner in n pairs of brackets.
func nest(n int, inner string) string {
	return strings.Repeat("[", n) + inner + strings.Repeat("]", n)
}

// The depth of a value is the count of path steps that reach it. Each format
// reads a value at exactly maxDepth and refuses one a level deeper, which is
// the planted break.
func TestExtractDepthBoundary(t *testing.T) {
	deep := func(first string, n int) string { return first + strings.Repeat("[0]", n) }
	for _, c := range []struct {
		file, body, path string
	}{
		{"f.json", nest(maxDepth, "1"), deep("$", maxDepth)},
		{"f.json", strings.Repeat(`{"a":`, maxDepth) + "1" + strings.Repeat("}", maxDepth), "$" + strings.Repeat(".a", maxDepth)},
		{"f.yaml", "a: " + nest(maxDepth-1, "1") + "\n", deep("a", maxDepth-1)},
		{"f.toml", "a = " + nest(maxDepth-1, "1") + "\n", deep("a", maxDepth-1)},
	} {
		s := map[string]Spec{
			"f.json": {JSON: &JSON{File: c.file, Path: c.path}},
			"f.yaml": {YAML: &YAML{File: c.file, Path: c.path}},
			"f.toml": {TOML: &TOML{File: c.file, Path: c.path}},
		}[c.file]
		if got := texts(mustRun(t, s, repo(t, map[string]string{c.file: c.body}))); got != "1@1" {
			t.Errorf("%s at depth %d: got %q", c.file, maxDepth, got)
		}
		_, err := run(t, s, repo(t, map[string]string{c.file: strings.Replace(c.body, "1", "[1]", 1)}))
		want := c.file + ": line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

// The kinds table is the only place Build counts and dispatches, so every
// field of Spec must have a row, under its JSON name, in field order. A Spec
// with only that field set builds through that row: the error names the kind.
func TestKindsCoverSpec(t *testing.T) {
	typ := reflect.TypeFor[Spec]()
	kinds := Spec{}.kinds()
	if typ.NumField() != len(kinds) {
		t.Fatalf("Spec has %d fields and kinds lists %d", typ.NumField(), len(kinds))
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if kinds[i].name != name {
			t.Errorf("field %s is %q in JSON, row %d is %q", f.Name, name, i, kinds[i].name)
		}
		var s Spec
		reflect.ValueOf(&s).Elem().Field(i).Set(reflect.New(f.Type.Elem()))
		_, err := Build(s)
		wantError(t, err, name+": file is required")
	}
}

// withMaxBytes is s with max_bytes set to n.
func withMaxBytes(s Spec, n int64) Spec {
	switch {
	case s.YAML != nil:
		y := *s.YAML
		y.MaxBytes = n
		return Spec{YAML: &y}
	case s.JSON != nil:
		j := *s.JSON
		j.MaxBytes = n
		return Spec{JSON: &j}
	case s.TOML != nil:
		x := *s.TOML
		x.MaxBytes = n
		return Spec{TOML: &x}
	case s.Text != nil:
		x := *s.Text
		x.MaxBytes = n
		return Spec{Text: &x}
	default:
		r := *s.Regex
		r.MaxBytes = n
		return Spec{Regex: &r}
	}
}

// Each extractor reads a file of exactly max_bytes, and refuses a file one
// byte over it before reading; the planted break is that byte. The value
// sits at the end of the file, so a read that stopped short would lose it.
func TestExtractFileSizeLimit(t *testing.T) {
	const limit = 1000
	for _, c := range []struct {
		file string
		tail string
		spec Spec
	}{
		{"f.yaml", "\na: 1", Spec{YAML: &YAML{File: "f.yaml", Path: "a"}}},
		{"f.toml", "\na = 1", Spec{TOML: &TOML{File: "f.toml", Path: "a"}}},
		{"f.json", "\n[1]", Spec{JSON: &JSON{File: "f.json", Path: "[0]"}}},
		{"f.txt", "\na: 1", Spec{Regex: &Regex{File: "f.txt", Pattern: "a: (1)"}}},
		{"f.md", "\na: 1", Spec{Text: &Text{File: "f.md", Pattern: "a: (1)"}}},
	} {
		pad := "#"
		if c.file == "f.json" {
			pad = " "
		}
		body := pad + strings.Repeat(" ", limit-len(c.tail)-1) + c.tail
		got := mustRun(t, withMaxBytes(c.spec, limit), repo(t, map[string]string{c.file: body}))
		if len(got) != 1 || got[0].Text != "1" {
			t.Errorf("%s of %d bytes: got %s", c.file, len(body), texts(got))
		}
		_, err := run(t, withMaxBytes(c.spec, limit), repo(t, map[string]string{c.file: " " + body}))
		want := "read " + c.file + ": the file is 1001 bytes, over the limit of 1000 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", c.file, err, want)
		}
	}
}

// Without max_bytes, every kind refuses a file one byte over 32 MiB, and a
// max_bytes above that raises the limit: the file is sparse, so the test
// writes nothing, and the refusal comes before a byte is read.
func TestExtractDefaultFileSizeLimit(t *testing.T) {
	root := repo(t, nil)
	f, err := os.Create(filepath.Join(root, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(defaultMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for kind, s := range eachKind("huge") {
		_, err := run(t, s, root)
		want := "read huge: the file is 33554433 bytes, over the limit of 33554432 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
	// A max_bytes over the default is the limit, not capped at the default.
	e, err := Build(withMaxBytes(Spec{JSON: &JSON{File: "huge", Path: "a"}}, defaultMaxBytes+1))
	if err != nil || e.(treeExtractor).limit != defaultMaxBytes+1 {
		t.Errorf("max_bytes over the default: %v, %+v", err, e)
	}
}

// The planted breaks: a negative max_bytes, which no file can meet, one
// byte over the ceiling, and the largest int64, which is how someone asks
// for no limit and which read every file as empty before. The ceiling itself
// builds.
func TestBuildRefusesMaxBytesOutsideItsRange(t *testing.T) {
	for kind, s := range eachKind("f") {
		for _, n := range []int64{-1, maxMaxBytes + 1, math.MaxInt64} {
			_, err := Build(withMaxBytes(s, n))
			want := kind + ": max_bytes is " + strconv.FormatInt(n, 10) + "; write a number of bytes from 1 to 67108864, or leave it out for 33554432"
			if err == nil || err.Error() != want {
				t.Errorf("error %v, want %q", err, want)
			}
		}
		if _, err := Build(withMaxBytes(s, maxMaxBytes)); err != nil {
			t.Errorf("%s: max_bytes at the ceiling: %v", kind, err)
		}
	}
}

// Every kind builds with a file name of maxFileName bytes and refuses one a
// byte longer, quoting a short part of it. The planted break is that byte.
func TestBuildRefusesAFileNameOverItsLimit(t *testing.T) {
	for kind, s := range eachKind(strings.Repeat("a", maxFileName)) {
		if _, err := Build(s); err != nil {
			t.Errorf("%s: a file name of %d bytes: %v", kind, maxFileName, err)
		}
	}
	for kind, s := range eachKind(strings.Repeat("a", maxFileName+1)) {
		_, err := Build(s)
		want := kind + `: file "` + strings.Repeat("a", maxQuote) + `"... is 4097 bytes long, over the limit of 4096; write a shorter path`
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

// The read cannot overflow whatever its limit: a source with the largest
// int64 as its limit, which Build refuses, still reads the whole file. The
// planted break is reading limit+1 bytes, which wraps to a negative count
// and reads nothing.
func TestReadDoesNotOverflowAtTheLargestLimit(t *testing.T) {
	root := repo(t, map[string]string{"f": "a: 1\n"})
	data, err := source{file: "f", limit: math.MaxInt64}.read(root)
	if err != nil || string(data) != "a: 1\n" {
		t.Errorf("read %q, %v", data, err)
	}
}

// An extractor returns maxValues values, and refuses one more, as the
// planted break. The yaml, json and toml extractors share values, so JSON
// stands for the three.
func TestExtractValueLimit(t *testing.T) {
	list := func(n int) string { return "[" + strings.Repeat("1,", n-1) + "1]" }
	for _, c := range []struct {
		file string
		body func(n int) string
		spec Spec
	}{
		{"f.json", list, Spec{JSON: &JSON{File: "f.json", Path: "[*]"}}},
		// (\d*) matches, empty, at each of the n positions of n-1 bytes.
		{"f.txt", func(n int) string { return strings.Repeat("a", n-1) }, Spec{Regex: &Regex{File: "f.txt", Pattern: `(\d*)`}}},
	} {
		if got := mustRun(t, c.spec, repo(t, map[string]string{c.file: c.body(maxValues)})); len(got) != maxValues {
			t.Errorf("%s: %d values, want %d", c.file, len(got), maxValues)
		}
		_, err := run(t, c.spec, repo(t, map[string]string{c.file: c.body(maxValues + 1)}))
		want := c.file + ": the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", c.file, err, want)
		}
	}
}

// A step of a path that selects more than maxValues nodes stops the path,
// even when a later step would select fewer.
func TestEvalStopsAtTheValueLimit(t *testing.T) {
	list := &node{shape: sequence}
	for range maxValues + 1 {
		list.items = append(list.items, newMapping(1))
	}
	p, err := parsePath("[*].absent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.eval(list); !errors.Is(err, errTooManyValues) {
		t.Errorf("a step of 100001 nodes: %v", err)
	}
	list.items = list.items[:maxValues]
	if got, err := p.eval(list); err != nil || len(got) != 0 {
		t.Errorf("a step of 100000 nodes: %d, %v", len(got), err)
	}
}

// Each format reads a file that can hold exactly its node limit, and refuses
// one more node, as the planted break. A max_bytes of 1000 nodes' worth keeps
// the files small; the limit scales with it.
func TestNodeLimit(t *testing.T) {
	items := func(head string, n int) string { return head + strings.Repeat("1,", n-1) + "1]\n" }
	for _, c := range []struct {
		file string
		f    *format
		head string
		n    int // the items that fill the limit
		spec Spec
	}{
		{"f.json", jsonFormat, "[", 498, Spec{JSON: &JSON{File: "f.json", Path: "[0]"}}},
		{"f.yaml", yamlFormat, "a: [", 496, Spec{YAML: &YAML{File: "f.yaml", Path: "a[0]"}}},
		{"f.toml", tomlFormat, "a = [", 496, Spec{TOML: &TOML{File: "f.toml", Path: "a[0]"}}},
	} {
		spec := withMaxBytes(c.spec, 1000*c.f.bytesPerNode)
		if got := mustRun(t, spec, repo(t, map[string]string{c.file: items(c.head, c.n)})); texts(got) != "1@1" {
			t.Errorf("%s at the limit: got %s", c.file, texts(got))
		}
		_, err := run(t, spec, repo(t, map[string]string{c.file: items(c.head, c.n+1)}))
		want := c.file + ": the file can hold up to 1002 nodes, over the limit of 1000, one for every " +
			strconv.Itoa(int(c.f.bytesPerNode)) + " bytes of max_bytes; set max_bytes: higher to read a file this dense"
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

// The ordinary files the node limit was measured on can hold, at their full
// size, at most a quarter of the nodes the default allows: an OpenAPI
// document of 4.66 MB, a Helm index of 6.3 MB and a uv.lock of 7.9 MB. Each
// generator repeats one unit whose count of node starts does not depend on
// its index, so the count is linear in the units, and two small files give
// it at the full size.
func TestNodeLimitLeavesRoomForOrdinaryFiles(t *testing.T) {
	for _, c := range []struct {
		name  string
		f     *format
		gen   func(n int) string
		units int
	}{
		{"OpenAPI of 3000 paths", yamlFormat, openAPI, 3000},
		{"Helm index of 220 charts of 40 versions", yamlFormat, func(n int) string { return helmIndex(n, 40) }, 220},
		{"uv.lock of 5000 packages", tomlFormat, func(n int) string { return uvLock(n, 20) }, 5000},
	} {
		one, two := c.f.countNodes([]byte(c.gen(1))), c.f.countNodes([]byte(c.gen(2)))
		if three := c.f.countNodes([]byte(c.gen(3))); three-two != two-one {
			t.Fatalf("%s: %d, %d, %d nodes for 1, 2, 3 units, not linear", c.name, one, two, three)
		}
		size1, size2 := len(c.gen(1)), len(c.gen(2))
		nodes, size := one+(c.units-1)*(two-one), size1+(c.units-1)*(size2-size1)
		if limit := int(defaultMaxBytes / c.f.bytesPerNode); 4*nodes > limit {
			t.Errorf("%s, %d bytes: %d nodes, more than a quarter of %d", c.name, size, nodes, limit)
		}
	}
}

// A message quotes at most maxQuote bytes of file text, since a key, an
// anchor or a number can be as long as the file. Each body holds text of
// 4 KiB; the planted break is each message quoting all of it. A message
// still names its refusal, which the start of each want checks, and the
// part of the text it quotes.
func TestMessagesQuoteAShortPartOfTheFile(t *testing.T) {
	messagesQuoteAShortPart(t, 1<<12)
}

// The same at 64 KiB, which -race slows past make test's time.
func TestMessagesQuoteAShortPartOfALargeFile(t *testing.T) {
	slowOnly(t)
	messagesQuoteAShortPart(t, 1<<16)
}

// messagesQuoteAShortPart is TestMessagesQuoteAShortPartOfTheFile with
// text of n bytes, n over 1024, past which a YAML key needs ?.
func messagesQuoteAShortPart(t *testing.T, n int) {
	t.Helper()
	long := strings.Repeat("a", n)
	nul := strings.Repeat(`\0`, n)
	short := strings.Repeat("a", maxQuote)
	for _, c := range []struct {
		file, body, want string
	}{
		// A key over 1024 characters needs ? in YAML.
		{"f.yaml", "? " + long + "\n: 1\n? " + long + "\n: 2\n", `f.yaml: line 3: key "` + short + `"... is repeated in one mapping`},
		{"f.yaml", "x: &k \"" + nul + "\"\nm:\n  ? \"" + nul + "\"\n  : 1\n  ? *k\n  : 2\n", `f.yaml: line 5: key "` + strings.Repeat(`\x00`, maxQuote) + `"... is repeated in one mapping`},
		{"f.yaml", "a: &" + long + " [*" + long + "]\n", "f.yaml: line 1: alias *" + short + "... is inside the anchor it names"},
		{"f.yaml", "m: &" + long + " x\nn:\n  ? *" + long + "\n  : 1\n  k: &" + long + " y\n  ? *" + long + "\n  : 2\n", "f.yaml: line 6: alias key *" + short + "... is repeated in one mapping"},
		{"f.yaml", "a: *" + long + "\n", "f.yaml: yaml: unknown anchor '" + strings.Repeat("a", 4*maxQuote-len("yaml: unknown anchor '")) + "..."},
		{"f.yaml", "a: !!int " + long + "\n", `f.yaml: line 1: "` + short + `"... is not a valid !!int`},
		{"f.json", `{"` + long + `": 1, "` + long + `": 2}`, `f.json: line 1: key "` + short + `"... is repeated`},
		{"f.toml", "a = 1" + strings.Repeat("1", n) + "\n", "f.toml: line 1: 1" + strings.Repeat("1", maxQuote-1) + "... is not a valid integer: it does not fit in 64 bits"},
		{"f.toml", "a = 1" + strings.Repeat("1", n) + ".0\n", "f.toml: line 1: 1" + strings.Repeat("1", maxQuote-1) + "... is not a valid float: it does not fit in 64 bits"},
		{"f.toml", long + " = 1\n" + long + " = 2\n", "f.toml: line 2: " + short + "... is already defined, as a value"},
		{"f.toml", "[" + long + "]\n[" + long + "]\n", "f.toml: line 2: " + short + "... is already defined, as a table"},
	} {
		spec := map[string]Spec{
			"f.yaml": {YAML: &YAML{File: c.file, Path: "a"}},
			"f.json": {JSON: &JSON{File: c.file, Path: "a"}},
			"f.toml": {TOML: &TOML{File: c.file, Path: "a"}},
		}[c.file]
		_, err := run(t, spec, repo(t, map[string]string{c.file: c.body}))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: error of %d bytes %.300q, want %q", c.file, len(fmt.Sprint(err)), fmt.Sprint(err), c.want)
		}
	}
}

// A message quotes at most maxQuote bytes of a path, a pattern, a file or a
// selector from the configuration too, since each can be as long as the
// configuration, and so do regexp's errors, which quote the pattern again.
// Each row holds text of 4 KiB; the planted breaks are the messages that
// quoted it whole. Each message must start with its want and stay under
// 400 bytes.
func TestMessagesQuoteAShortPartOfTheConfiguration(t *testing.T) {
	long := strings.Repeat("a", 1<<12)
	short := strings.Repeat("a", maxQuote)
	for _, c := range []struct {
		spec       Spec
		file, body string // read when Build accepts the spec
		want       string
	}{
		{Spec{JSON: &JSON{File: "f.json", Path: "['" + long + "']"}}, "f.json", `{"` + long + `": {}}`,
			`f.json: path "['` + short[:maxQuote-2] + `"... selects a mapping at line 1, not a scalar; add [*] to take its members`},
		{Spec{JSON: &JSON{File: "f.json", Path: long + "["}}, "", "",
			`json: path "` + short + `"...: column 4098: expected a quoted key, an index or * after [ but the path ends`},
		{Spec{JSON: &JSON{File: "f.json", Path: "a.1" + long}}, "", "",
			`json: path "a.1` + short[:maxQuote-3] + `"...: column 3: a key after . cannot start with a digit; write [n] for an index, or ['1` + short[:maxQuote-1] + `...'] for a key`},
		{Spec{JSON: &JSON{File: "f.json", Path: "a.-" + long}}, "", "",
			`json: path "a.-` + short[:maxQuote-3] + `"...: column 3: a key after . cannot start with -; write ['-` + short[:maxQuote-1] + `...']`},
		{Spec{JSON: &JSON{File: "f.json", Path: "a[" + strings.Repeat("9", 1<<12) + "]"}}, "", "",
			`json: path "a[` + strings.Repeat("9", maxQuote-2) + `"...: column 3: index ` + strings.Repeat("9", maxQuote) + `... is out of range`},
		{Spec{JSON: &JSON{File: "f.json", Path: "a[?@ == " + long + "]"}}, "", "",
			`json: path "a[?@ == ` + short[:maxQuote-8] + `"...: column 9: expected @ or a literal in the filter but found ` + short + `...; quote a string`},
		{Spec{JSON: &JSON{File: "f.json", Path: "a[?@ == " + long + "()]"}}, "", "",
			`json: path "a[?@ == ` + short[:maxQuote-8] + `"...: column 9: function ` + short + `...() is not supported in a filter`},
		{Spec{Regex: &Regex{File: "f.txt", Pattern: "(" + long}}, "", "",
			`regex: pattern "(` + short[:maxQuote-1] + `"...: error parsing regexp: missing closing ): ` + "`(aaa"},
		{Spec{Regex: &Regex{File: "f.txt", Pattern: "x" + long}}, "", "",
			`regex: pattern "x` + short[:maxQuote-1] + `"... has no capture group; the extractor returns group 1`},
		{Spec{Regex: &Regex{File: "../" + long[:maxFileName-3], Pattern: "(x)"}}, "", "",
			`regex: file "../` + short[:maxQuote-3] + `"... is outside the repository`},
		{Spec{YAML: &YAML{File: "f.yaml", Path: "a", Kind: long}}, "f.yaml", "a: 1\n",
			`f.yaml: no document has kind "` + short + `"... and name ""`},
	} {
		e, err := Build(c.spec)
		if err == nil {
			_, err = e.Extract(repo(t, map[string]string{c.file: c.body}))
		}
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || len(err.Error()) > 400 {
			t.Errorf("error of %d bytes %.300q, want one under 400 that starts %q", len(fmt.Sprint(err)), fmt.Sprint(err), c.want)
		}
	}
}

// utf16LE is s in UTF-16, little end first, after a byte order mark, which
// yaml.v3 reads as UTF-16.
func utf16LE(s string) string {
	b := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

// yaml.v3 breaks lines on NEL, LS and PS as on \n, and reads UTF-16 after a
// byte order mark, so the YAML count bounds the nodes of a file that uses
// them. Each body is a planted break: before, a thousand of each counted
// four nodes. é ends in the byte PS ends in, and does not count.
func TestNodeCountBoundsUnicodeLineBreaks(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("-\u0085", 1000),
		strings.Repeat("- ", 1000),
		strings.Repeat("- ", 1000),
		strings.Repeat("x --- ", 1000),
		utf16LE(strings.Repeat("- ", 1000) + "x\n"),
		utf16LE(strings.Repeat("-\u0085", 1000)),
	} {
		n := yamlNodes([]byte(body))
		if bound := yamlFormat.countNodes([]byte(body)); n < 1000 || n > bound {
			t.Errorf("%.12q: yaml.v3 builds %d nodes, the bound is %d", body, n, bound)
		}
	}
	if n := yamlFormat.countNodes([]byte(strings.Repeat("é", 1000))); n != 4 {
		t.Errorf("1000 é count %d nodes, want 4", n)
	}
}

// countNodes is four for the start of the file and two for each byte after
// which a node can start. Each row pins one way a byte counts or does not:
// a start byte, a byte that starts a node only before white space or the
// end, a NEL, LS or PS ending at a byte, a break cut short at the end of the
// file, and UTF-16 decoded as yaml.v3 decodes it, odd bytes and lone
// surrogates included. The values follow from the rule, worked by hand.
func TestCountNodesExactly(t *testing.T) {
	le := func(units ...uint16) string {
		b := []byte{0xff, 0xfe}
		for _, u := range units {
			b = append(b, byte(u), byte(u>>8))
		}
		return string(b)
	}
	for _, c := range []struct {
		name string
		f    *format
		body string
		want int
	}{
		{"an empty file", jsonFormat, "", 4},
		{"JSON's starts", jsonFormat, `{"a":[1,2]}`, 12},
		{"TOML's starts", tomlFormat, "a.b = [1,{c=2}]\n", 18},
		{"YAML's starts", yamlFormat, "a: b\r\n", 10},
		{"- before a space", yamlFormat, "- a", 6},
		{"- before a letter", yamlFormat, "-a", 4},
		{"- at the end", yamlFormat, "-", 6},
		{"? before a tab", yamlFormat, "?\tx", 6},
		{"- before a NEL", yamlFormat, "-\u0085", 8},
		{"- before a NEL cut short", yamlFormat, "-\xc2", 4},
		{"- before an LS", yamlFormat, "-\u2028", 8},
		{"- before a PS", yamlFormat, "-\u2029", 8},
		{"- before an LS cut short", yamlFormat, "-\xe2\x80", 4},
		{"- before a rune that is not a break", yamlFormat, "-\xe2\x80\xa7", 4},
		{"an LS after a letter", yamlFormat, "x\u2028", 6},
		{"an LS first in the file", yamlFormat, "\u2028", 6},
		{"the last byte of an LS alone", yamlFormat, "\xa8", 4},
		{"the last two bytes of an LS", yamlFormat, "\x80\xa8", 4},
		{"the last byte of a NEL alone", yamlFormat, "\x85", 4},
		{"a NEL", yamlFormat, "\u0085", 6},
		{"0x85 after a letter", yamlFormat, "a\x85", 4},
		{"é, which ends in PS's last byte", yamlFormat, "é", 4},
		{"- before a NEL, in JSON", jsonFormat, "-\u0085", 4},
		{"UTF-16LE", yamlFormat, le('a', ':'), 6},
		{"UTF-16BE", yamlFormat, "\xfe\xff\x00a\x00:", 6},
		{"UTF-16 with an odd last byte", yamlFormat, le('a') + ":", 4},
		{"a surrogate pair", yamlFormat, le(0xd83d, 0xde00, ':'), 6},
		{"a lone high surrogate before :", yamlFormat, le(0xd83d, ':'), 6},
		{"a lone high surrogate and an odd byte", yamlFormat, le(0xd83d) + ":", 4},
		// A high surrogate joins the unit after it, not the one before: read
		// from i-2, the pair would take the low surrogate first and skip the
		// colon, counting 6. yaml.v3 refuses this file, so only the limit's
		// message could differ.
		{"a lone low surrogate before a high one", yamlFormat, le(0xdc00, 0xd800, ':', '\n'), 8},
		{"a byte order mark in JSON", jsonFormat, le('a', ':'), 6},
		{"0xff alone first", yamlFormat, "\xff:", 6},
	} {
		if got := c.f.countNodes([]byte(c.body)); got != c.want {
			t.Errorf("%s: %q counts %d nodes, want %d", c.name, c.body, got, c.want)
		}
	}
}

// allocated is the bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// JSON and regex count lines with a cursor that only moves forward, so a
// file of newlines costs no memory for each one. The planted break is the
// index of every newline, 8 bytes each, which took 1.2 GB for a JSON file of
// 32 MiB with one node, and here 52 times the file. What JSON still
// allocates is encoding/json's buffer, which grows to hold a run of white
// space: 4 times the file. The regex extractor allocates nothing of its
// size, since the file has no CRLF to copy away.
func TestLinesCostNoMemoryForEachNewline(t *testing.T) {
	data := []byte("1" + strings.Repeat("\n", 1<<18) + "x")
	for name, spec := range map[string]Spec{
		"json":  {JSON: &JSON{File: "f", Path: "$"}},
		"regex": {Regex: &Regex{File: "f", Pattern: "(x)"}},
	} {
		e, err := Build(spec)
		if err != nil {
			t.Fatal(err)
		}
		var got []check.Value
		n := allocated(func() { got, err = e.(fromData).fromData(data) })
		if name == "json" {
			wantError(t, err, "line 262145: data after the top-level value")
		} else if err != nil || texts(got) != "x@262145" {
			t.Errorf("regex: %s, %v", texts(got), err)
		}
		if bound := map[string]uint64{"json": 5, "regex": 1}[name] * uint64(len(data)); n > bound {
			t.Errorf("%s: %d bytes allocated for a file of %d, over %d", name, n, len(data), bound)
		}
	}
}

// Place keeps every kind: for each row of the kinds table, a Spec with only
// that kind set, its file a/../f and a max_bytes, has a place that sets the
// same kind, with the file cleaned and no max_bytes, and two such specs of
// different files have different places. A kind added to Spec and kinds
// without a case in Place fails here. The planted break: Place without its
// text case, which gave every text extractor the empty place, so a rule of
// two text extractors was refused as reading one place.
func TestPlaceKeepsEveryKind(t *testing.T) {
	kindSpec := func(i int, file string) Spec {
		var s Spec
		f := reflect.ValueOf(&s).Elem().Field(i)
		f.Set(reflect.New(f.Type().Elem()))
		f.Elem().FieldByName("File").SetString(file)
		f.Elem().FieldByName("MaxBytes").SetInt(10)
		return s
	}
	for i, k := range (Spec{}).kinds() {
		p := reflect.ValueOf(kindSpec(i, "a/../f").Place()).Field(i)
		if p.IsNil() {
			t.Errorf("%s: Place drops the kind", k.name)
			continue
		}
		if file, n := p.Elem().FieldByName("File").String(), p.Elem().FieldByName("MaxBytes").Int(); file != "f" || n != 0 {
			t.Errorf("%s: Place keeps file %q and max_bytes %d, want f and 0", k.name, file, n)
		}
		if reflect.DeepEqual(kindSpec(i, "f").Place(), kindSpec(i, "g").Place()) {
			t.Errorf("%s: files f and g have one place", k.name)
		}
	}
}
