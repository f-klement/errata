package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// fakeReader returns one value for each line of its file, with the file
// as its location, and records the limit it was built with.
type fakeReader struct {
	file  string
	limit int64
	inSet bool
}

func (r fakeReader) Extract(context.Context, string) ([]check.Value, error) {
	return nil, errors.New("not read whole")
}
func (r fakeReader) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return r.Extract(ctx, root)
}
func (r fakeReader) FromShared(data []byte, _ *Spent) ([]check.Value, error) {
	var out []check.Value
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line != "" {
			out = append(out, check.Value{Text: line, At: check.Location{File: r.file, Line: i + 1}})
		}
	}
	return out, nil
}

// built records each reader a set builds.
type built struct{ readers []fakeReader }

func (b *built) build(src Source, inSet bool) (FileReader, error) {
	if strings.Contains(src.File, "refused") {
		return nil, errors.New("fake: refused " + src.File)
	}
	r := fakeReader{file: src.File, limit: src.Limit, inSet: inSet}
	b.readers = append(b.readers, r)
	return r, nil
}

// A set returns the values of each file in the files' order, each with its
// own file; it builds a reader of the first entry at load, refusing what
// that refuses, and each file's reader to what the files before it left,
// as a file of a set. Planted breaks: the probe at load dropped; the limit
// of each file the whole max_bytes; inSet false.
func TestFileSet(t *testing.T) {
	root := repo(t, map[string]string{"b.txt": "1\n2\n", "a/c.txt": "3\n", "skip.md": "x\n"})
	b := &built{}
	s, err := BuildFileSet("fake", []string{"**/*.txt"}, 100, b.build)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.readers) != 1 || b.readers[0].file != "**/*.txt" || !b.readers[0].inSet {
		t.Errorf("the probe at load: %+v", b.readers)
	}
	b.readers = nil
	vs, err := s.Extract(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, fmt.Sprintf("%s@%s:%d", v.Text, v.At.File, v.At.Line))
	}
	if want := []string{"3@a/c.txt:1", "1@b.txt:1", "2@b.txt:2"}; !slices.Equal(got, want) {
		t.Errorf("values %q, want %q", got, want)
	}
	if len(b.readers) != 2 || b.readers[0].limit != 100 || b.readers[1].limit != 98 || !b.readers[1].inSet {
		t.Errorf("readers: %+v", b.readers)
	}

	_, err = BuildFileSet("fake", []string{"refused/*.txt"}, 0, b.build)
	extracttest.WantError(t, err, "fake: refused refused/*.txt")
	_, err = BuildFileSet("fake", []string{}, 0, b.build)
	extracttest.WantError(t, err, "fake: files lists no entry")
	_, err = BuildFileSet("fake", []string{"a"}, -1, b.build)
	extracttest.WantError(t, err, "fake: max_bytes is -1")
}

// A glob that matches no file gives no value and no error, which the rule's
// allow_empty then decides; a file the set reads that is missing, or a
// reader that refuses one, fails it. Planted breaks: a missing file passed
// over; the reader's error dropped.
func TestFileSetEmptyAndRefused(t *testing.T) {
	root := repo(t, map[string]string{"a.txt": "1\n", "refused.txt": "2\n"})
	b := &built{}
	s, _ := BuildFileSet("fake", []string{"*.none"}, 0, b.build)
	if vs, err := s.Extract(t.Context(), root); err != nil || len(vs) != 0 || vs == nil {
		t.Errorf("a glob that matches none: %v, %v; want an empty list", vs, err)
	}
	s, _ = BuildFileSet("fake", []string{"missing.txt"}, 0, b.build)
	_, err := s.Extract(t.Context(), root)
	extracttest.WantError(t, err, "read missing.txt")
	s, _ = BuildFileSet("fake", []string{"*.txt"}, 0, b.build)
	_, err = s.Extract(t.Context(), root)
	extracttest.WantError(t, err, "fake: refused refused.txt")
}

// The files hold at most max_bytes in all, each read to what the files
// before it left, with the refusal naming both; the values to MaxValues
// and to max_bytes in all. Planted breaks: each file read to max_bytes;
// the bytes left not lowered; the value bounds dropped or one more.
func TestFileSetBounds(t *testing.T) {
	root := repo(t, map[string]string{"a.txt": "1234\n", "b.txt": "5678\n"})
	b := &built{}
	s, _ := BuildFileSet("fake", []string{"*.txt"}, 10, b.build)
	if _, err := s.Extract(t.Context(), root); err != nil {
		t.Errorf("10 bytes under 10: %v", err)
	}
	s, _ = BuildFileSet("fake", []string{"*.txt"}, 9, b.build)
	_, err := s.Extract(t.Context(), root)
	extracttest.WantError(t, err, "fake: read b.txt: the file is 5 bytes, over the limit of 4 bytes; set max_bytes: on the extractor to read a larger file; the files the extractor reads hold at most max_bytes, 9, in all")

	// The values of a.txt and b.txt hold 8 bytes, which a fake reader can
	// make more of than the files hold.
	long := func(src Source, _ bool) (FileReader, error) {
		return doubled{fakeReader{file: src.File}}, nil
	}
	s, _ = BuildFileSet("fake", []string{"*.txt"}, 16, long)
	if _, err := s.Extract(t.Context(), root); err != nil {
		t.Errorf("16 bytes of values under 16: %v", err)
	}
	s, _ = BuildFileSet("fake", []string{"*.txt"}, 15, long)
	_, err = s.Extract(t.Context(), root)
	extracttest.WantError(t, err, "fake: the values of the files hold more than 15 bytes in all, the extractor's max_bytes")

	many := repo(t, map[string]string{"a.txt": strings.Repeat("x\n", MaxValues/2), "b.txt": strings.Repeat("y\n", MaxValues/2)})
	s, _ = BuildFileSet("fake", []string{"*.txt"}, 0, b.build)
	if vs, err := s.Extract(t.Context(), many); err != nil || len(vs) != MaxValues {
		t.Errorf("%d values: %d, %v", MaxValues, len(vs), err)
	}
	writeFile(t, many, "c.txt", "z\n")
	git(t, many, "add", "c.txt")
	_, err = s.Extract(t.Context(), many)
	extracttest.WantError(t, err, fmt.Sprintf("fake: the files hold more than %d values, the limit", MaxValues))
}

// doubled is a reader whose values are each line twice.
type doubled struct{ fakeReader }

func (r doubled) FromShared(data []byte, spent *Spent) ([]check.Value, error) {
	vs, err := r.fakeReader.FromShared(data, spent)
	for i := range vs {
		vs[i].Text += vs[i].Text
	}
	return vs, err
}

// A regex search of a file of a set takes the deadline and the time limit
// of the set's Spent, so the files share one, and a search past the
// deadline says the limit is the files'; a search with no deadline in its
// Spent takes its own time limit. Planted breaks: the deadline not taken
// from Spent; the time limit left at the search's own.
func TestFileSetSharesTheRegexTime(t *testing.T) {
	search, err := CompileSearch("regex", "a.txt", "(a)", 0)
	if err != nil {
		t.Fatal(err)
	}
	gone := Spent{deadline: time.Now().Add(-time.Second), timeLimit: 3 * time.Second}
	_, err = search.FromShared([]byte("a\n"), &gone)
	extracttest.WantError(t, err, "a.txt: matching took more than 3s, the limit of the files the extractor reads, together")
	if vs, err := search.FromShared([]byte("a\n"), &Spent{}); err != nil || len(vs) != 1 {
		t.Errorf("a search of one file: %v, %v", vs, err)
	}

	root := repo(t, map[string]string{"a.txt": "a\n"})
	set, err := BuildFileSet("regex", []string{"*.txt"}, 0, func(src Source, _ bool) (FileReader, error) {
		return CompileSearchAt("regex", src, "(a)")
	})
	if err != nil {
		t.Fatal(err)
	}
	set.timeLimit = -time.Second
	_, err = set.Extract(t.Context(), root)
	extracttest.WantError(t, err, "a.txt: matching took more than -1s, the limit of the files the extractor reads, together")
}

// BuildOneOrSet builds the reader of file when files is nil, as no file of
// a set, refuses both, and builds a set for files. Planted breaks: the
// refusal of both dropped; a file read as a file of a set.
func TestBuildOneOrSet(t *testing.T) {
	b := &built{}
	x, err := BuildOneOrSet("fake", Where{File: "a.txt"}, b.build)
	if r, ok := x.(fakeReader); err != nil || !ok || r.inSet || r.file != "a.txt" {
		t.Errorf("one file: %#v, %v", x, err)
	}
	if x, err := BuildOneOrSet("fake", Where{File: "refused.txt"}, b.build); err == nil || x != nil {
		t.Errorf("a refused file: %#v, %v; want nil and the error", x, err)
	}
	_, err = BuildOneOrSet("fake", Where{File: "a.txt", Files: []string{"*.txt"}}, b.build)
	extracttest.WantError(t, err, "fake: file and files are both set")
	x, err = BuildOneOrSet("fake", Where{Files: []string{"*.txt"}}, b.build)
	if _, ok := x.(FileSet); err != nil || !ok {
		t.Errorf("a set: %#v, %v", x, err)
	}
}

// A search with a set's deadline stops at it, whatever its own time limit.
// Planted break: Matches reading its own limit only, which passed a search
// whose limit was an hour past a deadline already gone.
func TestSearchStopsAtTheSharedDeadline(t *testing.T) {
	search, err := CompileSearch("regex", "a.txt", "(a)", 0)
	if err != nil {
		t.Fatal(err)
	}
	search.TimeLimit, search.shared = time.Hour, time.Now().Add(-time.Second)
	_, err = search.Matches([]byte("a\n"))
	extracttest.WantError(t, err, "a.txt: matching took more than 1h0m0s, the limit of the files the extractor reads, together")
	search.shared = time.Time{}
	if _, err := search.Matches([]byte("a\n")); err != nil {
		t.Errorf("a search of one file: %v", err)
	}
}

// spending is a reader that spends one unit of each budget per file and
// records what the files before it had spent, and refuses a file named
// dense or work as the node limit and the filter work refuse.
type spending struct {
	file string
	saw  *[]Spent
}

func (r spending) Extract(context.Context, string) ([]check.Value, error) {
	return nil, errors.New("not read whole")
}
func (r spending) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return r.Extract(ctx, root)
}
func (r spending) FromShared(_ []byte, spent *Spent) ([]check.Value, error) {
	*r.saw = append(*r.saw, *spent)
	spent.Expanded++
	spent.Filter++
	if strings.HasSuffix(r.file, "dense.txt") {
		return nil, tooDenseError(r.file + ": the file can hold up to 9 nodes, over the limit of 1")
	}
	if strings.HasSuffix(r.file, "work.txt") {
		return nil, fmt.Errorf("%s: path %q: %w", r.file, "x", ErrFilterWork)
	}
	return nil, nil
}

// The files of a set share the alias and filter budgets, each file's
// reader starting at what the files before it spent, and a refusal on the
// node limit names the bytes the earlier files left, on filter work that
// the files share it. Planted breaks: a budget for each file, which every
// reader saw empty; the node refusal passed through bare; a deadline for
// each file.
func TestFileSetSharesTheBudgets(t *testing.T) {
	root := repo(t, map[string]string{"a.txt": "12", "b.txt": "3", "c.txt": ""})
	var saw []Spent
	build := func(src Source, _ bool) (FileReader, error) { return spending{file: src.File, saw: &saw}, nil }
	s, _ := BuildFileSet("fake", []string{"*.txt"}, 100, build)
	if _, err := s.Extract(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	// Every file sees the one deadline the set set, with its time limit.
	deadline := saw[0].deadline
	for i := range saw {
		if saw[i].deadline.IsZero() || !saw[i].deadline.Equal(deadline) || saw[i].timeLimit != s.timeLimit {
			t.Errorf("file %d saw the deadline %v and the time limit %v, want %v and %v", i, saw[i].deadline, saw[i].timeLimit, deadline, s.timeLimit)
		}
		saw[i].deadline, saw[i].timeLimit = time.Time{}, 0
	}
	if want := []Spent{{stringLimit: 100}, {Expanded: 1, Filter: 1, stringLimit: 100}, {Expanded: 2, Filter: 2, stringLimit: 100}}; fmt.Sprint(saw) != fmt.Sprint(want) {
		t.Errorf("each file saw %v spent before it, want %v", saw, want)
	}
	dense := repo(t, map[string]string{"a.txt": "1234", "dense.txt": "x"})
	s, _ = BuildFileSet("fake", []string{"*.txt"}, 100, build)
	_, err := s.Extract(t.Context(), dense)
	extracttest.WantError(t, err, "fake: dense.txt: the file can hold up to 9 nodes, over the limit of 1; in a set, max_bytes is what the files before it left, 96 of 100")
	work := repo(t, map[string]string{"work.txt": "x"})
	_, err = s.Extract(t.Context(), work)
	extracttest.WantError(t, err, "its filters would hash and compare more than 4000000000 bytes, the limit; narrow the path before the filter, or compare fewer or shorter keys and literals; the files of a set share the limit")
}

// A path's filters start at the work spent before them and stop at
// MaxFilterBytes, so a set's later file is refused where it would pass
// alone. Planted break: the meter started at 0.
func TestSharedValuesStartAtTheWorkSpent(t *testing.T) {
	p, err := parsePath("a[?@ == 'x']")
	if err != nil {
		t.Fatal(err)
	}
	root := &Node{Shape: Mapping, Field: map[string]*Node{"a": {Shape: Sequence, Items: []*Node{{Text: "x"}}}}, Keys: []string{"a"}}
	root.Vals = []*Node{root.Field["a"]}
	src := Source{File: "f", Limit: DefaultMaxBytes}
	spent := Spent{}
	if vs, err := src.SharedValues(p, &spent, root); err != nil || len(vs) != 1 || spent.Filter == 0 {
		t.Fatalf("fresh: %v, %v, spent %d", vs, err, spent.Filter)
	}
	spent.Filter = MaxFilterBytes - 1
	_, err = src.SharedValues(p, &spent, root)
	if !errors.Is(err, ErrFilterWork) {
		t.Errorf("near the limit: %v, want the filter work refused", err)
	}
}

// A tracked symlink that a glob matches and that leads outside the
// repository is refused, as file: refuses it. Planted break: the set's
// read through a Source that follows the link, which read the outside file.
func TestFileSetRefusesALinkOutOfTheRepository(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "s\n")
	root := extracttest.Repo(t, map[string]string{"d/a.txt": "1\n"})
	extracttest.Symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "d", "x.txt"))
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	b := &built{}
	s, _ := BuildFileSet("fake", []string{"d/*.txt"}, 0, b.build)
	_, err := s.Extract(t.Context(), root)
	extracttest.WantError(t, err, "read d/x.txt: a symlink leads outside the repository")
}

// A link and its target that a set both names are one file, read once at
// its first name, so a rule that each value occurs once, as exists_one
// tests, holds; a link to a file outside the set still reads it. Planted
// break: the names compared, not where they resolve, which read the
// target twice.
func TestFileSetReadsALinkAndItsTargetOnce(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"d/rules.txt": "100\n", "e/other.txt": "200\n"})
	extracttest.Symlink(t, "rules.txt", filepath.Join(root, "d", "again.txt"))
	extracttest.Symlink(t, "../e/other.txt", filepath.Join(root, "d", "out.txt"))
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	b := &built{}
	s, _ := BuildFileSet("fake", []string{"d/*.txt"}, 0, b.build)
	vs, err := s.Extract(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, v.Text+"@"+v.At.File)
	}
	if want := []string{"100@d/again.txt", "200@d/out.txt"}; !slices.Equal(got, want) {
		t.Errorf("values %q, want %q", got, want)
	}
}
