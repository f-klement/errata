package jenkins

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/f-klement/errata/internal/extract/core"
)

// The scan reads the lexer's tokens into the few statements the reader
// looks for, keeping a stack of the blocks it is inside, each a { } and
// what opened it: pipeline, environment, stage('name'), parameters, axis,
// matrix, or anything else. It does not build a tree, and keeps of a statement
// only where its value starts and ends, so its memory is the depth of the
// file, at most core.MaxDepth, and what it records of the spec's variable.

// block is what opened a { }.
type block uint8

const (
	blockOther block = iota
	blockPipeline
	blockEnvironment
	blockStage
	blockParameters
	blockAxis
	blockMatrix
)

// blockNames are the names that open a block the scan knows.
var blockNames = map[string]block{
	"pipeline": blockPipeline, "environment": blockEnvironment, "stage": blockStage,
	"parameters": blockParameters, "axis": blockAxis, "matrix": blockMatrix,
}

// state is where a statement is, as the scan reads its tokens.
type state uint8

const (
	stStart    state = iota // no token yet
	stHead                  // a name, the head
	stArgs                  // inside the parentheses after the head
	stCalled                // after the head's parentheses
	stDot                   // after head.
	stProperty              // after env.NAME
	stDecl                  // after def, final, var or a type
	stDeclName              // after def NAME
	stAnnot                 // after @
	stValue                 // after the = of an assignment
	stOther                 // anything else, to the statement's end
)

// form is what kind of assignment a value comes from.
type form uint8

const (
	formNone        form = iota
	formEnvironment      // X = v in an environment block
	formEnv              // env.X = v
	formDef              // def X = v
)

// level is one block on the scan's stack, with the statement being read in
// it.
type level struct {
	block block
	stage int // the stage's number, for a stage's block
	// matrix is the number of the innermost matrix block this block is in,
	// 0 for none.
	matrix int
	// line is the line of the { that opened the block.
	line int
	// stageName is a stage's name, when it is literal, and counted whether
	// its environment has been counted as one that sets the variable.
	stageName []byte
	counted   bool
	// parens counts the ( and [ open in the statement.
	parens int
	st     state
	head   token
	// arg is the single string argument of a stage(...), when it has one.
	arg     token
	args    int
	assign  form
	name    bool // the assignment's name is the spec's variable, in any case
	exact   bool // and in the spec's case
	val     token
	valEnd  int
	valToks int
	// last is the statement's last token, for whether a line end goes on.
	last token
	// withEnv is the parens at which a withEnv( opened, 0 for none.
	withEnv int
	// prev to prev6 are the six tokens before, for name: 'X' in a
	// parameters or axis block, this.load, env.setProperty and
	// ++(this.env.X).
	prev, prev2, prev3, prev4, prev5, prev6 token
	// setProperty is whether the token before was the ( of an
	// env.setProperty( or an env.putAt(.
	setProperty bool
	// envName is whether the token before was the variable's name in
	// env.X, env?.X or this.env.X, in any case.
	envName bool
}

// record is one assignment of the spec's variable.
type record struct {
	form  form
	owner int // for formEnvironment: 0 the pipeline, else the stage's number
	val   token
	end   int
	toks  int
	// exact is whether the assignment spells the variable in the spec's
	// case; Jenkins's EnvVars does not tell two cases apart.
	exact bool
	line  int // the line of the assignment's name
}

// mention is a place in the file that makes the variable unknown: a
// library, a load, a parameter, a matrix axis, or a withEnv.
type mention struct {
	what string
	line int
}

// scan is one pass over a file for the variable name, and the stage target
// when the spec names one.
type scan struct {
	data   []byte
	name   []byte
	target []byte
	lx     *lexer
	stack  []level
	// open counts the blocks of each kind on the stack.
	opened [blockMatrix + 1]int
	// pushed is a token read ahead to see whether a line end goes on.
	pushed   *token
	stages   int // stages opened so far
	matrices int // matrix blocks opened so far
	pipeline int // pipeline blocks opened, which Jenkins allows one of
	// chain is the second pass's set of stages around the target, from
	// the first: their environment blocks are the ones that count.
	chain map[int]bool
	// cell is the second pass's set of matrix blocks around the target,
	// from the first: an axis of the variable in one sets it in each cell.
	cell map[int]bool
	// The first pass's findings.
	targets     int      // stages named target
	targetLine  []int    // the lines of the first two
	around      []int    // the stages around the first target, innermost first
	aroundCell  []int    // the matrix blocks around the first target
	unnamed     int      // the line of the first stage whose name is not literal, 0 for none
	settingIn   [][]byte // the first stages whose environment sets the variable
	settingMore int      // and how many more do
	pipelineEnv *record  // the pipeline's environment's last value
	stageEnv    map[int]record
	// records are the env.X = and def X = assignments, at most
	// core.MaxValues+1 of them, and only env.X = once envSets.
	records []record
	// envSets is whether an environment block or an env.X = sets the
	// variable anywhere in the file, in any case; then def X = does not
	// count, since a step's shell does not see a Groovy variable.
	envSets bool
	// The first of each kind of mention, nil for none. envOther is a way
	// to set the variable errata does not read, and reassigned an X = or
	// X += that changes what def X = set.
	loads, declared, withEnv, cellAxis, envOther, reassigned *mention
}

func newScan(data, name, target []byte, m *core.Meter) *scan {
	return &scan{data: data, name: name, target: target, lx: newLexer(data, m), stack: []level{{}}, stageEnv: map[int]record{}}
}

// token is the next token, the one read ahead first.
func (s *scan) token() (token, error) {
	if s.pushed != nil {
		t := *s.pushed
		s.pushed = nil
		return t, nil
	}
	return s.lx.next()
}

// run reads the whole file.
func (s *scan) run() error {
	for {
		t, err := s.token()
		if err != nil {
			return err
		}
		if t.kind == tokEOF {
			return s.end()
		}
		if err := s.feed(t); err != nil {
			return err
		}
	}
}

// goesOn are the operators that, first on a line, go on with the statement
// before: Groovy reads a method call or a ternary over a line end.
var goesOn = map[string]bool{".": true, "?.": true, "*.": true, ".&": true, ".@": true, "?.@": true, "?": true, ":": true, "?:": true, "&&": true, "||": true}

// endsStatement reports whether a line end ends the statement the level
// is in: an operator other than a closing bracket or a postfix ++ or --
// at the end of a line goes on to the next. A ++ or -- after no value is a
// prefix, which goes on to its operand (groovy.g 2815, INC^ nls!).
func endsStatement(lv *level) bool {
	t := lv.last
	if t.is("++") || t.is("--") {
		// lv.prev2 is the token before it, none when it starts the statement
		return lv.prev2.kind != tokEOF && isValue(lv.prev2)
	}
	return isValue(t)
}

// isValue reports whether t ends a value: it is no operator, or it is a
// closing bracket or a postfix ++ or --.
func isValue(t token) bool {
	return t.kind != tokOp || valueEnds[t.op]
}

// feed reads one token in the innermost block.
func (s *scan) feed(t token) error {
	lv := &s.stack[len(s.stack)-1]
	if t.kind == tokNewline {
		if lv.parens > 0 || lv.st == stStart {
			return nil
		}
		if !endsStatement(lv) {
			return nil
		}
		// A line that starts with . or ?: goes on with this statement.
		next, err := s.lx.next()
		for err == nil && next.kind == tokNewline {
			next, err = s.lx.next()
		}
		if err != nil {
			return err
		}
		s.pushed = &next
		if next.kind == tokOp && goesOn[next.op] {
			return nil
		}
		s.endStatement(len(s.stack) - 1)
		return nil
	}
	s.watch(lv, t)
	switch {
	case t.is(";") && lv.parens == 0:
		s.endStatement(len(s.stack) - 1)
		return nil
	case t.is("{"):
		return s.open(lv, t)
	case t.is("}"):
		return s.close(t)
	case t.is("(") || t.is("[") || t.is("?["):
		lv.parens++
	case t.is(")") || t.is("]"): // the lexer refuses one that closes nothing
		lv.parens--
		if lv.withEnv > lv.parens {
			lv.withEnv = 0
		}
	}
	s.step(lv, t)
	lv.last = t
	return nil
}

// watch looks at every token for what makes the variable unknown, whatever
// statement it is in: a library or a load, a withEnv that sets the
// variable, and a parameter or axis of its name.
func (s *scan) watch(lv *level, t token) {
	d := s.data
	switch {
	case lv.prev.kind == tokAt && t.kind == tokName && string(t.text(d)) == "Library":
		s.note(&s.loads, "the shared library of the @Library", t.line)
	case lv.prev.kind == tokName && loaders[string(lv.prev.text(d))] && (t.is("(") || t.kind == tokString || t.kind == tokName) &&
		(!member[lv.prev2.op] || lv.prev2.is(".") && s.isName(lv.prev3, "this")):
		s.note(&s.loads, "the file that "+string(lv.prev.text(d))+" reads", lv.prev.line)
	case s.isName(lv.prev, "libraries") && t.is("{"):
		s.note(&s.loads, "the shared library of the libraries directive", lv.prev.line)
	case s.isName(lv.prev, "withEnv") && t.is("("):
		lv.withEnv = lv.parens + 1
	case lv.withEnv > 0 && s.withEnvMaySet(t):
		s.note(&s.withEnv, "withEnv", t.line)
	case t.kind == tokString && s.declares(lv) && s.literalFold(t):
		what := "a parameter of the pipeline, which whoever starts a build chooses"
		if s.opened[blockAxis] > 0 {
			what = "an axis of a matrix, whose cells errata does not read"
			if s.cell[lv.matrix] {
				s.note(&s.cellAxis, what, t.line)
			}
		}
		s.note(&s.declared, what, t.line)
	}
	s.watchEnv(lv, t)
	s.watchChange(lv, t)
	lv.prev6, lv.prev5, lv.prev4, lv.prev3, lv.prev2, lv.prev = lv.prev5, lv.prev4, lv.prev3, lv.prev2, lv.prev, t
}

// watchChange looks for what changes the variable in a way the scan does
// not read: ++ or -- before or after X or env.X, in parentheses or not, an
// operator assignment to env.X, and an env.X = v anywhere but at a
// statement's start, which is the one the scan reads: env?.X = v,
// this.env.X = v, (env).X = v, if (c) env.X = v.
func (s *scan) watchChange(lv *level, t token) {
	d := s.data
	after := lv.envName
	lv.envName = false
	if after && t.is(")") { // (env.X)++
		lv.envName = true
		return
	}
	switch {
	case after && (t.is("++") || t.is("--") || t.kind == tokOp && compound[t.op]):
		s.note(&s.envOther, fmt.Sprintf("env.%s %s changes %s", s.name, t.op, s.name), t.line)
	case after && t.is("=") && lv.st != stProperty:
		s.note(&s.envOther, fmt.Sprintf("this assignment to env.%s may set %s", s.name, s.name), t.line)
	case t.kind == tokName && bytes.EqualFold(t.text(d), s.name) && s.afterEnv(lv):
		lv.envName = true
		if op := prefix(s.beforeEnv(lv)); op.is("++") || op.is("--") {
			s.note(&s.envOther, fmt.Sprintf("%s env.%s changes %s", op.op, s.name, s.name), t.line)
		}
	case (t.is("++") || t.is("--")) && s.postfixOnName(lv):
		s.note(&s.reassigned, fmt.Sprintf("%s%s", s.name, t.op), t.line)
	case s.isName(t, string(s.name)):
		if op := prefix(lv.prev, lv.prev2); op.is("++") || op.is("--") {
			s.note(&s.reassigned, fmt.Sprintf("%s%s", op.op, s.name), t.line)
		}
	}
}

// postfixOnName reports whether a ++ or -- after the last tokens changes
// the variable: X++ or (X)++, and not a.X++.
func (s *scan) postfixOnName(lv *level) bool {
	name := string(s.name)
	if lv.prev.is(")") && lv.prev3.is("(") {
		return s.isName(lv.prev2, name)
	}
	return s.isName(lv.prev, name) && !member[lv.prev2.op]
}

// prefix is the token before an operand whose two tokens before are
// before and beforeThat: before, or beforeThat when before is the operand's
// opening parenthesis, as in ++(X).
func prefix(before, beforeThat token) token {
	if before.is("(") {
		return beforeThat
	}
	return before
}

// afterEnv reports whether the name after the last tokens is a property of
// the env object: env.X, env?.X, this.env.X, this?.env.X or (env).X, and
// not x.env.X.
func (s *scan) afterEnv(lv *level) bool {
	if !lv.prev.is(".") && !lv.prev.is("?.") {
		return false
	}
	if lv.prev2.is(")") {
		return s.isName(lv.prev3, "env") && lv.prev4.is("(")
	}
	if !s.isName(lv.prev2, "env") {
		return false
	}
	return !member[lv.prev3.op] || s.thisBefore(lv)
}

// thisBefore reports whether this. or this?. comes before the env of an
// env.X whose last token is the X.
func (s *scan) thisBefore(lv *level) bool {
	return (lv.prev3.is(".") || lv.prev3.is("?.")) && s.isName(lv.prev4, "this")
}

// beforeEnv is the two tokens before the env reference that afterEnv
// found: before env, this.env or (env).
func (s *scan) beforeEnv(lv *level) (token, token) {
	if lv.prev2.is(")") || s.thisBefore(lv) {
		return lv.prev5, lv.prev6
	}
	return lv.prev3, lv.prev4
}

// watchEnv looks for the ways to set an environment variable errata does
// not read, which make the variable unknown when they may name it:
// env['X'], env."X", env?.'X', env.setProperty('X', v) and
// env.putAt('X', v), which env['X'] = v calls.
func (s *scan) watchEnv(lv *level, t token) {
	pending := lv.setProperty
	lv.setProperty = false
	switch {
	case pending:
	case s.isName(lv.prev2, "env") && (lv.prev.is("[") || lv.prev.is("?[")):
	case s.isName(lv.prev2, "env") && (lv.prev.is(".") || lv.prev.is("?.")) && t.kind == tokString:
	case s.isName(lv.prev3, "env") && (lv.prev2.is(".") || lv.prev2.is("?.")) && (s.isName(lv.prev, "setProperty") || s.isName(lv.prev, "putAt")):
		if t.is("(") {
			lv.setProperty = true
			return
		}
	default:
		return
	}
	if t.kind == tokString {
		if text, ok := literalText(t, s.data); ok && !bytes.EqualFold(text, s.name) {
			return
		}
	}
	s.note(&s.envOther, fmt.Sprintf("the env[...], env.\"...\", env.setProperty or env.putAt here may set %s", s.name), t.line)
}

// isName reports whether t is the name n.
func (s *scan) isName(t token, n string) bool {
	return t.kind == tokName && string(t.text(s.data)) == n
}

// literalFold reports whether t is a literal string that holds the
// variable's name, in any case.
func (s *scan) literalFold(t token) bool {
	text, ok := literalText(t, s.data)
	return ok && bytes.EqualFold(text, s.name)
}

// withEnvMaySet reports whether the token t inside a withEnv's
// parentheses may set the variable. Only literal strings, commas and
// brackets are read: a name, a call or an operator may build any list. A
// string sets the name before its first =, and PATH+NAME=v changes PATH;
// a string whose name is not literal may set any.
func (s *scan) withEnvMaySet(t token) bool {
	if t.kind != tokString {
		return !t.is(",") && !t.is("[") && !t.is("]") && !t.is(")")
	}
	body, ok := literalText(t, s.data)
	if !ok {
		body = t.body(s.data)
	}
	key, _, found := bytes.Cut(body, []byte("="))
	if bytes.ContainsAny(key, "$\\\r") {
		return true
	}
	key, _, _ = bytes.Cut(key, []byte("+"))
	return found && bytes.EqualFold(key, s.name)
}

// note keeps the first mention of a kind: one is enough to refuse.
func (s *scan) note(first **mention, what string, line int) {
	if *first == nil {
		*first = &mention{what, line}
	}
}

// member are the operators before a method's name: x.load(...) is no step.
var member = map[string]bool{".": true, "?.": true, "*.": true, ".&": true, "?.@": true, ".@": true}

// declares reports whether a string here names a parameter or an axis:
// after name: or name inside a parameters or axis block.
func (s *scan) declares(lv *level) bool {
	if named := s.isName(lv.prev, "name") || lv.prev.is(":") && s.isName(lv.prev2, "name"); !named {
		return false
	}
	return s.opened[blockParameters] > 0 || s.opened[blockAxis] > 0
}

// push enters a block, and pop leaves the innermost.
func (s *scan) push(l level) {
	if l.block != blockMatrix {
		l.matrix = s.stack[len(s.stack)-1].matrix
	}
	s.stack = append(s.stack, l)
	s.opened[l.block]++
}

func (s *scan) pop() {
	s.opened[s.stack[len(s.stack)-1].block]--
	s.stack = s.stack[:len(s.stack)-1]
}

// loaders are the steps that read Groovy errata does not: a shared library
// and a file the pipeline loads or evaluates.
var loaders = map[string]bool{"library": true, "load": true, "evaluate": true}

// declarers are the words that start a declaration: def X = v, and a type
// or modifier in place of def.
var declarers = map[string]bool{"def": true, "final": true, "var": true, "static": true}

// builtInTypes are the types of Groovy 2.4 that are keywords. Such a type,
// or a name that starts with a capital, followed by a name starts a
// declaration (groovy.g, declarationStart and upperCaseIdent).
var builtInTypes = map[string]bool{
	"void": true, "boolean": true, "byte": true, "char": true, "short": true, "int": true, "float": true,
	"long": true, "double": true,
}

// compound are the assignments that change a value by an operator.
var compound = map[string]bool{
	"+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "&=": true, "^=": true, "|=": true,
	"<<=": true, ">>=": true, ">>>=": true, "**=": true, "?=": true,
}

// step moves the statement's state on by one token at the block's level.
func (s *scan) step(lv *level, t token) {
	d := s.data
	if lv.st == stValue {
		if lv.valToks == 0 {
			lv.val = t
		}
		lv.valToks++
		lv.valEnd = t.end
		return
	}
	if lv.st == stArgs {
		if lv.parens == 0 { // the head's ) closed
			lv.st = stCalled
			return
		}
		if lv.args++; lv.args == 1 && t.kind == tokString {
			lv.arg = t
		}
		return
	}
	if lv.parens > 0 && lv.st != stHead {
		lv.st = stOther
		return
	}
	switch lv.st {
	case stStart:
		switch {
		case t.kind == tokName && (declarers[string(t.text(d))] || builtInTypes[string(t.text(d))]):
			lv.st = stDecl
		case t.kind == tokName:
			lv.st, lv.head = stHead, t
		case t.kind == tokAt:
			lv.st = stAnnot
		default:
			lv.st = stOther
		}
	case stHead:
		switch {
		case t.is("("):
			lv.st, lv.args, lv.arg = stArgs, 0, token{}
		case t.is("=") && lv.block == blockEnvironment:
			s.startValue(lv, formEnvironment, lv.head)
		case t.is("=") || t.kind == tokOp && compound[t.op]:
			lv.st = stOther
			if bytes.Equal(lv.head.text(d), s.name) {
				s.note(&s.reassigned, fmt.Sprintf("%s %s", s.name, t.op), t.line)
			}
		case t.is("."):
			lv.st = stDot
		case t.kind == tokName && isTypeName(lv.head.text(d)):
			lv.st, lv.head = stDeclName, t
		default:
			lv.st = stOther
		}
	case stDot:
		lv.st = stOther
		if t.kind == tokName && string(lv.head.text(d)) == "env" {
			lv.st, lv.head = stProperty, t
		}
	case stProperty:
		lv.st = stOther
		if t.is("=") {
			s.startValue(lv, formEnv, lv.head)
		}
	case stDecl:
		if t.kind == tokName && !declarers[string(t.text(d))] {
			lv.st, lv.head = stDeclName, t
		} else if t.kind != tokName {
			lv.st = stOther
		}
	case stDeclName:
		switch {
		case t.is("="):
			s.startValue(lv, formDef, lv.head)
		case t.kind == tokName:
			lv.head = t // the name before was a type
		default:
			lv.st = stOther
		}
	default:
		lv.st = stOther
	}
}

// startValue starts the value of an assignment to the name n. An
// environment variable's name matches in any case, a Groovy variable's
// only as written.
func (s *scan) startValue(lv *level, f form, n token) {
	lv.st, lv.assign, lv.valToks = stValue, f, 0
	lv.exact = bytes.Equal(n.text(s.data), s.name)
	lv.name = lv.exact || f != formDef && bytes.EqualFold(n.text(s.data), s.name)
	lv.head = n
}

// isTypeName reports whether a name before another name is a type, which
// makes the two a declaration: a name that starts with a capital.
func isTypeName(n []byte) bool { return 'A' <= n[0] && n[0] <= 'Z' }

// open enters a { }. After a head, or a head and its parentheses, it is the
// block the head names; anything else is a closure or a block of code.
func (s *scan) open(lv *level, t token) error {
	if len(s.stack) >= core.MaxDepth {
		return core.TooDeep(t.line)
	}
	b := blockOther
	if lv.parens == 0 && (lv.st == stHead || lv.st == stCalled) {
		b = blockNames[string(lv.head.text(s.data))]
	}
	if b == blockStage && lv.st != stCalled {
		b = blockOther // stage { } names no stage
	}
	nl := level{block: b, line: t.line}
	switch b {
	case blockPipeline:
		if s.pipeline++; s.pipeline == 2 {
			return core.LineError(t.line, "the file holds a second pipeline block, and Jenkins runs one; errata reads a file of one")
		}
	case blockStage:
		s.stages++
		nl.stage = s.stages
		nl.stageName = s.openStage(lv)
	case blockMatrix:
		s.matrices++
		nl.matrix = s.matrices
	default: // the other blocks need no note when they open
	}
	if lv.st == stValue {
		if lv.valToks++; lv.valToks == 1 {
			lv.val = t
		}
	} else {
		lv.st = stOther
	}
	lv.last = t
	s.push(nl)
	return nil
}

// openStage notes a stage, from the level whose stage(...) opens it, and
// returns its name when it is literal.
func (s *scan) openStage(lv *level) []byte {
	name, literal := []byte(nil), false
	if lv.args == 1 && lv.arg.kind == tokString {
		name, literal = literalText(lv.arg, s.data)
	}
	if !literal {
		if s.unnamed == 0 {
			s.unnamed = lv.head.line
		}
		return nil
	}
	if s.target == nil || !bytes.Equal(name, s.target) {
		return name
	}
	if s.targets++; s.targets <= 2 {
		s.targetLine = append(s.targetLine, lv.head.line)
	}
	if s.targets == 1 {
		s.around = append(s.around, s.stages)
		for i := len(s.stack) - 1; i >= 0; i-- {
			if s.stack[i].block == blockStage {
				s.around = append(s.around, s.stack[i].stage)
			}
			if s.stack[i].block == blockMatrix {
				s.aroundCell = append(s.aroundCell, s.stack[i].matrix)
			}
		}
	}
	return name
}

// close leaves a { }, ending the statement inside it. A } with no block
// open is Groovy's error.
func (s *scan) close(t token) error {
	if len(s.stack) == 1 {
		return core.LineError(t.line, "a } closes no block")
	}
	if s.stack[len(s.stack)-1].parens > 0 {
		return core.LineError(t.line, "a } closes a block in which a ( or [ is not closed")
	}
	s.endStatement(len(s.stack) - 1)
	s.pop()
	lv := &s.stack[len(s.stack)-1]
	if lv.st == stValue {
		lv.valToks++
		lv.valEnd = t.end
	}
	lv.last = t
	return nil
}

// end ends the file and the statement still open at its top. A block
// still open is Groovy's error.
func (s *scan) end() error {
	if len(s.stack) > 1 {
		return core.LineError(s.stack[len(s.stack)-1].line, "a { is not closed: errata looked for its } to the end of the file")
	}
	if s.stack[0].parens > 0 {
		return core.LineError(s.lx.lineOf(len(s.data)), "a ( or [ is not closed at the end of the file")
	}
	s.endStatement(0)
	return nil
}

// endStatement ends the statement of the block at depth i, and records an
// assignment of the variable it holds.
func (s *scan) endStatement(i int) {
	lv := &s.stack[i]
	if lv.st == stValue && lv.name && lv.valToks > 0 {
		s.assigned(i)
	}
	*lv = level{block: lv.block, stage: lv.stage, matrix: lv.matrix, line: lv.line, stageName: lv.stageName, counted: lv.counted}
}

// assigned records the assignment the block at depth i ends a statement
// with. Of an environment block only the last value counts, and only in
// the pipeline, at its top or in a stage.
func (s *scan) assigned(i int) {
	lv := &s.stack[i]
	r := record{form: lv.assign, val: lv.val, end: lv.valEnd, toks: lv.valToks, exact: lv.exact, line: lv.head.line}
	if lv.assign == formEnv && !lv.exact {
		s.note(&s.envOther, fmt.Sprintf("an env. assignment sets %s in another case, which Jenkins reads as the same name", s.name), r.line)
		return
	}
	// Once an environment block or an env.X = sets the variable, def X =
	// does not count: the def records go, and none is kept after, so they
	// never fill the cap that the env.X = records count toward.
	if lv.assign != formDef && !s.envSets {
		s.envSets = true
		s.records = slices.DeleteFunc(s.records, func(r record) bool { return r.form == formDef })
	}
	if lv.assign == formDef && s.envSets {
		return
	}
	if lv.assign != formEnvironment {
		if len(s.records) <= core.MaxValues {
			s.records = append(s.records, r)
		}
		return
	}
	if i == 0 || s.opened[blockPipeline] == 0 {
		return
	}
	switch parent := &s.stack[i-1]; parent.block {
	case blockPipeline:
		s.pipelineEnv = &r
	case blockStage:
		r.owner = parent.stage
		if s.chain[parent.stage] {
			s.stageEnv[parent.stage] = r
		}
		if !parent.counted && s.chain == nil {
			// The note of a spec that names no stage: the first stages whose
			// environment sets the variable, by name.
			parent.counted = true
			if len(s.settingIn) < 3 && parent.stageName != nil {
				s.settingIn = append(s.settingIn, parent.stageName)
			} else {
				s.settingMore++
			}
		}
	default: // an environment block anywhere else is none Jenkins reads
	}
}
