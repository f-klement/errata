// Package jenkins is the jenkins extractor: the value of one environment
// variable that a stage of a declarative Jenkinsfile sees, read with a
// lexer of Groovy's comments and string forms (lex.go) and a scan of the
// blocks a declarative pipeline writes (scan.go), through core's entry
// extractor. It reads environment { X = v } at the pipeline's top and in
// stages, a stage's over the pipeline's, and else every env.X = v in the
// file, and def X = v when nothing else sets X. It does not run Groovy: a value it computes, such
// as a call, credentials() or a GString with ${}, comes back as the file
// wrote it, marked not literal, and what only Jenkins knows (a shared
// library, a loaded file, a build parameter, the controller's settings)
// makes the value unknown.
package jenkins

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads the variable Variable of the Jenkinsfile File, as the stage
// Stage sees it, or as the pipeline's top sees it when Stage is "".
type Spec struct {
	File     string `json:"file"`
	Variable string `json:"variable"`
	Stage    string `json:"stage,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// maxName is the longest variable or stage name that loads, in bytes,
// which keeps a message's quote of it and each comparison short.
const maxName = 1024

// variableName is what Jenkins's declarative environment accepts as a
// name: a Groovy identifier of ASCII letters, digits and _.
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	src, err := core.CheckSource("jenkins", s.File, s.MaxBytes)
	if err != nil {
		return nil, err
	}
	switch {
	case s.Variable == "":
		return nil, errors.New("jenkins: variable is required")
	case len(s.Variable) > maxName || !variableName.MatchString(s.Variable):
		return nil, fmt.Errorf("jenkins: variable %s is no name an environment block accepts: write a letter or _, then up to %d letters, digits and _", core.Quote(s.Variable), maxName-1)
	case len(s.Stage) > maxName:
		return nil, fmt.Errorf("jenkins: stage %s is %d bytes long, over the limit of %d", core.Quote(s.Stage), len(s.Stage), maxName)
	}
	r := reader{file: s.File, variable: s.Variable, stage: s.Stage, staged: s.Stage != ""}
	return core.EntryExtractor{Source: src, Query: core.Query{Key: []byte(s.Variable), HasKey: true}, Read: r.read, NotLiteral: core.TemplateNotLiteral(s.File, computed)}, nil
}

// computed is the reader's test of a not literal value: the reader marks it.
func computed(e *core.Entry) bool { return e.Computed }

// Place is s with what decides where it reads: its file cleaned and no
// max_bytes. The stage stays as written, since Jenkins compares it whole.
func Place(s Spec) Spec {
	s.File, s.MaxBytes = filepath.Clean(s.File), 0
	return s
}

// reader reads one spec's file.
type reader struct {
	file, variable, stage string
	staged                bool
}

// read finds the values the spec's stage sees and visits each. The first
// pass reads the whole file; when the spec names a stage, a second pass
// reads the environment blocks of the stages around it, which the first
// found.
func (r reader) read(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	var target []byte
	if r.staged {
		target = []byte(r.stage)
	}
	s := newScan(data, []byte(r.variable), target, m)
	if err := s.run(); err != nil {
		return err
	}
	// withEnv sets a variable for the steps inside it, over every
	// environment block.
	if s.withEnv != nil {
		return core.LineError(s.withEnv.line, "withEnv sets %s for the steps inside it, which errata does not read", r.variable)
	}
	if r.staged {
		if err := r.checkStage(s); err != nil {
			return err
		}
		second := newScan(data, []byte(r.variable), target, m)
		second.chain, second.cell = map[int]bool{}, map[int]bool{}
		for _, id := range s.around {
			second.chain[id] = true
		}
		for _, id := range s.aroundCell {
			second.cell[id] = true
		}
		if err := second.run(); err != nil {
			return err
		}
		// A matrix sets its axes in each cell, over every environment
		// block around the matrix.
		if second.cellAxis != nil {
			return core.LineError(second.cellAxis.line, "%s is %s, and stage %s is in that matrix", r.variable, second.cellAxis.what, core.Quote(r.stage))
		}
		// Innermost first: a stage's environment over the stages around it.
		for _, id := range s.around {
			if rec, ok := second.stageEnv[id]; ok {
				return r.environment(data, rec, visit)
			}
		}
	}
	if s.pipelineEnv != nil {
		return r.environment(data, *s.pipelineEnv, visit)
	}
	if err := r.unknown(s); err != nil {
		return err
	}
	// def X = counts only when no environment block and no env.X = sets
	// the variable, since a step's shell does not see a Groovy variable:
	// then the scan keeps no def record.
	recs := s.records
	if !s.envSets && len(recs) > 0 && s.reassigned != nil {
		return core.LineError(s.reassigned.line, "%s changes what def %s = set, and errata does not follow which value a step sees", s.reassigned.what, r.variable)
	}
	if len(recs) > 0 {
		return emit(data, r.variable, recs, visit)
	}
	return &check.NoValue{Reason: r.none(s)}
}

// environment visits the value of an environment block, unless the block
// spells the variable in another case: Jenkins does not tell the two
// apart, and errata does not guess which spelling the stage sees.
func (r reader) environment(data []byte, rec record, visit func(*core.Entry) error) error {
	if !rec.exact {
		return core.LineError(rec.line, "an environment block sets %s in another case, and Jenkins does not tell names apart by case; errata does not guess which spelling the stage sees", r.variable)
	}
	return emit(data, r.variable, []record{rec}, visit)
}

// checkStage refuses a stage the file does not name, or names twice.
func (r reader) checkStage(s *scan) error {
	switch {
	case s.targets == 0 && s.unnamed != 0:
		return core.LineError(s.unnamed, "the file names no stage %s, and the name of this stage is not literal, so errata cannot tell whether it is that stage", core.Quote(r.stage))
	case s.targets == 0:
		return fmt.Errorf("the file names no stage %s", core.Quote(r.stage))
	case s.targets > 1:
		return core.LineError(s.targetLine[1], "a second stage is named %s, after the one at line %d; Jenkins refuses a pipeline that repeats a stage's name", core.Quote(r.stage), s.targetLine[0])
	}
	return nil
}

// unknown refuses a variable no environment block sets when something
// errata does not read may set it: a shared library or a loaded file, a
// build parameter or a matrix axis.
func (r reader) unknown(s *scan) error {
	if s.loads != nil {
		return core.LineError(s.loads.line, "%s may set %s, and errata cannot read it", s.loads.what, r.variable)
	}
	if s.declared != nil {
		return core.LineError(s.declared.line, "%s is %s", r.variable, s.declared.what)
	}
	if s.envOther != nil {
		return core.LineError(s.envOther.line, "%s; errata does not read it", s.envOther.what)
	}
	return nil
}

// none says where errata looked for a variable no file sets, and where it
// may come from instead.
func (r reader) none(s *scan) string {
	where := "the pipeline's environment block"
	if r.staged {
		where = fmt.Sprintf("the environment blocks of stage %s, the stages around it and the pipeline", core.Quote(r.stage))
	}
	msg := fmt.Sprintf("no file sets %s: errata read %s, and every env.%s = and def %s = in %s; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read", r.variable, where, r.variable, r.variable, r.file)
	if !r.staged && (len(s.settingIn) > 0 || s.settingMore > 0) {
		names := make([]string, len(s.settingIn))
		for i, n := range s.settingIn {
			names[i] = core.Quote(string(n))
		}
		if s.settingMore > 0 {
			names = append(names, fmt.Sprintf("%d more", s.settingMore))
		}
		msg += fmt.Sprintf("; the environment of stage %s sets it, so name one with stage:", strings.Join(names, ", "))
	}
	return msg
}

// emit visits the values of recs in file order.
func emit(data []byte, name string, recs []record, visit func(*core.Entry) error) error {
	key := []byte(name)
	for _, rec := range recs {
		text, computed := value(rec, data)
		if err := visit(&core.Entry{Key: key, Value: text, Line: rec.val.line, Computed: computed}); err != nil {
			return err
		}
	}
	return nil
}
