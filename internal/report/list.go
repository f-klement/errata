package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// ListVersion is the version of the JSON errata list writes. It changes when
// a key is removed or renamed or a value changes its meaning. A new key does
// not change it, and neither does a new value of type or compare, or a new
// kind of extractor: a consumer reads a check with one it does not know as
// a check that changed. The README's "Listing the checks" says the same.
const ListVersion = 1

// Lister writes the checks a config loads, as errata list prints them, and
// required, the ids its required key lists, nil when it has none.
type Lister interface {
	WriteList(w io.Writer, checks []check.Check, required []string) error
}

// ListFormats lists the listers by the name errata list's --format flag
// accepts.
var ListFormats = map[string]Lister{
	"text": ListText{},
	"json": ListJSON{},
}

// ListFormatNames returns the accepted --format values of errata list in a
// stable order.
func ListFormatNames() []string {
	return slices.Sorted(maps.Keys(ListFormats))
}

// ListJSON is the format a program reads: one object with the version of its
// shape and every check in the order the config holds them, each with the
// values the loader resolved. The README's "Listing the checks" documents
// the keys; TestListJSONKeysAreDocumented holds the two together.
//
// The relation of a rule is an object, so a compare mode on agree is one
// more value of its compare key and not a new version.
type ListJSON struct{}

type listDoc struct {
	Version  int         `json:"version"`
	Checks   []listCheck `json:"checks"`
	Required []string    `json:"required"`
}

// listCheck is one check of the JSON: its id, reason, scope, place and the
// name of its kind, then the keys of its kind (check.Kind.ListFields).
type listCheck check.Check

func (c listCheck) MarshalJSON() ([]byte, error) {
	fields := check.Fields{
		{Key: "id", Value: c.ID},
		{Key: "reason", Value: c.Reason},
		{Key: "scope", Value: string(c.Scope)},
		{Key: "at", Value: listAt{File: c.At.File, Line: c.At.Line, Outside: c.At.Outside}},
		{Key: "type", Value: c.Kind.Name()},
	}
	return append(fields, c.Kind.ListFields(check.Check(c))...).MarshalJSON()
}

type listAt struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Outside bool   `json:"outside"`
}

// WriteList writes checks and required as one JSON object of version
// ListVersion, required as an empty list when the config has none.
func (ListJSON) WriteList(w io.Writer, checks []check.Check, required []string) error {
	doc := listDoc{Version: ListVersion, Checks: make([]listCheck, 0, len(checks)), Required: check.OrEmpty(required)}
	for _, c := range checks {
		doc.Checks = append(doc.Checks, listCheck(c))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	// encoding/json writes C1 controls and direction controls as they are,
	// so they are written as JSON escapes, as in SARIF.
	_, err := w.Write(escapeActedOn(buf.Bytes(), func(r rune) string { return fmt.Sprintf(`\u%04x`, r) }))
	return err
}

// ListText is the listing a person reads: each check with its scope and
// type, its reason, and what it runs, in the labels of the text report, then
// a count. Its layout may change between releases; a program reads ListJSON.
// Every name, argument and string of an extractor is written by textValue,
// so no text from the config reads as a second extractor, argument or name.
// Like Text, it goes to the terminal through QuoteControls.
type ListText struct{}

// countedFirst are the kinds the count names whether or not a check is of
// them, as it has since v0.1.0. It names every other kind after them, in
// the order the checks first hold it, once a check is of it.
var countedFirst = []check.Kind{&check.Rule{}, &check.Program{}}

// WriteList writes each of checks with its labels, then the count, which
// says when the config requires every one of them.
func (ListText) WriteList(w io.Writer, checks []check.Check, required []string) error {
	var b strings.Builder
	kinds := slices.Clone(countedFirst)
	count := map[string]int{}
	for _, c := range checks {
		one, _ := c.Kind.Nouns()
		if !slices.ContainsFunc(kinds, func(k check.Kind) bool { return k.Name() == c.Kind.Name() }) {
			kinds = append(kinds, c.Kind)
		}
		count[c.Kind.Name()]++
		fmt.Fprintf(&b, "%s (%s) %s\n", c.ID, c.Scope, one)
		writeField(&b, "why:", c.Reason)
		for _, l := range c.Kind.ListLines(c, listQuoter{}) {
			writeField(&b, l.Label, l.Text)
		}
		b.WriteString("\n")
	}
	counts := make([]string, 0, len(kinds))
	for i, k := range kinds {
		n := count[k.Name()]
		if n == 0 && i >= len(countedFirst) {
			continue
		}
		one, many := k.Nouns()
		counts = append(counts, plural(n, one, many))
	}
	fmt.Fprintf(&b, "%s: %s", plural(len(checks), "check", "checks"), strings.Join(counts, ", "))
	if len(required) > 0 {
		// The loader refuses a required list that is not the checks' ids.
		b.WriteString("; required lists every one")
	}
	b.WriteString(".\n")
	_, err := io.WriteString(w, QuoteControls(b.String()))
	return err
}

// listQuoter writes the values of the text listing with textValue and
// specText.
type listQuoter struct{}

func (listQuoter) Value(s string) string { return textValue(s) }
func (listQuoter) Spec(spec any) string  { return specText(spec) }

// writeField writes text after label, each further line indented to the
// column the first starts in. The indent is the label's own, not one width
// for every label, so a reason of many lines costs a few bytes a line.
func writeField(b *strings.Builder, label, text string) {
	pad := strings.Repeat(" ", len(label)+1)
	for i, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if i == 0 {
			fmt.Fprintf(b, "  %s %s\n", label, line)
		} else {
			fmt.Fprintf(b, "  %s%s\n", pad, line)
		}
	}
}

// plainText is the text textValue writes as it is: no space, comma, quote,
// backquote or brace, so it cannot read as two values or as a quoted one.
var plainText = regexp.MustCompile(`^[A-Za-z0-9_./@%+=:$*?^()\\[\]|-]+$`)

// textValue is s as the text listing writes a name, an argument or a string
// of an extractor: as it is when plainText holds it, so a regular expression
// reads as written; otherwise between backquotes, still as written, when
// every rune of it is one strconv.IsPrint names and none is a backquote, so
// QuoteControls changes nothing inside them; otherwise quoted with Go's
// escapes, as check's messages quote every name.
func textValue(s string) string {
	switch {
	case plainText.MatchString(s):
		return s
	case s != "" && !strings.ContainsRune(s, '`') && !strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }):
		return "`" + s + "`"
	default:
		return strconv.Quote(s)
	}
}

// specText is an extractor's entry as the text listing writes it: its kind,
// then each key and value in the order errata.yaml's JSON names them, such
// as regex file README.md, pattern `v(\d+)`, max_bytes 33554432. A string
// goes through textValue and a map, such as xml's namespaces, between braces.
func specText(spec any) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		return fmt.Sprintf("(%v)", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var b strings.Builder
	writeSpecValue(&b, dec, 0)
	return b.String()
}

// writeSpecValue writes the next value of dec at depth: the entry itself at
// 0, whose one key is the kind; the kind's keys at 1, joined with commas;
// a map further in between braces.
func writeSpecValue(b *strings.Builder, dec *json.Decoder, depth int) {
	tok, err := dec.Token()
	if err != nil {
		return
	}
	switch t := tok.(type) {
	case json.Delim:
		// A list is its values, each written as a value here is, between
		// brackets, such as the embedded steps of a yaml, json or toml
		// extractor: [{format toml, path x}].
		if t == '[' {
			b.WriteString("[")
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				writeSpecValue(b, dec, 2)
			}
			_, _ = dec.Token()
			b.WriteString("]")
			return
		}
		sep := ", "
		if depth == 0 {
			sep = "; "
		}
		if depth > 1 {
			b.WriteString("{")
		}
		for i := 0; dec.More(); i++ {
			if i > 0 {
				b.WriteString(sep)
			}
			key, _ := dec.Token()
			k, _ := key.(string)
			b.WriteString(textValue(k) + " ")
			writeSpecValue(b, dec, depth+1)
		}
		_, _ = dec.Token()
		if depth > 1 {
			b.WriteString("}")
		}
	case string:
		b.WriteString(textValue(t))
	case nil:
		b.WriteString("null")
	default:
		fmt.Fprint(b, t)
	}
}

// plural is n with one or many, as "1 rule" and "2 rules".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
