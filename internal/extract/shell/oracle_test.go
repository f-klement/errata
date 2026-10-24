package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The reader's literal values are compared with what bash and dash assign
// through corpora of generated scripts and the value each shell gave each
// variable (extracttest.OracleCase, an entry of name and value for each).
// Neither corpus needs a shell to read, so CI reads them as committed, and
// TestRegenerateOracleCorpora writes them again:
//
//	ERRATA_BASH=bash ERRATA_DASH=dash go test ./internal/extract/shell -run TestRegenerateOracleCorpora
//
// The corpora of this commit came from GNU bash 5.2.37 and dash 0.5.12, on
// Debian 13. A value errata marks not literal is not compared: errata does
// not claim to know it. mksh has no corpus, since none was installed; where
// its reading was not seen to agree, the reader marks a value not literal.
const (
	bashCorpus = "testdata/oracle/bash.json"
	dashCorpus = "testdata/oracle/dash.json"
)

// The pieces a generated value is made of: text, every quote, backslashes
// in and out of quotes, and a few expansions, whose values are not compared.
var (
	unquotedPieces = []string{"a", "b1", `\ `, `\\`, `\$`, `\'`, `\"`, `x\y`, "é", "*", "?", "[a]", "{a,b}", "%", "=", ":", "#", `\#`, "a~"}
	quotedPieces   = []string{"'a b'", `'\'`, `'"'`, "''", "'$x'", "'a\nb'", `"a b"`, `"\""`, `"\\"`, `"\$"`, `"\a"`, `"'"`, `""`, `"é"`, "\"a\\\nb\"", `"\` + "`" + `"`}
	ansiPieces     = []string{`$'\t'`, `$'\x41'`, `$'\101'`, `$'\''`, `$'\\'`, `$'\e'`, `$'a b'`, `$'\q'`, `$'\x'`}
	expandPieces   = []string{"$x", "${x}", "$((1+1))", "~/a"}
)

// oracleScript draws a script of up to six lines, each assigning one
// variable of its own, V0 to V5, in one of the forms the shell has.
func oracleScript(r *rand.Rand, bash bool) string {
	pick := func(from []string) string { return from[r.IntN(len(from))] }
	pieces := append(append([]string{}, unquotedPieces...), quotedPieces...)
	if bash {
		pieces = append(pieces, ansiPieces...)
	}
	forms := []string{"V=W", "export V=W", "readonly V=W", ": ${V:=W}", ": \"${V:=W}\""}
	if bash {
		forms = append(forms, "declare V=W", "declare -r V=W", "typeset V=W")
	}
	var b strings.Builder
	for i := range 1 + r.IntN(6) {
		var w strings.Builder
		for range 1 + r.IntN(4) {
			if r.IntN(15) == 0 {
				w.WriteString(pick(expandPieces))
			} else {
				w.WriteString(pick(pieces))
			}
		}
		line := strings.ReplaceAll(pick(forms), "V", "V"+strconv.Itoa(i))
		b.WriteString(strings.Replace(line, "W", w.String(), 1) + "\n")
	}
	return b.String()
}

func oracleInputs(seed uint64, bash bool) []string {
	return extracttest.GenerateCorpus(seed, func(r *rand.Rand) string { return oracleScript(r, bash) })
}

// TestRegenerateOracleCorpora writes the corpora again from ERRATA_BASH and
// ERRATA_DASH.
func TestRegenerateOracleCorpora(t *testing.T) {
	bash, dash := os.Getenv("ERRATA_BASH"), os.Getenv("ERRATA_DASH")
	if bash == "" || dash == "" {
		t.Skip("set ERRATA_BASH and ERRATA_DASH to write the oracle corpora again")
	}
	writeShellCorpus(t, bashCorpus, oracleInputs(34, true), bash)
	writeShellCorpus(t, dashCorpus, oracleInputs(35, false), dash)
}

// writeShellCorpus runs each script in shell, and writes the value it gives
// each of the script's variables, one case to a line.
func writeShellCorpus(t *testing.T, corpus string, inputs []string, shell string) {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for i, in := range inputs {
		file := filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(file, []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		n := 0
		for strings.Contains(in, "V"+strconv.Itoa(n)) {
			n++
		}
		// The script is sourced, then each variable printed with a NUL
		// after it, which no value holds.
		cmd := exec.CommandContext(t.Context(), shell, "-c", `x=X; . "$1"; i=0; while [ $i -lt $2 ]; do eval "printf '%s\0' \"\$V$i\""; i=$((i+1)); done`, "sh", file, strconv.Itoa(n))
		cmd.Dir = dir
		// A script may complain, as bash does of readonly V={a,b}, which
		// it expands to two assignments of one readonly variable; the
		// values it gave are still the oracle's.
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		c := extracttest.OracleCase{Input: in}
		values := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if err != nil || len(values) != n {
			// A script the shell does not run, such as one dash cannot
			// parse, is not compared.
			c.Refused = strings.TrimSpace(stderr.String())
		} else {
			for j := range n {
				c.Entries = append(c.Entries, []string{"V" + strconv.Itoa(j), values[j]})
			}
		}
		line, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(line))
	}
	if err := os.MkdirAll(filepath.Dir(corpus), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpus, []byte("[\n"+strings.Join(lines, ",\n")+"\n]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// compareShell reads every variable of every case in lang and compares
// each literal value with the shell's. It counts the values compared, those
// errata marked not literal, the scripts the shell refused, and those the
// shell ran that mvdan.cc/sh does not parse (unparsed). errata refuses
// such a script, so a rule on it is unknown, which is safe; it is not
// compared, so a fresh seed does not fail on the parser.
func compareShell(tb testing.TB, cases []extracttest.OracleCase, lang string) (literal, notLiteral, refused, unparsed int) {
	tb.Helper()
	for i, c := range cases {
		if c.Refused != "" {
			refused++
			continue
		}
		if _, err := syntax.NewParser(syntax.Variant(languages[lang])).Parse(strings.NewReader(c.Input), ""); err != nil {
			unparsed++
			continue
		}
		for _, e := range c.Entries {
			var got []core.Entry
			r := reader{name: e[0], language: lang, file: "f"}
			err := r.read([]byte(c.Input), nil, func(en *core.Entry) error {
				got = append(got, core.Entry{Value: bytes.Clone(en.Value), Computed: en.Computed})
				return nil
			})
			switch {
			case err != nil:
				tb.Errorf("case %d %q: %v", i, c.Input, err)
			case len(got) == 0:
				tb.Errorf("case %d %q: no value of %s, the shell's %q", i, c.Input, e[0], e[1])
			case got[0].Computed:
				notLiteral++
			case string(got[0].Value) != e[1]:
				tb.Errorf("case %d %q: %s is %q, the shell's %q", i, c.Input, e[0], got[0].Value, e[1])
			default:
				literal++
			}
		}
	}
	return literal, notLiteral, refused, unparsed
}

// The reader gives every variable of the bash corpus the value bash gave
// it, or marks it not literal; and so for dash in posix.
func TestShellAgreesWithBash(t *testing.T) {
	checkCorpus(t, bashCorpus, oracleInputs(34, true), "bash")
}

func TestPOSIXAgreesWithDash(t *testing.T) {
	checkCorpus(t, dashCorpus, oracleInputs(35, false), "posix")
}

func checkCorpus(t *testing.T, corpus string, inputs []string, lang string) {
	t.Helper()
	cases := extracttest.ReadCorpus(t, corpus, inputs)
	literal, notLiteral, refused, unparsed := compareShell(t, cases, lang)
	// The corpus must hold both kinds in number, and few scripts the shell
	// refused or errata could not parse, or it compares little.
	if literal < extracttest.OracleCases || notLiteral < extracttest.OracleCases/10 || refused+unparsed > extracttest.OracleCases/10 {
		t.Errorf("%d literal values, %d not literal, %d scripts refused, %d not parsed", literal, notLiteral, refused, unparsed)
	}
	if testing.Verbose() {
		t.Logf("%s: %d literal values, %d not literal, %d scripts refused, %d not parsed", lang, literal, notLiteral, refused, unparsed)
	}
}

// reports records what compareShell reports, in place of a test's failure.
type reports struct {
	testing.TB
	got []string
}

func (e *reports) Helper() {}

func (e *reports) Errorf(format string, args ...any) {
	e.got = append(e.got, fmt.Sprintf(format, args...))
}

// A script bash ran that mvdan.cc/sh does not parse, as bash seed 136 case
// 668 is (the correctness review's A-N1), is counted and not compared, and
// one that parses is compared still. The planted break: compareShell
// without its parse, which failed on the script errata refuses.
func TestCompareShellSkipsWhatTheParserRefuses(t *testing.T) {
	cases := []extracttest.OracleCase{
		{Input: ": \"${V1:={a,b}\"'\"\"\"}\"\nV0=a\n", Entries: [][]string{{"V0", "a"}}},
		{Input: "V0=a\n", Entries: [][]string{{"V0", "b"}}},
	}
	e := &reports{TB: t}
	_, _, _, unparsed := compareShell(e, cases, "bash")
	if unparsed != 1 || len(e.got) != 1 || !strings.Contains(e.got[0], `V0 is "a", the shell's "b"`) {
		t.Errorf("%d not parsed, reported %q", unparsed, e.got)
	}
}
