package core

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// fieldNameRule is how a refusal of a field name words the form, which
// check.IdentifierForm holds: what agree names after the extractor's name,
// and what CEL reads after a dot, r.name.
const fieldNameRule = "a field name is a letter or _, then letters, digits and _, so assert reads it as r.name"

// checkFieldName refuses a name that is no field name, saying why, after
// what, which names where it was found.
func checkFieldName(what, name string) error {
	if !check.IdentifierForm(name) {
		return fmt.Errorf("%s %s is no field name; %s", what, Quote(name), fieldNameRule)
	}
	if check.Reserved(name) {
		return fmt.Errorf("%s %s is a word CEL reserves, which its language definition keeps out of field names, so assert may not read it as r.%s; name the field otherwise", what, Quote(name), name)
	}
	return nil
}

// Field is one field of the records a yaml, json or toml extractor reads
// with as: record: its name, and the path read from each element the
// extractor's path selects.
type Field struct {
	Name string
	path Path
}

// CheckFields checks the fields key of a kind's extractor against its as
// key: as: record takes fields, a name for each path read from the element,
// no two of them over the same path, and no other as takes the key. It returns the fields sorted by name,
// which is the order of a record's keys.
func CheckFields(kind, as string, fields map[string]string) ([]Field, error) {
	if as != "record" {
		if fields != nil {
			return nil, fmt.Errorf("%s: fields takes as: record, which reads each field from the element the path selects into one record", kind)
		}
		return nil, nil
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("%s: as: record takes fields, a name for each path read from the element the path selects, such as fields: {name: name, url: url}", kind)
	}
	out, err := readFields(kind, fields)
	if err != nil {
		return nil, err
	}
	if err := checkPathsOnce(kind, out, fields); err != nil {
		return nil, err
	}
	return out, nil
}

// readFields checks each field's name and parses its path, and returns the
// fields sorted by name.
func readFields(kind string, fields map[string]string) ([]Field, error) {
	out := make([]Field, 0, len(fields))
	for name, p := range fields {
		if err := checkFieldName(kind+": fields:", name); err != nil {
			return nil, err
		}
		path, err := CheckPath(kind+": fields: "+name, p)
		if err != nil {
			return nil, err
		}
		out = append(out, Field{Name: name, path: path})
	}
	slices.SortFunc(out, func(a, b Field) int {
		if a.Name < b.Name {
			return -1
		}
		return 1 // names are a map's keys, so no two are equal
	})
	return out, nil
}

// checkPathsOnce refuses two fields over one path, taken in the order of
// out. Two fields over one path hold the same node in every record, so an
// agree over both would compare each node with itself and hold.
func checkPathsOnce(kind string, out []Field, fields map[string]string) error {
	byPath := make(map[string]string, len(out))
	for _, f := range out {
		if first, ok := byPath[fields[f.Name]]; ok {
			return fmt.Errorf("%s: fields: %s and %s both read the path %s; two fields of one path hold the same node, so agree over them would compare it with itself; name the path once", kind, Quote(first), Quote(f.Name), Quote(fields[f.Name]))
		}
		byPath[fields[f.Name]] = f.Name
	}
	return nil
}

// records turns each node a reading selected into a record: a mapping of
// the reading's fields, each the node its path selects from the element,
// whole, at the line of the file its lineMap gives. A field whose path
// selects nothing is left out of its record, so has() tests it; one whose
// path selects more than one node is an error, since a field holds one
// value, and so is a scalar or a sequence that holds none of the fields,
// such as the string a ConfigMap's key holds before an embedded step reads
// it. A mapping that holds none is an empty record: a path that misses in
// some elements is what has() tests, and one that misses in every element
// is the rule's to refuse. The records count against MaxValues and
// max_bytes as mappings handed over whole do, each record and each field's
// name included.
func (r *reading) records(nodes []*Node, at []lineMap) ([]check.Value, error) {
	w := wholeWalk{r: r}
	out := make([]check.Value, 0, len(nodes))
	for i, n := range nodes {
		v, err := r.record(&w, n, at[i])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// record is the record of the element n, whose lines at maps, counted
// through w.
func (r *reading) record(w *wholeWalk, n *Node, at lineMap) (check.Value, error) {
	d := &check.Data{Kind: check.DataMap, At: check.Location{File: r.File, Line: r.line(at, n.Line)}}
	if err := w.count(1, 0); err != nil {
		return check.Value{}, err
	}
	for _, f := range r.Fields {
		if err := r.addField(w, d, f, n, at); err != nil {
			return check.Value{}, err
		}
	}
	if len(d.Keys) == 0 && n.Shape != Mapping {
		return check.Value{}, r.noFieldsError(n, d.At.Line)
	}
	return check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d}, nil
}

// addField adds to the record d the field f, the node its path selects
// from the element n, if it selects one, counted through w.
func (r *reading) addField(w *wholeWalk, d *check.Data, f Field, n *Node, at lineMap) error {
	sel, err := f.path.eval(&r.m, []*Node{n})
	if err != nil {
		return fmt.Errorf("%s: %s, field %s: %w", r.File, r.chain, Quote(f.Name), clipTooMany(err))
	}
	if len(sel) == 0 {
		return nil
	}
	if len(sel) > 1 {
		return fmt.Errorf("%s: %s: field %s selects %d nodes in the element at line %d; a field holds one value, so select one, such as [0]",
			r.File, r.chain, Quote(f.Name), len(sel), d.At.Line)
	}
	if err := w.count(0, int64(len(f.Name))); err != nil {
		return err
	}
	v, err := w.data(sel[0], at)
	if err != nil {
		return err
	}
	d.Keys = append(d.Keys, f.Name)
	d.Items = append(d.Items, v)
	return nil
}

// noFieldsError refuses the element n at line, a scalar or a sequence that
// holds none of the fields.
func (r *reading) noFieldsError(n *Node, line int) error {
	what := "a scalar"
	if n.Shape == Sequence {
		what = n.Shape.String()
	}
	return fmt.Errorf("%s: %s selects %s at line %d, which holds none of the fields %s; as: record reads each field's path from the element, so check the paths, or select the elements that hold them",
		r.File, r.chain, what, line, strings.Join(r.fieldNames(), ", "))
}

// fieldNames are the names of the reading's fields, in a record's order.
func (r *reading) fieldNames() []string {
	names := make([]string, len(r.Fields))
	for i, f := range r.Fields {
		names[i] = f.Name
	}
	return names
}

// clipTooMany is err, or the reason of TooManyValues when err is a path's
// refusal of too many values, which says only "too many values". The
// caller names the file once, before it.
func clipTooMany(err error) error {
	if errors.Is(err, ErrTooManyValues) {
		return errTooMany
	}
	return err
}

// Records returns e as the search of a regex extractor with as: record: one
// record for each match, whose fields are the pattern's named groups, in the
// order the pattern opens them, each at the line where its group starts. A
// group that took no part in a match is left out of its record. kind starts
// every refusal: a pattern with no named group, a name that is no field
// name or one CEL reserves, and a name given to two groups.
func (e Search) Records(kind string) (Search, error) {
	names := e.re.SubexpNames()
	seen := map[string]bool{}
	for _, name := range names[1:] {
		if name == "" {
			continue
		}
		if err := checkFieldName(kind+": as: record: the group name", name); err != nil {
			return Search{}, err
		}
		if seen[name] {
			return Search{}, fmt.Errorf("%s: as: record: the pattern names two groups %s; a record holds each field once", kind, Quote(name))
		}
		seen[name] = true
	}
	if len(seen) == 0 {
		return Search{}, fmt.Errorf("%s: as: record reads the pattern's named groups, such as (?P<name>...), as the fields of each match's record, and the pattern names none", kind)
	}
	e.fields = names
	return e, nil
}

// RegexFields returns the field names of the records a regex pattern's
// matches give with as: record: its named groups, in the order the pattern
// opens them. It is nil for a pattern that does not compile.
func RegexFields(pattern string) []string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range re.SubexpNames()[1:] {
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// records returns one record for each match in data with the context
// e.around asks for, as Records says. The records count against MaxValues
// and max_bytes as mappings handed over whole do: each record and each
// field is a node, and each field's name and text are bytes.
func (e Search) records(data []byte) ([]check.Value, error) {
	l := NewLineCursor(data)
	out := []check.Value{}
	var b recordBudget
	clock := e.clock()
	in := e.around.of(data, clock.deadline, e.File, e.TimeLimit, e.whoseLimit())
	err := e.each(data, clock, func(m []int) error {
		// Each match counts, its fields with it, before the context drops
		// any, as the matches of a search for group 1 do (ADR 0012).
		if err := b.add(e, m); err != nil {
			return err
		}
		if ok, err := in.holds(m[0], m[1]); err != nil || !ok {
			return err
		}
		out = append(out, e.record(data, l, m))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// recordBudget is what the records of a search have counted so far: each
// record and each field a node, and each field's name and text in bytes.
type recordBudget struct {
	nodes int
	size  int64
}

// add counts the record of the match m and its fields, and refuses records
// that pass MaxValues nodes or e's max_bytes.
func (b *recordBudget) add(e Search, m []int) error {
	b.nodes++
	for g := 1; g < len(e.fields); g++ {
		if start, end := m[2*g], m[2*g+1]; e.fields[g] != "" && start >= 0 {
			b.nodes++
			b.size += int64(len(e.fields[g]) + end - start)
		}
	}
	if b.nodes > MaxValues {
		return fmt.Errorf("%s: the records the pattern matches hold more than %d nodes in all, each record and each field one, the limit on an extractor's values; narrow the pattern", e.File, MaxValues)
	}
	if b.size > e.Limit {
		return fmt.Errorf("%s: the records the pattern matches hold more than %d bytes of field names and text in all, the extractor's max_bytes; narrow the pattern, or set max_bytes: higher", e.File, e.Limit)
	}
	return nil
}

// record is the record of the match m: a field for each named group that
// took part in it, at the line where the group starts.
func (e Search) record(data []byte, l *LineCursor, m []int) check.Value {
	d := &check.Data{Kind: check.DataMap, At: check.Location{File: e.File, Line: l.At(m[0])}}
	for g := 1; g < len(e.fields); g++ {
		start, end := m[2*g], m[2*g+1]
		if e.fields[g] == "" || start < 0 {
			continue
		}
		d.Keys = append(d.Keys, e.fields[g])
		d.Items = append(d.Items, &check.Data{Kind: check.DataScalar, Text: string(data[start:end]),
			At: check.Location{File: e.File, Line: l.At(start)}})
	}
	return check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d}
}
