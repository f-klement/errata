package table

import (
	"bytes"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// cell is one value of a row: a field, or one item of a list. Its text is
// a part of the file, or a column's default, and valid while the read
// runs.
type cell struct {
	text []byte
	line int
	// computed marks a cell of a template's line the engine computes, and
	// varies one whose text the engine writes (marker).
	computed, varies bool
}

// row is the cells of each column of one row, in the dialect's order: one
// for a field, the items for a list.
type row [][]cell

// cellWork is the work of one cell a row holds, besides its line's.
const cellWork = 16

// cSpace is the white space of C's isspace in the C locale: what glibc's
// strtol and strtoul skip before a number, and what glibc skips before
// each member of a group's user list.
const cSpace = " \t\n\v\f\r"

// eachLine calls f with each line of data, without its line end, and its
// number, charging m for each. Lines end where core says they do: at a LF,
// a CRLF and a lone CR.
func eachLine(data []byte, m *core.Meter, f func(n int, line []byte) error) error {
	for off, n := 0, 1; off < len(data); n++ {
		next := core.NextLine(data, off)
		if err := m.Charge(core.LineWork + next - off); err != nil {
			return err
		}
		line := data[off:next]
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if err := f(n, line); err != nil {
			return err
		}
		off = next
	}
	return nil
}

// isSpace reports a space or a tab, the white space between the fields of
// the tables that split at white space.
func isSpace(r rune) bool { return r == ' ' || r == '\t' }

// cutAt is b up to the first c, or b.
func cutAt(b []byte, c byte) []byte {
	before, _, _ := bytes.Cut(b, []byte{c})
	return before
}

// layout is how a table's tool splits a line into fields.
type layout struct {
	// colon splits at :, the last column taking the rest of the line, as
	// glibc's nss_files reads passwd and group; otherwise fields are runs
	// of anything but spaces and tabs.
	colon bool
	// hashAnywhere ends a line at any #, as glibc reads hosts; otherwise
	// only a line whose first field starts with # is a comment.
	hashAnywhere bool
	// required is the fields a row has at least; a line with fewer is
	// passed over, as the tool passes it over.
	required int
}

// fields are the fields of line in l, nil for a comment or a blank line.
// In a template, tagged is set, and a field runs on through an expression
// of the engine, whose spaces and colons split nothing.
func (l layout) fields(line []byte, columns int, tagged bool) [][]byte {
	if l.colon {
		s := bytes.TrimLeft(line, " \t\v\f")
		if len(s) == 0 || s[0] == '#' {
			return nil
		}
		return splitColons(s, columns, tagged)
	}
	if l.hashAnywhere {
		line = cutAt(line, '#')
	}
	f := splitSpaces(line, tagged)
	if len(f) > 0 && f[0][0] == '#' {
		return nil
	}
	return f
}

// splitColons is s split at :, into at most most fields.
func splitColons(s []byte, most int, tagged bool) [][]byte {
	if !tagged {
		return bytes.SplitN(s, []byte(":"), most)
	}
	var out [][]byte
	start := 0
	for i := 0; i < len(s) && len(out) < most-1; {
		if s[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
		i = skipTag(s, i)
	}
	return append(out, s[start:])
}

// splitSpaces is the runs of s between spaces and tabs.
func splitSpaces(s []byte, tagged bool) [][]byte {
	if !tagged {
		return bytes.FieldsFunc(s, isSpace)
	}
	var out [][]byte
	start := -1
	for i := 0; i <= len(s); {
		if i == len(s) || isSpace(rune(s[i])) {
			if start >= 0 {
				out = append(out, s[start:i])
			}
			start = -1
			i++
			continue
		}
		if start < 0 {
			start = i
		}
		i = skipTag(s, i)
	}
	return out
}

// tags are the start of each template engine's expressions and the end
// that closes it: Jinja's and Go's, ERB's and Terraform's.
var tags = [][2]string{{"{{", "}}"}, {"{%", "%}"}, {"{#", "#}"}, {"<%", "%>"}, {"${", "}"}, {"%{", "}"}}

// skipTag is the offset past the expression that starts at i of s and the
// end that closes it, or past the byte at i when none starts there.
func skipTag(s []byte, i int) int {
	for _, t := range tags {
		if !bytes.HasPrefix(s[i:], []byte(t[0])) {
			continue
		}
		if body, _, closed := bytes.Cut(s[i+len(t[0]):], []byte(t[1])); closed {
			return i + len(t[0]) + len(body) + len(t[1])
		}
	}
	return i + 1
}

// read visits the rows of data in file order, charging m, each cell
// marked by mk: with the dialect's own reader, or a line for each row,
// split as its layout says.
func (d *dialect) read(data []byte, m *core.Meter, mk *marker, visit func(row) error) error {
	if d.own != nil {
		return d.own(data, m, mk, visit)
	}
	return eachLine(data, m, func(n int, line []byte) error {
		f := d.layout.fields(line, len(d.columns), mk.test != nil)
		if len(f) == 0 || len(f) < d.layout.required {
			return nil
		}
		if err := mk.line(n, line); err != nil {
			return err
		}
		r := rowOf(d.columns, f, n, mk)
		if err := m.Charge(cellWork * cellCount(r)); err != nil {
			return err
		}
		if d.drops != nil && d.drops(r, len(f)) {
			return nil
		}
		return visit(r)
	})
}

// rowOf is the row of fields, the fields of line n.
func rowOf(columns []column, fields [][]byte, n int, mk *marker) row {
	r := make(row, len(columns))
	for i, c := range columns {
		r[i] = cellsOf(c, i, fields, r, n, mk)
	}
	return r
}

// cellsOf is the cells of c, column i of a row of fields at line n, whose
// columns before i are in r already.
func cellsOf(c column, i int, fields [][]byte, r row, n int, mk *marker) []cell {
	switch {
	case c.union != nil:
		return unionOf(r, c.union)
	case i >= len(fields):
		return defaultOf(c, n, mk)
	case c.list == listWords:
		return wordsOf(fields[i:], n, mk)
	case c.list != listNone:
		text := c.read(fields[i])
		return listItems(mk.cell(text, n), text, c.list)
	}
	return []cell{mk.cell(c.read(fields[i]), n)}
}

// read is the text the tool reads for field, a field of c as the file
// writes it: with fstab's octal escapes decoded, and a word the tool reads
// as another, such as crypttab's - for none, as that word.
func (c column) read(field []byte) []byte {
	if c.unmangle {
		field = unmangle(field)
	}
	if v, ok := c.means[string(field)]; ok {
		return []byte(v)
	}
	return field
}

// unionOf is the cells of the columns cols of r, in order.
func unionOf(r row, cols []int) []cell {
	var out []cell
	for _, u := range cols {
		out = append(out, r[u]...)
	}
	return out
}

// defaultOf is the cells of c when a row of line n leaves it out: its
// default, the empty value for a column the tool reads as empty, or no
// cell.
func defaultOf(c column, n int, mk *marker) []cell {
	switch {
	case c.def != "":
		return []cell{mk.cell([]byte(c.def), n)}
	case c.blank:
		return []cell{mk.cell([]byte{}, n)}
	}
	return nil
}

// wordsOf is a cell for each of words, at line n.
func wordsOf(words [][]byte, n int, mk *marker) []cell {
	out := make([]cell, 0, len(words))
	for _, w := range words {
		out = append(out, mk.cell(w, n))
	}
	return out
}

// cellCount is the cells r holds.
func cellCount(r row) int {
	n := 0
	for _, cells := range r {
		n += len(cells)
	}
	return n
}

// listItems are the items of text, a list read as k says, each at the
// line of in and with its marks, empty items left out: split at commas,
// outside double quotes for listComma, as libmount reads options, and
// with the white space glibc skips taken off the start of each member for
// listMembers. A template's expression may hold a comma, so each item
// keeps the marks of the whole field.
func listItems(in cell, text []byte, k listKind) []cell {
	var out []cell
	quoted, start := false, 0
	for i := 0; i <= len(text); i++ {
		switch {
		case i < len(text) && text[i] == '"' && k == listComma:
			quoted = !quoted
		case i == len(text) || text[i] == ',' && !quoted:
			out = appendItem(out, in, text[start:i], k)
			start = i + 1
		}
	}
	return out
}

// appendItem is out with item, a cell of in, unless it is empty.
func appendItem(out []cell, in cell, item []byte, k listKind) []cell {
	if k == listMembers {
		item = bytes.TrimLeft(item, cSpace)
	}
	if len(item) == 0 {
		return out
	}
	return append(out, sub(in, item))
}

// octal is the byte the three octal digits that start b write, as
// libmount's unmangle and nfs-utils' xgettok read \ooo, and whether b
// starts with three; a value past 255 keeps its low byte, as C's char
// does.
func octal(b []byte) (byte, bool) {
	if len(b) < 3 || !isOctal(b[0]) || !isOctal(b[1]) || !isOctal(b[2]) {
		return 0, false
	}
	return b[0]&7<<6 | b[1]&7<<3 | b[2]&7, true
}

// isOctal reports an octal digit.
func isOctal(c byte) bool { return '0' <= c && c <= '7' }

// unmangle is b with each \ooo decoded, as libmount decodes a field of
// fstab: \040 is a space.
func unmangle(b []byte) []byte {
	if bytes.IndexByte(b, '\\') < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if v, ok := octal(b[i+1:]); b[i] == '\\' && ok {
			out = append(out, v)
			i += 3
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// sub is a cell of text, a part of in's, at its line and with its marks.
func sub(in cell, text []byte) cell {
	in.text = text
	return in
}

// The exports dialect, as nfs-utils reads /etc/exports and the .exports
// files of /etc/exports.d (support/nfs/exports.c and xio.c): a # that
// starts a word starts a comment to the end of the line, and a \ right
// before the line end joins the next line to it, as one space, a quote
// left open going on across it. A word runs to white space outside double
// quotes; its quotes are dropped and each \ooo is the byte it writes. A
// line is a path and clients, each a host, a network, a wildcard or an
// @netgroup with its options in parentheses. A word that starts with -
// where a client would start adds to the default options, and the word
// after it is the client whatever it starts with, or, at the end of the
// line, the client *. Each client is one row of path, client and options,
// the defaults then its own; a client written as (options) alone, and a
// path with no client, are the client * too, every host. nfs-utils stops
// reading the file at an empty path, an option list without its ) at the
// end of the word, and an option it does not know, and so does errata,
// unless the engine of a template writes the word.

// readExports reads an exports file into rows.
func readExports(data []byte, m *core.Meter, mk *marker, visit func(row) error) error {
	x := exportLexer{mk: mk}
	err := eachLine(data, m, func(n int, line []byte) error {
		if err := mk.line(n, line); err != nil {
			return err
		}
		joined, err := x.line(n, line)
		if joined || err != nil {
			return err
		}
		return x.flush(m, visit)
	})
	if err == nil && x.open != nil {
		err = core.LineError(x.openAt, "a double quote is left open at the end of the file")
	}
	if err != nil {
		return err
	}
	return x.flush(m, visit)
}

// exportLexer gathers the words of one export, over the lines a \ joins.
type exportLexer struct {
	mk    *marker
	words []cell
	// open is a word a join left inside double quotes, as written so far,
	// which starts at line openAt; nil when there is none.
	open   []byte
	openAt int
}

// line takes the words of line n, and reports whether a \ joins the next
// line to it. A quote left open at a line end that does not join is
// refused: nfs-utils would read on over the line end, in a file it then
// reads as one its author did not write.
func (x *exportLexer) line(n int, line []byte) (bool, error) {
	body, joined := bytes.CutSuffix(line, []byte(`\`))
	raws, comment, open := exportWords(body, x.open != nil)
	joined = joined && !comment
	for k, w := range raws {
		if err := x.word(n, w, k == 0, k == len(raws)-1 && open, joined); err != nil {
			return false, err
		}
	}
	return joined, nil
}

// word takes w, a word of line n. The first word of a line goes on with
// a word a join left inside quotes; a word left inside quotes itself
// (open) waits for the next line when the line joins it, and is refused
// when it does not, unless the engine of a template writes the line.
func (x *exportLexer) word(n int, w []byte, first, open, joined bool) error {
	at := n
	if first && x.open != nil {
		w, at, x.open = append(append(x.open, ' '), w...), x.openAt, nil
	}
	switch {
	case open && joined:
		x.open, x.openAt = slices.Clone(w), at
		return nil
	case open && !x.mk.varies:
		return core.LineError(n, "a double quote is left open at the line end, where nfs-utils reads on into the next line")
	}
	x.words = append(x.words, x.mk.cell(xdecode(w), at))
	return nil
}

// flush visits the rows of the words gathered, and starts the next
// export.
func (x *exportLexer) flush(m *core.Meter, visit func(row) error) error {
	err := exportRows(x.words, m, visit)
	x.words = x.words[:0]
	return err
}

// exportWords are the words of b as written, whether a comment ended them,
// and whether the last is left inside double quotes: a word runs to a
// space or a tab outside double quotes, and a # where a word would start
// begins a comment. With cont set, b goes on with a word a join left
// inside quotes, from its first byte that is no space or tab.
func exportWords(b []byte, cont bool) (words [][]byte, comment, open bool) {
	i := 0
	if cont {
		for i < len(b) && isSpace(rune(b[i])) {
			i++
		}
		end, q := wordEnd(b, i, true)
		words, i, open = append(words, b[i:end]), end, q
	}
	for i < len(b) {
		switch {
		case isSpace(rune(b[i])):
			i++
		case b[i] == '#':
			return words, true, false
		default:
			end, q := wordEnd(b, i, false)
			words, i, open = append(words, b[i:end]), end, q
		}
	}
	return words, false, open
}

// wordEnd is where the word of b that starts at i ends, at a space or a
// tab outside double quotes, the word starting inside them when quoted is
// set, and whether it ends inside them.
func wordEnd(b []byte, i int, quoted bool) (int, bool) {
	for ; i < len(b); i++ {
		if b[i] == '"' {
			quoted = !quoted
		}
		if !quoted && isSpace(rune(b[i])) {
			return i, false
		}
	}
	return i, quoted
}

// xdecode is the word w as nfs-utils' xgettok reads it: without its double
// quotes, and each \ooo of a value below 256 the byte it writes.
func xdecode(w []byte) []byte {
	if !bytes.ContainsAny(w, "\"\\") {
		return w
	}
	out := make([]byte, 0, len(w))
	for _, c := range w {
		if c == '"' {
			continue
		}
		out = append(out, c)
		if n := len(out); n >= 4 && out[n-4] == '\\' && out[n-3] <= '3' {
			if v, ok := octal(out[n-3:]); ok {
				out = append(out[:n-4], v)
			}
		}
	}
	return out
}

// exportRows visits the rows of one export, the words of a line and the
// lines joined to it.
func exportRows(words []cell, m *core.Meter, visit func(row) error) error {
	if len(words) == 0 {
		return nil
	}
	e := export{m: m, visit: visit, path: words[0]}
	if len(e.path.text) == 0 && !e.path.varies {
		return core.LineError(e.path.line, "the path is empty, at which nfs-utils stops reading the file")
	}
	if len(words) == 1 {
		return e.emit(anyHost(e.path), nil)
	}
	for i := 1; i < len(words); i++ {
		var err error
		if i, err = e.client(words, i); err != nil {
			return err
		}
	}
	return nil
}

// export is one export's path and the default options its words set so
// far, and where its rows go.
type export struct {
	m        *core.Meter
	visit    func(row) error
	path     cell
	defaults []cell
}

// client visits the row of the client whose words start at words[i]: a
// word that starts with -, whose options add to the defaults, and then
// the client, or the client * when the line ends there. It returns the
// index of the client's word.
func (e *export) client(words []cell, i int) (int, error) {
	if w := words[i]; bytes.HasPrefix(w.text, []byte("-")) {
		opts, err := options(w, w.text[1:])
		if err != nil {
			return i, err
		}
		// Each row copies the defaults (emit), so they grow in place.
		e.defaults = append(e.defaults, opts...)
		if i++; i == len(words) {
			return i, e.emit(anyHost(w), nil)
		}
	}
	name, own, err := client(words[i])
	if err != nil {
		return i, err
	}
	return i, e.emit(name, own)
}

// emit visits the row of one client of the export, whose options are the
// defaults and then its own, charging the meter for its cells before it
// makes them: the defaults are copied for each client.
func (e *export) emit(name cell, own []cell) error {
	if err := e.m.Charge(cellWork * (2 + len(e.defaults) + len(own))); err != nil {
		return err
	}
	return e.visit(row{{e.path}, {name}, append(slices.Clip(e.defaults), own...)})
}

// client is the name of the client w writes, * when it writes none, and
// its options: the items in the parentheses after the name, which must
// end the word.
func client(w cell) (name cell, own []cell, err error) {
	i := bytes.IndexByte(w.text, '(')
	if i < 0 {
		return w, nil, nil
	}
	name = sub(w, w.text[:i])
	if i == 0 {
		name = anyHost(w)
	}
	inner := w.text[i+1:]
	if j := bytes.IndexByte(inner, ')'); (j < 0 || j != len(inner)-1) && !w.varies {
		return name, nil, core.LineError(w.line, "the client %s has no ) at the end of its options, at which nfs-utils stops reading the file", core.Quote(string(w.text)))
	}
	opts, err := options(w, bytes.TrimSuffix(inner, []byte(")")))
	return name, opts, err
}

// options are the items of text, an option list of w, as nfs-utils'
// parseopts reads them: split at commas, but for a comma before a digit
// in a squash_uids or squash_gids list, which parsesquash reads on over;
// a trailing comma ends the list, and an empty item before it is an
// option parseopts does not know. Each is an option it knows, with a
// value it reads. A template's list the engine writes is split at every
// comma and not checked.
func options(w cell, text []byte) ([]cell, error) {
	if w.varies {
		return listItems(w, text, listPlain), nil
	}
	var opts []cell
	for i := 0; i < len(text); {
		end := optionEnd(text, i)
		o := text[i:end]
		if fault := optionFault(string(o)); fault != "" {
			return nil, core.LineError(w.line, "the option %s %s, at which nfs-utils stops reading the file", core.Quote(string(o)), fault)
		}
		opts = append(opts, sub(w, o))
		i = end + 1
	}
	return opts, nil
}

// optionEnd is where the option of text that starts at i ends: at a comma,
// but for one a digit follows in a squash list.
func optionEnd(text []byte, i int) int {
	squash := bytes.HasPrefix(text[i:], []byte("squash_uids=")) || bytes.HasPrefix(text[i:], []byte("squash_gids="))
	for j := i; j < len(text); j++ {
		if text[j] == ',' && (!squash || j+1 == len(text) || !isDigit(text[j+1])) {
			return j
		}
	}
	return len(text)
}

// isDigit reports a decimal digit.
func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// optionFault is why parseopts refuses the option o, "" when it reads it:
// a keyword of exportOptions, or a key of exportKeys with = and a value
// the key's test takes.
func optionFault(o string) string {
	if slices.Contains(exportOptions, o) {
		return ""
	}
	key, value, found := strings.Cut(o, "=")
	test, known := exportKeys[key]
	switch {
	case !found || !known:
		return "is none nfs-utils knows"
	case test != nil && !test(value):
		return "has a value nfs-utils does not read"
	}
	return ""
}

// exportOptions and exportKeys are the options nfs-utils' parseopts
// reads (support/nfs/exports.c), the keywords alone and the keys that
// take a value, each with the test of the values it reads, nil for a
// value errata does not check.
var (
	exportOptions = []string{
		"ro", "rw", "secure", "insecure", "sync", "async", "wdelay", "no_wdelay", "hide", "nohide",
		"crossmnt", "nocrossmnt", "subtree_check", "no_subtree_check", "root_squash", "no_root_squash",
		"all_squash", "no_all_squash", "secure_locks", "insecure_locks", "auth_nlm", "no_auth_nlm",
		"acl", "no_acl", "mountpoint", "mp", "nordirplus", "security_label", "pnfs", "no_pnfs", "sign_fh",
	}
	exportKeys = map[string]func(string) bool{
		"anonuid": decimal, "anongid": decimal, "fsid": fsid,
		"sec":        oneOf(":", "krb5", "krb5i", "krb5p", "unix", "sys", "null", "none"),
		"xprtsec":    oneOf(":", "none", "tls", "mtls"),
		"reexport":   oneOf("", "auto-fsidnum", "predefined-fsidnum", "none"),
		"mountpoint": nil, "mp": nil, "refer": nil, "replicas": nil, "squash_uids": nil, "squash_gids": nil,
	}
)

// decimal reports a value strtol reads whole in base 10, as anonuid= and
// anongid= take it: white space, a sign, and at least one digit, with
// nothing after them.
func decimal(v string) bool {
	_, digits, rest := splitNumber(bytes.TrimLeft([]byte(v), cSpace))
	return len(digits) > 0 && len(rest) == 0
}

// fsid reports a value fsid= takes: root, a number strtoul reads whole in
// base 0, or a UUID, which nfs-utils' valid_uuid reads as any text of 32
// hex digits.
func fsid(v string) bool {
	if v == "root" || cNumber(v) {
		return true
	}
	hex := 0
	for _, c := range []byte(v) {
		if isHex(c) {
			hex++
		}
	}
	return hex == 32
}

// cNumber reports a value strtoul reads whole in base 0: after white
// space and a sign, 0x and hex digits, 0 and octal digits, or decimal
// digits.
func cNumber(v string) bool {
	b := bytes.TrimLeft([]byte(v), cSpace)
	if len(b) > 0 && (b[0] == '+' || b[0] == '-') {
		b = b[1:]
	}
	b, digit := cBase(b)
	return len(b) > 0 && !slices.ContainsFunc(b, func(c byte) bool { return !digit(c) })
}

// cBase is b, the digits of a number strtoul reads in base 0, without
// its 0x, and the test of its digits: hex after 0x, octal after a 0,
// decimal otherwise.
func cBase(b []byte) ([]byte, func(byte) bool) {
	switch {
	case bytes.HasPrefix(b, []byte("0x")) || bytes.HasPrefix(b, []byte("0X")):
		return b[2:], isHex
	case len(b) > 0 && b[0] == '0':
		return b, isOctal
	}
	return b, isDigit
}

// isHex reports a hex digit.
func isHex(c byte) bool { return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }

// oneOf is the test of a value whose parts, split at sep, or the whole
// value when sep is "", are each one of words.
func oneOf(sep string, words ...string) func(string) bool {
	return func(v string) bool {
		parts := []string{v}
		if sep != "" {
			parts = strings.Split(v, sep)
		}
		for _, p := range parts {
			if !slices.Contains(words, p) {
				return false
			}
		}
		return true
	}
}

// anyHost is the client * at the line of c: every host, which nfs-utils
// reads for a client that names none.
func anyHost(c cell) cell { return sub(c, []byte(anyClient)) }

// anyClient is the client that names every host.
const anyClient = "*"

// The dialects. Each column is named as the table's man page names it.

// fstab, as util-linux's libmount reads it (fstab(5) and tab_parse.c):
// fields split at white space, a line whose first field starts with # a
// comment, a row of three fields at least, each \ooo in the first four
// decoded, no option when a row leaves them out, freq and passno 0 when it
// leaves them out, and a row whose freq or passno is no number passed over.
var fstabDialect = &dialect{
	name:  "fstab",
	names: named("fstab"),
	columns: []column{
		{name: "spec", unmangle: true}, {name: "file", unmangle: true}, {name: "vfstype", unmangle: true},
		{name: "mntops", list: listComma, unmangle: true},
		{name: "freq", def: "0"}, {name: "passno", def: "0"},
	},
	layout: layout{required: 3},
	drops:  badNumbers,
}

// badNumbers reports an fstab row of fields fields whose freq or passno
// libmount refuses, and sets each it reads to the number it reads
// (mountNumber); a template's may render as a number, so it stays as it
// is.
func badNumbers(r row, fields int) bool {
	for i := 4; i < 6 && i < fields; i++ {
		c := &r[i][0]
		if c.varies {
			continue
		}
		v, ok := mountNumber(c.text, i+1 < fields)
		if !ok {
			return true
		}
		c.text = v
	}
	return false
}

// mountNumber is the number libmount's next_s32 reads from t, a freq or
// a passno field, followed by another field or not, and whether libmount
// reads the row: strtol's sign and decimal digits, past 64 bits the long
// it clamps to, kept to its low 32 bits as an int keeps it. A field with
// no digit, or with anything after its digits, fails the row, since
// next_s32 then leaves the line where the read stopped; so does a number
// past 64 bits with more of the line after it.
func mountNumber(t []byte, followed bool) ([]byte, bool) {
	neg, digits, rest := splitNumber(t)
	if len(digits) == 0 || len(rest) > 0 {
		return nil, false
	}
	n, err := strconv.ParseInt(string(neg)+string(digits), 10, 64)
	if err != nil && followed {
		return nil, false
	}
	return []byte(strconv.FormatInt(low32(n), 10)), true
}

// low32 is n kept to its low 32 bits, read as a signed number, as C
// stores a long in an int.
func low32(n int64) int64 {
	n &= 1<<32 - 1
	if n >= 1<<31 {
		n -= 1 << 32
	}
	return n
}

// splitNumber is t as strtol reads it: its sign, "-" or none, the decimal
// digits after it, and the rest.
func splitNumber(t []byte) (neg, digits, rest []byte) {
	s := t
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		if s[0] == '-' {
			neg = s[:1]
		}
		s = s[1:]
	}
	i := 0
	for i < len(s) && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	return neg, s[:i], s[i:]
}

// badIDs is the test of a passwd or group row whose columns ids glibc
// reads as numbers: it reports a row glibc passes over and sets each id
// to the number glibc reads (glibcID).
//
// A row whose name starts with + or - is one of nss compat's, which glibc
// reads with INT_FIELD_MAYBE_NULL: an empty id is 0. passwd's needs a : after
// the empty id, which atEnd says it may lack in group's.
func badIDs(atEnd bool, ids ...int) func(row, int) bool {
	// Each id is a column the layout requires, so every row has it.
	return func(r row, fields int) bool {
		compat := bytes.HasPrefix(r[0][0].text, []byte("+")) || bytes.HasPrefix(r[0][0].text, []byte("-"))
		for _, i := range ids {
			if !readID(&r[i][0], compat && (atEnd || i+1 < fields)) {
				return true
			}
		}
		return false
	}
}

// readID sets c, an id, to the number glibc reads, an empty one 0 when
// maybeNull is set, and reports false for a row glibc passes over. A
// template's id the engine writes stays as it is.
func readID(c *cell, maybeNull bool) bool {
	switch {
	case c.varies:
		return true
	case maybeNull && len(c.text) == 0:
		c.text = []byte("0")
		return true
	}
	v, ok := glibcID(c.text)
	if ok {
		c.text = v
	}
	return ok
}

// glibcID is the number glibc's INT_FIELD reads from t, a uid or a gid,
// and whether it reads one: strtoul skips white space and takes a sign
// and decimal digits, which must run to the end of the field, and the
// value must fit 32 bits, so -0 is 0 and -1, past 32 bits once strtoul
// negates it, fails the row.
func glibcID(t []byte) ([]byte, bool) {
	neg, digits, rest := splitNumber(bytes.TrimLeft(t, cSpace))
	if len(digits) == 0 || len(rest) > 0 {
		return nil, false
	}
	n, err := strconv.ParseUint(string(digits), 10, 32)
	if err != nil || len(neg) > 0 && n != 0 {
		return nil, false
	}
	return []byte(strconv.FormatUint(n, 10)), true
}

// passwd, as glibc's nss_files reads it (passwd(5) and fgetpwent_r.c):
// fields split at :, the shell the rest of the line, a line that starts
// with # after white space a comment, and a row of four fields at least,
// gecos, directory and shell empty when it leaves them out.
var passwdDialect = &dialect{
	name:  "passwd",
	names: named("passwd"),
	columns: []column{
		{name: "name"}, {name: "password"}, {name: "uid"}, {name: "gid"},
		{name: "gecos", blank: true}, {name: "directory", blank: true}, {name: "shell", blank: true},
	},
	layout: layout{colon: true, required: 4},
	drops:  badIDs(false, 2, 3),
}

// group, as glibc's nss_files reads it (group(5) and fgetgrent_r.c):
// fields split at :, a row of three fields at least, and the members the
// rest of the line, split at commas, the white space before each skipped.
var groupDialect = &dialect{
	name:    "group",
	names:   named("group"),
	columns: []column{{name: "group_name"}, {name: "password"}, {name: "gid"}, {name: "user_list", list: listMembers}},
	layout:  layout{colon: true, required: 3},
	drops:   badIDs(true, 2),
}

// hosts, as glibc's nss_files reads it (hosts(5) and files-hosts.c): a #
// anywhere starts a comment, fields split at white space, the aliases
// every word after the canonical name, and names the canonical name and
// the aliases, all compared in any case as glibc compares them. A row
// whose address is no IPv4 or IPv6 address is passed over, as glibc
// passes it over.
var hostsDialect = &dialect{
	name:  "hosts",
	names: named("hosts"),
	columns: []column{
		{name: "ip_address"}, {name: "canonical_hostname", fold: true},
		{name: "aliases", list: listWords, fold: true}, {name: "names", fold: true, union: []int{1, 2}},
	},
	layout: layout{hashAnywhere: true, required: 2},
	drops:  badAddress,
}

// badAddress reports a hosts row whose address inet_pton does not read,
// which glibc passes over; a template's may render as one.
func badAddress(r row, _ int) bool {
	c := r[0][0]
	a, err := netip.ParseAddr(string(c.text))
	return (err != nil || a.Zone() != "") && !c.varies
}

// exports, read by readExports (exports(5)).
var exportsDialect = &dialect{
	name: "exports",
	names: func(dirs []string, base string) bool {
		stem, ok := strings.CutSuffix(base, ".exports")
		return base == "exports" || ok && stem != "" && dirs[len(dirs)-1] == "exports.d"
	},
	columns: []column{{name: "path"}, {name: "client", def: anyClient}, {name: "options", list: listPlain}},
	own:     readExports,
}

// crypttab, as systemd-cryptsetup-generator reads it (crypttab(5)): fields
// split at white space, a line whose first field starts with # a comment,
// the key file none when a row leaves it out or writes -, and no option
// when it leaves them out.
var crypttabDialect = &dialect{
	name:  "crypttab",
	names: named("crypttab"),
	columns: []column{
		{name: "target"}, {name: "source"}, {name: "key_file", def: "none", means: map[string]string{"-": "none"}}, {name: "options", list: listComma, means: map[string]string{"-": "", "none": ""}},
	},
	layout: layout{required: 2},
}
