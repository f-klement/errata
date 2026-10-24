package shell

import (
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A here-document's body is read once the parser has read it, also when
// another statement follows the redirect on its line, before ; or &. The
// planted break: neither walk nor read flushing the pending documents,
// which lost every default but the last statement's on a line.
func TestShellHeredocAfterAStatement(t *testing.T) {
	for _, c := range []struct{ lang, body, want string }{
		{"", "cat <<EOF\n${X:-1}\nEOF\n", "1@2"},
		{"", "cat <<EOF; echo\n${X:-1}\nEOF\n", "1@2"},
		{"", "cat <<EOF & echo\n${X:-1}\nEOF\n", "1@2"},
		{"", "cat <<A; cat <<B\n${X:-1}\nA\n${X:-2}\nB\n", "1@2 2@4"},
		{"", ": ${X:-a}; cat <<EOF; : ${X:-c}\n${X:-b}\nEOF\n", "a@1 c@1 b@2"},
		{"posix", "cat <<EOF; echo\n${X:-1}\nEOF\n", "1@2"},
		{"", "cat <<EOF;\n${X:-1}\nEOF", "1@2"},
		{"", "cat <<-E; echo\n\t${X:-2}\n\tE\n", "2@2"},
	} {
		if got := values(t, Spec{File: "deploy.sh", Name: "X", Language: c.lang}, c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// Values come back in file order, whatever order the walk reads them in: a
// command before its redirects, a list's left side and its here-documents
// before its right side. The planted break: emit without its sort.
func TestShellValuesInFileOrder(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"cat <<EOF && X=1\n${X:-h}\nEOF\n", "1@1 h@2"},
		{"> ${X:-a} \\\n X=1 cmd\n", "a@1 1@2"},
		{"cat <<A >${X:-a}\n${X:-b}\nA\n", "a@1 b@2"},
	} {
		if got := values(t, spec("X"), c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// Inside the pattern or replacement of ${Y#p}, ${Y%p} and ${Y/p/r}, bash
// and dash read quotes afresh, so a nested default with quotes is not
// literal; plain text stays literal. A default in a default keeps its
// quotes. The planted break: quoting without its pattern case, which gave
// 'a' as literal.
func TestShellDefaultInAPattern(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`: "${Y#${X:='a'}}"`, "'a'!@1"},
		{`: "${Y%${X:='c'}}"`, "'c'!@1"},
		{`: "${Y##${X:='a'}}"`, "'a'!@1"},
		{`: "${Y//${X:='b'}}"`, "'b'!@1"},
		{`: "${Y/z/${X:='e'}}"`, "'e'!@1"},
		{`: "${Y#${X:=a}}"`, "a@1"},
		{`: "${Y:-${X:='b'}}"`, "'b'@1"},
	} {
		if got := values(t, spec("X"), c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// In a template file, what the engine renders is not literal, as for ini,
// properties and envfile. The planted break: NotLiteral without
// core.TemplateNotLiteral.
func TestShellTemplate(t *testing.T) {
	s := Spec{File: "deploy.sh.j2", Name: "X"}
	if got, want := values(t, s, "X=\"{{ version }}\"\nX=1\n"), "{{ version }}!@1 1@2"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A quoted argument of a declaration assigns, as the shell splits it at its
// = after removing the quotes; it is literal when all of it is. The planted
// break: declWord without splitAssign, which read "X=1" as an option it
// cannot read.
func TestShellQuotedDeclarationArgument(t *testing.T) {
	for _, c := range []struct{ lang, body, want string }{
		{"", `export "X=1"`, "1@1"},
		{"", `declare "X=2"`, "2@1"},
		{"", `declare 'X'=3`, "3@1"},
		{"", `export "X=$PATH:/x"`, `"X=$PATH:/x"!@1`},
		{"", `export "X=1" Y=2 X=3`, "1@1 3@1"},
		{"", `export "Y=1" X=2`, "2@1"},
		{"", `declare "-i" X=010`, "010!@1"},
		{"", `export "X"=~/a`, `"X"=~/a!@1`},
		{"posix", `export "X=1"`, "1@1"},
		{"posix", `export X"=4"`, "4@1"},
		{"posix", `readonly "X=$(a)"`, `"X=$(a)"!@1`},
	} {
		if got := values(t, Spec{File: "deploy.sh", Name: "X", Language: c.lang}, c.body); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.body, got, c.want)
		}
	}
}

// A declaration in the value of another leaves the outer one's marks: $o
// may be -i, so 5 is not literal. The planted break: decl clearing the marks,
// as it did, which gave 5 as literal.
func TestShellNestedDeclarationKeepsMarks(t *testing.T) {
	if got, want := values(t, spec("X"), `declare "$o" Y=$(declare Z=1) X=5`), "5!@1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A reference $Y or ${Y} resolves to the value the script last gave Y,
// where that leaves no doubt, and else is not literal. The planted break:
// walk without its test of quiet, which resolved a reference in a loop.
func TestShellResolvesEarlierAssignments(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"plain", "BASE=/opt\nX=$BASE/bin\n", "/opt/bin@2"},
		{"braces and quotes", "V=1.4\nexport X=\"v${V}\"\n", "v1.4@2"},
		{"a chain", "A=a\nB=${A}b\nX=$B$A\n", "aba@3"},
		{"in a default", "P=8080\nexec srv --port \"${X:-$P}\"\n", "8080@2"},
		{"in a condition", "Y=1\nif true; then X=$Y; fi\n", "$Y!@2"},
		{"in the same assignment", "Y=1 X=$Y\n", "1@1"},
		{"the last value", "Y=1\nY=2\nX=$Y\n", "2@3"},
		{"never set", "X=$HOME/bin\n", "$HOME/bin!@1"},
		{"set later", "X=$Y\nY=1\n", "$Y!@1"},
		{"declare expands first", "Y=1\ndeclare Y=2 X=$Y\n", "1@2"},
		{"posix export expands first", "#!/bin/sh\nY=1\nexport Y=2 X=$Y\n", "1@3"},
		{"before a command", "Y=1 cmd\nX=$Y\n", "$Y!@2"},
		{"in a condition before", "Y=1\nif c; then Y=2; fi\nX=$Y\n", "$Y!@3"},
		{"in a loop", "Y=1\nwhile c; do X=$Y; Y=2; done\n", "$Y!@2"},
		{"in a function", "Y=1\nf() { X=$Y; }\n", "$Y!@2"},
		{"a function changes it", "f() { Y=2; }\nY=1\nf\nX=$Y\n", "$Y!@4"},
		{"in a pipeline", "Y=1\nX=$Y | cat\n", "$Y!@2"},
		{"in the background", "Y=1\nX=$Y &\n", "$Y!@2"},
		{"read", "Y=1\nread Y\nX=$Y\n", "$Y!@3"},
		{"unset", "Y=1\nunset Y\nX=$Y\n", "$Y!@3"},
		{"for", "Y=1\nfor Y in a b; do :; done\nX=$Y\n", "$Y!@3"},
		{"arithmetic", "Y=1\n(( Y++ ))\nX=$Y\n", "$Y!@3"},
		{"a default assigns", "Y=1\n: ${Y:=2}\nX=$Y\n", "$Y!@3"},
		{"eval", "Y=1\neval \"$s\"\nX=$Y\n", "$Y!@3"},
		{"source", "Y=1\n. ./lib.sh\nX=$Y\n", "$Y!@3"},
		{"a nameref", "Y=1\ndeclare -n R=Y\nX=$Y\n", "$Y!@3"},
		{"an option that changes it", "Y=010\ndeclare -i Y\nX=$Y\n", "$Y!@3"},
		{"not literal itself", "Y=$(date)\nX=$Y\n", "$Y!@2"},
		{"local", "f() { local Y=1; X=$Y; }\n", "$Y!@1"},
		{"dynamic", "RANDOM=1\nX=$RANDOM\n", "$RANDOM!@2"},
		{"a here-document still to read", "Y=1\ncat <<E; X=$Y\n${Y:=2}\nE\n", "$Y!@2"},
		{"positional", "X=$1\n", "$1!@1"},
		{"after a here-document", "cat <<E; echo\nbody\nE\nY=1\nX=$Y\n", "$Y!@5"},
		{"a modifier", "Y=1\nX=${Y:-2}\n", "${Y:-2}!@2"},
	} {
		if got := values(t, spec("X"), c.body); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// The values the walk keeps for references hold at most MaxMaxBytes in all,
// and each copy a reference makes is charged to the work meter, so a chain
// that doubles a value is refused, not grown without end.
func TestShellResolutionIsCharged(t *testing.T) {
	body := "A=" + strings.Repeat("a", 1024) + "\n" + strings.Repeat("A=$A$A\n", 40) + "X=$A\n"
	got := refusal(t, spec("X"), body)
	if want := "deploy.sh: reading the file and matching the spec take more than 128 units of work"; !strings.HasPrefix(got, want) {
		t.Errorf("error %q, want %q", got, want)
	}
}

// A declaration's argument of many braces is not literal without
// SplitBraces, whose memory is the square of the braces. The planted break:
// bracesExpand calling SplitBraces, under which 8000 braces took over 1 GB.
func TestShellManyBracesInADeclaration(t *testing.T) {
	body := "declare X=" + strings.Repeat("{", 8000) + "a" + strings.Repeat("}", 8000) + "\n"
	x := entryExtractor(t, spec("X"))
	var got []string
	allocated := extracttest.Allocated(func() {
		vs, err := x.FromData([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			got = append(got, v.Text[:2])
		}
	})
	if len(got) != 1 || got[0] != "{{" {
		t.Errorf("values %q", got)
	}
	if allocated > 64<<20 {
		t.Errorf("allocated %d MiB", allocated>>20)
	}
}

// A statement longer than an eighth of max_bytes is refused, naming the
// limit, since its syntax is held whole while it is read; many short
// statements of the same size read. The planted break: Read without the
// statement test.
func TestShellStatementLimit(t *testing.T) {
	s := Spec{File: "deploy.sh", Name: "X", MaxBytes: 80 << 10}
	long := "X=1\necho" + strings.Repeat(" a", 12<<10) + "\nX=2\n"
	got := refusal(t, s, long)
	if want := "deploy.sh: line 2: a statement, with the comments and blank lines before it, is more than 10240 bytes long, the limit, an eighth of max_bytes"; !strings.HasPrefix(got, want) {
		t.Errorf("error %q, want %q", got, want)
	}
	if got := values(t, s, strings.Repeat("echo a a a a a a a a\n", 3<<10)+"X=1\n"); got != "1@3073" {
		t.Errorf("got %q", got)
	}
}

// env's options that take a word skip it, and busybox runs the applet its
// first argument names.
func TestShellShebangOptions(t *testing.T) {
	for _, body := range []string{"#!/usr/bin/env -u FOO bash\nX=(a)\n", "#!/usr/bin/env -C /tmp -P /bin bash\nX=(a)\n"} {
		if got := values(t, spec("X"), body); got != "(a)!@2" {
			t.Errorf("%q: got %q", body, got)
		}
	}
	if got := refusal(t, spec("X"), "#!/bin/busybox sh\nX=(a)\n"); !strings.Contains(got, "read as posix, which its shebang names") {
		t.Errorf("busybox: %q", got)
	}
}

// The walk's refusal of a deep tree takes its line from the statement, so
// it never calls Pos on the node at the limit, which for a sum walks down
// the rest of the sum, one call a term: with a stack of 32 MiB, a sum of
// 1.5 million terms (3 MiB, under the statement limit) overflowed it there. It runs in a process of its own, which
// a stack overflow ends.
func TestShellDeepTreeLineTakesNoStack(t *testing.T) {
	slowOnly(t)
	if os.Getenv("ERRATA_SHELL_SPINE") != "" {
		debug.SetMaxStack(32 << 20)
		x := entryExtractor(t, spec("X"))
		_, err := x.FromData([]byte("X=$((1" + strings.Repeat("+1", 3<<19) + "))\n"))
		os.Stdout.WriteString("ERR " + err.Error() + "\n")
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestShellDeepTreeLineTakesNoStack$")
	cmd.Env = append(os.Environ(), "ERRATA_SHELL_SPINE=1")
	out, err := cmd.CombinedOutput()
	if want := "ERR deploy.sh: line 1: the script's syntax is more than 20000 levels deep"; err != nil || !strings.Contains(string(out), want) {
		t.Errorf("%v: %s, want %q", err, core.Clip(string(out)), want)
	}
}
