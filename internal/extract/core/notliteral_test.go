package core

import (
	"strings"
	"testing"
)

// A scalar the file only computes makes a value marked not literal, and a
// filter that would compare it refuses, naming its text and line, while
// one that only tests it exists, and a comparison of a literal beside it,
// read as ever. Planted breaks: values leaving NotLiteral out; holds
// comparing the node's text.
func TestNotLiteralNodes(t *testing.T) {
	computed := &Node{Shape: Scalar, Text: "os.environ['X']", Line: 3, NotLiteral: true}
	literal := &Node{Shape: Scalar, Text: "api", Line: 4}
	item := NewMapping(2)
	item.Set("name", computed)
	other := NewMapping(4)
	other.Set("name", literal)
	root := NewMapping(1)
	root.Set("a", computed)
	root.Set("list", &Node{Shape: Sequence, Line: 2, Items: []*Node{item, other}})
	src := Source{File: "f.py", Limit: DefaultMaxBytes}
	path := func(s string) Path {
		t.Helper()
		p, err := parsePath(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	vs, err := src.Values(path("a"), root)
	if err != nil || len(vs) != 1 || !vs[0].NotLiteral || vs[0].Text != "os.environ['X']" || vs[0].At.Line != 3 {
		t.Errorf("a: %+v, %v", vs, err)
	}
	_, err = src.Values(path("list[?@.name == 'api'].name"), root)
	if err == nil || !strings.Contains(err.Error(), `f.py: path "list[?@.name == 'api'].name": a filter compares "os.environ['X']" at line 3, a value the file only computes`) {
		t.Errorf("a filter comparing a computed value: %v", err)
	}
	vs, err = src.Values(path("list[?@.name].name"), root)
	if err != nil || len(vs) != 2 || !vs[0].NotLiteral || vs[1].NotLiteral {
		t.Errorf("an existence test: %+v, %v", vs, err)
	}
}
