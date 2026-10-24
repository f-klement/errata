package shell

import (
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"

	"github.com/f-klement/errata/internal/extract/core"
)

// xtrace expands PS4 before each command, and PS4 may assign (the
// correctness review's A1): bash and dash give the second X 5 here. So set
// -x is off the list, and a shebang that passes the shell -x, or any option
// set may not take, ends resolution from the first line, whatever the
// language. The planted breaks: x back in setOptions, the shebang's test
// left out of read, and its program not passed over, which made the rows
// 1, or made env bash not literal.
func TestShellXtraceEndsResolution(t *testing.T) {
	for _, c := range []struct{ lang, body, want string }{
		{"", "Y=1\nPS4='+$((Y=5)) '\nset -x\nX=$Y\nX=$Y\n", "$Y!@4 $Y!@5"},
		{"posix", "Y=1\nPS4='+${Y:=0}$((Y=5)) '\nset -x\nX=$Y\nX=$Y\n", "$Y!@4 $Y!@5"},
		{"", "#!/bin/bash -x\nY=1\nPS4='+$((Y=5)) '\nX=$Y\nX=$Y\n", "$Y!@4 $Y!@5"},
		{"", "#!/bin/sh -ex\nY=1\nX=$Y\n", "$Y!@3"},
		{"", "#!/usr/bin/env bash -x\nY=1\nX=$Y\n", "$Y!@3"},
		{"", "#!/bin/bash -o xtrace\nY=1\nX=$Y\n", "$Y!@3"},
		{"", "#!/bin/bash -k\nY=1\nX=$Y\n", "$Y!@3"},
		{"bash", "#!/bin/bash -x\nY=1\nX=$Y\n", "$Y!@3"},
		{"", "#!/bin/bash\nY=1\nX=$Y\n", "1@3"},
		{"", "#!/bin/sh -eu\nY=1\nX=$Y\n", "1@3"},
		{"", "#!/bin/bash +e -o pipefail\nY=1\nX=$Y\n", "1@3"},
		{"", "#!/usr/bin/env bash\nY=1\nX=$Y\n", "1@3"},
		{"", "#!/usr/bin/env sh -e\nY=1\nX=$Y\n", "1@3"},
	} {
		if got := values(t, Spec{File: "deploy.sh", Name: "X", Language: c.lang}, c.body); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.body, got, c.want)
		}
	}
}

// In posix the parser reads export, readonly and local as commands, and a
// name shares its first part with the value: when that part is not literal
// as a whole, for a tilde after a colon, a trailing backslash or a $ before
// a quote, the value still comes back as written, not literal (the
// correctness review's A3: it was lost). A name the shell reads after quote
// removal still comes from the text before the first part that is not
// literal. The planted breaks: splitAssign without the first part's name,
// which lost the first four rows' values, and that without its test of an
// = already found, which lost the last.
func TestShellPOSIXDeclarationKeepsItsValue(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"V", "export V=a:~/b\n", "a:~/b!@1"},
		{"V", "readonly V=a:~/b\n", "a:~/b!@1"},
		{"V", "local V=x:~\n", "x:~!@1"},
		{"V", `export V=a\`, `a\!@1`},
		{"V", "export V=a$'x'\n", "a$'x'!@1"},
		{"V", "export V={a,b}%:~/a\n", "{a,b}%:~/a!@1"},
		{"ab", "export a\\b=$x\n", "a\\b=$x!@1"},
	} {
		if got := values(t, Spec{File: "deploy.sh", Name: c.name, Language: "posix"}, c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// The pending here-documents after each statement, when the parser leaves
// an empty body nil (the security review's S1): they are dropped as empty
// once the walk reaches a statement past the newline that ends their line,
// after blanks or a comment, and kept while the line goes on, or goes on
// past a backslash. Every redirect is released once read. The planted
// breaks: lineEnds returning false, which kept every row's document, and
// flushDocs not releasing the redirects it drops.
func TestShellEmptyDocumentsAreDropped(t *testing.T) {
	for _, c := range []struct {
		body string
		want []int
	}{
		{"cat <<A\nA\nY=1\n", []int{1, 0}},
		{"cat <<'A'\nA\nY=1\n", []int{1, 0}},
		{"cat <<A \nA\nY=1\n", []int{1, 0}},
		{"cat <<A # c\nA\nY=1\n", []int{1, 0}},
		{"cat <<A; Y=1\nA\nZ=2\n", []int{1, 1, 0}},
		{"cat <<A; \\\nY=1\nA\nZ=2\n", []int{1, 1, 0}},
		{"cat <<A; cat <<B; Y=1\nA\nB\nZ=2\n", []int{1, 2, 2, 0}},
		{"cat <<A; Y=1\nx\nA\nZ=2\n", []int{1, 0, 0}},
	} {
		data := []byte(c.body)
		w := newWalker("X", syntax.LangBash, data, core.NewMeter(len(data)), len(data))
		var got []int
		var redirs []*syntax.Redirect
		for stmt, err := range syntax.NewParser().StmtsSeq(strings.NewReader(c.body)) {
			if err != nil {
				t.Fatal(err)
			}
			syntax.Walk(stmt, func(n syntax.Node) bool {
				if r, ok := n.(*syntax.Redirect); ok {
					redirs = append(redirs, r)
				}
				return true
			})
			w.walk(stmt)
			got = append(got, len(w.pending))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: pending %v, want %v", c.body, got, c.want)
		}
		for _, r := range redirs {
			if len(r.Word.Parts) != 0 || r.Hdoc != nil && len(r.Hdoc.Parts) != 0 {
				t.Errorf("%q: the redirect at %s keeps its word or body", c.body, r.Pos())
			}
		}
	}
}

// An empty here-document, or one pending on a long line, holds no
// statement but its own (the security review's S1): before, the walk
// released nothing while a document was pending, and an empty one stayed
// pending to the end, so 16 statements of 512 KiB after cat <<A, A took
// 930 MiB against 100 MiB. Read at 1 MiB, eight statements of 128 KiB
// after each head may peak at 1.5 times eight with no here-document. The
// planted break, the old walk: release returning while a document is
// pending, and lineEnds false, which took 107 MiB against 18 MiB (5.9
// times) on every row.
func TestShellEmptyDocumentHoldsNoStatements(t *testing.T) {
	// The race detector multiplies its time twentyfold and inflates the
	// memory it measures, so make slow runs it without race.
	slowOnly(t)
	size := 1 << 20
	s := Spec{File: "deploy.sh", Name: "X", MaxBytes: int64(size)}
	stmt := "Y=" + strings.Repeat("$a", (size/8-64)/2)
	stmts := make([]string, 8)
	for i := range stmts {
		stmts[i] = stmt
	}
	lines := strings.Join(stmts, "\n") + "\nX=1\n"
	none := peakHeap(t, s, lines)
	for _, body := range []string{
		"cat <<A\nA\n" + lines,
		"cat <<'A'\nA\n" + lines,
		"cat <<A; " + strings.Join(stmts, "; ") + "; X=1\nx\nA\n",
	} {
		got := peakHeap(t, s, body)
		t.Logf("%.9q: %d MiB, %d MiB with no here-document", body, got>>20, none>>20)
		if float64(got) > 1.5*float64(none) {
			t.Errorf("%.9q: peaks at %d MiB, %d MiB with no here-document: over 1.5 times", body, got>>20, none>>20)
		}
	}
}

// A pending here-document keeps its delimiter, which the parser reads the
// body by once the line ends: with it cleared, the body of a quoted
// delimiter was read as if unquoted, which gave 1 here. The planted break:
// release clearing a pending redirect.
func TestShellPendingDocumentKeepsItsDelimiter(t *testing.T) {
	for _, body := range []string{"cat <<'A'; echo\n${X:-1}\nA\nX=2\n", "cat <<-'A'; echo\n\t${X:-1}\n\tA\nX=2\n"} {
		if got, want := values(t, spec("X"), body), "2@4"; got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
}
