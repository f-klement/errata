package core

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/f-klement/errata/internal/check"
)

// Embedded is one step into a document a string holds, as a rule writes it
// under the embedded key of a yaml, json or toml extractor: the format the
// string is parsed in and the path read in the document. Gatus's config.yaml
// in a ConfigMap's data, or a runner's config.toml in a Helm value, is such a
// document.
type Embedded struct {
	Format string `json:"format"`
	Path   string `json:"path"`
}

// MaxEmbedded is how many steps embedded takes at most: a document in a
// string of a document in a string, and so on, four deep. The deepest real
// nesting found is two, a JSON value in an env var of a manifest in a Helm
// value. A config that writes more does not load, so a message names a
// chain a reader can follow.
const MaxEmbedded = 4

// Embedding is an extractor's embedded steps, checked, with the parsers each
// step's format is read with.
type Embedding struct {
	steps []embedStep
}

type embedStep struct {
	format string
	f      *Format
	path   Path
	parse  func(data []byte, expanded int) ([]*Node, int, error)
}

// CheckEmbedded checks the embedded steps of a kind's extractor: at most
// MaxEmbedded of them, each with a format errata reads and a path that
// parses. p are the parsers the formats are read with.
func CheckEmbedded(kind string, steps []Embedded, p Parsers) (Embedding, error) {
	if len(steps) > MaxEmbedded {
		return Embedding{}, fmt.Errorf("%s: embedded takes %d steps, over the limit of %d", kind, len(steps), MaxEmbedded)
	}
	var e Embedding
	for i, s := range steps {
		at := fmt.Sprintf("%s: embedded[%d]", kind, i)
		st, err := p.step(at, s.Format)
		if err != nil {
			return Embedding{}, err
		}
		if st.path, err = CheckPath(at, s.Path); err != nil {
			return Embedding{}, err
		}
		e.steps = append(e.steps, st)
	}
	return e, nil
}

// step is the step that reads format, with its node limit and its parser.
// An embedded YAML string is read whole, so it holds one document: picking
// one of several would compare the wrong one, as for a file (yaml.Spec).
func (p Parsers) step(at, format string) (embedStep, error) {
	if format == "yaml" {
		return embedStep{format: format, f: YAMLFormat, parse: func(data []byte, expanded int) ([]*Node, int, error) {
			docs, expanded, err := p.YAML(data, expanded)
			if err == nil && len(docs) > 1 {
				err = fmt.Errorf("holds %d documents; an embedded YAML document is read whole, so it holds one", len(docs))
			}
			return docs, expanded, err
		}}, nil
	}
	if format == "json" {
		parse := one(p.JSON)
		return embedStep{format: format, f: JSONFormat, parse: func(data []byte, expanded int) ([]*Node, int, error) {
			if len(bytes.Trim(data, " \t\r\n")) == 0 {
				return nil, expanded, errors.New("the string holds nothing but white space, and no JSON value")
			}
			return parse(data, expanded)
		}}, nil
	}
	if format == "toml" {
		return embedStep{format: format, f: TOMLFormat, parse: one(p.TOML)}, nil
	}
	if format == "" {
		return embedStep{}, fmt.Errorf("%s: format is required; write yaml, json or toml", at)
	}
	return embedStep{}, fmt.Errorf("%s: format %s is not one errata reads; write yaml, json or toml", at, Quote(format))
}

// one is a parser of one document, as a step calls it. JSON and TOML have
// no aliases, so the count of what aliases add passes through.
func one(parse func([]byte) (*Node, error)) func([]byte, int) ([]*Node, int, error) {
	return func(data []byte, expanded int) ([]*Node, int, error) {
		root, err := parse(data)
		return []*Node{root}, expanded, err
	}
}

// reading is one run of an extractor's path and embedded steps over its
// file. The bounds a file has are counted over every document its strings
// hold, together: their bytes and the nodes they can hold against max_bytes,
// as for one more file, and the nodes YAML aliases and merge keys add, the
// filters' work and the values against the file's own counts.
type reading struct {
	Source
	Reading
	chain    string // every path of the reading, for a message
	m        meter
	expanded int   // the nodes YAML aliases and merge keys added so far
	bytes    int64 // the bytes of the documents in strings parsed so far
	nodes    int64 // the nodes they can hold, each counted as its format's bytes per node
	limit    int64 // what bytes and nodes may reach: the file's max_bytes, or its set's
}

// lineMap gives the line of the file a line of a document stands at. A
// document in a YAML literal block scalar keeps its lines, so its line k is
// the file's line offset+k. Any other string folds or escapes its lines, so
// every line of its document is reported at fixed, the line where the
// string starts. The zero lineMap is the file's own.
type lineMap struct {
	offset int
	fixed  int
}

// line is the file's line for line k of the document.
func (m lineMap) line(k int) int {
	if m.fixed != 0 {
		return m.fixed
	}
	return m.offset + k
}

// into is the lineMap of the document the string n holds, n being a node
// of the document m maps.
func (m lineMap) into(n *Node) lineMap {
	if m.fixed == 0 && n.Block != 0 {
		return lineMap{offset: m.offset + int(n.Block)}
	}
	return lineMap{fixed: m.line(n.Line)}
}

// Reading is what a reader knows of its file that a reading needs: the
// line it ends on, which no line a value is reported at passes. A zero Last
// reports every line as the tree has it.
type Reading struct {
	Last int
}

// line is the file's line for line k of the document m maps, kept inside
// the file. A block scalar's lines are yaml.v3's, which also breaks a line
// on a CR, a NEL, an LS or a PS, so in a file with such breaks before it a
// value could otherwise land below the file's last line.
func (r *reading) line(m lineMap, k int) int {
	if r.Last == 0 {
		return m.line(k)
	}
	return min(m.line(k), r.Last)
}

// EmbeddedValues evaluates p on roots, then reads each of e's steps in turn
// in the strings the step before selected, and turns what the last selects
// into values, as Values does. f is what the reader knows of the file, as
// Reading says. spent is the work the file's own parse and the files of a
// set read before it spent: the reading starts the nodes YAML aliases and
// merge keys add and the filters' work at it, and leaves it at what they
// reach, so the documents in strings and the files of a set share them.
// A node a step reads that is not a string, a string that does not parse
// in the step's format, and a document past a bound are errors, each
// naming every path of the reading. A value is at the line of the file it
// stands at when every string on its way is a YAML literal block scalar,
// and at the line where the outermost string that is not starts otherwise.
func (s Source) EmbeddedValues(p Path, e Embedding, f Reading, spent *Spent, roots ...*Node) ([]check.Value, error) {
	r := &reading{Source: s, Reading: f, chain: "path " + Quote(p.text), m: meter(spent.Filter), expanded: spent.Expanded,
		bytes: spent.stringBytes, nodes: spent.stringNodes, limit: s.Limit}
	if spent.stringLimit != 0 {
		r.limit = spent.stringLimit
	}
	defer func() {
		spent.Filter, spent.Expanded = int64(r.m), r.expanded
		spent.stringBytes, spent.stringNodes = r.bytes, r.nodes
	}()
	for _, st := range e.steps {
		r.chain += " then " + st.format + " path " + Quote(st.path.text)
	}
	nodes, err := r.eval(p, roots)
	if err != nil {
		return nil, err
	}
	at := make([]lineMap, len(nodes))
	for _, st := range e.steps {
		if nodes, at, err = r.step(st, nodes, at); err != nil {
			return nil, err
		}
	}
	return r.values(nodes, at)
}

// eval evaluates p on roots against the reading's filter budget.
func (r *reading) eval(p Path, roots []*Node) ([]*Node, error) {
	nodes, err := p.eval(&r.m, roots)
	if errors.Is(err, ErrTooManyValues) {
		return nil, TooManyValues(r.File)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", r.File, r.chain, err)
	}
	return nodes, nil
}

// step parses the document each of nodes holds in st's format and returns
// what st's path selects in them, each with the lineMap of its document.
func (r *reading) step(st embedStep, nodes []*Node, at []lineMap) ([]*Node, []lineMap, error) {
	var next []*Node
	var nextAt []lineMap
	for i, n := range nodes {
		line := r.line(at[i], n.Line)
		if n.Shape != Scalar || n.Type != TypeString {
			return nil, nil, fmt.Errorf("%s: %s: the value at line %d is %s, not a string that holds a %s document", r.File, r.chain, line, describe(n), st.format)
		}
		docs, err := r.parse(st, n.Text, line)
		if err != nil {
			return nil, nil, err
		}
		sel, err := r.eval(st.path, docs)
		if err != nil {
			return nil, nil, err
		}
		inner := at[i].into(n)
		for range sel {
			nextAt = append(nextAt, inner)
		}
		if next = append(next, sel...); len(next) > MaxValues {
			return nil, nil, TooManyValues(r.File)
		}
	}
	return next, nextAt, nil
}

// parse reads text, the string at line, as a document in st's format,
// after counting its bytes and the nodes it can hold against the reading's
// bounds.
func (r *reading) parse(st embedStep, text string, line int) ([]*Node, error) {
	if r.bytes += int64(len(text)); r.bytes > r.limit {
		return nil, fmt.Errorf("%s: %s: the documents in strings hold more than %d bytes in all, the extractor's max_bytes; aliases can repeat a string, so narrow the paths, or set max_bytes: higher", r.File, r.chain, r.limit)
	}
	data := []byte(text)
	if r.nodes += int64(st.f.CountNodes(data)) * st.f.BytesPerNode; r.nodes > r.limit {
		return nil, fmt.Errorf("%s: %s: the documents in strings can hold more nodes in all than max_bytes allows, one for every %d bytes of it in %s; set max_bytes: higher to read documents this dense", r.File, r.chain, st.f.BytesPerNode, st.format)
	}
	docs, expanded, err := st.parse(data, r.expanded)
	r.expanded = expanded
	if err != nil {
		return nil, fmt.Errorf("%s: %s: the %s document in the string at line %d: %w", r.File, r.chain, st.format, line, err)
	}
	return docs, nil
}

// describe names what n is, for the message that refuses to read a
// document in it.
func describe(n *Node) string {
	if n.Shape != Scalar {
		return n.Shape.String()
	}
	if n.Type == TypeNumber {
		return "a number"
	}
	if n.Type == TypeBool {
		return "a bool"
	}
	return "null"
}
