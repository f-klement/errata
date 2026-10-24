package shell

import (
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// between runs the script Y=1, then between, then X=$Y, in lang, and
// reports X's value as values marks it.
func between(t *testing.T, lang, between string) (got, line string) {
	t.Helper()
	body := "Y=1\n" + between + "\nX=$Y\n"
	n := strings.Count(body, "\n")
	return values(t, Spec{File: "deploy.sh", Name: "X", Language: lang}, body), strconv.Itoa(n)
}

// The correctness review's R1 to R7: each script changes Y in a way a list
// of the constructs that change a variable missed, and each resolved to a
// wrong literal. Under the allowlist each is not literal. The planted
// breaks: keeps returning true, which gave 2, 1+1, 1 and the like as
// literal; quiet returning true, which gave R1's 2.
func TestShellAllowlistEndsResolution(t *testing.T) {
	for _, c := range []struct{ name, lang, between string }{
		{"R1 a declaration nested in a value", "", "declare Y=2 Z=$(declare W=1) X=$Y"},
		{"R2 declare -i", "", "declare -i Y\nY=1+1"},
		{"R2 typeset -u", "", "typeset -u Y\nY=abc"},
		{"R2 mksh typeset -i", "mksh", "typeset -i Y\nY=1+1"},
		{"R3 unset a quoted name", "", `unset "Y"`},
		{"R3 unset a name in a variable", "", "V=Y\nunset $V"},
		{"R3 read a quoted name", "", `read "Y" <<< z`},
		{"R3 let a quoted assignment", "", `let "Y=3"`},
		{"R3 arithmetic on a quoted name", "", `(( "Y=3" ))`},
		{"R3 printf -v in a function", "", "f() { printf -v \"$1\" %s z; }\nf Y"},
		{"R3 declare -g in a function", "", "f() { declare -g \"$1\"=7; }\nf Y"},
		{"R5 a quoted command name", "", `"export" Y=2`},
		{"R5 an escaped command name", "", `\export Y=2`},
		{"R5 a command name in a variable", "", "e=export\n$e Y=2"},
		{"R6 arithmetic in [[ ]]", "", "[[ 1 -eq Y=3 ]]"},
		{"R6 an assignment to an integer", "", "declare -i Z\nZ=Y=3"},
		{"R7 mksh set -A", "mksh", "set -A Y z"},
		{"R7 mksh nameref", "mksh", "nameref R=Y\nR=5"},
	} {
		got, line := between(t, c.lang, c.between)
		if c.name == "R1 a declaration nested in a value" {
			// X is assigned twice: in the declaration, then after.
			if want := "$Y!@2 $Y!@3"; got != want {
				t.Errorf("%s: got %q, want %q", c.name, got, want)
			}
			continue
		}
		if want := "$Y!@" + line; got != want {
			t.Errorf("%s: got %q, want %q", c.name, got, want)
		}
	}
}

// Commands that set a variable they are not given (R4) are not on the
// allowlist, and the variables they set are the shell's, so neither the
// command nor the name lets a reference resolve. The planted break: owned
// without its map, with read on the list, which gave 1.
func TestShellImplicitNamesAreNotLiteral(t *testing.T) {
	for _, body := range []string{
		"REPLY=1\nread <<< z\nX=$REPLY\n",
		"OPTIND=1\ngetopts a o -a\nX=$OPTIND\n",
		"MAPFILE=1\nmapfile < /dev/null\nX=$MAPFILE\n",
	} {
		if got := values(t, spec("X"), body); !strings.HasSuffix(got, "!@3") {
			t.Errorf("%q: got %q", body, got)
		}
	}
}

// What the allowlist keeps: a reference resolves after comments, plain
// assignments, export and readonly with names and values, and the commands
// it names with the arguments it allows. The planted breaks: keptCommands
// without echo, keepsSet refusing -o, and nameArgument refusing NAME alone,
// each of which made a row not literal.
func TestShellAllowlistKeepsResolution(t *testing.T) {
	for _, c := range []struct{ lang, between string }{
		{"", "# a comment"},
		{"", "echo \"$Y\" 'a' b >&2"},
		{"", "printf 'done\\n'"},
		{"", "printf a \"$Y\""},
		{"", "true\nfalse\n:"},
		{"", "cd /tmp\nmkdir -p a 2>/dev/null"},
		{"", "set -e"},
		{"", "set -euo pipefail"},
		{"", "set +e -u -o errexit -o nounset"},
		{"", "export Z=2 W\nreadonly V=3"},
		{"", "export Y"},
		{"", "Z=2 W=$Z"},
		{"", "echo ${Z:-$Y} ${Z-a} ${Z:+b} ${Z+c} ${Z:-} $1 \"$@\""},
		{"posix", "export Z=2 W\nreadonly V=3"},
		{"posix", "export Y"},
		{"mksh", "export Z=2\nreadonly V"},
	} {
		got, line := between(t, c.lang, c.between)
		if want := "1@" + line; got != want {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.between, got, want)
		}
	}
}

// What the allowlist refuses: every other command, a kept command with an
// argument that may change a variable, any expansion that is not a
// parameter with a default, a redirect that names a descriptor by a
// variable or stands on an assignment, and anything not a simple command.
// Each row is a branch of quiet or keeps; the planted break of each is the
// branch removed, which made its row 1.
func TestShellAllowlistRefuses(t *testing.T) {
	for _, c := range []struct{ lang, between string }{
		{"", "cmd"},
		{"", "printf -v Y z"},
		{"", "printf '-v' Y z"},
		{"", `printf "$f" z`},
		{"", `printf \-v Y z`},
		{"", "printf"},
		{"", "printf '%s\\n' \"$Y\""},
		{"", "printf %s a"},
		{"", "printf 'a%n' Y"},
		{"", "printf \"%s%n\" abc Y"},
		{"", "printf %%"},
		{"", "set -x"},
		{"", "set +x"},
		{"", "set -eux"},
		{"", "set -o xtrace"},
		{"", "set +o xtrace"},
		{"", "set -a"},
		{"", "set -k"},
		{"", "set -o allexport"},
		{"", "set -o"},
		{"", "set -"},
		{"", "set --"},
		{"", "set a b"},
		{"", `set "$o"`},
		{"", "set -e $o"},
		{"", "Y=2 echo"},
		{"", "echo $(true)"},
		{"", "echo `true`"},
		{"", "echo $((1))"},
		{"", "echo ${Z:=1}"},
		{"", "echo ${Z?}"},
		{"", "echo ${Z[0]}"},
		{"", "echo ${#Z}"},
		{"", "echo ${Z:1}"},
		{"", "echo ${Z/a/b}"},
		{"", "echo ${!Z}"},
		{"", "echo ${Z@Q}"},
		{"", "echo ${Z:-$(true)}"},
		{"", "echo \"$(true)\""},
		{"", "echo @(a)"},
		{"", "echo <(true)"},
		{"", "echo <<E\nE"},
		{"", "echo <<-E\nE"},
		{"", "{fd}>f echo"},
		{"", "echo > $(true)"},
		{"", "Y=2 >f"},
		{"", "export Y=2 >f"},
		{"", "Z=(a)"},
		{"", "Z[0]=a"},
		{"", "! echo"},
		{"", "echo &"},
		{"", "echo | cat"},
		{"", "echo && echo"},
		{"", "declare Z=1"},
		{"", "typeset Z=1"},
		{"", "export -n Z"},
		{"", `export "Y=2"`},
		{"", "if true; then :; fi"},
		{"", "f() { :; }"},
		{"", "( : )"},
		{"", "{ :; }"},
		{"", "\\echo"},
		{"", `"echo"`},
		{"", "$e"},
		{"posix", "export Y=2 >f"},
		{"posix", `export "Y=2"`},
		{"posix", `export Y"=2"`},
		{"posix", "export $Z"},
		{"posix", "local Y=2"},
		{"posix", `readonly "Y"`},
	} {
		got, line := between(t, c.lang, c.between)
		if want := "$Y!@" + line; got != want {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.between, got, want)
		}
	}
}

// A read only variable keeps its value when a later statement assigns it,
// which bash reports and then goes on from, so the walk no longer knows it.
// Its own value resolves. The planted break: set without its test of
// readonly, which gave 2.
func TestShellReadonlyKeepsItsValue(t *testing.T) {
	for _, c := range []struct{ lang, body, want string }{
		{"", "readonly Y=1\nX=$Y\n", "1@2"},
		{"", "readonly Y=1\nexport Y=2\nX=$Y\n", "$Y!@3"},
		{"", "Y=1\nreadonly Y\nexport Y=2\nX=$Y\n", "$Y!@4"},
		{"", "readonly Y=1 Y=2\nX=$Y\n", "$Y!@2"},
		{"", "readonly Y=1\nY=2\nX=$Y\n", "$Y!@3"},
		{"", "export Y=1\nexport Y=2\nX=$Y\n", "2@3"},
		{"posix", "readonly Y=1\nexport Y=2\nX=$Y\n", "$Y!@3"},
		{"posix", "Y=1\nreadonly Y\nexport Y=2\nX=$Y\n", "$Y!@4"},
		{"posix", "export Y=1\nexport Y\nX=$Y\n", "1@3"},
	} {
		if got := values(t, Spec{File: "deploy.sh", Name: "X", Language: c.lang}, c.body); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.body, got, c.want)
		}
	}
}

// A variable the shell owns is not literal whatever the script gave it:
// cd changes PWD, a command changes PIPESTATUS, and bash keeps BASH_ARGV0
// as $0. The planted break: owned without its prefixes, which gave a.
func TestShellOwnedVariables(t *testing.T) {
	for _, name := range []string{"PWD", "OLDPWD", "PIPESTATUS", "RANDOM", "BASH_ARGV0", "KSH_VERSION", "COMP_LINE"} {
		body := name + "=a\nX=$" + name + "\n"
		if got, want := values(t, spec("X"), body), "$"+name+"!@2"; got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

// A reference whose value would make the text longer than max_bytes is
// not resolved, since no such value can be returned; the copies stay within
// it (the security review's R4). At max_bytes exactly it resolves. The
// planted break: part without its test of the length, which grew the
// value.
func TestShellResolutionStopsAtMaxBytes(t *testing.T) {
	a := strings.Repeat("a", 20)
	s := Spec{File: "deploy.sh", Name: "X", MaxBytes: 40}
	if got, want := values(t, s, "A="+a+"\nX=$A$A\n"), strings.Repeat(a, 2)+"@2"; got != want {
		t.Errorf("at max_bytes: got %q, want %q", got, want)
	}
	if got, want := values(t, s, "A="+a+"a\nX=$A$A\n"), "$A$A!@2"; got != want {
		t.Errorf("past max_bytes: got %q, want %q", got, want)
	}
}

// The values the walk knows hold at most max_bytes in all, counted once for
// each variable: a value reassigned replaces the old one's bytes. The
// planted breaks: set with >= for >, and set without subtracting the old
// value, which each left B unknown.
func TestShellKnownValuesHoldMaxBytes(t *testing.T) {
	body := "A=aaaa\nA=aaaa\nB=$A$A$A$A$A$A$A$A$A$A\nX=$B\n"
	if got, want := values(t, Spec{File: "deploy.sh", Name: "X", MaxBytes: 44}, body), strings.Repeat("a", 40)+"@4"; got != want {
		t.Errorf("at the limit: got %q, want %q", got, want)
	}
	if got, want := values(t, Spec{File: "deploy.sh", Name: "X", MaxBytes: 43}, body), "$B!@4"; got != want {
		t.Errorf("past the limit: got %q, want %q", got, want)
	}
}

// Once a statement and its here-documents are walked, the walk clears its
// words and literals, so the parser's batches of 32 hold no tree of a
// statement before (the security review's R1). A statement whose
// here-document is still to read keeps its syntax until it is read, since
// the parser reads the delimiter from it; a redirect that is no
// here-document is not pending. The planted breaks: walk without release,
// and release without its test of pending, which cleared the delimiter and
// lost the body's default.
func TestShellReleasesEachStatement(t *testing.T) {
	data := []byte("Y=$a\"$b\"'c'\ncat <<A; echo \"${X:-$c}\"\n${X:-1}\nA\necho >f\nZ=d W=$Y\n")
	w := newWalker("X", syntax.LangBash, data, core.NewMeter(len(data)), len(data))
	var stmts []*syntax.Stmt
	for stmt, err := range syntax.NewParser().StmtsSeq(strings.NewReader(string(data))) {
		if err != nil {
			t.Fatal(err)
		}
		w.walk(stmt)
		stmts = append(stmts, stmt)
	}
	// The parser reads the body before it gives the echo, which ends the
	// line, so the body's default comes first.
	if len(w.found) != 2 || w.found[0].text != "1" || w.found[1].text != "$c" {
		t.Errorf("found %+v", w.found)
	}
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.Stmt:
				if n.Cmd != nil || n.Redirs != nil {
					t.Errorf("a statement at %s keeps its command", n.Pos())
				}
			case *syntax.Word:
				if len(n.Parts) != 0 {
					t.Errorf("a word at %s keeps %d parts", n.Pos(), len(n.Parts))
				}
			case *syntax.DblQuoted:
				if len(n.Parts) != 0 {
					t.Errorf("a string at %s keeps %d parts", n.Pos(), len(n.Parts))
				}
			case *syntax.Lit:
				if n.Value != "" {
					t.Errorf("a literal keeps %q", n.Value)
				}
			case *syntax.SglQuoted:
				if n.Value != "" {
					t.Errorf("a quote keeps %q", n.Value)
				}
			}
			return true
		})
	}
}

// peakHeap is the most heap read takes of body, sampled every 100 µs with
// the collector at 10%, so what a read keeps shows over what it throws away.
func peakHeap(t *testing.T, s Spec, body string) uint64 {
	t.Helper()
	x := entryExtractor(t, s)
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var peak atomic.Uint64
	done := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			peak.Store(max(peak.Load(), m.HeapAlloc))
			select {
			case <-done:
				return
			case <-time.After(100 * time.Microsecond):
			}
		}
	}()
	_, err := x.FromData([]byte(body))
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	return peak.Load()
}

// Eight statements at the statement limit peak at about what one does: the
// walk releases each statement's syntax. Before, the parser's batches kept
// every statement's. Read at 1 MiB, whose statements are 128 KiB, eight
// words took 1.1 times one and eight groups 1.5; make slow reads 32 MiB,
// where they took 1.1 times 410 MiB and 1.5 times 542 MiB. The planted
// breaks: walk without release, which took 2.1 times one for both, and
// release leaving each statement's command, 2.0 for groups.
func TestShellMemoryDoesNotGrowWithStatements(t *testing.T) {
	// The race detector multiplies its time twentyfold and inflates the
	// memory it measures, so make slow runs it without race.
	slowOnly(t)
	statementsPeak(t, 1<<20, 1.8)
}

func TestShellMemoryDoesNotGrowWithStatementsAtFullSize(t *testing.T) {
	slowOnly(t)
	statementsPeak(t, core.DefaultMaxBytes, 2)
}

// statementsPeak compares the peak heap of one statement just under the
// statement limit at max_bytes size, a word of $a or a group of a;, with
// that of eight, which may be at most ratio times it.
func statementsPeak(t *testing.T, size int, ratio float64) {
	t.Helper()
	s := Spec{File: "deploy.sh", Name: "X", MaxBytes: int64(size)}
	half := (size/8 - 64) / 2
	for _, stmt := range []string{"Y=" + strings.Repeat("$a", half) + "\n", "{ " + strings.Repeat("a;", half-2) + " }\n"} {
		one := peakHeap(t, s, stmt)
		eight := peakHeap(t, s, strings.Repeat(stmt, 8))
		t.Logf("%.8s: one statement %d MiB, eight %d MiB", stmt, one>>20, eight>>20)
		if float64(eight) > ratio*float64(one) {
			t.Errorf("%.8s: eight statements peak at %d MiB, one at %d MiB: over %.1f times", stmt, eight>>20, one>>20, ratio)
		}
	}
}

// A here-document still to read keeps its redirect alone, not a copy of the
// walk's stack, which held 20000 nodes under a pipeline (the security
// review's R2: 4000 documents took 1232 MiB). The planted break: a pending
// document with a clone of the stack, which allocated 57 MiB more here.
func TestShellPendingDocumentsCopyNoStack(t *testing.T) {
	pipe := strings.Repeat("|a", 9000)
	docs := 200
	with := "{ cat" + strings.Repeat(" <<A", docs) + "; }" + pipe + "; :\n" + strings.Repeat("A\n", docs)
	without := "{ cat" + strings.Repeat(" <A", docs) + "; }" + pipe + "; :\n" + strings.Repeat("A\n", docs)
	x := entryExtractor(t, spec("X"))
	read := func(body string) uint64 {
		return extracttest.Allocated(func() {
			if _, err := x.FromData([]byte(body)); err != nil {
				t.Fatal(err)
			}
		})
	}
	if a, b := read(with), read(without); a > b+8<<20 {
		t.Errorf("here-documents allocated %d MiB, redirects %d MiB", a>>20, b>>20)
	}
}

// A comment block counts towards the statement after it, and the message
// says so (the security review's R5). Its line is where the parser had
// read to at the last statement, up to 1 KiB past it. The planted break:
// the message as it was, without the comments.
func TestShellLongCommentBlock(t *testing.T) {
	body := "X=0\n" + strings.Repeat("# a comment line\n", 1<<10) + "X=1\n"
	got := refusal(t, Spec{File: "deploy.sh", Name: "X", MaxBytes: 80 << 10}, body)
	if want := ": a statement, with the comments and blank lines before it, is more than 10240 bytes long"; !strings.Contains(got, want) {
		t.Errorf("error %q, want %q", got, want)
	}
}

// A { in a declaration's argument is not literal, whether or not bash
// expands it; a } alone is text. The planted break: bracesExpand reporting
// false, which gave b for declare X={a,b}.
func TestShellBracesInADeclaration(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"declare X={a,b}", "{a,b}!@1"},
		{"declare X={a}", "{a}!@1"},
		{"declare X=a}", "a}@1"},
		{"declare X='{a,b}'", "{a,b}@1"},
		{"X={a,b}", "{a,b}@1"},
	} {
		if got := values(t, spec("X"), c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// The text of quotes and backslashes, the rows each branch of lit, escape
// and dollarQuote takes. A backslash that ends a word quotes nothing, so it
// is not literal. The planted breaks: each test turned, which changed its
// row.
func TestShellQuotingRows(t *testing.T) {
	for _, c := range []struct{ lang, body, want string }{
		{"", "X=a\\", "a\\!@1"},
		{"", "X=a\\b", "ab@1"},
		{"", `: "${X:-a\$b\c}"`, `a$b\c@1`},
		{"", `: "${X:-\\}"`, `\@1`},
		{"", `X=$'\101\x41\x4a\x4A\377\7'`, "AAJJ\xff\a@1"},
		{"", `X=$'\1015'`, "A5@1"},
		{"", `X=$'\x415'`, "A5@1"},
		{"", `X=$'\400'`, `$'\400'!@1`},
		{"", `X=$'\0'`, `$'\0'!@1`},
		{"", `X=$'\1\x1'`, "\x01\x01@1"},
		{"", `X=$'\x'`, `$'\x'!@1`},
		{"", `X=$'\8'`, `$'\8'!@1`},
		{"", `X=$'\a\b\e\E\f\n\r\t\v\\\'\"\?'`, "\a\b\x1b\x1b\f\n\r\t\v\\'\"?@1"},
		{"", "cat <<E\n${X:-a\\b}\nE\n", `a\b!@2`},
		{"", "cat <<E\n${X:-ab}\nE\n", "ab@2"},
		{"posix", `X=\$'a'`, "$a@1"},
		{"posix", `X=\\$'a'`, `\\$'a'!@1`},
		{"posix", `X=\\\$'a'`, `\$a@1`},
		{"posix", `X=$"a"`, `$"a"!@1`},
		{"posix", `X=a$`, "a$@1"},
		{"posix", `X=$b'a'`, `$b'a'!@1`},
	} {
		if got := values(t, Spec{File: "deploy.sh", Name: "X", Language: c.lang}, c.body); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.body, got, c.want)
		}
	}
}

// Names: a letter or _ first, then letters, digits and _, at each end of
// each range. The planted break: isName taking nameBytes for the first
// byte, which accepted 0X.
func TestShellNameBytes(t *testing.T) {
	for _, name := range []string{"_", "a", "z", "A", "Z", "a0", "a9", "a_"} {
		if !isName(name) {
			t.Errorf("%q is a name", name)
		}
	}
	for _, name := range []string{"", "0X", "9", "a-", "a.", "@", "[", "`", "{", "a/", "a:", "\xc3\xa9"} {
		if isName(name) {
			t.Errorf("%q is no name", name)
		}
	}
}

// A shebang through env with options alone, or busybox alone, names no
// program. The planted breaks: the loop to <= len(fields), and busybox's
// test as len(fields) >= 1, which each indexed past the fields.
func TestShellShebangWithNoProgram(t *testing.T) {
	for _, body := range []string{"#!/usr/bin/env -i\nX=1\n", "#!/usr/bin/env -u\nX=1\n", "#!/bin/busybox\nX=1\n", "#!\nX=1\n"} {
		if got := refusal(t, spec("X"), body); !strings.Contains(got, `the shebang names "", which is no shell errata reads`) {
			t.Errorf("%q: %q", body, got)
		}
	}
}

// An assignment before a command sets the variable for the command alone,
// after the shell has expanded the command's words, so the walk forgets it:
// bash gives the default 1 here. The planted break: assign recording it,
// which gave 2.
func TestShellPrefixAssignmentIsTemporary(t *testing.T) {
	if got, want := values(t, spec("X"), "Y=1\nY=2 cmd \"${X:-$Y}\"\n"), "$Y!@2"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
