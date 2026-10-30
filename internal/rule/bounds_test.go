package rule

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// unknownWith runs c and fails t unless the rule is unknown with a message
// that holds want.
func unknownWith(t *testing.T, c check.Check, want string) {
	t.Helper()
	res := Run(t.Context(), "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, want) {
		t.Fatalf("verdict %s message %q, want unknown and %q", res.Verdict, res.Message, want)
	}
}

// same is n values x at f:1.
func same(n int) []check.Value {
	out := make([]check.Value, n)
	for i := range out {
		out[i] = at("x", "f", 1)
	}
	return out
}

// An extractor that returns more than MaxValues values makes its rule
// unknown at the rule, with a message naming the bound, whatever its
// reader checked; MaxValues of them pass. A value handed over whole counts
// each of its nodes. Planted break: the count check removed from bounded.
func TestRuleHoldsAnExtractorToMaxValues(t *testing.T) {
	unknownWith(t, agreeRule(named("a", fake{values: same(check.MaxValues + 1)})),
		`extractor "a": it returned more than 100000 values, the limit on an extractor's values`)
	if res := Run(t.Context(), "", agreeRule(named("a", fake{values: same(check.MaxValues)}))); res.Verdict != check.VerdictTrue {
		t.Errorf("MaxValues values: verdict %s message %q, want true", res.Verdict, res.Message)
	}
	items := make([]*check.Data, check.MaxValues)
	for i := range items {
		items[i] = &check.Data{Text: "x"}
	}
	whole := check.Value{Text: "[...]", At: check.Location{File: "f", Line: 1}, Data: &check.Data{Kind: check.DataList, Items: items}}
	unknownWith(t, agreeRule(named("a", fake{values: []check.Value{whole}})),
		`extractor "a": it returned more than 100000 values`)
}

// An extractor whose values hold more bytes in all than its max_bytes makes
// its rule unknown, naming the bound; values that hold exactly max_bytes
// pass. Planted break: the bytes check removed from bounded.
func TestRuleHoldsAnExtractorToItsMaxBytes(t *testing.T) {
	limited := func(values []check.Value) check.NamedExtractor {
		e := named("a", fake{values: values})
		e.Description.MaxBytes = 10
		return e
	}
	unknownWith(t, agreeRule(limited(same(11))),
		`extractor "a": its values hold more than 10 bytes in all, the extractor's max_bytes; set max_bytes: higher`)
	if res := Run(t.Context(), "", agreeRule(limited(same(10)))); res.Verdict != check.VerdictTrue {
		t.Errorf("max_bytes bytes: verdict %s message %q, want true", res.Verdict, res.Message)
	}
}

// An extractor that returns a value at a location that is no path inside
// the repository makes its rule unknown, naming the location: a path out of
// the root, through . too, an absolute one, none, a config file outside it,
// and a backslash that is part of a name on Linux and climbs out there.
// Planted breaks: the location check removed from bounded; . read as a
// name, which passed ./../x; Inside asking only the Windows reading, which
// passed a\b/../...
func TestRuleRefusesAValueOutsideTheRoot(t *testing.T) {
	for _, l := range []check.Location{{File: "../x"}, {File: "a/../../x"}, {File: "./../x"}, {File: "a/./../../x"}, {File: `a\b/../..`}, {File: "/etc/passwd"}, {File: ""}, {File: "f", Outside: true}} {
		v := check.Value{Text: "x", At: l}
		unknownWith(t, agreeRule(named("a", fake{values: []check.Value{v}})),
			`extractor "a": it returned a value at `+strconv.Quote(l.File)+`, which is no path inside the repository`)
	}
	if res := Run(t.Context(), "", agreeRule(named("a", fake{values: []check.Value{at("x", "a/b.yaml", 1), at("x", "./c", 2)}}))); res.Verdict != check.VerdictTrue {
		t.Errorf("a value inside: verdict %s message %q, want true", res.Verdict, res.Message)
	}
}

// The location check reads a path as text, the same on every OS: a
// backslash separates names as a slash does, and a path that starts with
// one, or whose first name once . and .. are resolved is a drive, is
// absolute, so a .. through a backslash, C:\x and a/../C:x are refused on
// Linux too, with a message that says why, since on Linux the file is in
// the repository. A name Windows keeps for a device, such as aux.yaml,
// which filepath.IsLocal refuses there, is a path inside the repository
// like any other, and so are a drive after the first name and a digit
// before a colon. Planted breaks, each in check's local: only a leading
// slash absolute; only a slash a separator; a drive refused only at the
// start of the path, which passed ./C:x, a/../C:x, a/b/../../C: and
// a\..\c:\x; any byte before a colon a drive, which refused 1:x; and a name
// whose part before the first dot is aux refused, as filepath.IsLocal
// refuses it on Windows. And in bounded: the message for a path outside
// on Windows only dropped.
func TestRuleReadsALocationTheSameOnEveryOS(t *testing.T) {
	for _, f := range []string{`a\..\..\x`, `..\x`, `C:\x`, `c:x`, `c:z.yaml`, `\\host\share\x`, `\x`, "./C:x", "a/../C:x", "a/b/../../C:", `a\..\c:\x`, "a/../b:c"} {
		v := check.Value{Text: "x", At: check.Location{File: f}}
		unknownWith(t, agreeRule(named("a", fake{values: []check.Value{v}})),
			`extractor "a": it returned a value at `+strconv.Quote(f)+`, which is absolute, names a drive or climbs out where a backslash separates names, as on Windows, so it is refused on every OS`)
	}
	inside := []check.Value{at("x", "aux.yaml", 1), at("x", "NUL", 2), at("x", "dir/con.txt", 3), at("x", "COM1.yml", 4), at("x", `a\..\b`, 5), at("x", "b/c:d", 6), at("x", "1:x", 7)}
	if res := Run(t.Context(), "", agreeRule(named("a", fake{values: inside}))); res.Verdict != check.VerdictTrue {
		t.Errorf("reserved names inside: verdict %s message %q, want true", res.Verdict, res.Message)
	}
}

// A value at a path that holds a newline or another control character, a
// direction control or a line or paragraph separator makes its rule unknown,
// naming the path quoted, so neither the text report nor a SARIF viewer
// prints a line the path forged or reordered. A path with a space, a
// backslash or a letter outside ASCII passes. Planted break: the control
// character check removed from bounded.
func TestRuleRefusesALocationWithAControlCharacter(t *testing.T) {
	for _, f := range []string{"a\nERRATUM forged.yaml", "a\r.yaml", "a\x1b[2J.yaml", "a\x7f", "a\u0085b", "a\u202e.yaml", "a\u2066b", "a\u2028b"} {
		v := check.Value{Text: "x", At: check.Location{File: f, Line: 1}}
		unknownWith(t, agreeRule(named("a", fake{values: []check.Value{v}})),
			`extractor "a": it returned a value at `+strconv.Quote(f)+`, a path that holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on`)
	}
	ok := []check.Value{at("x", "a b.yaml", 1), at("x", `a\tb.yaml`, 2), at("x", "é.yaml", 3)}
	if res := Run(t.Context(), "", agreeRule(named("a", fake{values: ok}))); res.Verdict != check.VerdictTrue {
		t.Errorf("printable paths: verdict %s message %q, want true", res.Verdict, res.Message)
	}
}

// A value handed over whole whose Text holds more than MaxShown bytes makes
// its rule unknown, though its Data is small and its max_bytes not reached;
// one whose Text is MaxShown bytes passes. Planted break: the check of a
// whole value's Text removed from bounded.
func TestRuleHoldsAWholeValuesText(t *testing.T) {
	whole := func(text string) check.NamedExtractor {
		v := check.Value{Text: text, At: check.Location{File: "f", Line: 1}, Data: &check.Data{Text: "x"}}
		e := named("a", fake{values: []check.Value{v, v}})
		e.Description.Shape = check.ShapeMap
		return e
	}
	c := func(e check.NamedExtractor) check.Check {
		return check.Check{ID: "r", Kind: &check.Rule{Extractors: []check.NamedExtractor{e}, Assert: "x", Compiled: counting{new(int)}}}
	}
	unknownWith(t, c(whole(strings.Repeat("a", 1<<20))),
		`extractor "a": it returned a value handed over whole at "f:1" whose text holds 1048576 bytes, over the 259 a whole value shows`)
	if res := Run(t.Context(), "", c(whole(strings.Repeat("a", check.MaxShown)))); res.Verdict == check.VerdictUnknown {
		t.Errorf("a text of MaxShown bytes: unknown, %q", res.Message)
	}
}

// A value whose Shown the extractor set makes its rule unknown: only the
// rule sets it, and a report writes it unquoted. Planted break: the check
// of Shown removed from bounded.
func TestRuleRefusesAShownFormFromAnExtractor(t *testing.T) {
	forged := at("x", "f", 1)
	forged.Shown = "x\nERRATUM forged"
	unknownWith(t, agreeRule(named("a", fake{values: []check.Value{forged, at("x", "f", 2)}})),
		`extractor "a": it returned a value at "f:1" with its shown form set, which only the rule sets`)
}

// cancelling cancels the run when it extracts, so the extractor after it
// meets a context that has ended.
type cancelling struct{ cancel context.CancelFunc }

func (c cancelling) Extract(context.Context, string) ([]check.Value, error) {
	c.cancel()
	return []check.Value{at("x", "f", 1)}, nil
}

func (c cancelling) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return c.Extract(ctx, root)
}

// A rule whose context ends while an extractor runs stops before the next
// one, which never runs. Planted break: the check of the context between
// extractors removed.
func TestRuleStopsBeforeTheNextExtractorOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ran := []string{}
	c := agreeRule(
		check.NamedExtractor{Name: "a", Extractor: cancelling{cancel}},
		check.NamedExtractor{Name: "b", Extractor: ranFake{name: "b", ran: &ran, fake: fake{values: []check.Value{at("x", "f", 1)}}}},
	)
	res := Run(ctx, "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `cancelled before extractor "b" ran`) || len(ran) != 0 {
		t.Fatalf("verdict %s message %q ran %q, want unknown, b cancelled and never run", res.Verdict, res.Message, ran)
	}
}

// errNotCancelled is what blocking returns when its context did not end in
// time: the rule did not hand it the run's context.
var errNotCancelled = errors.New("the context did not end")

// blocking waits until its context ends, as a plugin's process would run
// until it is stopped, and returns the context's error.
type blocking struct{}

func (blocking) Extract(ctx context.Context, _ string) ([]check.Value, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errNotCancelled
	}
}

func (b blocking) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return b.Extract(ctx, root)
}

// An extractor that blocks until its context ends returns when the run is
// cancelled, and its rule is unknown with the reason. Planted break: the
// rule hands the extractor a context of its own, which never ends.
func TestABlockedExtractorReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	res := Run(ctx, "", agreeRule(check.NamedExtractor{Name: "a", Extractor: blocking{}}))
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `extractor "a": context deadline exceeded`) {
		t.Fatalf("verdict %s message %q, want unknown with the deadline", res.Verdict, res.Message)
	}
}
