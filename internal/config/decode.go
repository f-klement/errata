package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// typedFile is the target the YAML is converted against. sigs.k8s.io/yaml
// turns a YAML scalar into a string only where the target field is a string,
// so converting against the real types keeps run: [ls, .inf] a list of
// strings ("+Inf") rather than a float that JSON cannot hold.
type typedFile struct {
	Checks []checkEntry `json:"checks"`
}

// toJSON converts raw YAML to JSON with the coercions of the real types and
// the keys exactly as the author spelt them. The library decodes the JSON
// into the target with encoding/json, which matches keys without regard to
// case; capturing the JSON before that decode lets Parse refuse a key in the
// wrong case instead of having it merged with the right one.
func toJSON(raw []byte) (json.RawMessage, error) {
	var (
		out        json.RawMessage
		captureErr error
	)
	capture := func(d *json.Decoder) *json.Decoder {
		captureErr = d.Decode(&out)
		// The library decodes this instead, so its own decode cannot fail
		// on what the per-check pass reports with the check's name.
		return json.NewDecoder(strings.NewReader("{}"))
	}
	if err := yaml.UnmarshalStrict(raw, &typedFile{}, capture); err != nil {
		return nil, err
	}
	if captureErr != nil {
		return nil, captureErr
	}
	return out, nil
}

// documents counts the YAML documents in raw. The library reads only the
// first, so a check after a second --- would be dropped without an error.
// YAML forbids a line that starts with --- or ... followed by a space or the
// end of the line anywhere but as a document marker, quoted and block scalars
// included, so the markers can be found line by line.
func documents(raw []byte) int {
	var (
		n       int
		open    bool // an explicit --- started the current document
		content bool // the current document has a node
	)
	text := strings.TrimPrefix(string(raw), "\ufeff")
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case marker(line, "---"):
			if open || content {
				n++
			}
			rest := strings.TrimSpace(line[3:])
			open, content = true, rest != "" && !strings.HasPrefix(rest, "#")
		case marker(line, "..."):
			if open || content {
				n++
			}
			open, content = false, false
		case !open && !content && strings.HasPrefix(line, "%"):
			// A directive belongs to the document that follows it.
		default:
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
				content = true
			}
		}
	}
	if open || content {
		n++
	}
	return n
}

// marker reports whether line is the document marker m at column 0.
func marker(line, m string) bool {
	rest, ok := strings.CutPrefix(line, m)
	return ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t')
}

// decodeStrict decodes raw into v and refuses a key v does not have.
func decodeStrict(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// exactKeys returns an error for every key in raw that matches a key of t
// only when case is ignored, with the path to it. A key that matches nothing
// is left to decodeStrict, and a value of the wrong shape to the decode.
func exactKeys(raw json.RawMessage, t reflect.Type, path string) []error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType {
		return nil
	}
	var errs []error
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		fields := jsonFields(t)
		names := slices.Sorted(maps.Keys(fields))
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			if ft, ok := fields[k]; ok {
				errs = append(errs, exactKeys(obj[k], ft, join(path, k))...)
				continue
			}
			for _, name := range names {
				if strings.EqualFold(k, name) {
					errs = append(errs, fmt.Errorf("%skey %q must be spelled %q", prefix(path), k, name))
					break
				}
			}
		}
	case reflect.Map:
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			errs = append(errs, exactKeys(obj[k], t.Elem(), join(path, k))...)
		}
	case reflect.Slice:
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return nil
		}
		for i, v := range arr {
			errs = append(errs, exactKeys(v, t.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
	default: // a scalar has no keys
	}
	return errs
}

// jsonFields maps each JSON key of struct t to the field's type.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func prefix(path string) string {
	if path == "" {
		return ""
	}
	return path + ": "
}
