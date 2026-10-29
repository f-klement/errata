package check

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DataKind is what a Data holds: a scalar, a mapping or a sequence.
type DataKind uint8

// The kinds of a Data.
const (
	DataScalar DataKind = iota
	DataMap
	DataList
)

// Data is a value an extractor hands an assert whole, for a yaml, json or
// toml extractor with as: map: a mapping with every key and value under it,
// each with the place it was found. A scalar is its string form, the text a
// flat value would have. A record, several paths of one element kept
// together, is a Data of kind DataMap with a fixed set of keys.
type Data struct {
	Kind DataKind
	// Text is a scalar's string form; "" for a mapping or a sequence.
	Text string
	// Keys are a mapping's keys, in the order the file wrote them.
	Keys []string
	// Items are a mapping's values, in the order of Keys, or a sequence's
	// items, in file order.
	Items []*Data
	// At is where the value starts: the mapping, the sequence or the
	// scalar.
	At Location
}

// Field is the value of key in d, a mapping, or nil when d has no such key
// or is no mapping.
func (d *Data) Field(key string) *Data {
	if d.Kind != DataMap {
		return nil
	}
	if i := slices.Index(d.Keys, key); i >= 0 {
		return d.Items[i]
	}
	return nil
}

// Size is how many nodes d holds, itself included, and the bytes of its
// keys and scalars, which the bounds on an extractor's values count.
func (d *Data) Size() (nodes int, bytes int64) {
	nodes, bytes = 1, int64(len(d.Text))
	for _, k := range d.Keys {
		bytes += int64(len(k))
	}
	for _, it := range d.Items {
		n, b := it.Size()
		nodes, bytes = nodes+n, bytes+b
	}
	return nodes, bytes
}

// Normalized is a copy of d with f applied to every scalar, as a rule's
// normalize applies to a flat value. Keys are left as the file wrote them,
// since an expression names them.
func (d *Data) Normalized(f func(string) string) *Data {
	out := &Data{Kind: d.Kind, Text: d.Text, Keys: d.Keys, At: d.At}
	if d.Kind == DataScalar {
		out.Text = f(d.Text)
	}
	if len(d.Items) > 0 {
		out.Items = make([]*Data, len(d.Items))
		for i, it := range d.Items {
			out.Items[i] = it.Normalized(f)
		}
	}
	return out
}

// ShownBytes is how many bytes of a whole value Show writes for a message,
// a report and a value's Text; assert reads the value itself.
const ShownBytes = 256

// Show is d as a message shows it, close to JSON: a mapping as
// {"key": value, ...} in file order, a sequence as [value, ...], and a
// scalar quoted as Go quotes a string. It stops after limit bytes and ends
// with ... where it cut, so a large mapping costs limit bytes, not its
// size.
func (d *Data) Show(limit int) string {
	w := renderer{limit: limit}
	w.data(d)
	if !w.cut {
		return w.b.String()
	}
	// The cut starts at a character, so it does not split one.
	s, n := w.b.String(), limit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// renderer writes a Data for Show until it has written past its limit.
type renderer struct {
	b     strings.Builder
	limit int
	cut   bool
}

func (w *renderer) write(s string) {
	if w.cut {
		return
	}
	w.b.WriteString(s)
	if w.b.Len() > w.limit {
		w.cut = true
	}
}

func (w *renderer) data(d *Data) {
	if w.cut {
		return
	}
	if d.Kind == DataScalar {
		w.write(strconv.Quote(d.Text))
		return
	}
	open, end := "[", "]"
	if d.Kind == DataMap {
		open, end = "{", "}"
	}
	w.write(open)
	for i, it := range d.Items {
		if i > 0 {
			w.write(", ")
		}
		if d.Kind == DataMap {
			w.write(strconv.Quote(d.Keys[i]) + ": ")
		}
		w.data(it)
	}
	w.write(end)
}
