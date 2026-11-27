package dockerfile

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// selection is what a read visits: the values of instruction, of each key
// as an entry of kind EntryName when everyKey is set, in every stage or,
// when staged, in the stage of index or name. every visits each ENV and
// ARG instead, with its stage as its origin, for the env kind.
type selection struct {
	instruction string
	everyKey    bool
	staged      bool
	index       int
	name        string
	every       bool
	template    bool // the file is a template, whose engine's lines are no instructions
	// parseLimit is the largest file BuildKit's parser is given, a
	// parseShare of max_bytes.
	parseLimit int64
}

// parseShare is the share of max_bytes a Dockerfile may be. BuildKit's
// parser builds the syntax of the whole file before errata reads an
// instruction, and keeps up to 164 bytes of it for each byte of the file,
// on builder: 1 MiB of lines of one unknown word holds 164 MiB once
// parsed and 2 MiB 329 MiB, of ENV a= pairs 159 and 319 MiB, of ENV a=1
// pairs 120 and 240 MiB, of EXPOSE 1 words 79 and 158 MiB, and of FROM a
// lines 49 and 99 MiB. So 32 MiB, the default max_bytes, would hold about
// 5.2 GB, past a 2 GiB runner. At a 32nd the default reads a Dockerfile of
// up to 1 MiB, and the densest file at the limit holds 164 MiB; a read
// of the densest file allocates up to 469 MiB for each MiB. At the 64 MiB
// ceiling the limit is 2 MiB, 329 MiB held.
const parseShare = 32

// read parses data with BuildKit's parser and visits the values sel
// selects, in file order, each at the line its word starts on. A file over
// sel's parse limit is refused before the parser sees it. A file the
// parser refuses, or that docker build would refuse for an instruction
// before the first FROM, an instruction it does not know, a FROM of
// neither one word nor three or a stage name used twice, is refused. A
// stage sel names that the file does not have gives no value, and says so
// (check.NoValue). A panic in BuildKit's parser or lexer refuses the file
// rather than ending the run (ADR 0020); one in errata's own code is not
// recovered (guard).
func (sel selection) read(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	if int64(len(data)) > sel.parseLimit {
		return fmt.Errorf("the file is %d bytes, over the limit of %d that BuildKit's parser is given, max_bytes divided by %d; set max_bytes: higher to read a larger Dockerfile",
			len(data), sel.parseLimit, parseShare)
	}
	if err := m.Charge(len(data)); err != nil {
		return err
	}
	var res *parser.Result
	var parseErr error
	if err := guard("parser", func() { res, parseErr = parse(bytes.NewReader(data)) }); err != nil {
		return err
	}
	lines := newLines(data)
	if parseErr != nil {
		return parseError(parseErr, data, lines)
	}
	r := &reading{sel: sel, m: m, visit: visit, lex: shell.NewLex(res.EscapeToken), escape: escapeByte(res.EscapeToken),
		lines: lines, cur: core.NewLineCursor(data), global: scope{}, named: map[string]int{}, declared: map[string]bool{}, stage: -1}
	for _, n := range res.AST.Children {
		if err := r.instruction(n); err != nil {
			return err
		}
	}
	if sel.staged && !r.found {
		return &check.NoValue{Reason: "the Dockerfile has no stage " + sel.stageText()}
	}
	return nil
}

// parse is BuildKit's parser, and processWord its shell lexer's
// expansion; a test replaces each to witness guard.
var (
	parse       = parser.Parse
	processWord = (*shell.Lex).ProcessWordWithMatches
)

// guard runs call, which is one call into BuildKit's part, and refuses the
// file when it panics, as gomod guards modfile's parse. It recovers around
// that call alone, so a panic in errata's own reading still ends the run
// and the fuzz target sees it. One piece of errata's code runs inside the
// lexer's guard: lookup.Get, which the lexer calls back for each name, so
// a panic there reads as the lexer's and the fuzz target takes it for a
// refusal.
func guard(part string, call func()) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("BuildKit's Dockerfile %s stopped on the file: %s", part, core.Clip(fmt.Sprint(v)))
		}
	}()
	call()
	return nil
}

// stageText names the stage sel selects, as a message writes it.
func (sel selection) stageText() string {
	if sel.name != "" {
		return core.Quote(sel.name)
	}
	return strconv.Itoa(sel.index)
}

// escapeByte is the escape the parser read from the escape directive, \
// or `, the only two it takes.
func escapeByte(token rune) byte {
	if token == '`' {
		return '`'
	}
	return '\\'
}

// tooLong starts the parser's refusal of a line its scanner cannot hold,
// one of more than bufio.MaxScanTokenSize bytes with its \n.
const tooLong = "dockerfile line greater than max allowed size"

// parseError is the parser's refusal at the line it names, which errata
// counts by its own line ends, a lone \r included.
func parseError(err error, data []byte, l lines) error {
	var located *parser.LocationError
	if errors.As(err, &located) && len(located.Locations) > 0 && len(located.Locations[0]) > 0 {
		line, msg := located.Locations[0][0].Start.Line, located.Unwrap().Error()
		off := l.offset(line)
		if strings.HasPrefix(msg, tooLong) {
			// The parser names a line it read before, which is not always the
			// one before: the line too long is the first that fills the
			// scanner's buffer.
			off = l.longLine()
		}
		return core.LineError(core.LineOf(data, off), "%s", core.Clip(msg))
	}
	return errors.New(core.Clip(err.Error()))
}

// lineOf is the line of the file the next word of at starts on.
func (r *reading) lineOf(at *positions, word string) (int, error) {
	off, err := at.next(word, r.m)
	return r.cur.At(off), err
}

// startLine is the line of the file n starts on.
func (r *reading) startLine(n *parser.Node) int {
	return r.cur.At(r.lines.offset(n.StartLine))
}

// binding is the value a variable holds at a point of the file: known is
// false when only the build knows it.
type binding struct {
	value string
	known bool
}

// scope is the variables a word is expanded against, by name.
type scope map[string]binding

// stage is a stage read so far: its name, "" for none, and the variables
// it ends with, which a stage FROM it starts with.
type stage struct {
	name string
	vars scope
}

// reading is one read's state.
type reading struct {
	sel    selection
	m      *core.Meter
	visit  func(*core.Entry) error
	lex    *shell.Lex
	escape byte
	lines  lines
	cur    *core.LineCursor
	// global is the ARGs before the first FROM, which a FROM expands
	// against; vars is the current stage's, global's before the first FROM.
	global scope
	vars   scope
	stages []stage
	named  map[string]int // the index of each named stage
	// declared is the name of each ARG before the first FROM, with or
	// without a default.
	declared map[string]bool
	stage    int  // the current stage's index, -1 before the first FROM
	found    bool // the stage sel names was read
	e        core.Entry
}

// instruction reads the instruction n.
func (r *reading) instruction(n *parser.Node) error {
	if err := r.m.Charge(core.LineWork); err != nil {
		return err
	}
	name := strings.ToLower(n.Value)
	if _, ok := command.Commands[name]; !ok {
		if r.sel.template {
			return nil // the engine's, such as {% if %}
		}
		return core.LineError(r.startLine(n), "%s is no instruction docker build knows", core.Quote(n.Value))
	}
	if r.stage < 0 && name != command.From && name != command.Arg {
		return core.LineError(r.startLine(n), "%s comes before the first FROM, where docker build takes only ARG", strings.ToUpper(name))
	}
	return r.dispatch(name, n)
}

// dispatch reads n by its instruction's name, which docker build knows;
// an instruction errata reads nothing from, such as RUN, is skipped.
func (r *reading) dispatch(name string, n *parser.Node) error {
	switch name {
	case command.From:
		return r.from(n)
	case command.Arg:
		return r.arg(n)
	case command.Env:
		return r.pairs(n, true)
	case command.Label:
		return r.pairs(n, false)
	case command.Expose:
		return r.expose(n)
	}
	return nil
}

// from starts a stage: FROM image, or FROM image AS name. The image is
// expanded against the ARGs before the first FROM, as BuildKit expands
// it. A stage FROM an earlier stage, by its name, starts with the
// variables that stage ended with; any other starts with none errata
// knows, since its image's are the image's. In a template, an image the
// engine writes in several words, such as {{ image }}, is those words as
// written, not literal.
func (r *reading) from(n *parser.Node) error {
	words, name, err := r.fromWords(n)
	if err != nil {
		return err
	}
	st := stage{vars: scope{}, name: name}
	at := r.positions(n)
	line, err := r.lineOf(&at, words[0])
	if err != nil {
		return err
	}
	image, literal, err := r.expandWith(words[0], r.global, r.unsetInFrom)
	if err != nil {
		return err
	}
	if len(words) > 1 {
		image, literal = strings.Join(words, " "), false
	}
	if literal {
		if st.vars, err = r.inherited(image); err != nil {
			return err
		}
	}
	if err := r.start(st, line); err != nil {
		return err
	}
	return r.emit(command.From, "", image, line, !literal)
}

// fromWords is FROM's image words and its stage name, lower case, or ""
// for a stage FROM names none. The image is one word, or in a template
// one or more.
func (r *reading) fromWords(n *parser.Node) ([]string, string, error) {
	words, name := wordsOf(n), ""
	if len(words) >= 3 && strings.EqualFold(words[len(words)-2], "as") {
		name = strings.ToLower(words[len(words)-1])
		words = words[:len(words)-2]
	}
	if len(words) != 1 && (!r.sel.template || len(words) == 0) {
		return nil, "", core.LineError(r.startLine(n), "FROM takes an image, or an image, AS and a name, and docker build refuses this one")
	}
	return words, name, nil
}

// start makes st, whose FROM is at line, the stage the instructions after
// it read into. A name an earlier stage has is refused, as docker build
// refuses it, but in a template, whose engine may write one stage in each
// branch of an if: there every stage of the name is selected, and a stage
// FROM the name inherits from the first.
func (r *reading) start(st stage, line int) error {
	if _, ok := r.named[st.name]; ok && !r.sel.template {
		return core.LineError(line, "stage name %s is taken by an earlier stage, which docker build refuses", core.Quote(st.name))
	} else if !ok && st.name != "" {
		r.named[st.name] = len(r.stages)
	}
	r.stages = append(r.stages, st)
	r.stage = len(r.stages) - 1
	r.vars = st.vars
	if r.selected() {
		r.found = true
	}
	return nil
}

// inherited is a copy of the variables the stage named image ended with,
// or none for an image that names no earlier stage. Each variable copied
// is charged copyWork, so a file of many stages, each FROM the one before
// with many variables, is refused for work before it takes time in the
// square of its size.
func (r *reading) inherited(image string) (scope, error) {
	i, ok := r.named[strings.ToLower(image)]
	if !ok {
		return scope{}, nil
	}
	vars := r.stages[i].vars
	if err := r.m.Charge(copyWork * len(vars)); err != nil {
		return nil, err
	}
	out := make(scope, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out, nil
}

// arg reads ARG name or ARG name=default, several to an instruction. A
// default is expanded against the variables before it. With no default,
// an ARG in a stage takes the value of the ARG of its name before the
// first FROM, and else the value only a --build-arg gives, which errata
// returns as an empty value marked not literal; before the first FROM it
// keeps the value an ARG of its name gave before.
func (r *reading) arg(n *parser.Node) error {
	at := r.positions(n)
	for _, word := range wordsOf(n) {
		line, err := r.lineOf(&at, word)
		if err != nil {
			return err
		}
		key, def, hasDefault := strings.Cut(word, "=")
		if key == "" {
			return core.LineError(line, "an ARG with no name, which docker build refuses")
		}
		b, set, err := r.argValue(key, def, hasDefault)
		if err != nil {
			return err
		}
		if r.stage < 0 {
			r.declared[key] = true
		}
		if set {
			r.scopeNow()[key] = b
		}
		if err := r.emit(command.Arg, key, b.value, line, !b.known); err != nil {
			return err
		}
	}
	return nil
}

// scopeNow is the variables an instruction sets: the current stage's, or
// global before the first FROM.
func (r *reading) scopeNow() scope {
	if r.stage < 0 {
		return r.global
	}
	return r.vars
}

// argValue is the value an ARG of key gives, def when hasDefault, else
// the value of the ARG of key before the first FROM, and whether the ARG
// sets the variable: an ARG with no default and none before the first
// FROM leaves it as it was.
func (r *reading) argValue(key, def string, hasDefault bool) (binding, bool, error) {
	if hasDefault {
		v, literal, err := r.expand(def, r.scopeNow())
		return binding{value: v, known: literal}, true, err
	}
	b, ok := r.global[key]
	return b, ok, nil
}

// pairs reads ENV or LABEL: key=value pairs, or the old form of one key,
// white space and the rest of the line as its value. Each key and value
// is expanded against the variables before the instruction, and an ENV
// sets the variable for the instructions after it. --unset drops the keys
// it names, which errata reads as setting no value.
func (r *reading) pairs(n *parser.Node, env bool) error {
	in := command.Label
	if env {
		in = command.Env
	}
	if hasFlag(n.Flags, "--unset") {
		// LABEL --unset drops labels, and no variable.
		for _, w := range wordsOf(n) {
			if env {
				delete(r.vars, w)
			}
		}
		return nil
	}
	at := r.positions(n)
	words := wordsOf(n)
	var sets []setting
	for i := 0; i+1 < len(words); i += 3 {
		key, b, err := r.pair(in, words[i], words[i+1], &at)
		if err != nil {
			return err
		}
		sets = append(sets, setting{key, b})
	}
	if env {
		// After every pair, as BuildKit expands every word of the
		// instruction before it sets any.
		for _, st := range sets {
			r.vars[st.key] = st.b
		}
	}
	return nil
}

// setting is a variable an ENV sets.
type setting struct {
	key string
	b   binding
}

// pair reads one key and value of an ENV or a LABEL, expanded against the
// variables before the instruction, and returns them.
func (r *reading) pair(in, rawKey, rawValue string, at *positions) (string, binding, error) {
	line, err := r.lineOf(at, rawKey)
	if err != nil {
		return "", binding{}, err
	}
	at.skip("=") // the separator, which the old form of KEY value has not
	if _, err := r.lineOf(at, rawValue); err != nil {
		return "", binding{}, err
	}
	if rawKey == "" {
		return "", binding{}, core.LineError(line, "%s sets a key with no name, which docker build refuses", strings.ToUpper(in))
	}
	key, keyLiteral, err := r.expand(rawKey, r.vars)
	if err != nil {
		return "", binding{}, err
	}
	value, literal, err := r.expand(rawValue, r.vars)
	if err != nil {
		return "", binding{}, err
	}
	if !keyLiteral {
		key, value, literal = rawKey, rawValue, false
	}
	return key, binding{value: value, known: literal}, r.emit(in, key, value, line, !literal)
}

// hasFlag reports whether flags holds name, alone or with =true.
func hasFlag(flags []string, name string) bool {
	for _, f := range flags {
		n, v, _ := strings.Cut(f, "=")
		if n == name && !strings.EqualFold(v, "false") {
			return true
		}
	}
	return false
}

// expose reads EXPOSE: each port, expanded.
func (r *reading) expose(n *parser.Node) error {
	at := r.positions(n)
	for _, word := range wordsOf(n) {
		line, err := r.lineOf(&at, word)
		if err != nil {
			return err
		}
		port, literal, err := r.expand(word, r.vars)
		if err != nil {
			return err
		}
		if err := r.emit(command.Expose, "", port, line, !literal); err != nil {
			return err
		}
	}
	return nil
}

// wordsOf is the words the parser split n's arguments into.
func wordsOf(n *parser.Node) []string {
	var out []string
	for p := n.Next; p != nil; p = p.Next {
		out = append(out, p.Value)
	}
	return out
}

// selected reports whether the current stage is the one sel names, or
// any stage when it names none; an ARG before the first FROM is in none.
// Only a template may hold two stages of the name, and both are selected
// (start).
func (r *reading) selected() bool {
	switch {
	case !r.sel.staged:
		return true
	case r.stage < 0:
		return false
	case r.sel.name != "":
		return r.stages[r.stage].name == r.sel.name
	}
	return r.stage == r.sel.index
}

// emit visits the value of key that the instruction in sets at line, if
// the selection wants it.
func (r *reading) emit(in, key, value string, line int, notLiteral bool) error {
	r.e = core.Entry{Key: []byte(key), Value: []byte(value), Line: line, Computed: notLiteral}
	switch {
	case r.sel.every:
		if in != command.Env && in != command.Arg {
			return nil
		}
		r.e.Origin = r.origin()
	case !strings.EqualFold(in, r.sel.instruction) || !r.selected():
		return nil
	case r.sel.everyKey:
		r.e.Kind = core.EntryName
	}
	return r.visit(&r.e)
}

// origin is the part of the file the current instruction is in, as an env
// value names it.
func (r *reading) origin() string {
	switch {
	case r.stage < 0:
		return "global"
	case r.stages[r.stage].name != "":
		return "stage " + core.Quote(r.stages[r.stage].name)
	}
	return fmt.Sprintf("stage %d", r.stage)
}

// copyWork is the work of each byte a lookup copies from a variable's
// value, as envfile's, so a chain of variables that each repeat the one
// before is refused for work before its values fill memory; a lookup in a
// long word costs more, since a pattern of ${X%%pattern} runs over the
// value once for each byte of the pattern at worst.
const copyWork = 32

// expand is word expanded against vars as BuildKit's shell lexer expands
// it, and whether that is literal: a word that names a variable vars does
// not know, or that the lexer refuses, is returned as written and is not.
func (r *reading) expand(word string, vars scope) (string, bool, error) {
	return r.expandWith(word, vars, nil)
}

// builtinArgs are the ARGs BuildKit gives a FROM without a declaration,
// each of which --build-arg may set too.
var builtinArgs = map[string]bool{
	"BUILDPLATFORM": true, "BUILDOS": true, "BUILDOSVERSION": true, "BUILDARCH": true, "BUILDVARIANT": true,
	"TARGETPLATFORM": true, "TARGETOS": true, "TARGETOSVERSION": true, "TARGETARCH": true, "TARGETVARIANT": true,
	"TARGETSTAGE": true,
}

// unsetInFrom reports whether name is unset where a FROM is expanded: no
// ARG before the first FROM declares it and BuildKit gives no ARG of the
// name, so no --build-arg can set it either, and ${name:-word} is word.
// In a template none is, since the engine may write the ARG that declares
// it.
func (r *reading) unsetInFrom(name string) bool {
	return !r.sel.template && !r.declared[name] && !builtinArgs[name]
}

// expandWith is expand, where a name unset reports, of those vars does not
// know, is one the build cannot set either, so a word that names only such
// names is still literal.
func (r *reading) expandWith(word string, vars scope, unset func(string) bool) (string, bool, error) {
	if err := r.m.Charge(len(word)); err != nil {
		return "", false, err
	}
	l := lookup{vars: vars, m: r.m, perByte: copyWork + len(word)}
	var res shell.ProcessWordResult
	var lexErr error
	if err := guard("lexer", func() { res, lexErr = processWord(r.lex, word, &l) }); err != nil {
		return "", false, err
	}
	if l.err != nil {
		return "", false, l.err
	}
	// A word the lexer refuses, such as ${A, or one that names a variable
	// errata does not know, is the file's text, not literal.
	if literal := lexErr == nil && allUnset(res.Unmatched, unset); !literal {
		return word, false, nil
	}
	return res.Result, true, nil
}

// allUnset reports whether unset reports every name of names, none for a
// nil unset.
func allUnset(names map[string]struct{}, unset func(string) bool) bool {
	for name := range names {
		if unset == nil || !unset(name) {
			return false
		}
	}
	return true
}

// lookup is the variables the lexer asks, charging each value it gives.
type lookup struct {
	vars    scope
	m       *core.Meter
	perByte int
	err     error // the budget spent, which ends the read
}

// Get is the value of a known variable; one only the build knows is
// unmatched, so the word is not literal.
func (l *lookup) Get(name string) (string, bool) {
	b, ok := l.vars[name]
	if !ok || !b.known || l.err != nil {
		return "", false
	}
	if l.err = l.m.Charge(l.perByte * len(b.value)); l.err != nil {
		return "", false
	}
	return b.value, true
}

// Keys is none: the lexer asks a name at a time.
func (l *lookup) Keys() []string { return nil }
