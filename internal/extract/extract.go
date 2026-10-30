// Package extract builds the extractors a rule names in errata.yaml. Each one
// reads one file and returns every value it finds there, with its location.
// Each extractor kind, the key an extract: entry names, is a reader in a
// package of its own below this one (yaml, json, toml, regex, text, xml,
// ini, properties, toolversion, packageversion, envfile, gitlabci, shell,
// githubactions, jenkins, and filestat for exists and size), and the
// readers share internal/extract/core; no reader imports another. The env
// kind asks those that declare themselves env readers for one variable
// across several files, through the functions this package hands it. Below, a kind is always an
// extractor kind, never a check's.
//
// Each reader declares its extractor kind beside its Spec, as a core.Kind:
// its key, its Place, Resolve and Describe, and for an env reader its Env. This package lists every
// kind once, in kinds, each with its Build and the parsers and readers it
// wires, as a check.ExtractorKind, and holds the Spec a rule's extract:
// entry decodes into, which finds its kind by key.
package extract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/env"
	"github.com/f-klement/errata/internal/extract/envfile"
	"github.com/f-klement/errata/internal/extract/filestat"
	"github.com/f-klement/errata/internal/extract/githubactions"
	"github.com/f-klement/errata/internal/extract/gitlabci"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/jenkins"
	ejson "github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/packageversion"
	"github.com/f-klement/errata/internal/extract/properties"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/shell"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/toolversion"
	"github.com/f-klement/errata/internal/extract/xml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// kinds are the kinds a rule's extract: entry may set, in the order a
// message lists their keys. A kind is one line here: the reader's Kind and
// its Build, given the parsers or readers it needs.
var kinds = registry(
	register(yaml.Kind, with(yaml.Build, parsers)),
	register(ejson.Kind, with(ejson.Build, parsers)),
	register(toml.Kind, with(toml.Build, parsers)),
	register(regex.Kind, regex.Build),
	register(text.Kind, text.Build),
	register(xml.Kind, xml.Build),
	register(ini.Kind, ini.Build),
	register(properties.Kind, properties.Build),
	// tool_version and package_version read their JSON and TOML files with
	// the json and toml readers' parsers, since a reader imports no other.
	register(toolversion.Kind, with(toolversion.Build, parsers)),
	register(packageversion.Kind, with(packageversion.Build, parsers)),
	register(envfile.Kind, envfile.Build),
	// gitlab-ci and github-actions read their YAML with the yaml reader's
	// parser.
	register(gitlabci.Kind, with(gitlabci.Build, yaml.ParseNoted)),
	register(shell.Kind, shell.Build),
	register(githubactions.Kind, with(githubactions.Build, yaml.ParseNoted)),
	register(jenkins.Kind, jenkins.Build),
	// env asks the kinds above that declare themselves env readers;
	// envReaders is filled from kinds once kinds is, so env's Build reads it
	// when it is called.
	register(env.Kind, func(s env.Spec) (check.Extractor, error) { return env.Build(s, envReaders) }),
	register(filestat.ExistsKind, filestat.BuildExists),
	register(filestat.SizeKind, filestat.BuildSize),
)

// envReaders are the readers the env kind asks, each for one variable in
// one file: every kind of kinds that declares itself an env reader
// (core.EnvReader), and the shell reader's test of a shebang, which
// chooses it for a script its name does not.
var envReaders env.Readers

func init() { envReaders = readersOf(kinds) }

// readersOf are the env readers of t's kinds. Two at one Order, one with
// no Chooses or no Read, or one that sniffs no bytes, is a bug in a reader's declaration, so it
// panics when the program starts and every test fails: two at one Order
// would leave env's messages, and the reader of a name both take, to the
// order of kinds.
func readersOf(t table) env.Readers {
	var r env.Readers
	at := map[int]string{}
	for _, k := range t.order {
		if k.Env == nil {
			continue
		}
		if k.Env.Chooses == nil || k.Env.Read == nil {
			panic(fmt.Sprintf("extract: the env reader %q has no Chooses or no Read", k.Key))
		}
		if k.Env.Sniffs != nil && k.Env.SniffBytes <= 0 {
			panic(fmt.Sprintf("extract: the env reader %q sniffs no bytes: its SniffBytes is %d", k.Key, k.Env.SniffBytes))
		}
		if other, dup := at[k.Env.Order]; dup {
			panic(fmt.Sprintf("extract: the env readers %q and %q are both at order %d", other, k.Key, k.Env.Order))
		}
		at[k.Env.Order] = k.Key
		r.List = append(r.List, env.Reader{Name: k.Key, EnvReader: *k.Env})
	}
	return r
}

// parsers are the yaml, json and toml readers' parsers, which tool_version
// and package_version read their JSON and TOML files with, and the yaml,
// json and toml readers the documents embedded in strings.
var parsers = core.Parsers{
	YAML: func(data []byte, expanded int) ([]*core.Node, int, error) {
		return yaml.ParseNoted(data, expanded, nil)
	},
	JSON:  ejson.Parse,
	TOML:  toml.Parse,
	Noted: yaml.ParseNoted,
}

// with is build given w, the parsers or readers it takes beside its spec.
func with[S, W any](build func(S, W) (check.Extractor, error), w W) func(S) (check.Extractor, error) {
	return func(s S) (check.Extractor, error) { return build(s, w) }
}

// kind is one extractor kind of the registry, as check names it.
type kind = check.ExtractorKind

// register is the extractor kind k declares, with build, over a spec held
// as any, which is always an S. A function k leaves out stays nil, which
// TestEveryKindRoundTrips reports. Its Describe carries the max_bytes the
// spec sets, which S states (core.Bounded), so no kind's own Describe
// repeats it.
func register[S core.Bounded](k core.Kind[S], build func(S) (check.Extractor, error)) *kind {
	r := &kind{Key: k.Key, Spec: reflect.TypeFor[S](), Decode: func(raw json.RawMessage) (any, error) { return decodeSpec[S](k.Key, raw) }}
	if build != nil {
		r.Build = func(s any) (check.Extractor, error) { return build(s.(S)) }
	}
	if k.Place != nil {
		r.Place = func(s any) any { return k.Place(s.(S)) }
	}
	if k.Resolve != nil {
		r.Resolve = func(s any) any { return k.Resolve(s.(S)) }
	}
	if k.Describe != nil {
		r.Describe = func(s any) check.Description {
			d := k.Describe(s.(S))
			d.MaxBytes = s.(S).Bound()
			return d
		}
	}
	if e := k.Env; e != nil {
		r.Env = &check.EnvReader{Order: e.Order, Chooses: e.Chooses, Sniffs: e.Sniffs, SniffBytes: e.SniffBytes}
		if e.Read != nil {
			r.Env.Read = func(file, name string, maxBytes int64) (check.Extractor, error) {
				return e.Read(file, name, maxBytes, parsers)
			}
		}
	}
	return r
}

// missing names each function of k that is nil.
func missing(k *kind) []string {
	var out []string
	for _, f := range []struct {
		name   string
		absent bool
	}{{"Decode", k.Decode == nil}, {"Build", k.Build == nil}, {"Place", k.Place == nil}, {"Resolve", k.Resolve == nil}, {"Describe", k.Describe == nil}} {
		if f.absent {
			out = append(out, f.name)
		}
	}
	return out
}

// decodeSpec reads raw strictly as an S, the spec of the kind under key: a key
// the spec does not have is an error, as it is anywhere in a config. A
// type error names the field under the kind's key, so the decode of the
// whole entry names it from the rule down.
func decodeSpec[S any](key string, raw json.RawMessage) (any, error) {
	var v S
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			te.Field = join(key, te.Field)
			if te.Struct == "" {
				te.Struct = "Spec" // as the entry's own decode names it
			}
		}
		return nil, err
	}
	return v, nil
}

func join(key, field string) string {
	if field == "" {
		return key
	}
	return key + "." + field
}

// table is the registry: the kinds in order, and each by its key.
type table struct {
	order []*kind
	byKey map[string]*kind
	keys  []string
	// schema is the struct a Spec is written as, for Schema.
	schema reflect.Type
}

// registry is the table of ks. Two kinds under one key, or one key under
// two spec types, is a bug in the list above, so it panics when the
// program starts and every test fails.
func registry(ks ...*kind) table {
	t := table{byKey: map[string]*kind{}}
	var fields []reflect.StructField
	for i, k := range ks {
		if _, dup := t.byKey[k.Key]; dup || k.Key == "" {
			panic(fmt.Sprintf("extract: the kind %q is registered twice, or has no key", k.Key))
		}
		t.order = append(t.order, k)
		t.byKey[k.Key] = k
		t.keys = append(t.keys, k.Key)
		fields = append(fields, reflect.StructField{
			Name: "Kind" + strconv.Itoa(i),
			Type: reflect.PointerTo(k.Spec),
			Tag:  reflect.StructTag(`json:"` + k.Key + `,omitempty"`),
		})
	}
	t.schema = reflect.StructOf(fields)
	return t
}

// Keys are the keys a rule's extract: entry chooses its kind by, in the
// order a message lists them.
func Keys() []string { return append([]string(nil), kinds.keys...) }

// Schema is the struct type an extract: entry is written as: one optional
// field for each kind, under its key, of the kind's spec type. config
// reads a rule's YAML against it, so a string field of a kind is held as
// written, and checks each key's case and each string's YAML 1.1 reading
// against it, as it does for every other key of a check.
func Schema() reflect.Type { return kinds.schema }

// Spec is one named entry under a rule's extract:: the kind its key
// chooses, and the kind's spec. Exactly one kind is set for Build to
// build it; an entry that sets none, or more than one, decodes, so the
// load error names the extractor (Build). The zero Spec sets none.
type Spec struct {
	set []entry
}

// entry is one key of a Spec and the kind's spec under it.
type entry struct {
	kind *kind
	spec any
}

// Of is the Spec that sets the kind k with the spec s, as an extract:
// entry that writes s under k's key decodes. k must be a kind kinds
// lists.
func Of[S any](k core.Kind[S], s S) Spec {
	r, ok := kinds.byKey[k.Key]
	if !ok || r.Spec != reflect.TypeFor[S]() {
		panic(fmt.Sprintf("extract: no kind %q of %v is registered", k.Key, reflect.TypeFor[S]()))
	}
	return Spec{set: []entry{{r, s}}}
}

// UnmarshalJSON reads an extract: entry: an object whose every key is a
// kind's, each decoded strictly into that kind's spec. A key written null
// sets no kind, as an entry that leaves it out. A key no kind has is
// refused, as a check's unknown key is; a key in the wrong case is
// config's to refuse first, with the key it should be.
func (s *Spec) UnmarshalJSON(raw []byte) error {
	*s = Spec{}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil // null sets no kind
	}
	if tok != json.Delim('{') {
		return &json.UnmarshalTypeError{Value: jsonKind(tok), Type: reflect.TypeFor[Spec](), Offset: d.InputOffset()}
	}
	got := map[*kind]any{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return err
		}
		name, _ := key.(string)
		k, ok := kinds.byKey[name]
		if !ok {
			return fmt.Errorf("json: unknown field %q", name)
		}
		if bytes.Equal(value, []byte("null")) {
			delete(got, k)
			continue
		}
		spec, err := k.Decode(value)
		if err != nil {
			return err
		}
		got[k] = spec
	}
	for _, k := range kinds.order {
		if spec, ok := got[k]; ok {
			s.set = append(s.set, entry{k, spec})
		}
	}
	return nil
}

// jsonKind names the JSON value tok opens, as encoding/json's type error
// does.
func jsonKind(tok json.Token) string {
	switch tok.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case json.Number, float64:
		return "number"
	default:
		return "array"
	}
}

// MarshalJSON writes s as errata.yaml spells it: each kind it sets under its
// key, such as {"yaml": {"file": ...}}, in the order of kinds. It escapes
// no <, > or &, so the encoder that asked escapes them only when it is set
// to, as it does a struct's fields.
func (s Spec) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	b.WriteByte('{')
	for i, e := range s.set {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := enc.Encode(e.kind.Key); err != nil {
			return nil, err
		}
		b.WriteByte(':')
		if err := enc.Encode(e.spec); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// each is s with f applied to the spec of each kind it sets.
func (s Spec) each(f func(*kind, any) any) Spec {
	var out Spec
	for _, e := range s.set {
		out.set = append(out.set, entry{e.kind, f(e.kind, e.spec)})
	}
	return out
}

// Place returns s with only what decides where it reads: each file cleaned,
// so f, ./f and a/../f are one file, and max_bytes left out, since a limit
// does not change what is read. Two specs with one place read the same
// files, and return the same values at the same locations wherever both
// return values. Each kind's Place says what else it keeps or writes out.
func (s Spec) Place() Spec {
	return s.each(func(k *kind, v any) any { return k.Place(v) })
}

// Resolved returns s with the defaults Build fills in written out: the
// max_bytes of the kind it sets, core.DefaultMaxBytes when s leaves it out,
// and for ini the dialect Build chooses by the file's name when s names none.
// errata list prints it, so a consumer reads the limit and the dialect a
// check runs with, not only what its author wrote. s is not changed, and
// what Resolved returns shares no slice, map or pointer with it.
func (s Spec) Resolved() Spec {
	return s.each(func(k *kind, v any) any { return k.Resolve(v) })
}

// Build checks s and returns its extractor. A Spec that sets no kind, or more
// than one, is a load error, and so is a file outside the repository, a
// max_bytes below zero or over its ceiling, a path that does not parse, or a
// pattern that does not compile.
func Build(s Spec) (check.Extractor, error) {
	if len(s.set) != 1 {
		return nil, fmt.Errorf("an extractor sets exactly one of %s", strings.Join(kinds.keys, ", "))
	}
	return s.set[0].kind.Build(s.set[0].spec)
}

// Describe is what the extractor s builds returns, as its kind tells it
// from the spec (check.Description). It is the zero Description when s
// sets no kind or more than one.
func (s Spec) Describe() check.Description {
	if len(s.set) != 1 {
		return check.Description{}
	}
	return s.set[0].kind.Describe(s.set[0].spec)
}
