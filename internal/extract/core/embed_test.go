package core_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// steps is embedded steps from format and path pairs.
func steps(pairs ...string) []core.Embedded {
	var out []core.Embedded
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, core.Embedded{Format: pairs[i], Path: pairs[i+1]})
	}
	return out
}

// Each format reads a document in a string of each format, and a value is at
// the file's line: the line it stands at when every string on its way is a
// YAML literal block scalar, the line its string starts at otherwise. The
// planted breaks: into ignoring Block, which put the TOML value at line 3,
// where its block scalar starts; into keeping the offset under a string
// that is not a block (folded, quoted, JSON), which put those values below
// their strings; and isBlock without its LS and PS test, which put b at
// line 3, where the TOML document's line 2 is not.
func TestEmbeddedReadsEachFormat(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"f.yaml": "kind: ConfigMap\n" +
			"data:\n" +
			"  config.toml: |\n" +
			"    [s3_api]\n" +
			"    s3_region = \"garage\"\n" +
			"  tenants: '[{\"name\": \"ci\"}, {\"name\": \"logs\"}]'\n" +
			"  nested: |\n" +
			"    inner: |\n" +
			"      x = 1\n" +
			"      y = 2\n" +
			"  folded: >\n" +
			"    a: 1\n" +
			"  quoted: \"a: 1\\nb: 2\"\n" +
			"  noted: |  # a comment\n" +
			"\n" +
			"    x = 1\n",
		"ls.yaml":  "s: |\n  a = \"x\u2028  \"\n  b = 2\n",
		"ps.yaml":  "s: |\n  a = \"x\u2029  \"\n  b = 2\n",
		"f.json":   `{"cfg": "a: |\n  x = 1\n", "n": 1}`,
		"f.toml":   "name = \"x\"\ncfg = '''\n{\"a\": [1, 2]}\n'''\n",
		"two.yaml": "a: |\n  b: |\n    {\"c\": [3]}\n",
	})
	yaml := func(file, path string, e ...string) extract.Spec {
		return extract.Spec{YAML: &extract.YAML{File: file, Path: path, Embedded: steps(e...)}}
	}
	for _, c := range []struct {
		name string
		spec extract.Spec
		want string
	}{
		{"toml in a block", yaml("f.yaml", "data['config.toml']", "toml", "s3_api.s3_region"), "garage@5"},
		{"json in a quoted string", yaml("f.yaml", "data.tenants", "json", "[*].name"), "ci@6 logs@6"},
		{"a block in a block", yaml("f.yaml", "data.nested", "yaml", "inner", "toml", "y"), "2@10"},
		{"folded", yaml("f.yaml", "data.folded", "yaml", "a"), "1@11"},
		{"quoted over lines", yaml("f.yaml", "data.quoted", "yaml", "b"), "2@13"},
		{"a block after a comment and a blank line", yaml("f.yaml", "data.noted", "toml", "x"), "1@16"},
		{"a block that holds an LS", yaml("ls.yaml", "s", "toml", "b"), "2@1"},
		{"a block that holds a PS", yaml("ps.yaml", "s", "toml", "b"), "2@1"},
		{"a block in a JSON string", extract.Spec{JSON: &extract.JSON{File: "f.json", Path: "cfg", Embedded: steps("yaml", "a", "toml", "x")}}, "1@1"},
		{"json in a TOML string", extract.Spec{TOML: &extract.TOML{File: "f.toml", Path: "cfg", Embedded: steps("json", "a[*]")}}, "1@2 2@2"},
		{"json in a block in a block", yaml("two.yaml", "a", "yaml", "b", "json", "c[0]"), "3@3"},
		{"no steps", yaml("f.yaml", "kind"), "ConfigMap@1"},
	} {
		if got := extracttest.Texts(mustRun(t, c.spec, root)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A block scalar's values are at the lines they stand on when an anchor, a
// tag, a comment or a blank line stands between the scalar's event and its
// |, which yaml.v3 gives the line of the anchor or the tag, and an alias of
// such an anchor reports the anchor's lines. A UTF-8 byte order mark takes
// no column. In a UTF-16 file the | is not looked for, so the value is at
// the line the string starts. In a file whose lines before the block end in
// a CR or an LS, which yaml.v3 counts and errata does not, no value lands
// past the file's last line. The planted breaks: Block set to the event's
// line, which put y at line 4; the cursor not skipping a UTF-8 byte order
// mark, or not set off for UTF-16, which read the | in s| as the block's;
// and the clamp to the last line removed, which put y at line 5 of 2.
func TestEmbeddedBlockLinesFromTheIndicator(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"anchor.yaml":  "a: 0\ns: &a\n  |\n  x = 1\n  y = 2\nt: *a\n",
		"tag.yaml":     "a: 0\ns: !!str\n  |\n  x = 1\n  y = 2\n",
		"both.yaml":    "\u00e9: &a !!str # a note\n\n  |\n  x = 1\n",
		"bom.yaml":     "\xef\xbb\xbfs: &a |\n  x = 1\n",
		"bom2.yaml":    "\xef\xbb\xbfa: 0\nb: 1\ns: &a\n  |\n  x = 1\n",
		"bomroot.yaml": "\xef\xbb\xbf|\n  x = 1\n",
		"utf16.yaml":   extracttest.UTF16LE("a: 0\ns: &a\n  |\n  x = 1\n"),
		"utf16b.yaml":  extracttest.UTF16LE("s|: |\n  x = 1\n"),
		"cr.yaml":      "a: 0\rb: 1\rs: |\n  y = 2",
		"ls.yaml":      "a: 0\u2028b: 1\u2028s: |\n  y = 2",
		"crs.yaml":     "a: 0\rs: |\r  x = 1\r  y = 2\r",
	})
	for _, c := range []struct{ file, path, key, want string }{
		{"anchor.yaml", "s", "y", "2@5"},
		{"anchor.yaml", "t", "y", "2@5"},
		{"tag.yaml", "s", "y", "2@5"},
		{"both.yaml", "['\u00e9']", "x", "1@4"},
		{"bom.yaml", "s", "x", "1@2"},
		{"bom2.yaml", "s", "x", "1@5"},
		{"bomroot.yaml", "$", "x", "1@2"},
		{"utf16.yaml", "s", "x", "1@2"},
		// A UTF-16 file whose key holds a |. errata does not scan a UTF-16
		// file for the |, so its block is reported at the line where the
		// string starts, 1. Without the UTF-16 check, yaml.v3's column read
		// in the file's bytes would land on the key's |.
		{"utf16b.yaml", "['s|']", "x", "1@1"},
		{"cr.yaml", "s", "y", "2@2"},
		{"ls.yaml", "s", "y", "2@2"},
		{"crs.yaml", "s", "y", "2@1"},
	} {
		got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: c.file, Path: c.path, Embedded: steps("toml", c.key)}}, root)
		if extracttest.Texts(got) != c.want {
			t.Errorf("%s %s: got %q, want %q", c.file, c.path, extracttest.Texts(got), c.want)
		}
	}
}

// An empty JSON string, or one of white space, is refused as a string that
// holds no JSON value, not as a file that ends early. The planted break:
// the test removed, which gave the file's message.
func TestEmbeddedEmptyJSONIsRefused(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.json": `{"s": "", "w": " \n\t"}`})
	for _, path := range []string{"s", "w"} {
		_, err := run(t, extract.Spec{JSON: &extract.JSON{File: "f.json", Path: path, Embedded: steps("json", "$")}}, root)
		extracttest.WantError(t, err, `f.json: path "`+path+`" then json path "$": the json document in the string at line 1: the string holds nothing but white space, and no JSON value`)
	}
}

// An empty YAML string holds no document, so the step selects nothing,
// which allow_empty decides, as for an empty YAML file.
func TestEmbeddedEmptyYAMLSelectsNothing(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": "s: \"\"\n"})
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "s", Embedded: steps("yaml", "a")}}, root)
	if len(got) != 0 {
		t.Fatalf("got %q, want nothing", extracttest.Texts(got))
	}
}

// Steps that cannot be read do not load, each with its reason: past
// MaxEmbedded, a format left out or one errata does not read, and a path
// left out or that does not parse. MaxEmbedded steps load. The planted
// breaks: the limit off by one either way, and each refusal removed.
func TestEmbeddedRefusesStepsItCannotRead(t *testing.T) {
	four := steps("yaml", "a", "yaml", "a", "yaml", "a", "yaml", "a")
	if _, err := extract.Build(extract.Spec{YAML: &extract.YAML{File: "f", Path: "a", Embedded: four}}); err != nil {
		t.Fatalf("%d steps: %v", core.MaxEmbedded, err)
	}
	for _, c := range []struct {
		spec extract.Spec
		want string
	}{
		{extract.Spec{YAML: &extract.YAML{File: "f", Path: "a", Embedded: append(four, core.Embedded{Format: "yaml", Path: "a"})}},
			"yaml: embedded takes 5 steps, over the limit of 4"},
		{extract.Spec{YAML: &extract.YAML{File: "f", Path: "a", Embedded: steps("", "a")}},
			"yaml: embedded[0]: format is required; write yaml, json or toml"},
		{extract.Spec{JSON: &extract.JSON{File: "f", Path: "a", Embedded: steps("toml", "a", "xml", "a")}},
			`json: embedded[1]: format "xml" is not one errata reads; write yaml, json or toml`},
		{extract.Spec{TOML: &extract.TOML{File: "f", Path: "a", Embedded: steps("json", "")}},
			"toml: embedded[0]: path is required"},
		{extract.Spec{TOML: &extract.TOML{File: "f", Path: "a", Embedded: steps("json", "a..b")}},
			`toml: embedded[0]: path "a..b": column 3: recursive descent`},
	} {
		_, err := extract.Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
}

// A value a step reads that is not a string, a string that does not parse
// in the step's format, a YAML string of two documents and a step's path
// that selects a container make the extractor fail, each message naming
// every path of the chain and the file's line of the string. The planted
// breaks: the string test removed, which parsed 8080 as TOML; each describe
// case wrong; and the document count test removed, which read two.
func TestEmbeddedRefusesWhatItCannotRead(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": "n: 8080\n" +
		"b: true\n" +
		"z: null\n" +
		"m: {a: 1}\n" +
		"s: [1]\n" +
		"bad: \"a = = 1\"\n" +
		"two: \"a: 1\\n---\\nb: 2\\n\"\n" +
		"str: !!str 1\n" +
		"inner: \"a: {b: 1}\"\n" +
		"deep: |\n" +
		"  a: |\n" +
		"    x = = 1\n"})
	for _, c := range []struct {
		path string
		e    []core.Embedded
		want string
	}{
		{"n", steps("toml", "x"), `f.yaml: path "n" then toml path "x": the value at line 1 is a number, not a string that holds a toml document`},
		{"b", steps("toml", "x"), "the value at line 2 is a bool, not a string"},
		{"z", steps("json", "x"), "the value at line 3 is null, not a string that holds a json document"},
		{"m", steps("yaml", "x"), "the value at line 4 is a mapping, not a string"},
		{"s", steps("yaml", "x"), "the value at line 5 is a sequence, not a string"},
		{"bad", steps("toml", "x"), `f.yaml: path "bad" then toml path "x": the toml document in the string at line 6: toml: line 1:`},
		{"two", steps("yaml", "a"), "the yaml document in the string at line 7: holds 2 documents; an embedded YAML document is read whole, so it holds one"},
		{"inner", steps("yaml", "a"), `f.yaml: path "inner" then yaml path "a" selects a mapping at line 9, not a scalar`},
		{"deep", steps("yaml", "a", "toml", "x"), `f.yaml: path "deep" then yaml path "a" then toml path "x": the toml document in the string at line 11: toml: line 1:`},
	} {
		_, err := run(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: c.path, Embedded: c.e}}, root)
		extracttest.WantError(t, err, c.want)
	}
	// A string a tag makes one holds a document as any string does.
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "str", Embedded: steps("json", "$")}}, root)
	if extracttest.Texts(got) != "1@8" {
		t.Fatalf("got %q, want 1@8", extracttest.Texts(got))
	}
}

// The documents in strings count against max_bytes in all: their bytes, an
// alias repeating one string counting each time, and the nodes they can
// hold, two documents each under the limit counting together. Each count
// may reach max_bytes: 8 strings of 43 bytes read at 344, and two documents
// of 124 nodes of 8 bytes at 1984. The planted breaks: each count reset for
// each string, each test removed, and each test refusing its limit itself.
func TestEmbeddedDocumentsCountAgainstMaxBytes(t *testing.T) {
	long := `"x = 1 #` + strings.Repeat("a", 36) + `"`
	ones := `"[` + strings.Repeat("1,", 59) + `1]"`
	root := extracttest.Repo(t, map[string]string{
		"bytes.yaml": "s: &s " + long + "\nl: [*s, *s, *s, *s, *s, *s, *s, *s]\n",
		"nodes.toml": "a = [" + ones + ", " + ones + "]\n",
	})
	_, err := run(t, extract.Spec{YAML: &extract.YAML{File: "bytes.yaml", Path: "l[*]", Embedded: steps("toml", "x"), MaxBytes: 300}}, root)
	extracttest.WantError(t, err, `bytes.yaml: path "l[*]" then toml path "x": the documents in strings hold more than 300 bytes in all, the extractor's max_bytes`)
	_, err = run(t, extract.Spec{YAML: &extract.YAML{File: "bytes.yaml", Path: "l[*]", Embedded: steps("toml", "x"), MaxBytes: 343}}, root)
	extracttest.WantError(t, err, "the documents in strings hold more than 343 bytes in all")
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "bytes.yaml", Path: "l[*]", Embedded: steps("toml", "x"), MaxBytes: 344}}, root)
	if len(got) != 8 {
		t.Fatalf("at 344 bytes: got %q, want 8 values", extracttest.Texts(got))
	}
	_, err = run(t, extract.Spec{TOML: &extract.TOML{File: "nodes.toml", Path: "a[*]", Embedded: steps("json", "[0]"), MaxBytes: 1000}}, root)
	extracttest.WantError(t, err, "the documents in strings can hold more nodes in all than max_bytes allows, one for every 8 bytes of it in json")
	got = mustRun(t, extract.Spec{TOML: &extract.TOML{File: "nodes.toml", Path: "a[0]", Embedded: steps("json", "[0]"), MaxBytes: 1000}}, root)
	if extracttest.Texts(got) != "1@1" {
		t.Fatalf("one document: got %q, want 1@1", extracttest.Texts(got))
	}
	_, err = run(t, extract.Spec{TOML: &extract.TOML{File: "nodes.toml", Path: "a[*]", Embedded: steps("json", "[0]"), MaxBytes: 1983}}, root)
	extracttest.WantError(t, err, "the documents in strings can hold more nodes in all than max_bytes allows")
	got = mustRun(t, extract.Spec{TOML: &extract.TOML{File: "nodes.toml", Path: "a[*]", Embedded: steps("json", "[0]"), MaxBytes: 1984}}, root)
	if extracttest.Texts(got) != "1@1 1@1" {
		t.Fatalf("two documents at 1984 bytes: got %q, want 1@1 1@1", extracttest.Texts(got))
	}
}

// What YAML aliases and merge keys add counts over the file and the YAML
// documents in its strings together: the file's aliases reach the limit and
// one alias in a string passes it, and the refusal speaks of no set. The
// planted breaks: the documents' count started at zero, which read y; the
// set's note given to one file.
func TestEmbeddedAliasesCountWithTheFile(t *testing.T) {
	items := strings.Repeat("0, ", core.MaxExpanded/1000-2) + "0"
	aliases := strings.Repeat("*a, ", 999) + "*a"
	root := extracttest.Repo(t, map[string]string{
		"f.yaml": "a: &a [" + items + "]\nb: [" + aliases + "]\ns: \"x: &x 1\\ny: *x\\n\"\n",
		"g.yaml": "s: \"x: &x 1\\ny: *x\\n\"\n",
	})
	_, err := run(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "s", Embedded: steps("yaml", "y")}}, root)
	extracttest.WantError(t, err, "the yaml document in the string at line 3: line 2: aliases and merge keys expand to more than 1000000 nodes")
	if strings.Contains(err.Error(), "files of a set") {
		t.Errorf("one file's refusal speaks of a set: %v", err)
	}
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "g.yaml", Path: "s", Embedded: steps("yaml", "y")}}, root)
	if extracttest.Texts(got) != "1@1" {
		t.Fatalf("got %q, want 1@1", extracttest.Texts(got))
	}
}

// The filters of an extractor's paths share one budget over every
// document: two strings each under MaxFilterBytes pass it together. The
// planted break: a meter for each evaluation, which selected nothing and
// returned no error.
func TestEmbeddedFiltersShareTheBudget(t *testing.T) {
	lit := "'" + strings.Repeat("L", core.MaxLiteral) + "'"
	terms := make([]string, core.MaxComparisons)
	for i := range terms {
		terms[i] = "@ == " + lit
	}
	perMember := int64(core.MaxComparisons * (core.StepBytes + core.MaxLiteral))
	members := core.MaxFilterBytes/perMember/2 + 1 // two strings pass the budget, one does not
	doc := `"[` + strings.Repeat("0,", int(members)-1) + `0]"`
	root := extracttest.Repo(t, map[string]string{"f.yaml": "l: [" + doc + ", " + doc + "]\n"})
	filter := "[?" + strings.Join(terms, " && ") + "]"
	_, err := run(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "l[*]", Embedded: steps("json", filter)}}, root)
	extracttest.WantError(t, err, "its filters would hash and compare more than")
	extracttest.WantError(t, err, `f.yaml: path "l[*]" then json path "[?@ == `)
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "l[0]", Embedded: steps("json", filter)}}, root)
	if len(got) != 0 {
		t.Fatalf("one string: got %d values, want none", len(got))
	}
}

// The values of every document count against MaxValues together, and may
// reach it. The planted breaks: the count for each string alone, which
// returned 120000 values, and the test refusing MaxValues itself.
func TestEmbeddedValuesCountTogether(t *testing.T) {
	half := core.MaxValues*6/10 - 1
	doc := `"[` + strings.Repeat("0,", half) + `0]"`
	exact := `"[` + strings.Repeat("0,", core.MaxValues/2-1) + `0]"`
	root := extracttest.Repo(t, map[string]string{
		"f.yaml":     "l: [" + doc + ", " + doc + "]\n",
		"exact.yaml": "l: [" + exact + ", " + exact + "]\n",
	})
	if got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "exact.yaml", Path: "l[*]", Embedded: steps("json", "[*]")}}, root); len(got) != core.MaxValues {
		t.Fatalf("two strings at the limit: got %d values, want %d", len(got), core.MaxValues)
	}
	_, err := run(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "l[*]", Embedded: steps("json", "[*]")}}, root)
	extracttest.WantError(t, err, "f.yaml: the extractor selects more than 100000 values")
	if got := mustRun(t, extract.Spec{YAML: &extract.YAML{File: "f.yaml", Path: "l[0]", Embedded: steps("json", "[*]")}}, root); len(got) != half+1 {
		t.Fatalf("one string: got %d values, want %d", len(got), half+1)
	}
}

// In a file set, the documents in the strings of every file count together
// against the set's max_bytes, as one more file, the way one file's strings
// do: their bytes, and the nodes they can hold. Each file's strings fit
// alone, and so do the files' own bytes, but the strings of both do not.
// What aliases add carries over from file to file into the strings too, in
// a yaml set and in a json set of YAML strings, and its refusal says the
// files share it. The planted breaks: the counts of the strings started at
// zero for each file, which read both sets; the set's max_bytes not handed
// to the strings, which refused the set of 300 bytes at the bytes the files
// before b.yaml left; the note on the shared aliases dropped, for yaml and
// for json; the note given to a refusal on bytes.
func TestEmbeddedStringsOfASetCountTogether(t *testing.T) {
	long := `"x = 1 #` + strings.Repeat("a", 36) + `"`   // 44 bytes of TOML
	bytesFile := "s: &s " + long + "\nl: [*s, *s, *s]\n" // 69 bytes, 132 in strings
	ones := `"[` + strings.Repeat("1,", 59) + `1]"`
	nodesFile := "a = [" + ones + "]\n"
	items := strings.Repeat("0, ", core.MaxExpanded/1000-2) + "0"
	aliases := strings.Repeat("*a, ", 999) + "*a"
	root := extracttest.Tracked(t, map[string]string{
		"bytes/a.yaml":  bytesFile,
		"bytes/b.yaml":  bytesFile,
		"nodes/a.toml":  nodesFile,
		"nodes/b.toml":  nodesFile,
		"alias/a.yaml":  "a: &a [" + items + "]\nb: [" + aliases + "]\ns: ''\n",
		"alias/b.yaml":  "s: \"x: &x 1\\ny: *x\\n\"\n",
		"jalias/a.json": `{"s": ` + strconv.Quote("a: &a ["+items+"]\nb: ["+aliases+"]\n") + `}`,
		"jalias/b.json": `{"s": "x: &x 1\ny: *x\n"}`,
	})
	yamlSet := func(glob, path string, maxBytes int64, embedded []core.Embedded) extract.Spec {
		return extract.Spec{YAML: &extract.YAML{Files: []string{glob}, Path: path, Embedded: embedded, MaxBytes: maxBytes}}
	}
	_, err := run(t, yamlSet("bytes/*.yaml", "l[*]", 220, steps("toml", "x")), root)
	extracttest.WantError(t, err, `bytes/b.yaml: path "l[*]" then toml path "x": the documents in strings hold more than 220 bytes in all, the extractor's max_bytes`)
	if strings.Contains(err.Error(), "files of a set") {
		t.Errorf("a refusal on bytes speaks of the shared aliases: %v", err)
	}
	if got := mustRun(t, yamlSet("bytes/a.yaml", "l[*]", 220, steps("toml", "x")), root); len(got) != 3 {
		t.Fatalf("one file at 220 bytes: got %q, want 3 values", extracttest.Texts(got))
	}
	if got := mustRun(t, yamlSet("bytes/*.yaml", "l[*]", 300, steps("toml", "x")), root); len(got) != 6 {
		t.Fatalf("two files at 300 bytes: got %q, want 6 values", extracttest.Texts(got))
	}
	_, err = run(t, extract.Spec{TOML: &extract.TOML{Files: []string{"nodes/*.toml"}, Path: "a[*]", Embedded: steps("json", "[0]"), MaxBytes: 1000}}, root)
	extracttest.WantError(t, err, "nodes/b.toml: path \"a[*]\" then json path \"[0]\": the documents in strings can hold more nodes in all than max_bytes allows")
	_, err = run(t, yamlSet("alias/*.yaml", "s", 0, steps("yaml", "y")), root)
	extracttest.WantError(t, err, "alias/b.yaml: path \"s\" then yaml path \"y\": the yaml document in the string at line 1: line 2: aliases and merge keys expand to more than 1000000 nodes")
	extracttest.WantError(t, err, "; the files of a set share the limits on aliases and merge keys")
	_, err = run(t, extract.Spec{JSON: &extract.JSON{Files: []string{"jalias/*.json"}, Path: "s", Embedded: steps("yaml", "y")}}, root)
	extracttest.WantError(t, err, "jalias/b.json: path \"s\" then yaml path \"y\": the yaml document in the string at line 1: line 2: aliases and merge keys expand to more than 1000000 nodes")
	extracttest.WantError(t, err, "; the files of a set share the limits on aliases and merge keys")
}

// A later file of a set holds the values its path selects to the set's
// max_bytes, as FileSet holds all the files' values, not to the bytes the
// files before it left; past the set's max_bytes the message names it.
// b.yaml's aliases repeat 200 bytes three times, 600 bytes of values, while
// a.yaml leaves it 225 of the 1000. The planted break: the values held to
// the reader's own limit, which refused the set at 225 bytes.
func TestSetValuesAreHeldToTheSetsMaxBytes(t *testing.T) {
	b := "a: &a [" + strings.Repeat("y", 200) + "]\nb: [*a, *a, *a]\n"
	long := "a: &a [" + strings.Repeat("y", 200) + "]\nb: [*a, *a, *a, *a, *a, *a]\n"
	pad := func(other string) string { return "p: " + strings.Repeat("x", 1000-len(other)-4) + "\n" }
	root := extracttest.Tracked(t, map[string]string{"s/a.yaml": pad(b), "s/b.yaml": b, "t/a.yaml": pad(long), "t/b.yaml": long})
	set := func(dir string) extract.Spec {
		return extract.Spec{YAML: &extract.YAML{Files: []string{dir + "/*.yaml"}, Path: "b[*][*]", MaxBytes: 1000}}
	}
	if got := mustRun(t, set("s"), root); len(got) != 3 {
		t.Fatalf("got %d values, want 3", len(got))
	}
	_, err := run(t, set("t"), root)
	extracttest.WantError(t, err, "t/b.yaml: the values the path selects hold more than 1000 bytes in all, the extractor's max_bytes")
}

// Each file of a set reports its values at its own lines: a literal block
// scalar's values at the lines they stand at in that file, and no line past
// that file's last. The planted break: every file's reading clamped to the
// last line of a.yaml, 4, which put b.yaml's value at line 4.
func TestEmbeddedLinesInEachFileOfASet(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.yaml": "kind: x\nc: |\n  y = 1\n",
		"b.yaml": "kind: x\nother: 0\nmore: 1\nc: |\n  z = 0\n  y = 2\n",
	})
	got := mustRun(t, extract.Spec{YAML: &extract.YAML{Files: []string{"*.yaml"}, Path: "c", Embedded: steps("toml", "y")}}, root)
	if extracttest.Texts(got) != "1@3 2@6" || got[0].At.File != "a.yaml" || got[1].At.File != "b.yaml" {
		t.Fatalf("got %v, want 1@3 in a.yaml and 2@6 in b.yaml", got)
	}
}
