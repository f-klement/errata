package xml

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
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

func xmlSpec(path string, ns map[string]string) Spec {
	return Spec{File: "f.xml", Path: path, Namespaces: ns}
}

// shop has the shapes a path meets: siblings of one name, an attribute on a
// line of its own, white space around a value, an element inside one of the
// same name, and text split by an element, a comment and a CDATA section.
const shop = `<?xml version="1.0"?>
<!-- a catalogue -->
<shop id="s1" xml:lang="en">
  <item sku="a1" kind="tool">
    <name>Hammer</name>
    <price currency="EUR">12</price>
  </item>
  <item sku="b2"
        kind="toy">
    <name> Ball </name>
    <price currency="USD">3</price>
  </item>
  <item sku="c3" kind="tool">
    <name>Saw</name>
    <price currency="EUR">20</price>
    <item sku="c3-1"><name>Blade</name></item>
  </item>
  <note>a <b>bold</b> word<!-- split -->, then <![CDATA[<raw>]]> &amp; &#x41;</note>
</shop>
`

// Each path selects what XPath 1.0 selects, in document order, at the line
// where each element or attribute starts.
func TestXMLSelectsWhatXPathSelects(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.xml": shop})
	for path, want := range map[string]string{
		"/shop/item/name":      "Hammer@5 Ball@10 Saw@14",
		"//item/name":          "Hammer@5 Ball@10 Saw@14 Blade@16",
		"//item//name":         "Hammer@5 Ball@10 Saw@14 Blade@16",
		"/shop/item/item/name": "Blade@16",
		"/shop/item[2]/@sku":   "b2@8",
		// A // step to attributes with a position counts per element.
		"//@*[2]":                       "en@3 tool@4 toy@9 tool@13",
		"/shop/item[2]/@*[2]":           "toy@9",
		"//item/@*[1]":                  "a1@4 b2@8 c3@13 c3-1@16",
		"/shop/item[2]/@kind":           "toy@9",
		"/ shop / item [ 2 ] / @ kind ": "toy@9",
		// [1] counts among the children of each element, so it selects the
		// first item of the shop and the first item inside c3.
		"//item[1][1]/@sku":                              "a1@4 c3-1@16",
		"//*[@currency='EUR'][1]":                        "12@6 20@15",
		"//item[1]/name":                                 "Hammer@5 Blade@16",
		"/shop/item[@kind='tool']/name":                  "Hammer@5 Saw@14",
		"/shop/item[@kind='tool'][2]/name":               "Saw@14",
		"/shop/item[2][@kind='tool']/name":               "",
		"/shop/item[price='20' and @kind=\"tool\"]/@sku": "c3@13",
		"/shop/item[name='Ball']/@sku":                   "b2@8",
		"/shop/item[price='12']/price/@currency":         "EUR@6",
		"/shop/item[*='3']/@sku":                         "b2@8",
		"/shop/item[@*='toy']/@sku":                      "b2@8",
		"/shop/*[3]/@sku":                                "c3@13",
		"/shop/*/@sku":                                   "a1@4 b2@8 c3@13",
		"//@currency":                                    "EUR@6 USD@11 EUR@15",
		"/shop/@xml:lang":                                "en@3",
		"/shop[@id='s1']/note/b":                         "bold@18",
		"/shop/missing":                                  "",
		"/shop/item[9]":                                  "",
		"/@id":                                           "",
		"/text()":                                        "",
		"/shop/note/text()[2]":                           " word@18",
	} {
		got := extracttest.Texts(mustRun(t, xmlSpec(path, nil), root))
		if got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// An element's value is the text of everything inside it, entities and
// character references read and CDATA as written, without the white space
// at its ends. text() gives each text node as the file has it: text and
// CDATA next to each other are one, and a comment or an element ends one.
func TestXMLElementValueIsItsTextTrimmed(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.xml": shop})
	got := mustRun(t, xmlSpec("/shop/note", nil), root)
	if len(got) != 1 || got[0].Text != "a bold word, then <raw> & A" {
		t.Errorf("note: %q", extracttest.Texts(got))
	}
	var nodes []string
	for _, v := range mustRun(t, xmlSpec("/shop/note/text()", nil), root) {
		nodes = append(nodes, v.Text)
	}
	if want := []string{"a ", " word", ", then <raw> & A"}; !slices.Equal(nodes, want) {
		t.Errorf("note/text(): %q, want %q", nodes, want)
	}
	got = mustRun(t, xmlSpec("//name/text()", nil), root)
	if extracttest.Texts(got) != "Hammer@5  Ball @10 Saw@14 Blade@16" {
		t.Errorf("name/text(): %q", extracttest.Texts(got))
	}
	// White space is XML's four characters: a no-break space is text.
	root = extracttest.Repo(t, map[string]string{"f.xml": "<a>\u00a0x\r\n\t</a>"})
	if got := extracttest.Texts(mustRun(t, xmlSpec("/a", nil), root)); got != "\u00a0x@1" {
		t.Errorf("no-break space: %q", got)
	}
}

// pom is a Maven POM under its default namespace, with an element that
// undeclares it and one under a prefix of the file's own.
const pom = `<project xmlns="http://maven.apache.org/POM/4.0.0"
    xmlns:x="urn:x">
  <version>1.0</version>
  <x:extra x:flag="on" flag="off"/>
  <dependencies>
    <dependency>
      <artifactId>guava</artifactId>
      <version>33.0.0-jre</version>
    </dependency>
    <dependency xmlns="">
      <artifactId>guava</artifactId>
      <version>none</version>
    </dependency>
  </dependencies>
</project>
`

var pomNS = map[string]string{"m": "http://maven.apache.org/POM/4.0.0", "y": "urn:x"}

// A step without a prefix selects elements in no namespace, so under a
// default namespace it selects nothing: the trap the README shows with this
// file. A prefix of the spec matches by the namespace it names, whatever
// prefix the file writes. A namespace declaration is not an attribute.
func TestXMLNamespaces(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"pom.xml": pom})
	for path, want := range map[string]string{
		"/project/version":                               "",
		"/m:project/m:version":                           "1.0@3",
		"//m:dependency[m:artifactId='guava']/m:version": "33.0.0-jre@8",
		"//dependency/version":                           "none@12",
		"/m:project/y:extra/@y:flag":                     "on@4",
		"/m:project/y:extra/@flag":                       "off@4",
		"/m:project/*/@*":                                "on@4 off@4",
		"/m:project/@*":                                  "",
		"/*/*[1]":                                        "1.0@3",
		"/*/m:dependencies/*/m:version":                  "33.0.0-jre@8",
	} {
		got := extracttest.Texts(mustRun(t, Spec{File: "pom.xml", Path: path, Namespaces: pomNS}, root))
		if got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
	other := map[string]string{"m": "urn:other"}
	if got := mustRun(t, Spec{File: "pom.xml", Path: "/m:project/m:version", Namespaces: other}, root); len(got) != 0 {
		t.Errorf("a prefix bound to a namespace the file lacks: %q", extracttest.Texts(got))
	}
}

// A byte order mark, CRLF line ends and an ASCII file that declares a
// Latin encoding read as the plain file does.
func TestXMLReadsWhatEncodingXMLReads(t *testing.T) {
	for name, body := range map[string]string{
		"bom":    "\xef\xbb\xbf<a>\n<b>1</b></a>",
		"crlf":   "<a>\r\n<b>1</b></a>",
		"latin1": "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><a>\n<b>1</b></a>",
		"ascii":  "<?xml version='1.0' encoding='us-ascii'?><a>\n<b>1</b></a>",
	} {
		got := extracttest.Texts(mustRun(t, xmlSpec("/a/b", nil), extracttest.Repo(t, map[string]string{"f.xml": body})))
		if got != "1@2" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

// Each planted break is a file that is not well formed, or not namespace
// well formed, in a way encoding/xml's raw tokens let through or report;
// each control beside it is the same file mended.
func TestXMLRefusesAMalformedFile(t *testing.T) {
	many := func(dup string) string {
		var b strings.Builder
		b.WriteString("<a")
		for i := range repeatedPairs + 1 {
			fmt.Fprintf(&b, " k%d='1'", i)
		}
		return b.String() + dup + "/>"
	}
	for _, c := range []struct{ body, want, control string }{
		{"<a>\n</b>", "line 2: </b> closes <a>, opened at line 1", "<a>\n</a>"},
		{"<a>\n<b>", "the file ends before <b>, opened at line 2, is closed", "<a>\n<b/></a>"},
		{"<a/></b>", "line 1: </b> closes no element", "<a/>"},
		{"<a/>\n<b/>", "line 2: <b> is a second root element; an XML file has one", "<a/>\n"},
		{"<a/>\nx", "line 2: text outside the root element", "<a/>\n "},
		{" \n<!-- c -->\n", "the file holds no root element", "<a/>"},
		{"", "the file holds no root element", "<a/>"},
		{"<p:a/>", `line 1: the prefix p of "p:a" is not bound; declare it with xmlns:p`, "<p:a xmlns:p='u'/>"},
		{"<a p:b='1'/>", `line 1: the prefix p of "p:b" is not bound; declare it with xmlns:p`, "<a p:b='1' xmlns:p='u'/>"},
		{"<a xmlns:p='u'><p:b/></a><p:c/>", "line 1: <p:c> is a second root element", "<a xmlns:p='u'><p:b/></a>"},
		{"<a xmlns:p='u'/>\n", "", "<a xmlns:p='u'/>"},
		{"<a><b xmlns:p='u'/><p:c/></a>", `line 1: the prefix p of "p:c" is not bound`, "<a><b xmlns:p='u'/><c/></a>"},
		{"<a b='1' b='2'/>", "line 1: <a> repeats an attribute or a namespace declaration", "<a b='1' c='2'/>"},
		{"<a xmlns:p='u' xmlns:q='u' p:b='1' q:b='2'/>", "line 1: <a> repeats an attribute or a namespace declaration", "<a xmlns:p='u' xmlns:q='v' p:b='1' q:b='2'/>"},
		{"<a xmlns:p='u' xmlns:p='v'/>", "line 1: <a> repeats an attribute or a namespace declaration", "<a xmlns:p='u' xmlns:q='v'/>"},
		{many(" k3='2'"), "line 1: <a> repeats an attribute or a namespace declaration", many(" kk='2'")},
		{"<a xmlns:p=''/>", "line 1: xmlns:p is empty; XML 1.0 cannot unbind a prefix", "<a xmlns=''/>"},
		{"<a xmlns:xmlns='u'/>", "line 1: the prefix xmlns is reserved and cannot be declared", "<a xmlns:xmln='u'/>"},
		{"<a xmlns:xml='u'/>", "line 1: the prefix xml names http://www.w3.org/XML/1998/namespace and no other namespace", "<a xmlns:xml='http://www.w3.org/XML/1998/namespace'/>"},
		{"<xmlns:a/>", "line 1: the prefix xmlns is reserved for namespace declarations", "<xml:a/>"},
		{"<a: />", `line 1: "a:" has an empty prefix or local name`, "<a />"},
		{"<a :b='1'/>", `line 1: ":b" has an empty prefix or local name`, "<a b='1'/>"},
		{"<a>&foo;</a>", "line 1: invalid character entity &foo; (errata reads only the five predefined entities and character references, never an entity a DTD declares)", "<a>&amp;</a>"},
		{"<a>\n&#0;</a>", "line 2: illegal character code U+0000", "<a>\n&#9;</a>"},
		{"\xff\xfe<\x00a\x00/\x00>\x00", "the file is UTF-16; errata reads XML in UTF-8 only", "<a/>"},
		{"\xfe\xff\x00<\x00a\x00/\x00>", "the file is UTF-16; errata reads XML in UTF-8 only", "<a/>"},
		{"<?xml version='1.0' encoding='ISO-8859-1'?><a>\xe9</a>", `xml: opening charset "ISO-8859-1": errata reads XML in UTF-8, or in an encoding such as ISO-8859-1 only when every byte is ASCII; a regex extractor can still read the file`, "<?xml version='1.0' encoding='UTF-8'?><a>\xc3\xa9</a>"},
		{"<?xml version='1.0' encoding='EBCDIC'?><a/>", `xml: opening charset "EBCDIC": errata reads XML in UTF-8`, "<?xml version='1.0'?><a/>"},
		{"<?xml version='1.1'?><a/>", `xml: unsupported version "1.1"; only version 1.0 is supported`, "<?xml version='1.0'?><a/>"},
	} {
		_, err := run(t, xmlSpec("/*", nil), extracttest.Repo(t, map[string]string{"f.xml": c.body}))
		if c.want == "" {
			if err != nil {
				t.Errorf("%q: %v", c.body, err)
			}
			continue
		}
		extracttest.WantError(t, err, "f.xml: "+c.want)
		if _, err := run(t, xmlSpec("/*", nil), extracttest.Repo(t, map[string]string{"f.xml": c.control})); err != nil {
			t.Errorf("control %q: %v", c.control, err)
		}
	}
}

// Each planted break is a construct of XPath 1.0 outside the subset, or a
// path that is no XPath, refused at its column, by name where XPath has it.
func TestXPathRefusesWhatTheSubsetLeavesOut(t *testing.T) {
	ns := map[string]string{"m": "urn:m", "fn": "urn:fn"}
	for path, want := range map[string]string{
		"":                         "xml: path is required; write one from the root, such as /project/version",
		" ":                        "column 2: the path is empty; write one from the root, such as /project/version",
		"a/b":                      "column 1: a path starts at the root, with / or //; write /a/b or //a/b",
		"./a":                      "column 1: the self step (.) is not supported",
		"/a/.":                     "column 4: the self step (.) is not supported",
		"/a/..":                    "column 4: the parent step (..) is not supported",
		"$x":                       "column 1: a variable ($) is not supported",
		"(/a)":                     "column 1: a parenthesised expression is not supported",
		"/child::a":                "column 2: the axis child:: is not supported; the subset has child steps, // and @",
		"/a/following :: b":        "column 4: the axis following:: is not supported",
		"/a[last()]":               "column 4: the function last() is not supported; the subset has no functions",
		"/a[fn:x()='1']":           "column 4: the function fn:x() is not supported",
		"/a/node()":                "column 4: the node test node() is not supported; the subset has text()",
		"/comment()":               "column 2: the node test comment() is not supported",
		"/a/@text()":               "column 5: text() is a step of its own, as /a/text(), and takes no prefix or @",
		"/a/text(1)":               "column 9: text() takes no argument; write text()",
		"/a | /b":                  "column 4: a union (|) is not supported; write one path for each extractor",
		"/a*2":                     "column 3: arithmetic is not supported",
		"/a[b!='x']":               "column 5: the operator != is not supported; a predicate compares with =",
		"/a[b<'x']":                "column 5: the operator < is not supported",
		"/a[b >= 'x']":             "column 6: the operator >= is not supported",
		"/a[b div 2]":              "column 6: arithmetic is not supported",
		"/a[b='x' + 1]":            "column 10: arithmetic is not supported",
		"/a[@b]":                   "column 6: a predicate that a child or attribute exists is not supported; compare its value, as [@b='value']",
		"/a[b and c='1']":          "column 6: a predicate that a child or attribute exists is not supported",
		"/a[b='x' or c='y']":       "column 10: or is not supported; a predicate joins comparisons with and",
		"/a[b='x' c='y']":          `column 10: expected and or ] after a comparison but found 'c'`,
		"/a[1.5]":                  "column 4: a position is a whole number",
		"/a[0]":                    "column 4: positions count from 1, so [0] selects nothing; write [1] for the first",
		"/a[-1]":                   "column 4: a negative position is not supported; positions count from 1 at the start",
		"/a[99999999999999999999]": "column 4: the position 99999999999999999999 is too large",
		"/a[1 and b='x']":          "column 6: expected ] after a position but found 'a'",
		"/a[1=b]":                  "column 4: write the child or attribute before =, and the literal after it",
		"/a['x'=b]":                "column 4: write the child or attribute before =, and the literal after it",
		"/a[b=1]":                  "column 6: values compare as strings; quote the number, as '1'",
		"/a[b=-1.5]":               "column 6: values compare as strings; quote the number, as '-1.5'",
		"/a[b=c]":                  "column 6: a comparison takes a quoted literal after =; comparing two nodes is not supported",
		"/a[b=$v]":                 "column 6: a variable ($) is not supported",
		"/a[b='x]":                 "column 6: the literal is not closed; end it with '",
		"/a[]":                     "column 4: the predicate is empty",
		"/a[":                      "column 4: the path ends inside a predicate",
		"/a[b":                     "column 5: the path ends inside a predicate",
		"/a[b=":                    "column 6: the path ends inside a predicate",
		"/a[b/c='x']":              "column 5: a path inside a predicate is not supported; a comparison takes one child or attribute",
		"/a[b[1]='x']":             "column 5: a predicate on a comparison's child is not supported",
		"/@/a":                     `column 3: expected a step: an element, @attribute or text() but found '/'`,
		"/[1]":                     `column 2: expected a step: an element, @attribute or text() but found '['`,
		"/a[text()='x']":           "column 4: text() is not supported in a predicate; compare the element itself, as [name='value']",
		"/a[.='x']":                "column 4: the self step (.) is not supported",
		"/a/@b/c":                  "column 6: an attribute has no children, so its step ends the path",
		"/a/text()/b":              "column 10: text has no children, so its step ends the path",
		"/a/text()[b='x']":         "column 10: a comparison on text() is not supported",
		"/a/@b[c='x']":             "column 6: a comparison on an attribute is not supported; compare on the element that holds it, as [@name='value']",
		"///a":                     "column 1: /// is not a separator; write / for a child or // for a descendant",
		"/a/":                      "column 4: the path ends after /; name a step, such as an element",
		"/":                        "column 2: the path ends after /",
		"//":                       "column 3: the path ends after /",
		"/a b":                     `column 4: expected / or // before the next step, or the end of the path but found 'b'`,
		"/a[b='x']]":               `column 10: expected / or // before the next step, or the end of the path but found ']'`,
		"/x:a":                     "column 2: the prefix x is not bound; bind it under namespaces:, as {x: <namespace URI>}",
		"/xmlns:a":                 "column 2: the prefix xmlns names namespace declarations, which are not nodes a path selects",
		"/a/@xmlns:b":              "column 5: the prefix xmlns names namespace declarations, which are not nodes a path selects",
		"/a[x:b='1']":              "column 4: the prefix x is not bound",
		"/m:*":                     "column 2: the wildcard m:* is not supported; write * for any element",
		"/m:":                      "column 4: expected a local name after m:",
		"/1a":                      `column 2: expected a step: an element, @attribute or text() but found '1'`,
		"/×":                       `column 2: expected a step: an element, @attribute or text() but found '×'`,
		"/a[text ()='x']":          "column 4: text() is not supported in a predicate",
		"/a/m:b ()":                "column 4: the function m:b() is not supported",
		"/a\x00":                   "column 3: control character U+0000 is not allowed in a path",
		"/\xff":                    "column 2: the path is not valid UTF-8",
		"/a[b='" + strings.Repeat("x", core.MaxLiteral+1) + "']":             "column 6: the literal holds 2049 bytes, over the limit of 2048",
		"/a" + strings.Repeat("/a", maxXPathTerms):                           "column 66: the path holds more than 32 steps and predicates, the limit",
		"/a" + strings.Repeat("[1]", maxXPathTerms):                          "column 96: the path holds more than 32 steps and predicates, the limit",
		"/a[" + strings.Repeat("b='x' and ", core.MaxComparisons) + "b='x']": "column 644: a predicate holds more than 64 comparisons, the limit",
	} {
		_, err := Build(xmlSpec(path, ns))
		if !strings.HasPrefix(want, "xml: ") {
			want = "xml: path " + core.Quote(path) + ": " + want
		}
		extracttest.WantError(t, err, want)
	}
	// The controls: the same paths at their limits, and each construct
	// the subset has.
	for _, path := range []string{
		"/a" + strings.Repeat("/a", maxXPathTerms-1),
		"/a" + strings.Repeat("[1]", maxXPathTerms-1),
		"/a[" + strings.Repeat("b='x' and ", core.MaxComparisons-1) + "b='x']",
		"/a[b='" + strings.Repeat("x", core.MaxLiteral) + "']",
		"//m:a[@m:b='1' and m:c=\"2\"][3]/text()",
		"/*/@*", "/a/text", "/a[text='1']", "/a[and='1']", "/a/@xml:lang", "/a[b='\t']",
		"\n/a\t/\r\nb ", "/a/text ()",
	} {
		if _, err := Build(xmlSpec(path, ns)); err != nil {
			t.Errorf("%q: %v", path, err)
		}
	}
}

// The planted breaks: a prefix that is not a name, the empty prefix, the
// reserved xmlns, xml bound elsewhere, and a prefix bound to nothing.
func TestXMLRefusesANamespaceNoPathCanUse(t *testing.T) {
	for want, ns := range map[string]map[string]string{
		`xml: namespaces: the prefix "1m" is not a name without a colon; a path needs one, such as m, to write m:name`:           {"1m": "u"},
		`xml: namespaces: the prefix "" is not a name without a colon`:                                                           {"": "u"},
		`xml: namespaces: the prefix "a:b" is not a name without a colon`:                                                        {"a:b": "u"},
		"xml: namespaces: the prefix xmlns is reserved and cannot be bound":                                                      {"xmlns": "u"},
		"xml: namespaces: the prefix xml names http://www.w3.org/XML/1998/namespace and no other namespace":                      {"xml": "u"},
		`xml: namespaces: the prefix "m" is bound to the empty string; an element in no namespace takes a step without a prefix`: {"m": ""},
	} {
		_, err := Build(xmlSpec("/a", ns))
		extracttest.WantError(t, err, want)
	}
	if _, err := Build(xmlSpec("/xml:a/@xml:b", map[string]string{"xml": xmlNamespace, "m": "u"})); err != nil {
		t.Errorf("control: %v", err)
	}
}

// The xml extractor reads its file through the checks every kind shares:
// the repository's bounds, max_bytes and the file size. The planted breaks
// are the shared tests' inputs.
func TestXMLSharesTheSourceChecks(t *testing.T) {
	for _, f := range []string{"/etc/passwd", "../secret.xml", ".."} {
		_, err := Build(Spec{File: f, Path: "/a"})
		extracttest.WantError(t, err, `xml: file "`+f+`" is outside the repository`)
	}
	for _, n := range []int64{-1, core.MaxMaxBytes + 1} {
		_, err := Build(Spec{File: "f", Path: "/a", MaxBytes: n})
		extracttest.WantError(t, err, "xml: max_bytes is "+strconv.FormatInt(n, 10)+"; write a number of bytes from 1 to 67108864")
	}
	for want, s := range map[string]Spec{
		"xml: file is required": {},
		"xml: path is required": {File: "f"},
	} {
		_, err := Build(s)
		extracttest.WantError(t, err, want)
	}
	body := "<a>1</a>" + strings.Repeat(" ", 992)
	if got := extracttest.Texts(mustRun(t, Spec{File: "f.xml", Path: "/a", MaxBytes: 1000}, extracttest.Repo(t, map[string]string{"f.xml": body}))); got != "1@1" {
		t.Errorf("a file of max_bytes: %q", got)
	}
	_, err := run(t, Spec{File: "f.xml", Path: "/a", MaxBytes: 1000}, extracttest.Repo(t, map[string]string{"f.xml": body + " "}))
	extracttest.WantError(t, err, "read f.xml: the file is 1001 bytes, over the limit of 1000 bytes")

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.xml"), []byte("<a>secret</a>"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := extracttest.Repo(t, nil)
	extracttest.Symlink(t, filepath.Join(outside, "secret.xml"), filepath.Join(root, "link.xml"))
	_, err = run(t, Spec{File: "link.xml", Path: "/a"}, root)
	if err == nil || err.Error() != "read link.xml: a symlink leads outside the repository" {
		t.Errorf("symlink out: %v", err)
	}
}

// No entity a DTD declares is ever expanded or fetched: a reference to one
// is refused, an internal one too, and a DTD that is never referenced is
// skipped, its external parts unread. The server counts every request, and
// the test's own request first shows that it counts.
func TestXMLNeverExpandsOrFetchesAnEntity(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `<!ENTITY leak "FETCHED">`)
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/witness", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("the server counted %d requests, want 1", hits.Load())
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := "invalid character entity &%s; (errata reads only the five predefined entities and character references, never an entity a DTD declares)"
	laughs := `<!DOCTYPE l [<!ENTITY l0 "lol">` + extracttest.RepeatLines(9, func(i int) string {
		return fmt.Sprintf(`<!ENTITY l%d "%s">`, i+1, strings.Repeat("&l"+strconv.Itoa(i)+";", 10))
	}) + "]>"
	for name, c := range map[string]struct{ body, want string }{
		"external file":      {`<!DOCTYPE a [<!ENTITY xxe SYSTEM "file://` + secret + `">]><a>&xxe;</a>`, fmt.Sprintf(refused, "xxe")},
		"external url":       {`<!DOCTYPE a [<!ENTITY xxe SYSTEM "` + srv.URL + `/e">]><a>&xxe;</a>`, fmt.Sprintf(refused, "xxe")},
		"in an attribute":    {`<!DOCTYPE a [<!ENTITY xxe SYSTEM "` + srv.URL + `/e">]><a b="&xxe;">1</a>`, fmt.Sprintf(refused, "xxe")},
		"internal":           {`<!DOCTYPE a [<!ENTITY i "x">]><a>&i;</a>`, fmt.Sprintf(refused, "i")},
		"billion laughs":     {laughs + "<l>&l9;</l>", fmt.Sprintf(refused, "l9")},
		"parameter entity":   {`<!DOCTYPE a [<!ENTITY % p SYSTEM "` + srv.URL + `/p"> %p;]><a>1</a>`, ""},
		"external DTD":       {`<!DOCTYPE a SYSTEM "` + srv.URL + `/a.dtd"><a>1</a>`, ""},
		"public DTD":         {`<!DOCTYPE a PUBLIC "-//A//DTD A//EN" "` + srv.URL + `/a.dtd"><a>1</a>`, ""},
		"declared, unused":   {`<!DOCTYPE a [<!ENTITY xxe SYSTEM "file://` + secret + `">]><a>1</a>`, ""},
		"predefined control": {`<!DOCTYPE a [<!ENTITY xxe SYSTEM "file://` + secret + `">]><a>&#49;</a>`, ""},
	} {
		start := time.Now()
		got, err := run(t, xmlSpec("/*", nil), extracttest.Repo(t, map[string]string{"f.xml": c.body}))
		if elapsed := time.Since(start); elapsed > time.Second*extracttest.RaceSlowdown {
			t.Errorf("%s: took %v", name, elapsed)
		}
		switch {
		case c.want != "":
			extracttest.WantError(t, err, "f.xml: line 1: "+c.want)
		case err != nil:
			t.Errorf("%s: %v", name, err)
		case extracttest.Texts(got) != "1@1":
			t.Errorf("%s: got %q", name, extracttest.Texts(got))
		}
		if err != nil && (strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "FETCHED")) {
			t.Errorf("%s: the message holds what an entity names: %v", name, err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the extractor made %d requests", n-1)
	}
}

// A file nests 1000 elements deep, the limit, and the planted break nests
// one more.
func TestXMLDepthLimit(t *testing.T) {
	nest := func(n int) string { return strings.Repeat("<a>", n) + "x" + strings.Repeat("</a>", n) }
	got := mustRun(t, xmlSpec("/a"+strings.Repeat("/a", maxXPathTerms-1), nil), extracttest.Repo(t, map[string]string{"f.xml": nest(core.MaxDepth)}))
	if extracttest.Texts(got) != "x@1" {
		t.Errorf("at the limit: %q", extracttest.Texts(got))
	}
	_, err := run(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": nest(core.MaxDepth + 1)}))
	extracttest.WantError(t, err, "f.xml: line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file")
}

// The node limit is max_bytes over xmlBytesPerNode, and counts elements,
// attributes and text nodes. Text between empty elements is the densest
// shape XML has, so it can reach the limit inside max_bytes: the control is
// the file at the limit, and the planted break holds one node more.
func TestXMLNodeLimit(t *testing.T) {
	const limit = 1000
	nodes := func(k int) string { return "<r>" + strings.Repeat("x<a/>", k) + "</r>" } // 1 + 2k nodes
	k := (limit/xmlBytesPerNode - 1) / 2
	s := Spec{File: "f.xml", Path: "/r/a[1]", MaxBytes: limit}
	if _, err := run(t, s, extracttest.Repo(t, map[string]string{"f.xml": nodes(k)})); err != nil {
		t.Errorf("%d nodes: %v", 1+2*k, err)
	}
	_, err := run(t, s, extracttest.Repo(t, map[string]string{"f.xml": strings.Replace(nodes(k), "<r>", "<r b='1'>", 1)}))
	extracttest.WantError(t, err, fmt.Sprintf("f.xml: line 1: the file holds more than %d nodes, the limit, one for every 3 bytes of max_bytes; set max_bytes: higher to read a file this dense", limit/xmlBytesPerNode))
}

// An extractor returns maxValues values, and the planted break selects one
// more, through each of the three ways a step selects: a child step, a //
// step to elements and a // step to attributes. Each runs its control and
// its break through the same path. Here the document's limit is 1000, so
// the files are small; the message names maxValues, which the document's
// limit stands for. TestXMLValueLimitAtItsSize runs the files of 100000.
func TestXMLValueLimit(t *testing.T) {
	xmlValueLimit(t, 1000)
}

// TestXMLValueLimit at maxValues, which -race slows past make test's time.
func TestXMLValueLimitAtItsSize(t *testing.T) {
	slowOnly(t)
	xmlValueLimit(t, 0)
}

// xmlValueLimit runs TestXMLValueLimit with the document's value limit
// set to limit, or at maxValues when limit is zero.
func xmlValueLimit(t *testing.T, limit int) {
	t.Helper()
	n := limit
	if n == 0 {
		n = core.MaxValues
	}
	for path, unit := range map[string]string{"/r/a": "<a/>", "//a": "<a/>", "//@k": "<a k='1'/>"} {
		file := func(n int) string { return "<r>" + strings.Repeat(unit, n) + "</r>" }
		got, err := readXMLWithin(t, xmlSpec(path, nil), file(n), limit, 0)
		if err != nil || len(got) != n {
			t.Errorf("%s: %d values, %v", path, len(got), err)
		}
		_, err = readXMLWithin(t, xmlSpec(path, nil), file(n+1), limit, 0)
		extracttest.WantError(t, err, "f.xml: the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer")
	}
}

// readXMLWithin reads body as the XML extractor of spec does, with the
// document's value and work limits set to values and work, the constants
// where zero.
func readXMLWithin(t *testing.T, spec Spec, body string, values int, work int64) ([]check.Value, error) {
	t.Helper()
	e, err := Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	x := e.(xmlExtractor)
	doc, err := parseXML([]byte(body), x.Limit/xmlBytesPerNode)
	if err != nil {
		t.Fatal(err)
	}
	doc.valueLimit, doc.workLimit = values, work
	return x.fromDoc(doc)
}

// A document's limits are the constants unless a test sets them.
func TestXMLDocLimitsAreTheConstants(t *testing.T) {
	var d xmlDoc
	if d.valuesAllowed() != core.MaxValues || d.workAllowed() != core.MaxFilterBytes {
		t.Errorf("limits %d and %d, want %d and %d", d.valuesAllowed(), d.workAllowed(), core.MaxValues, int64(core.MaxFilterBytes))
	}
	d.valueLimit, d.workLimit = 7, 9
	if d.valuesAllowed() != 7 || d.workAllowed() != 9 {
		t.Errorf("limits %d and %d, want 7 and 9", d.valuesAllowed(), d.workAllowed())
	}
}

// A child step over nested contexts finds a nested context's children
// before the outer one's later children, so it sorts what it selects into
// document order; a // step to text() reads a context inside another once.
// The planted breaks: no sort gives 2 1, no skip gives x y y.
func TestXMLStepKeepsDocumentOrderAndReadsANodeOnce(t *testing.T) {
	for body, c := range map[string]struct{ path, want string }{
		"<a><a><b>1</b></a><b>2</b></a>": {"//a/b", "1@1 2@1"},
		"<a>x<a>y</a></a>":               {"//a//text()", "x@1 y@1"},
	} {
		if got := extracttest.Texts(mustRun(t, xmlSpec(c.path, nil), extracttest.Repo(t, map[string]string{"f.xml": body}))); got != c.want {
			t.Errorf("%s: got %q, want %q", c.path, got, c.want)
		}
	}
}

// Nested elements each hold the text inside them, so a path can select the
// file's text many times over. The values hold at most max_bytes in all:
// ten nested elements around 1000 bytes hold 10000, the control, and the
// planted break nests one more.
func TestXMLValuesHoldAtMostMaxBytes(t *testing.T) {
	const limit = 10000
	nest := func(n int) string {
		return strings.Repeat("<a>", n) + strings.Repeat("x", 1000) + strings.Repeat("</a>", n)
	}
	s := Spec{File: "f.xml", Path: "//a", MaxBytes: limit}
	if got := mustRun(t, s, extracttest.Repo(t, map[string]string{"f.xml": nest(10)})); len(got) != 10 {
		t.Errorf("%d values", len(got))
	}
	_, err := run(t, s, extracttest.Repo(t, map[string]string{"f.xml": nest(11)}))
	extracttest.WantError(t, err, "f.xml: the values the path selects hold more than 10000 bytes in all, the extractor's max_bytes; an element's value holds the text of every element inside it, so narrow the path, or set max_bytes: higher")
}

// The comparisons of a path are counted against maxFilterBytes as they run:
// stepBytes for each child or attribute looked at, and the literal's length
// for each value compared. Each a below has 100 children before the b that
// 64 comparisons find, so it costs 64 * (100*256 + 257) bytes. Here the
// document's work limit is three a's exactly: the control reads three, the
// planted break is a fourth, and the planted break >= refuses the control.
// TestXMLComparisonsStopAtTheFilterLimitAtItsSize runs it at
// maxFilterBytes, 2417 a's.
func TestXMLComparisonsStopAtTheFilterLimit(t *testing.T) {
	xmlComparisonsAtTheLimit(t, 3*xmlComparisonCost, 3)
}

// TestXMLComparisonsStopAtTheFilterLimit at maxFilterBytes, which looks at
// 15 million children: under a second plain, over 70 s under -race.
func TestXMLComparisonsStopAtTheFilterLimitAtItsSize(t *testing.T) {
	slowOnly(t)
	xmlComparisonsAtTheLimit(t, 0, int(core.MaxFilterBytes/xmlComparisonCost))
}

// xmlComparisonCost is what the comparisons on one a cost.
const xmlComparisonCost = 64 * (100*core.StepBytes + core.StepBytes + 1)

// xmlComparisonsAtTheLimit reads most a's within the work limit work, the
// constant where zero, and refuses one more.
func xmlComparisonsAtTheLimit(t *testing.T, work int64, most int) {
	t.Helper()
	file := func(n int) string {
		return "<r>" + strings.Repeat("<a>"+strings.Repeat("<c/>", 100)+"<b>x</b></a>", n) + "</r>"
	}
	path := "/r/a[" + strings.TrimSuffix(strings.Repeat("b='x' and ", 64), " and ") + "][1]/b"
	got, err := readXMLWithin(t, xmlSpec(path, nil), file(most), 0, work)
	if err != nil || extracttest.Texts(got) != "x@1" {
		t.Errorf("%d a's: %q, %v", most, extracttest.Texts(got), err)
	}
	_, err = readXMLWithin(t, xmlSpec(path, nil), file(most+1), 0, work)
	extracttest.WantError(t, err, "f.xml: path "+core.Quote(path)+": its filters would hash and compare more than 4000000000 bytes, the limit")
}

// A declaration that names an encoding other than UTF-8 asks whether the
// file is ASCII, which looks at the whole file. A declaration stands only at
// the start, so a file of 10000 more is refused at the second, and a file
// of 15000 other processing instructions after one is read: each in one
// pass, where a look for each declaration took hours at 200000.
// TestXMLDeclarationsLookAtALargeFileOnce runs those of 8 MB.
func TestXMLDeclarationsLookAtTheFileOnce(t *testing.T) {
	xmlDeclarationsLookOnce(t, 10_000)
}

// TestXMLDeclarationsLookAtTheFileOnce at 200000 declarations, 8 MB, which
// -race slows past make test's time.
func TestXMLDeclarationsLookAtALargeFileOnce(t *testing.T) {
	slowOnly(t)
	xmlDeclarationsLookOnce(t, 200_000)
}

// xmlDeclarationsLookOnce reads n declarations after the first, and one and
// a half times as many other instructions, each within 2 s.
func xmlDeclarationsLookOnce(t *testing.T, n int) {
	t.Helper()
	decl := "<?xml version='1.0' encoding='us-ascii'?>"
	for name, c := range map[string]struct{ body, want string }{
		"declarations": {decl + "<a>\n" + strings.Repeat(decl, n) + "1</a>", "f.xml: line 2: a second XML declaration is not well formed"},
		"instructions": {decl + "<a>\n" + strings.Repeat("<?pi encoding='us-ascii'?>", n*3/2) + "1</a>", ""},
	} {
		start := time.Now()
		got, err := run(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": c.body}))
		if elapsed := time.Since(start); elapsed > 2*time.Second*extracttest.RaceSlowdown {
			t.Errorf("%s: took %v", name, elapsed)
		}
		switch {
		case c.want != "":
			extracttest.WantError(t, err, c.want)
		case err != nil || extracttest.Texts(got) != "1@1":
			t.Errorf("%s: got %q, %v", name, extracttest.Texts(got), err)
		}
	}
}

// XML 1.0 sections 2.6 and 2.8: the XML declaration stands only at the very
// start of the file, after a byte order mark at most, and once; no other
// processing instruction takes the target xml in any case; and a DOCTYPE
// names the root. The planted breaks are each of those; the controls a
// declaration at the start, after a BOM, and another instruction anywhere.
func TestXMLRefusesADeclarationOutOfPlace(t *testing.T) {
	for body, want := range map[string]string{
		"<a>\n<?xml version='1.0'?></a>":                 "line 2: an XML declaration after the start of the file is not well formed; it comes first, before any blank",
		" <?xml version='1.0'?><a/>":                     "line 1: an XML declaration after the start of the file is not well formed",
		"<!DOCTYPE a>\n<?xml version='1.0'?><a/>":        "line 2: an XML declaration after the start of the file is not well formed",
		"<?xml version='1.0'?><?xml version='1.0'?><a/>": "line 1: a second XML declaration is not well formed",
		"<?XML version='1.0'?><a/>":                      `line 1: the processing instruction target "XML" is reserved for the XML declaration`,
		"<a><?Xml x?></a>":                               `line 1: the processing instruction target "Xml" is reserved for the XML declaration`,
		"<!DOCTYPE><a/>":                                 "line 1: a <!DOCTYPE> with no name is not well formed; it names the root element",
		"<!DOCTYPE [<!ELEMENT a ANY>]><a/>":              "line 1: a <!DOCTYPE> with no name is not well formed",
		"<!DOCTYPE\n><a/>":                               "line 1: a <!DOCTYPE> with no name is not well formed",
	} {
		_, err := run(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": body}))
		extracttest.WantError(t, err, "f.xml: "+want)
	}
	for _, body := range []string{
		"<?xml version='1.0'?>\n<a>1</a>",
		"\xef\xbb\xbf<?xml version='1.0'?><a>1</a>",
		"<?xml-stylesheet href='s.xsl'?><a><?xml-model x?>1</a>",
		"<!DOCTYPE\na\n[<!ELEMENT a ANY>]><a>1</a>",
	} {
		if got := mustRun(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": body})); len(got) != 1 || got[0].Text != "1" {
			t.Errorf("control %q: %q", body, extracttest.Texts(got))
		}
	}
}

// XML 1.0 section 3.3.3: with no DTD every attribute is CDATA, so each tab,
// CR and LF written in its value reads as a space, a CRLF as one, while a
// character reference keeps the character it names. The planted break is
// each literal character; the control is each reference.
func TestXMLNormalisesAttributeValues(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.xml": "<a b=\"x\ty\nz\r\nw  v\" c='&#9;&#10;&#13;' d=\"p&#10;q\nr&amp;\tA&#x41;\"/>"})
	for path, want := range map[string]string{
		"/a/@b": "x y z w  v",
		"/a/@c": "\t\n\r",
		"/a/@d": "p\nq r& AA",
	} {
		got := mustRun(t, xmlSpec(path, nil), root)
		if len(got) != 1 || got[0].Text != want {
			t.Errorf("%s: got %q, want %q", path, extracttest.Texts(got), want)
		}
	}
	if got := extracttest.Texts(mustRun(t, xmlSpec("/a[@b='x y z w  v']/@d", nil), root)); got != "p\nq r& AA@3" {
		t.Errorf("a comparison on the normalised value: %q", got)
	}
}

// XML 1.0 section 2.8: the one directive a document holds is its DOCTYPE,
// in the prolog. The planted breaks are one inside and one after the root
// element, a second one and another directive; the control is a DOCTYPE
// before the root.
func TestXMLRefusesADirectiveOutsideTheProlog(t *testing.T) {
	for body, want := range map[string]string{
		"<a>\n<!DOCTYPE a></a>":          "line 2: a <!DOCTYPE> after the root element starts is not well formed; it comes before the root",
		"<a/>\n<!DOCTYPE a>":             "line 2: a <!DOCTYPE> after the root element starts is not well formed; it comes before the root",
		"<!DOCTYPE a>\n<!DOCTYPE a><a/>": "line 2: a second <!DOCTYPE> is not well formed",
		"<!ENTITY e 'x'>\n<a/>":          "line 1: a <!ENTITY> directive outside a <!DOCTYPE> is not well formed",
		"<a><!ELEMENT a ANY></a>":        "line 1: a <!ELEMENT> directive outside a <!DOCTYPE> is not well formed",
	} {
		_, err := run(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": body}))
		extracttest.WantError(t, err, "f.xml: "+want)
	}
	got := mustRun(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": "<?xml version='1.0'?>\n<!DOCTYPE a [<!ELEMENT a ANY>]>\n<!-- c -->\n<a>1</a>"}))
	if extracttest.Texts(got) != "1@4" {
		t.Errorf("control: %q", extracttest.Texts(got))
	}
}

// XML 1.0 section 2.11: a CRLF, a lone CR and a lone LF each end a line, so
// a file with the CR line ends of classic Mac OS reports the lines an editor
// shows, in values and in messages, the decoder's too. The planted break is
// each lone CR.
func TestXMLCountsALoneCRAsALineEnd(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.xml": "<a>\r<b>1</b>\r\n<c\rx='2'/>\n\rtext</a>"})
	for path, want := range map[string]string{
		"/a/b":         "1@2",
		"/a/c/@x":      "2@4",
		"/a/c":         "@3",
		"/a/text()[3]": "\n\ntext@4",
	} {
		if got := extracttest.Texts(mustRun(t, xmlSpec(path, nil), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
	for body, want := range map[string]string{
		"<a>\r\r</b>":      "f.xml: line 3: </b> closes <a>, opened at line 1",
		"<a>\r\r&foo;</a>": "f.xml: line 3: invalid character entity &foo;",
		"<a>\r\n\r<b>":     "f.xml: the file ends before <b>, opened at line 3, is closed",
	} {
		_, err := run(t, xmlSpec("/a", nil), extracttest.Repo(t, map[string]string{"f.xml": body}))
		extracttest.WantError(t, err, want)
	}
}

// a//@k is a/descendant-or-self::node()/@k, so from an element with no
// children it reads that element's own attributes. The planted break is a
// range of one element read as empty.
func TestDescendantAttributesOfALeafAreItsOwn(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.xml": "<r><a k='1'/><b><c k='2'/></b></r>"})
	for path, want := range map[string]string{"/r/a//@k": "1@1", "//a//@k": "1@1", "/r/b//@k": "2@1"} {
		if got := extracttest.Texts(mustRun(t, xmlSpec(path, nil), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
