package jsonc

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// buildC is Build without parsers, which no spec of these tests needs: the
// embedded steps are tested in internal/extract/core, through the facade.
func buildC(s Spec) (check.Extractor, error) { return Build(s, core.Parsers{}) }

// build5 is BuildJSON5 without parsers.
func build5(s Spec) (check.Extractor, error) { return BuildJSON5(s, core.Parsers{}) }

// The dialects, by the key of the kind that reads each.
const (
	JSONC = "jsonc"
	JSON5 = "json5"
)

// builder is the Build of the kind that reads dialect.
func builder(dialect string) func(Spec) (check.Extractor, error) {
	if dialect == JSON5 {
		return build5
	}
	return buildC
}

func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, buildC, s, root)
}

func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, buildC, s, root)
}

// A JSONC file reads as JSON does, past its comments and its trailing
// commas, each value at the line it starts on. Planted breaks: a // comment
// read to the end of the file, the line of a value taken after the comment
// before it, a trailing comma in an array refused.
func TestJSONCValuesAndLines(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"tsconfig.json": `// TypeScript's settings
{
  /* the compiler */
  "compilerOptions": {
    "target": "es2022", // the runtime's
    "strict": true,
    "lib": ["dom",
      /* the newest */ "es2023",],
    "n": 1e3, "z": -0.5, "none": null,
  },
  "s": "a // not a comment /* nor this */",
}
/* after */`})
	for path, want := range map[string]string{
		"compilerOptions.target": "es2022@5",
		"compilerOptions.strict": "true@6",
		"compilerOptions.lib[*]": "dom@7 es2023@8",
		"compilerOptions.n":      "1e3@9",
		"compilerOptions.z":      "-0.5@9",
		"compilerOptions.none":   "null@9",
		"s":                      "a // not a comment /* nor this */@11",
	} {
		if got := extracttest.Texts(mustRun(t, Spec{File: "tsconfig.json", Path: path}, root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// A JSON5 file reads its identifier keys, single quoted strings, escapes,
// line continuations, and numbers, each number as the file wrote it.
// Planted breaks: \x read as x, a line continuation keeping the line end,
// an escaped key not decoded, a leading point refused.
func TestJSON5ValuesAndLines(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"renovate.json5": "{\n" +
		"  extends: ['config:recommended',],\n" +
		"  $schema: 'https://docs.renovatebot.com/renovate-schema.json',\n" +
		"  'quoted': \"it's\",\n" +
		"  esc: '\\x41\\u00e9\\'\\v\\0\\q',\n" +
		"  long: 'one \\\n" +
		"two',\n" +
		"  hex: 0x1F, inf: -Infinity, nan: NaN, half: .5, whole: 5., plus: +1,\n" +
		"  \\u0061b: 'escaped key', \u00fcn\u00ef: 'letters',\n" +
		"}\n"})
	for path, want := range map[string]string{
		"extends[*]":    "config:recommended@2",
		"['$schema']":   "https://docs.renovatebot.com/renovate-schema.json@3",
		"quoted":        "it's@4",
		"esc":           "A\u00e9'\v\x00q@5",
		"long":          "one two@6",
		"hex":           "0x1F@8",
		"inf":           "-Infinity@8",
		"nan":           "NaN@8",
		"half":          ".5@8",
		"whole":         "5.@8",
		"plus":          "+1@8",
		"ab":            "escaped key@9",
		"\u00fcn\u00ef": "letters@9",
	} {
		vs, err := extracttest.Run(t, build5, Spec{File: "renovate.json5", Path: path}, root)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got := extracttest.Texts(vs); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// Each body is a planted break of a refusal, in the dialect named: what
// the dialect's grammar does not hold, a repeated key however JSON5 spells
// it, and a file holding no value or more than one.
func TestWhatDoesNotParseIsAnError(t *testing.T) {
	for _, c := range []struct{ dialect, body, want string }{
		{JSONC, "", "f: line 1: the file holds no value"},
		{JSONC, "// only a comment\n", "f: line 2: the file holds no value"},
		{JSONC, "{\n/* open", "f: line 2: the comment that opens here is not closed"},
		{JSONC, "{\"a\": 1\n", "f: line 2: the file ends before the value is complete"},
		{JSONC, `{"a": 1,`, "f: line 1: the file ends before the value is complete"},
		{JSONC, `["a`, "f: line 1: the string that opens here is not closed"},
		{JSONC, "{\n\"a\":\n\n \"abc", "f: line 4: the string that opens here is not closed"},
		{JSON5, "[\n'a\\", "f: line 2: the string that opens here is not closed"},
		{JSON5, "[\n'a \\\nb", "f: line 2: the string that opens here is not closed"},
		{JSONC, `[1,,]`, `f: line 1: a value cannot start with ','`},
		{JSONC, `{,}`, `f: line 1: a key cannot start with ','`},
		{JSONC, `{"a" 1}`, `f: line 1: a key is followed by :, not '1'`},
		{JSONC, `[1 2]`, `f: line 1: expected , or ], found '2'`},
		{JSONC, "{\"a\": 1,\n \"a\": 2}", `f: line 2: key "a" is repeated`},
		{JSONC, "{}\n{}", "f: line 2: data after the top-level value"},
		{JSONC, `{a: 1}`, `f: line 1: a key cannot start with 'a'`},
		{JSONC, `['a']`, `f: line 1: a value cannot start with '\''`},
		{JSONC, `[0x1]`, `f: line 1: a number cannot be followed by 'x'`},
		{JSONC, `[01]`, `f: line 1: a number cannot be followed by '1'`},
		{JSONC, `[.5]`, `f: line 1: a value cannot start with '.'`},
		{JSONC, `[1.]`, `f: line 1: a number cannot hold ']' here`},
		{JSONC, `[1e]`, `f: line 1: a number cannot hold ']' here`},
		{JSONC, `[tru]`, `f: line 1: a value cannot start with 't'`},
		{JSONC, `[truex]`, `f: line 1: a value cannot start with 't'`},
		{JSONC, `["\x41"]`, `f: line 1: \x is no escape of JSON`},
		{JSONC, "[\"a\tb\"]", `f: line 1: a string does not hold the control character '\t'`},
		{JSONC, "[\"a\nb\"]", `f: line 1: a string does not hold a line end`},
		{JSONC, `["\u12"]`, `f: line 1: \u takes four hex digits`},
		{JSONC, "[\v1]", `f: line 1: a value cannot start with '\v'`},
		{JSON5, `{a: 1, "a": 2}`, `f: line 1: key "a" is repeated`},
		{JSON5, `{\u0061: 1, a: 2}`, `f: line 1: key "a" is repeated`},
		{JSON5, `{1a: 1}`, `f: line 1: a key cannot start with '1'`},
		{JSON5, `{\u0031: 1}`, `f: line 1: the escape \u0031 is no character of a name`},
		{JSON5, `{a\x: 1}`, `f: line 1: a name holds \ only before u and four hex digits`},
		{JSON5, `['\1']`, `f: line 1: \1 is no escape of JSON5`},
		{JSON5, `['\01']`, `f: line 1: \0 is not followed by a digit`},
		{JSON5, `['\xg0']`, `f: line 1: \x takes two hex digits`},
		{JSON5, "['a\nb']", `f: line 1: a string does not hold a line end`},
		{JSON5, `[+]`, `f: line 1: a number cannot hold ']' here`},
		{JSON5, `[.]`, `f: line 1: a number cannot hold ']' here`},
		{JSON5, `[0x]`, `f: line 1: a number cannot be followed by 'x'`},
		{JSON5, `[Inf]`, `f: line 1: a number cannot hold 'I' here`},
		{JSON5, `[1..]`, `f: line 1: a number cannot be followed by '.'`},
	} {
		root := extracttest.Repo(t, map[string]string{"f": c.body})
		_, err := extracttest.Run(t, builder(c.dialect), Spec{File: "f", Path: "a"}, root)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s %q: error %v, want %q", c.dialect, c.body, err, c.want)
		}
	}
}

// The white space JSON5 adds, and a byte order mark, which JSONC takes at
// the start only. Planted breaks: a Zs character refused, the mark taken
// inside a JSONC file.
func TestWhiteSpace(t *testing.T) {
	for _, c := range []struct{ dialect, body, want string }{
		{JSONC, core.BOM + `{"a": 1}`, "1@1"},
		{JSON5, "\ufeff{\u00a0a\u2003:\v\f1\u2028}", "1@1"},
		{JSON5, "{a: 'x\u2028y'}", "x\u2028y@1"},
	} {
		root := extracttest.Repo(t, map[string]string{"f": c.body})
		vs, err := extracttest.Run(t, builder(c.dialect), Spec{File: "f", Path: "a"}, root)
		if err != nil || extracttest.Texts(vs) != c.want {
			t.Errorf("%s %q: got %v, %v, want %q", c.dialect, c.body, vs, err, c.want)
		}
	}
	root := extracttest.Repo(t, map[string]string{"f": `{"a": 1}` + core.BOM})
	_, err := run(t, Spec{File: "f", Path: "a"}, root)
	extracttest.WantError(t, err, `f: line 1: data after the top-level value`)
}

// Strings decode as encoding/json decodes them for the json reader: a
// surrogate pair is one character, half of one and a byte that is not
// UTF-8 are U+FFFD. Planted break: the low half read as a character of its
// own.
func TestStringsDecodeAsJSON(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": `{"a": "\ud83d\ude00", "b": "\ud83dx", "c": "\ud83d\u0041", "d": "\u00e9\/", "e": "` + "\xff" + `"}`})
	for path, want := range map[string]string{"a": "\U0001f600@1", "b": "\ufffdx@1", "c": "\ufffdA@1", "d": "\u00e9/@1", "e": "\ufffd@1"} {
		if got := extracttest.Texts(mustRun(t, Spec{File: "f", Path: path}, root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// The kind is the dialect for every file it reads, whatever the file's
// name: jsonc refuses a .json5 file's JSON5, so a rule that asked for
// JSONC is unknown and never true on it, and json5 reads a set of JSONC and
// JSON5 files alike. Planted breaks: jsonc choosing JSON5 for a name ending
// in .json5, the parse of one kind handed to the other.
func TestTheKindIsTheDialect(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.json": `{"v": "1"}`, "b.json5": "{v: '2'}", "c.jsonc": `{"v": "3", /* c */}`, "tsconfig.json": "// c\n{\"v\": '4'}",
	})
	_, err := run(t, Spec{File: "b.json5", Path: "v"}, root)
	extracttest.WantError(t, err, `b.json5: line 1: a key cannot start with 'v'`)
	_, err = run(t, Spec{Files: []string{"?.json*"}, Path: "v"}, root)
	extracttest.WantError(t, err, `b.json5: line 1: a key cannot start with 'v'`)
	vs, err := extracttest.Run(t, build5, Spec{Files: []string{"?.json*"}, Path: "v"}, root)
	if err != nil || extracttest.Texts(vs) != "1@1 2@1 3@1" || vs[1].At.File != "b.json5" {
		t.Errorf("json5: got %v, %v", vs, err)
	}
	vs, err = extracttest.Run(t, build5, Spec{File: "tsconfig.json", Path: "v"}, root)
	if err != nil || extracttest.Texts(vs) != "4@2" {
		t.Errorf("json5 on tsconfig.json: got %v, %v", vs, err)
	}
	_, err = run(t, Spec{File: "tsconfig.json", Path: "v"}, root)
	extracttest.WantError(t, err, `tsconfig.json: line 2: a value cannot start with '\''`)
	if got := Kind.Describe(Spec{File: "a.json"}).Label; got != "jsonc" {
		t.Errorf("label %q", got)
	}
	if got := JSON5Kind.Describe(Spec{File: "a.json"}).Label; got != "json5" {
		t.Errorf("label %q", got)
	}
}

// A path outside the repository does not load, as for every kind.
func TestAPathOutsideTheRepositoryDoesNotLoad(t *testing.T) {
	_, err := buildC(Spec{File: "../tsconfig.json", Path: "a"})
	extracttest.WantError(t, err, `jsonc: file "../tsconfig.json" is outside the repository`)
}

// The reader reads a value at exactly core.MaxDepth and refuses one a
// level deeper, which is the planted break.
func TestDepthBoundary(t *testing.T) {
	for _, d := range []string{JSONC, JSON5} {
		s := Spec{File: "f", Path: "$" + strings.Repeat("[0]", core.MaxDepth)}
		if got := extracttest.Texts(extracttest.MustRun(t, builder(d), s, extracttest.Repo(t, map[string]string{"f": extracttest.Nest(core.MaxDepth, "1")}))); got != "1@1" {
			t.Errorf("%s at depth %d: got %q", d, core.MaxDepth, got)
		}
		_, err := extracttest.Run(t, builder(d), s, extracttest.Repo(t, map[string]string{"f": extracttest.Nest(core.MaxDepth+1, "1")}))
		extracttest.WantError(t, err, "f: line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file")
	}
}

// A value that ends the file, as a root scalar does, is read to its last
// byte with no byte after it to look at, and one cut short there is
// refused. Each row is a planted break of a bound at the end of the file:
// a reader that looks one byte past the end panics, and one that stops a
// byte early reads a shorter number or refuses a whole one.
func TestAValueAtTheEndOfTheFile(t *testing.T) {
	for _, c := range []struct{ dialect, body, want string }{
		{JSONC, "0", "0"}, {JSONC, "7", "7"}, {JSONC, "-12", "-12"}, {JSONC, "1.25", "1.25"},
		{JSONC, "1e5", "1e5"}, {JSONC, "1E+5", "1E+5"}, {JSONC, "2e-10", "2e-10"}, {JSONC, "0.0", "0.0"},
		{JSONC, "true", "true"}, {JSONC, "null", "null"}, {JSONC, `"s"`, "s"}, {JSONC, "[1]", ""},
		{JSON5, "0", "0"}, {JSON5, "0X1f", "0X1f"}, {JSON5, "0xA", "0xA"}, {JSON5, "+.5", "+.5"},
		{JSON5, "5.", "5."}, {JSON5, ".5e1", ".5e1"}, {JSON5, "Infinity", "Infinity"}, {JSON5, "-NaN", "-NaN"},
		{JSON5, "'s'", "s"}, {JSON5, "10", "10"}, {JSON5, "1E-2", "1E-2"},
	} {
		tree, err := parse([]byte(c.body), c.dialect == JSON5)
		if err != nil {
			t.Errorf("%s %q: %v", c.dialect, c.body, err)
			continue
		}
		if c.want != "" && tree.Text != c.want {
			t.Errorf("%s %q: read %q", c.dialect, c.body, tree.Text)
		}
	}
	for _, c := range []struct{ dialect, body, want string }{
		{JSONC, "1e", "line 1: a number cannot hold the end of the file here"},
		{JSONC, "1e+", "line 1: a number cannot hold the end of the file here"},
		{JSONC, "1.", "line 1: a number cannot hold the end of the file here"},
		{JSONC, "-", "line 1: a number cannot hold the end of the file here"},
		{JSONC, "tru", "line 1: a value cannot start with 't'"},
		{JSONC, `"a\`, "line 1: the string that opens here is not closed"},
		{JSONC, `"\u00`, `line 1: \u takes four hex digits`},
		{JSONC, "[", "line 1: the file ends before the value is complete"},
		{JSONC, "{", "line 1: the file ends before the value is complete"},
		{JSONC, `{"a"`, "line 1: a key is followed by :, not the end of the file"},
		{JSONC, "/", "line 1: a value cannot start with '/'"},
		{JSONC, "1 /", "line 1: data after the top-level value"},
		{JSONC, "1 /*", "line 1: the comment that opens here is not closed"},
		{JSONC, "1 /* *", "line 1: the comment that opens here is not closed"},
		{JSON5, "0x", "line 1: a number cannot be followed by 'x'"},
		{JSON5, "+", "line 1: a number cannot hold the end of the file here"},
		{JSON5, ".", "line 1: a number cannot hold the end of the file here"},
		{JSON5, "1e", "line 1: a number cannot hold the end of the file here"},
		{JSON5, "Inf", "line 1: a number cannot hold 'I' here"},
		{JSON5, `'\x4`, `line 1: \x takes two hex digits`},
		{JSON5, `'\0`, "line 1: the string that opens here is not closed"},
		{JSON5, `{a`, "line 1: a key is followed by :, not the end of the file"},
		{JSON5, `{\u006`, `line 1: \u takes four hex digits`},
		{JSON5, `{\`, `line 1: a name holds \ only before u and four hex digits`},
		{JSON5, `{`, "line 1: the file ends before the value is complete"},
	} {
		_, err := parse([]byte(c.body), c.dialect == JSON5)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s %q: error %v, want %q", c.dialect, c.body, err, c.want)
		}
	}
}

// The bytes at the edge of each class the reader tests: the first and the
// last digit, hex letter and escape digit, LS and PS against the characters
// beside them, a line continuation over CRLF and a lone CR, a byte of 0x80
// in a string, and a high surrogate the file ends after. Each row is a
// planted break of one bound, a < read as <= or a range cut by one.
func TestTheEdgesOfEachClass(t *testing.T) {
	for _, c := range []struct{ dialect, body, want string }{
		{JSONC, "9", "9"}, {JSONC, "10", "10"}, {JSONC, "19", "19"}, {JSONC, "/**/1", "1"},
		{JSON5, "NaN", "NaN"}, {JSON5, "9", "9"},
		{JSONC, `"\uaAfF"`, "\uaaff"}, {JSONC, `"\u00C9\u00e9"`, "\u00c9\u00e9"}, {JSONC, `"\u0A0f"`, "\u0a0f"},
		{JSONC, "\"\x80\"", "\ufffd"}, {JSONC, "\"\x7f\"", "\x7f"},
		{JSON5, "// c\u2028 1", "1"}, {JSON5, "// c\u2029 1", "1"}, {JSON5, "[1 // c\u2027 ]\n]", ""},
		{JSON5, "'a\\\u2028b'", "ab"}, {JSON5, "'a\\\u2029b'", "ab"}, {JSON5, "'a\\\r\nb'", "ab"}, {JSON5, "'a\\\rb'", "ab"},
		{JSON5, "'a\\\nb'", "ab"}, {JSON5, "'a\\\u2027b'", "a\u2027b"}, {JSON5, "1 // c\xe2\x80", "1"},
		{JSON5, `'\x4F\xa0'`, "O\u00a0"},
	} {
		tree, err := parse([]byte(c.body), c.dialect == JSON5)
		if err != nil {
			t.Errorf("%s %q: %v", c.dialect, c.body, err)
			continue
		}
		if c.want != "" && tree.Text != c.want {
			t.Errorf("%s %q: read %q, want %q", c.dialect, c.body, tree.Text, c.want)
		}
	}
	for _, c := range []struct{ dialect, body, want string }{
		{JSONC, "// c\u2028 1", "line 1: the file holds no value"},
		{JSON5, "// c\xe2\x80", "line 1: the file holds no value"},
		{JSON5, "// c\u2027 1", "line 1: the file holds no value"},
		{JSON5, "// c\u2068 1", "line 1: the file holds no value"},
		{JSONC, `"\u0041`, "line 1: the string that opens here is not closed"},
		{JSONC, `"\u00g0"`, `line 1: \u takes four hex digits`},
		{JSONC, `"\u00G0"`, `line 1: \u takes four hex digits`},
		{JSONC, "\"\\u00\x600\"", `line 1: \u takes four hex digits`},
		{JSONC, `"\u00@0"`, `line 1: \u takes four hex digits`},
		{JSONC, `"\ud83d\`, "line 1: the string that opens here is not closed"},
		{JSON5, `'\00'`, `line 1: \0 is not followed by a digit`},
		{JSON5, `'\09'`, `line 1: \0 is not followed by a digit`},
		{JSON5, `'\9'`, `line 1: \9 is no escape of JSON5`},
	} {
		_, err := parse([]byte(c.body), c.dialect == JSON5)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s %q: error %v, want %q", c.dialect, c.body, err, c.want)
		}
	}
}

// An object nests to core.MaxDepth as an array does, and no deeper.
// Planted break: an object's member read one level up.
func TestObjectDepthBoundary(t *testing.T) {
	deep := func(n int) []byte {
		return []byte(strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n))
	}
	if _, err := ParseJSONC(deep(core.MaxDepth)); err != nil {
		t.Errorf("%d levels: %v", core.MaxDepth, err)
	}
	_, err := ParseJSONC(deep(core.MaxDepth + 1))
	extracttest.WantError(t, err, "line 1: nested more than 1000 levels deep")
}

// singleEscape gives each one character escape its character, \' and \v in
// JSON5 alone, and refuses any other. Only the rapid property reached most
// arms, by chance, so the coverage of singleEscape, and its CRAP score
// (scripts/crap), changed from run to run.
func TestSingleEscapeEachCharacter(t *testing.T) {
	for _, c := range []struct {
		c      byte
		json5  bool
		want   byte
		wantOK bool
	}{
		{'"', false, '"', true}, {'\\', false, '\\', true}, {'/', false, '/', true},
		{'b', false, '\b', true}, {'f', false, '\f', true}, {'n', false, '\n', true},
		{'r', false, '\r', true}, {'t', false, '\t', true},
		{'\'', true, '\'', true}, {'\'', false, '\'', false},
		{'v', true, '\v', true}, {'v', false, '\v', false},
		{'x', true, 0, false},
	} {
		if got, ok := singleEscape(c.c, c.json5); got != c.want || ok != c.wantOK {
			t.Errorf("singleEscape(%q, %v) = %q, %v; want %q, %v", c.c, c.json5, got, ok, c.want, c.wantOK)
		}
	}
}
