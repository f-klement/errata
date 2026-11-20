package wire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A message is read strictly: it is valid UTF-8 and one JSON value, holds
// no null, no member the protocol does not define, none in another case
// than the protocol's and none twice, and holds every member it requires.
// Planted breaks: each check of Decode, strict, exactNames and present
// dropped in turn.
func TestDecodeIsStrict(t *testing.T) {
	for raw, want := range map[string]string{
		"{\"values\": [], \"x\": \"\xff\"}": "not valid UTF-8",
		`{"values": []} {}`:                 "more than one JSON value",
		``:                                  "empty",
		`{"values": [{"file": "a", "line": 1, "key": null}]}`: "holds null",
		`{"values": [], "x": 1}`:                              `unknown field "x"`,
		`{"values": [{"file": "a"}]}`:                         `the member "line" is missing`,
		`{"values": [], "error": "e"}`:                        "exactly one of values, error and no_value",
		`{}`:                                                  "exactly one of values, error and no_value",
		`{"values": [{"file": "a", "line": 1, "data": {"line": 1}}]}`:                            "exactly one of scalar, map and list",
		`{"values": [{"file": "a", "line": 1, "data": {"line": 1, "scalar": "x", "list": []}}]}`: "exactly one of scalar, map and list",
		`{"values": [{"file": "a", "line": 1, "data": {"scalar": "x"}}]}`:                        `the member "line" is missing`,
		`{"Values": []}`: `the member "Values" must be written "values"`,
		`{"values": [{"File": "a", "line": 1, "text": "x"}]}`:            `the member "File" must be written "file"`,
		`{"values": [], "values": []}`:                                   `the member "values" is written twice`,
		`{"values": [{"file": "a", "line": 1, "text": "x", "line": 2}]}`: `the member "line" is written twice`,
		`[]`: "an object is required",
	} {
		var r ExtractReply
		if err := Decode([]byte(raw), &r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %s", raw, err, want)
		}
	}
	var r ExtractReply
	if err := Decode([]byte(`{"values": [{"text": "1", "file": "a", "line": 2, "key": "k"}]}`), &r); err != nil || *r.Values[0].Key != "k" {
		t.Fatalf("a good reply: %+v, %v", r, err)
	}
}

// A build result holds error alone, or spec and description. Planted
// breaks: an error result with a spec accepted; a result with no
// description accepted.
func TestABuiltResultIsErrorOrSpec(t *testing.T) {
	desc := `"description": {"label": "l", "not_literal": false, "shape": "flat", "fields": [], "reads": []}`
	for raw, ok := range map[string]bool{
		`{"error": "e"}`:                              true,
		`{"error": "e", "spec": {}}`:                  false,
		`{"spec": {}, ` + desc + `}`:                  true,
		`{"spec": {}}`:                                false,
		`{"spec": {}, "description": {"label": "l"}}`: false,
	} {
		var b Built
		if err := Decode([]byte(raw), &b); (err == nil) != ok {
			t.Errorf("%s: got %v, want ok %v", raw, err, ok)
		}
	}
}

// The members every version shares read alone: protocol or protocols,
// never both or neither, with name and version. Planted breaks: both
// accepted; name not required.
func TestReadVersion(t *testing.T) {
	v, err := ReadVersion([]byte(`{"protocols": [2, 3], "name": "csv", "version": "0.4.0", "anything": 1}`))
	if err != nil || v.Chose() || len(v.Protocols) != 2 {
		t.Fatalf("protocols: %+v, %v", v, err)
	}
	if v, err := ReadVersion([]byte(`{"protocol": 1, "name": "csv", "version": "0.4.0"}`)); err != nil || !v.Chose() {
		t.Fatalf("protocol: %+v, %v", v, err)
	}
	for _, raw := range []string{`{"protocol": 1, "protocols": [1], "name": "a", "version": "1"}`, `{"name": "a", "version": "1"}`, `{"protocol": 1, "version": "1"}`} {
		if _, err := ReadVersion([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
}

// A node and a reply write and read back the same, an empty values reply
// writes values, and a node with no kind writes an empty list. Planted
// breaks: Node.MarshalJSON writing an empty map as no member; ExtractReply
// writing nil values as null.
func TestNodesAndRepliesRoundTrip(t *testing.T) {
	s := "1"
	n := Node{Line: 1, Map: []Pair{{Key: "a", Value: Node{Line: 2, List: []Node{{Line: 2, Scalar: &s}}}}, {Key: "b", Value: Node{Line: 3, Map: []Pair{}}}, {Key: "c", Value: Node{Line: 4, List: []Node{}}}}}
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	// An empty map stays a map; read back as an empty list it would round
	// trip all the same, so the encoding is pinned here.
	if !strings.Contains(string(raw), `{"line":3,"map":[]}`) {
		t.Fatalf("an empty map was written as %s", raw)
	}
	var back Node
	if err := Decode(raw, &back); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	again, _ := json.Marshal(back)
	if string(again) != string(raw) {
		t.Fatalf("%s came back as %s", raw, again)
	}
	msg := "m"
	for want, v := range map[string]any{
		`{"values":[]}`:        ExtractReply{},
		`{"error":"m"}`:        ExtractReply{Error: &msg},
		`{"no_value":"m"}`:     ExtractReply{NoValue: &msg},
		`{"line":1,"list":[]}`: Node{Line: 1},
	} {
		if raw, _ := json.Marshal(v); string(raw) != want {
			t.Errorf("%+v written as %s, want %s", v, raw, want)
		}
	}
}

// A plugin reads its spec as strictly as a message: a member in another
// case, one written twice, one the plugin does not define and a null are
// refused. Planted breaks: DecodeSpec skipping oneValueNoNull; strict
// skipping exactNames.
func TestDecodeSpecIsStrict(t *testing.T) {
	type spec struct {
		Word string `json:"word"`
		Loud bool   `json:"loud,omitempty"`
	}
	for raw, want := range map[string]string{
		`{"Word": "x"}`:              `the member "Word" must be written "word"`,
		`{"word": "x", "word": "y"}`: `the member "word" is written twice`,
		`{"word": "x", "other": 1}`:  `unknown field "other"`,
		`{"word": null}`:             "holds null",
		"{\"word\": \"\xff\"}":       "not valid UTF-8",
	} {
		var s spec
		if err := DecodeSpec([]byte(raw), &s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %s", raw, err, want)
		}
	}
	var s spec
	if err := DecodeSpec([]byte(`{"word": "x", "loud": true}`), &s); err != nil || s != (spec{"x", true}) {
		t.Fatalf("a good spec: %+v, %v", s, err)
	}
}

// A describe reply is refused for a where, or a field, the protocol does
// not allow. Planted breaks: each case of Field.check dropped in turn.
func TestADescriptionIsCheckedFieldByField(t *testing.T) {
	field := func(name, typ string) DescribeReply {
		return DescribeReply{Where: WhereFile, Fields: []Field{{Name: "a", Type: TypeString}, {Name: name, Type: typ}}}
	}
	for want, d := range map[string]DescribeReply{
		"neither file nor files":          {Where: "dir"},
		"is not lower case letters":       field("Word", TypeString),
		"the field max_bytes is errata's": field("max_bytes", TypeInt),
		"the field a is declared twice":   field("a", TypeBool),
		`the type "float"`:                field("b", "float"),
	} {
		if err := d.Check(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	if err := field("b", TypeMap).Check(); err != nil {
		t.Fatalf("a good description: %v", err)
	}
}

// An extract request carries max_bytes, the entry's bound on its files,
// which errata always sends as 1 byte or more (owner, 2026-11-20).
// Planted breaks: max_bytes left out of the required members; the bound
// checked with < 0.
func TestAnExtractRequestHoldsMaxBytes(t *testing.T) {
	const head = `{"protocol": 1, "op": "extract", "spec": {}, "files": [], "values": {}`
	for raw, want := range map[string]string{
		head + `}`:                   `the member "max_bytes" is missing`,
		head + `, "max_bytes": 0}`:   "max_bytes is 0; errata sends a bound of 1 byte or more",
		head + `, "max_bytes": -5}`:  "max_bytes is -5",
		head + `, "max_bytes": "1"}`: "cannot unmarshal",
		head + `, "Max_bytes": 1}`:   `the member "Max_bytes" must be written "max_bytes"`,
	} {
		var r ExtractRequest
		if err := Decode([]byte(raw), &r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", raw, err, want)
		}
	}
	var r ExtractRequest
	if err := Decode([]byte(head+`, "max_bytes": 1}`), &r); err != nil || r.MaxBytes != 1 {
		t.Fatalf("a request of max_bytes 1: %+v, %v", r, err)
	}
}

// The extract timeout is the protocol's 30 s (docs/plugin-protocol.md,
// Bounds), held here where it is defined so a change to it fails in this
// package. Planted break: the constant set to 20 s.
func TestTheExtractTimeoutIsThirtySeconds(t *testing.T) {
	if ExtractTimeout != 30*time.Second {
		t.Fatalf("ExtractTimeout is %v, the protocol says 30s", ExtractTimeout)
	}
}
