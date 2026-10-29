package core

import (
	"fmt"

	"github.com/f-klement/errata/internal/check"
)

// CheckAs checks the as key of a kind's extractor: "" for flat values, map
// to hand assert each mapping the path selects whole, or record to read the
// fields of each element the path selects into one mapping (CheckFields). It
// reports whether the extractor returns mappings.
func CheckAs(kind, as string) (bool, error) {
	if as == "" {
		return false, nil
	}
	if as == "map" || as == "record" {
		return true, nil
	}
	return false, fmt.Errorf("%s: as %s is not one errata reads; write map to hand assert each mapping the path selects whole, record to read the fields of each element it selects into one, or leave the key out for its scalars", kind, Quote(as))
}

// whole turns the nodes a reading selected into values that each hold a
// mapping whole, as check.Data, every node of it at the line of the file
// its lineMap gives. A node that is no mapping is an error, as a mapping
// is for a flat value. The nodes of every mapping count against MaxValues
// together, and the bytes of their keys and scalars against the
// extractor's max_bytes, so a mapping YAML aliases repeat costs each time
// it is selected, and the walk stops at the first node past a bound.
func (r *reading) whole(nodes []*Node, at []lineMap) ([]check.Value, error) {
	w := wholeWalk{r: r}
	out := make([]check.Value, 0, len(nodes))
	for i, n := range nodes {
		if n.Shape != Mapping {
			what := "a scalar"
			if n.Shape == Sequence {
				what = "a sequence"
			}
			return nil, fmt.Errorf("%s: %s selects %s at line %d, not a mapping; as: map hands assert each mapping the path selects, so add [*] to take a sequence's members", r.File, r.chain, what, r.line(at[i], n.Line))
		}
		d, err := w.data(n, at[i])
		if err != nil {
			return nil, err
		}
		out = append(out, check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d})
	}
	return out, nil
}

// wholeWalk copies nodes into check.Data, counting what it copies.
type wholeWalk struct {
	r     *reading
	nodes int
	bytes int64
}

// count adds nodes and bytes to what the walk has copied, and refuses them
// past MaxValues nodes or the extractor's max_bytes.
func (w *wholeWalk) count(nodes int, bytes int64) error {
	if w.nodes += nodes; w.nodes > MaxValues {
		return fmt.Errorf("%s: the mappings the path selects hold more than %d nodes in all, the limit on an extractor's values; aliases can repeat a mapping, so narrow the path", w.r.File, MaxValues)
	}
	if w.bytes += bytes; w.bytes > w.r.Limit {
		return fmt.Errorf("%s: the mappings the path selects hold more than %d bytes of keys and scalars in all, the extractor's max_bytes; aliases can repeat a mapping, so narrow the path, or set max_bytes: higher", w.r.File, w.r.Limit)
	}
	return nil
}

// data is n as check.Data, at the lines m gives.
func (w *wholeWalk) data(n *Node, m lineMap) (*check.Data, error) {
	size := int64(len(n.Text))
	for _, k := range n.Keys {
		size += int64(len(k))
	}
	if err := w.count(1, size); err != nil {
		return nil, err
	}
	d := &check.Data{At: check.Location{File: w.r.File, Line: w.r.line(m, n.Line)}}
	if n.Shape == Scalar {
		d.Kind, d.Text = check.DataScalar, n.Text
		return d, nil
	}
	items := n.Items
	d.Kind = check.DataList
	if n.Shape == Mapping {
		d.Kind, d.Keys, items = check.DataMap, n.Keys, n.Vals
	}
	d.Items = make([]*check.Data, len(items))
	for i, it := range items {
		c, err := w.data(it, m)
		if err != nil {
			return nil, err
		}
		d.Items[i] = c
	}
	return d, nil
}
