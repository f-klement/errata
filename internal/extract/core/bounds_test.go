package core_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/json"
)

// An extractor returns maxValues values, and refuses one more, as the
// planted break. The yaml, json and toml extractors share values, so JSON
// stands for the three.
func TestExtractValueLimit(t *testing.T) {
	list := func(n int) string { return "[" + strings.Repeat("1,", n-1) + "1]" }
	for _, c := range []struct {
		file string
		body func(n int) string
		spec extract.Spec
	}{
		{"f.json", list, extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "[*]"}}},
		// (\d*) matches, empty, at each of the n positions of n-1 bytes.
		{"f.txt", func(n int) string { return strings.Repeat("a", n-1) }, extract.Spec{Regex: &extract.Regex{File: "f.txt", Pattern: `(\d*)`}}},
	} {
		if got := mustRun(t, c.spec, extracttest.Repo(t, map[string]string{c.file: c.body(core.MaxValues)})); len(got) != core.MaxValues {
			t.Errorf("%s: %d values, want %d", c.file, len(got), core.MaxValues)
		}
		_, err := run(t, c.spec, extracttest.Repo(t, map[string]string{c.file: c.body(core.MaxValues + 1)}))
		want := c.file + ": the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", c.file, err, want)
		}
	}
}

// Each format reads a file that can hold exactly its node limit, and refuses
// one more node, as the planted break. A max_bytes of 1000 nodes' worth keeps
// the files small; the limit scales with it.
func TestNodeLimit(t *testing.T) {
	items := func(head string, n int) string { return head + strings.Repeat("1,", n-1) + "1]\n" }
	for _, c := range []struct {
		file string
		f    *core.Format
		head string
		n    int // the items that fill the limit
		spec extract.Spec
	}{
		{"f.json", core.JSONFormat, "[", 498, extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "[0]"}}},
		{"f.yaml", core.YAMLFormat, "a: [", 496, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "a[0]"}}},
		{"f.toml", core.TOMLFormat, "a = [", 496, extract.Spec{TOML: &extract.TOML{File: "f.toml", Path: "a[0]"}}},
	} {
		spec := withMaxBytes(c.spec, 1000*c.f.BytesPerNode)
		if got := mustRun(t, spec, extracttest.Repo(t, map[string]string{c.file: items(c.head, c.n)})); extracttest.Texts(got) != "1@1" {
			t.Errorf("%s at the limit: got %s", c.file, extracttest.Texts(got))
		}
		_, err := run(t, spec, extracttest.Repo(t, map[string]string{c.file: items(c.head, c.n+1)}))
		want := c.file + ": the file can hold up to 1002 nodes, over the limit of 1000, one for every " +
			strconv.Itoa(int(c.f.BytesPerNode)) + " bytes of max_bytes; set max_bytes: higher to read a file this dense"
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

// A message quotes at most maxQuote bytes of file text, since a key, an
// anchor or a number can be as long as the file. Each body holds text of
// 4 KiB; the planted break is each message quoting all of it. A message
// still names its refusal, which the start of each want checks, and the
// part of the text it quotes.
func TestMessagesQuoteAShortPartOfTheFile(t *testing.T) {
	messagesQuoteAShortPart(t, 1<<12)
}

// The same at 64 KiB, which -race slows past make test's time.
func TestMessagesQuoteAShortPartOfALargeFile(t *testing.T) {
	slowOnly(t)
	messagesQuoteAShortPart(t, 1<<16)
}

// messagesQuoteAShortPart is TestMessagesQuoteAShortPartOfTheFile with
// text of n bytes, n over 1024, past which a YAML key needs ?.
func messagesQuoteAShortPart(t *testing.T, n int) {
	t.Helper()
	long := strings.Repeat("a", n)
	nul := strings.Repeat(`\0`, n)
	short := strings.Repeat("a", core.MaxQuote)
	for _, c := range []struct {
		file, body, want string
	}{
		// A key over 1024 characters needs ? in YAML.
		{"f.yaml", "? " + long + "\n: 1\n? " + long + "\n: 2\n", `f.yaml: line 3: key "` + short + `"... is repeated in one mapping`},
		{"f.yaml", "x: &k \"" + nul + "\"\nm:\n  ? \"" + nul + "\"\n  : 1\n  ? *k\n  : 2\n", `f.yaml: line 5: key "` + strings.Repeat(`\x00`, core.MaxQuote) + `"... is repeated in one mapping`},
		{"f.yaml", "a: &" + long + " [*" + long + "]\n", "f.yaml: line 1: alias *" + short + "... is inside the anchor it names"},
		{"f.yaml", "m: &" + long + " x\nn:\n  ? *" + long + "\n  : 1\n  k: &" + long + " y\n  ? *" + long + "\n  : 2\n", "f.yaml: line 6: alias key *" + short + "... is repeated in one mapping"},
		{"f.yaml", "a: *" + long + "\n", "f.yaml: yaml: unknown anchor '" + strings.Repeat("a", 4*core.MaxQuote-len("yaml: unknown anchor '")) + "..."},
		{"f.yaml", "a: !!int " + long + "\n", `f.yaml: line 1: "` + short + `"... is not a valid !!int`},
		{"f.json", `{"` + long + `": 1, "` + long + `": 2}`, `f.json: line 1: key "` + short + `"... is repeated`},
		{"f.toml", "a = 1" + strings.Repeat("1", n) + "\n", "f.toml: line 1: 1" + strings.Repeat("1", core.MaxQuote-1) + "... is not a valid integer: it does not fit in 64 bits"},
		{"f.toml", "a = 1" + strings.Repeat("1", n) + ".0\n", "f.toml: line 1: 1" + strings.Repeat("1", core.MaxQuote-1) + "... is not a valid float: it does not fit in 64 bits"},
		{"f.toml", long + " = 1\n" + long + " = 2\n", "f.toml: line 2: " + short + "... is already defined, as a value"},
		{"f.toml", "[" + long + "]\n[" + long + "]\n", "f.toml: line 2: " + short + "... is already defined, as a table"},
	} {
		spec := map[string]extract.Spec{
			"f.yaml": {YAML: &extract.YAML{File: c.file, Path: "a"}},
			"f.json": {JSON: &extract.JSON{File: c.file, Path: "a"}},
			"f.toml": {TOML: &extract.TOML{File: c.file, Path: "a"}},
		}[c.file]
		_, err := run(t, spec, extracttest.Repo(t, map[string]string{c.file: c.body}))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: error of %d bytes %.300q, want %q", c.file, len(fmt.Sprint(err)), fmt.Sprint(err), c.want)
		}
	}
}

// A message quotes at most maxQuote bytes of a path, a pattern, a file or a
// selector from the configuration too, since each can be as long as the
// configuration, and so do regexp's errors, which quote the pattern again.
// Each row holds text of 4 KiB; the planted breaks are the messages that
// quoted it whole. Each message must start with its want and stay under
// 400 bytes.
func TestMessagesQuoteAShortPartOfTheConfiguration(t *testing.T) {
	long := strings.Repeat("a", 1<<12)
	short := strings.Repeat("a", core.MaxQuote)
	for _, c := range []struct {
		spec       extract.Spec
		file, body string // read when Build accepts the spec
		want       string
	}{
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "['" + long + "']"}}, "f.json", `{"` + long + `": {}}`,
			`f.json: path "['` + short[:core.MaxQuote-2] + `"... selects a mapping at line 1, not a scalar; add [*] to take its members`},
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: long + "["}}, "", "",
			`json: path "` + short + `"...: column 4098: expected a quoted key, an index or * after [ but the path ends`},
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "a.1" + long}}, "", "",
			`json: path "a.1` + short[:core.MaxQuote-3] + `"...: column 3: a key after . cannot start with a digit; write [n] for an index, or ['1` + short[:core.MaxQuote-1] + `...'] for a key`},
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "a.-" + long}}, "", "",
			`json: path "a.-` + short[:core.MaxQuote-3] + `"...: column 3: a key after . cannot start with -; write ['-` + short[:core.MaxQuote-1] + `...']`},
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "a[" + strings.Repeat("9", 1<<12) + "]"}}, "", "",
			`json: path "a[` + strings.Repeat("9", core.MaxQuote-2) + `"...: column 3: index ` + strings.Repeat("9", core.MaxQuote) + `... is out of range`},
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "a[?@ == " + long + "]"}}, "", "",
			`json: path "a[?@ == ` + short[:core.MaxQuote-8] + `"...: column 9: expected @ or a literal in the filter but found ` + short + `...; quote a string`},
		{extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "a[?@ == " + long + "()]"}}, "", "",
			`json: path "a[?@ == ` + short[:core.MaxQuote-8] + `"...: column 9: function ` + short + `...() is not supported in a filter`},
		{extract.Spec{Regex: &extract.Regex{File: "f.txt", Pattern: "(" + long}}, "", "",
			`regex: pattern "(` + short[:core.MaxQuote-1] + `"...: error parsing regexp: missing closing ): ` + "`(aaa"},
		{extract.Spec{Regex: &extract.Regex{File: "f.txt", Pattern: "x" + long}}, "", "",
			`regex: pattern "x` + short[:core.MaxQuote-1] + `"... has no capture group; the extractor returns group 1`},
		{extract.Spec{Regex: &extract.Regex{File: "../" + long[:core.MaxFileName-3], Pattern: "(x)"}}, "", "",
			`regex: file "../` + short[:core.MaxQuote-3] + `"... is outside the repository`},
		{extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "a", Kind: long}}, "f.yaml", "a: 1\n",
			`f.yaml: no document has kind "` + short + `"... and name ""`},
	} {
		e, err := extract.Build(c.spec)
		if err == nil {
			_, err = e.Extract(extracttest.Repo(t, map[string]string{c.file: c.body}))
		}
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || len(err.Error()) > 400 {
			t.Errorf("error of %d bytes %.300q, want one under 400 that starts %q", len(fmt.Sprint(err)), fmt.Sprint(err), c.want)
		}
	}
}

// The values an extractor returns hold no more bytes in all than its
// max_bytes, so no file returns more text than its limit, however its
// aliases repeat a value or its decoding lengthens one. The planted breaks:
// one alias of a 1000 byte value more than the limit allows, and a JSON
// string of 2000 bytes of invalid UTF-8, which decodes to 6000.
func TestValuesHoldNoMoreThanMaxBytes(t *testing.T) {
	value := strings.Repeat("v", 1000)
	file := func(aliases int) string {
		return "s: &s " + value + "\nrefs: [" + strings.Repeat("*s, ", aliases-1) + "*s]\n"
	}
	root := extracttest.Repo(t, map[string]string{"four.yaml": file(4), "five.yaml": file(5)})
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "four.yaml", Path: "refs[*]", MaxBytes: 4000}}, root)
	if len(got) != 4 {
		t.Fatalf("4000 bytes of values at a limit of 4000: %d values", len(got))
	}
	_, err := run(t, extract.Spec{YAML: &extract.YAML{File: "five.yaml", Path: "refs[*]", MaxBytes: 4000}}, root)
	extracttest.WantError(t, err, "five.yaml: the values the path selects hold more than 4000 bytes in all, the extractor's max_bytes; aliases can repeat a value and decoding can lengthen one, so narrow the path, or set max_bytes: higher")
	root = extracttest.Repo(t, map[string]string{"ff.json": `{"a": "` + strings.Repeat("\xff", 2000) + `"}`})
	_, err = run(t, extract.Spec{JSON: &json.Spec{File: "ff.json", Path: "a", MaxBytes: 4000}}, root)
	extracttest.WantError(t, err, "ff.json: the values the path selects hold more than 4000 bytes in all, the extractor's max_bytes; aliases can repeat a value and decoding can lengthen one")
}
