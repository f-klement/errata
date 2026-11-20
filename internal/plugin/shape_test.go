package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// A value handed over whole is a mapping, every node of it on a line of
// the file, and a record's keys are its fields. Planted breaks: fromNode
// reading a nested map as a list; fromNode skipping the line check of a
// nested node; whole skipping the record's keys.
func TestAValueHandedOverWholeIsReadNodeByNode(t *testing.T) {
	var n wire.Node
	if err := wire.Decode([]byte(`{"line": 1, "map": [{"key": "a", "value": {"line": 2, "list": [{"line": 2, "scalar": "x"}, {"line": 3, "map": []}]}}]}`), &n); err != nil {
		t.Fatal(err)
	}
	var v check.Value
	if err := whole(&v, wire.Value{File: "f", Data: &n}, check.Description{Shape: check.ShapeMap}, 3); err != nil {
		t.Fatal(err)
	}
	a := v.Data.Field("a")
	if v.Data.Kind != check.DataMap || a == nil || a.Kind != check.DataList || a.Items[0].Text != "x" || a.Items[1].Kind != check.DataMap || a.Items[1].At.Line != 3 {
		t.Fatalf("got %+v", v.Data)
	}
	if err := whole(&v, wire.Value{File: "f", Data: &n}, check.Description{Shape: check.ShapeMap}, 2); err == nil || !strings.Contains(err.Error(), "a node's line 3 is not in f") {
		t.Fatalf("a nested node past the file: %v", err)
	}
	if err := whole(&v, wire.Value{File: "f", Data: &n}, check.Description{Shape: check.ShapeRecord, Fields: []string{"b"}}, 3); err == nil || !strings.Contains(err.Error(), `its keys ["a"] are not the record's fields ["b"]`) {
		t.Fatalf("a record of other keys: %v", err)
	}
	text := "t"
	if err := whole(&v, wire.Value{File: "f", Text: &text, Data: &n}, check.Description{Shape: check.ShapeMap}, 3); err == nil || !strings.Contains(err.Error(), "no value handed over whole") {
		t.Fatalf("text with data: %v", err)
	}
}

// A value errata read whole is sent node by node, keeping its kinds and
// lines, and refused when a key or a scalar is not UTF-8. Planted breaks:
// toNode sending a map as a list; toPairs not checking its keys.
func TestAValueReadWholeIsSentNodeByNode(t *testing.T) {
	at := func(line int) check.Location { return check.Location{File: "f", Line: line} }
	d := &check.Data{Kind: check.DataMap, At: at(1), Keys: []string{"a", "b"}, Items: []*check.Data{
		{Kind: check.DataList, At: at(2), Items: []*check.Data{{Kind: check.DataScalar, Text: "x", At: at(2)}}},
		{Kind: check.DataMap, At: at(3)},
	}}
	w, ok := toWire(check.Value{Data: d, At: at(1)})
	raw, _ := json.Marshal(w.Data)
	if want := `{"line":1,"map":[{"key":"a","value":{"line":2,"list":[{"line":2,"scalar":"x"}]}},{"key":"b","value":{"line":3,"map":[]}}]}`; !ok || string(raw) != want {
		t.Fatalf("got %s, %v, want %s", raw, ok, want)
	}
	d.Keys[1] = "\xff"
	if _, ok := toWire(check.Value{Data: d, At: at(1)}); ok {
		t.Fatal("a key that is not UTF-8 was sent")
	}
	d.Keys[1], d.Items[0].Items[0].Text = "b", "\xff"
	if _, ok := toWire(check.Value{Data: d, At: at(1)}); ok {
		t.Fatal("a scalar that is not UTF-8 was sent")
	}
}
