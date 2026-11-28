package compose

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	cg "github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// fieldPaths are the paths, by their YAML names, of every field below t,
// a part of a service's configuration, where the configuration's JSON may
// hold a mapping that a list wrote: a field whose type is a mapping, or a
// type of compose-go's own decoding or JSON. A step [] stands for each
// item of a list and * for each value of a mapping.
func fieldPaths(t reflect.Type, at []string, seen map[reflect.Type]bool, out *[]fieldPath) {
	if len(at) > 0 && mayMap(t) {
		*out = append(*out, fieldPath{at, t.Kind() == reflect.Map})
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice:
		fieldPaths(t.Elem(), append(slices.Clip(at), "[]"), seen, out)
	case reflect.Map:
		fieldPaths(t.Elem(), append(slices.Clip(at), "*"), seen, out)
	default:
		fieldsOf(t, at, seen, out)
	}
}

// fieldsOf is fieldPaths of each field of t, a struct, by its YAML name,
// an inline struct's fields as t's own; nothing for a type of another
// kind.
func fieldsOf(t reflect.Type, at []string, seen map[reflect.Type]bool, out *[]fieldPath) {
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch {
		case name == "" && strings.Contains(opts, "inline"):
			fieldPaths(f.Type, at, seen, out)
		case name != "" && name != "-" && !strings.HasPrefix(name, "#") && f.IsExported():
			fieldPaths(f.Type, append(slices.Clip(at), name), seen, out)
		}
	}
}

// fieldPath is a field fieldPaths finds, and whether its type is a
// mapping.
type fieldPath struct {
	at    []string
	isMap bool
}

// mayMap reports whether a value of t may be a mapping in the JSON of a
// configuration although a list wrote it.
func mayMap(t reflect.Type) bool {
	_, decodes := reflect.PointerTo(t).MethodByName("DecodeMapstructure")
	return t.Kind() == reflect.Map || decodes || t.Implements(reflect.TypeFor[json.Marshaler]())
}

// probe loads, through compose-go, a service whose field at path holds v,
// and is the mapping or the list the service's tree holds there, nil
// when the load fails or the tree holds neither. A [] step writes a list of one item and a
// * step a mapping of one key k.
func probe(t *testing.T, path []string, v any) (map[string]any, []any) {
	t.Helper()
	cur := loadAt(t, path, v)
	m, _ := cur.(map[string]any)
	list, _ := cur.([]any)
	return m, list
}

// loadAt loads, through compose-go, a project of a file per value of vs,
// in their order, each writing its value at path in a service s, and is
// what the service's tree holds at path, the service active under its
// profiles or not; nil when the load fails. The first file also gives s
// an image and a build context. Every load has one working directory, so
// that the paths compose-go makes absolute compare across loads; nothing
// is read from it.
func loadAt(t *testing.T, path []string, vs ...any) any {
	t.Helper()
	files := make([]types.ConfigFile, len(vs))
	for i, v := range vs {
		svc := map[string]any{}
		if i == 0 {
			svc = map[string]any{"image": "x", "build": map[string]any{"context": "."}}
		}
		for k, v := range nest(path, v).(map[string]any) {
			if m, ok := svc[k].(map[string]any); ok && k == "build" {
				for bk, bv := range v.(map[string]any) {
					m[bk] = bv
				}
				continue
			}
			svc[k] = v
		}
		data, err := json.Marshal(map[string]any{"services": map[string]any{"s": svc}})
		if err != nil {
			t.Fatal(err)
		}
		files[i] = types.ConfigFile{Filename: fmt.Sprintf("f%d.yaml", i), Content: data}
	}
	proj, err := safely(t.Context(), types.ConfigDetails{WorkingDir: "/probe", ConfigFiles: files},
		func(o *cg.Options) {
			o.SetProjectName("p", true)
			o.SkipValidation, o.SkipConsistencyCheck, o.SkipResolveEnvironment, o.SkipResolveLabels = true, true, true, true
		})
	if err != nil {
		return nil
	}
	cur, err := serviceTree(proj.AllServices()["s"])
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(path, "."), err)
	}
	for _, step := range path {
		cur = probeStep(cur, step)
	}
	return cur
}

func probeStep(cur any, step string) any {
	switch c := cur.(type) {
	case map[string]any:
		if step == "*" {
			step = "k"
		}
		return c[step]
	case []any:
		if len(c) > 0 {
			return c[0]
		}
	}
	return nil
}

// nest is v at path, in the mappings and lists of one item path's steps
// name. An item of volumes: is a volume mount, which needs a type.
func nest(path []string, v any) any {
	if len(path) == 0 {
		return v
	}
	inner := nest(path[1:], v)
	switch path[0] {
	case "[]":
		if m, ok := inner.(map[string]any); ok && len(path) > 1 && path[1] == "volume" {
			m["type"], m["source"], m["target"] = "volume", "a", "/b"
		}
		return []any{inner}
	case "*":
		return map[string]any{"k": inner}
	}
	return map[string]any{path[0]: inner}
}

// namedLists are the keyed lists compose-go v2.16.2 has, derived from its
// own types and loader: each field of a service's configuration where a
// list of strings loads as a mapping, with the characters that end an
// item's name, found by which of the items n, n=v and n:v it names n by;
// and each where the list loads sorted by its items' text, named by the
// text before each separator its items load with (keyedBy). A field of a mapping type where not even a mapping
// loads fails the test, so a field the probe cannot reach is never taken
// for no list.
func namedLists(t *testing.T) map[string]string {
	t.Helper()
	var paths []fieldPath
	fieldPaths(reflect.TypeFor[types.ServiceConfig](), nil, map[reflect.Type]bool{}, &paths)
	out := map[string]string{}
	for _, p := range paths {
		if p.isMap && !reaches(t, p.at) {
			t.Errorf("no mapping loads at %s, so the probe cannot tell whether a list there is keyed", strings.Join(p.at, "."))
		}
		if ends, ok := keyedBy(t, p.at); ok {
			out[strings.Join(p.at, ".")] = ends
		}
	}
	return out
}

// reaches reports whether a mapping {n: v} loads at path, for one value v
// of a word, a number and a mapping: the control that a list probed there
// reaches compose-go's decoding of the field.
func reaches(t *testing.T, path []string) bool {
	t.Helper()
	for _, v := range []any{"v", 1, map[string]any{}} {
		m, list := probe(t, path, map[string]any{"n": v})
		if _, ok := m["n"]; ok || len(list) > 0 {
			return true
		}
	}
	return false
}

// probeValues are the values an item n=v or n:v is tried with, one of
// which each keyed list of compose-go takes: a word, a number, an address
// and a path.
var probeValues = []string{"v", "1", "10.0.0.1", "/p"}

// keyedBy is the ends of a name in the list at path, and whether it is a
// keyed list at all: for each of no separator, = and :, the first value
// with which the item loads names whether the name ends there. A list
// compose-go sorts is named by the text before each separator its items
// load with: it sorts by name, separator and value, and a name holds no
// separator, so the names decide the order before the values do.
func keyedBy(t *testing.T, path []string) (string, bool) {
	t.Helper()
	var ends strings.Builder
	keyed := false
	for _, sep := range []string{"", "=", ":"} {
		for _, val := range probeValues {
			m, list := probe(t, path, []any{item("n", sep, val)})
			if len(list) == 1 {
				if sorts(t, path, sep, val) {
					keyed = true
					ends.WriteString(sep)
				}
				break
			}
			if len(m) == 0 {
				continue
			}
			keyed = true
			if _, ok := m["n"]; ok {
				ends.WriteString(sep)
			}
			break
		}
	}
	return ends.String(), keyed
}

// item is name, and sep and val after it when sep is not empty.
func item(name, sep, val string) string {
	if sep == "" {
		return name
	}
	return name + sep + val
}

// sorts reports whether the list at path loads its items z and a, each
// with sep and val after it, as a list in the order a, z.
func sorts(t *testing.T, path []string, sep, val string) bool {
	t.Helper()
	_, list := probe(t, path, []any{item("z", sep, val), item("a", sep, val)})
	return len(list) == 2 && list[0] != list[1] && strings.HasPrefix(fmt.Sprint(list[0]), "a")
}

// Every keyed list of the table is one compose-go has, with the same ends
// of a name, and every one compose-go has is in the table: a compose-go
// that turns one more list into a mapping, or sorts one more, fails here,
// so a name the shell picks in it cannot read as an absent key. The set
// comes from the compose-go the module builds with, its types (fieldPaths)
// and its loader (probe), at test time, not from a list. Planted breaks: a
// row left out of keyedLists; a row's ends changed.
func TestKeyedListsAreComposeGos(t *testing.T) {
	got := map[string]string{}
	for _, kl := range keyedLists {
		got[strings.Join(kl.field, ".")] = kl.ends
	}
	want := namedLists(t)
	for k, ends := range want {
		if g, ok := got[k]; !ok || g != ends {
			t.Errorf("compose-go turns the list %s into a mapping, the name ending at %q; the table has %q, %v", k, ends, g, ok)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("the table lists %s, which compose-go does not turn into a mapping", k)
		}
	}
}
