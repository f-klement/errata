package extract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// parseJSON reads one JSON value into a tree. encoding/json's Decoder.Token
// gives the tokens and InputOffset the byte after each one, which is enough to
// find where every value starts. A number keeps the text the file wrote, so
// 1e3 stays 1e3. An object that repeats a key is an error, since JSON readers
// disagree on which of the two counts.
func parseJSON(data []byte) (*node, error) {
	r := jsonReader{dec: json.NewDecoder(bytes.NewReader(data)), data: data, lines: newLineCursor(data)}
	r.dec.UseNumber()
	root, err := r.value(0)
	if err != nil {
		return nil, err
	}
	if _, err := r.dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("line %d: data after the top-level value", r.lines.at(r.start()))
	}
	return root, nil
}

type jsonReader struct {
	dec   *json.Decoder
	data  []byte
	lines *lineCursor // every offset it is asked comes after the one before
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

func (r *jsonReader) value(depth int) (*node, error) {
	line := r.lines.at(r.start())
	if depth > maxDepth {
		return nil, tooDeep(line)
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
		return &node{text: t, line: line}, nil
	case json.Number:
		return &node{typ: typeNumber, text: string(t), line: line}, nil
	case bool:
		return &node{typ: typeBool, text: strconv.FormatBool(t), line: line}, nil
	default: // nil, the JSON null
		return &node{typ: typeNull, text: "null", line: line}, nil
	}
}

func (r *jsonReader) object(line, depth int) (*node, error) {
	n := newMapping(line)
	for r.dec.More() {
		keyLine := r.lines.at(r.start())
		tok, err := r.token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string) // Token returns only a string in key position
		if _, dup := n.field[key]; dup {
			return nil, fmt.Errorf("line %d: key %s is repeated", keyLine, quote(key))
		}
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		n.set(key, v)
	}
	_, err := r.token() // the closing }
	return n, err
}

func (r *jsonReader) array(line, depth int) (*node, error) {
	n := &node{shape: sequence, line: line}
	for r.dec.More() {
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, v)
	}
	_, err := r.token() // the closing ]
	return n, err
}
