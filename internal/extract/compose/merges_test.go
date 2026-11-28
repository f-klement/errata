package compose

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

// mergePaths are the paths, by their YAML names, of every field of t, a
// part of a service's configuration, that is not below a list: an item of
// a list is one source as a whole, and its list is the fold. * stands for
// each value of a mapping.
func mergePaths(t reflect.Type, at []string, seen map[reflect.Type]bool, out *[][]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if len(at) > 0 {
		*out = append(*out, at)
	}
	switch t.Kind() {
	case reflect.Map:
		if mayMap(t) && t.Elem().Kind() != reflect.Pointer && t.Elem().Kind() != reflect.Struct {
			return // a mapping of compose-go's own decoding, such as extra_hosts, whose values are no field
		}
		mergePaths(t.Elem(), append(slices.Clip(at), "*"), seen, out)
	case reflect.Struct:
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		for _, f := range yamlFields(t) {
			mergePaths(f.Type, append(slices.Clip(at), f.name), seen, out)
		}
	default:
	}
}

// yamlField is a field of a struct by its YAML name.
type yamlField struct {
	name string
	reflect.Type
}

// yamlFields are the fields of t, a struct, by their YAML names, an
// inline struct's as t's own, without its x- keys.
func yamlFields(t reflect.Type) []yamlField {
	var out []yamlField
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case name == "" && strings.Contains(opts, "inline") && ft.Kind() == reflect.Struct:
			out = append(out, yamlFields(ft)...)
		case name != "" && name != "-" && !strings.HasPrefix(name, "#") && f.IsExported():
			out = append(out, yamlField{name, ft})
		}
	}
	return out
}

// itemPairs are the items a list is tried with, two at a time, one of
// which each list of strings compose-go has takes: words, numbers, and
// host=address pairs.
var itemPairs = [][2]string{{"a", "b"}, {"1", "2"}, {"a=1", "b=2"}}

// replacedBy reports whether compose-go replaces the list at path whole
// when an override writes it, and whether a list loads there at all: for
// the first pair of items with which a list of one loads, a base file's
// [x] and an override's [y] merging to the override's list alone.
func replacedBy(t *testing.T, path []string) (replaced, loads bool) {
	t.Helper()
	for _, p := range itemPairs {
		base, _ := loadAt(t, path, []any{p[0]}).([]any)
		over, _ := loadAt(t, path, []any{p[1]}).([]any)
		if len(base) != 1 || len(over) != 1 {
			continue
		}
		merged := loadAt(t, path, []any{p[0]}, []any{p[1]})
		return reflect.DeepEqual(merged, over), true
	}
	return false, false
}

// keyWise reports whether compose-go merges the mapping at path, of
// struct type st, key by key, and whether it could tell: for each field of
// st that holds text, a base file that writes every field of st that
// holds text or a mapping of text and an override that writes that field
// alone must merge to the base's other fields kept. It could tell when the
// base loads.
func keyWise(t *testing.T, path []string, st reflect.Type) (keyed, told bool) {
	t.Helper()
	base := map[string]any{}
	var texts []string
	for _, f := range yamlFields(st) {
		switch {
		case f.Kind() == reflect.String:
			base[f.name] = "a"
			texts = append(texts, f.name)
		case f.Kind() == reflect.Map && f.Elem().Kind() == reflect.String:
			base[f.name] = map[string]any{"k": "a"}
		}
	}
	if len(texts) == 0 || len(base) < 2 {
		return true, false
	}
	alone, _ := loadAt(t, path, base).(map[string]any)
	if alone == nil {
		return true, false
	}
	for _, f := range texts {
		merged, _ := loadAt(t, path, base, map[string]any{f: "b"}).(map[string]any)
		for k, v := range alone {
			if k != f && !reflect.DeepEqual(merged[k], v) {
				return false, true
			}
		}
	}
	return true, true
}

// merges are the folds compose-go v2.16.2 has that errata must know by
// name, derived from its own types and loader: the lists an override
// replaces whole, which errata may take from the last file alone, and the
// mappings it does not merge key by key, which errata must take as folds.
// A list of strings where no list loads fails the test, so a list the
// probe cannot reach is never taken for one an override does not replace.
func merges(t *testing.T) (replaced, foldedMaps map[string]bool, told int) {
	t.Helper()
	var paths [][]string
	mergePaths(reflect.TypeFor[types.ServiceConfig](), nil, map[reflect.Type]bool{}, &paths)
	replaced, foldedMaps = map[string]bool{}, map[string]bool{}
	for _, p := range paths {
		ft := typeAt(reflect.TypeFor[types.ServiceConfig](), p)
		key := strings.Join(p, ".")
		switch ft.Kind() {
		case reflect.Slice:
			r, loads := replacedBy(t, p)
			if !loads && ft.Elem().Kind() == reflect.String {
				t.Errorf("no list of strings loads at %s, so the probe cannot tell whether an override replaces it", key)
			}
			replaced[key] = r
		case reflect.Struct:
			k, ok := keyWise(t, p, ft)
			if ok {
				told++
			}
			foldedMaps[key] = !k
		default:
		}
	}
	return replaced, foldedMaps, told
}

// typeAt is the type of the field of t at path, pointers taken away.
func typeAt(t reflect.Type, path []string) reflect.Type {
	for _, step := range path {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if step == "*" {
			t = t.Elem()
			continue
		}
		i := slices.IndexFunc(yamlFields(t), func(f yamlField) bool { return f.name == step })
		t = yamlFields(t)[i].Type
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// The lists errata takes from the last file alone are those an override
// replaces in compose-go, and the mappings it takes as folds are those
// compose-go does not merge key by key: a compose-go that replaces one
// list less, or merges one more mapping by a value of its own, fails here,
// so that a source the shell picks cannot fold into a value errata calls
// literal. Every other list is a fold (foldAt), whatever compose-go does
// with it. The sets come from the compose-go the module builds with, its
// types (mergePaths) and its loader (loadAt), at test time, not from a
// list. An extends base and a pre_start hook's service merge by the same
// rules (override.MergeYaml). Planted breaks: a list compose-go appends
// added to replacedLists; logging left out of foldedMappings.
func TestMergesAreComposeGos(t *testing.T) {
	replaced, foldedMaps, told := merges(t)
	for _, r := range replacedLists {
		if k := strings.Join(r, "."); !replaced[k] {
			t.Errorf("errata takes %s from the last file alone, which compose-go does not replace whole", k)
		}
	}
	for k, r := range replaced {
		if r && !isRow(replacedLists, strings.Split(k, ".")) {
			t.Errorf("compose-go replaces %s whole, which replacedLists lacks", k)
		}
	}
	for k, f := range foldedMaps {
		if listed := isRow(foldedMappings, strings.Split(k, ".")); f != listed {
			t.Errorf("compose-go merges %s key by key: %v; foldedMappings lists it: %v", k, !f, listed)
		}
	}
	if told < 5 {
		t.Errorf("the probe told how compose-go merges %d mappings, too few to trust", told)
	}
}
