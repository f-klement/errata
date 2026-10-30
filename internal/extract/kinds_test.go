package extract

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// decode is the Spec an extract: entry written as raw decodes into.
func decode(t *testing.T, raw string) Spec {
	t.Helper()
	var s Spec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return s
}

// specOf is a Spec of the kind k whose spec set fills in from its zero value.
func specOf(k *kind, set func(reflect.Value)) Spec {
	v := reflect.New(k.Spec).Elem()
	set(v)
	return Spec{set: []entry{{k, v.Interface()}}}
}

// fileSpec is a Spec of k that reads file, in File or, for a kind that
// takes only a list, in Files, with max_bytes n where the kind has it.
func fileSpec(k *kind, file string, n int64) Spec {
	return specOf(k, func(v reflect.Value) {
		if one := v.FieldByName("File"); one.IsValid() {
			one.SetString(file)
		} else {
			v.FieldByName("Files").Set(reflect.ValueOf([]string{file}))
		}
		if m := v.FieldByName("MaxBytes"); m.IsValid() {
			m.SetInt(n)
		}
	})
}

// fill gives every slice, map and pointer of v, at any depth, one element
// or a value, each string in it "s", so a spec holds something that
// Resolve could share.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("s")
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(key)
		fill(value)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, value)
	case reflect.Struct:
		for i := range v.NumField() {
			fill(v.Field(i))
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Bool:
		// a number or a bool holds nothing to share
	default:
		// A kind this does not know could be shared unseen.
		panic("fill: a spec holds a " + v.Kind().String())
	}
}

// change writes to every value that v, a value of a spec, reaches through
// a slice, a map or a pointer: a string, a number or a bool. One held in
// the value itself is its copy, and writing to it changes nothing that is
// shared. A map's value is changed in a copy and set back.
func change(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(v.String() + " changed")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.CanSet() {
			v.SetInt(v.Int() + 1)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if v.CanSet() {
			v.SetUint(v.Uint() + 1)
		}
	case reflect.Float32, reflect.Float64:
		if v.CanSet() {
			v.SetFloat(v.Float() + 1)
		}
	case reflect.Bool:
		if v.CanSet() {
			v.SetBool(!v.Bool())
		}
	case reflect.Pointer:
		if !v.IsNil() {
			change(v.Elem())
		}
	case reflect.Slice:
		for i := range v.Len() {
			change(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			change(e)
			v.SetMapIndex(k, e)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			change(v.Field(i))
		}
	default:
		// A kind this does not know could be shared unseen.
		panic("change: a spec holds a " + v.Kind().String())
	}
}

// change reaches every value a plain copy shares: a number, a bool and a
// string behind a pointer, in a slice or in a map, a map of lists among
// them; and nothing the copy holds itself. fill and change panic on a kind
// of value they do not know, such as an interface, which could be shared
// unseen. Planted break: change without
// its case for numbers, which let a shared *int pass.
func TestChangeReachesWhatACopyShares(t *testing.T) {
	type shares struct {
		P *int
		N []int
		B *bool
		F *float64
		U []uint8
		S *string
		M map[string][]string
		K map[string]int
	}
	for i := range reflect.TypeFor[shares]().NumField() {
		var given, fresh shares
		fill(reflect.ValueOf(&given).Elem())
		fill(reflect.ValueOf(&fresh).Elem())
		// A copy that shares only this field, and holds a fresh value
		// of every other.
		var copied shares
		fill(reflect.ValueOf(&copied).Elem())
		reflect.ValueOf(&copied).Elem().Field(i).Set(reflect.ValueOf(given).Field(i))
		change(reflect.ValueOf(copied))
		if reflect.DeepEqual(given, fresh) {
			t.Errorf("%s: change left a shared value as it was", reflect.TypeFor[shares]().Field(i).Name)
		}
	}
	var own, fresh struct {
		I int
		S string
	}
	change(reflect.ValueOf(own))
	if own != fresh {
		t.Errorf("change wrote to the value itself: %+v", own)
	}
	for name, f := range map[string]func(reflect.Value){"fill": fill, "change": change} {
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "a spec holds a interface") {
					t.Errorf("%s of an interface: recovered %v, want its panic", name, r)
				}
			}()
			var v struct{ I any }
			f(reflect.ValueOf(&v).Elem().Field(0))
		}()
	}
}

// fileOf and maxBytesOf read a kind's spec as fileSpec wrote it.
func fileOf(s Spec) string {
	v := reflect.ValueOf(s.set[0].spec)
	if one := v.FieldByName("File"); one.IsValid() {
		return one.String()
	}
	if fs := v.FieldByName("Files"); fs.Len() == 1 {
		return fs.Index(0).String()
	}
	return ""
}

func maxBytesOf(s Spec) (int64, bool) {
	if m := reflect.ValueOf(s.set[0].spec).FieldByName("MaxBytes"); m.IsValid() {
		return m.Int(), true
	}
	return 0, false
}

// Every kind round trips: a spec of it, its file a/../f and its max_bytes
// 10, marshals under its key and decodes back to itself; its place keeps
// the kind with the file cleaned and no max_bytes, and two files have two
// places; Resolved keeps the kind and the file as written, writes out
// core's default for no max_bytes, keeps one the spec sets, and leaves the
// spec as it was; and the zero spec builds through the kind's own Build,
// whose error names it. What Resolved returns shares no slice, map or
// pointer with the spec it was given. A kind registered without one of its
// functions fails here, by name. The planted breaks: text registered
// without Place; the toml reader's Kind under the key tomls; size's
// Resolve replaced by its Place, which cleaned the file errata list prints
// as written; xml's Resolve not cloning its namespaces.
func TestEveryKindRoundTrips(t *testing.T) {
	for _, k := range kinds.order {
		if absent := missing(k); len(absent) > 0 {
			t.Errorf("%s is registered without %s", k.Key, strings.Join(absent, ", "))
			continue
		}
		s := fileSpec(k, "a/../f", 10)
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(raw), `{"`+k.Key+`":{`) {
			t.Errorf("%s marshals as %s", k.Key, raw)
		}
		if back := decode(t, string(raw)); !reflect.DeepEqual(back, s) {
			t.Errorf("%s: %s decodes as %+v, want %+v", k.Key, raw, back, s)
		}
		p := s.Place()
		n, limited := maxBytesOf(p)
		if len(p.set) != 1 || p.set[0].kind != k || fileOf(p) != "f" || n != 0 {
			t.Errorf("%s: Place gives %+v, want the kind with file f and no max_bytes", k.Key, p)
		}
		if reflect.DeepEqual(fileSpec(k, "f", 0).Place(), fileSpec(k, "g", 0).Place()) {
			t.Errorf("%s: files f and g have one place", k.Key)
		}
		for _, set := range []int64{0, 10} {
			s := fileSpec(k, "./f", set)
			r := s.Resolved()
			got, _ := maxBytesOf(r)
			// exists and size read no byte, so they have no limit to
			// write out.
			want := set
			switch {
			case !limited:
				want = 0
			case set == 0:
				want = core.DefaultMaxBytes
			}
			if len(r.set) != 1 || r.set[0].kind != k || fileOf(r) != "./f" || got != want {
				t.Errorf("%s with max_bytes %d: Resolved gives %+v, want file ./f as written and max_bytes %d", k.Key, set, r, want)
			}
			if was, _ := maxBytesOf(s); limited && was != set {
				t.Errorf("%s: Resolved changed the spec's max_bytes to %d", k.Key, was)
			}
		}
		full := func() Spec { return specOf(k, fill) }
		given := full()
		change(reflect.ValueOf(given.Resolved().set[0].spec))
		if !reflect.DeepEqual(given, full()) {
			t.Errorf("%s: changing what Resolved returns changed the spec it was given: %+v", k.Key, given)
		}
		_, err = Build(specOf(k, func(reflect.Value) {}))
		switch k.Key {
		case "package_version":
			// Nothing is required: without files it reads the root's
			// manifests.
			if err != nil {
				t.Errorf("package_version with no field set: %v", err)
			}
		default:
			extracttest.WantError(t, err, k.Key+": ")
		}
	}
}

// describes are specs of every kind, each written as errata.yaml writes it,
// and more than one of a kind whose description hangs on its spec.
var describes = []string{
	`{"yaml": {"file": "a.yaml", "path": "a"}}`,
	`{"yaml": {"file": "a.yaml", "path": "a[*]", "as": "map"}}`,
	`{"json": {"files": ["*.json"], "path": "a"}}`,
	`{"toml": {"file": "a.toml", "path": "a[*]", "as": "record", "fields": {"n": "n"}}}`,
	`{"regex": {"file": "a", "pattern": "(x)"}}`,
	`{"regex": {"file": "a", "pattern": "((?{b}))"}}`,
	`{"regex": {"files": ["a", "b"], "pattern": "((?{c}))"}}`,
	`{"regex": {"file": "a", "pattern": "(?P<n>x)", "as": "record"}}`,
	`{"text": {"file": "a.md", "pattern": "(x)"}}`,
	`{"xml": {"file": "a.xml", "path": "/a"}}`,
	`{"ini": {"file": "a.ini", "section": "s", "key": "k"}}`,
	`{"ini": {"file": ".gitconfig", "section": "s", "key": "k"}}`,
	`{"ini": {"file": ".gitconfig.j2", "section": "s", "key": "k"}}`,
	`{"ini": {"file": "php.ini", "key": "k"}}`,
	`{"ini": {"file": "setup.cfg", "section": "s", "key": "k"}}`,
	`{"ini": {"file": "inventory/hosts", "section": "web", "key": "k"}}`,
	`{"properties": {"file": "a.properties", "key": "k"}}`,
	`{"properties": {"file": "a.properties.j2", "key": "k"}}`,
	`{"tool_version": {"tool": "node"}}`,
	`{"package_version": {}}`,
	`{"envfile": {"file": ".env", "name": "A"}}`,
	`{"gitlab-ci": {"file": ".gitlab-ci.yml", "job": "j", "key": "image"}}`,
	`{"shell": {"file": "a.sh", "name": "A"}}`,
	`{"github-actions": {"file": "ci.yml", "job": "j", "key": "runs-on"}}`,
	`{"jenkins": {"file": "Jenkinsfile", "variable": "A"}}`,
	`{"env": {"name": "A", "files": [".env"]}}`,
	`{"exists": {"file": "a"}}`,
	`{"size": {"file": "a"}}`,
}

// mayNotLiteral are the specs of describes whose built extractor may
// return a not literal value and does not show it in its type: a reader
// whose own tests show it marking such values. A core.EntryExtractor shows
// it in its NotLiteral, so the test reads that instead and none is listed.
var mayNotLiteral = map[string]bool{
	`{"tool_version": {"tool": "node"}}`:                                    true,
	`{"package_version": {}}`:                                               true,
	`{"gitlab-ci": {"file": ".gitlab-ci.yml", "job": "j", "key": "image"}}`: true,
	`{"github-actions": {"file": "ci.yml", "job": "j", "key": "runs-on"}}`:  true,
	`{"env": {"name": "A", "files": [".env"]}}`:                             true,
}

// builtNotLiteral is whether x, built from raw, may return a not literal
// value: for a core.EntryExtractor, whether it has a test for one; for any
// other, what mayNotLiteral says of raw.
func builtNotLiteral(t *testing.T, raw string, x check.Extractor) bool {
	t.Helper()
	e, ok := x.(core.EntryExtractor)
	if !ok {
		return mayNotLiteral[raw]
	}
	if _, listed := mayNotLiteral[raw]; listed {
		t.Errorf("%s: listed in mayNotLiteral, but its extractor shows it", raw)
	}
	return e.NotLiteral != nil
}

// builtReads is the extractors x, built from raw, reads the values of:
// none unless it is a regex.Reference, and for one, the name in d.Reads
// whose values ExtractWith searches for, seen by handing it a value under
// that name and under another. A file a, and b, hold the value.
func builtReads(t *testing.T, raw string, x check.Extractor, d check.Description) []string {
	t.Helper()
	if _, ok := x.(regex.Reference); !ok {
		return nil
	}
	if len(d.Reads) != 1 {
		return []string{"one extractor, the one the pattern references"}
	}
	root := extracttest.Repo(t, map[string]string{"a": "v\n", "b": "v\n"})
	found := func(name string) bool {
		vs, err := x.ExtractWith(root, map[string][]check.Value{name: {{Text: "v"}}})
		var none *check.NoValue
		if err != nil && !errors.As(err, &none) {
			t.Errorf("%s: %v", raw, err)
		}
		return len(vs) > 0
	}
	if !found(d.Reads[0]) || found(d.Reads[0]+"_other") {
		return []string{"another extractor than " + d.Reads[0]}
	}
	return d.Reads
}

// Each kind's description, which is all that config and rule learn of an
// extractor before it runs, tells what its built extractor does: it may
// return a not literal value where the built extractor marks one, and it
// reads the extractor whose values the built one searches for. Every kind
// has a spec here that builds. The planted breaks, each in a kind's
// Describe: gitlab-ci described as literal only; properties described as
// literal only; regex's reference left out of Reads; regex reading an
// extractor its pattern does not reference.
func TestDescribeTellsWhatBuildReturns(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range describes {
		s := decode(t, raw)
		x, err := Build(s)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		seen[s.set[0].kind.Key] = true
		d := s.Describe()
		if want := builtNotLiteral(t, raw, x); d.NotLiteral != want {
			t.Errorf("%s: described as not literal %t, and its extractor %t", raw, d.NotLiteral, want)
		}
		if want := builtReads(t, raw, x, d); !slices.Equal(d.Reads, want) {
			t.Errorf("%s: described as reading %q, and its extractor reads %q", raw, d.Reads, want)
		}
		if label := strings.SplitN(d.Label, ",", 2)[0]; label != s.set[0].kind.Key {
			t.Errorf("%s: labelled %q", raw, d.Label)
		}
	}
	for _, k := range Keys() {
		if !seen[k] {
			t.Errorf("no spec of %s is described here", k)
		}
	}
}

// Every kind that reads one file builds a core.FileReader, which reads the
// bytes core hands it: the seam a file set and a plugin host build on. The
// kinds that find their files by their own logic are the exceptions,
// gitlab-ci among them since it reads the files a pipeline includes, and so
// are a spec of files:, which builds a core.FileSet of FileReaders, and a
// regex that searches for another extractor's values, which reads once
// those values are known. Planted break: text's FromShared removed.
func TestEveryOneFileReaderIsAFileReader(t *testing.T) {
	ownFiles := map[string]bool{"tool_version": true, "package_version": true, "env": true, "exists": true, "size": true, "gitlab-ci": true}
	seen := map[string]bool{}
	// describes holds json only as a file set.
	for _, raw := range append(slices.Clone(describes), `{"json": {"file": "a.json", "path": "a"}}`) {
		s := decode(t, raw)
		k := s.set[0].kind.Key
		x, err := Build(s)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		dependent := len(s.Describe().Reads) > 0
		_, set := x.(core.FileSet)
		if ownFiles[k] || dependent || set {
			continue
		}
		seen[k] = true
		if _, ok := x.(core.FileReader); !ok {
			t.Errorf("%s builds a %T, which is no core.FileReader", raw, x)
		}
	}
	for _, k := range Keys() {
		if !ownFiles[k] && !seen[k] {
			t.Errorf("no spec of %s is checked here", k)
		}
	}
}

// Describe gives each shape, with the fields of a record, and the zero
// description to a spec that sets no kind or two.
func TestDescribeShapes(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want check.Description
	}{
		{`{"yaml": {"file": "a.yaml", "path": "a"}}`, check.Description{Label: "yaml"}},
		{`{"json": {"file": "a.json", "path": "a", "as": "map"}}`, check.Description{Label: "json", Shape: check.ShapeMap}},
		{`{"toml": {"file": "a.toml", "path": "a", "as": "record", "fields": {"b": "x", "a": "y"}}}`, check.Description{Label: "toml", Shape: check.ShapeRecord, Fields: []string{"a", "b"}}},
		{`{"regex": {"file": "a", "pattern": "(?P<b>x)(?P<a>y)", "as": "record"}}`, check.Description{Label: "regex", Shape: check.ShapeRecord, Fields: []string{"b", "a"}}},
		{`{"regex": {"file": "a", "pattern": "((?{v}))"}}`, check.Description{Label: "regex", Reads: []string{"v"}}},
		{`{"regex": {"file": "a", "pattern": "((?{v})(?{w}))"}}`, check.Description{Label: "regex"}},
		{`{"ini": {"file": "php.ini", "key": "k"}}`, check.Description{Label: "ini, php dialect on php.ini", NotLiteral: true}},
		{`{"gitlab-ci": {"file": "c.yml", "job": "j", "key": "image"}}`, check.Description{Label: "gitlab-ci", NotLiteral: true}},
		{`{}`, check.Description{}},
		{`{"exists": {"file": "a"}, "size": {"file": "a"}}`, check.Description{}},
	} {
		if got := decode(t, c.raw).Describe(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.raw, got, c.want)
		}
	}
}

// An entry decodes strictly: a key no kind has and a key a kind's spec
// does not have are refused, as any unknown key of a check is; an entry
// that is no object is a type error, and a type error inside a kind names
// the field under the kind's key. null, and a kind written null, set no
// kind, so Build refuses them with the keys. Two kinds decode, in the
// order of kinds whatever the order written, for Build to refuse.
func TestSpecDecodes(t *testing.T) {
	for raw, want := range map[string]string{
		`{"yml": {"file": "a"}}`:                       `json: unknown field "yml"`,
		`{"yaml": {"file": "a", "path": "a", "x": 1}}`: `json: unknown field "x"`,
		`["yaml"]`:                  "json: cannot unmarshal array into Go value of type extract.Spec",
		`"yaml"`:                    "json: cannot unmarshal string into Go value of type extract.Spec",
		`true`:                      "json: cannot unmarshal bool into Go value of type extract.Spec",
		`5`:                         "json: cannot unmarshal number into Go value of type extract.Spec",
		`{"yaml": {"file": ["a"]}}`: "json: cannot unmarshal array into Go struct field Spec.yaml.file of type string",
		`{"yaml": "a"}`:             "json: cannot unmarshal string into Go struct field Spec.yaml of type yaml.Spec",
		`{"yaml": {"file": "a"`:     "unexpected end of JSON input",
	} {
		var s Spec
		err := json.Unmarshal([]byte(raw), &s)
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want exactly %s", raw, err, want)
		}
	}
	for _, raw := range []string{`null`, `{"yaml": null}`, `{"yaml": {"file": "a", "path": "a"}, "yaml": null}`} {
		s := decode(t, raw)
		if len(s.set) != 0 {
			t.Errorf("%s sets %+v", raw, s.set)
		}
	}
	two := decode(t, `{"text": {"file": "a", "pattern": "(x)"}, "yaml": {"file": "a", "path": "a"}}`)
	want := Spec{set: []entry{{kinds.byKey["yaml"], yaml.Spec{File: "a", Path: "a"}}, {kinds.byKey["text"], text.Spec{File: "a", Pattern: "(x)"}}}}
	if !reflect.DeepEqual(two, want) {
		t.Errorf("two kinds decode as %+v, want %+v", two, want)
	}
	if raw, err := json.Marshal(two); err != nil || string(raw) != `{"yaml":{"file":"a","path":"a"},"text":{"file":"a","pattern":"(x)"}}` {
		t.Errorf("two kinds marshal as %s, %v", raw, err)
	}
	if raw, err := json.Marshal(Spec{}); err != nil || string(raw) != `{}` {
		t.Errorf("no kind marshals as %s, %v", raw, err)
	}
	var te *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{"yaml": {"max_bytes": "x"}}`), &Spec{}); !errors.As(err, &te) || te.Field != "yaml.max_bytes" {
		t.Errorf("a type error inside yaml is %#v, want the field yaml.max_bytes", err)
	}
}

// Keys lists every kind once, in the order of kinds, and Schema has one
// field for each, under its key, of a pointer to its spec type. Changing
// what Keys returns changes no later call.
func TestKeysAndSchema(t *testing.T) {
	keys := Keys()
	if len(keys) != len(kinds.order) || keys[0] != "yaml" || keys[len(keys)-1] != "size" {
		t.Fatalf("Keys() = %q", keys)
	}
	keys[0] = "changed"
	if Keys()[0] != "yaml" {
		t.Error("Keys shares its slice")
	}
	schema := Schema()
	if schema.NumField() != len(kinds.order) {
		t.Fatalf("Schema has %d fields for %d kinds", schema.NumField(), len(kinds.order))
	}
	for i, k := range kinds.order {
		f := schema.Field(i)
		if f.Tag.Get("json") != k.Key+",omitempty" || f.Type != reflect.PointerTo(k.Spec) {
			t.Errorf("field %d is %s %v, want %s *%v", i, f.Tag.Get("json"), f.Type, k.Key, k.Spec)
		}
	}
}

// The registry refuses a key written twice and a kind with no key, and Of
// a kind kinds does not list.
func TestRegistryRefuses(t *testing.T) {
	for name, f := range map[string]func(){
		"twice":   func() { registry(register(text.Kind, text.Build), register(text.Kind, text.Build)) },
		"no key":  func() { registry(register(core.Kind[text.Spec]{}, text.Build)) },
		"of":      func() { Of(core.Kind[text.Spec]{Key: "nope"}, text.Spec{}) },
		"of type": func() { Of(core.Kind[yaml.Spec]{Key: "text"}, yaml.Spec{}) },
	} {
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), "extract: ") {
					t.Errorf("%s: recovered %v, want extract's panic", name, r)
				}
			}()
			f()
		}()
	}
	if got := missing(register(core.Kind[text.Spec]{Key: "text"}, nil)); !slices.Equal(got, []string{"Build", "Place", "Resolve", "Describe"}) {
		t.Errorf("a kind with no function misses %q", got)
	}
}

// A Spec escapes <, > and & only where the encoder that writes it is set
// to, as errata list's is not. Planted break: the kind's spec written with
// json.Marshal, which listed each < of a pattern as the escape \u003c.
func TestSpecMarshalsAsTheEncoderAsks(t *testing.T) {
	s := Of(text.Kind, text.Spec{File: "a", Pattern: "(a << b)"})
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil || b.String() != `{"text":{"file":"a","pattern":"(a << b)"}}`+"\n" {
		t.Errorf("unescaped: %q, %v", b.String(), err)
	}
	if raw, err := json.Marshal(s); err != nil || string(raw) != `{"text":{"file":"a","pattern":"(a \u003c\u003c b)"}}` {
		t.Errorf("escaped: %s, %v", raw, err)
	}
}
