package htmlparse

import (
	"fmt"

	"github.com/f-klement/errata/internal/extract/core"
)

// nest follows the elements open in a block of HTML, from its start, as a
// browser's tree builder follows them, so that a pre, a listing and, when
// the scanner follows headings, a heading each end where a browser ends
// them: at their own end tag, at the end tag of an element open around or
// inside them, or, for a heading, at the start of the next one. An end tag
// closes the nearest element of its name in scope and every one above it;
// the start of a p's closer, a heading, a list item, a table, a row, a
// cell, a table section or an option closes what a browser closes; html,
// head and body are always open and none of their tags closes anything.
// When the scanner reads several blocks, each read as alone, an element
// may have opened outside the block, in the Markdown around it: an end tag
// of an element the block does not hold, which may close a pre or a
// heading open, is refused rather than guessed (NestError). In one file of
// HTML such a tag closes nothing, as in a browser. Each element costs a
// constant: the indexes below are stacks kept as the elements open and
// close, and at most core.MaxDepth elements are open.
type nest struct {
	headings bool
	// outside is whether elements may have opened before the block, and
	// quirks whether the document is in a browser's quirks mode, which
	// the page's first doctype sets (doctypeQuirks).
	outside, quirks bool
	// ignored is whether the last tag opened and closed nothing, as a
	// browser ignores a stray end tag; </p> and </br> make an element.
	// closedLine is whether it closed an element a browser starts a line
	// at, as </listing> closes a div open inside it.
	ignored, closedLine bool
	stack               []string
	// pre and heads count the pre or listing elements and the headings
	// open.
	pre, heads int
	// pos holds, for each name, the indexes in stack of its elements and
	// the name's kind, found once; open, for each element of stack, its
	// name's entry. The rest are indexes of the elements of a kind, each
	// in order.
	pos      map[string]*opened
	open     []*opened
	bounds   []int // the elements that end a scope (scopeEnds)
	buttons  []int // those and button, which end a p's scope
	specials []int // the elements of special
	notADP   []int // the special elements but address, div and p, where a list item's search stops
	heading  []int
	tabular  []int // table, td, th and caption
	sections []int // tbody, thead and tfoot
	// afe is the browser's list of active formatting elements, which it
	// reopens after a block's end closed them: each formatting element,
	// with its index in stack while open and -1 once closed so, and a
	// marker, at -2, for each cell, caption and object open.
	afe []afeEntry
	// afeAt is, for each element of stack, the index in afe of its entry,
	// or -1, so that closing one costs a constant.
	afeAt []int
}

// opened is the indexes in stack of the elements of one name, and the
// kind of element the name is.
type opened struct {
	at []int
	// kinded is whether the elements are on one of nest's kind stacks
	// (kinds), line whether LineTag holds, and marker whether each puts a
	// marker in the list of active formatting elements.
	kinded, line, marker bool
}

// posOf is the indexes in stack of the elements of name, in order.
func (n *nest) posOf(name string) []int {
	if e := n.pos[name]; e != nil {
		return e.at
	}
	return nil
}

type afeEntry struct {
	name string
	at   int
}

func (n *nest) reset() {
	for len(n.stack) > 0 {
		n.pop()
	}
	n.afe = n.afe[:0]
}

// afeLast is the index in afe of the last element of name after the last
// marker, or -1.
func (n *nest) afeLast(name string) int {
	for j := len(n.afe) - 1; j >= 0 && n.afe[j].at != -2; j-- {
		if n.afe[j].name == name {
			return j
		}
	}
	return -1
}

// afeRemove removes entry j of afe.
func (n *nest) afeRemove(j int) {
	if at := n.afe[j].at; at >= 0 {
		n.afeAt[at] = -1
	}
	n.afe = append(n.afe[:j], n.afe[j+1:]...)
	for m := j; m < len(n.afe); m++ {
		if at := n.afe[m].at; at >= 0 {
			n.afeAt[at] = m
		}
	}
}

// reconstruct reopens, as a browser does before text and before most
// inline start tags, the formatting elements a block's end closed after
// the last marker, in their order: the HTML standard's reconstruction of
// the active formatting elements. It returns a refusal past the depth
// limit, or "".
func (n *nest) reconstruct() string {
	k := len(n.afe)
	if k == 0 || n.afe[k-1].at != -1 {
		return "" // the last is a marker or open: nothing to reopen
	}
	j := k - 1
	for j > 0 && n.afe[j-1].at == -1 {
		j--
	}
	for ; j < k; j++ {
		if len(n.stack) >= core.MaxDepth {
			return fmt.Sprintf("nested more than %d levels deep, the limit; %s", core.MaxDepth, core.OtherWay)
		}
		n.afe[j].at = n.pushElement(n.afe[j].name)
		n.afeAt[n.afe[j].at] = j
	}
	return ""
}

// reconstructs reports whether a start tag of name reopens the formatting
// elements first, as a browser's "in body" mode does for inline elements
// and the few special ones the standard names, but not for a block.
func reconstructs(name string) bool {
	return !special[name] && !isHeading(name) || reopenFirst[name]
}

// reopenFirst are the special elements whose start tags reconstruct.
var reopenFirst = set(`applet marquee object area br embed img keygen wbr input select button xmp`)

// watching reports whether a pre or a followed heading is open.
func (n *nest) watching() bool { return n.pre > 0 || n.headings && n.heads > 0 }

// formattingEnd follows the end tag of a formatting element in afe, entry
// j, as the adoption agency does, in its order: one closed by a block's end
// leaves the list; one out of scope, past a table or a cell, stays and the
// tag is ignored; one open with no special element inside it closes; one
// with a special element open inside it the browser rebuilds around that
// block, which errata does not follow, and refuses inside a pre or a
// heading, where the rebuild could move text. It returns a refusal or "".
func (n *nest) formattingEnd(name string, j int) string {
	at := n.afe[j].at
	switch {
	case at < 0:
	case n.last(n.bounds) > at:
		return "" // out of scope, past a table or a cell: a browser ignores it
	case n.last(n.specials) > at:
		if n.watching() {
			return fmt.Sprintf("</%s> ends a formatting element around a block still open inside it, which a browser rebuilds in a way errata does not follow; close the elements in order; %s", name, core.OtherWay)
		}
	default:
		n.popThrough(at)
	}
	n.afeRemove(n.afeLast(name))
	return ""
}

// start follows a start tag of name, which opens an element unless opens
// is false, for a void element or a self closing one inside svg or math.
// It returns a refusal, or "".
func (n *nest) start(name string, opens bool) string {
	n.closedLine = false
	n.ignored = alwaysOpen[name]
	if n.ignored {
		return ""
	}
	if closesP[name] && (name != "table" || !n.quirks) { // in quirks mode a table opens in a p
		if i := n.last(n.posOf("p")); i > n.last(n.buttons) {
			n.popThrough(i)
		}
	}
	switch name {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if isHeading(n.top()) {
			n.pop()
		} else if n.headings && len(n.heading) > 0 {
			return "a heading opens inside a heading still open, which errata does not read; end the heading first; " + core.OtherWay
		}
	case "a":
		// A link ends a link open, as the adoption agency does. One out of
		// scope, past a table, the agency leaves, and a browser then takes
		// it off the list and the stack, whatever it holds.
		if j := n.afeLast("a"); j >= 0 {
			if at := n.afe[j].at; at >= 0 && n.last(n.bounds) > at {
				n.afeRemove(j)
				n.remove(at)
			} else if err := n.formattingEnd("a", j); err != "" {
				return err
			}
		}
	case "li", "dd", "dt":
		if i := n.last(n.notADP); i >= 0 && sameItem(n.stack[i], name) {
			n.popThrough(i)
		}
	case "table":
		// A table in a table outside a cell ends it.
		if i := n.last(n.tabular); i >= 0 && n.stack[i] == "table" {
			n.popThrough(i)
		}
	case "td", "th", "tr", "tbody", "thead", "tfoot":
		if !n.tablePart(name) {
			n.ignored = true // outside a table a browser ignores it
			return ""
		}
	case "option", "optgroup":
		if n.top() == "option" {
			n.pop()
		}
		if name == "optgroup" && n.top() == "optgroup" {
			n.pop()
		}
	}
	if reconstructs(name) {
		if err := n.reconstruct(); err != "" {
			return err
		}
	}
	switch {
	case !opens:
	case len(n.stack) >= core.MaxDepth:
		return fmt.Sprintf("nested more than %d levels deep, the limit; %s", core.MaxDepth, core.OtherWay)
	default:
		n.push(name)
	}
	return ""
}

// tablePart clears what a table part's start tag closes, as a browser's
// table modes do: a cell or a caption open, then everything above the row
// for a cell, above the section for a row, above the table for a section.
// It reports false outside a table, where a browser ignores the tag.
func (n *nest) tablePart(name string) bool {
	t := n.last(n.posOf("table"))
	if t < 0 {
		return false
	}
	if i := n.last(n.tabular); n.stack[i] != "table" {
		n.popThrough(i) // the cell or caption open in this table
	}
	keep := t // the element the part goes in
	switch name {
	case "td", "th":
		if r := n.last(n.posOf("tr")); r > t {
			keep = r
		}
	case "tr":
		if r := n.last(n.posOf("tr")); r > t {
			n.popThrough(r)
		}
		if sec := n.last(n.sections); sec > t {
			keep = sec
		}
	}
	n.popThrough(keep + 1)
	// A row goes in a section and a cell in a row: a browser opens the
	// tbody and the tr a page leaves out, which </tbody> and </tr> close.
	if (name == "tr" || name == "td" || name == "th") && keep == t {
		n.push("tbody")
	}
	if (name == "td" || name == "th") && n.top() != "tr" {
		n.push("tr")
	}
	return true
}

// end follows an end tag of name, and returns a refusal, or "".
func (n *nest) end(name string) string {
	n.closedLine = false
	depth := len(n.stack)
	defer func() { n.ignored = len(n.stack) == depth && name != "p" && name != "br" }()
	if j := n.afeLast(name); formatting[name] && j >= 0 {
		return n.formattingEnd(name, j)
	}
	i := n.last(n.posOf(name))
	if isHeading(name) {
		i = n.last(n.heading)
	}
	switch {
	case i >= 0 && !special[name] && !isHeading(name):
		if n.last(n.specials) <= i {
			n.popThrough(i)
		}
	case i >= 0:
		blocks := n.bounds
		switch {
		case name == "p":
			blocks = n.buttons
		case tableParts[name]:
			// A table's parts close in table scope, which only a table
			// ends: </table> closes the cell and the row open in it.
			blocks = n.posOf("table")
			if name == "table" {
				blocks = nil
			}
		}
		bound := n.last(blocks)
		if name == "li" { // list item scope: a list ends it too
			bound = max(bound, n.last(n.posOf("ul")), n.last(n.posOf("ol")))
		}
		if bound <= i {
			n.popThrough(i)
		}
	case !n.outside || n.pre == 0 && (!n.headings || n.heads == 0):
		// No element of the name is open in the file, and a browser
		// ignores the tag; or none is open that the tag might end.
	case name == "p" || alwaysOpen[name] || !special[name] && !isHeading(name) || len(n.bounds) > 0 || holdsNoElement(name):
		// A browser ignores it, or inserts an empty p for </p>, whatever
		// is open outside the block: an element of no special kind never
		// closes a special one, a scope's end hides what lies beyond it,
		// and an element that holds text alone or nothing is no ancestor.
	default:
		what := "pre"
		if n.pre == 0 {
			what = "heading"
		}
		return fmt.Sprintf("</%s> may end the %s open inside it through an element opened before the HTML block, which errata cannot tell from the block alone; end the %s first; %s",
			name, what, what, core.OtherWay)
	}
	return ""
}

// top is the element on top, or "".
func (n *nest) top() string {
	if len(n.stack) == 0 {
		return ""
	}
	return n.stack[len(n.stack)-1]
}

// last is the last index of idx, or -1.
func (n *nest) last(idx []int) int {
	if len(idx) == 0 {
		return -1
	}
	return idx[len(idx)-1]
}

// push opens an element of name, and enters a formatting element in the
// list of active formatting elements, or a marker.
func (n *nest) push(name string) {
	i := n.pushElement(name)
	switch {
	case formatting[name]:
		// The Noah's Ark clause: three alike after the last marker at most.
		var alike, first int
		for j := len(n.afe) - 1; j >= 0 && n.afe[j].at != -2; j-- {
			if n.afe[j].name == name {
				alike, first = alike+1, j
			}
		}
		if alike >= 3 {
			n.afeRemove(first)
		}
		n.afeAt[i] = len(n.afe)
		n.afe = append(n.afe, afeEntry{name: name, at: i})
	case markers[name]:
		n.afe = append(n.afe, afeEntry{at: -2})
	}
}

// pushElement opens an element of name on the stack, and returns its index.
func (n *nest) pushElement(name string) int {
	i := len(n.stack)
	n.stack = append(n.stack, name)
	n.afeAt = append(n.afeAt, -1)
	e := n.pos[name]
	if e == nil {
		if n.pos == nil {
			n.pos = map[string]*opened{}
		}
		e = &opened{kinded: special[name] || isHeading(name), line: LineTag(name), marker: markers[name]}
		n.pos[name] = e
	}
	e.at = append(e.at, i)
	n.open = append(n.open, e)
	if e.kinded {
		for _, k := range n.kinds(name) {
			*k = append(*k, i)
		}
	}
	n.count(name, 1)
	return i
}

func (n *nest) pop() {
	i := len(n.stack) - 1
	name := n.stack[i]
	n.stack = n.stack[:i]
	e := n.open[i]
	n.open = n.open[:i]
	e.at = e.at[:len(e.at)-1]
	if e.kinded {
		for _, k := range n.kinds(name) {
			*k = (*k)[:len(*k)-1]
		}
	}
	n.count(name, -1)
	n.closedLine = n.closedLine || e.line
	if j := n.afeAt[i]; j >= 0 {
		n.afe[j].at = -1 // closed by a block's end: the browser reopens it
	}
	n.afeAt = n.afeAt[:i]
	if e.marker {
		for j := len(n.afe) - 1; j >= 0; j-- {
			if n.afe[j].at == -2 {
				n.afe = n.afe[:j]
				break
			}
		}
	}
}

// remove takes the element at index i, a link, off the stack, not one
// above it, each of which moves down one. A link is on no kind's stack
// (kinds) and counts as no pre or heading. It costs the elements above i,
// each of which opened after the link, so each element moves once.
func (n *nest) remove(i int) {
	// No link is open above it: one opened later would have ended it
	// first, or opened past a marker and closed with it.
	e := n.open[i]
	e.at = e.at[:len(e.at)-1]
	n.stack = append(n.stack[:i], n.stack[i+1:]...)
	n.open = append(n.open[:i], n.open[i+1:]...)
	n.afeAt = append(n.afeAt[:i], n.afeAt[i+1:]...)
	for j := i; j < len(n.stack); j++ {
		o := n.open[j]
		for m := len(o.at) - 1; m >= 0; m-- {
			if o.at[m] == j+1 {
				o.at[m] = j
				break
			}
		}
		if a := n.afeAt[j]; a >= 0 {
			n.afe[a].at = j
		}
	}
	for _, k := range []*[]int{&n.bounds, &n.buttons, &n.specials, &n.notADP, &n.heading, &n.tabular, &n.sections} {
		for m := len(*k) - 1; m >= 0 && (*k)[m] > i; m-- {
			(*k)[m]--
		}
	}
}

// popThrough pops the element at index i and every one above it.
func (n *nest) popThrough(i int) {
	for len(n.stack) > i {
		n.pop()
	}
}

// kinds are the index stacks an element of name is on.
func (n *nest) kinds(name string) []*[]int {
	var out []*[]int
	if scopeEnds[name] {
		out = append(out, &n.bounds, &n.buttons)
	} else if name == "button" {
		out = append(out, &n.buttons)
	}
	if special[name] || isHeading(name) {
		out = append(out, &n.specials)
	}
	if (special[name] || isHeading(name)) && name != "address" && name != "div" && name != "p" {
		out = append(out, &n.notADP)
	}
	if isHeading(name) {
		out = append(out, &n.heading)
	}
	if name == "table" || name == "td" || name == "th" || name == "caption" {
		out = append(out, &n.tabular)
	}
	if name == "tbody" || name == "thead" || name == "tfoot" {
		out = append(out, &n.sections)
	}
	return out
}

// count moves the counts of pre elements and headings by d for name.
func (n *nest) count(name string, d int) {
	switch {
	case name == "pre" || name == "listing":
		n.pre += d
	case isHeading(name):
		n.heads += d
	}
}

// sameItem reports whether an open element of name ends at the start of a
// list item of item: li at li, dd or dt at dd or dt.
func sameItem(name, item string) bool {
	if item == "li" {
		return name == "li"
	}
	return name == "dd" || name == "dt"
}

// holdsNoElement reports whether an element of name holds no element: a
// void one, or one whose text the tokenizer reads raw.
func holdsNoElement(name string) bool { return void[name] || hidden[name] || name == "textarea" }

// isHeading reports whether name is h1 to h6.
func isHeading(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

// formatting are the HTML standard's formatting elements, which a browser
// keeps in its list of active formatting elements.
var formatting = set(`a b big code em font i nobr s small strike strong tt u`)

// markers are the elements that put a marker in that list.
var markers = set(`td th caption applet object marquee`)

// tableParts are the elements whose end tags close in table scope.
var tableParts = set(`table tbody thead tfoot tr td th`)

// alwaysOpen are the elements a browser holds open whatever their tags
// say: their start tags open nothing more and their end tags close
// nothing.
var alwaysOpen = set(`html head body`)

// closesP are the start tags that close a p open in its scope.
var closesP = set(`address article aside blockquote center details dialog dir div dl fieldset
	figcaption figure footer header hgroup main menu nav ol p search section summary ul h1 h2 h3 h4
	h5 h6 pre listing form plaintext table hr xmp li dd dt`)

// scopeEnds are the elements that end an element's scope in a browser.
var scopeEnds = set(`applet caption html table td th marquee object template`)

// special are the elements of the HTML standard's special category, but
// the headings, which isHeading names: no end tag of an element outside it
// closes one.
var special = set(`address applet area article aside base basefont bgsound blockquote body br
	button caption center col colgroup dd details dir div dl dt embed fieldset figcaption figure
	footer form frame frameset head header hgroup hr html iframe img input keygen li link listing
	main marquee menu meta nav noembed noframes noscript object ol p param plaintext pre script
	search section select source style summary table tbody td template textarea tfoot th thead
	title tr track ul wbr xmp`)
