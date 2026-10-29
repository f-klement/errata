package core

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// fieldName is the form of a record's field name: what agree names after
// the extractor's name, and what CEL reads after a dot, r.name, unless it
// is one of celReserved.
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// fieldNameRule is how a refusal of a field name words the form.
const fieldNameRule = "a field name is a letter or _, then letters, digits and _, so assert reads it as r.name"

// celReserved are the words CEL's language definition reserves. It keeps
// false, in, null and true out of field names, so r.in is a syntax error,
// and reserves the rest for the languages that embed CEL, advising against
// them as field names; cel-go v0.32 reads those after a dot, and a later
// one may not.
var celReserved = []string{
	"as", "break", "const", "continue", "else", "false", "for", "function", "if", "import",
	"in", "let", "loop", "namespace", "null", "package", "return", "true", "var", "void", "while",
}

// checkFieldName refuses a name that is no field name, saying why, after
// what, which names where it was found.
func checkFieldName(what, name string) error {
	if !fieldName.MatchString(name) {
		return fmt.Errorf("%s %s is no field name; %s", what, Quote(name), fieldNameRule)
	}
	if slices.Contains(celReserved, name) {
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
	// Two fields over one path hold the same node in every record, so an
	// agree over both would compare each node with itself and hold.
	byPath := make(map[string]string, len(out))
	for _, f := range out {
		if first, ok := byPath[fields[f.Name]]; ok {
			return nil, fmt.Errorf("%s: fields: %s and %s both read the path %s; two fields of one path hold the same node, so agree over them would compare it with itself; name the path once", kind, Quote(first), Quote(f.Name), Quote(fields[f.Name]))
		}
		byPath[fields[f.Name]] = f.Name
	}
	return out, nil
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
		d := &check.Data{Kind: check.DataMap, At: check.Location{File: r.File, Line: r.line(at[i], n.Line)}}
		if err := w.count(1, 0); err != nil {
			return nil, err
		}
		for _, f := range r.Fields {
			sel, err := f.path.eval(&r.m, []*Node{n})
			if err != nil {
				return nil, fmt.Errorf("%s: %s, field %s: %w", r.File, r.chain, Quote(f.Name), clipTooMany(err, r.File))
			}
			if len(sel) == 0 {
				continue
			}
			if len(sel) > 1 {
				return nil, fmt.Errorf("%s: %s: field %s selects %d nodes in the element at line %d; a field holds one value, so select one, such as [0]",
					r.File, r.chain, Quote(f.Name), len(sel), d.At.Line)
			}
			if err := w.count(0, int64(len(f.Name))); err != nil {
				return nil, err
			}
			v, err := w.data(sel[0], at[i])
			if err != nil {
				return nil, err
			}
			d.Keys = append(d.Keys, f.Name)
			d.Items = append(d.Items, v)
		}
		if len(d.Keys) == 0 && n.Shape != Mapping {
			what := "a scalar"
			if n.Shape == Sequence {
				what = n.Shape.String()
			}
			return nil, fmt.Errorf("%s: %s selects %s at line %d, which holds none of the fields %s; as: record reads each field's path from the element, so check the paths, or select the elements that hold them",
				r.File, r.chain, what, d.At.Line, strings.Join(r.fieldNames(), ", "))
		}
		out = append(out, check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d})
	}
	return out, nil
}

// fieldNames are the names of the reading's fields, in a record's order.
func (r *reading) fieldNames() []string {
	names := make([]string, len(r.Fields))
	for i, f := range r.Fields {
		names[i] = f.Name
	}
	return names
}

// clipTooMany is err, or the refusal of too many values in file when err is
// that one, which names the file on its own.
func clipTooMany(err error, file string) error {
	if errors.Is(err, ErrTooManyValues) {
		return TooManyValues(file)
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
	nodes, size := 0, int64(0)
	clock := e.clock()
	in := e.around.of(data, clock.deadline, e.File, e.TimeLimit, e.whoseLimit())
	err := e.each(data, clock, func(m []int) error {
		// Each match counts, its fields with it, before the context drops
		// any, as the matches of a search for group 1 do (ADR 0012).
		nodes++
		for g := 1; g < len(e.fields); g++ {
			if start, end := m[2*g], m[2*g+1]; e.fields[g] != "" && start >= 0 {
				nodes++
				size += int64(len(e.fields[g]) + end - start)
			}
		}
		if nodes > MaxValues {
			return fmt.Errorf("%s: the records the pattern matches hold more than %d nodes in all, each record and each field one, the limit on an extractor's values; narrow the pattern", e.File, MaxValues)
		}
		if size > e.Limit {
			return fmt.Errorf("%s: the records the pattern matches hold more than %d bytes of field names and text in all, the extractor's max_bytes; narrow the pattern, or set max_bytes: higher", e.File, e.Limit)
		}
		if ok, err := in.holds(m[0], m[1]); err != nil || !ok {
			return err
		}
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
		out = append(out, check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
