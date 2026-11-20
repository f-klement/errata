// Package wire holds the messages of the plugin protocol
// (docs/plugin-protocol.md) as Go types, and the strict decode both sides
// read them with. errata's plugin host writes requests and reads replies
// with it, and a plugin built from this repository imports it to read
// requests and write replies, so the two cannot drift apart. It imports
// nothing but the standard library. A plugin built from this repository
// imports this package, and internal/extract/core/lineend to count lines as
// errata does, which imports only bytes; it imports nothing else of errata.
//
// Every type decodes strictly, as the protocol asks of both sides: a member
// the type does not define, one named in another case, one written twice,
// a member of the wrong JSON type, a member the protocol does not mark as
// optional left out, and null anywhere are refused. Decode adds what a type
// cannot see on its own: the message is valid UTF-8 and one JSON value with
// nothing after it but white space. DecodeSpec reads a plugin's spec as
// strictly, and DescribeReply.Check holds a describe reply to what the
// protocol allows of its where and fields.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Protocol is the newest version of the protocol this package speaks, and
// Protocols every version it speaks, lowest first.
const Protocol = 1

// Protocols are the versions a describe request offers.
var Protocols = []int{1}

// MaxValues is the most values an extract reply may hold, each node of a
// mapping one: check.MaxValues, which errata holds every extractor to. A
// plugin stops reading at one more and replies with an error, as a built
// in reader does, so it never builds a reply errata would refuse.
const MaxValues = 100_000

// The requests' ops.
const (
	OpDescribe = "describe"
	OpBuild    = "build"
	OpExtract  = "extract"
)

// The types of a field a plugin declares, as describe's reply names them.
const (
	TypeString  = "string"
	TypeInt     = "int"
	TypeBool    = "bool"
	TypeStrings = "strings"
	TypeMap     = "map"
)

// The values of a describe reply's where.
const (
	WhereFile  = "file"
	WhereFiles = "files"
)

// The shapes of a description.
const (
	ShapeFlat   = "flat"
	ShapeMap    = "map"
	ShapeRecord = "record"
)

// Decode reads data, one message, into v, which is a pointer to one of this
// package's types. It refuses data that is not valid UTF-8, that holds a
// null, or that holds more than one JSON value, and then decodes it with
// v's own strict decode.
func Decode(data []byte, v any) error {
	if !utf8.Valid(data) {
		return errors.New("the message is not valid UTF-8")
	}
	if err := oneValueNoNull(data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// oneValueNoNull refuses data unless it is one JSON value, with only white
// space after it, that holds no null.
func oneValueNoNull(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	depth := 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return errors.New("the message is empty")
		}
		if err != nil {
			return err
		}
		if tok == nil {
			return errors.New("the message holds null, which is no value for any member")
		}
		depth += nesting(tok)
		if depth == 0 {
			break
		}
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("the message holds more than one JSON value")
	}
	return nil
}

// nesting is how far tok moves the depth of a JSON value: +1 for an opening
// delimiter, -1 for a closing one, 0 for anything else.
func nesting(tok json.Token) int {
	switch tok {
	case json.Delim('{'), json.Delim('['):
		return 1
	case json.Delim('}'), json.Delim(']'):
		return -1
	}
	return 0
}

// DecodeSpec reads data, the spec of an entry as a build request carries
// it, into v, a pointer to a plugin's struct of its fields, as strictly as
// this package reads its own messages: Decode's checks, then a member v
// does not define, one named in another case than v's, and one written
// twice are refused.
func DecodeSpec(data []byte, v any) error {
	if !utf8.Valid(data) {
		return errors.New("the spec is not valid UTF-8")
	}
	if err := oneValueNoNull(data); err != nil {
		return err
	}
	return strict(data, v)
}

// strict decodes data, a JSON object, into v, a pointer to a struct with
// no methods of its own, refusing a member v does not define, one named in
// another case than v's json tag, and one written twice, and then refuses
// it when a member named in required is left out. encoding/json alone
// would read a member in any case and keep the last of two.
func strict(data []byte, v any, required ...string) error {
	if err := exactNames(data, v); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	return present(data, required...)
}

// exactNames refuses data, a JSON object, when it writes a member twice or
// names one of v's members in another case. A member v does not define at
// all is left to the decode, which names it.
func exactNames(data []byte, v any) error {
	names, err := memberNames(data)
	if err != nil {
		return err
	}
	tags := jsonNames(reflect.TypeOf(v).Elem())
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			return fmt.Errorf("the member %q is written twice", n)
		}
		seen[n] = true
		if tag, ok := foldMatch(n, tags); ok && tag != n {
			return fmt.Errorf("the member %q must be written %q", n, tag)
		}
	}
	return nil
}

// memberNames are the names of data's members, data a JSON object, in
// their order, duplicates kept.
func memberNames(data []byte) ([]string, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("an object is required")
	}
	var names []string
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return nil, err
		}
		var skip json.RawMessage
		if err := d.Decode(&skip); err != nil {
			return nil, err
		}
		names = append(names, k.(string))
	}
	return names, nil
}

// jsonNames are the member names of t, a struct, as its json tags name
// them.
func jsonNames(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// foldMatch is the first of names that n matches when case is ignored.
func foldMatch(n string, names []string) (string, bool) {
	for _, name := range names {
		if strings.EqualFold(n, name) {
			return name, true
		}
	}
	return "", false
}

// present refuses data, a JSON object, unless it holds every member of
// names.
func present(data []byte, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	for _, n := range names {
		if _, ok := members[n]; !ok {
			return fmt.Errorf("the member %q is missing", n)
		}
	}
	return nil
}

// members is the set of the members data, a JSON object, holds.
func members(data []byte) (map[string]bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out, nil
}

// DescribeRequest asks a plugin to describe itself and choose a version.
type DescribeRequest struct {
	Op        string `json:"op"`
	Protocols []int  `json:"protocols"`
}

// UnmarshalJSON reads a describe request strictly (Decode).
func (r *DescribeRequest) UnmarshalJSON(data []byte) error {
	type plain DescribeRequest
	return strict(data, (*plain)(r), "op", "protocols")
}

// Version is the part of a describe reply that is the same in every version
// of the protocol: the version the plugin chose, or the versions it speaks
// when it speaks none of those offered, and its name and release. It
// decodes leniently, since the rest of the reply is the chosen version's.
type Version struct {
	Protocol  int    `json:"protocol,omitempty"`
	Protocols []int  `json:"protocols,omitempty"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// Chose reports whether the reply chose a version, rather than listing the
// versions the plugin speaks.
func (v Version) Chose() bool { return v.Protocols == nil }

// ReadVersion reads the members of a describe reply that every version
// shares, and refuses one that holds neither or both of protocol and
// protocols, or leaves out name or version.
func ReadVersion(data []byte) (Version, error) {
	if err := Decode(data, &json.RawMessage{}); err != nil {
		return Version{}, err
	}
	var v Version
	if err := json.Unmarshal(data, &v); err != nil {
		return Version{}, err
	}
	m, err := members(data)
	if err != nil {
		return Version{}, err
	}
	if m["protocol"] == m["protocols"] {
		return Version{}, errors.New("the describe reply holds neither or both of protocol and protocols")
	}
	if err := present(data, "name", "version"); err != nil {
		return Version{}, err
	}
	return v, nil
}

// DescribeReply is a plugin's description of itself in protocol 1: the
// version it chose, its name and release, where an entry of its kind reads,
// and the fields an entry takes besides errata's.
type DescribeReply struct {
	Protocol int     `json:"protocol"`
	Name     string  `json:"name"`
	Version  string  `json:"version"`
	Where    string  `json:"where"`
	Fields   []Field `json:"fields"`
}

// UnmarshalJSON reads a describe reply strictly, with every member protocol 1 requires.
func (r *DescribeReply) UnmarshalJSON(data []byte) error {
	type plain DescribeReply
	return strict(data, (*plain)(r), "protocol", "name", "version", "where", "fields")
}

// Check refuses a describe reply whose where or fields protocol 1 does not
// allow. errata checks every reply with it, and a plugin's tests can check
// the reply the plugin writes.
func (r DescribeReply) Check() error {
	if r.Where != WhereFile && r.Where != WhereFiles {
		return fmt.Errorf("where is %s, neither file nor files", strconv.Quote(r.Where))
	}
	seen := map[string]bool{}
	for _, f := range r.Fields {
		if err := f.check(seen); err != nil {
			return err
		}
		seen[f.Name] = true
	}
	return nil
}

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// check refuses a field whose name or type the protocol does not allow,
// or whose name is among seen, the names declared before it.
func (f Field) check(seen map[string]bool) error {
	switch {
	case !fieldName.MatchString(f.Name):
		return fmt.Errorf("the field name %s is not lower case letters, digits and _, starting with a letter", strconv.Quote(f.Name))
	case f.Name == "file" || f.Name == "files" || f.Name == "max_bytes":
		return fmt.Errorf("the field %s is errata's", f.Name)
	case seen[f.Name]:
		return fmt.Errorf("the field %s is declared twice", f.Name)
	case !slices.Contains([]string{TypeString, TypeInt, TypeBool, TypeStrings, TypeMap}, f.Type):
		return fmt.Errorf("the field %s has the type %s, which protocol 1 does not have", f.Name, strconv.Quote(f.Type))
	}
	return nil
}

// Field is one key an entry of the plugin's kind takes.
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
}

// UnmarshalJSON reads a field of a describe reply strictly, with its name and type.
func (f *Field) UnmarshalJSON(data []byte) error {
	type plain Field
	return strict(data, (*plain)(f), "name", "type")
}

// BuildRequest sends every entry of the plugin's kind in a config.
type BuildRequest struct {
	Protocol int     `json:"protocol"`
	Op       string  `json:"op"`
	Entries  []Entry `json:"entries"`
}

// UnmarshalJSON reads a build request strictly, with every member it requires.
func (r *BuildRequest) UnmarshalJSON(data []byte) error {
	type plain BuildRequest
	return strict(data, (*plain)(r), "protocol", "op", "entries")
}

// Entry is one entry of a build request: the id of the rule it belongs to,
// and the plugin's fields as the entry writes them, a JSON object.
type Entry struct {
	Rule string          `json:"rule"`
	Spec json.RawMessage `json:"spec"`
}

// UnmarshalJSON reads an entry of a build request strictly, with its rule and spec.
func (e *Entry) UnmarshalJSON(data []byte) error {
	type plain Entry
	return strict(data, (*plain)(e), "rule", "spec")
}

// BuildReply is the result of each entry of a build request, in order.
type BuildReply struct {
	Built []Built `json:"built"`
}

// UnmarshalJSON reads a build reply strictly, with its results.
func (r *BuildReply) UnmarshalJSON(data []byte) error {
	type plain BuildReply
	return strict(data, (*plain)(r), "built")
}

// Built is the result of one entry: its spec with every default written
// out and its description, or the error the plugin refuses the spec with.
type Built struct {
	Spec        json.RawMessage `json:"spec,omitempty"`
	Description *Description    `json:"description,omitempty"`
	Error       *string         `json:"error,omitempty"`
}

// UnmarshalJSON reads one result of a build reply: error alone, or spec and description.
func (b *Built) UnmarshalJSON(data []byte) error {
	type plain Built
	if err := strict(data, (*plain)(b)); err != nil {
		return err
	}
	m, err := members(data)
	if err != nil {
		return err
	}
	if m["error"] {
		if len(m) != 1 {
			return errors.New("a result that holds error holds nothing else")
		}
		return nil
	}
	return present(data, "spec", "description")
}

// Description is what an entry's extractor returns, as check.Description
// holds it for a built in kind.
type Description struct {
	Label      string   `json:"label"`
	NotLiteral bool     `json:"not_literal"`
	Shape      string   `json:"shape"`
	Fields     []string `json:"fields"`
	Reads      []string `json:"reads"`
}

// UnmarshalJSON reads an entry's description strictly, with every member.
func (d *Description) UnmarshalJSON(data []byte) error {
	type plain Description
	return strict(data, (*plain)(d), "label", "not_literal", "shape", "fields", "reads")
}

// ExtractRequest asks for the values of one entry in its files.
type ExtractRequest struct {
	Protocol int                `json:"protocol"`
	Op       string             `json:"op"`
	Spec     json.RawMessage    `json:"spec"`
	Files    []File             `json:"files"`
	Values   map[string][]Value `json:"values"`
}

// UnmarshalJSON reads an extract request strictly, with every member it requires.
func (r *ExtractRequest) UnmarshalJSON(data []byte) error {
	type plain ExtractRequest
	return strict(data, (*plain)(r), "protocol", "op", "spec", "files", "values")
}

// File is one file of an extract request: its path from the root, with
// slashes, and its bytes, which JSON carries as base64.
type File struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

// UnmarshalJSON reads a file of an extract request strictly, with its path and data.
func (f *File) UnmarshalJSON(data []byte) error {
	type plain File
	return strict(data, (*plain)(f), "path", "data")
}

// ExtractReply is the values a plugin found, or the error it could not read
// its files for, or why it found no value. Exactly one is set: Values is
// set, and maybe empty, when the reply holds values.
type ExtractReply struct {
	Values  []Value `json:"values,omitempty"`
	Error   *string `json:"error,omitempty"`
	NoValue *string `json:"no_value,omitempty"`
}

// UnmarshalJSON reads an extract reply that holds exactly one of values, error and no_value.
func (r *ExtractReply) UnmarshalJSON(data []byte) error {
	type plain ExtractReply
	if err := strict(data, (*plain)(r)); err != nil {
		return err
	}
	m, err := members(data)
	if err != nil {
		return err
	}
	if len(m) != 1 {
		return errors.New("an extract reply holds exactly one of values, error and no_value")
	}
	if m["values"] && r.Values == nil {
		r.Values = []Value{}
	}
	return nil
}

// MarshalJSON writes r with its one member, values even when it is empty.
func (r ExtractReply) MarshalJSON() ([]byte, error) {
	switch {
	case r.Error != nil:
		return json.Marshal(struct {
			Error string `json:"error"`
		}{*r.Error})
	case r.NoValue != nil:
		return json.Marshal(struct {
			NoValue string `json:"no_value"`
		}{*r.NoValue})
	}
	values := r.Values
	if values == nil {
		values = []Value{}
	}
	return json.Marshal(struct {
		Values []Value `json:"values"`
	}{values})
}

// Value is one value of an extract reply or of an extract request's values.
// Text is set for a flat value, and Data for one handed over whole.
type Value struct {
	Text       *string `json:"text,omitempty"`
	File       string  `json:"file"`
	Line       int     `json:"line"`
	NotLiteral bool    `json:"not_literal,omitempty"`
	Key        *string `json:"key,omitempty"`
	Condition  *string `json:"condition,omitempty"`
	Data       *Node   `json:"data,omitempty"`
}

// UnmarshalJSON reads a value strictly, with its file and line.
func (v *Value) UnmarshalJSON(data []byte) error {
	type plain Value
	return strict(data, (*plain)(v), "file", "line")
}

// Node is one node of a value handed over whole: a scalar, a mapping or a
// list, with the line it starts on. Exactly one of Scalar, Map and List is
// set; a Map or a List that is set and empty is an empty slice, not nil.
type Node struct {
	Line   int     `json:"line"`
	Scalar *string `json:"scalar,omitempty"`
	Map    []Pair  `json:"map,omitempty"`
	List   []Node  `json:"list,omitempty"`
}

// UnmarshalJSON reads a node that holds its line and exactly one of scalar, map and list.
func (n *Node) UnmarshalJSON(data []byte) error {
	type plain Node
	if err := strict(data, (*plain)(n), "line"); err != nil {
		return err
	}
	// A map or a list that is set and empty decodes as an empty slice, not
	// nil, so only the count of members is left to check.
	m, err := members(data)
	if err != nil {
		return err
	}
	if len(m) != 2 {
		return errors.New("a node holds line and exactly one of scalar, map and list")
	}
	return nil
}

// MarshalJSON writes n with its line and its one kind, a map or a list even
// when it is empty.
func (n Node) MarshalJSON() ([]byte, error) {
	switch {
	case n.Scalar != nil:
		return json.Marshal(struct {
			Line   int    `json:"line"`
			Scalar string `json:"scalar"`
		}{n.Line, *n.Scalar})
	case n.Map != nil:
		return json.Marshal(struct {
			Line int    `json:"line"`
			Map  []Pair `json:"map"`
		}{n.Line, n.Map})
	}
	list := n.List
	if list == nil {
		list = []Node{}
	}
	return json.Marshal(struct {
		Line int    `json:"line"`
		List []Node `json:"list"`
	}{n.Line, list})
}

// Pair is one key of a mapping node and its value.
type Pair struct {
	Key   string `json:"key"`
	Value Node   `json:"value"`
}

// UnmarshalJSON reads a pair of a mapping node strictly, with its key and value.
func (p *Pair) UnmarshalJSON(data []byte) error {
	type plain Pair
	return strict(data, (*plain)(p), "key", "value")
}

// KeyValue is one key of a field of type map in a spec: {"key": k,
// "value": v}, in the config's order.
type KeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UnmarshalJSON reads a key and value of a map field strictly.
func (kv *KeyValue) UnmarshalJSON(data []byte) error {
	type plain KeyValue
	return strict(data, (*plain)(kv), "key", "value")
}
