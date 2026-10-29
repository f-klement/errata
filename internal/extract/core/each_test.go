package core

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

func mustEach(t *testing.T, file string, maxBytes int64, before, after string, values ...string) Each {
	t.Helper()
	e := NewEach("regex", before, after, maxBytes)
	add(t, &e, values...)
	e, err := e.At("regex", file, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func add(t *testing.T, e *Each, values ...string) {
	t.Helper()
	for _, v := range values {
		if err := e.Add(func() (string, error) { return v, nil }); err != nil {
			t.Fatal(err)
		}
	}
}

// The values of every pattern come in the order their matches start, two at
// one place in the order of the values, each at its line, with CRLF read
// as LF, and a match in which group 1 took no part gives none. Each value
// stands in a group of its own, so the repeat after it repeats it whole.
// Planted breaks: the values of each pattern in turn, unsorted; a sort
// that is not stable; group 1 not checked for taking part; no group
// around the value.
func TestEachOrdersByWhereMatchesStart(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "b2\r\na1\nb3 ab\nc\nxyxy\n"})
	vs, err := mustEach(t, "f", 0, "(", ")", `a\d`, `b\d`, `a(?:b)?`, `a.`, `(?:x)?c`).Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := extracttest.Texts(vs); got != "b2@1 a1@2 a@2 a1@2 b3@3 ab@3 ab@3 c@4" {
		t.Errorf("got %q", got)
	}
	vs, err = mustEach(t, "f", 0, "^(", "+)$", `xy`, `(x)?c`).Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := extracttest.Texts(vs); got != "c@4 xyxy@5" {
		t.Errorf("got %q", got)
	}
	vs, err = mustEach(t, "f", 0, "", "", `(x)?c`).Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("group 1 took no part, got %q", extracttest.Texts(vs))
	}
	if vs, _ = mustEach(t, "f", 0, "(", ")", `a`).Extract(root); vs[0].At.File != "f" {
		t.Errorf("value at %v, want f", vs[0].At)
	}
}

// A file that is not there, or past its limit, is refused as a search's.
func TestEachReadsAsASearch(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "aaaa"})
	_, err := mustEach(t, "missing", 0, "(", ")", `a`).Extract(root)
	extracttest.WantError(t, err, "missing")
	_, err = mustEach(t, "f", 2, "(", ")", `a`).Extract(root)
	extracttest.WantError(t, err, "over the limit")
	_, err = (Each{}).At("regex", "../f", 0)
	extracttest.WantError(t, err, "regex: file \"../f\" is outside the repository")
}

// Two patterns can match the same text, so the values of all of them are
// held to the file's limit in bytes. Planted break: no sum of the bytes.
func TestEachHoldsValuesToMaxBytes(t *testing.T) {
	e := mustEach(t, "f", 10, "(", ")", `.+`, `.+`)
	if _, err := e.FromData([]byte("12345")); err != nil {
		t.Fatalf("two values of 5 bytes under a limit of 10: %v", err)
	}
	_, err := e.FromData([]byte("123456"))
	extracttest.WantError(t, err, "f: the values of every pattern hold more than 10 bytes in all, the extractor's max_bytes")
}

// The matches of every pattern together are held to MaxValues. Planted
// break: the count of each pattern's matches alone, which Search holds.
func TestEachHoldsMatchesToMaxValues(t *testing.T) {
	half := MaxValues/2 + 1
	e := mustEach(t, "f", 0, "(", ")", `a`, `a`)
	_, err := e.FromData([]byte(strings.Repeat("a", half)))
	extracttest.WantError(t, err, "selects more than 100000 values")
	if vs, err := e.FromData([]byte(strings.Repeat("a", half-1))); err != nil || len(vs) != MaxValues {
		t.Fatalf("%d values, %v; want %d", len(vs), err, MaxValues)
	}
}

// Every search shares one time limit, from when the Each was made: the
// first search past it is refused, though the limit would give a search of
// its own an hour. Planted break: each search with a limit of its own from
// when it starts.
func TestEachSharesOneTimeLimit(t *testing.T) {
	e := newEach("regex", "(", ")", time.Hour)
	add(t, &e, `a`, `b`)
	e, err := e.At("regex", "f", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.FromData([]byte("ab")); err != nil {
		t.Fatalf("within the limit: %v", err)
	}
	e.deadline = time.Now().Add(-time.Second)
	_, err = e.FromData([]byte("ab"))
	extracttest.WantError(t, err, "f: matching took more than 1h0m0s, "+eachLimit+", and stopped after 0 matches")
}

// A pattern that does not compile is refused with its value's place in the
// list, when its search is due.
func TestEachNamesThePattern(t *testing.T) {
	_, err := mustEach(t, "f", 0, "(", ")", `a`, `b(`).FromData([]byte("a"))
	var bad *PatternError
	if !errors.As(err, &bad) || bad.Index != 1 {
		t.Fatalf("err %v, want a *PatternError at 1", err)
	}
	extracttest.WantError(t, err, "regex: pattern \"((?:b())\": error parsing regexp: missing closing )")
	if !errors.Is(err, bad.Err) {
		t.Errorf("the refusal does not unwrap to %v", bad.Err)
	}
}

// FromShared is FromData: an Each spends no budget of a set.
func TestEachFromShared(t *testing.T) {
	spent := Spent{}
	vs, err := mustEach(t, "f", 0, "(", ")", `a`).FromShared([]byte("a"), &spent)
	if err != nil || extracttest.Texts(vs) != "a@1" || spent != (Spent{}) {
		t.Fatalf("got %v, %v, spent %+v", vs, err, spent)
	}
}

// Preparing the values counts against the time limit: Add refuses a value
// once the limit has passed, without preparing it, and returns a value's
// own refusal as it is. Planted break: no look at the clock in Add.
func TestEachAddSharesTheTimeLimit(t *testing.T) {
	e := newEach("regex", "(", ")", -time.Second)
	called := false
	err := e.Add(func() (string, error) { called = true; return "a", nil })
	extracttest.WantError(t, err, "regex: preparing a pattern for each value took more than -1s, "+eachLimit+", and stopped after 0 values")
	if called {
		t.Error("Add prepared a value past the limit")
	}
	e = newEach("regex", "(", ")", time.Hour)
	refused := errors.New("the value is no RE2 pattern")
	if err := e.Add(func() (string, error) { return "", refused }); !errors.Is(err, refused) || len(e.values) != 0 {
		t.Fatalf("Add: %v, values %q; want the value's refusal and no value", err, e.values)
	}
}

// The review's shape, scaled down: values that each cost a compile to
// check, more of them than the limit has time for. Add stops at the limit,
// one value past it at most, where without the clock all of them were
// checked first: 20000 took 23 s against a limit of 10 s. Planted break:
// no look at the clock in Add.
func TestEachAddStopsCostlyValuesAtTheLimit(t *testing.T) {
	const heavy = `(?:\pL{1000}|\pN{1000}|\pS{1000}|\pP{1000})`
	limit := 100 * time.Millisecond
	e := newEach("regex", "(", ")", limit)
	start := time.Now()
	var err error
	for i := 0; i < 5000 && err == nil; i++ {
		err = e.Add(func() (string, error) {
			regexp.MustCompile(heavy)
			return heavy, nil
		})
	}
	extracttest.WantError(t, err, "preparing a pattern for each value took more than 100ms")
	if took := time.Since(start); took > 20*limit {
		t.Errorf("Add took %s to stop, against a limit of %s", took, limit)
	}
}
