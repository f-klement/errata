package core

import (
	"errors"
	"testing"
)

// Member walks mappings only: a key that is absent, a scalar or a sequence
// on the way, and a nil node all give nil. The planted break: Member
// without the nil check, which panics on a key below a missing one.
func TestMember(t *testing.T) {
	leaf := &Node{Shape: Scalar, Text: "1", Line: 3}
	inner := NewMapping(2)
	inner.Set("b", leaf)
	root := NewMapping(1)
	root.Set("a", inner)
	root.Set("s", &Node{Shape: Sequence, Items: []*Node{leaf}})
	for _, c := range []struct {
		keys []string
		want *Node
	}{
		{nil, root}, {[]string{"a"}, inner}, {[]string{"a", "b"}, leaf},
		{[]string{"a", "c"}, nil}, {[]string{"a", "b", "c"}, nil}, {[]string{"s", "0"}, nil},
	} {
		if got := root.Member(c.keys...); got != c.want {
			t.Errorf("%q: %v, want %v", c.keys, got, c.want)
		}
	}
	var none *Node
	if none.Member("a") != nil {
		t.Error("a nil node has a member")
	}
}

// ParseManifest refuses a file past the node limit before it parses, names
// the reader errata lacks, drops a byte order mark only when asked, and
// returns the parser's error as the parser gave it.
func TestParseManifest(t *testing.T) {
	var seen []byte
	parse := func(data []byte) (*Node, error) {
		seen = data
		if string(data) == "bad" {
			return nil, errors.New("does not parse")
		}
		return NewMapping(1), nil
	}
	s := Source{File: "d/package.json", Limit: 1 << 20}
	if _, err := s.ParseManifest([]byte(BOM+"{}"), "package.json", JSONFormat, parse, true); err != nil || string(seen) != "{}" {
		t.Errorf("skipBOM: %v, parsed %q", err, seen)
	}
	if _, err := s.ParseManifest([]byte(BOM+"{}"), "package.json", JSONFormat, parse, false); err != nil || string(seen) != BOM+"{}" {
		t.Errorf("keeping the mark: %v, parsed %q", err, seen)
	}
	if _, err := s.ParseManifest([]byte("bad"), "package.json", JSONFormat, parse, false); err == nil || err.Error() != "does not parse" {
		t.Errorf("parser's error: %v", err)
	}
	if _, err := s.ParseManifest([]byte("{}"), "Cargo.toml", TOMLFormat, nil, false); err == nil || err.Error() != "errata was built without the reader for Cargo.toml" {
		t.Errorf("no parser: %v", err)
	}
	seen = nil
	dense := Source{File: "d/package.json", Limit: 8}
	_, err := dense.ParseManifest([]byte("[1,2,3]"), "package.json", JSONFormat, parse, false)
	if !errors.Is(err, ErrTooDense) || seen != nil {
		t.Errorf("over the node limit: %v, parsed %q", err, seen)
	}
}
