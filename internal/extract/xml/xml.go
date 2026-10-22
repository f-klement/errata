// Package xml is the xml extractor: the values a path of an XPath 1.0
// subset selects in an XML file.
package xml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns the values Path selects in File, an XML file. Path is the
// XPath 1.0 subset in xpath.go. Namespaces binds the prefixes Path uses to
// namespace URIs; a file's own prefixes do not count, since a file may bind
// any prefix to any namespace. An element's value is its text content, the
// text of every element inside it too, without the white space at its ends;
// an attribute's is its value, with each tab, CR and LF written in it read
// as a space (normalise), and text()'s the text as the file has it. Lines
// end as XML ends them (xmlLines).
type Spec struct {
	File       string            `json:"file"`
	Path       string            `json:"path"`
	Namespaces map[string]string `json:"namespaces,omitempty"`
	MaxBytes   int64             `json:"max_bytes,omitempty"`
}

// xmlBytesPerNode is how many bytes of max_bytes allow one node of an XML
// file: an element, an attribute, a namespace declaration or a text node,
// counted as the builder makes them. The densest of 39 real files measured,
// MARC records from a library catalogue, hold one node for every 11.5
// bytes, so the default leaves them almost 4 times the room; a POM, a
// .csproj or an XML schema holds one for every 16 to 34. The densest file
// XML can write, text between empty elements (x<a/>), holds one for every
// 2.5 bytes, and reaches the limit at 28 MB.
//
// The limit bounds nodes, not memory, as ADR 0003 has it, and an attribute
// costs more than an element: encoding/xml builds a whole start tag before
// the builder counts its attributes. The densest shape measured so far in
// memory, an example and not a ceiling, is one start tag of 8.4 million
// attributes in 32 MiB, which peaks at 1.94 GB before it is refused; 32 MiB
// of empty elements takes 1.3 GB and the 28 MB of x<a/> 1.0 GB.
const xmlBytesPerNode = 3

// xmlExtractor reads the values its path selects in an XML file.
type xmlExtractor struct {
	core.Source
	path xpath
}

// Build checks x and returns its extractor.
func Build(x Spec) (check.Extractor, error) {
	src, err := core.CheckSource("xml", x.File, x.MaxBytes)
	if err != nil {
		return nil, err
	}
	if err := checkNamespaces(x.Namespaces); err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}
	if x.Path == "" {
		return nil, errors.New("xml: path is required; write one from the root, such as /project/version")
	}
	p, err := parseXPath(x.Path, x.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}
	return xmlExtractor{Source: src, path: p}, nil
}

// Extract reads the file under root and returns every value its path
// selects.
func (e xmlExtractor) Extract(root string) ([]check.Value, error) {
	data, err := e.Read(root)
	if err != nil {
		return nil, err
	}
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e xmlExtractor) FromData(data []byte) ([]check.Value, error) {
	doc, err := parseXML(data, e.Limit/xmlBytesPerNode)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	return e.fromDoc(doc)
}

// fromDoc returns the values the path selects from doc.
func (e xmlExtractor) fromDoc(doc *xmlDoc) ([]check.Value, error) {
	kind, ids, err := e.path.eval(doc)
	switch {
	case errors.Is(err, core.ErrTooManyValues):
		return nil, core.TooManyValues(e.File)
	case err != nil:
		return nil, fmt.Errorf("%s: path %s: %w", e.File, core.Quote(e.path.text), err)
	}
	return e.values(doc, kind, ids)
}

// values turns the nodes the path selected into values, refusing them when
// they hold more bytes in all than max_bytes. An element's text holds the
// text of every element inside it, so a path that selects nested elements
// can return the same text many times over.
func (e xmlExtractor) values(doc *xmlDoc, kind xstepKind, ids []int32) ([]check.Value, error) {
	var total int64
	for _, i := range ids {
		if total += int64(doc.size(kind, i)); total > e.Limit {
			return nil, fmt.Errorf("%s: the values the path selects hold more than %d bytes in all, the extractor's max_bytes; an element's value holds the text of every element inside it, so narrow the path, or set max_bytes: higher", e.File, e.Limit)
		}
	}
	out := make([]check.Value, 0, len(ids))
	for _, i := range ids {
		text, line := doc.value(kind, i)
		out = append(out, check.Value{Text: text, At: check.Location{File: e.File, Line: int(line)}})
	}
	return out, nil
}

// size is the length of the value of node i of the kind a step selects.
func (d *xmlDoc) size(kind xstepKind, i int32) int {
	switch kind {
	case xAttribute:
		return len(d.attrs[i].value)
	case xText:
		return int(d.texts[i].end - d.texts[i].start)
	default:
		return int(d.elems[i].trim1 - d.elems[i].trim0)
	}
}

// value is the value of node i of the kind a step selects, and its line.
func (d *xmlDoc) value(kind xstepKind, i int32) (string, int32) {
	switch kind {
	case xAttribute:
		return d.attrs[i].value, d.attrs[i].line
	case xText:
		t := d.texts[i]
		return string(d.buf[t.start:t.end]), t.line
	default:
		el := d.elems[i]
		return string(d.buf[el.trim0:el.trim1]), el.line
	}
}

// xmlDoc is a parsed XML file. Its elements are in document order, the
// order of their start tags, so the elements inside one are those after it
// up to its last. Names and namespaces are kept as ids, so a test of a name
// costs the same however long the name is.
//
// Every id, offset and line is an int32, which halves the memory a dense file
// takes: a file holds at most maxMaxBytes bytes, far under 2^31, so none of
// them overflows (i32).
type xmlDoc struct {
	elems  []xmlElem
	attrs  []xmlAttr // each element's after the one before's
	texts  []xmlText
	buf    []byte           // the text of every text node, in document order
	spaces map[string]int32 // namespace URI to id; "", no namespace, is 0
	names  map[string]int32 // local name to id
	// valueLimit and workLimit replace maxValues and maxFilterBytes when
	// set, so a test reaches each limit with a small file; zero, as in
	// every document errata parses, is the constant.
	valueLimit int
	workLimit  int64
}

// valuesAllowed is the most nodes one step of a path may select.
func (d *xmlDoc) valuesAllowed() int {
	if d.valueLimit > 0 {
		return d.valueLimit
	}
	return core.MaxValues
}

// workAllowed is the most work a path's comparisons may cost, in bytes.
func (d *xmlDoc) workAllowed() int64 {
	if d.workLimit > 0 {
		return d.workLimit
	}
	return core.MaxFilterBytes
}

type xmlElem struct {
	space, name  int32
	line         int32
	last         int32 // the last element inside it, or itself
	kid, next    int32 // its first child element and its next sibling, or -1
	attr0        int32 // its first attribute, in attrs; see attrsOf
	text         int32 // its first text node, or -1
	trim0, trim1 int32 // its text content, trimmed: buf[trim0:trim1]
}

// attrsOf returns the range of attrs that holds element i's attributes,
// which end where the next element's start.
func (d *xmlDoc) attrsOf(i int32) (int32, int32) {
	if int(i)+1 < len(d.elems) {
		return d.elems[i].attr0, d.elems[i+1].attr0
	}
	return d.elems[i].attr0, i32(len(d.attrs))
}

type xmlAttr struct {
	space, name, line int32
	value             string
}

// xmlText is a text node: text and CDATA sections next to each other, up to
// a tag, a comment or a processing instruction.
type xmlText struct {
	start, end int32 // buf[start:end]
	line       int32
	next       int32 // the next text node of its element, or -1
}

// i32 is n as an int32. Every n it is given counts bytes, lines or nodes of
// one file, which holds at most maxMaxBytes bytes.
func i32(n int) int32 {
	return int32(n) //nolint:gosec // n is below maxMaxBytes, under 2^31
}

// intern returns the id of s in m, adding it when it is new.
func intern(m map[string]int32, s string) int32 {
	id, ok := m[s]
	if !ok {
		id = i32(len(m))
		m[s] = id
	}
	return id
}

// xmlBuilder builds an xmlDoc from encoding/xml's raw tokens. It checks
// what RawToken leaves out: that end tags match, that every prefix is
// bound, that no attribute repeats, that the file has one root element and
// no text outside it, the depth and the node limit.
//
// The decoder never reads a DTD: a <!DOCTYPE> is a directive token, which
// the builder skips. So no entity a DTD declares is expanded, internal or
// external, and none is fetched: in strict mode a reference to one is a
// syntax error. Only the five predefined entities and character references
// are read, each no longer than what it stands for.
type xmlBuilder struct {
	doc   *xmlDoc
	data  []byte
	lines *xmlLines
	open  []openElem
	// The elements open[pending:] have no text content yet that is not
	// white space, so their trim0 is not known; solid is where the last
	// byte of buf that is not white space ends.
	pending, solid int
	inText         bool             // the token before was text, which a text token continues
	bound          map[string]int32 // each prefix in scope to its namespace's id; "" is the default
	undo           []binding        // what each declaration in scope replaced
	nodes, limit   int64
	rooted         bool
	doctype        bool // a <!DOCTYPE> came before the root
	declared       bool // the file starts with an XML declaration
}

type openElem struct {
	index           int32
	name            xml.Name // as the start tag wrote it, for its end tag
	line            int
	lastKid, lastTx int32
	undo            int // len(undo) before its declarations
}

type binding struct {
	prefix string
	id     int32
	had    bool
}

var (
	utf8BOM  = []byte("\xef\xbb\xbf")
	utf16BOM = [][]byte{[]byte("\xff\xfe"), []byte("\xfe\xff")}
)

// parseXML reads data into an xmlDoc, refusing more than limit nodes. Every
// pass is linear in data: the decoder reads each byte once, names and
// namespaces are hashed once where the file writes them, and an element's
// text content is a range of buf, found as the text arrives.
func parseXML(data []byte, limit int64) (*xmlDoc, error) {
	for _, bom := range utf16BOM {
		if bytes.HasPrefix(data, bom) {
			return nil, errors.New("the file is UTF-16; errata reads XML in UTF-8 only")
		}
	}
	// A byte order mark holds no newline, so lines count the same without it.
	data = bytes.TrimPrefix(data, utf8BOM)
	b := &xmlBuilder{
		doc:   &xmlDoc{spaces: map[string]int32{"": 0}, names: map[string]int32{}},
		data:  data,
		lines: &xmlLines{data: data, line: 1},
		bound: map[string]int32{},
		limit: limit,
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = asciiOnly(data)
	for {
		off := int(d.InputOffset())
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, xmlError(err, b.lines.at(int(d.InputOffset())))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			err = b.start(t, off, int(d.InputOffset()))
		case xml.EndElement:
			err = b.end(t, off)
		case xml.CharData:
			err = b.chars(t, off)
		case xml.Directive:
			err = b.directive(t, off)
		case xml.ProcInst:
			err = b.procInst(t, off)
		default: // a comment
			b.inText = false
		}
		if err != nil {
			return nil, err
		}
	}
	if n := len(b.open); n > 0 {
		top := b.open[n-1]
		return nil, fmt.Errorf("the file ends before <%s>, opened at line %d, is closed", core.Clip(qname(top.name)), top.line)
	}
	if !b.rooted {
		return nil, errors.New("the file holds no root element")
	}
	return b.doc, nil
}

// asciiOnly is the decoder's CharsetReader for data, which it calls for
// each declaration that names an encoding other than UTF-8. A file in an
// encoding that agrees with UTF-8 on ASCII, and that holds ASCII only, reads
// the same in both, so it is read; any other is refused.
//
// Each call looks at the whole file, and keeps no cache of what it found:
// a declaration stands only at the start (procInst), so the decoder calls
// this at most twice, the second time just before procInst refuses the
// file. A cache saves only that second look. Measured over 8 MB, best of 5,
// three runs each: 200000 declarations are refused in 23 to 28 ms with a
// cache and 47 to 58 ms without; one declaration before 300000 other
// instructions, one look either way, reads in 185 to 192 ms and 205 to
// 224 ms. Before procInst read the declarations, a look for each of the
// 200000 took hours.
func asciiOnly(data []byte) func(string, io.Reader) (io.Reader, error) {
	return func(label string, r io.Reader) (io.Reader, error) {
		switch strings.ToLower(label) {
		case "us-ascii", "ascii", "iso-8859-1", "latin1", "iso-8859-15", "windows-1252":
			if !slices.ContainsFunc(data, func(c byte) bool { return c >= 0x80 }) {
				return r, nil
			}
		}
		return nil, fmt.Errorf("errata reads XML in UTF-8, or in an encoding such as ISO-8859-1 only when every byte is ASCII; %s", core.OtherWay)
	}
}

// xmlError is the decoder's error, at line, cut to a length a message may
// have. line is where the decoder stopped, as xmlLines counts it: the
// decoder's own line counts LF only.
func xmlError(err error, line int) error {
	var se *xml.SyntaxError
	if !errors.As(err, &se) {
		return core.ClipError(err)
	}
	msg := fmt.Sprintf("line %d: %s", line, se.Msg)
	if strings.HasPrefix(se.Msg, "invalid character entity") {
		return fmt.Errorf("%w (errata reads only the five predefined entities and character references, never an entity a DTD declares)", core.ClipError(errors.New(msg)))
	}
	return core.ClipError(errors.New(msg))
}

func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// count adds n nodes and refuses them past the limit.
func (b *xmlBuilder) count(n int, line int) error {
	if b.nodes += int64(n); b.nodes > b.limit {
		return fmt.Errorf("line %d: the file holds more than %d nodes, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this dense",
			line, b.limit, xmlBytesPerNode)
	}
	return nil
}

func (b *xmlBuilder) start(t xml.StartElement, off, end int) error {
	line := b.lines.at(off)
	if len(b.open) == 0 && b.rooted {
		return fmt.Errorf("line %d: <%s> is a second root element; an XML file has one", line, core.Clip(qname(t.Name)))
	}
	if len(b.open) == core.MaxDepth {
		return core.TooDeep(line)
	}
	if err := b.count(1+len(t.Attr), line); err != nil {
		return err
	}
	doc := b.doc
	o := openElem{index: i32(len(doc.elems)), name: t.Name, line: line, lastKid: -1, lastTx: -1, undo: len(b.undo)}
	keys := make([][2]int32, 0, len(t.Attr))
	for _, a := range t.Attr {
		prefix, ok := declares(a.Name)
		if !ok {
			continue
		}
		if err := b.declare(prefix, a.Value, line); err != nil {
			return err
		}
		keys = append(keys, [2]int32{-1, intern(doc.names, prefix)})
	}
	space, err := b.resolve(t.Name, true, line)
	if err != nil {
		return err
	}
	el := xmlElem{
		space: space, name: intern(doc.names, t.Name.Local), line: i32(line),
		last: o.index, kid: -1, next: -1, text: -1, attr0: i32(len(doc.attrs)),
	}
	spans := attrSpans(b.data[off:end], len(t.Attr))
	for k, a := range t.Attr {
		if _, ok := declares(a.Name); ok {
			continue
		}
		aline := b.lines.at(off + spans[k].name)
		aspace, err := b.resolve(a.Name, false, aline)
		if err != nil {
			return err
		}
		name := intern(doc.names, a.Name.Local)
		keys = append(keys, [2]int32{aspace, name})
		value := normalise(a.Value, b.data[off+spans[k].value0:off+spans[k].value1])
		doc.attrs = append(doc.attrs, xmlAttr{space: aspace, name: name, line: i32(aline), value: value})
	}
	if repeated(keys) {
		return fmt.Errorf("line %d: <%s> repeats an attribute or a namespace declaration", line, core.Clip(qname(t.Name)))
	}
	if n := len(b.open); n > 0 {
		parent := &b.open[n-1]
		if parent.lastKid < 0 {
			doc.elems[parent.index].kid = o.index
		} else {
			doc.elems[parent.lastKid].next = o.index
		}
		parent.lastKid = o.index
	}
	doc.elems = append(doc.elems, el)
	b.open = append(b.open, o)
	b.rooted, b.inText = true, false
	return nil
}

// declares reports whether an attribute declares a namespace, and the
// prefix it binds, "" for the default namespace.
func declares(n xml.Name) (string, bool) {
	switch {
	case n.Space == "xmlns":
		return n.Local, true
	case n.Space == "" && n.Local == "xmlns":
		return "", true
	default:
		return "", false
	}
}

func (b *xmlBuilder) declare(prefix, uri string, line int) error {
	switch {
	case prefix == "xmlns":
		return fmt.Errorf("line %d: the prefix xmlns is reserved and cannot be declared", line)
	case prefix == "xml" && uri != xmlNamespace:
		return fmt.Errorf("line %d: the prefix xml names %s and no other namespace", line, xmlNamespace)
	case prefix != "" && uri == "":
		return fmt.Errorf("line %d: xmlns:%s is empty; XML 1.0 cannot unbind a prefix", line, core.Clip(prefix))
	}
	old, had := b.bound[prefix]
	b.undo = append(b.undo, binding{prefix: prefix, id: old, had: had})
	b.bound[prefix] = intern(b.doc.spaces, uri)
	return nil
}

// resolve returns the id of a name's namespace: its prefix's, or for an
// element without one the default namespace's. An attribute without a
// prefix is in no namespace.
func (b *xmlBuilder) resolve(n xml.Name, element bool, line int) (int32, error) {
	if strings.Contains(n.Local, ":") {
		return 0, fmt.Errorf("line %d: %s has an empty prefix or local name", line, core.Quote(n.Local))
	}
	switch {
	case n.Space == "" && !element:
		return 0, nil
	case n.Space == "xml":
		return intern(b.doc.spaces, xmlNamespace), nil
	case n.Space == "xmlns":
		return 0, fmt.Errorf("line %d: the prefix xmlns is reserved for namespace declarations", line)
	}
	id, ok := b.bound[n.Space]
	if !ok && n.Space != "" {
		return 0, fmt.Errorf("line %d: the prefix %s of %s is not bound; declare it with xmlns:%s", line, core.Clip(n.Space), core.Quote(qname(n)), core.Clip(n.Space))
	}
	return id, nil
}

// repeatedPairs is how many keys repeated compares pairwise before it
// uses a map.
const repeatedPairs = 16

// repeated reports whether keys holds one key twice.
func repeated(keys [][2]int32) bool {
	if len(keys) <= repeatedPairs {
		for i := range keys {
			if slices.Contains(keys[i+1:], keys[i]) {
				return true
			}
		}
		return false
	}
	seen := make(map[[2]int32]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}

// attrSpan is where an attribute's name starts in its start tag, and the
// bytes of its value between the quotes, as the file writes them.
type attrSpan struct {
	name, value0, value1 int
}

// attrSpans returns the spans of a start tag's n attributes, in tag, the
// tag's bytes from its <, which the decoder has read and checked. A name, =
// and a quoted value come in that order, with white space around = and
// none needed between attributes, as encoding/xml reads them, and a value
// ends at the next byte of its quote.
func attrSpans(tag []byte, n int) []attrSpan {
	out := make([]attrSpan, n)
	i := 1
	for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '/' && tag[i] != '>' {
		i++
	}
	for k := range out {
		for i < len(tag) && isXMLSpace(tag[i]) {
			i++
		}
		out[k].name = i
		eq := bytes.IndexByte(tag[i:], '=')
		if eq < 0 {
			break
		}
		i += eq + 1
		for i < len(tag) && isXMLSpace(tag[i]) {
			i++
		}
		if i == len(tag) {
			break
		}
		q := tag[i]
		end := bytes.IndexByte(tag[i+1:], q)
		if end < 0 {
			break
		}
		out[k].value0, out[k].value1 = i+1, i+1+end
		i += end + 2
	}
	return out
}

// normalise is an attribute's value as XML 1.0 section 3.3.3 has it with no
// DTD, where every attribute is CDATA: each tab, CR and LF the file writes
// is a space, a CRLF one space, and a character reference keeps the
// character it names. decoded is the decoder's value, which has the
// references read and the literal characters kept, and raw the value as
// written. The value is decoded again only when raw holds one of the three,
// from raw, whose references the decoder has checked.
func normalise(decoded string, raw []byte) string {
	if !slices.ContainsFunc(raw, func(c byte) bool { return c == '\t' || c == '\n' || c == '\r' }) {
		return decoded
	}
	out := make([]byte, 0, len(decoded))
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; c {
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			out = append(out, ' ')
		case '\t', '\n':
			out = append(out, ' ')
		case '&':
			end := i + bytes.IndexByte(raw[i:], ';')
			out = appendReference(out, string(raw[i+1:end]))
			i = end
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// predefined are the five entities XML names without a DTD.
var predefined = map[string]byte{"lt": '<', "gt": '>', "amp": '&', "apos": '\'', "quot": '"'}

// appendReference appends what a reference the decoder has checked stands
// for: a predefined entity, or a character by its number, read as the
// decoder reads it.
func appendReference(out []byte, ref string) []byte {
	if c, ok := predefined[ref]; ok {
		return append(out, c)
	}
	digits, base := ref[1:], 10
	if strings.HasPrefix(digits, "x") {
		digits, base = digits[1:], 16
	}
	n, _ := strconv.ParseUint(digits, base, 32)
	return utf8.AppendRune(out, rune(n)) //nolint:gosec // the decoder took n as a rune, so n is at most U+10FFFF
}

// directive refuses a directive XML 1.0 does not allow where it stands: the
// one a document may hold is a DOCTYPE, before the root element, once.
func (b *xmlBuilder) directive(t xml.Directive, off int) error {
	b.inText = false
	line := b.lines.at(off)
	end := bytes.IndexFunc(t, func(r rune) bool { return ('A' > r || r > 'Z') && ('a' > r || r > 'z') })
	if end < 0 {
		end = len(t)
	}
	name := string(t[:end])
	rest := t[end:]
	named := bytes.TrimLeftFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	switch {
	case name != "DOCTYPE":
		return fmt.Errorf("line %d: a <!%s> directive outside a <!DOCTYPE> is not well formed", line, core.Clip(name))
	case len(named) == len(rest) || len(named) == 0 || !isXMLNameStart(firstRune(string(named[:min(len(named), utf8.UTFMax)]))):
		// doctypedecl ::= '<!DOCTYPE' S Name ...
		return fmt.Errorf("line %d: a <!DOCTYPE> with no name is not well formed; it names the root element", line)
	case b.rooted:
		return fmt.Errorf("line %d: a <!DOCTYPE> after the root element starts is not well formed; it comes before the root", line)
	case b.doctype:
		return fmt.Errorf("line %d: a second <!DOCTYPE> is not well formed", line)
	}
	b.doctype = true
	return nil
}

// procInst refuses a processing instruction XML 1.0 does not allow where
// it stands: the XML declaration only at the very start of the file, once,
// and the target xml in any case for nothing else (sections 2.6 and 2.8).
// data has its byte order mark cut, so the start is offset 0. The decoder
// has read a declaration's encoding before this sees it, so a file of many
// is refused at the second, after one look at the file (asciiOnly).
func (b *xmlBuilder) procInst(t xml.ProcInst, off int) error {
	b.inText = false
	if !strings.EqualFold(t.Target, "xml") {
		return nil
	}
	line := b.lines.at(off)
	switch {
	case t.Target != "xml":
		return fmt.Errorf("line %d: the processing instruction target %s is reserved for the XML declaration", line, core.Quote(t.Target))
	case b.declared:
		return fmt.Errorf("line %d: a second XML declaration is not well formed", line)
	case off != 0:
		return fmt.Errorf("line %d: an XML declaration after the start of the file is not well formed; it comes first, before any blank", line)
	}
	b.declared = true
	return nil
}

// xmlLines gives the line of an offset in data as XML 1.0 section 2.11 ends
// lines: at a CRLF, a lone CR or a lone LF. The other readers count LF only
// (lineCursor), which their formats define; XML's readers, editors and the
// spec count a lone CR too, as classic Mac OS wrote it. Like lineCursor it
// counts from the offset asked before, so offsets in file order take one
// pass over the file.
type xmlLines struct {
	data []byte
	off  int // the offset asked before
	line int // its line, counted from 1
}

func (c *xmlLines) at(off int) int {
	if off >= c.off {
		c.line += c.ends(c.off, off)
	} else {
		c.line -= c.ends(off, c.off)
	}
	c.off = off
	return c.line
}

// ends counts the line ends that start in data[from:to]: each LF, and each
// CR that no LF follows. The CR of a CRLF is not counted, its LF is, so a
// CRLF split across two ranges counts once.
func (c *xmlLines) ends(from, to int) int {
	seg := c.data[from:to]
	n := bytes.Count(seg, newline)
	for i := bytes.IndexByte(seg, '\r'); i >= 0; {
		if j := from + i + 1; j == len(c.data) || c.data[j] != '\n' {
			n++
		}
		next := bytes.IndexByte(seg[i+1:], '\r')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return n
}

func (b *xmlBuilder) end(t xml.EndElement, off int) error {
	n := len(b.open)
	if n == 0 {
		return fmt.Errorf("line %d: </%s> closes no element", b.lines.at(off), core.Clip(qname(t.Name)))
	}
	top := b.open[n-1]
	if t.Name != top.name {
		return fmt.Errorf("line %d: </%s> closes <%s>, opened at line %d", b.lines.at(off), core.Clip(qname(t.Name)), core.Clip(qname(top.name)), top.line)
	}
	el := &b.doc.elems[top.index]
	if n-1 >= b.pending {
		// Nothing but white space inside it.
		el.trim0, el.trim1 = i32(len(b.doc.buf)), i32(len(b.doc.buf))
	} else {
		el.trim1 = i32(b.solid)
	}
	el.last = i32(len(b.doc.elems) - 1)
	for len(b.undo) > top.undo {
		u := b.undo[len(b.undo)-1]
		if u.had {
			b.bound[u.prefix] = u.id
		} else {
			delete(b.bound, u.prefix)
		}
		b.undo = b.undo[:len(b.undo)-1]
	}
	b.open = b.open[:n-1]
	b.pending = min(b.pending, n-1)
	b.inText = false
	return nil
}

func (b *xmlBuilder) chars(t xml.CharData, off int) error {
	if len(t) == 0 {
		return nil
	}
	first := slices.IndexFunc(t, func(c byte) bool { return !isXMLSpace(c) })
	n := len(b.open)
	if n == 0 {
		if first >= 0 {
			return fmt.Errorf("line %d: text outside the root element", b.lines.at(off+first))
		}
		return nil
	}
	doc := b.doc
	start := len(doc.buf)
	if !b.inText {
		line := b.lines.at(off)
		if err := b.count(1, line); err != nil {
			return err
		}
		idx := i32(len(doc.texts))
		doc.texts = append(doc.texts, xmlText{start: i32(start), line: i32(line), next: -1})
		o := &b.open[n-1]
		if o.lastTx < 0 {
			doc.elems[o.index].text = idx
		} else {
			doc.texts[o.lastTx].next = idx
		}
		o.lastTx = idx
	}
	doc.buf = append(doc.buf, t...)
	doc.texts[len(doc.texts)-1].end = i32(len(doc.buf))
	if first >= 0 {
		for _, o := range b.open[b.pending:] {
			doc.elems[o.index].trim0 = i32(start + first)
		}
		b.pending = n
		last := len(t) - 1
		for isXMLSpace(t[last]) {
			last--
		}
		b.solid = start + last + 1
	}
	b.inText = true
	return nil
}

// newline is a line break, as bytes.Count takes it.
var newline = []byte("\n")

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// firstRune is the first rune of s.
func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}
