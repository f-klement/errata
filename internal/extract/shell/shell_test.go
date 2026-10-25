package shell

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// marked writes each value as text@line, with a ! after the text of one
// marked not literal.
func marked(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		if v.NotLiteral {
			v.Text += "!"
		}
		out = append(out, extracttest.Texts([]check.Value{v}))
	}
	return strings.Join(out, " ")
}

// values is what s extracts from body in a file of s's name, marked.
func values(t *testing.T, s Spec, body string) string {
	t.Helper()
	return marked(extracttest.MustRun(t, Build, s, extracttest.Repo(t, map[string]string{s.File: body})))
}

// refusal is the error s gives on body.
func refusal(t *testing.T, s Spec, body string) string {
	t.Helper()
	got, err := extracttest.Run(t, Build, s, extracttest.Repo(t, map[string]string{s.File: body}))
	if err == nil {
		t.Fatalf("no error, values %s", marked(got))
	}
	return err.Error()
}

func spec(name string) Spec { return Spec{File: "deploy.sh", Name: name} }

// Every form of assignment the ticket names, and every default, in file
// order with its line; another variable's are left out.
func TestShellReadsEveryAssignment(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"plain", "X=8080\nY=1\n", "8080@1"},
		{"declarations", "export X=1\nreadonly X=2\ndeclare X=3\nf() { local X=4; }\ntypeset X=5\nX=6 cmd\nexport Y=7 X=8\n",
			"1@1 2@2 3@3 4@4 5@5 6@6 8@7"},
		{"defaults", "echo \"${X:-a}\" ${X-b}\n: ${X:=c} \"${X=d}\"\n", "a@1 b@1 c@2 d@2"},
		{"empty", "X=\n: ${X:-}\n", "@1 @2"},
		{"defaults that are no defaults", "echo ${X:+a} ${X#b} ${X?c} ${#X} ${!X:-d} ${X[0]:-e} ${Y:-f}\n", ""},
		{"inside a function and a subshell", "f() {\n  ( X=1 )\n}\nv=$(X=2 cmd)\nif true; then X=3; fi\n", "1@2 2@4 3@5"},
		{"names without values", "export X\nlocal X\ndeclare -x X\nreadonly X\n", ""},
		{"a default inside a value", "Y=\"${X:-a}/b\"\n", "a@1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := values(t, spec("X"), c.body); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// An assignment inside a here-document, a string or a comment is text, not
// an assignment. A default in the body of a here-document whose delimiter is
// not quoted is an expansion, so it counts.
func TestShellTextIsNoAssignment(t *testing.T) {
	body := "cat <<'EOF'\nX=1 ${X:-2}\nEOF\necho \"X=3\" 'X=4' # X=5\necho X=6 \"${Y:-X=7}\"\ncat <<EOF\n${X:=8}\nX=9\nEOF\ncat <<-\"E\"\n\t${X:-10}\n\tE\n"
	if got, want := values(t, spec("X"), body), "8@7"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Quotes and backslashes are read as the shell reads them; the value is the
// text the variable holds.
func TestShellLiteralValues(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`X='a b'`, "a b@1"},
		{`X="a\"b\$c\\d\qe"`, `a"b$c\d\qe@1`},
		{`X=a\ b\\c`, `a b\c@1`},
		{`X=a"b"'c'd`, "abcd@1"},
		{`X=$'a\tb\x41\101\'\\'`, "a\tbAA'\\@1"},
		{`Y="${X:-'q'}"`, "'q'@1"},
		{`Y=${X:-'q'}`, "q@1"},
		{`Y="${X:-"a b"}"`, "a b@1"},
		{`Y="${X:-a\ b\$}"`, `a\ b$@1`},
		{`X=*.txt`, "*.txt@1"},
		{`X={a,b}`, "{a,b}@1"},
		{`X=a~b`, "a~b@1"},
		{`X=\~`, "~@1"},
		{"X='a\nb'\nX=2", "a\nb@1 2@3"},
		{"X=a\\\nb", "ab@1"},
		{`export X=a\ b`, "a b@1"},
		{`declare -x X=1 -r`, "1@1"},
		{`declare -- X=1`, "1@1"},
		{`export -n X=1`, "1@1"},
		{`declare +i X=010`, "010@1"},
	} {
		if got := values(t, spec("X"), c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// A value the shell computes comes back as the script wrote it, marked not
// literal: an expansion, a tilde, an array, an element, an append, braces in
// a declaration, and any value of a variable an option makes the shell
// compute. The planted break: literal without its tilde test, which returned
// ~/a as its text.
func TestShellNotLiteralValues(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`X=$(cat VERSION)`, "$(cat VERSION)!@1"},
		{"X=`cat VERSION`", "`cat VERSION`!@1"},
		{`X="$BASE/bin"`, `"$BASE/bin"!@1`},
		{`X=${BASE}`, "${BASE}!@1"},
		{`X=$((1+2))`, "$((1+2))!@1"},
		{`X=~/a`, "~/a!@1"},
		{`X=~`, "~!@1"},
		{`X=a:~/b`, "a:~/b!@1"},
		{`X="a:"~/b`, `"a:"~/b!@1`},
		{`X=(a b)`, "(a b)!@1"},
		{`X+=c`, "c!@1"},
		{`X+=`, "!@1"},
		{`X[0]=d`, "d!@1"},
		{`declare -A X=([k]=v)`, "([k]=v)!@1"},
		{`declare -i X=1+2`, "1+2!@1"},
		{`local -l X=ABC`, "ABC!@1"},
		{`declare -n X=Y`, "Y!@1"},
		{`readonly -a X`, ""},
		{`nameref X=Y`, "Y!@1"},
		{"declare -i X\nX=010", "010!@2"},
		{"X=010\ntypeset -u X", "010!@1"},
		// An option after a value returns it as written, not as read: FuzzShell
		// found the byte $'\x01' stands for, which the script does not hold,
		// marked not literal. The planted break: emit returning the text read,
		// or a keep without where the value is written.
		{"X=$'\\x01)'\ndeclare -i X", "$'\\x01)'!@1"},
		{"X=a\\b\ntypeset -u X", "a\\b!@1"},
		{"export X=\"a\"\ndeclare -i X", `"a"!@1`},
		{"Y=ab\nX=$Y\ndeclare -i X", "$Y!@2"},
		{": ${X:-'a'}\ndeclare -i X", "'a'!@1"},
		{"declare \"X=\\$b\"\ndeclare -i X", `"X=\$b"!@1`},
		{`declare "$opts" X=1`, "1!@1"},
		{`declare X={a,b}`, "{a,b}!@1"},
		{`export X=a{1..3}`, "a{1..3}!@1"},
		{`X=${X:-$(date)}`, "${X:-$(date)}!@1 $(date)!@1"},
		{`X=$"translated"`, `$"translated"!@1`},
		{"X=$'\\u00e9'", "$'\\u00e9'!@1"},
		{"X=$'\\z'", "$'\\z'!@1"},
		{`X=$'a\0b'`, `$'a\0b'!@1`},
		{`X="${X:-$'a'}"`, `"${X:-$'a'}"!@1 $'a'!@1`},
		{`X=$(( ${X:-1} ))`, "$(( ${X:-1} ))!@1 1@1"},
		{`X=@(a|b)`, "@(a|b)!@1"},
		{"cat <<E\n${X:-a\\b}\nE", "a\\b!@2"},
	} {
		if got := values(t, spec("X"), c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
	// Another variable's declaration changes nothing.
	if got := values(t, spec("X"), "declare -i Y\nX=010"); got != "010@2" {
		t.Errorf("got %q", got)
	}
}

// Every extractor of the kind may return a not literal value, which is
// what lets a rule list it in skip_not_literal.
func TestShellMayReturnNotLiteral(t *testing.T) {
	e, err := Build(spec("X"))
	if err != nil {
		t.Fatal(err)
	}
	if !check.MayReturnNotLiteral(e) {
		t.Error("a shell extractor declares that it returns literal values only")
	}
}

// The dialect is chosen by language:, else the shebang, else the name, else
// bash; a script that does not parse in it is an error naming the dialect
// and why it was chosen.
func TestShellChoosesTheDialect(t *testing.T) {
	array := "X=(a b)\n"
	for _, c := range []struct {
		file, language, body, want string
	}{
		{"a.sh", "", array, "(a b)!@1"},
		{"a.sh", "", "#!/bin/bash\n" + array, "(a b)!@2"},
		{"a.sh", "", "#!/usr/bin/env bash\n" + array, "(a b)!@2"},
		{"a.sh", "", "#!/usr/bin/env -S LC_ALL=C bash -e\n" + array, "(a b)!@2"},
		{"a.sh", "bash", "#!/bin/sh\n" + array, "(a b)!@2"},
		{"a.bash", "", array, "(a b)!@1"},
		{"a.ksh", "", array, "(a b)!@1"},
		{"a", "", "#!/bin/mksh\n" + array, "(a b)!@2"},
		{"a.sh", "", "#!/bin/sh -e\nX=1\n", "1@2"},
		{"a.sh", "posix", "X=$'a'\n", "$'a'!@1"},
		{"a.sh", "posix", "X=a$\"b\"\n", "a$\"b\"!@1"},
		{"a.sh", "posix", "X=a\\$'b'\n", "a$b@1"},
		{"a.sh", "posix", "export X=1 Y=2 X=a\\ b\nreadonly X=3\nlocal X=~/a\nexport -p\nexport \"$o\" X=4\n", "1@1 a b@1 3@2 ~/a!@3 4!@5"},
		{"a.sh", "posix", "export X=$(a)\nexport X\n", "$(a)!@1"},
		{"a.sh", "mksh", "X=$'a'\n", "$'a'!@1"},
		{"a.sh", "mksh", "X=\"${X:-'a'}\"\n", "\"${X:-'a'}\"!@1 'a'!@1"},
		{"a.sh", "posix", "export X={a,b}\n", "{a,b}@1"},
	} {
		s := Spec{File: c.file, Name: "X", Language: c.language}
		if got := values(t, s, c.body); got != c.want {
			t.Errorf("%s %q %q: got %q, want %q", c.file, c.language, c.body, got, c.want)
		}
	}
	for _, c := range []struct {
		file, language, body, want string
	}{
		{"a.sh", "", "#!/bin/sh\n" + array, "a.sh: line 2: arrays parses only in bash and mksh and zsh, and the script is read as posix, which its shebang names"},
		{"a.bash", "posix", array, "read as posix, which language: names"},
		{"a.sh", "", "X=$(\n", "a.sh: line 1: the script does not parse as bash, since no shebang names a shell: "},
		{"a.mksh", "", "X=1\nY=$(\n", "line 2: the script does not parse as mksh, by its name"},
		{"a.sh", "", "#!/usr/bin/env python3\nX=1\n", `a.sh: line 1: the shebang names "python3", which is no shell errata reads; set language: to bash, posix or mksh`},
		{"a.sh", "", "#!/bin/zsh\nX=1\n", `the shebang names "zsh"`},
		{"a.sh", "", "#!\nX=1\n", `the shebang names ""`},
	} {
		s := Spec{File: c.file, Name: "X", Language: c.language}
		if got := refusal(t, s, c.body); !strings.Contains(got, c.want) {
			t.Errorf("%s %q %q: error %q, want %q", c.file, c.language, c.body, got, c.want)
		}
	}
}

// A spec without a file or a name, with a name no shell variable has, or
// with a language errata does not read, does not load.
func TestShellBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Name: "X"}, "shell: file is required"},
		{Spec{File: "a.sh"}, "shell: name is required"},
		{Spec{File: "a.sh", Name: "1X"}, `shell: name "1X" is no shell variable name`},
		{Spec{File: "a.sh", Name: "X-Y"}, `shell: name "X-Y" is no shell variable name`},
		{Spec{File: "a.sh", Name: "X Y"}, `shell: name "X Y" is no shell variable name`},
		{Spec{File: "a.sh", Name: "X", Language: "zsh"}, `shell: language "zsh" is not one errata reads; write bash, posix or mksh`},
		{Spec{File: "/etc/profile", Name: "X"}, "outside the repository"},
	} {
		_, err := Build(c.s)
		extracttest.WantError(t, err, c.want)
	}
	for _, name := range []string{"X", "_", "a_1", "PATH", "_9"} {
		if _, err := Build(Spec{File: "a.sh", Name: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Place cleans the file and drops max_bytes, and keeps the name and the
// language.
func TestShellPlace(t *testing.T) {
	got := Place(Spec{File: "a/../b/./c.sh", Name: "X", Language: "posix", MaxBytes: 10})
	if want := (Spec{File: "b/c.sh", Name: "X", Language: "posix"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A script that nests past the guard's limit is refused, with the line the
// parser had read to, before it can grow the stack to Go's limit, and one
// that nests a little less reads. The planted break: Read without its
// check, under which the shape of 1 MiB ended the test binary on a stack
// overflow.
func TestShellRefusesDeepNesting(t *testing.T) {
	levels := maxFrames / framesPerLevel
	// Deep enough to be read in more than checkEvery reads.
	deep := "X=$((" + strings.Repeat("(", 100*levels) + "1" + strings.Repeat(")", 100*levels) + "))\n"
	got := refusal(t, spec("X"), deep)
	if want := "deploy.sh: line 1: the script nests more deeply than errata reads: the parser's stack passed 20000 calls, the limit, which about 344 levels of parentheses in (( )) reach"; !strings.HasPrefix(got, want) {
		t.Errorf("error %q, want %q", got, want)
	}
	// $( ) takes fewer calls a level, so as many levels of it read.
	shallow := "X=" + strings.Repeat("$(", 2*levels) + strings.Repeat(")", 2*levels) + "\n"
	if got := values(t, spec("X"), shallow); !strings.HasPrefix(got, "$($(") {
		t.Errorf("got %.40q", got)
	}
}

// A pipeline, a list of && and a sum are trees one level deeper for each
// command or term, which the parser builds in a loop; the walk refuses one
// past maxTreeDepth, before syntax.Walk recurses past Go's stack limit,
// and reads one under it. The planted break: visit without its depth test,
// under which 32 MiB of +1 ended the process on a stack overflow.
func TestShellRefusesADeepTree(t *testing.T) {
	for _, c := range []struct{ name, unit string }{
		{"pipeline", " | a"},
		{"list", " && a"},
		{"sum", "+1"},
	} {
		// The statements after the refused one stay unread; the parser's
		// iterator is not stopped early, which it does not allow.
		body := func(n int) string { return "X=$((1))\na" + strings.Repeat(c.unit, n) + "\nX=2\nX=3\n" }
		if c.name == "sum" {
			body = func(n int) string { return "X=$((1" + strings.Repeat(c.unit, n) + "))\nX=2\n" }
		}
		got := refusal(t, spec("X"), body(maxTreeDepth))
		if want := "the script's syntax is more than 20000 levels deep, the limit"; !strings.Contains(got, want) {
			t.Errorf("%s: error %q, want %q", c.name, got, want)
		}
		// Each command of a pipeline or a list is two levels.
		values(t, spec("X"), body(maxTreeDepth/4))
	}
}

// The parser's iterator yields a statement together with the error that
// ended it, then the error again, so a read that stops at the first is
// refused, not ended by Go's panic on a loop that went on. The planted
// break: read returning from inside the loop, which FuzzShell found
// panicking on this input.
func TestShellParseErrorAfterAStatement(t *testing.T) {
	got := refusal(t, spec("A"), "(0)&0\xbc")
	if want := "deploy.sh: line 1: the script does not parse as bash"; !strings.HasPrefix(got, want) {
		t.Errorf("error %q, want %q", got, want)
	}
}
