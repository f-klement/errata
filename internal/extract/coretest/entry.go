package coretest

import (
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
	lines := core.LastLine(data)
	want, total, rerr := wanted(t, e, data, lines)
	if total > bound {
		t.Fatalf("the entries hold %d bytes, more than the file's %d", total, bound)
	}
	if msg := verdict(got, want, err, rerr); msg != "" {
		t.Fatal(msg)
	}
	for i, v := range got {
		if v.Text != want[i] || v.At.Line < 1 || v.At.Line > lines || v.At.File != e.File {
			t.Fatalf("value %q at %s:%d of %d lines, want %q", v.Text, v.At.File, v.At.Line, lines, want[i])
		}
	}
}

// verdict is what is wrong with FromData's values and error against the
// values the query wants and the reader's error, "" for nothing.
func verdict[V any](got []V, want []string, err, rerr error) string {
	if err != nil {
		return refusedVerdict(got, want, err, rerr)
	}
	if rerr != nil {
		return fmt.Sprintf("values %v, and the reader refuses the file: %v", got, rerr)
	}
	if len(got) != len(want) {
		return fmt.Sprintf("%d values, the query wants %d", len(got), len(want))
	}
	return ""
}

// refusedVerdict is verdict for a FromData that refused the file: it
// returns no values, and refuses only what the reader refuses or what
// passes the bound on values.
func refusedVerdict[V any](got []V, want []string, err, rerr error) string {
	if got != nil {
		return fmt.Sprintf("values %v with error %v", got, err)
	}
	if rerr == nil && len(want) <= core.MaxValues {
		return fmt.Sprintf("refused %v, and the reader reads the file", err)
	}
	return ""
}

// wanted reads data with e's reader and no meter, and returns the values
// the query's Wants selects, a reset dropping those before it, the bytes
// of every entry's value, and the reader's error. Each entry must lie on
// a line of the file's lines.
func wanted(t *testing.T, e core.EntryExtractor, data []byte, lines int) (want []string, total int, err error) {
	t.Helper()
	err = e.Read(data, nil, func(en *core.Entry) error {
		if en.Line < 1 || en.Line > lines {
			t.Fatalf("an entry at line %d of %d", en.Line, lines)
		}
		if !en.NoValue {
			total += len(en.Value)
		}
		switch {
		case !e.Query.Wants(en):
		case en.Kind == core.EntryReset:
			want = want[:0]
		default:
			want = append(want, string(en.Value))
		}
		return nil
	})
	return want, total, err
}
