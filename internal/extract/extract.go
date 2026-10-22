// Package extract builds the extractors a rule names in errata.yaml. Each one
// reads one file and returns every value it finds there, with its location.
// Each kind is a reader in a package of its own below this one (yaml, json,
// toml, regex, text, xml, ini, properties), and the readers share
// internal/extract/core; no reader imports another. This package holds what
// sees every kind: the Spec a rule decodes into, the kinds table, Build,
// Place and Kind.
//
// The types below are the seam between the config package, which decodes a
// rule, and this package, which runs its extractors. Their JSON names are the
// keys a user writes under a rule's extract:.
package extract

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/properties"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/xml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// Spec is one named entry under a rule's extract:. Exactly one field is set.
type Spec struct {
	YAML  *YAML  `json:"yaml,omitempty"`
	JSON  *JSON  `json:"json,omitempty"`
	TOML  *TOML  `json:"toml,omitempty"`
	Regex *Regex `json:"regex,omitempty"`
	Text  *Text  `json:"text,omitempty"`
	XML   *XML   `json:"xml,omitempty"`

	INI        *INI        `json:"ini,omitempty"`
	Properties *Properties `json:"properties,omitempty"`
}

// Each kind takes MaxBytes, the largest file it reads. Zero, or the key
// left out, is the default of 32 MiB.

// YAML reads a path in a YAML file, as yaml.Spec says.
type YAML = yaml.Spec

// JSON reads a path in a JSON file, as json.Spec says.
type JSON = json.Spec

// TOML reads a path in a TOML file, as toml.Spec says.
type TOML = toml.Spec

// Regex returns capture group 1 of every match of a pattern in a file, as
// regex.Spec says.
type Regex = regex.Spec

// Text returns capture group 1 of every match of a pattern in the prose of
// a Markdown file, as text.Spec says.
type Text = text.Spec

// XML returns the values an XPath selects in an XML file, as xml.Spec says.
type XML = xml.Spec

// INI returns every value of a key in a section of an INI file, as ini.Spec
// says.
type INI = ini.Spec

// Properties returns every value of a key in a Java .properties file, as
// properties.Spec says.
type Properties = properties.Spec

// Place returns s with only what decides where it reads: each file cleaned,
// so f, ./f and a/../f are one file, and max_bytes left out, since a limit
// does not change what is read. Two specs with one place return the same
// values at the same locations.
func (s Spec) Place() Spec {
	var p Spec
	if s.YAML != nil {
		y := *s.YAML
		y.File, y.MaxBytes = filepath.Clean(y.File), 0
		p.YAML = &y
	}
	if s.JSON != nil {
		j := *s.JSON
		j.File, j.MaxBytes = filepath.Clean(j.File), 0
		p.JSON = &j
	}
	if s.TOML != nil {
		t := *s.TOML
		t.File, t.MaxBytes = filepath.Clean(t.File), 0
		p.TOML = &t
	}
	if s.Regex != nil {
		r := *s.Regex
		r.File, r.MaxBytes = filepath.Clean(r.File), 0
		p.Regex = &r
	}
	if s.Text != nil {
		x := *s.Text
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.Text = &x
	}
	if s.XML != nil {
		x := *s.XML
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.XML = &x
	}
	if s.INI != nil {
		i := ini.Place(*s.INI)
		p.INI = &i
	}
	if s.Properties != nil {
		x := *s.Properties
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.Properties = &x
	}
	return p
}

// Build checks s and returns its extractor. A Spec that sets no kind, or more
// than one, is a load error, and so is a file outside the repository, a
// max_bytes below zero or over its ceiling, a path that does not parse, or a
// pattern that does not compile.
func Build(s Spec) (check.Extractor, error) {
	var names []string
	var set []extractorKind
	for _, k := range s.kinds() {
		names = append(names, k.name)
		if k.set {
			set = append(set, k)
		}
	}
	if len(set) != 1 {
		return nil, fmt.Errorf("an extractor sets exactly one of %s", strings.Join(names, ", "))
	}
	return set[0].build()
}

// extractorKind is one key of Spec: whether it is set, and how to build it.
// The count of set kinds and the dispatch both come from kinds, so a kind
// cannot be counted without being built. TestKindsCoverSpec fails when Spec
// gains a field that kinds does not list.
type extractorKind struct {
	name  string
	set   bool
	build func() (check.Extractor, error)
}

func (s Spec) kinds() []extractorKind {
	return []extractorKind{
		{"yaml", s.YAML != nil, func() (check.Extractor, error) { return yaml.Build(*s.YAML) }},
		{"json", s.JSON != nil, func() (check.Extractor, error) { return json.Build(*s.JSON) }},
		{"toml", s.TOML != nil, func() (check.Extractor, error) { return toml.Build(*s.TOML) }},
		{"regex", s.Regex != nil, func() (check.Extractor, error) { return regex.Build(*s.Regex) }},
		{"text", s.Text != nil, func() (check.Extractor, error) { return text.Build(*s.Text) }},
		{"xml", s.XML != nil, func() (check.Extractor, error) { return xml.Build(*s.XML) }},
		{"ini", s.INI != nil, func() (check.Extractor, error) { return ini.Build(*s.INI) }},
		{"properties", s.Properties != nil, func() (check.Extractor, error) { return properties.Build(*s.Properties) }},
	}
}

// Kind names the kind of extractor s sets, for a message: its key, and for
// ini the dialect it reads in and the file's name, since what a dialect's
// tool computes can hang on the file: "ini, plain dialect on pytest.ini".
// It is "" when s sets no kind or more than one, or names no dialect errata
// reads.
func (s Spec) Kind() string {
	kind := ""
	for _, k := range s.kinds() {
		if k.set {
			if kind != "" {
				return ""
			}
			kind = k.name
		}
	}
	if kind == "ini" {
		dialect, ok := ini.Describe(*s.INI)
		if !ok {
			return ""
		}
		kind += ", " + dialect
	}
	return kind
}
