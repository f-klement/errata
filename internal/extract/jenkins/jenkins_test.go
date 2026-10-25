package jenkins

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// got reads body as a Jenkinsfile for spec and gives its values as
// text@line, with ! after a not literal one, or the error.
func got(t *testing.T, body string, spec Spec) string {
	t.Helper()
	if spec.File == "" {
		spec.File = "Jenkinsfile"
	}
	values, err := extracttest.Run(t, Build, spec, extracttest.Repo(t, map[string]string{spec.File: body}))
	if err != nil {
		var none *check.NoValue
		if errors.As(err, &none) {
			return "none: " + none.Reason
		}
		return "error: " + err.Error()
	}
	var out []string
	for _, v := range values {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, fmt.Sprintf("%s@%d%s", v.Text, v.At.Line, mark))
	}
	return strings.Join(out, " ")
}

const pipeline = `pipeline {
    agent any
    environment {
        TAG = '1.0'
        IMAGE = "registry/app"
    }
    stages {
        stage('build') {
            environment {
                TAG = '1.1'
            }
            steps { sh 'make' }
        }
        stage('test') {
            steps { sh 'make test' }
        }
        stage('deploy') {
            stages {
                stage('push') {
                    steps { sh 'push' }
                }
                stage('roll') {
                    environment { TAG = "2.0" }
                    steps { sh 'roll' }
                }
            }
            environment { TAG = '1.9' }
        }
    }
}
`

// A stage's environment is over the pipeline's, an enclosing stage's over
// the pipeline's, and the innermost over every other; a spec without a
// stage reads the pipeline's.
func TestJenkinsStageOverPipeline(t *testing.T) {
	for _, c := range []struct{ stage, want string }{
		{"", "1.0@4"},
		{"build", "1.1@10"},
		{"test", "1.0@4"},
		{"deploy", "1.9@27"},
		{"push", "1.9@27"},
		{"roll", "2.0@23"},
	} {
		if g := got(t, pipeline, Spec{Variable: "TAG", Stage: c.stage}); g != c.want {
			t.Errorf("stage %q: got %s, want %s", c.stage, g, c.want)
		}
	}
	if g := got(t, pipeline, Spec{Variable: "IMAGE", Stage: "roll"}); g != "registry/app@5" {
		t.Errorf("IMAGE: %s", g)
	}
}

// With no environment block that sets it, the variable's values are every
// env.X = in the file, in file order, and every def X = when no env.X =
// sets it either. A type, a type that is a keyword, or final may stand for
// def.
func TestJenkinsAssignments(t *testing.T) {
	body := `def TAG = '1.0'
String OTHER = 'x'
final String TAG2 = "y"
pipeline {
  agent any
  stages {
    stage('a') {
      steps {
        script {
          env.TAG = "2.0"
          env.TAG = 3
          env.TAG = 'a' + 'b'
          other.TAG = 'no'
          env.TAGS = 'no'
        }
      }
    }
  }
}
`
	if g, want := got(t, body, Spec{Variable: "TAG"}), "2.0@10 3@11 'a' + 'b'@12!"; g != want {
		t.Errorf("got %s, want %s", g, want)
	}
	if g, want := got(t, body, Spec{Variable: "TAG2"}), "y@3"; g != want {
		t.Errorf("got %s, want %s", g, want)
	}
	if g, want := got(t, body, Spec{Variable: "OTHER", Stage: "a"}), "x@2"; g != want {
		t.Errorf("got %s, want %s", g, want)
	}
	for _, c := range []struct{ body, want string }{
		{"Integer X = 5\n", "5@1"},
		{"int X = 1\n", "1@1"},
		{"boolean X = true\n", "true@1!"},
		{"Map X = [:]\n", "[:]@1!"},
		{"def Map X = 'v'\n", "v@1"},
		{"final int X = 2\n", "2@1"},
		{"static final def X = 'f'\n", "f@1"},
		{"A X = 'a'\n", "a@1"},
		{"Z X = 'z'\n", "z@1"},
		{"foo X = 1\n", "none: " + noneX},
		{"def x = 'a'\n", "none: " + noneX},
	} {
		if g := got(t, c.body, Spec{Variable: "X"}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// noneX is the reason of a spec for X without stage: that no file sets.
const noneX = "no file sets X: errata read the pipeline's environment block, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read"

// def X = counts only when no environment block and no env.X = sets X
// anywhere in the file: a step's shell does not see a Groovy variable. The
// owner's decision of 2026-10-24 on #37. A def X = that an X =, an
// X += or an X++ changes, before it or after it, is unknown; the
// correctness review's witnesses of ++ and --, which gave 1@1.
func TestJenkinsDefOnlyWithoutEnv(t *testing.T) {
	for _, c := range []struct{ body, stage, want string }{
		{"def X = 'a'\n", "", "a@1"},
		{"def X = 'a'\nnode { env.X = 'b' }\n", "", "b@2"},
		{"def X = 'a'\nnode { env.x = 'b' }\n", "", "error: Jenkinsfile: line 2: an env. assignment sets X in another case, which Jenkins reads as the same name; errata does not read it"},
		{"def X = 'a'\npipeline {\n  stages {\n    stage('s') { environment { X = 'b' } }\n    stage('t') { }\n  }\n}\n", "t", "none: no file sets X: errata read the environment blocks of stage \"t\", the stages around it and the pipeline, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read"},
		{"def X = 'a'\nX = 'b'\n", "", "error: Jenkinsfile: line 2: X = changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 'a'\nX += 'b'\n", "", "error: Jenkinsfile: line 2: X += changes what def X = set, and errata does not follow which value a step sees"},
		{"X = 'b'\nenv.X = 'a'\n", "", "a@2"},
		{"def X = 1\nX++\n", "", "error: Jenkinsfile: line 2: X++ changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\nX--\n", "", "error: Jenkinsfile: line 2: X-- changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\n++X\n", "", "error: Jenkinsfile: line 2: ++X changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\nsh \"${--X}\"; y = --X\n", "", "error: Jenkinsfile: line 2: --X changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\ndef Y = X++\n", "", "error: Jenkinsfile: line 2: X++ changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\na.X++; x++; ++a.X; X\n++y; ++\nX\n", "", "error: Jenkinsfile: line 4: ++X changes what def X = set, and errata does not follow which value a step sees"},
		// Round 3: a prefix ++ or -- at a line's end goes on to its
		// operand on the next (groovy.g 2815, INC^ nls!); the delta
		// review's R2-D1, which gave 1@1. A postfix one ends the statement.
		{"def X = 1\ny = ++\nX\n", "", "error: Jenkinsfile: line 3: ++X changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\n--\n  X\n", "", "error: Jenkinsfile: line 3: --X changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\ny++\nX\n", "", "1@1"},
		// An operand in parentheses is the same operand (groovy.g 2866 to
		// 2887); R2-N1, which gave 1@1.
		{"def X = 1\n(X)++\n", "", "error: Jenkinsfile: line 2: X++ changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\ny = ++(X)\n", "", "error: Jenkinsfile: line 2: ++X changes what def X = set, and errata does not follow which value a step sees"},
		{"def X = 1\n(a.X)++; (x)++; ++(a.X); ++(x); y = (X) + 1\n", "", "1@1"},
	} {
		if g := got(t, c.body, Spec{Variable: "X", Stage: c.stage}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// What Groovy or Jenkins computes comes back as written, marked not
// literal.
func TestJenkinsNotLiteral(t *testing.T) {
	for _, c := range []struct{ value, want string }{
		{`'plain'`, "plain"},
		{`"plain"`, "plain"},
		{`'''plain'''`, "plain"},
		{`"""plain"""`, "plain"},
		{`/plain/`, "plain"},
		{`$/plain/$`, "plain"},
		{`'a\tb'`, "a\tb"},
		{`'it\'s'`, "it's"},
		{`"q\"q"`, `q"q`},
		{`'a\\b'`, `a\b`},
		{`'a\sb'`, `'a\sb'!`},
		{`42`, "42"},
		{`0`, "0"},
		{`"${BUILD_NUMBER}"`, `"${BUILD_NUMBER}"!`},
		{`"$BUILD_NUMBER"`, `"$BUILD_NUMBER"!`},
		{`'$HOME'`, `'$HOME'!`},
		{`credentials('id')`, `credentials('id')!`},
		{`sh(script: 'git rev-parse HEAD', returnStdout: true).trim()`, `sh(script: 'git rev-parse HEAD', returnStdout: true).trim()!`},
		{`'a' + 'b'`, `'a' + 'b'!`},
		{`010`, "010!"},
		{`1_000`, "1_000!"},
		{`1.50`, "1.50!"},
		{`true`, "true!"},
		{"'\\u0041'", "'\\u0041'!"},
		{`'\101'`, `'\101'!`},
		{`/a\d/`, `/a\d/!`},
		{`"a\rb"`, "a\rb"},
		{"'''a\r\nb'''", "'''a\r\nb'''!"},
		{`-1`, "-1!"},
		{`params.TAG`, "params.TAG!"},
	} {
		body := "pipeline {\n  environment {\n    X = " + c.value + "\n  }\n}\n"
		want := c.want + "@3"
		if text, computed := strings.CutSuffix(c.want, "!"); computed {
			want = text + "@3!"
		}
		if g := got(t, body, Spec{Variable: "X"}); g != want {
			t.Errorf("%s: got %q, want %q", c.value, g, want)
		}
	}
}

// Text in a comment or a string is no assignment, and neither is an
// assignment to another name, a member of another object or a variable of
// a block that is no environment block.
func TestJenkinsTextIsNoAssignment(t *testing.T) {
	body := `// env.X = 'comment'
def y = 1 // env.X = 'after a value'
def z = 2 /* env.X = 'after a value' */
/* def X = 'block'
   env.X = 'block' */
def s = 'env.X = "string"'
def g = "${ env.X == 'compare' }"
def h = """
env.X = 'triple'
"""
def r = /env.X = 'slashy'/
def d = $/env.X = 'dollar'/$
foo.X = 'member'
X = 'bare'
env.X == 'compare'
pipeline {
  options { X = 'options' }
  stages {
    stage('a') {
      steps { echo "X = 'echo'" }
    }
  }
}
`
	want := "none: no file sets X: errata read the pipeline's environment block, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read"
	if g := got(t, body, Spec{Variable: "X"}); g != want {
		t.Errorf("got %s", g)
	}
}

// A value goes on over a line end after an operator, inside brackets or a
// closure, and before a line that starts with a method call; anything
// else ends it.
func TestJenkinsValueSpansLines(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"env.X = 'a' +\n  'b'\n", "'a' +\n  'b'@1!"},
		{"env.X = sh(\n  script: 'x'\n)\n", "sh(\n  script: 'x'\n)@1!"},
		{"env.X = 'a'\n  .trim()\nenv.Y = 1\n", "'a'\n  .trim()@1!"},
		{"env.X = 'a'\n\n  ?.trim()\n", "'a'\n\n  ?.trim()@1!"},
		{"env.X = { -> 'a'\n}\n", "{ -> 'a'\n}@1!"},
		{"env.X = 'a'\n'b'\n", "a@1"},
		{"env.X = 'a'; env.X = 'b'\n", "a@1 b@1"},
		{"env.X =\n  'a'\n", "a@2"},
		{"if (x) { env.X = 'a' }\n", "a@1"},
		{"env.X = 'a' // why\n", "a@1"},
		{"def String X = 'a'\n", "a@1"},
		{"env.X = i++\nenv.X = 'v'\n", "i++@1! v@2"},
		{"env.X = a\n  ?: 'b'\n", "a\n  ?: 'b'@1!"},
		{"env.X = a\n  && b\n", "a\n  && b@1!"},
		{"env.X = a\n  || b\n", "a\n  || b@1!"},
		{"def X\nX = 'a'\n", "none: no file sets X: errata read the pipeline's environment block, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read"},
	} {
		if g := got(t, c.body, Spec{Variable: "X"}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// What only Jenkins knows makes the variable unknown: a shared library, a
// file the pipeline loads, a build parameter, a matrix axis, and a withEnv
// that sets it. A library or a parameter does not override an environment
// block, and a withEnv does, for the steps inside it.
func TestJenkinsUnknown(t *testing.T) {
	for _, c := range []struct{ body, stage, want string }{
		{"@Library('tools') _\nenv.X = 'a'\n", "", "error: Jenkinsfile: line 1: the shared library of the @Library may set X, and errata cannot read it"},
		{"library 'tools@1'\n", "", "error: Jenkinsfile: line 1: the file that library reads may set X, and errata cannot read it"},
		{"library identifier: 'tools@1'\n", "", "error: Jenkinsfile: line 1: the file that library reads may set X, and errata cannot read it"},
		{"\ndef lib = load 'lib.groovy'\n", "", "error: Jenkinsfile: line 2: the file that load reads may set X, and errata cannot read it"},
		{"evaluate(readFile('a.groovy'))\n", "", "error: Jenkinsfile: line 1: the file that evaluate reads may set X, and errata cannot read it"},
		{"x.load('a')\nenv.X = 'a'\n", "", "a@2"},
		{"pipeline {\n  parameters {\n    string(name: 'X', defaultValue: '1')\n  }\n}\n", "", "error: Jenkinsfile: line 3: X is a parameter of the pipeline, which whoever starts a build chooses"},
		{"pipeline {\n  parameters { string name: 'X' }\n  environment { X = 'e' }\n}\n", "", "e@3"},
		{"pipeline {\n  parameters { string(name: 'Y'); booleanParam(name: 'X') }\n}\n", "", "error: Jenkinsfile: line 2: X is a parameter of the pipeline, which whoever starts a build chooses"},
		{"pipeline {\n  stages {\n    stage('m') {\n      matrix {\n        axes {\n          axis {\n            name 'X'\n            values 'a', 'b'\n          }\n        }\n        stages { stage('in') { steps { sh 'a' } } }\n      }\n    }\n  }\n}\n", "in",
			"error: Jenkinsfile: line 7: X is an axis of a matrix, whose cells errata does not read, and stage \"in\" is in that matrix"},
		{"pipeline {\n  environment { X = 'e' }\n  stages { stage('a') { steps { withEnv(['X=1']) { sh 'a' } } } }\n}\n", "a",
			"error: Jenkinsfile: line 3: withEnv sets X for the steps inside it, which errata does not read"},
		{"withEnv([\"Y=1\", \"X=${a}\"]) { }\n", "", "error: Jenkinsfile: line 1: withEnv sets X for the steps inside it, which errata does not read"},
		{"withEnv(['XY=1']) { 'X=1' }\nenv.X = 'a'\n", "", "a@2"},
		{"pipeline {\n  environment { X = 'e' }\n}\n@Library('t') _\n", "", "e@2"},
	} {
		if g := got(t, c.body, Spec{Variable: "X", Stage: c.stage}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// A stage the file does not name, names twice, or may name under a name
// that is not literal is refused. A spec without a stage whose variable
// only stages set is told which.
func TestJenkinsStages(t *testing.T) {
	const two = "pipeline {\n  stages {\n    stage('a') { environment { X = '1' } }\n    stage(\"b\") { environment { X = '2' } }\n    stage('c') { }\n  }\n}\n"
	for _, c := range []struct{ body, stage, want string }{
		{two, "a", "1@3"},
		{two, "b", "2@4"},
		{two, "c", "none: no file sets X: errata read the environment blocks of stage \"c\", the stages around it and the pipeline, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read"},
		{two, "", "none: no file sets X: errata read the pipeline's environment block, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read; the environment of stage \"a\", \"b\" sets it, so name one with stage:"},
		{two, "d", "error: Jenkinsfile: the file names no stage \"d\""},
		{"pipeline {\n  stages {\n    stage('a') { }\n    stage('a') { }\n  }\n}\n", "a", "error: Jenkinsfile: line 4: a second stage is named \"a\", after the one at line 3; Jenkins refuses a pipeline that repeats a stage's name"},
		{"pipeline {\n  stages {\n    stage(\"${n}\") { }\n  }\n}\n", "a", "error: Jenkinsfile: line 3: the file names no stage \"a\", and the name of this stage is not literal, so errata cannot tell whether it is that stage"},
		{"pipeline { }\npipeline { }\n", "", "error: Jenkinsfile: line 2: the file holds a second pipeline block, and Jenkins runs one; errata reads a file of one"},
		{"pipeline { }\n}\n", "", "error: Jenkinsfile: line 2: a } closes no block"},
		{"environment { X = 'top' }\nstage('a') { environment { X = 'scripted' } }\n", "a", "none: no file sets X: errata read the environment blocks of stage \"a\", the stages around it and the pipeline, and every env.X = and def X = in Jenkinsfile; it may come from Jenkins, such as BUILD_NUMBER, from the controller's, a folder's or a node's settings, or from a plugin, which errata cannot read"},
		{"pipeline { environment { X = '1'\n X = '2' } }\n", "", "2@2"},
		{"pipeline { stages { stage('a') { environment { X = '1' }; environment { X = '2' } } } }\n", "a", "2@1"},
		{"pipeline {\n  stages {\n    stage { environment { X = '1' } }\n  }\n}\n", "a", "error: Jenkinsfile: the file names no stage \"a\""},
		{"pipeline {\n  stages {\n    stage('a', b) { environment { X = '1' } }\n  }\n}\n", "a", "error: Jenkinsfile: line 3: the file names no stage \"a\", and the name of this stage is not literal, so errata cannot tell whether it is that stage"},
		{"pipeline {\n  stages {\n" + strings.Repeat("    stage('s') { environment { X = '1' } }\n", 4) + "  }\n}\n", "",
			"none: " + noneX + "; the environment of stage \"s\", \"s\", \"s\", 1 more sets it, so name one with stage:"},
	} {
		if g := got(t, c.body, Spec{Variable: "X", Stage: c.stage}); g != c.want {
			t.Errorf("%q stage %q:\n got %q\nwant %q", c.body, c.stage, g, c.want)
		}
	}
}

// Blocks nest at most core.MaxDepth levels. The planted break: no depth
// test, which read 100000 levels.
func TestJenkinsRefusesDeepBlocks(t *testing.T) {
	deep := func(n int) string {
		return strings.Repeat("a {\n", n) + "env.X = 'v'\n" + strings.Repeat("}\n", n)
	}
	if g := got(t, deep(core.MaxDepth-1), Spec{Variable: "X"}); g != fmt.Sprintf("v@%d", core.MaxDepth) {
		t.Errorf("at the limit: %.200s", g)
	}
	want := fmt.Sprintf("error: Jenkinsfile: line %d: nested more than %d levels deep, the limit; %s", core.MaxDepth, core.MaxDepth, core.OtherWay)
	for _, n := range []int{core.MaxDepth, 100000} {
		if g := got(t, deep(n), Spec{Variable: "X"}); g != want {
			t.Errorf("%d levels: %.300s", n, g)
		}
	}
}

// Build refuses a spec with no variable, a variable no environment block
// accepts, or a stage name past the limit, naming why.
func TestJenkinsBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{File: "Jenkinsfile"}, "jenkins: variable is required"},
		{Spec{File: "Jenkinsfile", Variable: "1X"}, `jenkins: variable "1X" is no name an environment block accepts: write a letter or _, then up to 1023 letters, digits and _`},
		{Spec{File: "Jenkinsfile", Variable: "A-B"}, `jenkins: variable "A-B" is no name an environment block accepts`},
		{Spec{File: "Jenkinsfile", Variable: strings.Repeat("A", 1025)}, `jenkins: variable "AAAA`},
		{Spec{File: "Jenkinsfile", Variable: "X", Stage: strings.Repeat("s", 1025)}, `jenkins: stage "ssss`},
		{Spec{File: "../Jenkinsfile", Variable: "X"}, `jenkins: file "../Jenkinsfile" is outside the repository`},
		{Spec{Variable: "X"}, "jenkins: file is required"},
	} {
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	if _, err := Build(Spec{File: "Jenkinsfile", Variable: strings.Repeat("A", 1024), Stage: strings.Repeat("s", 1024)}); err != nil {
		t.Errorf("at the limits: %v", err)
	}
}

// Place cleans the file and drops max_bytes; the variable and the stage
// stay as written.
func TestJenkinsPlace(t *testing.T) {
	got := Place(Spec{File: "./ci/../Jenkinsfile", Variable: "X", Stage: "a b", MaxBytes: 10})
	if want := (Spec{File: "Jenkinsfile", Variable: "X", Stage: "a b"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A Jenkinsfile that a template engine renders first, such as
// Jenkinsfile.j2, marks a value with {{ }} not literal.
func TestJenkinsTemplate(t *testing.T) {
	body := "env.X = '{{ tag }}'\nenv.X = 'plain'\n"
	if g := got(t, body, Spec{File: "Jenkinsfile.j2", Variable: "X"}); g != "{{ tag }}@1! plain@2" {
		t.Errorf("got %s", g)
	}
}

// A file that ends with a block or a bracket still open is refused, as
// Groovy refuses it, and so is a } that closes a block with a ( or [ still
// open in it, and a ) or ] that closes nothing. The planted break of the
// first: none of these refused, and env.X={ panicked on a value with no
// end. The security review's stray ) took the lexer's count below 0, so
// the / after the line end in ( ) started a slashy string that hid X = 'b'
// and gave a@2.
func TestJenkinsRefusesOpenBlocks(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"env.X={", "error: Jenkinsfile: line 1: a { is not closed: errata looked for its } to the end of the file"},
		{"def X = {\n", "error: Jenkinsfile: line 1: a { is not closed: errata looked for its } to the end of the file"},
		{"pipeline { environment { X = {", "error: Jenkinsfile: line 1: a { is not closed: errata looked for its } to the end of the file"},
		{"pipeline {\n  environment { X = 'a' }\n", "error: Jenkinsfile: line 1: a { is not closed: errata looked for its } to the end of the file"},
		{"pipeline {\n  environment {\n    X = foo(\n    PASSWORD = 'hunter2'\n  }\n}\n", "error: Jenkinsfile: line 5: a } closes a block in which a ( or [ is not closed"},
		{"env.X = foo(\n'a'\n", "error: Jenkinsfile: line 3: a ( or [ is not closed at the end of the file"},
		{"env.X = { 'a' }\n", "{ 'a' }@1!"},
		{strayParen, "error: Jenkinsfile: line 6: a ) closes no ( or ["},
		{"env.X = 'a'\ny = 1 ]\n", "error: Jenkinsfile: line 2: a ] closes no ( or ["},
		{"env.X = \"${ a) }\"\n", "error: Jenkinsfile: line 1: a ) closes no ( or ["},
		{"node {\n  y = (a)\n}\nenv.X = [1]\n", "[1]@4!"},
	} {
		if g := got(t, c.body, Spec{Variable: "X"}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// strayParen is the security review's file with a ) that closes nothing.
const strayParen = `pipeline {
  environment { X = 'a' }
  stages {
    stage('s') {
      environment {
        Y = 1 )
        H = (T
 / 2)
        X = 'b'
        Z = 3 /)
      }
      steps { sh 'echo $X' }
    }
  }
}
`

// Jenkins's EnvVars does not tell names apart by case, so an environment
// block, an env. assignment, a withEnv or a parameter that spells the
// variable in another case makes it unknown rather than read past.
func TestJenkinsCaseOfNames(t *testing.T) {
	for _, c := range []struct{ body, stage, want string }{
		{"pipeline {\n  environment { IMAGE_TAG = 'a' }\n  stages { stage('s') { environment { image_tag = 'b' } } }\n}\n", "s",
			"error: Jenkinsfile: line 3: an environment block sets IMAGE_TAG in another case, and Jenkins does not tell names apart by case; errata does not guess which spelling the stage sees"},
		{"pipeline {\n  environment {\n    IMAGE_TAG = 'a'\n    Image_Tag = 'b'\n  }\n}\n", "",
			"error: Jenkinsfile: line 4: an environment block sets IMAGE_TAG in another case, and Jenkins does not tell names apart by case; errata does not guess which spelling the stage sees"},
		{"pipeline {\n  environment { image_tag = 'b'\n IMAGE_TAG = 'a' }\n}\n", "", "a@3"},
		{"pipeline {\n  environment { IMAGE_TAG = 'a' }\n  stages { stage('s') { steps { withEnv(['image_tag=b']) { sh 'a' } } } }\n}\n", "s",
			"error: Jenkinsfile: line 3: withEnv sets IMAGE_TAG for the steps inside it, which errata does not read"},
		{"pipeline {\n  parameters { string(name: 'image_tag') }\n}\n", "", "error: Jenkinsfile: line 2: IMAGE_TAG is a parameter of the pipeline, which whoever starts a build chooses"},
	} {
		if g := got(t, c.body, Spec{Variable: "IMAGE_TAG", Stage: c.stage}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// A withEnv reads only literal strings: a list in a variable, a call, a
// sum or a string whose name is not literal may set the variable, and so
// does PATH+NAME= for PATH. The security review's witnesses.
func TestJenkinsWithEnvNotLiteral(t *testing.T) {
	const want = "error: Jenkinsfile: line %d: withEnv sets X for the steps inside it, which errata does not read"
	for _, c := range []struct {
		body string
		line int
	}{
		{"def extra = ['X=b']\npipeline {\n  environment { X = 'a' }\n  stages { stage('s') { steps { withEnv(extra) { sh 'a' } } } }\n}\n", 4},
		{"pipeline {\n  environment { X = 'a' }\n  stages { stage('s') { steps { withEnv([\"${'X'}=b\"]) { sh 'a' } } } }\n}\n", 3},
		{"withEnv(vars()) { }\nenv.X = 'a'\n", 1},
		{"withEnv(['X' + '=b']) { }\nenv.X = 'a'\n", 1},
		{"withEnv(['X+bin=/x']) { }\nenv.X = 'a'\n", 1},
		{"withEnv(['X\\u003db']) { }\nenv.X = 'a'\n", 1},
		{"withEnv([\n  'Y=1',\n  vars,\n]) { }\nenv.X = 'a'\n", 3},
		{"withEnv(['Y=1'] + ['X=b']) { }\nenv.X = 'a'\n", 1},
	} {
		spec := Spec{Variable: "X"}
		if strings.Contains(c.body, "stage('s')") {
			spec.Stage = "s"
		}
		if g, w := got(t, c.body, spec), fmt.Sprintf(want, c.line); g != w {
			t.Errorf("%q: got %q, want %q", c.body, g, w)
		}
	}
	for _, body := range []string{
		"withEnv(['Y=1', \"Z=${a}\", 'PATH+Y=/b', 'XY=1']) { }\nenv.X = 'a'\n",
		"withEnv(['Y=1']); x('X=1')\nenv.X = 'a'\n",
	} {
		if g := got(t, body, Spec{Variable: "X"}); g != "a@2" {
			t.Errorf("%q: got %q", body, g)
		}
	}
}

// env['X'], env."X", env.setProperty('X', v), env.putAt('X', v), env.X += v,
// env.X++, ++env.X and env.X = v other than at a statement's start, such as
// env?.X = v and this.env.X = v, are ways to set X errata does not read:
// one that may name X makes it unknown, when no environment block sets
// it. The security review's witnesses.
func TestJenkinsEnvFormsNotRead(t *testing.T) {
	const want = "error: Jenkinsfile: line 3: the env[...], env.\"...\", env.setProperty or env.putAt here may set X; errata does not read it"
	const changes = "error: Jenkinsfile: line 3: %s changes X; errata does not read it"
	const sets = "error: Jenkinsfile: line 3: this assignment to env.X may set X; errata does not read it"
	for _, c := range []struct{ body, want string }{
		{"node {\n  env.X = 'a'\n  env['X'] = 'b'\n}\n", want},
		{"node {\n  env.X = 'a'\n  env[\"x\"] = 'b'\n}\n", want},
		{"node {\n  env.X = 'a'\n  env[name] = 'b'\n}\n", want},
		{"node {\n  env.X = 'a'\n  env.\"X\" = 'b'\n}\n", want},
		{"node {\n  env.X = 'a'\n  env.setProperty('X', 'b')\n}\n", want},
		{"node {\n  env.X = 'a'\n  env.setProperty 'X', 'b'\n}\n", want},
		{"node {\n  env.X = 'a'\n  env.setProperty(\"${n}\", 'b')\n}\n", want},
		{"node {\n  env.X = 'a'\n  env.X += 'b'\n}\n", "error: Jenkinsfile: line 3: env.X += changes X; errata does not read it"},
		// Round 2: the security review's D7 and the correctness review's
		// D-new-2 and N-new-1, each of which gave a@2.
		{"node {\n  env.X = 'a'\n  env.X++\n}\n", fmt.Sprintf(changes, "env.X ++")},
		{"node {\n  env.X = 'a'\n  y = env.x--\n}\n", fmt.Sprintf(changes, "env.X --")},
		{"node {\n  env.X = 'a'\n  ++env.X\n}\n", fmt.Sprintf(changes, "++ env.X")},
		{"node {\n  env.X = 'a'\n  y = --env?.X\n}\n", fmt.Sprintf(changes, "-- env.X")},
		{"node {\n  env.X = 'a'\n  ++this.env.X\n}\n", fmt.Sprintf(changes, "++ env.X")},
		{"node {\n  env.X = 'a'\n  this.env.X -= 'b'\n}\n", fmt.Sprintf(changes, "env.X -=")},
		{"node {\n  env.X = 'a'\n  env?.X = 'b'\n}\n", sets},
		{"node {\n  env.X = 'a'\n  this.env.X = 'b'\n}\n", sets},
		{"node {\n  env.X = 'a'\n  if (c) env.X = 'b'\n}\n", sets},
		{"node {\n  env.X = 'a'\n  y = env.X = 'b'\n}\n", sets},
		{"node {\n  env.X = 'a'\n  env?.'X' = 'b'\n}\n", want},
		{"node {\n  env.X = 'a'\n  env.putAt('X', 'b')\n}\n", want},
		{"node {\n  env.X = 'a'\n  env?.setProperty('X', 'b')\n}\n", want},
		{"node {\n  env.X = 'a'\n  x.env.X++; env.Y++; ++x.env.X; y = env.X == 'b'; z = env?.X\n  env.X\n  ++y\n}\n", "a@2"},
		{"node {\n  env.X = 'a'\n  this.x.X = 1; ++\n  env.X\n}\n", "error: Jenkinsfile: line 4: ++ env.X changes X; errata does not read it"},
		{"node {\n  env.X = 'a'\n  ++\n  env.X\n}\n", "error: Jenkinsfile: line 4: ++ env.X changes X; errata does not read it"},
		{"node {\n  env.X = 'a'\n  y++\n  env.X = 'b'\n}\n", "a@2 b@4"},
		// The delta review's R2-N1 and R2-N2, each of which gave a@2: an
		// operand in parentheses, this?.env, and env in parentheses.
		{"node {\n  env.X = 'a'\n  (env.X)++\n}\n", fmt.Sprintf(changes, "env.X ++")},
		{"node {\n  env.X = 'a'\n  y = ++(env.X)\n}\n", fmt.Sprintf(changes, "++ env.X")},
		{"node {\n  env.X = 'a'\n  --(this.env.X)\n}\n", fmt.Sprintf(changes, "-- env.X")},
		{"node {\n  env.X = 'a'\n  ++(env).X\n}\n", fmt.Sprintf(changes, "++ env.X")},
		{"node {\n  env.X = 'a'\n  this?.env.X++\n}\n", fmt.Sprintf(changes, "env.X ++")},
		{"node {\n  env.X = 'a'\n  this?.env.X = 'b'\n}\n", sets},
		{"node {\n  env.X = 'a'\n  (env).X = 'b'\n}\n", sets},
		{"node {\n  env.X = 'a'\n  (x.env.X)++; ++(x.env.X); (env).Y = 1; (y).X = 1; y = (env.X) + 1\n}\n", "a@2"},
		{"pipeline {\n  environment { X = 'e' }\n  stages { stage('s') { steps { script { env.X++; env?.X = 'b' } } } }\n}\n", "e@2"},
		{"node {\n  env.X = 'a'\n  env['Y'] = 'b'\n  env.setProperty('Z', 'b')\n  env.\"W\" = 1\n}\n", "a@2"},
		{"pipeline {\n  environment { X = 'e' }\n  stages { stage('s') { steps { script { env['X'] = 'b' } } } }\n}\n", "e@2"},
	} {
		if g := got(t, c.body, Spec{Variable: "X"}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// A matrix sets each axis in each of its cells, over the environment
// blocks around the matrix, so a stage inside a matrix whose axis is the
// variable is unknown. A stage outside it reads as before. The
// correctness review's witness.
func TestJenkinsMatrixAxisOverEnvironment(t *testing.T) {
	body := `pipeline {
  environment { X = 'p' }
  stages {
    stage('m') {
      matrix {
        stages { stage('b') { steps { echo X } } }
        axes { axis { name 'X'; values 'a', 'b' } }
      }
    }
    stage('out') { steps { echo X } }
  }
}
`
	// The same matrix with its axes first, as the correctness review wrote it.
	lines := strings.Split(body, "\n")
	lines[5], lines[6] = lines[6], lines[5]
	axesFirst := strings.Join(lines, "\n")
	for _, c := range []struct{ body, stage, want string }{
		{body, "b", "error: Jenkinsfile: line 7: X is an axis of a matrix, whose cells errata does not read, and stage \"b\" is in that matrix"},
		{axesFirst, "b", "error: Jenkinsfile: line 6: X is an axis of a matrix, whose cells errata does not read, and stage \"b\" is in that matrix"},
		{body, "out", "p@2"},
		{body, "", "p@2"},
		{twoMatrices, "b2", "p@2"},
		{twoMatrices, "b1", "error: Jenkinsfile: line 4: X is an axis of a matrix, whose cells errata does not read, and stage \"b1\" is in that matrix"},
		// Round 3: a stage directly in a matrix, which no block inside the
		// matrix holds; the delta review's R2-N3, a survivor.
		{stageInMatrix, "t", "error: Jenkinsfile: line 4: X is an axis of a matrix, whose cells errata does not read, and stage \"t\" is in that matrix"},
	} {
		if g := got(t, c.body, Spec{Variable: "X", Stage: c.stage}); g != c.want {
			t.Errorf("stage %q: got %q, want %q", c.stage, g, c.want)
		}
	}
}

// stageInMatrix has its stage directly in the matrix, not in its stages.
const stageInMatrix = `pipeline {
  environment { X = 'p' }
  stages { stage('o') { matrix {
    axes { axis { name 'X'; values 'a' } }
    stage('t') { steps { echo X } }
  } } }
}
`

// twoMatrices has an axis of X in the first matrix only: a stage in the
// second reads the pipeline's environment.
const twoMatrices = `pipeline {
  environment { X = 'p' }
  stages {
    stage('m1') { matrix { axes { axis { name 'X'; values 'a' } }
      stages { stage('b1') { steps { echo X } } } } }
    stage('m2') { matrix { axes { axis { name 'Y'; values 'a' } }
      stages { stage('b2') { steps { echo X } } } } }
  }
}
`

// A name: 'X' names a parameter only inside a parameters block, and an
// axis only inside an axis block, not after either has closed; an
// assignment with no value before its } sets nothing.
func TestJenkinsDeclaresOnlyInside(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"pipeline {\n  parameters { string(name: 'Y') }\n}\nnode { git(name: 'X'); env.X = 'a' }\n", "a@4"},
		{"pipeline {\n  parameters { string(name: 'X') }\n}\n", "error: Jenkinsfile: line 2: X is a parameter of the pipeline, which whoever starts a build chooses"},
		{"node { git(name: 'X'); env.X = 'a' }\nnode { env.X = }\n", "a@1"},
	} {
		if g := got(t, c.body, Spec{Variable: "X"}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// A shared library loaded by the libraries directive, and this.load,
// this.evaluate or this.library, make the variable unknown like the steps
// they name. The security review's witnesses.
func TestJenkinsLibrariesAndThis(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"pipeline {\n  libraries { lib('shared@main') }\n  stages { stage('s') { steps { script { env.X = 'a' } } } }\n}\n",
			"error: Jenkinsfile: line 2: the shared library of the libraries directive may set X, and errata cannot read it"},
		{"node {\n  this.load 'vars.groovy'\n  env.X = 'a'\n}\n", "error: Jenkinsfile: line 2: the file that load reads may set X, and errata cannot read it"},
		{"this.evaluate(s)\nenv.X = 'a'\n", "error: Jenkinsfile: line 1: the file that evaluate reads may set X, and errata cannot read it"},
		{"props.load(s)\nenv.X = 'a'\n", "a@2"},
	} {
		if g := got(t, c.body, Spec{Variable: "X"}); g != c.want {
			t.Errorf("%q: got %q, want %q", c.body, g, c.want)
		}
	}
}

// A line end inside ( ) or [ ] does not let the / after it start a slashy
// string, so the code after the / is read. Groovy 2.4's lexer skips that
// line end (groovy.g, rule NLS). The security review's witness: the / read
// as a slashy string swallowed X = 'b' and gave the pipeline's a@3.
func TestJenkinsDivisionInBrackets(t *testing.T) {
	for _, open := range []string{"(", "["} {
		closing := map[string]string{"(": ")", "[": "]"}[open]
		body := "pipeline {\n  agent any\n  environment { X = 'a' }\n  stages {\n    stage('s') {\n      environment {\n" +
			"        HALF = " + open + "TOTAL\n                / 2" + closing + "\n        X = 'b' // stage value\n      }\n" +
			"      steps { sh 'echo $X' }\n    }\n  }\n}\n"
		if g := got(t, body, Spec{Variable: "X", Stage: "s"}); g != "b@9" {
			t.Errorf("%s: got %q", open, g)
		}
	}
}
