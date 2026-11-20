package plugin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// A message quotes the last bytes of a long stderr after ..., cut at the
// start of a character, and a short one whole. Planted breaks: the length
// check at n inclusive; the cut taken inside a character.
func TestLastBytes(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "...bcd"},
		{"xé", 2, "...é"},
		{"xé", 1, "..."},
	} {
		if got := lastBytes(c.s, c.n); got != c.want {
			t.Errorf("lastBytes(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// A plugin built from this repository holds its values to wire.MaxValues,
// which is errata's limit on every extractor. Planted break: wire.MaxValues
// one more than check.MaxValues.
func TestWireMaxValuesIsCheckMaxValues(t *testing.T) {
	if wire.MaxValues != check.MaxValues {
		t.Fatalf("wire.MaxValues is %d, and check.MaxValues %d", wire.MaxValues, check.MaxValues)
	}
}

// The timeouts are the protocol's: 10 seconds to describe and to build,
// 30 to extract (docs/plugin-protocol.md, Bounds).
func TestTheTimeoutsAreTheProtocols(t *testing.T) {
	if describeTimeout != 10*time.Second || buildTimeout != 10*time.Second || extractTimeout != 30*time.Second {
		t.Fatalf("describe %v, build %v, extract %v", describeTimeout, buildTimeout, extractTimeout)
	}
}

// The stdout bound is 6 times max_bytes, 160 bytes and 6 times the
// longest path for each of MaxValues values, and 64 KiB.
func TestStdoutBound(t *testing.T) {
	x := &extractor{limit: 100}
	want := int64(6*100 + check.MaxValues*(160+6*3) + 64<<10)
	if got := x.stdoutBound([]wire.File{{Path: "a"}, {Path: "abc"}}); got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

// A value sent in a request carries its key, the empty key a reader named
// too, and its condition, and a byte that is not UTF-8 in either refuses
// it. Planted breaks: toWire dropping the key; dropping the condition;
// leaving either out of the UTF-8 check.
func TestASentValueCarriesItsKeyAndCondition(t *testing.T) {
	w, ok := toWire(check.Value{Text: "t", Key: "k", HasKey: true, Condition: "c", At: check.Location{File: "f", Line: 2}})
	if !ok || w.Key == nil || *w.Key != "k" || w.Condition == nil || *w.Condition != "c" || *w.Text != "t" {
		t.Fatalf("got %+v, %v", w, ok)
	}
	if w, _ := toWire(check.Value{Text: "t", HasKey: true}); w.Key == nil || *w.Key != "" || w.Condition != nil {
		t.Fatalf("an empty key named: %+v", w)
	}
	if w, _ := toWire(check.Value{Text: "t"}); w.Key != nil {
		t.Fatalf("no key: %+v", w)
	}
	for _, v := range []check.Value{{Text: "t", Key: "\xff"}, {Text: "t", Condition: "\xff"}} {
		if _, ok := toWire(v); ok {
			t.Errorf("%+v sent as UTF-8", v)
		}
	}
}

// The budget holds MaxValues nodes and max_bytes bytes, and refuses the
// node or the byte past either. Planted breaks: each bound taken as
// exclusive.
func TestTheBudgetHoldsItsBoundsExactly(t *testing.T) {
	b := budget{limit: 10}
	if err := b.add(check.MaxValues, 10); err != nil {
		t.Fatalf("at the bounds: %v", err)
	}
	if err := b.add(1, 0); err == nil || !strings.Contains(err.Error(), "the values pass") {
		t.Fatalf("a node past: %v", err)
	}
	b = budget{limit: 10}
	if err := b.add(0, 11); err == nil || !strings.Contains(err.Error(), "more than 10 bytes") {
		t.Fatalf("a byte past: %v", err)
	}
}

// A value or a node may be on line 0, no line, or on its file's last line,
// and not before or past them. Planted breaks: each bound of value and
// fromNode moved by one.
func TestAValuesLineIsInItsFile(t *testing.T) {
	x := &extractor{s: &Spec{res: &result{done: true}}}
	text := "t"
	for line, ok := range map[int]bool{-1: false, 0: true, 2: true, 3: false} {
		_, err := x.value(wire.Value{File: "f", Line: line, Text: &text}, map[string]int{"f": 2}, &budget{limit: 100})
		if (err == nil) != ok {
			t.Errorf("value on line %d: %v, want ok %v", line, err, ok)
		}
		_, err = fromNode(wire.Node{Line: line, Scalar: &text}, "f", 2)
		if (err == nil) != ok {
			t.Errorf("node on line %d: %v, want ok %v", line, err, ok)
		}
	}
}

// A set that names a file and a link to it sends the file once. Planted
// break: readSet keeping no record of the files it read.
func TestASetSendsEachFileOnce(t *testing.T) {
	root := newRepo(t, map[string]string{"a.txt": "x\n"}, "fake")
	if err := os.Symlink("a.txt", filepath.Join(root, "b.txt")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	gitAdd(t, root)
	got, err := extractOne(t, root, "fake", `{"files": ["a.txt", "b.txt"], "word": "x", "mode": "files"}`)
	if err != nil || len(got) != 1 || got[0].Text != "a.txt=x\n" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The fields of an entry are in the order the plugin declares them,
// whatever order the config or the build reply writes them in, and a map
// field prints as a mapping in its order. Planted breaks: either sort's
// comparator reversed; listForm taking every type for a map.
func TestAnEntrysFieldsAreInTheDeclaredOrder(t *testing.T) {
	root := newRepo(t, nil, "fake")
	set := openSet(t, root)
	k := set.Kind(context.Background(), "fake")
	ss := entries(t, set, "fake", `{"file": "a", "word": "w", "mode": "reorder"}`, `{"file": "a", "loud": true, "mode": "refuse", "word": "w"}`)
	if p := compact(t, extract.Spec{}.With(k, ss[0]).Place()); p != `{"fake":{"file":"a","word":"w","mode":"reorder","tags":{"z":"1","a":"2"},"loud":false}}` {
		t.Fatalf("built %s", p)
	}
	if r := compact(t, extract.Spec{}.With(k, ss[1]).Resolved()); r != `{"fake":{"file":"a","max_bytes":33554432,"word":"w","mode":"refuse","loud":true}}` {
		t.Fatalf("decoded %s", r)
	}
}

// An entry of a plugin errata described is read against its fields and
// errata's keys, files among them for a kind that reads a set; one of a
// plugin errata could not describe is read as it is written. Planted
// breaks: schema's unusable check reversed; files left out.
func TestAKindsSchema(t *testing.T) {
	usable := openSet(t, newRepo(t, nil, "fake")).Kind(context.Background(), "fake").Spec
	if _, ok := usable.FieldByName("Files"); usable.Kind() != reflect.Struct || !ok || usable.NumField() != 3+6 {
		t.Fatalf("fake: %v", usable)
	}
	root := newRepo(t, nil)
	writeFile(t, root, LockFile, pinLine("csv", strings.Repeat("a", 64))+"\n")
	if got := openSet(t, root).Kind(context.Background(), "csv").Spec; got != reflect.TypeFor[map[string]any]() {
		t.Fatalf("csv: %v", got)
	}
}

// A repository given by a relative root runs its plugins: the build is
// started from a directory of its own, so errata names it by an absolute
// path. Planted break: Open keeping the root as given, which started
// .errata/plugins/... in the empty directory and failed.
func TestARelativeRootRunsThePlugin(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "port: 1\n"}, "fake")
	t.Chdir(root)
	set, err := Open(".", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := entries(t, set, "fake", `{"file": "a", "word": "port:"}`)[0]
	x, err := set.Kind(context.Background(), "fake").Build(s)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := x.Extract(context.Background(), root); err != nil || len(got) != 1 || got[0].Text != "1" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
