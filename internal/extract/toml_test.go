package extract

import (
	"errors"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func tomlSpec(path string) Spec { return Spec{TOML: &TOML{File: "config.toml", Path: path}} }

// A runner config whose resource keys were written below the
// [runners.kubernetes.node_selector] header. TOML puts a key in the table of
// the last header above it, so the keys became node-selector labels, with no
// error, and no node carries those labels. At the path the author meant, the
// extractor finds nothing; the keys are under the header above them.
const runnerConfig = `concurrent = 4

[[runners]]
  name = "k3s"
  [runners.kubernetes]
    namespace = "gitlab-runner"
    pull_policy = "if-not-present"
    [runners.kubernetes.node_selector]
      "kubernetes.io/hostname" = "builder"
    cpu_limit = "2"
    memory_limit = "3Gi"
`

func TestTOMLKeyUnderTheWrongHeader(t *testing.T) {
	root := repo(t, map[string]string{"config.toml": runnerConfig})
	for path, want := range map[string]string{
		"runners[0].kubernetes.cpu_limit":                               "",
		"runners[0].kubernetes.memory_limit":                            "",
		"runners[0].kubernetes.node_selector.cpu_limit":                 "2@10",
		"runners[0].kubernetes.node_selector.memory_limit":              "3Gi@11",
		"runners[0].kubernetes.node_selector['kubernetes.io/hostname']": "builder@9",
		"runners[0].kubernetes.pull_policy":                             "if-not-present@7",
		"runners[*].name":                                               "k3s@4",
		"concurrent":                                                    "4@1",
	} {
		if got := texts(mustRun(t, tomlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// Each scalar keeps the text the file wrote.
func TestTOMLScalarsKeepTheirText(t *testing.T) {
	root := repo(t, map[string]string{"config.toml": `hex = 0x1F
big = 1_000
exp = 1e3
inf = -inf
yes = true
when = 1979-05-27T07:32:00Z
day = 1979-05-27
basic = "a\tb"
literal = 'C:\path'
multi = """
one
two"""
list = [
  1,
  "two",
]
inline = { x = 1, y.z = "deep" }
dotted.key = "d"
`})
	for path, want := range map[string]string{
		"hex":        "0x1F@1",
		"big":        "1_000@2",
		"exp":        "1e3@3",
		"inf":        "-inf@4",
		"yes":        "true@5",
		"when":       "1979-05-27T07:32:00Z@6",
		"day":        "1979-05-27@7",
		"basic":      "a\tb@8",
		"literal":    `C:\path@9`,
		"multi":      "one\ntwo@10",
		"list[*]":    "1@14 two@15",
		"inline.x":   "1@17",
		"inline.y.z": "deep@17",
		"dotted.key": "d@18",
	} {
		if got := texts(mustRun(t, tomlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestTOMLTables(t *testing.T) {
	root := repo(t, map[string]string{"config.toml": `[a.b]
x = 1
[a]
y = 2
[[p]]
n = "first"
[p.sub]
v = "s1"
[[p]]
n = "second"
[[p.list]]
v = "l1"
[[p.list]]
v = "l2"
`})
	for path, want := range map[string]string{
		"a.b.x":          "1@2",
		"a.y":            "2@4",
		"p[*].n":         "first@6 second@10",
		"p[0].sub.v":     "s1@8",
		"p[1].sub.v":     "",
		"p[1].list[*].v": "l1@12 l2@14",
	} {
		if got := texts(mustRun(t, tomlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
	_, err := run(t, tomlSpec("p"), root)
	wantError(t, err, `config.toml: path "p" selects a sequence at line 5, not a scalar`)
	_, err = run(t, tomlSpec("a"), root)
	wantError(t, err, `config.toml: path "a" selects a mapping at line 1, not a scalar`)
}

// What go-toml's decode refuses and its parser accepts, which the builder
// refuses itself. Each body is a planted break, and the test checks that
// go-toml refuses it too, so the builder is no stricter than the decode it
// replaces. The parser's own refusals come last.
func TestTOMLThatDoesNotDecodeIsAnError(t *testing.T) {
	for body, want := range map[string]string{
		"a = 1\na = 2\n":                   "line 2: a is already defined, as a value",
		"a = 1\na.b = 2\n":                 "line 2: a is already defined, as a value",
		"a = {x = 1}\na.y = 2\n":           "line 2: a is already defined, as a value",
		"a = {x = 1, x = 2}\n":             "line 1: x is already defined, as a value",
		"a = [{x = 1, x = 2}]\n":           "line 1: x is already defined, as a value",
		"[t]\n[t]\n":                       "line 2: t is already defined, as a table",
		"[a.b]\n[a]\n[a]\n":                "line 3: a is already defined, as a table",
		"a.b = 1\n[a]\n":                   "line 2: a is already defined, as a table by a dotted key",
		"[x]\na.b = 1\n[x.a]\n":            "line 3: a is already defined, as a table by a dotted key",
		"[a.b]\n[a]\nb.c = 1\n":            "line 3: b is already defined, as a table",
		"[[a]]\n[a]\n":                     "line 2: a is already defined, as an array of tables",
		"a = 1\n[a]\n":                     "line 2: a is already defined, as a value",
		"a = 1\n[a.b]\n":                   "line 2: a is already defined, as a value",
		"a = [1]\n[[a]]\n":                 "line 2: a is already defined, as a value",
		"[a]\n[[a]]\n":                     "line 2: a is already defined, as a table",
		"a = 1\n[[a.b]]\n":                 "line 2: a is already defined, as a value",
		"[[a]]\n[a.b]\n[a.b]\n":            "line 3: b is already defined, as a table",
		"a = 9223372036854775808\n":        "line 1: 9223372036854775808 is not a valid integer: it does not fit in 64 bits",
		"a = -9_223_372_036_854_775_809\n": "line 1: -9_223_372_036_854_775_809 is not a valid integer: it does not fit in 64 bits",
		"a = 0x8000000000000000\n":         "line 1: 0x8000000000000000 is not a valid integer: it does not fit in 64 bits",
		"a = 1e400\n":                      "line 1: 1e400 is not a valid float: it does not fit in 64 bits",
		"a = 2023-02-29\n":                 "line 1: 2023-02-29 is not a valid local date: impossible date",
		"a = 24:00:00\n":                   "line 1: 24:00:00 is not a valid local time: hour cannot be greater 23",
		"a = 2023-13-01T00:00:00\n":        "line 1: 2023-13-01T00:00:00 is not a valid local datetime: impossible date",
		"a = 2023-01-01T00:00:00+24:00\n":  "line 1: 2023-01-01T00:00:00+24:00 is not a valid datetime: the offset is not +hh:mm or -hh:mm",
		"a = 2023-01-01T00:00:00+01:60\n":  "line 1: 2023-01-01T00:00:00+01:60 is not a valid datetime: the offset is not +hh:mm or -hh:mm",
		"a = 2023-01-01T00:00Z01:00\n":     "line 1: 2023-01-01T00:00Z01:00 is not a valid datetime: the offset is not +hh:mm or -hh:mm",
		"a = 0000-01-01 00:00000Z00\n":     "line 1: 0000-01-01 00:00000Z00 is not a valid datetime: the offset is not +hh:mm or -hh:mm",
		"a = 2023-01-01T25:00:00Z\n":       "line 1: 2023-01-01T25:00:00Z is not a valid datetime: hour cannot be greater 23",
		"a = [\n":                          "toml: line 1: array is incomplete",
		"a = 1 b = 2\n":                    "toml: line 1: expected newline but got U+0062 'b'",
	} {
		root := repo(t, map[string]string{"config.toml": body})
		_, err := run(t, tomlSpec("a"), root)
		if err == nil || err.Error() != "config.toml: "+want {
			t.Errorf("%q: error %v, want %q", body, err, "config.toml: "+want)
		}
		var decoded map[string]any
		if toml.Unmarshal([]byte(body), &decoded) == nil {
			t.Errorf("%q: go-toml decodes it", body)
		}
	}
}

// What go-toml's decode accepts, the builder accepts too: tables that a
// header, a dotted key or an array of tables may extend, and numbers and
// dates at the edge of their range.
func TestTOMLThatDecodesIsRead(t *testing.T) {
	for _, c := range []struct{ body, path, want string }{
		{"[a.b]\n[a]\nx = 1\n", "a.x", "1@3"},
		{"a.b = 1\na.c = 2\n", "a.c", "2@2"},
		{"a.b = 1\n[a.c]\nx = 1\n", "a.c.x", "1@3"},
		{"[x]\na.b = 1\n[x.a.c]\nd = 2\n", "x.a.c.d", "2@4"},
		{"[[a]]\n[a.b]\nx = 1\n[[a]]\n[a.b]\nx = 2\n", "a[*].b.x", "1@3 2@6"},
		{"[[a]]\n[[a.b]]\n[[a.b]]\nx = 1\n", "a[0].b[1].x", "1@4"},
		{"a = 9223372036854775807\nb = -9223372036854775808\n", "b", "-9223372036854775808@2"},
		{"a = 0x7fff_ffff_ffff_ffff\n", "a", "0x7fff_ffff_ffff_ffff@1"},
		{"a = 0o777\nb = 0b1010\n", "b", "0b1010@2"},
		{"a = 1.7976931348623157e308\nb = -nan\nc = +inf\n", "a", "1.7976931348623157e308@1"},
		{"a = 2024-02-29\nb = 23:59:59.999\n", "a", "2024-02-29@1"},
		{"a = 2024-02-29T23:59:59-23:59\nb = 1979-05-27 07:32:00z\n", "b", "1979-05-27 07:32:00z@2"},
	} {
		got, err := run(t, tomlSpec(c.path), repo(t, map[string]string{"config.toml": c.body}))
		if err != nil || texts(got) != c.want {
			t.Errorf("%q: got %s, %v, want %s", c.body, texts(got), err, c.want)
		}
		var decoded map[string]any
		if err := toml.Unmarshal([]byte(c.body), &decoded); err != nil {
			t.Errorf("%q: go-toml refuses it: %v", c.body, err)
		}
	}
}

// keys returns n dotted key parts.
func keys(n int) string { return strings.TrimSuffix(strings.Repeat("a.", n), ".") }

// A dotted key and a table header count one level of depth for each part,
// and an array table one more for its element. Each case reaches exactly
// maxDepth; the planted break is one more part.
func TestTOMLDepthCountsKeyParts(t *testing.T) {
	for _, c := range []struct{ body, path, want, deeper, refused string }{
		{keys(maxDepth) + " = 1\n", keys(maxDepth), "1@1", keys(maxDepth+1) + " = 1\n", "line 1"},
		{"[" + keys(maxDepth-1) + "]\nb = 1\n", keys(maxDepth-1) + ".b", "1@2", "[" + keys(maxDepth) + "]\nb = 1\n", "line 2"},
		{"[[" + keys(maxDepth-2) + "]]\nb = 1\n", keys(maxDepth-2) + "[0].b", "1@2", "[[" + keys(maxDepth-1) + "]]\nb = 1\n", "line 2"},
		{"[" + keys(maxDepth-2) + "]\nb = {c = 1}\n", keys(maxDepth-2) + ".b.c", "1@2", "[" + keys(maxDepth-1) + "]\nb = {c = 1}\n", "line 2"},
	} {
		got, err := run(t, tomlSpec(c.path), repo(t, map[string]string{"config.toml": c.body}))
		if err != nil || texts(got) != c.want {
			t.Errorf("%.40s: got %s, %v", c.body, texts(got), err)
		}
		_, err = run(t, tomlSpec(c.path), repo(t, map[string]string{"config.toml": c.deeper}))
		wantError(t, err, "config.toml: "+c.refused+": nested more than 1000 levels deep, the limit")
	}
	// A header of maxDepth parts names a table at maxDepth, and that is read;
	// one more part is refused. An array table's element is one level below
	// its header.
	if _, err := parseTOML([]byte("[" + keys(maxDepth) + "]\n")); err != nil {
		t.Errorf("header at maxDepth: %v", err)
	}
	for _, body := range []string{"[" + keys(maxDepth+1) + "]\n", "[[" + keys(maxDepth) + "]]\n"} {
		_, err := parseTOML([]byte(body))
		wantError(t, err, "line 1: nested more than 1000 levels deep")
	}
	if _, err := parseTOML([]byte("[[" + keys(maxDepth-1) + "]]\n")); err != nil {
		t.Errorf("array table with its element at maxDepth: %v", err)
	}
}

// parseError's guard at off == b.size, the edge of `off > b.size`, is
// unreachable while go-toml widens an empty Highlight at the end of the
// input to the last byte: off reaches b.size only for an empty Highlight at
// the end of the buffer's capacity.
// This pins that widening for the errors that end at the end of the input,
// each read from a buffer with no spare capacity, so a go-toml that stops
// widening fails here before the guard can matter.
func TestGoTOMLWidensAnErrorAtTheEnd(t *testing.T) {
	for _, body := range []string{
		"a", "a =", "a = ", "a = [1", "a = [1,", "a = {b = 1", "[a", "[[a]", "a.", `a = "x`,
		"a = '''x", "a = 0x", "a = 1e", "a = tru", "a = 1 #\x00",
	} {
		data := []byte(body)[:len(body):len(body)]
		var p unstable.Parser
		p.Reset(data)
		for p.NextExpression() {
		}
		var perr *unstable.ParserError
		if !errors.As(p.Error(), &perr) {
			t.Errorf("%q: go-toml gives %v, want a ParserError", body, p.Error())
			continue
		}
		if len(perr.Highlight) == 0 || cap(perr.Highlight) == 0 {
			t.Errorf("%q: go-toml highlights %q with capacity %d; parseError's guard counts on go-toml widening an empty highlight at the end", body, perr.Highlight, cap(perr.Highlight))
		}
	}
}

// Were an empty Highlight at the end of the buffer to reach parseError, it
// names the last line, as any other offset does.
func TestTOMLParseErrorAtTheEndOfTheBuffer(t *testing.T) {
	data := []byte("a = 1\nb =")[:9:9]
	b := &tomlBuilder{lines: lineIndex(data), size: cap(data)}
	err := b.parseError(&unstable.ParserError{Highlight: data[9:], Message: "planted"})
	if want := "toml: line 2: planted"; err == nil || err.Error() != want {
		t.Fatalf("parseError = %v, want %q", err, want)
	}
}
