package extract

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/f-klement/errata/internal/check"
)

// shape is what a node holds: one scalar, a mapping from keys to nodes, or a
// sequence of nodes.
type shape int

const (
	scalar shape = iota
	mapping
	sequence
)

// String names a container shape in a message.
func (s shape) String() string {
	if s == mapping {
		return "a mapping"
	}
	return "a sequence"
}

// scalarType is what a scalar is, as its file's format types it, which a
// filter compares by. A YAML timestamp, a !!binary, a scalar of a tag
// errata does not know and a TOML date or time are strings of the text the
// file wrote.
type scalarType uint8

const (
	typeString scalarType = iota
	typeNumber
	typeBool
	typeNull
)

// node is one value of a parsed YAML, JSON or TOML file. All three formats
// are read into this tree, so one path evaluator serves them.
type node struct {
	shape shape
	typ   scalarType
	text  string // the scalar's string form
	line  int    // where the value starts, counted from 1
	keys  []string
	field map[string]*node
	vals  []*node // field's values in the order of keys
	items []*node
	num   *number // a number scalar's value, once a filter has compared it
}

// number is the value of a number scalar, and whether it has one.
type number struct {
	d  decimal
	ok bool
}

func newMapping(line int) *node {
	return &node{shape: mapping, line: line, field: map[string]*node{}}
}

// set adds key to a mapping, keeping the order keys were first written in.
// The readers never set a key twice, since each refuses a repeated key, so
// replacing a value takes a search through the keys.
func (n *node) set(key string, v *node) {
	if _, ok := n.field[key]; ok {
		n.vals[slices.Index(n.keys, key)] = v
	} else {
		n.keys = append(n.keys, key)
		n.vals = append(n.vals, v)
	}
	n.field[key] = v
}

// lines maps a byte offset in a file to its line, from an index of every
// line's start. TOML uses it, since the node limit counts each newline of a
// TOML file as a node; JSON and regex use a lineCursor.
type lines []int

func lineIndex(data []byte) lines {
	l := lines{0}
	for i, b := range data {
		if b == '\n' {
			l = append(l, i+1)
		}
	}
	return l
}

// at returns the line, counted from 1, that holds byte offset off.
func (l lines) at(off int) int {
	return sort.SearchInts(l, off+1)
}

// values evaluates p on roots and turns the nodes it selects into values. A
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
func (s source) values(p path, roots ...*node) ([]check.Value, error) {
	nodes, err := p.eval(roots...)
	switch {
	case errors.Is(err, errTooManyValues):
		return nil, tooManyValues(s.file)
	case err != nil:
		return nil, fmt.Errorf("%s: path %s: %w", s.file, quote(p.text), err)
	}
	var total int64
	for _, n := range nodes {
		if n.shape != scalar {
			return nil, fmt.Errorf("%s: path %s selects %s at line %d, not a scalar; add [*] to take its members", s.file, quote(p.text), n.shape, n.line)
		}
		if total += int64(len(n.text)); total > s.limit {
			return nil, fmt.Errorf("%s: the values the path selects hold more than %d bytes in all, the extractor's max_bytes; aliases can repeat a value and decoding can lengthen one, so narrow the path, or set max_bytes: higher", s.file, s.limit)
		}
	}
	out := make([]check.Value, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, check.Value{Text: n.text, At: check.Location{File: s.file, Line: n.line}})
	}
	return out, nil
}

// lineCursor gives the line of an offset in data by counting the newlines
// between it and the offset asked before, so offsets in file order take one
// pass over the file and no memory for each line.
type lineCursor struct {
	data []byte
	off  int // the offset asked before
	line int // its line, counted from 1
}

func newLineCursor(data []byte) *lineCursor {
	return &lineCursor{data: data, line: 1}
}

// at returns the line, counted from 1, that holds byte offset off. An
// offset before the one asked before counts back.
func (c *lineCursor) at(off int) int {
	if off >= c.off {
		c.line += bytes.Count(c.data[c.off:off], newline)
	} else {
		c.line -= bytes.Count(c.data[off:c.off], newline)
	}
	c.off = off
	return c.line
}

var newline = []byte("\n")
