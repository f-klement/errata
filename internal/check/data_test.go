package check

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func scalar(text string) *Data { return &Data{Kind: DataScalar, Text: text} }

// sample is {"name": "a", "tags": ["x", "y"], "sub": {"k": "v"}}.
func sample() *Data {
	return &Data{Kind: DataMap, Keys: []string{"name", "tags", "sub"}, Items: []*Data{
		scalar("a"),
		{Kind: DataList, Items: []*Data{scalar("x"), scalar("y")}},
		{Kind: DataMap, Keys: []string{"k"}, Items: []*Data{scalar("v")}},
	}}
}

// Size counts every node, the mapping itself and each sequence included,
// and the bytes of every key and scalar, which the bounds on an
// extractor's values count. Planted breaks: the keys' bytes left out; a
// nested node counted once whatever it holds.
func TestDataSize(t *testing.T) {
	if n, b := sample().Size(); n != 7 || b != 4+4+3+1+1+1+1+1 {
		t.Errorf("Size = %d nodes, %d bytes", n, b)
	}
	if n, b := scalar("abc").Size(); n != 1 || b != 3 {
		t.Errorf("a scalar: %d nodes, %d bytes", n, b)
	}
}

// Show writes a value close to JSON, keys in file order and every string
// quoted, and stops at its limit with ..., never inside a character.
// Planted breaks: the cut at the byte limit whatever it splits, which
// wrote half of é; a mapping's keys left out.
func TestDataShow(t *testing.T) {
	if got := sample().Show(1000); got != `{"name": "a", "tags": ["x", "y"], "sub": {"k": "v"}}` {
		t.Errorf("Show = %s", got)
	}
	if got := sample().Show(10); got != `{"name": "...` {
		t.Errorf("cut at 10: %s", got)
	}
	long := scalar(strings.Repeat("é", 10))
	for limit := range 22 {
		got := long.Show(limit)
		if !strings.HasSuffix(got, "...") || len(got) > limit+3 || !utf8.ValidString(got) || !strings.HasPrefix(long.Show(100), strings.TrimSuffix(got, "...")) {
			t.Errorf("cut at %d: %q", limit, got)
		}
	}
	if got := long.Show(3); got != `"é...` {
		t.Errorf("cut at 3: %q", got)
	}
	if got := long.Show(2); got != `"...` {
		t.Errorf("cut at 2, inside the é: %q", got)
	}
	if got := (&Data{Kind: DataList}).Show(10); got != `[]` {
		t.Errorf("an empty sequence: %s", got)
	}
}

// Normalized applies f to every scalar and to no key, and leaves d as it
// was. Planted breaks: the keys normalized too; d changed in place, which
// normalized the values a second rule over the same extractor reads.
func TestDataNormalized(t *testing.T) {
	d := sample()
	got := d.Normalized(strings.ToUpper)
	if r := got.Show(1000); r != `{"name": "A", "tags": ["X", "Y"], "sub": {"k": "V"}}` {
		t.Errorf("Normalized = %s", r)
	}
	if r := d.Show(1000); r != `{"name": "a", "tags": ["x", "y"], "sub": {"k": "v"}}` {
		t.Errorf("the original changed: %s", r)
	}
}

// A value that fits its limit exactly is shown whole, with no ... after
// it: Show cuts only past the limit. Planted break: the cut at the limit
// itself (>= for >), which wrote "éééééééééé"... at 22 bytes.
func TestDataShowFitsItsLimitExactly(t *testing.T) {
	long := scalar(strings.Repeat("é", 10))
	whole := long.Show(100)
	if got := long.Show(len(whole)); got != whole {
		t.Errorf("at its own length, %d bytes: %q", len(whole), got)
	}
	if got := sample().Show(52); got != `{"name": "a", "tags": ["x", "y"], "sub": {"k": "v"}}` {
		t.Errorf("a mapping at its own length: %q", got)
	}
}

// Normalized with a function that changes nothing is a copy equal to d,
// down to a scalar and an empty sequence having no items. Planted break:
// items made for a node with none (>= 0 for > 0), which gave every scalar
// an empty slice where d has none.
func TestDataNormalizedCopiesTheShape(t *testing.T) {
	d := &Data{Kind: DataList, Items: []*Data{sample(), {Kind: DataList}, scalar("s")}}
	if got := d.Normalized(func(s string) string { return s }); !reflect.DeepEqual(got, d) {
		t.Errorf("Normalized(identity) = %#v, want %#v", got, d)
	}
}

// Field is a mapping's value of a key, its first key included, and nil for
// a key the mapping lacks or a value that is no mapping. Planted breaks:
// the kind test negated; the first key missed (i > 0 for i >= 0).
func TestDataField(t *testing.T) {
	d := sample()
	if got := d.Field("name"); got == nil || got.Text != "a" {
		t.Errorf(`Field("name") = %v`, got)
	}
	if got := d.Field("sub"); got == nil || got.Field("k").Text != "v" {
		t.Errorf(`Field("sub") = %v`, got)
	}
	if got := d.Field("url"); got != nil {
		t.Errorf(`Field("url") = %v, want nil`, got)
	}
	if got := d.Items[1].Field("x"); got != nil {
		t.Errorf("a sequence's Field = %v, want nil", got)
	}
}
