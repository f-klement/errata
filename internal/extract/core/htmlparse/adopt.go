package htmlparse

import "slices"

// adoptPasses is how many times the adoption agency runs its outer loop
// for one end tag at most (the HTML standard's "adoption agency
// algorithm", step 4.1; x/net's inBodyEndTagFormatting, v0.62.0).
const adoptPasses = 8

// adopt follows the HTML standard's adoption agency for the end tag of the
// formatting element of afe entry j, which is open with a special element
// above it, outside a pre or a heading, and leaves on the stack and in the
// list what the tree builder leaves. Each pass finds the furthest block,
// the first special element above the formatting element; takes off the
// stack each element between them that is not on the list, and the fourth
// and later of those that are; and moves the formatting element, as a
// clone, to just above the furthest block, its entry after the entry of
// the nearest element it kept below that block. A pass that finds no
// furthest block closes the formatting element and everything above it;
// after eight passes the clone stays open. The text the rebuild moves is
// no pre's or heading's, so the scanner reads none of it otherwise, but
// what it leaves open decides what follows: an svg it closes ends foreign
// content. It costs the elements above the formatting element for each
// pass, and the entries of the list after its own, and adds what it
// touches to work, which the scanner charges against the token budget:
// a file that has the nest rebuild a deep stack again and again meets the
// token limit, so the work stays linear in max_bytes (ADR 0003).
func (n *nest) adopt(j int) {
	n.rebuilds++
	at := n.afe[j].at
	a := n.agencyFrom(j, at)
	a.run()
	n.work += 2 * len(a.els) // popped and pushed again
	closed := n.closedLine
	n.popThrough(at)
	n.afe = n.afe[:j]
	a.reopen()
	n.closedLine = closed || a.lineClosed
}

// agency is one run of the adoption agency over the list's entries from
// j on and the stack's elements from the formatting element on. Each
// entry has an id, its index in names; order is the ids in the list's
// order, and els the elements, each with the id of its entry or -1.
type agency struct {
	n          *nest
	names      []string
	order      []int
	els        []adoptElement
	lineClosed bool // whether an element dropped closes a line (LineTag)
}

// adoptElement is an element of the stack in the adoption agency.
type adoptElement struct {
	name string
	id   int
	d    draw // kept as it was: a switch's choice is the element's own
}

// agencyFrom is the agency for the afe entry j, whose element is at at.
// Every element above the formatting element opened after it, so its
// entry, if any, is after j; no marker is among them, as no cell is open
// above an element in scope.
func (n *nest) agencyFrom(j, at int) *agency {
	a := &agency{
		n:     n,
		names: make([]string, 0, len(n.afe)-j+adoptPasses),
		order: make([]int, 0, len(n.afe)-j+1),
		els:   make([]adoptElement, 0, len(n.stack)-at+1),
	}
	for k := j; k < len(n.afe); k++ {
		a.names = append(a.names, n.afe[k].name)
		a.order = append(a.order, k-j)
	}
	for i := at; i < len(n.stack); i++ {
		id := -1
		if k := n.afeAt[i]; k >= j {
			id = k - j
		}
		a.els = append(a.els, adoptElement{n.stack[i], id, n.drawn[i]})
	}
	return a
}

// run runs the agency's outer loop, at most adoptPasses times.
func (a *agency) run() {
	fe, feID, name := 0, 0, a.names[0]
	for range adoptPasses {
		a.n.work += len(a.els) + len(a.order)
		fb := furthestBlock(fe, len(a.els), func(k int) string { return a.els[k].name })
		if fb < 0 {
			a.closeFrom(fe, feID)
			return
		}
		fb, bookmark := a.clearBetween(fe, fb)
		fe, feID = a.moveClone(fe, feID, fb, bookmark, name)
	}
}

// closeFrom closes the formatting element at fe, whose entry is feID, and
// everything above it, as a pass that finds no furthest block does.
func (a *agency) closeFrom(fe, feID int) {
	for k := len(a.els) - 1; k >= fe; k-- {
		a.drop(k)
	}
	a.unlist(feID)
}

// clearBetween takes off the stack each element between the formatting
// element at fe and the furthest block at fb that is not on the list,
// and the fourth and later of those that are. It is the furthest block's
// index after, and the id of the nearest element kept below it, or -1:
// the bookmark.
func (a *agency) clearBetween(fe, fb int) (int, int) {
	count, bookmark := 0, -1
	for k := fb - 1; k > fe; k-- {
		count++
		id := a.els[k].id
		if count > 3 && id >= 0 {
			// The standard takes it off the list and then, as it is off
			// the list, off the stack; x/net v0.62.0 leaves it on the
			// stack (parse.go, inBodyEndTagFormatting), and errata
			// follows the standard, as browsers do.
			a.unlist(id)
			id = -1
			a.n.standardOnly = true
			if a.n.xnet {
				a.els[k].id = -1 // x/net's way: off the list, on the stack
				a.els[k].d |= kept
				continue
			}
		}
		if id < 0 {
			a.drop(k)
			fb--
			continue
		}
		if bookmark < 0 {
			bookmark = id // the bookmark moves after the first one kept
		}
	}
	return fb, bookmark
}

// moveClone moves the formatting element at fe, whose entry is feID, as
// a clone of name to just above the furthest block at fb, its entry
// after bookmark's or in its own place when there is none. It is the
// clone's index and id.
func (a *agency) moveClone(fe, feID, fb, bookmark int, name string) (int, int) {
	clone := len(a.names)
	a.names = append(a.names, name)
	place := slices.Index(a.order, feID)
	if bookmark >= 0 {
		place = slices.Index(a.order, bookmark) + 1
	}
	a.order = slices.Insert(a.order, place, clone)
	a.unlist(feID)
	a.els = slices.Delete(a.els, fe, fe+1)
	fb--
	a.els = slices.Insert(a.els, fb+1, adoptElement{name, clone, drawsText | a.els[fb].d&kept}) // a kept block keeps its clone in x/net's tree
	return fb + 1, clone
}

// unlist takes the entry id off the list.
func (a *agency) unlist(id int) {
	a.order = slices.DeleteFunc(a.order, func(o int) bool { return o == id })
}

// drop takes the element at k off the stack.
func (a *agency) drop(k int) {
	a.lineClosed = a.lineClosed || LineTag(a.els[k].name)
	a.els = slices.Delete(a.els, k, k+1)
}

// reopen enters the agency's list after the entries before its first,
// and pushes its elements again, each linked to its entry.
func (a *agency) reopen() {
	n := a.n
	index := make(map[int]int, len(a.order))
	for _, id := range a.order {
		index[id] = len(n.afe)
		n.afe = append(n.afe, afeEntry{name: a.names[id], at: -1})
	}
	for _, e := range a.els {
		i := n.pushElement(e.name)
		if n.drawn[i]&veils != 0 {
			n.veiled--
		}
		if n.drawn[i] = e.d; e.d&veils != 0 {
			n.veiled++
		}
		if e.id >= 0 {
			k := index[e.id]
			n.afe[k].at, n.afeAt[i] = i, k
		}
	}
}

// furthestBlock is the first of the elements after fe, of the n elements
// above the formatting element on, that is special or a heading, by the
// name nameAt gives for each, or -1: the adoption agency's furthest block.
func furthestBlock(fe, n int, nameAt func(int) string) int {
	for k := fe + 1; k < n; k++ {
		if special[nameAt(k)] || isHeading(nameAt(k)) {
			return k
		}
	}
	return -1
}
