// Package toml is the toml extractor: it reads a path in a TOML file, from
// the tree of core.Node the file parses into.
package toml

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads a path in a TOML file, File, or in each file Files names, a
// file or a glob (core.Files). Embedded reads a path in the document each
// string the path selects holds, and so on, step by step, as core.Embedded
// says.
type Spec struct {
	core.Where
	Path     string          `json:"path"`
	Embedded []core.Embedded `json:"embedded,omitempty"`
	// As is map to hand assert each mapping the path selects whole, or
	// record to read Fields from each node it selects into one, as
	// core.CheckAs says; "" for each scalar.
	As string `json:"as,omitempty"`
	// Fields names each path read from a selected node into its record,
	// for as: record, as core.CheckFields says.
	Fields map[string]string `json:"fields,omitempty"`
}

// Kind is the toml kind, which a rule writes under the key toml.
var Kind = core.Kind[Spec]{Key: "toml", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: its where part as
// core.Where.Place writes it.
func Place(s Spec) Spec {
	s.Where = s.Place()
	return s
}

// Resolve is s with its max_bytes written out, and its files, embedded
// steps and fields copied, for errata list.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	s.Embedded, s.Fields = slices.Clone(s.Embedded), maps.Clone(s.Fields)
	return s
}

// Describe is what s's extractor returns: literal values, each flat, a
// mapping or a record as its as key says.
func Describe(s Spec) check.Description {
	shape, fields := core.TreeShape(s.As, s.Fields)
	return check.Description{Label: "toml", Shape: shape, Fields: fields}
}

// Build checks s and returns its extractor, which reads the documents
// embedded names with p.
func Build(s Spec, p core.Parsers) (check.Extractor, error) {
	return core.BuildTree(Kind.Key, core.Tree{Where: s.Where, Path: s.Path, Embedded: s.Embedded, As: s.As, Fields: s.Fields}, core.TOMLFormat, Parse, p)
}

// Parse reads a TOML file into a tree. go-toml's unstable parser gives
// each value's byte range, which BurntSushi/toml does not expose, and the
// tree is built from its expressions the way TOML scopes them: a key belongs
// to the last table header above it. That parser checks only syntax, and
// decoding the file would check the rest, but the decode's repeated key check
// scans every key and table defined so far for each key it reads. So errata
// never decodes the file: the builder refuses what the decode refuses, a key
// or a table defined twice and a number or a date out of range, with the
// tree's hash maps.
//
// Every scalar keeps the text the file wrote: an integer 0x1F or 1_000, a
// float 1e3 or inf, a datetime 1979-05-27T07:32:00Z. A string is its content
// without quotes and with its escapes resolved.
func Parse(data []byte) (*core.Node, error) {
	b := &tomlBuilder{lines: core.LineIndex(data), root: core.NewMapping(1), size: cap(data), made: map[*core.Node]tomlKind{}}
	b.table = b.root
	var p unstable.Parser
	p.Reset(data)
	for p.NextExpression() {
		if err := b.expression(p.Expression()); err != nil {
			return nil, err
		}
	}
	if err := p.Error(); err != nil {
		return nil, b.parseError(err)
	}
	return b.root, nil
}

// tomlKind is how a table or an array of tables was made, which decides what
// may extend it, as go-toml's decode decides. A node absent from made is a
// value: a scalar, an array, or an inline table, which nothing extends.
type tomlKind uint8

const (
	tomlValue    tomlKind = iota
	tomlImplicit          // named only as a parent in a header, as a is by [a.b]
	tomlHeader            // defined by its own header
	tomlDotted            // made by a dotted key, as a is by a.b = 1
	tomlArray             // an array of tables
)

func (k tomlKind) String() string {
	switch k {
	case tomlImplicit, tomlHeader:
		return "a table"
	case tomlDotted:
		return "a table by a dotted key"
	case tomlArray:
		return "an array of tables"
	default:
		return "a value"
	}
}

type tomlBuilder struct {
	lines core.Lines
	size  int // the capacity of the input, against which Highlight is found
	root  *core.Node
	table *core.Node // the table the last header opened
	depth int        // the depth of table
	made  map[*core.Node]tomlKind
}

// parseError names the line of a syntax error. go-toml's ParserError marks
// the bytes it is about with Highlight, a slice of the input, but its message
// has no line.
func (b *tomlBuilder) parseError(err error) error {
	var perr *unstable.ParserError
	if !errors.As(err, &perr) {
		return err
	}
	off := b.size - cap(perr.Highlight)
	// A Highlight from another buffer could put off anywhere, so the range
	// is checked. off > b.size cannot hold, since no capacity is negative,
	// and off == b.size only for an empty Highlight at the end of the
	// buffer's capacity, which go-toml v2.4.3 never returns: NextExpression
	// widens an empty Highlight at the end of the input to the last byte.
	// TestGoTOMLWidensAnErrorAtTheEnd pins that, so a go-toml that stops
	// widening fails a test rather than reaching this guard unseen.
	if perr.Highlight == nil || off < 0 || off > b.size {
		return fmt.Errorf("toml: %w", err)
	}
	return fmt.Errorf("toml: line %d: %w", b.lines.At(off), err)
}

func (b *tomlBuilder) line(n *unstable.Node) int {
	return b.lines.At(int(n.Raw.Offset))
}

// add sets key in t to v, made as kind, and returns v.
func (b *tomlBuilder) add(t *core.Node, key string, v *core.Node, kind tomlKind) *core.Node {
	t.Set(key, v)
	b.made[v] = kind
	return v
}

func (b *tomlBuilder) expression(e *unstable.Node) error {
	switch e.Kind {
	case unstable.Table, unstable.ArrayTable:
		return b.header(e.Key(), e.Kind == unstable.ArrayTable)
	case unstable.KeyValue:
		return b.keyValue(b.table, b.depth, e)
	default:
		return nil
	}
}

// header walks a [table] or [[array table]] header from the root and makes
// its table the current one. A key that names an array of tables continues
// in its last table, as TOML says; that table is one level deeper.
func (b *tomlBuilder) header(keys unstable.Iterator, array bool) error {
	t, depth := b.root, 0
	for keys.Next() {
		k := keys.Node()
		name, line := string(k.Data), b.line(k)
		if depth++; depth > core.MaxDepth {
			return core.TooDeep(line)
		}
		child := t.Field[name]
		if keys.IsLast() && array {
			return b.arrayTable(t, child, name, line, depth)
		}
		kind := b.made[child]
		switch {
		case child == nil && keys.IsLast():
			child = b.add(t, name, core.NewMapping(line), tomlHeader)
		case child == nil:
			child = b.add(t, name, core.NewMapping(line), tomlImplicit)
		case keys.IsLast() && kind == tomlImplicit:
			b.made[child] = tomlHeader
		case keys.IsLast() || kind == tomlValue:
			return fmt.Errorf("line %d: %s is already defined, as %s", line, core.Clip(name), kind)
		case kind == tomlArray:
			child = child.Items[len(child.Items)-1]
			depth++
		}
		t = child
	}
	b.table, b.depth = t, depth
	return nil
}

// arrayTable adds a table to the array of tables name in t, or makes the
// array, and makes the new table the current one.
func (b *tomlBuilder) arrayTable(t, array *core.Node, name string, line, depth int) error {
	switch kind := b.made[array]; {
	case array == nil:
		array = b.add(t, name, &core.Node{Shape: core.Sequence, Line: line}, tomlArray)
	case kind != tomlArray:
		return fmt.Errorf("line %d: %s is already defined, as %s", line, core.Clip(name), kind)
	}
	if depth+1 > core.MaxDepth {
		return core.TooDeep(line)
	}
	b.table, b.depth = core.NewMapping(line), depth+1
	array.Items = append(array.Items, b.table)
	return nil
}

// keyValue sets a possibly dotted key = value in table t, at depth. A
// dotted key extends only a table another dotted key made.
func (b *tomlBuilder) keyValue(t *core.Node, depth int, kv *unstable.Node) error {
	keys := kv.Key()
	for keys.Next() {
		k := keys.Node()
		name, line := string(k.Data), b.line(k)
		if depth++; depth > core.MaxDepth {
			return core.TooDeep(line)
		}
		child := t.Field[name]
		switch {
		case child != nil && (keys.IsLast() || b.made[child] != tomlDotted):
			return fmt.Errorf("line %d: %s is already defined, as %s", line, core.Clip(name), b.made[child])
		case keys.IsLast():
			v, err := b.value(kv.Value(), line, depth)
			if err != nil {
				return err
			}
			t.Set(name, v)
		case child == nil:
			child = b.add(t, name, core.NewMapping(line), tomlDotted)
		}
		t = child
	}
	return nil
}

// value converts a value node at depth. An array has no byte range of its
// own, so it takes keyLine, the line of the key it is assigned to.
func (b *tomlBuilder) value(v *unstable.Node, keyLine, depth int) (*core.Node, error) {
	if depth > core.MaxDepth {
		return nil, core.TooDeep(keyLine)
	}
	switch v.Kind {
	case unstable.Array:
		n := &core.Node{Shape: core.Sequence, Line: keyLine}
		it := v.Children()
		for it.Next() {
			item, err := b.value(it.Node(), keyLine, depth+1)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, item)
		}
		return n, nil
	case unstable.InlineTable:
		n := core.NewMapping(b.line(v))
		it := v.Children()
		for it.Next() {
			if err := b.keyValue(n, depth, it.Node()); err != nil {
				return nil, err
			}
		}
		return n, nil
	default:
		if err := checkScalar(v.Kind, v.Data); err != nil {
			return nil, fmt.Errorf("line %d: %s is not a valid %s: %w", b.line(v), core.Clip(string(v.Data)), scalarNames[v.Kind], err)
		}
		return &core.Node{Type: tomlTypes[v.Kind], Text: string(v.Data), Line: b.line(v)}, nil
	}
}

// tomlTypes is the type a filter compares a TOML scalar as; a string, a date
// and a time are strings.
var tomlTypes = map[unstable.Kind]core.ScalarType{
	unstable.Integer: core.TypeNumber,
	unstable.Float:   core.TypeNumber,
	unstable.Bool:    core.TypeBool,
}

var scalarNames = map[unstable.Kind]string{
	unstable.Integer:       "integer",
	unstable.Float:         "float",
	unstable.LocalDate:     "local date",
	unstable.LocalTime:     "local time",
	unstable.LocalDateTime: "local datetime",
	unstable.DateTime:      "datetime",
}

// checkScalar refuses a number or a date that go-toml's decode refuses. Its
// parser checks the syntax of a number but only finds where a date ends, and
// leaves the rest to the decode: an integer outside 64 bits, a float out of
// range, and a date or a time that is malformed or does not exist.
func checkScalar(kind unstable.Kind, b []byte) error {
	switch kind {
	case unstable.Integer:
		return checkInteger(string(b))
	case unstable.Float:
		return checkFloat(string(b))
	case unstable.LocalDate:
		return new(toml.LocalDate).UnmarshalText(b)
	case unstable.LocalTime:
		return new(toml.LocalTime).UnmarshalText(b)
	case unstable.LocalDateTime:
		return new(toml.LocalDateTime).UnmarshalText(b)
	case unstable.DateTime:
		return checkDateTime(b)
	default: // a string or a bool, which the parser has read whole
		return nil
	}
}

var errRange = errors.New("it does not fit in 64 bits")

func checkInteger(s string) error {
	s = strings.ReplaceAll(s, "_", "")
	base := 10
	if len(s) > 2 && s[0] == '0' {
		base = map[byte]int{'x': 16, 'o': 8, 'b': 2}[s[1]]
		s = s[2:]
	}
	if _, err := strconv.ParseInt(s, base, 64); err != nil {
		return errRange
	}
	return nil
}

func checkFloat(s string) error {
	if t := strings.TrimLeft(s, "+-"); t == "inf" || t == "nan" {
		return nil
	}
	if _, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64); err != nil {
		return errRange
	}
	return nil
}

// checkDateTime checks a datetime with an offset: a local datetime, then Z
// or an offset of the form +hh:mm. The parser has found a Z, a + or a -
// after the date, so b is at least seven bytes long.
func checkDateTime(b []byte) error {
	if z := b[len(b)-1]; z == 'Z' || z == 'z' {
		return new(toml.LocalDateTime).UnmarshalText(b[:len(b)-1])
	}
	local, offset := b[:len(b)-6], b[len(b)-6:]
	hours, okH := twoDigits(offset[1:3])
	minutes, okM := twoDigits(offset[4:6])
	if (offset[0] != '+' && offset[0] != '-') || offset[3] != ':' || !okH || !okM || hours > 23 || minutes > 59 {
		return errors.New("the offset is not +hh:mm or -hh:mm")
	}
	return new(toml.LocalDateTime).UnmarshalText(local)
}

func twoDigits(b []byte) (int, bool) {
	ok := '0' <= b[0] && b[0] <= '9' && '0' <= b[1] && b[1] <= '9'
	return int(b[0]-'0')*10 + int(b[1]-'0'), ok
}
