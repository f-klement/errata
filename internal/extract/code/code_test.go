package code

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// read is what the code extractor reads in src, a file named file, for the
// name (or, when constant is set, the const) name: the value as text@line,
// after a ~ when it is computed, "none" for no value, or "error: " and the
// message.
func read(t *testing.T, file, src, name string, constant bool) string {
	t.Helper()
	s := Spec{OneFile: core.OneFile{File: file}, Name: name}
	if constant {
		s.Name, s.Const = "", name
	}
	x, err := Build(s)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	vs, err := x.(extractor).FromData([]byte(src))
	if err != nil {
		return "error: " + err.Error()
	}
	if len(vs) == 0 {
		return "none"
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Text + "@" + strconv.Itoa(v.At.Line)
		if v.NotLiteral {
			out[i] = "~" + out[i]
		}
	}
	return strings.Join(out, ", ")
}

type readCase struct{ file, src, name, want string }

func readAll(t *testing.T, constant bool, cs []readCase) {
	t.Helper()
	for _, c := range cs {
		if got := read(t, c.file, c.src, c.name, constant); got != c.want {
			t.Errorf("%s %.40q, %s:\n got %s\nwant %s", c.file, c.src, c.name, got, c.want)
		}
	}
}

// goSrc is a Go file with the forms of constant and var the reader reads.
const goSrc = `package server

import "time"

// DefaultPort is the port the server listens on.
const DefaultPort = 8080

const (
	Version     = "1.4.2"
	Hex         = 0x1F
	Big         = 1 << 100
	Ratio       = 7 / 2
	Half        = 7 / 2.0
	Million     = 1e6
	Tiny        = 1.5e-7
	Letter      = 'a'
	Joined      = Version + "-rc1"
	Enabled     = !false
	Timeout     = 5 * time.Second
	Same        = DefaultPort == 8080
	Masked      = ^uint8(0)
	Neg         = -Hex
	Paren       = (Hex)
	Shifted     = Hex << 2 >> 1
	Bits        = Hex &^ 1 | 2 ^ 4 & 7 % 5
)

type Port int
type Mode string
type Level = Port

const (
	Debug Level = iota
	Info
	Warn
	_
	Error
)

const (
	Admin Port = 9000 + iota*10
	Metrics
)

const ModeProd Mode = "prod"

const (
	KB = 1 << (10 * (iota + 1))
	MB
	GB
)

const F32 float32 = 0.1
const F32sum = F32 + 0.2
const Typed int64 = 42

var Name = "errata"
var Count = 3
var Hosts = []string{"a", "b"}
var Env = os.Getenv("ENV")
var Unset string
var Changed = "a"
var Bumped = 1
var Pointed = 2
var Ranged = 3
var A, B = 1, "two"
var C, D = pair()

func init() {
	Changed = "b"
	Bumped++
	p := &Pointed
	_ = p
	for Ranged = range 3 {
	}
	Name := "local"
	_ = Name
}
`

// The Go reader reads each package level const and var with a literal
// value, resolves typed constants, iota blocks and constant expressions as
// the compiler does, and marks a value it cannot know not literal. Planted
// breaks: integer division by QUO; iota not counted per spec; a var the
// file assigns read as literal; a typed float not rounded.
func TestGo(t *testing.T) {
	readAll(t, false, []readCase{
		{"server.go", goSrc, "DefaultPort", "8080@6"},
		{"server.go", goSrc, "Version", "1.4.2@9"},
		{"server.go", goSrc, "Hex", "0x1F@10"},
		{"server.go", goSrc, "Big", "1267650600228229401496703205376@11"},
		{"server.go", goSrc, "Ratio", "3@12"},
		{"server.go", goSrc, "Half", "3.5@13"},
		{"server.go", goSrc, "Million", "1e6@14"},
		{"server.go", goSrc, "Tiny", "1.5e-7@15"},
		{"server.go", goSrc, "Letter", "a@16"},
		{"server.go", goSrc, "Joined", "1.4.2-rc1@17"},
		{"server.go", goSrc, "Enabled", "true@18"},
		{"server.go", goSrc, "Timeout", "~5 * time.Second@19"},
		{"server.go", goSrc, "Same", "true@20"},
		{"server.go", goSrc, "Masked", "255@21"},
		{"server.go", goSrc, "Neg", "-31@22"},
		{"server.go", goSrc, "Paren", "31@23"},
		{"server.go", goSrc, "Shifted", "62@24"},
		{"server.go", goSrc, "Bits", "26@25"},
		{"server.go", goSrc, "Debug", "0@33"},
		{"server.go", goSrc, "Info", "1@34"},
		{"server.go", goSrc, "Warn", "2@35"},
		{"server.go", goSrc, "Error", "4@37"},
		{"server.go", goSrc, "Admin", "9000@41"},
		{"server.go", goSrc, "Metrics", "9010@42"},
		{"server.go", goSrc, "ModeProd", "prod@45"},
		{"server.go", goSrc, "KB", "1024@48"},
		{"server.go", goSrc, "GB", "1073741824@50"},
		{"server.go", goSrc, "F32", "0.1@53"},
		{"server.go", goSrc, "F32sum", "0.3@54"},
		{"server.go", goSrc, "Typed", "42@55"},
		{"server.go", goSrc, "Name", "errata@57"},
		{"server.go", goSrc, "Count", "3@58"},
		{"server.go", goSrc, "Hosts", `~[]string{"a", "b"}@59`},
		{"server.go", goSrc, "Env", `~os.Getenv("ENV")@60`},
		{"server.go", goSrc, "Unset", "~Unset string@61"},
		{"server.go", goSrc, "Changed", `~"a"@62`},
		{"server.go", goSrc, "Bumped", "~1@63"},
		{"server.go", goSrc, "Pointed", "~2@64"},
		{"server.go", goSrc, "Ranged", "~3@65"},
		{"server.go", goSrc, "A", "1@66"},
		{"server.go", goSrc, "B", "two@66"},
		{"server.go", goSrc, "C", "~C, D = pair()@67"},
		{"server.go", goSrc, "Missing", "none"},
		{"server.go", goSrc, "init", "none"},
	})
}

// A constant the compiler refuses, or whose value errata cannot know, is
// not literal: one past its type's range, a division by zero, a cycle, a
// reference to a var or to a name the file does not declare, a call, a
// conversion to a type of another package or to string from a number, two
// types mixed, a name declared twice, and an imaginary number. Planted
// breaks: checked not called on a typed result; the cycle not seen.
func TestGoNotLiteral(t *testing.T) {
	for _, c := range []struct{ decl, want string }{
		{"const X uint8 = 256", "~256"},
		{"const X int8 = -129", "~-129"},
		{"const X = uint8(255) + 1", "~uint8(255) + 1"},
		{"const X = -uint(1)", "~-uint(1)"},
		{"const X = 1 / 0", "~1 / 0"},
		{"const X = 1 % 0", "~1 % 0"},
		{"const X = Y\nconst Y = X", "~Y"},
		{"var v = 1\nconst X = v", "~v"},
		{"const X = Other", "~Other"},
		{"const X = len(\"abc\")", `~len("abc")`},
		{"const X = time.Duration(5)", "~time.Duration(5)"},
		{"const X = string(65)", "~string(65)"},
		{"type P int\ntype Q int\nconst X = P(1) + Q(1)", "~P(1) + Q(1)"},
		{"const X = 1\nconst X = 2", "~2"},
		{"const X = 2i", "~2i"},
		{"const X = 1 << 513", "~1 << 513"},
		{"const X = 1 << 600 >> 600", "~1 << 600 >> 600"},
		{"const X = 2.5 << 1", "~2.5 << 1"},
		{"const X int = 2.5", "~2.5"},
		{"const X float32 = 1e39", "~1e39"},
		{"const X = \"a\" + 1", `~"a" + 1`},
		{"const X = \"a\" - \"b\"", `~"a" - "b"`},
		{"const X = true + true", "~true + true"},
		{"const X = 1.5 % 1", "~1.5 % 1"},
		{"const X = true < false", "~true < false"},
		{"const X = 1 && 2", "~1 && 2"},
		{"const X = -\"a\"", `~-"a"`},
		{"const X = !1", "~!1"},
		{"const X = ^1.5", "~^1.5"},
		{"const X = \"\\xff\"", `~"\xff"`},
		{"var X = 1 << 63", "~1 << 63"},
		{"var X complex128 = 1", "~1"},
		{"const X = iota\nvar Y = iota", "0"},
		{"var X = iota", "~iota"},
		{"const X, Y = 1", "~X, Y = 1"},
		{"type T []int\nconst X T = 1", "~1"},
		{"type T T\nconst X T = 1", "~1"},
		{"const X = 1 == \"a\"", `~1 == "a"`},
		{"const X = true\nconst Y = X && 1 > 0", "true"},
		{"const X = int(1, 2)", "~int(1, 2)"},
		{"const X = 1 << -1", "~1 << -1"},
		{"const X = \"a\" << 1", `~"a" << 1`},
		{"const X = 1 << \"a\"", `~1 << "a"`},
		{"const X = 'a' + 1", "98"},
		{"const X = 'a' << 1", "194"},
		{"const X = float32(1) / 3", "0.33333334"},
		{"const X = float32(16777216) + 1 - 16777216", "0"},
		{"const X = 1e300 * 1e300", "1e+600"},
		{"const X = 1e21", "1e21"},
		{"const X = 1e21 + 0", "1e+21"},
		{"const X = 2.0 * 3", "6"},
		{"const X = 0.1 + 0.2", "0.3"},
		{"const X = 3 > 2 == true", "true"},
		{"const X = \"b\" > \"a\"", "true"},
		{"const X = 9 &^ 3", "8"},
		{"const X float64 = 1", "1"},
		{"const X = float64(1) + 0.5", "1.5"},
		{"const X = int8(-128)", "-128"},
		{"const X = uint64(1<<64 - 1)", "18446744073709551615"},
		{"const X = ^int8(0)", "-1"},
		{"const X = ','", ","},
		{"const X = '\\n' + 0", "10"},
		{"const X byte = 'a'", "97"},
		{"const X rune = 'a'", "a"},
		{"const X int32 = 'a'", "a"},
		{"type Delim rune\nconst X Delim = ','", ","},
		{"type Delim int32\nconst X Delim = ','", ","},
		{"type Code uint32\nconst X Code = ','", "44"},
		{"const X int = '0'", "48"},
		{"const X float64 = 'a'", "97"},
		{"const X = float64('a')", "97"},
		{"const X = ('a')", "97"},
		{"var X = 'a'", "a"},
		{"const X float32 = 16777217", "16777217"},
		{"var X = 0.10", "0.10"},
		{"var X float32 = 0.10", "0.10"},
		{"const X float64 = 3.14159265358979323846", "3.14159265358979323846"},
		{"const X float32 = 16777216", "16777216"},
		{"const X float64 = 0.5", "0.5"},
		{"type T = int\nconst A T = 1\nconst X = A + int(2)", "3"},
		{"const A byte = 1\nconst X = A + uint8(2)", "3"},
		{"const A rune = 1\nconst X = A + int32(2)", "3"},
		{"type T int\nconst A T = 1\nconst X = A + int(2)", "~A + int(2)"},
		{"type P int\ntype Q int\nconst A P = 1\nconst X Q = A", "~A"},
		{"const X = 0 << 512", "0"},
		{"const X = 1 << 511", "6703903964971298549787012499102923063739682910296196688861780721860882015036773488400937149083451713845015929093243025426876941405973284973216824503042048"},
		{"const X = uint8(200) << 1", "~uint8(200) << 1"},
		{"const X = 2.0 << 1", "4"},
		{"const X = 1 + uint8(255)", "~1 + uint8(255)"},
		{"const X = 2.5 * float32(2)", "5"},
		{"const X = 1 + 'a'", "98"},
		{"const X = int(2.0)", "2"},
		{"type B bool\nconst X B = 1 < 2", "true"},
		{"const X = bool(\"a\")", `~bool("a")`},
		{"const X = string('a')", "~string('a')"},
		{"const X = string(\"a\")", "a"},
		{"const X = 0b" + strings.Repeat("_0", 512), "0b" + strings.Repeat("_0", 512)},
		{"const X = 0b0" + strings.Repeat("_0", 512), "~0b0" + strings.Repeat("_0", 512)},
	} {
		src := "package p\n\n" + c.decl + "\n"
		got := read(t, "p.go", src, "X", false)
		if i := strings.LastIndexByte(got, '@'); i >= 0 {
			got = got[:i]
		}
		if got != c.want {
			t.Errorf("%q: got %s, want %s", c.decl, got, c.want)
		}
	}
}

// const: reads only a name declared a constant: in Go a const, elsewhere
// one a Constants word of the row declares. Planted break: the const check
// left out of FromData.
func TestConst(t *testing.T) {
	readAll(t, true, []readCase{
		{"server.go", goSrc, "DefaultPort", "8080@6"},
		{"server.go", goSrc, "Name", "error: server.go: line 57: Name is not a constant: the file does not declare it one, or changes it after it does, so the program may change it; write name: to read it anyway"},
		{"version.ts", "export const VERSION = '2.0.1'\nexport let build = 3\n", "VERSION", "2.0.1@1"},
		{"version.ts", "export const VERSION = '2.0.1'\nexport let build = 3\n", "build", "error: version.ts: line 2: build is not a constant: the file does not declare it one, or changes it after it does, so the program may change it; write name: to read it anyway"},
		{"Version.java", "public class Version {\n  public static final String V = \"1\";\n  public static String W = \"2\";\n}\n", "Version.V", "1@2"},
		{"Version.java", "public class Version {\n  public static final String V = \"1\";\n  public static String W = \"2\";\n}\n", "Version.W", "error: Version.java: line 3: Version.W is not a constant: the file does not declare it one, or changes it after it does, so the program may change it; write name: to read it anyway"},
		{"Config.kt", "object Config {\n    const val PORT = 8080\n}\n", "Config.PORT", "8080@2"},
		{"config.php", "<?php\nconst NAME = 'x';\n", "NAME", "x@2"},
		{"Limits.java", "interface Limits {\n  int MAX = 10;\n}\n", "Limits.MAX", "10@2"},
		{"c.js", "class C {\n  static X = 1\n}\nC.X = 2\n", "C.X", "error: c.js: line 4: C.X is not a constant: the file does not declare it one, or changes it after it does, so the program may change it; write name: to read it anyway"},
		{"a.ts", "export class A {\n  readonly port = 8080\n  constructor(p: number) { this.port = p }\n}\n", "A.port", "error: a.ts: line 2: A.port is not a constant: the file does not declare it one, or changes it after it does, so the program may change it; write name: to read it anyway"},
	})
}

// Every other language reads through the literal reader: module level
// assignments, class attributes, exported constants and static final
// fields, with the values a program computes not literal. A name that holds
// a list or a class is an error. Planted break: ParseMembers replaced by
// Parse.
func TestOtherLanguages(t *testing.T) {
	readAll(t, false, []readCase{
		{"src/app/__init__.py", "__version__ = '3.1.0'\nPORT = int(os.environ['PORT'])\n", "__version__", "3.1.0@1"},
		{"src/app/__init__.py", "__version__ = '3.1.0'\nPORT = int(os.environ['PORT'])\n", "PORT", "~int(os.environ['PORT'])@2"},
		{"settings.py", "class Config:\n    DEBUG = False\n    HOSTS = ['a']\n", "Config.DEBUG", "False@2"},
		{"settings.py", "class Config:\n    DEBUG = False\n    HOSTS = ['a']\n", "Config.HOSTS", "error: settings.py: line 3: Config.HOSTS holds a sequence, not a value; name one of its members, or read it with the literal extractor's path"},
		{"settings.py", "class Config:\n    DEBUG = False\n", "Config", "error: settings.py: line 1: Config holds a mapping, not a value; name one of its members, or read it with the literal extractor's path"},
		{"settings.py", "X = 1\n", "X.Y", "none"},
		{"settings.py", "class C:\n    X = 1\n\nC.X = 2\n", "C.X", "~C.X = 2@4"},
		{"A.java", "class A {\n  static int X = 1;\n  static void f() { A.X = 2; }\n}\n", "A.X", "~class A {\n  static int X = 1;\n  static void f() { A.X = 2; }\n}@1"},
		{"constants.ts", "export const DEFAULT_PORT = 3000\nexport const URL = `http://x:${DEFAULT_PORT}`\n", "DEFAULT_PORT", "3000@1"},
		{"constants.ts", "export const DEFAULT_PORT = 3000\nexport const URL = `http://x:${DEFAULT_PORT}`\n", "URL", "~`http://x:${DEFAULT_PORT}`@2"},
		{"Version.java", "package a;\npublic final class Version {\n  public static final String VERSION = \"4.2\";\n}\n", "Version.VERSION", "4.2@3"},
		{"lib/app/version.rb", "module App\n  VERSION = '0.5.0'.freeze\nend\n", "App.VERSION", "0.5.0@2"},
		{"lib/app/version.rb", "module App\n  VERSION = '0.5.0'\nend\n", "App.VERSION", "0.5.0@2"},
		{"a.py", "X = [\n", "X", "error: a.py: line 1: a bracket opens here and never closes, so the literal reader lost its place and cannot tell values from code after it (docs/reference.md#literal)"},
	})
}

// A Go file go/parser refuses is refused at the line of its first error;
// an evaluation nested past MaxDepth and a string constant longer than the
// file are refused; a file past the node limit is refused. Planted breaks:
// the depth not checked; the string's length not checked.
func TestGoRefusals(t *testing.T) {
	readAll(t, false, []readCase{
		{"p.go", "package p\r\rconst X = (\n", "X", "error: p.go: line 3: expected ';', found 'const'; errata reads a Go file the compiler parses"},
		{"p.go", "const X = 1\n", "X", "error: p.go: line 1: expected 'package', found 'const'; errata reads a Go file the compiler parses"},
	})
	// The constant's value is evaluated one level in, and each parenthesis,
	// operator and call one more: 999 parentheses reach the limit.
	parens := func(n int) string { return strings.Repeat("(", n) + "1" + strings.Repeat(")", n) }
	if got := read(t, "p.go", "package p\n\nconst X = "+parens(core.MaxDepth-1)+"\n", "X", false); got != "1@3" {
		t.Errorf("an expression nested to the limit: %.200s", got)
	}
	for _, deep := range []string{
		parens(core.MaxDepth),
		strings.Repeat("- ", core.MaxDepth) + "1",
		"1" + strings.Repeat(" + 1", core.MaxDepth),
		strings.Repeat("1 + (", core.MaxDepth/2+1) + "1" + strings.Repeat(")", core.MaxDepth/2+1),
		strings.Repeat("int(", core.MaxDepth) + "1" + strings.Repeat(")", core.MaxDepth),
	} {
		if got := read(t, "p.go", "package p\n\nconst X = "+deep+"\n", "X", false); !strings.Contains(got, "p.go: line 3: nested more than 1000 levels deep") {
			t.Errorf("an expression nested past the limit, %.20s: %.200s", deep, got)
		}
	}
	// A string as long as the file is read; one byte more is refused.
	exact := "package p\nconst A = \"" + strings.Repeat("x", 39) + "\"\nconst B = A + A\n"
	if got := read(t, "p.go", exact, "B", false); got != strings.Repeat("x", 78)+"@3" || len(exact) != 78 {
		t.Errorf("a string as long as the file, %d bytes: %.200s", len(exact), got)
	}
	var chain strings.Builder
	chain.WriteString("package p\n\nconst A0 = \"ab\"\n")
	for i := 1; i <= 40; i++ {
		chain.WriteString("const A" + strconv.Itoa(i) + " = A" + strconv.Itoa(i-1) + " + A" + strconv.Itoa(i-1) + "\n")
	}
	if got := read(t, "p.go", chain.String(), "A12", false); !strings.Contains(got, "a string constant is longer than the file") {
		// Fatal, so that a reader that builds A12, 8 KiB, never builds A40.
		t.Fatalf("a string doubled 12 times: %.200s", got)
	}
	if got := read(t, "p.go", chain.String(), "A40", false); !strings.Contains(got, "a string constant is longer than the file") {
		t.Errorf("a string doubled 40 times: %.200s", got)
	}
	if got := read(t, "p.go", chain.String(), "A5", false); got != strings.Repeat("ab", 32)+"@8" {
		t.Errorf("a string doubled 5 times: %.200s", got)
	}
	// max_bytes 400 allows 50 tokens: package, p and the ; go/scanner puts at
	// the line's end, const, X, = and 1, and 21 pairs of + 1 and the last ;
	// make 50; 22 pairs make 52.
	limited, err := Build(Spec{OneFile: core.OneFile{File: "p.go", MaxBytes: 400}, Name: "X"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pairs int
		want  string
	}{{21, "22@2"}, {22, "error: p.go: the file holds more than 50 tokens, the limit for a max_bytes of 400 (one for each 8 bytes); set max_bytes: higher to read it"}} {
		src := "package p\nconst X = 1" + strings.Repeat(" + 1", c.pairs) + "\n"
		vs, err := limited.(extractor).FromData([]byte(src))
		got := "error: " + fmt.Sprint(err)
		if err == nil && len(vs) == 1 {
			got = vs[0].Text + "@" + strconv.Itoa(vs[0].At.Line)
		}
		if got != c.want {
			t.Errorf("%d pairs: got %s, want %s", c.pairs, got, c.want)
		}
	}
	x, err := Build(Spec{OneFile: core.OneFile{File: "p.go", MaxBytes: 300}, Name: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.(extractor).FromData([]byte(strings.Repeat(",", 200))); err == nil || !strings.Contains(err.Error(), "node") {
		t.Errorf("a file past the node limit: %v", err)
	}
}

// Build refuses a spec with no name, both name and const, a name that is
// no dotted list of names, a language it does not read, a dotted Go name,
// and const for a language that declares no constant.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{OneFile: core.OneFile{File: "a.go"}}, "code: name or const is required"},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Name: "X", Const: "X"}, "code: write name or const, not both"},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Name: "a..b"}, `code: name "a..b" is not a name, or names joined by dots`},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Const: "1x"}, `code: const "1x" is not a name`},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Name: "a b"}, `code: name "a b" is not a name`},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Name: "a."}, `code: name "a." is not a name`},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Name: "a.b"}, `code: name "a.b" has a dot, and a Go file's top level names have none`},
		{Spec{OneFile: core.OneFile{File: "a.c"}, Name: "X"}, "code: a.c: its name chooses no language errata reads code of; set language: to one of go, java, kotlin, javascript, typescript, php, groovy, scala, swift, python, ruby, elixir, lua"},
		{Spec{OneFile: core.OneFile{File: "a.py"}, Name: "X", Language: "c"}, `code: language "c" is not one errata reads code of`},
		{Spec{OneFile: core.OneFile{File: "a.py"}, Name: "X", Language: "cobol"}, `code: language "cobol" is not one`},
		{Spec{OneFile: core.OneFile{File: "a.py"}, Const: "X"}, "code: python declares no constant, so const: names nothing; write name: X"},
		{Spec{OneFile: core.OneFile{File: "/a.py"}, Name: "X"}, "code: "},
	} {
		_, err := Build(c.s)
		extracttest.WantError(t, err, c.want)
	}
	for _, name := range []string{"_", "$x", "é", "a1.b_2", "__version__", "a", "z", "A", "Z", "x0", "x9"} {
		if _, err := Build(Spec{OneFile: core.OneFile{File: "a.js"}, Name: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The bytes just outside each range a name's byte may take.
	for _, name := range []string{"`", "{", "@", "[", "\x7f", "x/", "x:", "0x"} {
		_, err := Build(Spec{OneFile: core.OneFile{File: "a.js"}, Name: name})
		extracttest.WantError(t, err, "is not a name")
	}
}

// Place and Resolve write out the language the file's name chooses.
func TestPlaceAndResolveWriteTheLanguage(t *testing.T) {
	if got := Place(Spec{OneFile: core.OneFile{File: "./internal/server.go", MaxBytes: 5}, Name: "X"}); got.Language != "go" || got.File != "internal/server.go" || got.MaxBytes != 0 {
		t.Errorf("Place: %+v", got)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "A.java"}, Name: "A.X"}); got.Language != "java" || got.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve: %+v", got)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "a.c"}, Name: "X"}); got.Language != "" {
		t.Errorf("Resolve of a.c: %+v", got)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "a.cfg"}, Name: "X", Language: "lua"}); got.Language != "lua" {
		t.Errorf("Resolve with a language: %+v", got)
	}
	if d := Describe(Spec{}); d.Label != "code" || !d.NotLiteral {
		t.Errorf("Describe: %+v", d)
	}
}

// The extractor reads its file from the repository.
func TestExtract(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"internal/server/server.go": "package server\n\nconst DefaultPort = 8080\n"})
	got := extracttest.Texts(extracttest.MustRun(t, Build, Spec{OneFile: core.OneFile{File: "internal/server/server.go"}, Const: "DefaultPort"}, root))
	if got != "8080@3" {
		t.Errorf("got %s", got)
	}
}

// A rune written as one literal is its character, a string, while it stays
// untyped, rune or int32; given another number type it is that number. A
// rule that reads the value as a number never gets the character. Planted
// break: scalar typing the character as a number.
func TestGoRuneTypes(t *testing.T) {
	for _, c := range []struct {
		decl string
		typ  core.ScalarType
		text string
	}{
		{"const X = '7'", core.TypeString, "7"},
		{"const X rune = ','", core.TypeString, ","},
		{"type Delim rune\nconst X Delim = ','", core.TypeString, ","},
		{"const X int = '0'", core.TypeNumber, "48"},
		{"const X float64 = 'a'", core.TypeNumber, "97"},
		{"const X = '7' + 0", core.TypeNumber, "55"},
	} {
		g, err := readGo("p.go", []byte("package p\n"+c.decl+"\n"), core.DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		n, _, _, err := g.value("X")
		if err != nil || n.Type != c.typ || n.Text != c.text {
			t.Errorf("%s: %+v, %v; want %v %s", c.decl, n, err, c.typ, c.text)
		}
	}
}
