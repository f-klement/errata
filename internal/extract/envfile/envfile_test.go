package envfile

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// marked extracts from body in .env with s and writes each value as
// text@line, with a ! after the text when it is marked not literal.
func marked(t *testing.T, body string, s Spec) string {
	t.Helper()
	s.File = ".env"
	return texts(extracttest.MustRun(t, Build, s, extracttest.Repo(t, map[string]string{".env": body})))
}

func texts(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, v.Text+mark+"@"+strconv.Itoa(v.At.Line))
	}
	return strings.Join(out, " ")
}

// refusal is the error s gives on body in .env.
func refusal(t *testing.T, body string, s Spec) string {
	t.Helper()
	s.File = ".env"
	got, err := extracttest.Run(t, Build, s, extracttest.Repo(t, map[string]string{".env": body}))
	if err == nil {
		t.Fatalf("%q: no error, values %s", body, texts(got))
	}
	return err.Error()
}

func name(n string) Spec { return Spec{Name: n} }

// Each rule of docker compose's .env format, as compose-go v2.16.2 reads
// it. TestEnvfileAgreesWithCompose holds the reader to compose over a
// generated corpus; these name the rules one by one.
func TestEnvfileDialect(t *testing.T) {
	for _, c := range []struct{ name, body, key, want string }{
		{"a plain value", "API_PORT=8080\n", "API_PORT", "8080@1"},
		{"the line of each key", "# c\n\nA=1\n  B=2\n", "B", "2@4"},
		{"export", "export A=1\nexport\tB=2\n", "B", "2@2"},
		{"export alone is a key", "export=1\n", "export", "1@1"},
		{"yaml style", "A: 1\n", "A", "1@1"},
		{"white space around =", "A = 1  \n", "A", "1@1"},
		{"a comment after a space", "A=1 # c\n", "A", "1@1"},
		{"# with no space is the value's", "A=1#c\n", "A", "1#c@1"},
		{"a tab before # keeps it", "A=1\t# c\n", "A", "1\t# c@1"},
		{"single quotes are literal", `A='a ${B} \n # c'` + "\n", "A", `a ${B} \n # c@1`},
		{"\\' in single quotes", `A='it\'s'` + "\n", "A", "it's@1"},
		{"double quote escapes", `A="a\tb\n\"\\\$c\0101"` + "\n", "A", "a\tb\n\"\\$cA@1"},
		{"an escape compose keeps", `A="\c\q\012"` + "\n", "A", `\c\q\12@1`},
		{"a quoted value across lines", "A=\"a\nb\"\nB=2\n", "A", "a\nb@1"},
		{"the key after a value across lines", "A='a\nb'\nB=2\n", "B", "2@3"},
		{"after the quote, the next statement", "A='a' B=2\n", "B", "2@1"},
		{"CRLF", "A=1\r\nB=2\r\n", "A", "1@1"},
		{"CRLF counts one line", "A=1\r\nB=2\r\n", "B", "2@2"},
		{"a byte order mark", "\uFEFFA=1\n", "A", "1@1"},
		{"a key set twice", "A=1\nB=2\nA=3\n", "A", "1@1 3@3"},
		{"a key of letters, digits and marks", "x.y-z[0]_É=1\n", "x.y-z[0]_É", "1@1"},
		{"an empty value", "A=\n", "A", "@1"},
		{"a last line without = sets the empty key", "A=1\nB", "", "B@2"},
	} {
		spec := name(c.key)
		if c.key == "" {
			spec = Spec{}
			c.want = "1@1 " + c.want
		}
		if got := marked(t, c.body, spec); got != c.want {
			t.Errorf("%s: %q: got %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// A substitution reads the keys set before it in the file. One that names
// a key the file does not set, or sets after, or leaves to the environment,
// returns the value as written, marked not literal.
func TestEnvfileSubstitution(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"${NAME}", "A=x\nB=${A}/y\n", "x/y@2"},
		{"$NAME", "A=x\nB=$A-y\n", "x-y@2"},
		{"in double quotes", "A=x\nB=\"${A} y\"\n", "x y@2"},
		{"not in single quotes", "A=x\nB='${A}'\n", "${A}@2"},
		{"$$ is $", "A=x\nB=$${A}\n", "${A}@2"},
		{"\\$ in double quotes is $", "A=x\nB=\"\\$A\"\n", "$A@2"},
		{"a $ before no name", "B=$1 $ $-\n", "$1 $ $-@1"},
		{"the value before, set twice", "A=1\nA=${A}2\nB=$A\n", "12@3"},
		{"an unset key", "B=${A}/y\n", "${A}/y!@1"},
		{"a key set after", "B=${A}\nA=1\n", "${A}!@1"},
		{"a key from the environment", "A\nB=${A}\n", "${A}!@2"},
		{"a key from the environment after a value", "A=1\nA\nB=${A}\n", "${A}!@3"},
		{"quoted as written", "B=\"x\\t${A}\"\n", "x\\t${A}!@1"},
		{":- with the key set", "A=1\nB=${A:-d}\n", "1@2"},
		{":- with the key empty", "A=\nB=${A:-d}\n", "d@2"},
		{"- with the key empty", "A=\nB=${A-d}\n", "@2"},
		{":+ with the key set", "A=1\nB=${A:+y}\n", "y@2"},
		{":+ with the key empty", "A=\nB=${A:+y}\n", "@2"},
		{"+ with the key empty", "A=\nB=${A+y}\n", "y@2"},
		{"? with the key set", "A=1\nB=${A?m}\n", "1@2"},
		{"? with the key empty", "A=\nB=${A?m}\n", "@2"},
		{":- with the key unset", "B=${A:-d}\n", "${A:-d}!@1"},
		{"? with the key unset", "B=${A?m}\n", "${A?m}!@1"},
		{"a word substituted", "A=\nC=c\nB=${A:-${C}x}\n", "cx@3"},
		{"a word not read", "A=1\nB=${A:-${}\n", "1@2"},
		{"the rest after the closing }", "A=1\nB=${A:-x}${A}}\n", "11}@2"},
		{"a { hides the byte after it", "A=\nB=${A:-{}x}y}\n", "{}x}y@2"},
		{"the rest of a line, not the next", "A=\nB=\"${A:-x}\n}\"\n", "x\n}@2"},
		{"compose's case folded names", "\u212a=1\nB=$\u212a\n", "1@2"},
	} {
		if got := marked(t, c.body, name("B")); got != c.want {
			t.Errorf("%s: %q: got %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// What compose refuses makes the rule unknown, at the line it refuses.
// The planted breaks: each refusal removed, and the read went on.
func TestEnvfileRefuses(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"a character no name holds", "A=1\nA$=1\n", `line 2: the name holds "$", which compose refuses in a name`},
		{"a space in a name", "A B=1\n", `line 1: the name "A B" holds a space, which compose refuses`},
		{"a byte that is not UTF-8", "\xff=1\n", `line 1: the name holds "\xff", which compose refuses`},
		{"an open quote", "A=\"x\nB=2\n", `line 1: the value's " is not closed, which compose refuses`},
		{"export and nothing", "A=1\nexport ", "line 2: export is followed by no name, which compose refuses"},
		{"${ with no name", "A=${1}\n", `line 1: "${1}" starts no substitution compose reads; write $${ for a literal ${`},
		{"${ with no closing }", "A=${B:-x\n", `"${B:-x" starts no substitution`},
		{"${ and an operator compose has not", "A=${B:x}\n", `"${B:x}" starts no substitution`},
		{"${ and no } on the line", "A=\"${B:-x\n}\"\n", "starts no substitution"},
		{"an invalid substitution after an unset one", "A=${Z} ${\n", "starts no substitution"},
		{":? with the key empty", "B=\nA=${B:?m}\n", "line 2: ${B:?} needs a value that is not empty and the file sets it empty, which compose refuses"},
		{"an invalid word read", "B=\nA=${B:-${}\n", "starts no substitution"},
	} {
		got := refusal(t, c.body, name("A"))
		if !strings.Contains(got, c.want) || !strings.HasPrefix(got, ".env: ") {
			t.Errorf("%s: %q: got %q, want it to hold %q", c.name, c.body, got, c.want)
		}
	}
}

// Without a name, the extractor returns every key's value in file order,
// a key from the environment too, marked not literal.
func TestEnvfileEveryKey(t *testing.T) {
	got := marked(t, "A=1\n# c\nB=${A}\nC\nA=2\n", Spec{})
	if want := "1@1 1@3 !@4 2@5"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Each value carries the key it is set under, without a name and with one,
// the empty key of a line =x too.
func TestEnvfileValuesCarryTheirKey(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{".env": "A=1\nexport B=2\nC\nA=3\n=x\n"})
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{File: ".env"}, `"A"=1 "B"=2 "C"= "A"=3 ""=x`},
		{Spec{File: ".env", Name: "A"}, `"A"=1 "A"=3`},
	} {
		var got []string
		for _, v := range extracttest.MustRun(t, Build, c.spec, root) {
			if !v.HasKey {
				t.Errorf("%+v: %q has no key", c.spec, v.Text)
			}
			got = append(got, strconv.Quote(v.Key)+"="+v.Text)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%+v: got %q, want %q", c.spec, got, c.want)
		}
	}
}

// A name is one an env file can set; any other does not load. A file is
// required, and must be inside the repository, as for every kind.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Name: "A"}, "envfile: file is required"},
		{Spec{File: "../x.env", Name: "A"}, "envfile: file \"../x.env\" is outside the repository"},
		{Spec{File: ".env", Name: "A", MaxBytes: -1}, "envfile: max_bytes is -1"},
		{Spec{File: ".env", Name: "A B"}, `envfile: name "A B" is no key an env file can set; compose reads a name of letters, digits, _, ., -, [ and ]`},
		{Spec{File: ".env", Name: "A=B"}, `envfile: name "A=B" is no key`},
		{Spec{File: ".env", Name: "export A"}, `envfile: name "export A" is no key`},
		{Spec{File: ".env", Name: "#A"}, `envfile: name "#A" is no key`},
		{Spec{File: ".env", Name: "A\nB"}, `is no key`},
		{Spec{File: ".env", Name: "A$"}, `is no key`},
	} {
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	for _, ok := range []string{"A", "x.y-z[0]", "É1", "A\tB", "export", "1"} {
		if _, err := Build(Spec{File: ".env", Name: ok}); err != nil {
			t.Errorf("name %q: %v", ok, err)
		}
	}
}

// A template file's value the engine renders is not literal, judged on its
// text as written, and so is every substitution of its key, since the engine
// may render it empty. Terraform renders ${A} itself in a .tpl file. The
// extractor can always return a not literal value.
func TestEnvfileTemplate(t *testing.T) {
	for _, c := range []struct{ file, body, want string }{
		{".env.j2", "A={{ port }}\nB=1\n", "{{ port }}!@1 1@2"},
		{"app.env.tpl", "A=1\nB=${A}\n", "1@1 ${A}!@2"},
		{"app.env.j2", "C={{ x }}\nD=${C:+on}\n", "{{ x }}!@1 ${C:+on}!@2"},
		{"app.env.erb", "A=<%= x %>\nB=${A:+on}\n", "<%= x %>!@1 ${A:+on}!@2"},
		{"app.env", "A={{ x }}\nB=${A:+on}\n", "{{ x }}@1 on@2"},
	} {
		root := extracttest.Repo(t, map[string]string{c.file: c.body})
		if got := texts(extracttest.MustRun(t, Build, Spec{File: c.file}, root)); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.file, c.body, got, c.want)
		}
	}
	x, _ := Build(Spec{File: ".env", Name: "A"})
	if !check.MayReturnNotLiteral(x) {
		t.Error("an envfile extractor declares no not literal value")
	}
}

// A substitution nested past core.MaxDepth is refused, at its line. Each
// level reads its word again, so a comment gives the file the work budget
// that nesting costs.
func TestEnvfileDepth(t *testing.T) {
	deep := func(n int) string {
		return "A=\nB=" + strings.Repeat("${A:-", n) + "x" + strings.Repeat("}", n) + "\n#" + strings.Repeat("c", 1<<16) + "\n"
	}
	if got := marked(t, deep(core.MaxDepth), name("B")); got != "x@2" {
		t.Errorf("at the limit: %q", got)
	}
	got := refusal(t, deep(core.MaxDepth+1), name("B"))
	if !strings.Contains(got, "line 2: nested more than 1000 levels deep, the limit") {
		t.Errorf("past the limit: %q", got)
	}
}

// A chain of keys that each repeat the one before doubles the value on each
// line; the copies are charged, so the read is refused for work long before
// the values fill memory.
func TestEnvfileRefusesADoublingChain(t *testing.T) {
	var b strings.Builder
	b.WriteString("K0=xxxxxxxx\n")
	for i := 1; i <= 40; i++ {
		b.WriteString("K" + strconv.Itoa(i) + "=${K" + strconv.Itoa(i-1) + "}${K" + strconv.Itoa(i-1) + "}\n")
	}
	got := refusal(t, b.String(), name("K40"))
	if !strings.Contains(got, "units of work for each byte") {
		t.Errorf("got %q", got)
	}
}

// Every loop of the reader charges the meter: at each budget below what a
// read costs it is refused, and at its cost it reads what it reads with no
// meter. An ordinary file costs under a quarter of the budget a byte.
func TestEnvfileWork(t *testing.T) {
	body := "# a comment\nexport A=1\nB=\"${A} \\t x\"\nC='y'\nD=${B:-z}$A\nE\n  F=${Z:-q}\n"
	for _, s := range []Spec{{File: ".env", Name: "D"}, {File: ".env"}} {
		x, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		coretest.RefusesAtEveryBudget(t, x.(core.EntryExtractor), body)
		// Each statement is charged a line's work, so a file of short
		// assignments costs at least that much a line.
		if spent, lines := coretest.Spent(t, x.(core.EntryExtractor), []byte(strings.Repeat("D=1\n", 100))), 100; spent < core.LineWork*int64(lines) {
			t.Errorf("%d lines cost %d units, under %d each", lines, spent, core.LineWork)
		}
		coretest.LeavesRoom(t, x.(core.EntryExtractor), strings.Repeat(body, 50))
	}
}

// A template file costs no allocation more for each statement than a plain
// one: the template test reuses one entry. The planted break, a new entry for
// each test, allocated one for each of the 1000 statements.
func TestEnvfileTemplateAllocatesNoEntryAStatement(t *testing.T) {
	var b strings.Builder
	for i := range 1000 {
		b.WriteString("K" + strconv.Itoa(i) + "=1\n")
	}
	data := []byte(b.String())
	allocs := func(file string) float64 {
		x, err := Build(Spec{File: file, Name: "zz"})
		if err != nil {
			t.Fatal(err)
		}
		return testing.AllocsPerRun(5, func() {
			if _, err := x.(core.EntryExtractor).FromData(data); err != nil {
				t.Fatal(err)
			}
		})
	}
	if plain, tpl := allocs(".env"), allocs(".env.j2"); tpl > plain+100 {
		t.Errorf("a template file allocates %v times for 1000 statements, a plain one %v", tpl, plain)
	}
}
