package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/ini"
)

// ruleWith is a rule check whose extract: holds entry under the name v.
func ruleWith(entry string) []byte {
	return []byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract:\n    v: " + entry + "\n  assert: size(v) > 0\n")
}

// A string field of a kind written as a number YAML reads as the same text
// loads as that text, as any string field of a check does: the YAML is
// read against the types the entry is written as. Planted break: toJSON
// reading against typedFile, which read section: 5 as a number and
// refused the check.
func TestKindStringWrittenAsANumberLoads(t *testing.T) {
	cfg, err := parseRaw(ruleWith("{ini: {file: a.ini, section: 5, key: 20.04}}"))
	if err != nil {
		t.Fatal(err)
	}
	x := cfg.Checks[0].Rule().Extractors[0]
	want := extract.Of(ini.Kind, ini.Spec{File: "a.ini", Section: "5", Key: "20.04"}).Resolved()
	if !reflect.DeepEqual(x.Spec, want) {
		t.Errorf("loaded %+v, want %+v", x.Spec, want)
	}
}

// A type error inside an extractor names its path from the check down, as
// one anywhere else in a check does, and an entry that is no mapping names
// the extractor: the Spec's own decode cannot see the path, so the check
// is decoded again as written. Planted break: decodeStrict returning the
// first decode's error.
func TestExtractorTypeErrorNamesItsPath(t *testing.T) {
	for entry, want := range map[string]string{
		"[1]": `check "a": decode: json: cannot unmarshal array into Go struct field checkEntry.extract.v of type extract.Spec`,
		"{yaml: {file: a, path: x, max_bytes: x}}": `check "a": decode: json: cannot unmarshal string into Go struct field checkEntry.extract.v.yaml.max_bytes of type int64`,
		"{yaml: 5}": `check "a": decode: json: cannot unmarshal number into Go struct field checkEntry.extract.v.yaml of type yaml.Spec`,
	} {
		_, err := parseRaw(ruleWith(entry))
		if err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%s: %v, want it to end %s", entry, err, want)
		}
	}
}

// The decode as written runs only when the first fails, and returns the
// first decode's error when it decodes, as for an entry that sets two kinds
// in one key's case, which only exactKeys, run before, refuses.
func TestDecodeStrictKeepsTheSpecsError(t *testing.T) {
	var e checkEntry
	err := decodeStrict(json.RawMessage(`{"extract": {"v": {"YAML": {"file": "a"}}}}`), &e)
	if err == nil || err.Error() != `json: unknown field "YAML"` {
		t.Errorf("got %v, want the Spec's unknown field", err)
	}
	var f file
	if err := decodeStrict(json.RawMessage(`{"checks": 5}`), &f); err == nil || !strings.Contains(err.Error(), "Go struct field file.checks") {
		t.Errorf("a type that holds no Spec: %v", err)
	}
}

// asWritten keeps a type that holds no Spec, its methods with it, and
// rebuilds one that does with the kinds' Schema in the Spec's place.
func TestAsWritten(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[defaults](), reflect.TypeFor[[]string](), reflect.TypeFor[check.Location]()} {
		if got := asWritten(typ); got != typ {
			t.Errorf("%v is written as %v", typ, got)
		}
	}
	for _, c := range []struct {
		typ  reflect.Type
		want reflect.Type
	}{
		{reflect.TypeFor[extract.Spec](), extract.Schema()},
		{reflect.TypeFor[*extract.Spec](), reflect.PointerTo(extract.Schema())},
		{reflect.TypeFor[[]extract.Spec](), reflect.SliceOf(extract.Schema())},
		{reflect.TypeFor[map[string]extract.Spec](), reflect.MapOf(reflect.TypeFor[string](), extract.Schema())},
	} {
		if got := asWritten(c.typ); got != c.want {
			t.Errorf("%v is written as %v, want %v", c.typ, got, c.want)
		}
	}
	entry := asWritten(reflect.TypeFor[checkEntry]())
	f, ok := entry.FieldByName("Extract")
	if !ok || f.Type != reflect.MapOf(reflect.TypeFor[string](), extract.Schema()) || f.Tag.Get("json") != "extract,omitempty" {
		t.Errorf("checkEntry is written with extract %+v", f)
	}
	if entry.NumField() != reflect.TypeFor[checkEntry]().NumField() {
		t.Errorf("checkEntry is written with %d fields, not %d", entry.NumField(), reflect.TypeFor[checkEntry]().NumField())
	}
}

// jsonFields gives the keys of a struct embedded with no JSON name, or of a
// pointer to one, as the struct's own, as encoding/json promotes them, and
// a key the struct writes itself wins over a promoted one of that name.
// Planted breaks: the embedded struct read as a key of its own name; a
// promoted key written over the struct's own; the pointer not followed.
func TestJSONFieldsPromotesAnEmbeddedStruct(t *testing.T) {
	type inner struct {
		File  int    `json:"file"`
		Files []int  `json:"files"`
		Path  string `json:"-"`
	}
	type outer struct {
		inner
		File string `json:"file"`
		Key  string `json:"key"`
	}
	type pointer struct {
		*inner
		File string `json:"file"`
		Key  string `json:"key"`
	}
	want := map[string]reflect.Type{
		"file":  reflect.TypeFor[string](),
		"files": reflect.TypeFor[[]int](),
		"key":   reflect.TypeFor[string](),
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[outer](), reflect.TypeFor[pointer]()} {
		if got := jsonFields(typ); !reflect.DeepEqual(got, want) {
			t.Errorf("jsonFields(%s) = %v, want %v", typ, got, want)
		}
	}
}
