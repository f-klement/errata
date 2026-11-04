package html

import "github.com/f-klement/errata/internal/extract/core/htmlparse"

// prose is the text a reading builds from pieces of the file, with the
// offset in the file where each piece starts, so that an offset in the text
// maps back to a line of the file. A piece is either bytes copied from the
// file or bytes errata wrote, a space, a newline or the text a character
// reference stands for, which map to the offset they stand for.
type prose struct {
	text   []byte
	pieces []piece
	// maxPieces is the most pieces the prose may hold; over is whether it
	// ran out, and overAt the offset of the file where it did.
	maxPieces int
	over      bool
	overAt    int
}

type piece struct {
	at    int  // where the piece starts in the text
	from  int  // where it starts in the file
	wrote bool // whether errata wrote it rather than copied it
}

// add appends b, which starts at offset from of the file, or stands for it
// when wrote is set. A copied piece that goes on from the one before in the
// file joins it.
func (p *prose) add(b []byte, from int, wrote bool) {
	if len(b) == 0 || p.over {
		return
	}
	n := len(p.pieces)
	if wrote || n == 0 || p.pieces[n-1].wrote || p.pieces[n-1].from+len(p.text)-p.pieces[n-1].at != from {
		if len(p.pieces) >= p.maxPieces {
			p.over, p.overAt = true, from
			return
		}
		p.pieces = append(p.pieces, piece{at: len(p.text), from: from, wrote: wrote})
	}
	p.text = append(p.text, b...)
}

// truncate cuts the text back to its first n bytes.
func (p *prose) truncate(n int) {
	p.text = p.text[:min(n, len(p.text))]
	for len(p.pieces) > 0 && p.pieces[len(p.pieces)-1].at >= n {
		p.pieces = p.pieces[:len(p.pieces)-1]
	}
}

// offset is the offset in the file of offset at of the text, 0 for an
// empty text.
func (p *prose) offset(at int) int {
	if len(p.pieces) == 0 {
		return 0
	}
	lo, hi := 0, len(p.pieces)
	for lo+1 < hi { // the last piece that starts at or before at
		mid := (lo + hi) / 2
		if p.pieces[mid].at <= at {
			lo = mid
		} else {
			hi = mid
		}
	}
	pc := p.pieces[lo]
	if pc.wrote {
		return pc.from
	}
	return pc.from + at - pc.at
}

// atSpace reports whether the text is empty or ends in a space or a
// newline, where a browser shows no further space.
func (p *prose) atSpace() bool {
	n := len(p.text)
	return n == 0 || p.text[n-1] == ' ' || p.text[n-1] == '\n'
}

// space appends a space that stands for offset from, unless atSpace.
func (p *prose) space(from int) {
	if !p.atSpace() {
		p.add([]byte{' '}, from, true)
	}
}

// endLine ends a line of the text: it drops a space at its end, then
// appends a newline, unless the text is empty or ends in one.
func (p *prose) endLine() {
	if n := len(p.text); n > 0 && p.text[n-1] == ' ' {
		p.text = p.text[:n-1]
		if last := len(p.pieces) - 1; p.pieces[last].at == n-1 {
			p.pieces = p.pieces[:last]
		}
	}
	if n := len(p.text); n > 0 && p.text[n-1] != '\n' {
		p.add([]byte{'\n'}, p.offset(n-1)+1, true)
	}
}

// shown appends text of the file at from as a browser shows it: each
// character reference decoded, and each run of blanks one space, none
// where atSpace. A lone space is copied, so ordinary text stays one piece.
func (p *prose) shown(b []byte, from int) {
	htmlparse.Unescape(b, from, func(b []byte, from int, wrote bool) {
		start := 0
		for i := 0; i < len(b); {
			if !isBlank(b[i]) {
				i++
				continue
			}
			j := i
			for j < len(b) && isBlank(b[j]) {
				j++
			}
			p.add(b[start:i], from+offsetIn(start, wrote), wrote)
			switch {
			case p.atSpace():
			case j-i == 1 && b[i] == ' ' && !wrote:
				p.add(b[i:j], from+i, false)
			default:
				p.add([]byte{' '}, from+offsetIn(i, wrote), true)
			}
			start, i = j, j
		}
		p.add(b[start:], from+offsetIn(start, wrote), wrote)
	})
}

// written appends text of the file at from as it is written, each
// character reference decoded, as a browser shows the text of a pre.
func (p *prose) written(b []byte, from int) {
	htmlparse.Unescape(b, from, p.add)
}

// offsetIn is the offset of byte i of a piece from its start in the file:
// i for bytes copied, and 0 for bytes errata wrote, which all stand for
// the piece's start.
func offsetIn(i int, wrote bool) int {
	if wrote {
		return 0
	}
	return i
}

// isBlank reports whether b is HTML white space: a space, a tab, a line
// feed, a form feed or a carriage return.
func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\f' || b == '\r' }
