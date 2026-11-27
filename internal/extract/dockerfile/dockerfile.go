// Package dockerfile is the dockerfile extractor: the values of one
// instruction of a Dockerfile, FROM, EXPOSE, LABEL, ENV or ARG, in every
// stage or in one, through core's entry extractor. It parses the file with
// BuildKit's own parser (github.com/moby/buildkit/frontend/dockerfile/parser),
// which reads the escape directive, line continuations and heredocs as
// docker build does, and expands each word with BuildKit's shell lexer
// (frontend/dockerfile/shell), as the frontend expands it, against the ARG
// and ENV values the file sets before it (read.go). It runs no build: a
// value that names a variable only the build knows, such as a build
// argument with no default or one the base image sets, comes back as the
// file wrote it, marked not literal.
package dockerfile

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns the values Instruction sets in the Dockerfile File: FROM the
// image of each stage, EXPOSE each port, and LABEL, ENV and ARG the value
// of each key, or of Key alone when it is set. Stage keeps the values of
// one stage: its name, as AS names it, or its index from 0, as COPY
// --from counts the stages. An ARG before the first FROM is in no stage.
type Spec struct {
	core.OneFile
	Instruction string `json:"instruction"`
	Key         string `json:"key,omitempty"`
	Stage       string `json:"stage,omitempty"`
}

// kindKey is the kind's key, which Kind and Build both use. Build names
// the constant rather than Kind.Key because an env reader's Read calls
// Build's helpers, and reading Kind there would make an initialization
// cycle.
const kindKey = "dockerfile"

// Kind is the dockerfile kind, which a rule writes under the key
// dockerfile. It is an env reader, after k8s-env.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe,
	Env: &check.EnvChoice{Order: 80, Chooses: chooses}, EnvRead: envRead}

// chooses takes, for the env kind, the names docker build and podman read
// a Dockerfile under, a template suffix taken off: Dockerfile and
// Containerfile, either followed by a . and a suffix, such as
// Dockerfile.prod, or ending a name after a ., such as api.Dockerfile, and
// BuildKit's fallback dockerfile. It leaves to none a Dockerfile's own
// ignore file, Dockerfile.dockerignore, and the copies of one that are no
// Dockerfile, by the suffixes of notDockerfile.
func chooses(file string) bool {
	base := path.Base(core.Untemplated(file))
	for _, suffix := range notDockerfile {
		if strings.HasSuffix(base, suffix) {
			return false
		}
	}
	if base == "dockerfile" || strings.HasSuffix(base, ".dockerfile") {
		return true
	}
	for _, name := range []string{"Dockerfile", "Containerfile"} {
		if base == name || strings.HasPrefix(base, name+".") || strings.HasSuffix(base, "."+name) {
			return true
		}
	}
	return false
}

// notDockerfile are the suffixes of a file named after a Dockerfile that
// holds none: its ignore file, a Markdown page, a diff's leftovers and a
// backup.
var notDockerfile = []string{".dockerignore", ".md", ".orig", ".rej", ".bak"}

// envRead is the env kind's reader of the variable name in file: Every's.
func envRead(file, name string, maxBytes int64, _ core.Parsers) (check.Extractor, error) {
	return Every(file, name, maxBytes)
}

// Place is s with only what decides where it reads: the file cleaned, no
// max_bytes, the instruction in upper case and the stage in lower case,
// since Docker reads both in any case.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	s.Instruction = strings.ToUpper(s.Instruction)
	s.Stage = strings.ToLower(s.Stage)
	return s
}

// Resolve is s with its max_bytes written out, for errata list.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since a value may name a variable only the build knows.
func Describe(Spec) check.Description {
	return check.Description{Label: kindKey, NotLiteral: true}
}

// maxName is the longest key or stage that loads, in bytes, which keeps a
// message's quote of it and each comparison short.
const maxName = 1024

// stageName is a stage's name as BuildKit takes it after AS, in lower case,
// and stageIndex an index, which no name can be since a name starts with a
// letter.
var (
	stageName  = regexp.MustCompile(`^[a-z][a-z0-9-_.]*$`)
	stageIndex = regexp.MustCompile(`^[0-9]+$`)
)

// instructions are the instructions a spec selects, and whether each sets
// keys.
var instructions = map[string]bool{"FROM": false, "EXPOSE": false, "LABEL": true, "ENV": true, "ARG": true}

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	src, err := s.Check(kindKey)
	if err != nil {
		return nil, err
	}
	in := strings.ToUpper(s.Instruction)
	keyed, ok := instructions[in]
	if !ok {
		return nil, fmt.Errorf("dockerfile: instruction %s is none errata reads; write one of FROM, EXPOSE, LABEL, ENV and ARG", core.Quote(s.Instruction))
	}
	if err := checkKey(in, s.Key, keyed); err != nil {
		return nil, err
	}
	sel, err := checkStage(s.Stage)
	if err != nil {
		return nil, err
	}
	sel.instruction, sel.everyKey, sel.template, sel.parseLimit = in, s.Key == "", isTemplate(s.File), src.Limit/parseShare
	x := core.EntryExtractor{Source: src, Read: core.TemplateRead(s.File, sel.read), NotLiteral: core.TemplateNotLiteral(s.File, computed), Keyed: keyed}
	if s.Key != "" {
		x.Query = core.Query{Key: []byte(s.Key), HasKey: true}
	}
	return x, nil
}

// checkKey refuses a key on an instruction that sets none, and one over
// maxName bytes.
func checkKey(in, key string, keyed bool) error {
	switch {
	case key != "" && !keyed:
		return fmt.Errorf("dockerfile: %s sets no key; leave key out, or select LABEL, ENV or ARG", in)
	case len(key) > maxName:
		return fmt.Errorf("dockerfile: key %s is %d bytes long, over the limit of %d", core.Quote(key), len(key), maxName)
	}
	return nil
}

// checkStage is the selection of the stage stage names: every stage for
// "", else a name or an index.
func checkStage(stage string) (selection, error) {
	var sel selection
	lower := strings.ToLower(stage)
	switch {
	case stage == "":
	case len(stage) > maxName:
		return sel, fmt.Errorf("dockerfile: stage %s is %d bytes long, over the limit of %d", core.Quote(stage), len(stage), maxName)
	case stageIndex.MatchString(stage):
		i, err := strconv.Atoi(stage)
		if err != nil {
			return sel, fmt.Errorf("dockerfile: stage %s is no index a Dockerfile can reach", core.Quote(stage))
		}
		sel.index = i
	case stageName.MatchString(lower):
		sel.name = lower
	default:
		return sel, fmt.Errorf("dockerfile: stage %s is no stage name: write the name after AS, which starts with a letter and holds letters, digits, -, _ and ., or the stage's index from 0", core.Quote(stage))
	}
	sel.staged = stage != ""
	return sel, nil
}

// Every builds the extractor of the variable name in every stage of the
// Dockerfile: the value of each ENV and ARG that sets it, an ARG before
// the first FROM included, with the stage as each value's origin: stage
// "build" for a named stage, stage 1 for one with no name, and global for
// an ARG before the first FROM. The env extractor builds it.
func Every(file, name string, maxBytes int64) (check.Extractor, error) {
	src, err := core.OneFile{File: file, MaxBytes: maxBytes}.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if name == "" || len(name) > maxName {
		return nil, errors.New("dockerfile: the variable's name is empty or longer than 1024 bytes")
	}
	sel := selection{every: true, template: isTemplate(file), parseLimit: src.Limit / parseShare}
	return core.EntryExtractor{Source: src, Query: core.Query{Key: []byte(name), HasKey: true}, Read: core.TemplateRead(file, sel.read), NotLiteral: core.TemplateNotLiteral(file, computed), Keyed: true}, nil
}

// isTemplate reports whether file is a template's, by its name.
func isTemplate(file string) bool {
	_, base := core.FileParts(file)
	return core.IsTemplate(base)
}

// computed is the reader's test of a not literal value: the reader marks it.
func computed(e *core.Entry) bool { return e.Computed }
