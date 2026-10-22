package xml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

var fuzzNS = map[string]string{"m": "urn:m", "n": "urn:n"}

// printXPath writes p back in the subset's syntax, with the prefixes fuzzNS
// binds.
func printXPath(p xpath) string {
	prefixOf := map[string]string{xmlNamespace: "xml"}
	for prefix, uri := range fuzzNS {
		prefixOf[uri] = prefix
	}
	name := func(n xname) string {
		switch {
		case n.any:
			return "*"
		case n.space == "":
			return n.local
		default:
			return prefixOf[n.space] + ":" + n.local
		}
	}
	var b strings.Builder
	for _, st := range p.steps {
		b.WriteString("/")
		if st.descendant {
			b.WriteString("/")
		}
		switch st.kind {
		case xAttribute:
			b.WriteString("@" + name(st.name))
		case xText:
			b.WriteString("text()")
		default:
			b.WriteString(name(st.name))
		}
		for _, pred := range st.preds {
			if pred.position > 0 {
				b.WriteString("[" + strconv.Itoa(pred.position) + "]")
				continue
			}
			var cs []string
			for _, c := range pred.compares {
				q := "'"
				if strings.Contains(c.lit, "'") {
					q = `"`
				}
				at := ""
				if c.attr {
					at = "@"
				}
				cs = append(cs, at+name(c.name)+"="+q+c.lit+q)
			}
			b.WriteString("[" + strings.Join(cs, " and ") + "]")
		}
	}
	return b.String()
}

// The refusal quotes the path as every refusal does, clipped when it is
// long, and then names a column.
var columnPrefix = regexp.MustCompile(`^: column (\d+): `)

// Whatever the path, Build refuses it with a message that names a column
// inside it, or parses it into steps that print back as a path that parses
// into the same steps.
func FuzzXPath(f *testing.F) {
	for _, s := range []string{
		"//m:dependency[m:artifactId='guava']/m:version", "/a/b[2]/@c", "//a//b[@x='1' and c=\"2\"][1]/text()",
		"/ a / * [ 1 ] / @ * ", "/a[last()]", "/child::a", "/a[b!='x']", "/a | /b", "a", "/a/..", "/a[0]", "/x:a",
		"/m:*", "/a[b='x' or c='y']", "/a/text()/b", "/a[b=1]", "/a[\"'\"='x']", "/xml:a/@xml:lang", "/a[b='é\t']",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		_, err := Build(Spec{File: "f", Path: path, Namespaces: fuzzNS})
		if err != nil {
			msg := err.Error()
			if path == "" {
				return
			}
			rest, quoted := strings.CutPrefix(msg, "xml: path "+core.Quote(path))
			m := columnPrefix.FindStringSubmatch(rest)
			if !quoted || m == nil {
				t.Fatalf("%q: a message without a column: %s", path, msg)
			}
			if col, _ := strconv.Atoi(m[1]); col < 1 || col > utf8.RuneCountInString(path)+1 {
				t.Fatalf("%q: column %d of %d characters", path, col, utf8.RuneCountInString(path))
			}
			return
		}
		p, err := parseXPath(path, fuzzNS)
		if err != nil || p.text != path {
			t.Fatalf("%q: Build accepts, parseXPath %v", path, err)
		}
		printed := printXPath(p)
		again, err := parseXPath(printed, fuzzNS)
		if err != nil {
			t.Fatalf("%q prints as %q, which is refused: %v", path, printed, err)
		}
		if !reflect.DeepEqual(again.steps, p.steps) {
			t.Fatalf("%q prints as %q, which parses to other steps", path, printed)
		}
	})
}

// refElem is an element as encoding/xml's Token reads it, with names
// translated to namespaces and end tags checked, independent of errata's
// builder.
type refElem struct {
	space, local string
	line         int
	attrs        []string // space, local, value and line of each, joined
	text         []byte   // the text of every text token inside it
	texts        []string // its text nodes' text and line, joined
}

// rawAttr finds each attribute's name and quoted value in a start tag. No
// blank need stand before a name, since encoding/xml reads <a b=""c="">.
var rawAttr = regexp.MustCompile(`([^ \t\r\n=/><"']+)[ \t\r\n]*=[ \t\r\n]*("[^"]*"|'[^']*')`)

// lineEnds writes each XML line end, CRLF, CR or LF, as one LF.
var lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// xmlLineCount is the number of lines of data as XML counts them.
func xmlLineCount(data []byte) int {
	return strings.Count(lineEnds.Replace(string(data)), "\n") + 1
}

// refAttrValue is a quoted attribute value as XML 1.0 section 3.3.3 reads
// it: each literal tab, CR, LF and CRLF made a space first, then the
// references read, by encoding/xml from a document of its own.
func refAttrValue(t *testing.T, quoted []byte) string {
	t.Helper()
	q, raw := quoted[0], string(quoted[1:len(quoted)-1])
	raw = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(raw)
	tok, err := xml.NewDecoder(strings.NewReader("<x a=" + string(q) + raw + string(q) + "/>")).RawToken()
	if err != nil {
		t.Fatalf("the value %q alone does not read: %v", quoted, err)
	}
	return tok.(xml.StartElement).Attr[0].Value
}

// refParse reads data with Token, as a check of what errata's builder makes
// of it through RawToken.
func refParse(t *testing.T, data []byte) ([]*refElem, error) {
	t.Helper()
	data = bytes.TrimPrefix(data, utf8BOM)
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = asciiOnly(data)
	lineAt := func(off int) int { return xmlLineCount(data[:off]) }
	var all, open []*refElem
	inText := false
	for {
		off := int(d.InputOffset())
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, err
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			el := &refElem{space: tk.Name.Space, local: tk.Name.Local, line: lineAt(off)}
			tag := data[off:d.InputOffset()]
			found := rawAttr.FindAllSubmatchIndex(tag, -1)
			if len(found) != len(tk.Attr) {
				t.Fatalf("the oracle finds %d attributes in %q, Token %d", len(found), tag, len(tk.Attr))
			}
			for i, a := range tk.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				value := refAttrValue(t, tag[found[i][4]:found[i][5]])
				el.attrs = append(el.attrs, strings.Join([]string{a.Name.Space, a.Name.Local, value, strconv.Itoa(lineAt(off + found[i][2]))}, "|"))
			}
			all = append(all, el)
			open = append(open, el)
			inText = false
		case xml.EndElement:
			open = open[:len(open)-1]
			inText = false
		case xml.CharData:
			if len(open) == 0 || len(tk) == 0 {
				continue
			}
			for _, el := range open {
				el.text = append(el.text, tk...)
			}
			top := open[len(open)-1]
			if inText {
				last := len(top.texts) - 1
				text, line, _ := strings.Cut(top.texts[last], "|")
				top.texts[last] = text + string(tk) + "|" + line
			} else {
				top.texts = append(top.texts, string(tk)+"|"+strconv.Itoa(lineAt(off)))
			}
			inText = true
		default:
			inText = false
		}
	}
}

// Whatever the file, the extractor returns values at lines inside it, no
// more than maxValues of them, or an error and no values. A file errata
// reads, encoding/xml's Token reads too, element for element: the same
// namespaces, names, lines, attributes and text nodes, and the same text
// content, trimmed.
func FuzzXML(f *testing.F) {
	f.Add([]byte(shop), "//item[@kind='tool']/name")
	f.Add([]byte(pom), "//m:dependency/m:version")
	f.Add([]byte("<a xmlns='urn:m' xmlns:n='urn:n'><n:b n:c='1' c='2'>x<![CDATA[y]]>z<!---->w</n:b><b xmlns=''/></a>"), "//n:b/text()")
	f.Add([]byte("\xef\xbb\xbf<?xml version='1.0' encoding='latin1'?>\r\n<a\r\n b = 'x'\r\n/>"), "/a/@b")
	f.Add([]byte("<!DOCTYPE a [<!ENTITY e 'x'>]><a>&e;&amp;&#x41;</a>"), "/a")
	f.Add([]byte("<a><a><a/></a><a/></a>"), "//a//a[1]")
	f.Add([]byte("<a>\n</b>"), "/a")
	f.Add([]byte("<a xmlns:n='urn:n' xmlns:m='urn:m'><n:b m:x='1'><n:b m:x='2'/></n:b></a>"), "//n:b//@m:x")
	f.Add([]byte(shop), "/shop/item[@kind='tool'][1]/name")
	f.Add([]byte(shop), "//item/@*[1]")
	f.Add([]byte(shop), "//@*[2]")
	f.Add([]byte("<a>x<a>y</a><b>1</b></a>"), "//a//text()")
	f.Fuzz(func(t *testing.T, data []byte, path string) {
		e, err := Build(Spec{File: "f", Path: path, Namespaces: fuzzNS})
		if err == nil {
			got, err := e.(fromData).FromData(data)
			switch {
			case err != nil && got != nil:
				t.Fatalf("values %v with error %v", got, err)
			case len(got) > core.MaxValues:
				t.Fatalf("%d values, over the limit", len(got))
			}
			count := xmlLineCount(data)
			for _, v := range got {
				if v.At.Line < 1 || v.At.Line > count || v.At.File != "f" {
					t.Fatalf("value %q at %s:%d of %d lines", v.Text, v.At.File, v.At.Line, count)
				}
			}
		}
		doc, err := parseXML(data, core.DefaultMaxBytes/xmlBytesPerNode)
		if err != nil {
			return
		}
		ref, err := refParse(t, data)
		if err != nil {
			t.Fatalf("errata reads the file, Token refuses it: %v", err)
		}
		if len(ref) != len(doc.elems) {
			t.Fatalf("%d elements, Token %d", len(doc.elems), len(ref))
		}
		spaces, names := reverse(doc.spaces), reverse(doc.names)
		for i, el := range doc.elems {
			r := ref[i]
			if spaces[el.space] != r.space || names[el.name] != r.local || int(el.line) != r.line {
				t.Fatalf("element %d is {%s}%s at line %d, Token's {%s}%s at line %d", i, spaces[el.space], names[el.name], el.line, r.space, r.local, r.line)
			}
			var attrs []string
			a0, a1 := doc.attrsOf(i32(i))
			for _, a := range doc.attrs[a0:a1] {
				attrs = append(attrs, strings.Join([]string{spaces[a.space], names[a.name], a.value, strconv.Itoa(int(a.line))}, "|"))
			}
			if !reflect.DeepEqual(attrs, r.attrs) {
				t.Fatalf("element %d has attributes %q, Token's %q", i, attrs, r.attrs)
			}
			var texts []string
			for k := el.text; k >= 0; k = doc.texts[k].next {
				tx := doc.texts[k]
				texts = append(texts, string(doc.buf[tx.start:tx.end])+"|"+strconv.Itoa(int(tx.line)))
			}
			if !reflect.DeepEqual(texts, r.texts) {
				t.Fatalf("element %d has text nodes %q, Token's %q", i, texts, r.texts)
			}
			if got, want := string(doc.buf[el.trim0:el.trim1]), strings.Trim(string(r.text), " \t\r\n"); got != want {
				t.Fatalf("element %d holds %q, Token's text %q", i, got, want)
			}
		}
	})
}

// reverse lists the strings of an intern map by id.
func reverse(m map[string]int32) []string {
	out := make([]string, len(m))
	for s, id := range m {
		out[id] = s
	}
	return out
}

// fromData is what every extractor runs on the bytes of its file.
type fromData interface {
	FromData(data []byte) ([]check.Value, error)
}
