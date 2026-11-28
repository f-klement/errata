// Package compose is the compose extractor: the value one service of a
// Docker Compose project holds for a key, in the project's effective
// configuration, at the file and line that set it. compose-go, the
// reference loader that docker compose itself uses, builds that
// configuration: the file and its overrides merged in their order, extends
// and include applied, the profiles a rule names made active, and ${VAR}
// interpolated from the project's .env files alone. compose-go keeps no
// line of any value, so the reader reconstructs it (locate.go): it loads
// each file that contributes to the service on its own, finds the last one
// whose own configuration holds the value, and walks that file's YAML,
// which the facade's parser reads with lines, to the node that wrote it.
//
// compose-go runs nothing and opens no file of the repository. This
// package reads every file of the project first (walk.go), through core,
// within the repository, and copies each to a stage, a temporary directory
// that holds nothing else (stage.go). compose-go is handed the stage in
// place of the root, so an include, an extends file, an included project's
// .env and an include's env_file, which it opens itself, are each a copy
// errata read or no file at all (ADR 0022). Its resource loader refuses an
// include or extends path the walker did not route, and an include's
// env_file, which no loader sees, is refused when it names a variable, so
// that its path is the one the walker checked. compose-go sees no process environment, since
// every lookup it makes is handed the .env files' variables; a variable
// they do not set makes the value not literal.
package compose

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads one service of a Compose project: File is its main file and
// Overrides the files merged over it, in their order, as docker compose -f
// File -f Override takes them. Key is a key of the service's effective
// configuration, as docker compose config writes it, such as image,
// environment.API_PORT, build.context or ports; a key that holds a
// mapping or a list gives each value below it. Profiles are the profiles
// active, as --profile names them; a service that needs another is
// refused. With no overrides: key, a read is refused where docker compose
// run without -f would load more than File: an override beside it, or the
// files COMPOSE_FILE in the .env file names (alone). overrides: [], an
// empty list, reads File alone, as docker compose -f File does: no
// override beside it and no COMPOSE_FILE. So an absent key (nil) and an
// empty list are two specs, and omitzero keeps the empty list in the
// listing errata list prints.
type Spec struct {
	core.OneFile
	Overrides []string `json:"overrides,omitzero"`
	Service   string   `json:"service"`
	Key       string   `json:"key"`
	Profiles  []string `json:"profiles,omitempty"`
}

// kindKey is the kind's key, which Kind and Build both use. Build names
// the constant rather than Kind.Key because an env reader's Read calls
// Every, and Every reading Kind would make an initialization cycle.
const kindKey = "compose"

// Kind is the compose kind, which a rule writes under the key compose. It
// is an env reader, of a service's environment: and env_file:.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe,
	Env: &check.EnvChoice{Order: 90, Chooses: chooses}, EnvRead: envRead}

// chooses takes, for the env kind, a file docker compose finds by its
// name (defaultNames). An override file, such as compose.override.yaml, is
// not one: Every reads it merged over the file it lies beside.
func chooses(file string) bool { return slices.Contains(defaultNames, path.Base(file)) }

// envRead is the env kind's reader of the variable name in file: Every's,
// with the YAML parser the facade hands it.
func envRead(file, name string, maxBytes int64, p core.Parsers) (check.Extractor, error) {
	return Every(file, name, maxBytes, p.YAML)
}

// Parser parses every YAML document of a file into core's tree, counting
// the nodes YAML aliases add from expanded against core.MaxExpanded, and
// returns the count it ends at. The facade hands it the yaml reader's.
type Parser func(data []byte, expanded int) ([]*core.Node, int, error)

// maxName is the longest service, key, profile or variable that loads, in
// bytes, as the CI readers' names.
const maxName = 1024

// maxOverrides is the most override files a spec lists.
const maxOverrides = 64

// Build checks s and returns its extractor.
func Build(s Spec, parse Parser) (check.Extractor, error) {
	src, err := s.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if err := checkService(s.Service); err != nil {
		return nil, err
	}
	key, err := checkKey(s.Key)
	if err != nil {
		return nil, err
	}
	files, err := checkOverrides(src, s.Overrides)
	if err != nil {
		return nil, err
	}
	if err := checkProfiles(s.Profiles); err != nil {
		return nil, err
	}
	return extractor{Source: src, files: files, service: s.Service, key: key, profiles: s.Profiles, parse: parse, fileOnly: s.Overrides != nil && len(s.Overrides) == 0}, nil
}

// Every builds the extractor of the variable name across every service of
// the Compose file, active or not: the value each service's container
// gets, from its environment: or else its env_file:, each with the service
// as its origin, service "name", the services in the order of their
// names. A service that sets no such variable gives none. It reads the
// file as docker compose run without -f in its directory loads it, with
// the override beside it merged over it, and refuses it where docker
// compose would load another file in its place (alone). The env extractor
// builds it.
func Every(file, name string, maxBytes int64, parse Parser) (check.Extractor, error) {
	src, err := core.OneFile{File: file, MaxBytes: maxBytes}.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if name == "" || len(name) > maxName || strings.ContainsAny(name, "=\x00") {
		return nil, fmt.Errorf("compose: variable %s is no name an environment holds", core.Quote(name))
	}
	return extractor{Source: src, files: []core.Source{src}, variable: name, parse: parse, every: true}, nil
}

func checkService(service string) error {
	switch {
	case service == "":
		return errors.New("compose: service is required")
	case len(service) > maxName:
		return fmt.Errorf("compose: service %s is %d bytes long, over the limit of %d", core.Quote(service), len(service), maxName)
	}
	return nil
}

// checkKey splits a key into its parts, which may hold a dot of a key
// below them, such as a label's com.example.name (steps).
func checkKey(key string) ([]string, error) {
	if key == "" {
		return nil, errors.New("compose: key is required: name a key of the service, such as image or environment.API_PORT")
	}
	if len(key) > maxName {
		return nil, fmt.Errorf("compose: key %s is %d bytes long, over the limit of %d", core.Quote(key), len(key), maxName)
	}
	parts := strings.Split(key, ".")
	if slices.Contains(parts, "") {
		return nil, fmt.Errorf("compose: key %s has an empty part; write a key such as image, or build.context", core.Quote(key))
	}
	return parts, nil
}

// checkOverrides is the sources of the main file and each override, in
// their order, each refused as the main file is.
func checkOverrides(main core.Source, overrides []string) ([]core.Source, error) {
	if len(overrides) > maxOverrides {
		return nil, fmt.Errorf("compose: overrides lists %d files, over the limit of %d", len(overrides), maxOverrides)
	}
	out := []core.Source{main}
	for _, o := range overrides {
		src, err := core.CheckSource(kindKey, o, main.Limit)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, nil
}

func checkProfiles(profiles []string) error {
	for _, p := range profiles {
		if p == "" || len(p) > maxName {
			return fmt.Errorf("compose: profile %s is empty or over %d bytes long", core.Quote(p), maxName)
		}
	}
	return nil
}

// Place is s with only what decides where it reads: the files cleaned,
// the overrides in their order, since an override merges over the ones
// before it, the profiles sorted, and max_bytes left out.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	if s.Overrides != nil {
		// An empty list is a place of its own: the file alone, as -f reads it.
		s.Overrides = append([]string{}, core.Clean(s.Overrides)...)
	}
	s.Profiles = slices.Clone(s.Profiles)
	slices.Sort(s.Profiles)
	s.Profiles = slices.Compact(s.Profiles)
	return s
}

// Resolve is s with its max_bytes written out and its lists copied, so a
// change to one does not reach s.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	s.Overrides = slices.Clone(s.Overrides)
	s.Profiles = slices.Clone(s.Profiles)
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since a value that names a variable the .env files do not set
// is one only docker compose's environment decides.
func Describe(Spec) check.Description {
	return check.Description{Label: kindKey, NotLiteral: true}
}

// extractor is a built compose extractor.
type extractor struct {
	core.Source
	files    []core.Source // the main file and the overrides, in order
	service  string
	key      []string
	profiles []string
	parse    Parser
	variable string // the variable Every reads
	every    bool
	fileOnly bool // the spec's overrides: is an empty list, -f File alone
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract reads the project's files under root, has compose-go build its
// configuration, and returns the values of the key, or of the variable in
// every service.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	vs, _, err := e.ExtractRead(ctx, root)
	return vs, err
}

// ExtractRead is Extract, and the bytes of the files it read, for the env
// extractor, whose files hold its max_bytes in all, a project's other
// files too.
func (e extractor) ExtractRead(ctx context.Context, root string) ([]check.Value, int64, error) {
	w, err := newWalker(ctx, root, e.parse, e.Limit)
	if err != nil {
		return nil, 0, err
	}
	defer w.close()
	vs, err := e.extract(w)
	return vs, w.limit - w.left, err
}

// extract walks the project from e's files and returns its values.
func (e extractor) extract(w *walker) ([]check.Value, error) {
	p, err := w.project(e.files, e.every, e.fileOnly)
	if err != nil {
		return nil, err
	}
	l := newLoader(w)
	var vs []check.Value
	if e.every {
		vs, err = l.every(p, e.variable)
	} else {
		vs, err = l.values(p, e.service, e.key, e.profiles)
	}
	if err != nil || !w.merged || !w.fromShell() {
		return vs, err
	}
	return tainted(vs, e.every, e.key)
}

// tainted is vs, the values of a project built from more than one file or
// service in which some file looks up a variable no file sets, each not
// literal: compose-go merges such a project's files, its extends and its
// includes by rules errata does not follow value by value, so a value the
// shell gives anywhere in it may reach any value read (ADR 0022). A key
// that holds nothing is refused, since the shell may give it one; Every
// gives none for a service that sets no such variable, as keysFromShell
// and envFilesFromShell refuse every name and env file the shell could
// choose.
func tainted(vs []check.Value, every bool, key []string) ([]check.Value, error) {
	if len(vs) == 0 && !every {
		return nil, fmt.Errorf("compose: %s holds nothing here, but the project is built from more than one file or service and a file of it looks up a variable no file errata read sets, which docker compose takes from its shell and which may give it a value", core.Quote(strings.Join(key, ".")))
	}
	for i := range vs {
		vs[i].NotLiteral = true
	}
	return vs, nil
}
