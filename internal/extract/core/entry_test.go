package core

import (
	"bytes"
	"errors"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The matcher compares a section once for each header: an entry with the
// header's number keeps the answer of the entry before, whatever its bytes,
// so a long name costs its length once. The planted break is comparing for
// each entry, which reads the second entry's section.
func TestMatcherComparesEachSectionOnce(t *testing.T) {
	equal := func(host, want []byte, _ *Meter) (bool, error) { return bytes.Equal(host, want), nil }
	q := Query{Section: []byte("a"), Key: []byte("k"), HasKey: true, Host: []byte("h"), HasHost: true, MatchHost: equal}
	m := NewMatcher(&q, nil)
	first := Entry{Section: []byte("a"), SectionSeq: 1, Host: []byte("h"), HostSeq: 1, Key: []byte("k")}
	if !m.Wants(&first) {
		t.Fatal("the first entry is not wanted")
	}
	same := Entry{Section: []byte("b"), SectionSeq: 1, Host: []byte("x"), HostSeq: 1, Key: []byte("k")}
	if !m.Wants(&same) {
		t.Error("an entry of the same header and host line was compared again")
	}
	next := Entry{Section: []byte("b"), SectionSeq: 2, Host: []byte("h"), HostSeq: 2, Key: []byte("k")}
	if m.Wants(&next) {
		t.Error("an entry of the next header was not compared")
	}
}

// A charge within the budget passes, and the first past it is refused for
// work. A nil meter has no budget. The planted break is refusing at the
// budget itself, which TestWorkMeterRefusesAtEveryBudget also finds.
func TestMeterRefusesPastItsBudget(t *testing.T) {
	m := &Meter{Budget: 10}
	if err := m.Charge(10); err != nil {
		t.Fatalf("a charge of the whole budget: %v", err)
	}
	if err := m.Charge(1); !errors.Is(err, ErrWork) {
		t.Fatalf("a charge past the budget: %v, want the work refusal", err)
	}
	if m.Spent != 11 {
		t.Errorf("spent %d, want 11", m.Spent)
	}
	var none *Meter
	if err := none.Charge(1 << 40); err != nil {
		t.Errorf("a nil meter refuses: %v", err)
	}
	if got, want := NewMeter(3).Budget, int64(workFloor+3*WorkPerByte); got != want {
		t.Errorf("the budget of 3 bytes is %d, want %d", got, want)
	}
}

// Decide asks decide once for a run of the same text, charging its cost the
// first time and the text's length after, and asks again for another text.
// The planted breaks are asking each time and charging the cost each time.
func TestMeterDecidesATextOnce(t *testing.T) {
	asked := 0
	decide := func(s string) bool { asked++; return s == "a" }
	m := &Meter{Budget: 1000}
	for i, c := range []struct {
		text         string
		want         bool
		asked, spent int
	}{
		{"a", true, 1, 100},
		{"a", true, 1, 101},
		{"b", false, 2, 201},
		{"a", true, 3, 301},
	} {
		got, err := m.Decide([]byte(c.text), 100, decide)
		if err != nil || got != c.want || asked != c.asked || m.Spent != int64(c.spent) {
			t.Errorf("%d %q: %v, %v, asked %d, spent %d; want %v, asked %d, spent %d", i, c.text, got, err, asked, m.Spent, c.want, c.asked, c.spent)
		}
	}
	if _, err := (&Meter{Budget: 5}).Decide([]byte("a"), 10, decide); !errors.Is(err, ErrWork) || asked != 3 {
		t.Errorf("past the budget: %v, asked %d; want the work refusal before decide is asked", err, asked)
	}
	var none *Meter
	for range 2 {
		if got, err := none.Decide([]byte("a"), 10, decide); !got || err != nil {
			t.Errorf("a nil meter: %v, %v", got, err)
		}
	}
	if asked != 5 {
		t.Errorf("a nil meter keeps an answer: asked %d times, want 5", asked)
	}
}

// lineReader is a reader of one entry for each line of data, key k, its
// value the line, charging cost for each.
func lineReader(cost int) ReadFunc {
	return func(data []byte, m *Meter, visit func(*Entry) error) error {
		for i, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
			if err := m.Charge(cost); err != nil {
				return err
			}
			if bytes.HasPrefix(line, []byte("!")) {
				return LineError(i+1, "a line starts with !")
			}
			e := Entry{Kind: EntryKey, Key: []byte("k"), Value: line, Line: i + 1, SectionSeq: 1, HostSeq: i}
			if err := visit(&e); err != nil {
				return err
			}
		}
		return nil
	}
}

// The extractor returns the values its query wants, marked by its test of
// what is not literal, and names the file in each refusal: the reader's, a
// read past the work budget, and the host match's, at its line. The planted
// breaks are a value of a key the query does not want, a mark on a literal
// value, and a refusal without the file or the line.
func TestEntryExtractorReturnsTheWantedValues(t *testing.T) {
	x := EntryExtractor{
		Source:     Source{File: "f", Limit: DefaultMaxBytes},
		Query:      Query{Key: []byte("k"), HasKey: true},
		Read:       lineReader(LineWork),
		NotLiteral: func(e *Entry) bool { return bytes.HasPrefix(e.Value, []byte("$")) },
	}
	got, err := x.FromData([]byte("a\n$b\n"))
	if err != nil || len(got) != 2 || got[0].Text != "a" || got[0].At.Line != 1 || got[0].NotLiteral || !got[1].NotLiteral || got[1].At.File != "f" {
		t.Errorf("got %+v, %v", got, err)
	}
	other := x
	other.Query = Query{Key: []byte("j"), HasKey: true}
	if got, err := other.FromData([]byte("a\n")); err != nil || len(got) != 0 {
		t.Errorf("another key: %+v, %v", got, err)
	}
	_, err = x.FromData([]byte("a\n!b\n"))
	extracttest.WantError(t, err, "f: line 2: a line starts with !")
	costly := x
	costly.Read = lineReader(1 << 30)
	_, err = costly.FromData([]byte("a\n"))
	extracttest.WantError(t, err, "f: reading the file and matching the spec take more than 128 units of work for each byte")
	refused := x
	refused.Query.HasHost = true
	refused.Query.MatchHost = func(_, _ []byte, _ *Meter) (bool, error) {
		return false, errors.New("the host is refused")
	}
	_, err = refused.FromData([]byte("a\n"))
	extracttest.WantError(t, err, "f: line 1: the host is refused")
}

// A template's values are not literal where its engine renders them: Jinja's
// {{, {% and {# in any template, <% in ERB, ${ and %{ in Terraform's; and the
// dialect's own test still holds. Another file keeps the dialect's test,
// which may be nil. The planted breaks are each marker in a template that
// does not take it.
func TestTemplateNotLiteral(t *testing.T) {
	own := func(e *Entry) bool { return string(e.Value) == "own" }
	for _, c := range []struct {
		file, value string
		want        bool
	}{
		{"a.ini.j2", "{{ x }}", true}, {"a.ini.j2", "{% if %}", true}, {"a.ini.j2", "{# c #}", true},
		{"a.ini.j2", "<%= x %>", false}, {"a.ini.j2", "${x}", false}, {"a.ini.j2", "own", true}, {"a.ini.j2", "x", false},
		{"a.ini.erb", "<%= x %>", true}, {"a.ini.erb", "{{ x }}", true}, {"a.ini.erb", "${x}", false},
		{"a.ini.tpl", "${x}", true}, {"a.ini.tftpl", "%{ if x }", true}, {"a.ini.tmpl", "${x}", false},
		{"a.ini", "{{ x }}", false}, {"a.ini", "own", true},
	} {
		test := TemplateNotLiteral(c.file, own)
		if got := test(&Entry{Value: []byte(c.value)}); got != c.want {
			t.Errorf("%s %q: %v, want %v", c.file, c.value, got, c.want)
		}
	}
	if TemplateNotLiteral("a.ini", nil) != nil {
		t.Error("a file that is no template has a test where the dialect has none")
	}
	if test := TemplateNotLiteral("dir/a.properties.j2", nil); test == nil || !test(&Entry{Value: []byte("{{ x }}")}) || test(&Entry{Value: []byte("x")}) {
		t.Error("a template with no test of the dialect's")
	}
}

// Query.Wants is the reference the fuzz targets hold the matcher to: an
// entry of its section, of a host its host matches, and of its key, or a
// name where it has no key. The planted breaks are each condition dropped.
func TestQueryWants(t *testing.T) {
	equal := func(host, want []byte, _ *Meter) (bool, error) { return bytes.Equal(host, want), nil }
	key := Query{Section: []byte("s"), Key: []byte("k"), HasKey: true}
	names := Query{Section: []byte("s")}
	host := Query{Section: []byte("s"), Key: []byte("k"), HasKey: true, Host: []byte("h"), HasHost: true, MatchHost: equal}
	for i, c := range []struct {
		q    Query
		e    Entry
		want bool
	}{
		{key, Entry{Section: []byte("s"), Kind: EntryKey, Key: []byte("k")}, true},
		{key, Entry{Section: []byte("t"), Kind: EntryKey, Key: []byte("k")}, false},
		{key, Entry{Section: []byte("s"), Kind: EntryKey, Key: []byte("j")}, false},
		{key, Entry{Section: []byte("s"), Kind: EntryName, Key: []byte("k")}, false},
		{names, Entry{Section: []byte("s"), Kind: EntryName}, true},
		{names, Entry{Section: []byte("s"), Kind: EntryKey}, false},
		{host, Entry{Section: []byte("s"), Host: []byte("h"), Kind: EntryKey, Key: []byte("k")}, true},
		{host, Entry{Section: []byte("s"), Host: []byte("x"), Kind: EntryKey, Key: []byte("k")}, false},
	} {
		if got := c.q.Wants(&c.e); got != c.want {
			t.Errorf("%d: %v, want %v", i, got, c.want)
		}
		m := NewMatcher(&c.q, nil)
		if got := m.Wants(&c.e); got != c.want || m.Err != nil {
			t.Errorf("%d: the matcher %v, %v; want %v", i, got, m.Err, c.want)
		}
	}
}

// The matcher charges its comparisons, and a budget it spends ends the read
// as work, in FromData too. The planted break is a matcher that charges
// nothing, which a file of many entries would read for free.
func TestMatcherChargesItsWork(t *testing.T) {
	q := Query{Section: []byte("s"), Key: []byte("k"), HasKey: true}
	m := NewMatcher(&q, &Meter{Budget: entryWork})
	e := Entry{Section: []byte("s"), SectionSeq: 1, Kind: EntryKey, Key: []byte("k")}
	if m.Wants(&e) || !errors.Is(m.Err, ErrWork) {
		t.Errorf("past the budget: %v", m.Err)
	}
	if m := NewMatcher(&q, &Meter{Budget: entryWork + 1}); m.Wants(&e) || !errors.Is(m.Err, ErrWork) {
		t.Errorf("past the budget at the section: %v", m.Err)
	}
	x := EntryExtractor{Source: Source{File: "f", Limit: DefaultMaxBytes}, Query: q, Read: func(_ []byte, _ *Meter, visit func(*Entry) error) error {
		for range 1 << 20 {
			if err := visit(&e); err != nil {
				return err
			}
		}
		return nil
	}}
	_, err := x.FromData([]byte("a"))
	extracttest.WantError(t, err, "f: reading the file and matching the spec take more than 128 units of work")
}

// TooDeep names the line and the limit, and Shape names a container.
func TestTooDeepAndShapeNames(t *testing.T) {
	if got, want := TooDeep(7).Error(), "line 7: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if Mapping.String() != "a mapping" || Sequence.String() != "a sequence" {
		t.Errorf("%s and %s", Mapping, Sequence)
	}
}
