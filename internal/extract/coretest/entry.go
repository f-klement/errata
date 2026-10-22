package coretest

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// read reads data with x through its matcher under m, and returns the
// values the matcher wants as value@line.
func read(x core.EntryExtractor, data []byte, m *core.Meter) ([]string, error) {
	var got []string
	match := core.NewMatcher(&x.Query, m)
	err := x.Read(data, m, func(en *core.Entry) error {
		if match.Wants(en) {
			got = append(got, fmt.Sprintf("%s@%d", en.Value, en.Line))
		}
		return match.Err
	})
	return got, err
}

// Spent is the work reading data with x and its matcher costs.
func Spent(t *testing.T, x core.EntryExtractor, data []byte) int64 {
	t.Helper()
	m := &core.Meter{Budget: 1 << 40}
	if _, err := read(x, data, m); err != nil {
		t.Fatalf("%s: %v", x.File, err)
	}
	return m.Spent
}

// RefusesAtEveryBudget fails unless x, reading body, is refused for work at
// every budget below what the read costs, and at its cost reads what it
// reads with no meter.
func RefusesAtEveryBudget(t *testing.T, x core.EntryExtractor, body string) {
	t.Helper()
	data := []byte(body)
	want, err := read(x, data, nil)
	if err != nil {
		t.Fatalf("%s: %v", x.File, err)
	}
	cost := Spent(t, x, data)
	for budget := range cost {
		if _, err := read(x, data, &core.Meter{Budget: budget}); !errors.Is(err, core.ErrWork) {
			t.Fatalf("%s: a budget of %d of %d units: %v, want the work refusal", x.File, budget, cost, err)
		}
	}
	if got, err := read(x, data, &core.Meter{Budget: cost}); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("%s: at its cost: %q, %v; want %q", x.File, got, err, want)
	}
}

// LeavesRoom fails unless x reads body, an ordinary file, for under a
// quarter of core.WorkPerByte a byte, without the floor.
func LeavesRoom(t *testing.T, x core.EntryExtractor, body string) {
	t.Helper()
	data := []byte(body)
	m := core.NewMeter(len(data) + len(x.Query.Section) + len(x.Query.Host) + len(x.Query.Key))
	match := core.NewMatcher(&x.Query, m)
	if err := x.Read(data, m, func(en *core.Entry) error { match.Wants(en); return match.Err }); err != nil {
		t.Fatalf("%q: %v", core.Clip(body), err)
	}
	if limit := core.WorkPerByte / 4 * int64(len(data)); m.Spent > limit {
		t.Errorf("%q...: %d units for %d bytes, over %d", core.Clip(body), m.Spent, len(data), limit)
	}
}

// CheckEntryValues runs e on data and checks what every extractor promises:
// values at lines inside the file, no more than core.MaxValues of them, an
// error and no values otherwise, and the values the query wants from the
// entries the reader visits, which hold no more than bound bytes, git's
// true aside: the file's size, or more where a reader decodes a byte into
// more, as Java does a byte that is not UTF-8 into U+FFFD. The query's Wants
// is the reference for FromData's matcher, which compares a section once
// for each header.
func CheckEntryValues(t *testing.T, e core.EntryExtractor, data []byte, bound int) {
	t.Helper()
	got, err := e.FromData(data)
	var want []string
	total := 0
	lines := bytes.Count(data, []byte("\n")) + 1
	rerr := e.Read(data, nil, func(en *core.Entry) error {
		if en.Line < 1 || en.Line > lines {
			t.Fatalf("an entry at line %d of %d", en.Line, lines)
		}
		if !en.NoValue {
			total += len(en.Value)
		}
		if e.Query.Wants(en) {
			want = append(want, string(en.Value))
		}
		return nil
	})
	if total > bound {
		t.Fatalf("the entries hold %d bytes, more than the file's %d", total, bound)
	}
	switch {
	case err != nil && got != nil:
		t.Fatalf("values %v with error %v", got, err)
	case err != nil && rerr == nil && len(want) <= core.MaxValues:
		t.Fatalf("refused %v, and the reader reads the file", err)
	case err == nil && rerr != nil:
		t.Fatalf("values %v, and the reader refuses the file: %v", got, rerr)
	case err == nil && len(got) != len(want):
		t.Fatalf("%d values, the query wants %d", len(got), len(want))
	}
	for i, v := range got {
		if v.Text != want[i] || v.At.Line < 1 || v.At.Line > lines || v.At.File != e.File {
			t.Fatalf("value %q at %s:%d of %d lines, want %q", v.Text, v.At.File, v.At.Line, lines, want[i])
		}
	}
}
