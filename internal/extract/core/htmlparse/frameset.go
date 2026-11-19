package htmlparse

import "bytes"

// A <frameset> takes the page only while the HTML standard's frameset-ok
// flag holds (13.2.6.4.7, "in body"): before the body starts, or in a body
// that holds no text but blanks and none of the elements that clear the
// flag. There it replaces the body, and the tree builder keeps nothing
// after it but comments, so the scanner gives nothing else. Anywhere else
// a browser ignores a <frameset>, as it ignores a <frame> outside a
// frameset, and neither opens an element: <pre><frameset> and a line feed
// leave the line feed the pre's start tag's. A Markdown file's HTML block,
// which a site renders inside its body, takes no frameset.
//
// x/net v0.62.0, the browser tests' oracle, sets the flag back to ok at a
// body it implies (parse.go, afterHeadIM), where the standard keeps it, so
// a template in the head clears it for the standard alone. notOK holds
// the standard's flag first and x/net's second; where the two decide a
// frameset apart, the scan is marked StandardOnly.

// frames follows the frameset-ok flag of a page.
type frames struct {
	// body is whether the body started, afterHead whether a </head> came
	// before it.
	body, afterHead bool
	// notOK is whether the flag no longer holds, by the standard and by
	// x/net.
	notOK [2]bool
	// took is whether a frameset took the page; standardOnly whether the
	// standard and x/net decided a frameset apart.
	took, standardOnly bool
}

// htmlBlank are the HTML standard's blanks.
const htmlBlank = " \t\n\f\r"

// headTags are the start tags a browser reads in the head, before the body
// starts: in head or after head, but noscript, which only in head stays
// there (staysInHead).
var headTags = set(`base basefont bgsound head html link meta noframes script style template title`)

// clearsFlag are the start tags that clear the frameset-ok flag in the
// body; an input does unless its type is hidden. A </br> reads as a <br>.
var clearsFlag = set(`applet area body br button dd dt embed hr iframe image img keygen li listing
	marquee object pre select table textarea wbr xmp`)

// insideBody is the flag of a Markdown file's HTML block: inside a body,
// which no frameset takes.
func insideBody() frames { return frames{body: true, notOK: [2]bool{true, true}} }

// startBody follows a token that starts the body, or one inside it. x/net
// sets its flag back to ok at the start.
func (f *frames) startBody() {
	if !f.body {
		f.body, f.notOK[1] = true, false
	}
}

// clear clears the flag by the standard when std holds and by x/net when
// xnet does.
func (f *frames) clear(std, xnet bool) {
	f.notOK[0] = f.notOK[0] || std
	f.notOK[1] = f.notOK[1] || xnet
}

// text follows text a browser reads as the page's, not as a raw element's,
// b with its character references decoded: any but blanks starts the
// body, and any but blanks and NULs, which a browser drops, clears the
// flag.
func (f *frames) text(b []byte) {
	if len(bytes.Trim(b, htmlBlank)) == 0 {
		return
	}
	f.startBody()
	if len(bytes.Trim(b, htmlBlank+"\x00")) > 0 {
		f.clear(true, true)
	}
}

// staysInHead reports whether a start tag of name, before the body, keeps
// the browser in the head.
func (f *frames) staysInHead(name string) bool {
	return headTags[name] || name == "noscript" && !f.afterHead
}

// start follows an HTML start tag of name in the page but a frameset's or
// a frame's, typeHidden saying whether an input's type is hidden. A
// template clears the flag, in the head too.
func (f *frames) start(name string, typeHidden bool) {
	switch {
	case name == "template":
		f.clear(true, true)
	case !f.body && f.staysInHead(name):
	case name == "input":
		f.startBody()
		f.clear(!typeHidden, !typeHidden)
	default:
		f.startBody()
		f.clear(clearsFlag[name], clearsFlag[name])
	}
}

// end follows an HTML end tag of name in the page.
func (f *frames) end(name string) {
	switch name {
	case "br":
		f.startBody()
		f.clear(true, true)
	case "body", "html":
		f.startBody()
	case "head":
		f.afterHead = true
	}
}

// frame follows the start tag of a frameset or a frame, and reports
// whether it takes the page: a frameset before the body, or in it while
// the flag holds, by x/net's flag when xnet is true. A frame starts the
// body and takes nothing.
func (f *frames) frame(name string, xnet bool) bool {
	if name == "frame" {
		f.startBody()
		return false
	}
	if f.body && f.notOK[0] != f.notOK[1] {
		f.standardOnly = true
	}
	way := 0
	if xnet {
		way = 1
	}
	f.took = !f.body || !f.notOK[way]
	return f.took
}

// frameTag follows the start tag of a frameset or a frame in the page,
// and reports true: Next gives it. A frameset that takes the page closes
// everything open, as the body goes; any other is ignored.
func (s *Scanner) frameTag() bool {
	n := &s.nest
	n.closedLine, n.ignored = false, true
	if s.frames.frame(s.name, n.xnet) {
		n.ignored = false
		n.reset()
	}
	return true
}

// inputHidden reports whether the start tag is an input's whose type is
// hidden, in any case. The tokenizer keeps the first of an attribute set
// twice, as the standard does.
func (s *Scanner) inputHidden() bool {
	if s.name != "input" {
		return false
	}
	s.readAttrs()
	return s.typeHidden
}

// Framed reports whether a frameset took the page. A browser then draws
// nothing of the body before it, which holds at most empty headings, and
// nothing after it. The browser tests call it; no reader calls it.
func (s *Scanner) Framed() bool { return s.frames.took }
