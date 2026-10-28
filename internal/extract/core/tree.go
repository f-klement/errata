package core

import (
	"bytes"
	"fmt"
	"slices"
	"sort"

	"github.com/f-klement/errata/internal/check"
)

// Shape is what a node holds: one scalar, a mapping from keys to nodes, or a
// sequence of nodes.
type Shape int

// The shapes of a node.
const (
	Scalar Shape = iota
	Mapping
	Sequence
)

// String names a container shape in a message.
func (s Shape) String() string {
	if s == Mapping {
		return "a mapping"
	}
	return "a sequence"
}

// ScalarType is what a scalar is, as its file's format types it, which a
// filter compares by. A YAML timestamp, a !!binary, a scalar of a tag
// errata does not know and a TOML date or time are strings of the text the
// file wrote.
type ScalarType uint8

// The types of a scalar.
const (
	TypeString ScalarType = iota
	TypeNumber
	TypeBool
	TypeNull
)

// Node is one value of a parsed YAML, JSON or TOML file. All three formats
// are read into this tree, so one path evaluator serves them.
type Node struct {
	Shape Shape
	Type  ScalarType
	// Block is the line of the | of a YAML literal block scalar whose lines
	// are the file's lines from the line after it on, so line k of a
	// document the string holds is line Block+k of the file; 0 for any
	// other node. It is not always Line, which is where an anchor or a tag
	// written on the line above the | stands. The yaml reader sets it on a
	// scalar written with |, unless it holds an LS or a PS, which yaml.v3
	// breaks the file's lines on and keeps in the text. An int32 sits beside
	// Type in the space Text's alignment leaves, so the node is no larger.
	Block int32
	Text  string // the scalar's string form
	Line  int    // where the value starts, counted from 1
	Keys  []string
	Field map[string]*Node
	Vals  []*Node // field's values in the order of keys
	Items []*Node
	num   *number // a number scalar's value, once a filter has compared it
}

// number is the value of a number scalar, and whether it has one.
type number struct {
	d  decimal
	ok bool
}

// NewMapping is an empty mapping that starts at line.
func NewMapping(line int) *Node {
	return &Node{Shape: Mapping, Line: line, Field: map[string]*Node{}}
}

// Set adds key to a mapping, keeping the order keys were first written in.
// The readers never set a key twice, since each refuses a repeated key, so
// replacing a value takes a search through the keys.
func (n *Node) Set(key string, v *Node) {
	if _, ok := n.Field[key]; ok {
		n.Vals[slices.Index(n.Keys, key)] = v
	} else {
		n.Keys = append(n.Keys, key)
		n.Vals = append(n.Vals, v)
	}
	n.Field[key] = v
}

// Lines maps a byte offset in a file to its line, from an index of every
// line's start. TOML uses it, since the node limit counts each newline of a
// TOML file as a node; JSON and regex use a lineCursor.
type Lines []int

// LineIndex is the index of the lines of data.
func LineIndex(data []byte) Lines {
	l := Lines{0}
	for i, b := range data {
		if b == '\n' {
			l = append(l, i+1)
		}
	}
	return l
}

// At returns the line, counted from 1, that holds byte offset off.
func (l Lines) At(off int) int {
	return sort.SearchInts(l, off+1)
}

// Values evaluates p on roots and turns the nodes it selects into values. A
// path that selects a mapping or a sequence is an error: its string form says
// nothing a rule can compare, and returning nothing would let allow_empty
// hide a wrong path. So is a path that selects more than maxValues nodes, and
// one whose values hold more bytes in all than the extractor's max_bytes,
// since every value is copied, normalised and quoted downstream. A value is
// mostly a part of the file, so an ordinary file never meets that limit.
// Two things can make the values longer than the file: YAML aliases and
// merge keys, which repeat a value without the file growing, and decoding,
// which can lengthen one: an escape such as YAML's \L, two bytes for a
// character of three, or a byte of invalid UTF-8 that becomes U+FFFD.
func (s Source) Values(p Path, roots ...*Node) ([]check.Value, error) {
	return s.SharedValues(p, &Spent{}, roots...)
}

// SharedValues is Values with the filter work of spent, the work the files
// of a set read before spent: a set's filters share MaxFilterBytes.
func (s Source) SharedValues(p Path, spent *Spent, roots ...*Node) ([]check.Value, error) {
	return s.EmbeddedValues(p, Embedding{}, Reading{}, spent, roots...)
}

// LastLine is the line data ends on, counted from 1 as errata counts a
// file's lines, by its \n.
func LastLine(data []byte) int {
	return bytes.Count(data, newline) + 1
}

// values turns the nodes a reading selected into values, each at the line
// of the file its lineMap gives, as Values says. Their bytes are held to
// the reading's limit: the file's max_bytes, or its set's, since a set
// holds all its files' values to max_bytes, not each to the bytes the
// files before it left.
func (r *reading) values(nodes []*Node, at []lineMap) ([]check.Value, error) {
	var total int64
	for i, n := range nodes {
		if n.Shape != Scalar {
			return nil, fmt.Errorf("%s: %s selects %s at line %d, not a scalar; add [*] to take its members", r.File, r.chain, n.Shape, r.line(at[i], n.Line))
		}
		if total += int64(len(n.Text)); total > r.limit {
			return nil, fmt.Errorf("%s: the values the path selects hold more than %d bytes in all, the extractor's max_bytes; aliases can repeat a value and decoding can lengthen one, so narrow the path, or set max_bytes: higher", r.File, r.limit)
		}
	}
	out := make([]check.Value, 0, len(nodes))
	for i, n := range nodes {
		out = append(out, check.Value{Text: n.Text, At: check.Location{File: r.File, Line: r.line(at[i], n.Line)}})
	}
	return out, nil
}

// LineCursor gives the line of an offset in data by counting the newlines
// between it and the offset asked before, so offsets in file order take one
// pass over the file and no memory for each line.
type LineCursor struct {
	data []byte
	off  int // the offset asked before
	line int // its line, counted from 1
}

// NewLineCursor is a cursor at the start of data, on line 1.
func NewLineCursor(data []byte) *LineCursor {
	return &LineCursor{data: data, line: 1}
}

// At returns the line, counted from 1, that holds byte offset off. An
// offset before the one asked before counts back.
func (c *LineCursor) At(off int) int {
	if off >= c.off {
		c.line += bytes.Count(c.data[c.off:off], newline)
	} else {
		c.line -= bytes.Count(c.data[off:c.off], newline)
	}
	c.off = off
	return c.line
}

// newline is a line break, as bytes.Count takes it.
var newline = []byte("\n")

// treeExtractor reads a path in a JSON or TOML file.
type treeExtractor struct {
	Source
	path     Path
	embedded Embedding
	format   *Format
	parse    func([]byte) (*Node, error)
}

// Tree is what a JSON or TOML spec says of where it reads: its file or its
// files, its path and its embedded steps, with its max_bytes.
type Tree struct {
	File     string
	Files    []string
	Path     string
	Embedded []Embedded
	MaxBytes int64
}

// BuildTree checks t, the spec of a JSON or TOML extractor of kind, and
// returns its extractor: one that refuses a file denser than f allows, then
// parses it with parse, evaluates the path on the tree and reads each
// embedded step, in the format it names, with parsers, for the file or for
// each of the files (BuildOneOrSet).
func BuildTree(kind string, t Tree, f *Format, parse func([]byte) (*Node, error), parsers Parsers) (check.Extractor, error) {
	return BuildOneOrSet(kind, t.File, t.Files, t.MaxBytes, func(file string, maxBytes int64, _ bool) (FileReader, error) {
		src, err := CheckSource(kind, file, maxBytes)
		if err != nil {
			return nil, err
		}
		parsed, err := CheckPath(kind, t.Path)
		if err != nil {
			return nil, err
		}
		embedded, err := CheckEmbedded(kind, t.Embedded, parsers)
		if err != nil {
			return nil, err
		}
		return treeExtractor{Source: src, path: parsed, embedded: embedded, format: f, parse: parse}, nil
	})
}

// Extract reads the file under root and returns every scalar its path selects.
func (e treeExtractor) Extract(root string) ([]check.Value, error) {
	data, err := e.Read(root)
	if err != nil {
		return nil, err
	}
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e treeExtractor) FromData(data []byte) ([]check.Value, error) {
	return e.FromShared(data, &Spent{})
}

// FromShared is FromData with the filter work the files of a set read
// before it have spent.
func (e treeExtractor) FromShared(data []byte, spent *Spent) ([]check.Value, error) {
	if err := e.CheckNodes(data, e.format); err != nil {
		return nil, err
	}
	tree, err := e.parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	vs, err := e.EmbeddedValues(e.path, e.embedded, Reading{Last: LastLine(data)}, spent, tree)
	return vs, spent.SharedAliases(err)
}
