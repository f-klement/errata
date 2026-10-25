// Package extract builds the extractors a rule names in errata.yaml. Each one
// reads one file and returns every value it finds there, with its location.
// Each kind is a reader in a package of its own below this one (yaml, json,
// toml, regex, text, xml, ini, properties, toolversion, envfile, gitlabci,
// shell, githubactions, jenkins), and the readers share internal/extract/core;
// no reader imports another. This package holds what sees every kind: the
// Spec a rule decodes into, the kinds table, Build, Place and Kind.
//
// The types below are the seam between the config package, which decodes a
// rule, and this package, which runs its extractors. Their JSON names are the
// keys a user writes under a rule's extract:.
package extract

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/envfile"
	"github.com/f-klement/errata/internal/extract/githubactions"
	"github.com/f-klement/errata/internal/extract/gitlabci"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/jenkins"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/properties"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/shell"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/toolversion"
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

	ToolVersion *ToolVersion `json:"tool_version,omitempty"`

	EnvFile *EnvFile `json:"envfile,omitempty"`

	GitLabCI *GitLabCI `json:"gitlab-ci,omitempty"`

	Shell *Shell `json:"shell,omitempty"`

	GitHubActions *GitHubActions `json:"github-actions,omitempty"`

	Jenkins *Jenkins `json:"jenkins,omitempty"`
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

// ToolVersion returns every version of a tool in the files that pin it, as
// toolversion.Spec says.
type ToolVersion = toolversion.Spec

// EnvFile returns every value of a key in a dotenv file, or of every key,
// as envfile.Spec says.
type EnvFile = envfile.Spec

// GitLabCI returns the value one job of a GitLab CI file sees, for a
// variable or a keyword, as gitlabci.Spec says.
type GitLabCI = gitlabci.Spec

// Shell returns every value a shell script gives a variable, as shell.Spec
// says.
type Shell = shell.Spec

// GitHubActions returns the value one step or job of a GitHub Actions
// workflow sees, for an environment variable or a key, as
// githubactions.Spec says.
type GitHubActions = githubactions.Spec

// Jenkins returns the value of an environment variable that a stage of a
// Jenkinsfile sees, as jenkins.Spec says.
type Jenkins = jenkins.Spec

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
	if s.ToolVersion != nil {
		t := toolversion.Place(*s.ToolVersion)
		p.ToolVersion = &t
	}
	if s.EnvFile != nil {
		x := *s.EnvFile
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.EnvFile = &x
	}
	if s.GitLabCI != nil {
		g := gitlabci.Place(*s.GitLabCI)
		p.GitLabCI = &g
	}
	if s.Shell != nil {
		x := shell.Place(*s.Shell)
		p.Shell = &x
	}
	if s.GitHubActions != nil {
		x := githubactions.Place(*s.GitHubActions)
		p.GitHubActions = &x
	}
	if s.Jenkins != nil {
		x := jenkins.Place(*s.Jenkins)
		p.Jenkins = &x
	}
	return p
}

// Resolved returns s with the defaults Build fills in written out: the
// max_bytes of the kind it sets, core.DefaultMaxBytes when s leaves it out,
// and for ini the dialect Build chooses by the file's name when s names none.
// errata list prints it, so a consumer reads the limit and the dialect a
// check runs with, not only what its author wrote. s is not changed.
func (s Spec) Resolved() Spec {
	var r Spec
	if s.YAML != nil {
		y := *s.YAML
		y.MaxBytes = core.MaxBytes(y.MaxBytes)
		r.YAML = &y
	}
	if s.JSON != nil {
		j := *s.JSON
		j.MaxBytes = core.MaxBytes(j.MaxBytes)
		r.JSON = &j
	}
	if s.TOML != nil {
		t := *s.TOML
		t.MaxBytes = core.MaxBytes(t.MaxBytes)
		r.TOML = &t
	}
	if s.Regex != nil {
		x := *s.Regex
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.Regex = &x
	}
	if s.Text != nil {
		x := *s.Text
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.Text = &x
	}
	if s.XML != nil {
		x := *s.XML
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.XML = &x
	}
	if s.INI != nil {
		i := ini.Resolve(*s.INI)
		i.MaxBytes = core.MaxBytes(i.MaxBytes)
		r.INI = &i
	}
	if s.Properties != nil {
		x := *s.Properties
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.Properties = &x
	}
	if s.ToolVersion != nil {
		x := *s.ToolVersion
		x.Files = slices.Clone(x.Files)
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.ToolVersion = &x
	}
	if s.EnvFile != nil {
		x := *s.EnvFile
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.EnvFile = &x
	}
	if s.GitLabCI != nil {
		x := *s.GitLabCI
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.GitLabCI = &x
	}
	if s.Shell != nil {
		x := *s.Shell
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.Shell = &x
	}
	if s.GitHubActions != nil {
		x := *s.GitHubActions
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.GitHubActions = &x
	}
	if s.Jenkins != nil {
		x := *s.Jenkins
		x.MaxBytes = core.MaxBytes(x.MaxBytes)
		r.Jenkins = &x
	}
	return r
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
	// Which kinds are set is worked out before the rows: Go's coverage
	// profile leaves code after a statement's first func literal outside
	// every block, so a comparison written in a row after the first would
	// read as not covered to Gremlins however the tests run it.
	set := [...]bool{s.YAML != nil, s.JSON != nil, s.TOML != nil, s.Regex != nil, s.Text != nil,
		s.XML != nil, s.INI != nil, s.Properties != nil, s.ToolVersion != nil, s.EnvFile != nil,
		s.GitLabCI != nil, s.Shell != nil, s.GitHubActions != nil, s.Jenkins != nil}
	return []extractorKind{
		{"yaml", set[0], func() (check.Extractor, error) { return yaml.Build(*s.YAML) }},
		{"json", set[1], func() (check.Extractor, error) { return json.Build(*s.JSON) }},
		{"toml", set[2], func() (check.Extractor, error) { return toml.Build(*s.TOML) }},
		{"regex", set[3], func() (check.Extractor, error) { return regex.Build(*s.Regex) }},
		{"text", set[4], func() (check.Extractor, error) { return text.Build(*s.Text) }},
		{"xml", set[5], func() (check.Extractor, error) { return xml.Build(*s.XML) }},
		{"ini", set[6], func() (check.Extractor, error) { return ini.Build(*s.INI) }},
		{"properties", set[7], func() (check.Extractor, error) { return properties.Build(*s.Properties) }},
		{"tool_version", set[8], func() (check.Extractor, error) {
			// The reader reads its JSON and TOML files with the json and
			// toml readers' parsers, since it imports no other reader.
			return toolversion.Build(*s.ToolVersion, toolversion.Parsers{JSON: json.Parse, TOML: toml.Parse})
		}},
		{"envfile", set[9], func() (check.Extractor, error) { return envfile.Build(*s.EnvFile) }},
		// The reader reads its YAML with the yaml reader's parser.
		{"gitlab-ci", set[10], func() (check.Extractor, error) { return gitlabci.Build(*s.GitLabCI, yaml.ParseNoted) }},
		{"shell", set[11], func() (check.Extractor, error) { return shell.Build(*s.Shell) }},
		// The reader reads its YAML with the yaml reader's parser.
		{"github-actions", set[12], func() (check.Extractor, error) {
			return githubactions.Build(*s.GitHubActions, yaml.ParseNoted)
		}},
		{"jenkins", set[13], func() (check.Extractor, error) { return jenkins.Build(*s.Jenkins) }},
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
