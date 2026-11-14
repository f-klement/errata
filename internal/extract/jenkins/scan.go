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
	// cond is whether the ( open at the level's top is the condition of an
	// if, a while or a for, and condEnd the end of the ) that closed the
	// last such condition, 0 for none.
	cond    bool
	condEnd int
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
	// For (X) = v and (a, X) = v: bare is whether the token before was
	// the variable's name right after a ( or a comma, group the count of
	// brackets open at a group that holds it alone between commas, 0 for
	// none, and closed whether the token before closed such a group.
	bare, closed bool
	group        int
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

// settingStage is a stage whose environment sets the variable: its name,
// nil when it is not literal, and the line of its block.
type settingStage struct {
	name []byte
	line int
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
	// code is a level for each ${ } the lexer is inside, the innermost
	// last, for what its code changes; the lexer nests at most
	// core.MaxDepth of them.
	code []level
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
	// setting is every stage whose environment sets the variable, in the
	// file's order, for Every.
	setting     []settingStage
	pipelineEnv *record // the pipeline's environment's last value
	stageEnv    map[int]record
	// records are the env.X = and def X = assignments, at most
	// keepRecords+1 of them, and only env.X = once envSets.
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

// keepRecords is how many records a scan keeps before the one more that
// shows it is past the cap: core.MaxValues. A test lowers it, so that the cap
// is reached in a few lines and not in 100,000 read three times under -race.
var keepRecords = core.MaxValues

func newScan(data, name, target []byte, m *core.Meter) *scan {
	s := &scan{data: data, name: name, target: target, lx: newLexer(data, m), stack: []level{{}}, stageEnv: map[int]record{}}
	s.lx.code = s
	return s
}

// enterCode, codeToken and leaveCode watch the code inside a string's ${ } for what
// makes the variable unknown, as watch does outside a string: an X = or
// an X += that changes a def X, a ++ or a --, an assignment to env.X, an
// env['X'], a withEnv or a load. The string stays one token of its
// statement, so an assignment there sets no value; and since no statement
// is read there, a line end inside a ${ } does not end one.
func (s *scan) enterCode() {
	s.code = append(s.code, level{matrix: s.stack[len(s.stack)-1].matrix})
}

func (s *scan) leaveCode() { s.code = s.code[:len(s.code)-1] }

func (s *scan) codeToken(t token) {
	lv := &s.code[len(s.code)-1]
	if t.kind == tokNewline {
		return
	}
	s.watch(lv, t)
	switch {
	case t.is("(") || t.is("[") || t.is("?["):
		lv.parens++
	case t.is(")") || t.is("]"):
		lv.parens--
		if lv.withEnv > lv.parens {
			lv.withEnv = 0
		}
	}
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
// prefix, which goes on to its operand (groovy.g 2815, INC^ nls!). So is
// one after a keyword that an expression follows, such as return or else,
// and one after the ) of the condition of an if, a while or a for.
func (s *scan) endsStatement(lv *level) bool {
	t := lv.last
	if !t.is("++") && !t.is("--") {
		return isValue(t)
	}
	// lv.prev2 is the token before it, none when it starts the statement
	before := lv.prev2
	switch {
	case before.kind == tokEOF:
		return false
	case before.kind == tokName && beforeOperand[string(before.text(s.data))] && !member[lv.prev3.op]:
		return false // a.in++ is a property's postfix
	case before.is(")") && before.end == lv.condEnd:
		return false
	}
	return isValue(before)
}

// beforeOperand are the keywords an expression follows, so a ++ or --
// after one is a prefix.
var beforeOperand = map[string]bool{"else": true, "return": true, "assert": true, "throw": true, "case": true, "in": true}

// conditions are the keywords whose ( ) holds a condition, after which a
// statement follows.
var conditions = map[string]bool{"if": true, "while": true, "for": true}

// isValue reports whether t ends a value: it is no operator, or it is a
// closing bracket or a postfix ++ or --.
func isValue(t token) bool {
	return t.kind != tokOp || valueEnds[t.op]
}

// feed reads one token in the innermost block.
func (s *scan) feed(t token) error {
	lv := &s.stack[len(s.stack)-1]
	if t.kind == tokNewline {
		return s.newline(lv)
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
	}
	s.bracket(lv, t)
	s.step(lv, t)
	lv.last = t
	return nil
}

// newline reads a line end in the block of lv, which ends the statement
// there unless the statement goes on: inside brackets, after an operator,
// or before a line that starts with . or ?:.
func (s *scan) newline(lv *level) error {
	if lv.parens > 0 || lv.st == stStart || !s.endsStatement(lv) {
		return nil
	}
	next, err := s.nextLine()
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

// nextLine is the first token after the line ends that follow.
func (s *scan) nextLine() (token, error) {
	next, err := s.lx.next()
	for err == nil && next.kind == tokNewline {
		next, err = s.lx.next()
	}
	return next, err
}

// bracket keeps the count of the ( and [ open in the statement of lv, with
// whether the ( open is an if's, a while's or a for's condition and where
// that condition ends, and ends a withEnv( that t closes.
func (s *scan) bracket(lv *level, t token) {
	switch {
	case opens(t):
		if s.opensCondition(lv, t) {
			lv.cond = true
		}
		lv.parens++
	case closes(t): // the lexer refuses one that closes nothing
		lv.parens--
		if lv.parens == 0 && lv.cond {
			lv.cond, lv.condEnd = false, t.end
		}
		if lv.withEnv > lv.parens {
			lv.withEnv = 0
		}
	}
}

// opensCondition reports whether t is the ( after if, while or for, at
// the statement's own level.
func (s *scan) opensCondition(lv *level, t token) bool {
	return t.is("(") && lv.parens == 0 && lv.prev2.kind == tokName && conditions[string(lv.prev2.text(s.data))]
}

// opens reports whether t opens a ( or a [.
func opens(t token) bool { return t.is("(") || t.is("[") || t.is("?[") }

// closes reports whether t closes a ( or a [.
func closes(t token) bool { return t.is(")") || t.is("]") }

// watch looks at every token for what makes the variable unknown, whatever
// statement it is in: a library or a load, a withEnv that sets the
// variable, and a parameter or axis of its name.
func (s *scan) watch(lv *level, t token) {
	if !s.watchLoad(lv, t) {
		s.watchEnvAndDeclared(lv, t)
	}
	s.watchEnv(lv, t)
	s.watchChange(lv, t)
	lv.prev6, lv.prev5, lv.prev4, lv.prev3, lv.prev2, lv.prev = lv.prev5, lv.prev4, lv.prev3, lv.prev2, lv.prev, t
}

// watchLoad notes a library or a file that t, after the last tokens,
// loads, and reports whether it does: an @Library, a load, library or
// evaluate step, or a libraries directive.
func (s *scan) watchLoad(lv *level, t token) bool {
	switch {
	case lv.prev.kind == tokAt && s.isName(t, "Library"):
		s.note(&s.loads, "the shared library of the @Library", t.line)
	case s.loadsFile(lv, t):
		s.note(&s.loads, "the file that "+string(lv.prev.text(s.data))+" reads", lv.prev.line)
	case s.isName(lv.prev, "libraries") && t.is("{"):
		s.note(&s.loads, "the shared library of the libraries directive", lv.prev.line)
	default:
		return false
	}
	return true
}

// loadsFile reports whether the name before t is a step that loads Groovy
// errata does not read, called with t as its argument or its parenthesis.
func (s *scan) loadsFile(lv *level, t token) bool {
	return lv.prev.kind == tokName && loaders[string(lv.prev.text(s.data))] && startsArgs(t) && s.calledAsStep(lv)
}

// startsArgs reports whether t starts a call's arguments: a ( or, in a
// command call, a string or a name.
func startsArgs(t token) bool {
	return t.is("(") || t.kind == tokString || t.kind == tokName
}

// calledAsStep reports whether the name before the last token is a step's:
// no member operator comes before it, or only this.
func (s *scan) calledAsStep(lv *level) bool {
	return !member[lv.prev2.op] || lv.prev2.is(".") && s.isName(lv.prev3, "this")
}

// watchEnvAndDeclared keeps where a withEnv( opens and notes a withEnv
// that may set the variable, and notes a parameter or an axis of its name.
func (s *scan) watchEnvAndDeclared(lv *level, t token) {
	switch {
	case s.isName(lv.prev, "withEnv") && t.is("("):
		lv.withEnv = lv.parens + 1
	case lv.withEnv > 0 && s.withEnvMaySet(t):
		s.note(&s.withEnv, "withEnv", t.line)
	case t.kind == tokString && s.declares(lv) && s.literalFold(t):
		s.noteDeclared(lv, t)
	}
}

// noteDeclared notes the parameter or the axis of the variable's name that the
// string t declares. An axis in a matrix around the target sets the
// variable in the target's cell, and is noted as that too.
func (s *scan) noteDeclared(lv *level, t token) {
	what := "a parameter of the pipeline, which whoever starts a build chooses"
	if s.opened[blockAxis] > 0 {
		what = "an axis of a matrix, whose cells errata does not read"
		if s.cell[lv.matrix] {
			s.note(&s.cellAxis, what, t.line)
		}
	}
	s.note(&s.declared, what, t.line)
}

// watchChange looks for what changes the variable in a way the scan does
// not read: an X = or X += anywhere, ++ or -- before or after X or env.X,
// in parentheses or not, an operator assignment to env.X, and an
// env.X = v anywhere but at a statement's start, which is the one the
// scan reads: env?.X = v, this.env.X = v, (env).X = v, if (c) env.X = v.
func (s *scan) watchChange(lv *level, t token) {
	grouped := s.groupAssigned(lv, t)
	if assigns(t) && (grouped || s.reassigns(lv)) {
		s.note(&s.reassigned, fmt.Sprintf("%s %s", s.name, t.op), t.line)
	}
	after := lv.envName
	lv.envName = false
	if after && t.is(")") { // (env.X)++
		lv.envName = true
		return
	}
	if after && s.envChanged(lv, t) {
		return
	}
	s.watchName(lv, t)
}

// envChanged notes an operator t after env.X that changes the variable, or
// an = that may set it where the scan does not read the assignment, and
// reports whether it did.
func (s *scan) envChanged(lv *level, t token) bool {
	switch {
	case isIncrement(t) || isCompound(t):
		s.note(&s.envOther, fmt.Sprintf("env.%s %s changes %s", s.name, t.op, s.name), t.line)
	case t.is("=") && lv.st != stProperty:
		s.note(&s.envOther, fmt.Sprintf("this assignment to env.%s may set %s", s.name, s.name), t.line)
	default:
		return false
	}
	return true
}

// watchName looks at the variable's name t, and at a ++ or -- after it:
// env.X, with a ++ or -- before the env, and X with one before or after it.
func (s *scan) watchName(lv *level, t token) {
	switch {
	case t.kind == tokName && bytes.EqualFold(t.text(s.data), s.name) && s.afterEnv(lv):
		s.envNamed(lv, t)
	case isIncrement(t) && s.postfixOnName(lv):
		s.note(&s.reassigned, fmt.Sprintf("%s%s", s.name, t.op), t.line)
	case s.isName(t, string(s.name)):
		if op := prefix(lv.prev, lv.prev2); isIncrement(op) {
			s.note(&s.reassigned, fmt.Sprintf("%s%s", op.op, s.name), t.line)
		}
	}
}

// envNamed marks the variable's name t after env., for the token after
// it, and notes a ++ or -- before the env reference.
func (s *scan) envNamed(lv *level, t token) {
	lv.envName = true
	if op := prefix(s.beforeEnv(lv)); isIncrement(op) {
		s.note(&s.envOther, fmt.Sprintf("%s env.%s changes %s", op.op, s.name, s.name), t.line)
	}
}

// assigns reports whether t is an = or a compound assignment.
func assigns(t token) bool { return t.is("=") || isCompound(t) }

// isCompound reports whether t is a compound assignment, such as +=.
func isCompound(t token) bool { return t.kind == tokOp && compound[t.op] }

// isIncrement reports whether t is ++ or --.
func isIncrement(t token) bool { return t.is("++") || t.is("--") }

// reassigns reports whether an = or a compound assignment after the last
// tokens assigns the variable, wherever it stands: X = v, if (c) X = v,
// def Y = X = v or f(X = v). Not a.X = v, nor def X = v or String X = v,
// where the name before it declares it: def, final, static, var, a
// built in type or a name with a capital, as step reads a declaration
// (groovy.g, declarationStart), or another modifier, as in a class's
// private X = v. Any other name before it is a keyword an
// expression follows or a command call, so else X = v, return X = v and
// echo X = v count. In an environment block X = v sets the variable, and
// then no def X counts.
func (s *scan) reassigns(lv *level) bool {
	before := lv.prev2
	n := before.text(s.data)
	declares := before.kind == tokName &&
		(declarers[string(n)] || modifiers[string(n)] || builtInTypes[string(n)] || isTypeName(n))
	return s.isName(lv.prev, string(s.name)) && !member[before.op] && !declares
}

// groupAssigned reports whether t, after the last tokens, is an = or a
// compound assignment to a group of brackets that holds the variable's
// name alone between its commas: (X) = v, ((X)) = v and the multiple
// assignment (a, X) = v. For any other t it moves the group's state on
// by t, a step of constant work: lv.parens is the count of brackets open
// before t.
func (s *scan) groupAssigned(lv *level, t token) bool {
	inGroup := lv.group > 0 && lv.group == lv.parens
	if s.groupStep(lv, t, inGroup) {
		return true
	}
	if inGroup && closes(t) {
		lv.group = 0
	}
	return false
}

// groupStep moves the group's state on by t, and reports whether t
// assigns the group that closed before it. inGroup is whether the
// brackets open before t are the group's.
func (s *scan) groupStep(lv *level, t token, inGroup bool) bool {
	closed, bare := lv.closed, lv.bare
	lv.closed, lv.bare = false, false
	switch {
	case closed:
		lv.closed = t.is(")") // ((X)) = v
		return assigns(t)
	case t.is(")") && (bare || inGroup):
		lv.closed = true
	case t.is(",") && bare:
		lv.group = lv.parens
	case s.bareName(lv, t):
		lv.bare = true
	}
	return false
}

// bareName reports whether t is the variable's name right after a ( or a
// comma inside brackets.
func (s *scan) bareName(lv *level, t token) bool {
	return s.isName(t, string(s.name)) && lv.parens > 0 && (lv.prev.is("(") || lv.prev.is(","))
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
	// At the ( of env.setProperty( or env.putAt( the key comes next.
	lv.setProperty = s.envSetter(lv) && t.is("(")
	if lv.setProperty {
		return
	}
	if !pending && !s.mayNameEnv(lv, t) {
		return
	}
	if s.namesOther(t) {
		return
	}
	s.note(&s.envOther, fmt.Sprintf("the env[...], env.\"...\", env.setProperty or env.putAt here may set %s", s.name), t.line)
}

// mayNameEnv reports whether t, after the last tokens, names a variable
// of env in a way errata does not read: the key of env[ or env?[, the
// string after env. or env?., or the argument of an env.setProperty or
// env.putAt called without a (. watchEnv handles the ( of one before it
// asks.
func (s *scan) mayNameEnv(lv *level, t token) bool {
	if s.envSetter(lv) {
		return true
	}
	if !s.isName(lv.prev2, "env") {
		return false
	}
	return isIndex(lv.prev) || isDot(lv.prev) && t.kind == tokString
}

// envSetter reports whether the last tokens are env.setProperty or
// env.putAt, with . or ?..
func (s *scan) envSetter(lv *level) bool {
	return s.isName(lv.prev3, "env") && isDot(lv.prev2) && (s.isName(lv.prev, "setProperty") || s.isName(lv.prev, "putAt"))
}

// namesOther reports whether t is a literal string that names a variable
// other than the spec's.
func (s *scan) namesOther(t token) bool {
	if t.kind != tokString {
		return false
	}
	text, ok := literalText(t, s.data)
	return ok && !bytes.EqualFold(text, s.name)
}

// isDot reports whether t is . or ?..
func isDot(t token) bool { return t.is(".") || t.is("?.") }

// isIndex reports whether t is [ or ?[.
func isIndex(t token) bool { return t.is("[") || t.is("?[") }

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

// modifiers are Groovy's other modifiers, which declare another variable
// where they stand before X, as a class's private X = v does. Only
// reassigns reads them: in declarers, step would read private X = v as a
// def X = v.
var modifiers = map[string]bool{
	"private": true, "protected": true, "public": true, "transient": true, "volatile": true,
	"synchronized": true, "abstract": true, "native": true, "strictfp": true,
}

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
	switch {
	case lv.st == stValue:
		lv.addValue(t)
	case lv.st == stArgs:
		lv.addArg(t)
	case lv.parens > 0 && lv.st != stHead:
		lv.st = stOther
	default:
		s.stepStatement(lv, t)
	}
}

// addValue adds t to the value of the assignment the statement holds.
func (lv *level) addValue(t token) {
	if lv.valToks == 0 {
		lv.val = t
	}
	lv.valToks++
	lv.valEnd = t.end
}

// addArg adds t to the arguments in the parentheses after the head, or
// ends them at the ) that closes them.
func (lv *level) addArg(t token) {
	if lv.parens == 0 { // the head's ) closed
		lv.st = stCalled
		return
	}
	if lv.args++; lv.args == 1 && t.kind == tokString {
		lv.arg = t
	}
}

// stepStatement moves the state of a statement outside brackets on by t,
// one helper for each state that reads further.
func (s *scan) stepStatement(lv *level, t token) {
	switch lv.st {
	case stStart:
		s.stepStart(lv, t)
	case stHead:
		s.stepHead(lv, t)
	case stDot:
		s.stepDot(lv, t)
	case stProperty:
		s.stepProperty(lv, t)
	case stDecl:
		s.stepDecl(lv, t)
	case stDeclName:
		s.stepDeclName(lv, t)
	default:
		lv.st = stOther
	}
}

// stepStart reads the statement's first token: a declaration's def or
// type, a head, or an annotation's @.
func (s *scan) stepStart(lv *level, t token) {
	d := s.data
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
}

// stepHead reads the token after a head: its parentheses, an assignment,
// a . or the name a type declares.
func (s *scan) stepHead(lv *level, t token) {
	switch {
	case t.is("("):
		lv.st, lv.args, lv.arg = stArgs, 0, token{}
	case assigns(t): // watchChange notes X = v
		lv.st = stOther
		if t.is("=") && lv.block == blockEnvironment {
			s.startValue(lv, formEnvironment, lv.head)
		}
	case t.is("."):
		lv.st = stDot
	case t.kind == tokName && isTypeName(lv.head.text(s.data)):
		lv.st, lv.head = stDeclName, t
	default:
		lv.st = stOther
	}
}

// stepDot reads the name after head., a property of env.
func (s *scan) stepDot(lv *level, t token) {
	lv.st = stOther
	if t.kind == tokName && string(lv.head.text(s.data)) == "env" {
		lv.st, lv.head = stProperty, t
	}
}

// stepProperty reads the token after env.NAME, whose = starts a value.
func (s *scan) stepProperty(lv *level, t token) {
	lv.st = stOther
	if t.is("=") {
		s.startValue(lv, formEnv, lv.head)
	}
}

// stepDecl reads the token after def or a type: the declared name, or
// another declarer.
func (s *scan) stepDecl(lv *level, t token) {
	if t.kind == tokName && !declarers[string(t.text(s.data))] {
		lv.st, lv.head = stDeclName, t
	} else if t.kind != tokName {
		lv.st = stOther
	}
}

// stepDeclName reads the token after a declared name: the = of its value,
// or the name it declares when the name before was a type.
func (s *scan) stepDeclName(lv *level, t token) {
	switch {
	case t.is("="):
		s.startValue(lv, formDef, lv.head)
	case t.kind == tokName:
		lv.head = t // the name before was a type
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
	nl := level{block: s.blockOf(lv), line: t.line}
	if err := s.countBlock(lv, &nl, t); err != nil {
		return err
	}
	lv.openValue(t)
	lv.last = t
	s.push(nl)
	return nil
}

// blockOf is the kind of block a { opens after the statement of lv.
func (s *scan) blockOf(lv *level) block {
	b := blockOther
	if lv.parens == 0 && (lv.st == stHead || lv.st == stCalled) {
		b = blockNames[string(lv.head.text(s.data))]
	}
	if b == blockStage && lv.st != stCalled {
		b = blockOther // stage { } names no stage
	}
	return b
}

// countBlock counts the pipeline, stage or matrix block nl that the { t
// opens after the statement of lv, and numbers it. A second pipeline is
// refused.
func (s *scan) countBlock(lv *level, nl *level, t token) error {
	switch nl.block {
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
	return nil
}

// openValue adds the { t to the value the statement holds, or ends the
// statement's reading when it holds none.
func (lv *level) openValue(t token) {
	if lv.st != stValue {
		lv.st = stOther
		return
	}
	if lv.valToks++; lv.valToks == 1 {
		lv.val = t
	}
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
	if s.target != nil && bytes.Equal(name, s.target) {
		s.targetStage(lv)
	}
	return name
}

// targetStage notes a stage named the target, from the level whose
// stage(...) opens it: the lines of the first two, and the stages and
// matrix blocks around the first.
func (s *scan) targetStage(lv *level) {
	if s.targets++; s.targets <= 2 {
		s.targetLine = append(s.targetLine, lv.head.line)
	}
	if s.targets != 1 {
		return
	}
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
	if lv.assign != formDef {
		s.setsEnv()
	}
	switch {
	case lv.assign == formDef && s.envSets:
	case lv.assign != formEnvironment:
		if len(s.records) <= keepRecords {
			s.records = append(s.records, r)
		}
	default:
		s.environmentValue(i, r)
	}
}

// setsEnv notes that an environment block or an env.X = sets the
// variable. From then on def X = does not count: the def records go, and
// none is kept after, so they never fill the cap that the env.X = records
// count toward.
func (s *scan) setsEnv() {
	if s.envSets {
		return
	}
	s.envSets = true
	s.records = slices.DeleteFunc(s.records, func(r record) bool { return r.form == formDef })
}

// environmentValue records r, the value an environment block at depth i
// gives the variable, when the block is the pipeline's or a stage's.
func (s *scan) environmentValue(i int, r record) {
	if i == 0 || s.opened[blockPipeline] == 0 {
		return
	}
	switch parent := &s.stack[i-1]; parent.block {
	case blockPipeline:
		s.pipelineEnv = &r
	case blockStage:
		s.stageValue(parent, r)
	default: // an environment block anywhere else is none Jenkins reads
	}
}

// stageValue records r, the value the environment of the stage of level
// parent gives the variable.
func (s *scan) stageValue(parent *level, r record) {
	r.owner = parent.stage
	if s.chain[parent.stage] {
		s.stageEnv[parent.stage] = r
	}
	if parent.counted || s.chain != nil {
		return
	}
	// The note of a spec that names no stage: the first stages whose
	// environment sets the variable, by name.
	parent.counted = true
	s.setting = append(s.setting, settingStage{name: parent.stageName, line: parent.line})
	if len(s.settingIn) < 3 && parent.stageName != nil {
		s.settingIn = append(s.settingIn, parent.stageName)
	} else {
		s.settingMore++
	}
}
