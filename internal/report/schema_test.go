package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// This file holds a JSON Schema validator for the draft 04 keywords the SARIF
// 2.1.0 schema uses, so the SARIF test can validate against the published
// schema without a new module. It refuses to load a schema that uses any
// other keyword: a keyword it skipped would pass every document.

// schemaFile is the OASIS SARIF 2.1.0 errata01 schema, byte for byte as
// docs.oasis-open.org serves it. testdata/NOTICE holds its source, sha256
// and OASIS notice; TestSchemaFileIsTheOASISText pins the sha256.
const schemaFile = "testdata/sarif-schema-2.1.0.json"

// ignoredKeywords say nothing about whether a document is valid.
var ignoredKeywords = []string{"$schema", "id", "title", "description", "default"}

type schema struct {
	defs map[string]any
	root map[string]any
}

func loadSchema(tb testing.TB) *schema {
	tb.Helper()
	b, err := os.ReadFile(schemaFile)
	if err != nil {
		tb.Fatal(err)
	}
	s, err := compileSchema(b)
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

// compileSchema decodes a schema and refuses one with a keyword the
// validator does not implement.
func compileSchema(b []byte) (*schema, error) {
	var root map[string]any
	if err := decodeJSON(b, &root); err != nil {
		return nil, err
	}
	defs, _ := root["definitions"].(map[string]any)
	s := &schema{defs: defs, root: root}
	if err := s.checkKeywords(root, "#"); err != nil {
		return nil, err
	}
	for name, d := range defs {
		if err := s.checkKeywords(d, "#/definitions/"+name); err != nil {
			return nil, err
		}
	}
	return s, nil
}

var knownKeywords = []string{
	"$ref", "type", "properties", "additionalProperties", "required", "items",
	"minItems", "uniqueItems", "minimum", "maximum", "pattern", "format", "enum",
	"anyOf", "oneOf", "definitions",
}

var knownFormats = []string{"uri", "uri-reference", "date-time"}

func (s *schema) checkKeywords(node any, at string) error {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	for k, v := range m {
		switch {
		case slices.Contains(ignoredKeywords, k):
		case k == "definitions":
			// Walked by compileSchema, one definition at a time.
		case !slices.Contains(knownKeywords, k):
			return fmt.Errorf("schema keyword %q at %s is not implemented", k, at)
		case k == "format" && !slices.Contains(knownFormats, v.(string)):
			return fmt.Errorf("schema format %q at %s is not implemented", v, at)
		case k == "properties":
			for name, sub := range v.(map[string]any) {
				if err := s.checkKeywords(sub, at+"/properties/"+name); err != nil {
					return err
				}
			}
		case k == "anyOf" || k == "oneOf":
			for i, sub := range v.([]any) {
				if err := s.checkKeywords(sub, fmt.Sprintf("%s/%s/%d", at, k, i)); err != nil {
					return err
				}
			}
		case k == "items" || k == "additionalProperties":
			if err := s.checkKeywords(v, at+"/"+k); err != nil {
				return err
			}
		}
	}
	return nil
}

// validate returns every way doc breaks the schema, each with its JSON
// pointer.
func (s *schema) validate(doc []byte) []string {
	var v any
	if err := decodeJSON(doc, &v); err != nil {
		return []string{"not JSON: " + err.Error()}
	}
	var errs []string
	s.check(s.root, v, "", &errs)
	return errs
}

func decodeJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return fmt.Errorf("data after the JSON value at offset %d", d.InputOffset())
	}
	return nil
}

func (s *schema) check(node map[string]any, v any, at string, errs *[]string) {
	fail := func(format string, args ...any) {
		*errs = append(*errs, fmt.Sprintf("at %q: ", at)+fmt.Sprintf(format, args...))
	}
	if ref, ok := node["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/definitions/")
		def, ok := s.defs[name].(map[string]any)
		if !found || !ok {
			fail("$ref %q does not resolve", ref)
			return
		}
		s.check(def, v, at, errs)
		return
	}
	if t, ok := node["type"]; ok && !hasType(t, v) {
		fail("%s is not of type %v", kindOf(v), t)
		return
	}
	if e, ok := node["enum"].([]any); ok && !slices.ContainsFunc(e, func(x any) bool { return reflect.DeepEqual(x, v) }) {
		fail("%v is not one of %v", v, e)
	}
	for _, kw := range []string{"anyOf", "oneOf"} {
		alts, ok := node[kw].([]any)
		if !ok {
			continue
		}
		passed := 0
		for _, alt := range alts {
			var sub []string
			s.check(alt.(map[string]any), v, at, &sub)
			if len(sub) == 0 {
				passed++
			}
		}
		if passed == 0 || (kw == "oneOf" && passed != 1) {
			fail("%s: %d of %d alternatives hold", kw, passed, len(alts))
		}
	}
	switch v := v.(type) {
	case map[string]any:
		s.checkObject(node, v, at, errs, fail)
	case []any:
		s.checkArray(node, v, at, errs, fail)
	case string:
		if p, ok := node["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(v) {
			fail("%q does not match pattern %q", v, p)
		}
		if f, ok := node["format"].(string); ok && !hasFormat(f, v) {
			fail("%q is not a %s", v, f)
		}
	case json.Number:
		n, _ := new(big.Rat).SetString(v.String())
		if lim, ok := node["minimum"].(json.Number); ok && n.Cmp(rat(lim)) < 0 {
			fail("%s is less than the minimum %s", v, lim)
		}
		if lim, ok := node["maximum"].(json.Number); ok && n.Cmp(rat(lim)) > 0 {
			fail("%s is more than the maximum %s", v, lim)
		}
	}
}

func (s *schema) checkObject(node map[string]any, v map[string]any, at string, errs *[]string, fail func(string, ...any)) {
	props, _ := node["properties"].(map[string]any)
	if req, ok := node["required"].([]any); ok {
		for _, r := range req {
			if _, ok := v[r.(string)]; !ok {
				fail("missing required property %q", r)
			}
		}
	}
	names := make([]string, 0, len(v))
	for k := range v {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if sub, ok := props[k].(map[string]any); ok {
			s.check(sub, v[k], at+"/"+k, errs)
			continue
		}
		switch extra := node["additionalProperties"].(type) {
		case bool:
			if !extra {
				fail("additional property %q is not allowed", k)
			}
		case map[string]any:
			s.check(extra, v[k], at+"/"+k, errs)
		}
	}
}

func (s *schema) checkArray(node map[string]any, v []any, at string, errs *[]string, fail func(string, ...any)) {
	if lim, ok := node["minItems"].(json.Number); ok && big.NewRat(int64(len(v)), 1).Cmp(rat(lim)) < 0 {
		fail("%d items, fewer than minItems %s", len(v), lim)
	}
	if u, _ := node["uniqueItems"].(bool); u {
		for i := range v {
			for j := range i {
				if reflect.DeepEqual(v[i], v[j]) {
					fail("items %d and %d are equal, but uniqueItems is set", j, i)
				}
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		for i, x := range v {
			s.check(items, x, fmt.Sprintf("%s/%d", at, i), errs)
		}
	}
}

func rat(n json.Number) *big.Rat {
	r, _ := new(big.Rat).SetString(n.String())
	return r
}

func kindOf(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if r, ok := new(big.Rat).SetString(v.String()); ok && r.IsInt() {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func hasType(t, v any) bool {
	k := kindOf(v)
	match := func(name string) bool { return name == k || (name == "number" && k == "integer") }
	switch t := t.(type) {
	case string:
		return match(t)
	case []any:
		return slices.ContainsFunc(t, func(x any) bool { return match(x.(string)) })
	}
	return false
}

// uriReference is RFC 3986 URI-reference, by its character set: the
// unreserved and reserved characters, and percent escapes.
var (
	uriReference = regexp.MustCompile(`^([A-Za-z0-9\-._~!$&'()*+,;=:@/?#\[\]]|%[0-9A-Fa-f]{2})*$`)
	uriScheme    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)
)

func hasFormat(format, v string) bool {
	switch format {
	case "uri":
		return uriScheme.MatchString(v) && uriReference.MatchString(v)
	case "uri-reference":
		return uriReference.MatchString(v)
	case "date-time":
		_, err := time.Parse(time.RFC3339, v)
		return err == nil
	}
	return false
}
