package properties

import (
	"bytes"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, Build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, Build, s, root)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// values is the text@line of what s extracts from body in file.
func values(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	return extracttest.Texts(mustRun(t, s, extracttest.Repo(t, map[string]string{file: body})))
}

// refusal is the error s gives on body in file.
func refusal(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	got, err := run(t, s, extracttest.Repo(t, map[string]string{file: body}))
	if err == nil {
		t.Fatalf("no error, values %s", extracttest.Texts(got))
	}
	return err.Error()
}

// notLiterals extracts from body with s and writes each value as text@line,
// with a ! after the value when it is marked not literal.
func notLiterals(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	var out []string
	for _, v := range mustRun(t, s, extracttest.Repo(t, map[string]string{file: body})) {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, v.Text+mark+"@"+strconv.Itoa(v.At.Line))
	}
	return strings.Join(out, " ")
}

func props(key string) Spec {
	return Spec{File: "a.properties", Key: key}
}

// The examples of the java.util.Properties.load documentation, each with
// the key and the value it gives there.
func TestPropertiesReadsTheJavadocExamples(t *testing.T) {
	for _, c := range []struct{ body, key, want string }{
		{"Truth = Beauty\n", "Truth", "Beauty@1"},
		{"  Truth:Beauty\n", "Truth", "Beauty@1"},
		{"Truth                    :Beauty\n", "Truth", "Beauty@1"},
		{"fruits                           apple, banana, pear, \\\n" +
			"                                  cantaloupe, watermelon, \\\n" +
			"                                  kiwi, mango\n", "fruits", "apple, banana, pear, cantaloupe, watermelon, kiwi, mango@1"},
		{"cheeses\n", "cheeses", "@1"},
		{"\\:\\= = x\n", ":=", "x@1"},
	} {
		if got := values(t, "a.properties", c.body, props(c.key)); got != c.want {
			t.Errorf("%q, key %q: got %q, want %q", c.body, c.key, got, c.want)
		}
	}
}

// Each rule of LineReader and loadConvert. Every case was run through
// Properties.load of Temurin 21.0.12.1, which gives the same values but for
// the two with a surrogate alone, which Java keeps and UTF-8 cannot hold.
func TestPropertiesLines(t *testing.T) {
	for _, c := range []struct{ name, body, key, want string }{
		{"a dotted key", "server.port=8080\nserver.port.x=1\n", "server.port", "8080@1"},
		{"a key set twice", "a=1\nb=2\na=3\n", "a", "1@1 3@3"},
		{"comments", "# a=1\n  ! a=2\na=3 # kept\n", "a", "3 # kept@3"},
		{"a comment is never joined", "# x \\\na=1\n", "a", "1@2"},
		{"a joined line may start with #", "a=1 \\\n  # 2\n", "a", "1 # 2@1"},
		{"a lone \\ before a comment", "\\\n#a=1\na=2\n", "a", "2@3"},
		{"the comment after a lone \\ sets no key", "\\\n#a=1\na=2\n", "#a", ""},
		{"a lone \\ before a key", "\\\n  a=1\n", "a", "1@2"},
		{"an even run of \\ ends the line", "a=x\\\\\nb=y\n", "a", "x\\@1"},
		{"an odd run of \\ joins", "a=x\\\\\\\nb=y\n", "a", "x\\b=y@1"},
		{"\\ at the end of the file", "a=x\\", "a", "x@1"},
		{"\\ and a newline at the end", "a=x\\\n", "a", "x@1"},
		{"a joined empty line ends it", "a=x\\\n\nb=y\n", "a", "x@1"},
		{"a joined blank line ends it", "a=x\\\n   \nb=y\n", "b", "y@3"},
		{"CR ends a line", "a=1\rb=2\r", "b", "2@1"},
		{"CRLF", "a=1\r\nb=2\r\n", "b", "2@2"},
		{"\\ and CRLF joins", "a=1\\\r\n  2\r\nb=3\r\n", "a", "12@1"},
		{"\\ and CR joins", "a=1\\\r  2\rb=3\r", "a", "12@1"},
		{"form feed is white space", "\fa\f=\f1\n", "a", "1@1"},
		{"one separator, then white space", "a = = 1\n", "a", "= 1@1"},
		{"white space, then a separator", "a   :  1\n", "a", "1@1"},
		{"white space alone separates", "a b c\n", "a", "b c@1"},
		{"an escaped space in a key", "a\\ b = c\n", "a b", "c@1"},
		{"escapes", "a=\\t\\n\\r\\f\\b\\z\\\\\\\"\\'\n", "a", "\t\n\r\fbz\\\"'@1"},
		{"a unicode escape", "a=\\u0041\\u00e9\\u20AC\n", "a", "Aé€@1"},
		{"an escaped key", "\\u0061=1\n", "a", "1@1"},
		{"a surrogate pair", "a=\\uD83D\\uDE00\n", "a", "\U0001F600@1"},
		{"a surrogate alone", "a=\\uD83Dx\\uDE00\n", "a", "\uFFFDx\uFFFD@1"},
		{"two high surrogates", "a=\\uD83D\\uD83D\\uDE00\n", "a", "\uFFFD\U0001F600@1"},
		{"text that is not ASCII", "é=ü\n", "é", "ü@1"},
		{"blank lines first", "\n\n  \t\n\na=1\n", "a", "1@5"},
		{"the line of the key", "x=1\n\n  key \\\n = value\n", "key", "value@3"},
		{"no line break at the end", "a=1", "a", "1@1"},
		{"a byte order mark is a character", "\xef\xbb\xbfa=1\n", "a", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := values(t, "a.properties", c.body, props(c.key)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: a \u with fewer than four hex digits, in a key and in
// a value, which Properties.load refuses with IllegalArgumentException.
func TestPropertiesRefusesAMalformedUnicodeEscape(t *testing.T) {
	for body, want := range map[string]string{
		"a=\\u12\n":        "a.properties: line 1: the value: a \\u is not followed by four hex digits, which Properties.load refuses",
		"x=1\na=\\u12g4\n": "a.properties: line 2: the value: a \\u is not followed by four hex digits, which Properties.load refuses",
		"\\uXYZW=1\n":      "a.properties: line 1: the key: a \\u is not followed by four hex digits, which Properties.load refuses",
		"a=\\uD83D\\u12":   "a.properties: line 1: the value: a \\u is not followed by four hex digits, which Properties.load refuses",
	} {
		if got := refusal(t, "a.properties", body, props("a")); got != want {
			t.Errorf("%q: error %q, want %q", body, got, want)
		}
	}
}

// Java's UTF-8 reader puts U+FFFD for each bad sequence, as Temurin 21's
// Properties.load reads them.
func TestPropertiesReadsWhatJavaReads(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"a byte that is not UTF-8", "a=\xff\\\n b\n", "�b@1"},
		{"a cut sequence is one U+FFFD", "a=\xe2\x82x\xf4\x90\x80\x80\n", "�x����@1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := values(t, "a.properties", c.body, props("a")); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Java reads every value as written, so only a template's values are marked
// not literal, as its engine renders them.
func TestPropertiesMarksWhatATemplateComputes(t *testing.T) {
	for _, c := range []struct{ name, file, body, want string }{
		{"a jinja comment", "app.properties.j2", "a=x {# c #}\n", "x {# c #}!@1"},
		{"a template", "app.properties.j2", "a={{ x }}\nb=1\n", "{{ x }}!@1"},
		{"a file that is no template", "a.properties", "a={{ x }}\n", "{{ x }}@1"},
	} {
		if got := notLiterals(t, c.file, c.body, Spec{File: c.file, Key: "a"}); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	for file, want := range map[string]bool{"app.properties.tmpl": true, "a.properties": false} {
		e, err := Build(Spec{File: file, Key: "a"})
		if err != nil {
			t.Fatal(err)
		}
		if got := check.MayReturnNotLiteral(e); got != want {
			t.Errorf("%s: may return not literal %v, want %v", file, got, want)
		}
	}
}

// propertiesExtractor is the extractor of key in a.properties.
func propertiesExtractor(t *testing.T, key string) core.EntryExtractor {
	t.Helper()
	e, err := Build(props(key))
	if err != nil {
		t.Fatal(err)
	}
	return e.(core.EntryExtractor)
}

// An ordinary file, the tests' own, short and so denser than a real one,
// charges a few units a byte, under a quarter of workPerByte, without the
// floor: the budget leaves it room. TestWorkMeterLeavesRoomForOrdinaryFiles
// is the same for the ini dialects.
func TestPropertiesWorkMeterLeavesRoomForOrdinaryFiles(t *testing.T) {
	coretest.LeavesRoom(t, propertiesExtractor(t, "server.port"), "server.port=8080\nspring.datasource.url=jdbc:x \\\n  y\n# c\n")
}

// Each budget below what a read costs refuses it; at its cost it reads what
// it reads with no meter. TestWorkMeterRefusesAtEveryBudget is the same for
// the ini dialects.
func TestPropertiesWorkMeterRefusesAtEveryBudget(t *testing.T) {
	coretest.RefusesAtEveryBudget(t, propertiesExtractor(t, "server.port"), "# c\nserver.port=8080\nspring.datasource.url=jdbc:x \\\n  y\n")
}

// The reader charges each line it reads, a comment line too: 1000 comment
// lines, then an entry, cost at least lineWork a line, and so do lines a lone
// \r ends; and no more than the read's budget, which a charge that grows
// with the line's place in the file would pass. The planted breaks are a
// line loop that charges nothing, a reader that charges the comments before
// an entry as one line, one that counts \n alone, and a line charged from
// the file's start, by its bytes or by its line ends.
// TestWorkMeterChargesEveryLine is the same for the ini dialects.
func TestPropertiesWorkMeterChargesEveryLine(t *testing.T) {
	for _, comment := range []string{"# c\n", "# c\r", "k=1\r\r"} {
		data := []byte(strings.Repeat(comment, 1000) + "k=1\n")
		spent := coretest.Spent(t, propertiesExtractor(t, "k"), data)
		lines := 1000 * (strings.Count(comment, "\n") + strings.Count(strings.ReplaceAll(comment, "\r\n", ""), "\r"))
		if want := int64(lines) * core.LineWork; spent < want {
			t.Errorf("%q: %d units for %d lines, want at least %d", comment, spent, lines, want)
		}
		if budget := core.NewMeter(len(data)).Budget; spent > budget {
			t.Errorf("%q: %d units for %d lines, over the read's budget of %d", comment, spent, lines, budget)
		}
	}
}

// A read charges each line once, from the last charged end: 1000 comment
// lines cost as much after the entry as before it. The planted break is
// the read's last charge taken from the file's start instead, by its bytes
// or by its line ends, which costs the comments' 4000 bytes again when they
// come first.
func TestPropertiesChargesTheSameLinesAlikeInEitherOrder(t *testing.T) {
	comments, entry := strings.Repeat("# c\n", 1000), "k=1\n"
	first := coretest.Spent(t, propertiesExtractor(t, "k"), []byte(comments+entry))
	last := coretest.Spent(t, propertiesExtractor(t, "k"), []byte(entry+comments))
	if first != last {
		t.Errorf("%d units with the comments first, %d with them last", first, last)
	}
}

// properties decodes a line of bytes that are not UTF-8, three bytes each
// as U+FFFD, into one buffer sized once, and reads its escapes in place: a
// file of 256 KiB of them allocates three times its bytes. Growing the line
// by appends allocates 16 times, and a value buffer of its own 19 times;
// decoding the whole file first as well peaked at 1.76 GB at 64 MiB. A
// value joined over 16384 lines grows its buffer by doubling, under eight
// times its bytes; sized exactly for each line, it copied the value at
// every line, 128 MiB. The planted breaks are those three.
func TestPropertiesDecodesALineOnce(t *testing.T) {
	for _, c := range []struct {
		data        []byte
		value, most int
	}{
		{append([]byte("a="), bytes.Repeat([]byte{0xff}, 256<<10)...), 3 * (256 << 10), 4 * (256 << 10)},
		{append([]byte("a="), bytes.Repeat([]byte("x\\\n  "), 16384)...), 16384, 8 * 16384},
	} {
		// The fewest bytes of five reads, since the count is the
		// process's and a goroutine another test left can allocate
		// during one.
		least := uint64(1 << 62)
		for range 5 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			n := 0
			err := readProperties(c.data, nil, func(e *core.Entry) error { n += len(e.Value); return nil })
			runtime.ReadMemStats(&after)
			if err != nil || n != c.value {
				t.Fatalf("%d bytes, %v", n, err)
			}
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		if least > uint64(c.most) {
			t.Errorf("allocated %d bytes for a value of %d, over %d", least, c.value, c.most)
		}
	}
}

// Build names the field it needs.
func TestBuildRefusesPropertiesFields(t *testing.T) {
	for want, s := range map[string]Spec{
		"properties: file is required": {},
		"properties: key is required":  {File: "a.properties"},
	} {
		_, err := Build(s)
		if err == nil || err.Error() != want {
			t.Errorf("%+v: error %v, want %q", s, err, want)
		}
	}
}
