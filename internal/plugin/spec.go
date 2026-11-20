package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// Spec is one entry of a plugin's kind: errata's keys, file, files and
// max_bytes, and the plugin's fields the entry sets, each as a request
// carries it, in the order the plugin declares them. An entry of a plugin
// errata could not describe keeps its JSON whole in raw instead. The result
// of the build request is shared by every copy of a Spec, since config
// builds the extractor from a copy (check.ExtractorKind.ForRule).
type Spec struct {
	plugin *Plugin
	where  core.Where
	fields []fieldValue
	raw    json.RawMessage
	rule   string
	res    *result
}

// fieldValue is one field of a spec: its name, and its value as a request
// carries it.
type fieldValue struct {
	name  string
	value json.RawMessage
}

// result is what the build request made of an entry: the spec with every
// default written out and the description, or the plugin's refusal of the
// spec, which is a config error, or why the plugin could not build it,
// which makes the rule unknown.
type result struct {
	done    bool
	fields  []fieldValue
	desc    check.Description
	refused error
	failed  error
}

// MarshalJSON writes s as errata list prints it: errata's keys, then the
// plugin's fields in their order, a map as a mapping in the config's order.
// An entry of a plugin errata could not describe is written as it was.
func (s *Spec) MarshalJSON() ([]byte, error) {
	if s.raw != nil {
		return s.raw, nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	n := 0
	member := func(key string, value []byte) {
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		k, _ := json.Marshal(key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(value)
	}
	if s.where.File != "" {
		v, _ := json.Marshal(s.where.File)
		member("file", v)
	}
	if s.where.Files != nil {
		v, _ := json.Marshal(s.where.Files)
		member("files", v)
	}
	if s.where.MaxBytes != 0 {
		member("max_bytes", []byte(strconv.FormatInt(s.where.MaxBytes, 10)))
	}
	for _, f := range s.fields {
		member(f.name, listForm(s.plugin.field(f.name).Type, f.value))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// listForm is value, a field of type typ as a request carries it, as errata
// list writes it: a map as a mapping, in its order, and any other type as
// it is.
func listForm(typ string, value json.RawMessage) json.RawMessage {
	if typ != wire.TypeMap {
		return value
	}
	var kvs []wire.KeyValue
	if json.Unmarshal(value, &kvs) != nil {
		return value
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range kvs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv.Key)
		v, _ := json.Marshal(kv.Value)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// object is fields as one JSON object, as a request carries a spec.
func object(fields []fieldValue) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.name)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// decode reads raw, an entry of p's kind as config wrote it, in the
// config's order, into a Spec. It refuses a key the kind does not take, a
// value of the wrong type and a required field left out. An entry of a
// plugin errata could not describe is kept whole, once it is an object: the
// rule is unknown when it runs, naming why.
func (p *Plugin) decode(raw json.RawMessage) (any, error) {
	if p.unusable != nil {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("%s: %w", p.pin.Name, err)
		}
		return &Spec{plugin: p, raw: bytes.Clone(raw), res: &result{}}, nil
	}
	s := &Spec{plugin: p, res: &result{}}
	values, err := members(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.pin.Name, err)
	}
	for _, m := range values {
		if err := s.set(m.name, m.value); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", p.pin.Name, m.name, err)
		}
	}
	for _, f := range p.desc.Fields {
		if f.Required && !slices.ContainsFunc(s.fields, func(v fieldValue) bool { return v.name == f.Name }) {
			return nil, fmt.Errorf("%s: %s is required", p.pin.Name, f.Name)
		}
	}
	slices.SortStableFunc(s.fields, func(a, b fieldValue) int { return p.fieldIndex(a.name) - p.fieldIndex(b.name) })
	return s, nil
}

// set reads one member of an entry into s.
func (s *Spec) set(name string, value json.RawMessage) error {
	switch {
	case name == "file":
		return json.Unmarshal(value, &s.where.File)
	case name == "files" && s.plugin.desc.Where == wire.WhereFiles:
		return json.Unmarshal(value, &s.where.Files)
	case name == "max_bytes":
		return json.Unmarshal(value, &s.where.MaxBytes)
	}
	f := s.plugin.field(name)
	if f == nil {
		return errors.New("the kind takes no such key")
	}
	v, err := configValue(f.Type, value)
	if err != nil {
		return err
	}
	s.fields = append(s.fields, fieldValue{name, v})
	return nil
}

// member is one member of a JSON object.
type member struct {
	name  string
	value json.RawMessage
}

// members are the members of raw, a JSON object, in their order. A member
// written twice is refused: encoding/json would keep the last, and errata
// would send both.
func members(raw json.RawMessage) ([]member, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, errors.New("the entry is no mapping")
	}
	var out []member
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		name := k.(string)
		if slices.ContainsFunc(out, func(m member) bool { return m.name == name }) {
			return nil, fmt.Errorf("the member %s is written twice", strconv.Quote(name))
		}
		out = append(out, member{name, v})
	}
	if _, err := d.Token(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out, nil
}

// configValue is value, a field of type typ as the config wrote it, as a
// request carries it: a map's mapping becomes a list of keys and values in
// its order, and any other type is checked and kept.
func configValue(typ string, value json.RawMessage) (json.RawMessage, error) {
	if typ != wire.TypeMap {
		return requestValue(typ, value)
	}
	ms, err := members(value)
	if err != nil {
		return nil, errors.New("a mapping of strings to strings is required")
	}
	kvs := make([]wire.KeyValue, 0, len(ms))
	for _, m := range ms {
		var s string
		if err := json.Unmarshal(m.value, &s); err != nil {
			return nil, fmt.Errorf("the value of %s is no string", strconv.Quote(m.name))
		}
		kvs = append(kvs, wire.KeyValue{Key: m.name, Value: s})
	}
	return json.Marshal(kvs)
}

// requestValue checks value, a field of type typ as a request carries it,
// and returns it compact.
func requestValue(typ string, value json.RawMessage) (json.RawMessage, error) {
	var target any
	switch typ {
	case wire.TypeString:
		target = new(string)
	case wire.TypeInt:
		return intValue(value)
	case wire.TypeBool:
		target = new(bool)
	case wire.TypeStrings:
		target = new([]string)
	case wire.TypeMap:
		target = new([]wire.KeyValue)
	}
	if err := wire.Decode(value, target); err != nil {
		return nil, fmt.Errorf("a value of type %s is required: %w", typ, err)
	}
	return json.Marshal(target)
}

// intValue checks value, a field of type int: a JSON number with no
// fraction or exponent, within 64 bits. encoding/json fills a json.Number
// from a string that holds a number too, so a string is refused first.
func intValue(value json.RawMessage) (json.RawMessage, error) {
	if bytes.HasPrefix(bytes.TrimSpace(value), []byte(`"`)) {
		return nil, errors.New("an integer is required, not a string")
	}
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(value))
	d.UseNumber()
	if err := d.Decode(&n); err != nil {
		return nil, errors.New("an integer is required")
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("an integer within 64 bits is required, not %s", n)
	}
	return json.RawMessage(strconv.FormatInt(i, 10)), nil
}
