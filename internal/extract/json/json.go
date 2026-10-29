// Package json is the json extractor: it reads a path in a JSON file, from
// the tree of core.Node the file parses into.
package json

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads a path in a JSON file, File, or in each file Files names, a
// file or a glob (core.Files). Embedded reads a path in the document each
// string the path selects holds, and so on, step by step, as core.Embedded
// says.
type Spec struct {
	File     string          `json:"file,omitempty"`
	Files    []string        `json:"files,omitempty"`
	Path     string          `json:"path"`
	Embedded []core.Embedded `json:"embedded,omitempty"`
	MaxBytes int64           `json:"max_bytes,omitempty"`
	// As is map to hand assert each mapping the path selects whole, or
	// record to read Fields from each node it selects into one, as
	// core.CheckAs says; "" for each scalar.
	As string `json:"as,omitempty"`
	// Fields names each path read from a selected node into its record,
	// for as: record, as core.CheckFields says.
	Fields map[string]string `json:"fields,omitempty"`
}

// Kind is the json kind, which a rule writes under the key json.
var Kind = core.Kind[Spec]{Key: "json", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: its file or files as
// core.PlaceFiles writes them, and no max_bytes, since a limit does not
// change what is read.
func Place(s Spec) Spec {
	s.File, s.Files = core.PlaceFiles(s.File, s.Files)
	s.MaxBytes = 0
	return s
}

// Resolve is s with its max_bytes written out, and its files, embedded
// steps and fields copied, for errata list.
func Resolve(s Spec) Spec {
	s.Files, s.Embedded, s.Fields = slices.Clone(s.Files), slices.Clone(s.Embedded), maps.Clone(s.Fields)
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// Describe is what s's extractor returns: literal values, each flat, a
// mapping or a record as its as key says.
func Describe(s Spec) check.Description {
	shape, fields := core.TreeShape(s.As, s.Fields)
	return check.Description{Label: "json", Shape: shape, Fields: fields}
}

// Build checks s and returns its extractor, which reads the documents
// embedded names with p.
func Build(s Spec, p core.Parsers) (check.Extractor, error) {
	return core.BuildTree("json", core.Tree{File: s.File, Files: s.Files, Path: s.Path, Embedded: s.Embedded, MaxBytes: s.MaxBytes, As: s.As, Fields: s.Fields}, core.JSONFormat, Parse, p)
}

// Parse reads one JSON value into a tree. encoding/json's Decoder.Token
// gives the tokens and InputOffset the byte after each one, which is enough to
// find where every value starts. A number keeps the text the file wrote, so
// 1e3 stays 1e3. An object that repeats a key is an error, since JSON readers
// disagree on which of the two counts.
func Parse(data []byte) (*core.Node, error) {
	r := jsonReader{dec: json.NewDecoder(bytes.NewReader(data)), data: data, lines: core.NewLineCursor(data)}
	r.dec.UseNumber()
	root, err := r.value(0)
	if err != nil {
		return nil, err
	}
	if _, err := r.dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("line %d: data after the top-level value", r.lines.At(r.start()))
	}
	return root, nil
}

type jsonReader struct {
	dec   *json.Decoder
	data  []byte
	lines *core.LineCursor // every offset it is asked comes after the one before
}

// start returns the offset of the next token: the decoder's offset after the
// previous token, past the white space and the , or : that separate tokens.
func (r *jsonReader) start() int {
	i := int(r.dec.InputOffset())
	for i < len(r.data) {
		switch r.data[i] {
		case ' ', '\t', '\n', '\r', ',', ':':
			i++
		default:
			return i
		}
	}
	return i
}

// token is the decoder's next token. The file ending before a value is
// complete is an error here, since parseJSON reads only the one value.
func (r *jsonReader) token() (json.Token, error) {
	tok, err := r.dec.Token()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, errors.New("the file ends before the JSON value is complete")
	}
	return tok, err
}

func (r *jsonReader) value(depth int) (*core.Node, error) {
	line := r.lines.At(r.start())
	if depth > core.MaxDepth {
		return nil, core.TooDeep(line)
	}
	tok, err := r.token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return r.object(line, depth)
		}
		return r.array(line, depth)
	case string:
		return &core.Node{Text: t, Line: line}, nil
	case json.Number:
		return &core.Node{Type: core.TypeNumber, Text: string(t), Line: line}, nil
	case bool:
		return &core.Node{Type: core.TypeBool, Text: strconv.FormatBool(t), Line: line}, nil
	default: // nil, the JSON null
		return &core.Node{Type: core.TypeNull, Text: "null", Line: line}, nil
	}
}

func (r *jsonReader) object(line, depth int) (*core.Node, error) {
	n := core.NewMapping(line)
	for r.dec.More() {
		keyLine := r.lines.At(r.start())
		tok, err := r.token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string) // Token returns only a string in key position
		if _, dup := n.Field[key]; dup {
			return nil, fmt.Errorf("line %d: key %s is repeated", keyLine, core.Quote(key))
		}
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		n.Set(key, v)
	}
	_, err := r.token() // the closing }
	return n, err
}

func (r *jsonReader) array(line, depth int) (*core.Node, error) {
	n := &core.Node{Shape: core.Sequence, Line: line}
	for r.dec.More() {
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, v)
	}
	_, err := r.token() // the closing ]
	return n, err
}
