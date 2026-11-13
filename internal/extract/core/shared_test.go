package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// rewrite puts body in the file name under root, so a test sees whether
// the next read reads the disk again or gets what the run kept.
func rewrite(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// In a run a file is read once: a later read of it, by any extractor, gets
// the bytes the first read got, and a read outside a run, or in the next
// run, reads the disk. Planted break: Reader.Read reading through its tree
// whether or not the run shares (shared ignored).
func TestARunReadsAFileOnce(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "one\n"})
	run := check.WithRun(t.Context())
	src := Source{File: "f", Limit: 100}
	if data, err := src.Read(run, root); err != nil || string(data) != "one\n" {
		t.Fatalf("first read %q, %v", data, err)
	}
	rewrite(t, root, "f", "two\n")
	if data, err := src.Read(run, root); err != nil || string(data) != "one\n" {
		t.Errorf("second read in the run %q, %v; want the bytes the run read first", data, err)
	}
	r, err := OpenReader(run, root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if data, err := r.Read(src); err != nil || string(data) != "one\n" {
		t.Errorf("a reader of the run read %q, %v; want the bytes the run read first", data, err)
	}
	if data, err := src.Read(t.Context(), root); err != nil || string(data) != "two\n" {
		t.Errorf("read outside a run %q, %v; want the disk's", data, err)
	}
	if data, err := src.Read(check.WithRun(t.Context()), root); err != nil || string(data) != "two\n" {
		t.Errorf("read in the next run %q, %v; want the disk's", data, err)
	}
}

// Each read holds the file the run kept to its own limit, and a file over
// one read's limit is refused with the words a read of its own would give,
// so a rule with a smaller max_bytes sees what it saw before files were
// shared. A refusal on size is not kept: a read with a higher limit reads
// the file. Planted breaks: within dropping its size check; keepRefusal
// keeping a refusal on size; the size check in within
// refusing a file of exactly the limit.
func TestEachReadHoldsTheFileToItsOwnLimit(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "0123456789"})
	small, large := Source{File: "f", Limit: 5}, Source{File: "f", Limit: 100}
	_, alone := small.Read(t.Context(), root)
	if !errors.Is(alone, ErrTooLarge) {
		t.Fatalf("a read of its own over the limit: %v", alone)
	}

	run := check.WithRun(t.Context())
	if data, err := large.Read(run, root); err != nil || string(data) != "0123456789" {
		t.Fatalf("read at 100 bytes %q, %v", data, err)
	}
	_, err := small.Read(run, root)
	if !errors.Is(err, ErrTooLarge) || err.Error() != alone.Error() {
		t.Errorf("read at 5 bytes of a file the run kept: %v; want %v", err, alone)
	}
	if data, err := (Source{File: "f", Limit: 10}).Read(run, root); err != nil || string(data) != "0123456789" {
		t.Errorf("read at exactly the file's 10 bytes %q, %v; want the file", data, err)
	}

	run = check.WithRun(t.Context())
	if _, err := small.Read(run, root); err == nil || err.Error() != alone.Error() {
		t.Errorf("first read at 5 bytes: %v; want %v", err, alone)
	}
	if data, err := large.Read(run, root); err != nil || string(data) != "0123456789" {
		t.Errorf("read at 100 bytes after a refusal at 5: %q, %v; want the file", data, err)
	}
}

// A read the run refused for any reason but size is refused the same way
// for every later read of the run, so two rules that name one file fail on
// it alike, even if the disk changed between them. Planted break:
// keepRefusal keeping nothing.
func TestARunKeepsARefusal(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"dir/f": "x"})
	run := check.WithRun(t.Context())
	for _, name := range []string{"missing", "dir"} {
		src := Source{File: name, Limit: 100}
		_, first := src.Read(run, root)
		if first == nil {
			t.Fatalf("%s: read, want a refusal", name)
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		rewrite(t, root, name, "now a file")
		if _, err := src.Read(run, root); err == nil || err.Error() != first.Error() {
			t.Errorf("%s: second read in the run: %v; want %v", name, err, first)
		}
		if data, err := src.Read(t.Context(), root); err != nil || string(data) != "now a file" {
			t.Errorf("%s: read outside the run %q, %v; want the file now there", name, data, err)
		}
	}
}

// Each read gets bytes of its own: an extractor that writes to the bytes it
// was handed changes nothing another reads. Planted break: within handing
// out the kept bytes themselves.
func TestEachReadGetsItsOwnBytes(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "abc"})
	run := check.WithRun(t.Context())
	src := Source{File: "f", Limit: 100}
	for range 3 {
		data, err := src.Read(run, root)
		if err != nil || string(data) != "abc" {
			t.Fatalf("read %q, %v; want abc", data, err)
		}
		data[0] = 'X'
	}
}

// A run keeps the files it reads while they hold at most its limit
// together, and reads each file past that again for every extractor, so
// the bytes a run holds stay bounded on a large tree. Planted break: the
// limit check in count dropped.
func TestARunKeepsAtMostItsLimit(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a": "aaaaa", "b": "bbbbb", "c": "ccc"})
	tr, err := openTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	r := &Reader{t: tr, shared: newShared(8)}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := r.Read(Source{File: name, Limit: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if r.shared.bytes != 8 {
		t.Errorf("the run keeps %d bytes; want 8, a and c", r.shared.bytes)
	}
	for _, name := range []string{"a", "b", "c"} {
		rewrite(t, root, name, "new")
	}
	for name, want := range map[string]string{"a": "aaaaa", "b": "new", "c": "ccc"} {
		if data, err := r.Read(Source{File: name, Limit: 100}); err != nil || string(data) != want {
			t.Errorf("%s: read %q, %v; want %q", name, data, err, want)
		}
	}
}

// A witness's run shares nothing: its fixture is opened again for each
// check, and a read of it goes through that tree. Nor does a read outside
// a run. Planted breaks: readerOf sharing on a held tree; sharedAt
// building a store outside a run.
func TestAWitnessRunSharesNothing(t *testing.T) {
	run := check.WithRun(t.Context())
	if r := readerOf(run, "root", tree{held: true}); r.shared != nil {
		t.Error("a reader of a witness's fixture shares what it reads")
	}
	if r := readerOf(run, "root", tree{}); r.shared == nil {
		t.Error("a reader of the repository shares nothing")
	}
	if r := readerOf(t.Context(), "root", tree{}); r.shared != nil {
		t.Error("a reader outside a run shares what it reads")
	}
}

// newShared is an empty store of a run at its first generation that keeps
// at most limit bytes.
func newShared(limit int64) *sharedFiles {
	return &sharedFiles{kept: map[string]*entry{}, found: map[string]resolution{}, limit: limit}
}

// readerIn is a reader of root through f, closed when the test ends.
func readerIn(t *testing.T, root string, f *sharedFiles) *Reader {
	t.Helper()
	tr, err := openTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.close)
	return &Reader{t: tr, shared: f}
}

// Once a check of the run may have written to the repository
// (check.Wrote), the run reads its files again: a rule after a program
// that rewrote a file sees what the program wrote, as it did before the run
// kept anything. Names are looked up again too. Planted break: sharedAt
// not emptying the store at a later generation.
func TestARunReadsAgainAfterAWrite(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "old\n", "a": "a", "b": "b"})
	link := filepath.Join(root, "link")
	extracttest.Symlink(t, "a", link)
	run := check.WithRun(t.Context())
	src := Source{File: "f", Limit: 100}
	resolve := func() string {
		t.Helper()
		r, err := OpenReader(run, root)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		at, _ := r.Resolved(Source{File: "link"})
		return at
	}
	if data, err := src.Read(run, root); err != nil || string(data) != "old\n" || resolve() != "a" {
		t.Fatalf("first read %q, %v", data, err)
	}
	rewrite(t, root, "f", "new\n")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, "b", link)
	check.Wrote(run)
	if data, err := src.Read(run, root); err != nil || string(data) != "new\n" {
		t.Errorf("read after a write %q, %v; want new", data, err)
	}
	if at := resolve(); at != "b" {
		t.Errorf("link resolves to %s after a write, want b", at)
	}
}

// A read that finds another read of the same file under way waits for it
// and gets its bytes, and reads nothing itself; when the read under way
// kept nothing, as a refusal on size, the waiting read reads the file. The
// disk holds other bytes than the read under way, so a second read would
// show. Planted break: read filling a new entry whatever it found.
func TestAReadWaitsForTheReadUnderWay(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "disk"})
	f := newShared(100)
	r := readerIn(t, root, f)
	for _, none := range []bool{false, true} {
		e := &entry{done: make(chan struct{})}
		f.kept["f"] = e
		got := make(chan string, 1)
		go func() {
			data, err := r.Read(Source{File: "f", Limit: 100})
			got <- fmt.Sprint(string(data), err)
		}()
		select {
		case g := <-got:
			t.Fatalf("none %v: the read returned %q before the read under way ended", none, g)
		case <-time.After(20 * time.Millisecond):
		}
		e.k, e.none = keptOf([]byte("first"), 5), none
		close(e.done)
		want := "first<nil>"
		if none {
			want = "disk<nil>"
		}
		if g := <-got; g != want {
			t.Errorf("none %v: the waiting read got %q, want %q", none, g, want)
		}
	}
}

// A file that grew while it was read is kept at the bytes read, so a later
// read whose limit lies between its size when opened and the bytes read is
// refused as its own open would refuse the file now. Planted break:
// keptOf keeping the size the open saw.
func TestAGrownFileIsKeptAtItsBytes(t *testing.T) {
	k := keptOf([]byte("0123456789"), 5)
	_, err := k.within(Source{File: "f", Limit: 7})
	if err == nil || err.Error() != tooLarge("f", 10, 7).Error() {
		t.Errorf("read at 7 of a file read at 10 bytes: %v; want %v", err, tooLarge("f", 10, 7))
	}
}

// An empty file reads as empty bytes, not nil, in a run as outside one, as
// io.ReadAll gives it. Planted break: within copying with append to nil.
func TestAnEmptyFileReadsAsEmpty(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"empty": ""})
	run := check.WithRun(t.Context())
	for range 2 {
		if data, err := (Source{File: "empty", Limit: 100}).Read(run, root); err != nil || data == nil {
			t.Errorf("read %v, %v; want empty bytes", data, err)
		}
	}
}

// A file the run kept stays kept when the disk changes under it: a later
// read in the run that would be refused gets the bytes, and the count of
// kept bytes stays that of the file. Planted break: fill replacing a kept
// entry.
func TestAKeptFileIsNotReplacedByARefusal(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "abc"})
	f := newShared(100)
	r := readerIn(t, root, f)
	for range 2 {
		if data, err := r.Read(Source{File: "f", Limit: 100}); err != nil || string(data) != "abc" {
			t.Fatalf("read %q, %v; want abc", data, err)
		}
		if err := os.Remove(filepath.Join(root, "f")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if f.bytes != 3 {
		t.Errorf("the run counts %d bytes, want 3", f.bytes)
	}
}

// In a run a name resolves once, as a file is read once: a reader of the
// run gets where the name led the first time. Planted break:
// Reader.Resolved looking the name up through its tree whether or not the
// run shares.
func TestARunResolvesANameOnce(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a": "a", "b": "b"})
	link := filepath.Join(root, "link")
	extracttest.Symlink(t, "a", link)
	run := check.WithRun(t.Context())
	resolve := func() string {
		t.Helper()
		r, err := OpenReader(run, root)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		at, err := r.Resolved(Source{File: "link"})
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	if at := resolve(); at != "a" {
		t.Fatalf("link resolves to %s, want a", at)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, "b", link)
	if at := resolve(); at != "a" {
		t.Errorf("link resolves to %s in the run, want a, where it led first", at)
	}
}

// Two file sets of a run that name the same files read them once: the
// second set's values are those of the bytes the first read, so every
// extractor that names a file is handed the bytes the run read. Planted
// break as TestARunReadsAFileOnce.
func TestTwoSetsOfARunShareTheirReads(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	var b built
	set, err := BuildFileSet("fake", []string{"a.txt", "b.txt"}, 0, b.build)
	if err != nil {
		t.Fatal(err)
	}
	run := check.WithRun(t.Context())
	first, err := set.Extract(run, root)
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, root, "a.txt", "changed\n")
	second, err := set.Extract(run, root)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Errorf("the second set read %v, the first %v; want the same", second, first)
	}
}

// The readers of a run may read at once, as the checks of a run will: each
// gets its file's bytes held to its own limit. The race detector watches
// the map and the count the run keeps. Planted break: the lock in read
// dropped, which the race detector reports.
func TestReadersOfARunReadAtOnce(t *testing.T) {
	files := map[string]string{}
	for i := range 16 {
		files[fmt.Sprintf("f%d", i)] = fmt.Sprintf("file %d\n", i)
	}
	root := extracttest.Repo(t, files)
	run := check.WithRun(t.Context())
	var wg sync.WaitGroup
	errs := make(chan error, 8*len(files))
	for g := range 8 {
		wg.Go(func() {
			r, err := OpenReader(run, root)
			if err != nil {
				errs <- err
				return
			}
			defer r.Close()
			for name, body := range files {
				limit := int64(100)
				if g%2 == 1 {
					limit = 3
				}
				data, err := r.Read(Source{File: name, Limit: limit})
				switch {
				case limit == 3 && !errors.Is(err, ErrTooLarge):
					errs <- fmt.Errorf("%s at 3 bytes: %q, %w; want a refusal on size", name, data, err)
				case limit == 100 && (err != nil || string(data) != body):
					errs <- fmt.Errorf("%s: %q, %w; want %q", name, data, err, body)
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Two spellings of one root share one store, so a run keeps at most
// MaxShared of one tree whichever way its readers name the root. Planted
// break: sharedAt keying the store by the root as passed.
func TestOneRootSharesOneStore(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	run := check.WithRun(t.Context())
	if sharedAt(run, ".") != sharedAt(run, root) {
		t.Error(". and the absolute root keep two stores")
	}
}

// The store empties only for a later generation: a reader that took an
// earlier generation before the run moved on keeps what the later one
// kept (readonce verify, N1). Planted break: at emptying on any other
// generation, gen != f.gen.
func TestAnEarlierGenerationEmptiesNothing(t *testing.T) {
	f := newShared(100)
	f.gen = 2
	f.kept["f"] = &entry{done: make(chan struct{})}
	f.found["f"] = resolution{rel: "f"}
	f.at(1)
	if len(f.kept) != 1 || len(f.found) != 1 || f.gen != 2 {
		t.Errorf("at an earlier generation: %d kept, %d found, generation %d; want 1, 1 and 2", len(f.kept), len(f.found), f.gen)
	}
	f.at(3)
	if len(f.kept) != 0 || len(f.found) != 0 || f.gen != 3 {
		t.Errorf("at a later generation: %d kept, %d found, generation %d; want 0, 0 and 3", len(f.kept), len(f.found), f.gen)
	}
}

// Lookups of one name at once, as rules on the engine's pool make them,
// all get the lookup the store kept, the first to finish, though a link
// changes target while they look (readonce verify, F8). Under -race the
// test also shows the lookups share the store safely. Planted break:
// resolved keeping its own lookup over the first, where lookups that
// finished later returned another target. That plant fails only when a
// swap lands between two lookups, which it did in round 13 of 20 on a four
// core runner and may not on one core; a deterministic witness would need
// a seam inside the lookup to hold it, which the four lines it guards do
// not earn. The test itself passes whatever the timing.
func TestLookupsAtOnceGetTheOneKept(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a": "a", "b": "b"})
	link := filepath.Join(root, "link")
	extracttest.Symlink(t, "a", link)
	for round := range 20 {
		f := newShared(100)
		r := readerIn(t, root, f)
		stop := make(chan struct{})
		var swaps sync.WaitGroup
		swaps.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				tmp := filepath.Join(root, "link.tmp")
				_ = os.Remove(tmp)
				_ = os.Symlink([]string{"a", "b"}[i%2], tmp)
				_ = os.Rename(tmp, link)
			}
		})
		got := make([]string, 16)
		var lookups sync.WaitGroup
		for i := range got {
			lookups.Go(func() {
				at, err := r.Resolved(Source{File: "link"})
				got[i] = fmt.Sprint(at, err)
			})
		}
		lookups.Wait()
		close(stop)
		swaps.Wait()
		kept := f.found["link"]
		want := fmt.Sprint(kept.rel, kept.err)
		for i, g := range got {
			if g != want {
				t.Fatalf("round %d: lookup %d got %s, the store kept %s", round, i, g, want)
			}
		}
	}
}
