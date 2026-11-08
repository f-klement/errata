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
	// The entries from j on, by id, their order in the list, and the
	// elements from at on, each with the id of its entry or -1. Every
	// element above the formatting element opened after it, so its entry,
	// if any, is after j; no marker is among them, as no cell is open above
	// an element in scope.
	names := make([]string, 0, len(n.afe)-j+adoptPasses)
	order := make([]int, 0, len(n.afe)-j+1)
	for k := j; k < len(n.afe); k++ {
		names = append(names, n.afe[k].name)
		order = append(order, k-j)
	}
	type element struct {
		name string
		id   int
		d    draw // kept as it was: a switch's choice is the element's own
	}
	els := make([]element, 0, len(n.stack)-at+1)
	for i := at; i < len(n.stack); i++ {
		id := -1
		if k := n.afeAt[i]; k >= j {
			id = k - j
		}
		els = append(els, element{n.stack[i], id, n.drawn[i]})
	}
	unlist := func(id int) { order = slices.DeleteFunc(order, func(o int) bool { return o == id }) }
	lineClosed := false
	drop := func(k int) {
		lineClosed = lineClosed || LineTag(els[k].name)
		els = slices.Delete(els, k, k+1)
	}
	fe, feID, name := 0, 0, names[0]
	for range adoptPasses {
		n.work += len(els) + len(order)
		fb := furthestBlock(fe, len(els), func(k int) string { return els[k].name })
		if fb < 0 {
			for k := len(els) - 1; k >= fe; k-- {
				drop(k)
			}
			unlist(feID)
			break
		}
		count, bookmark := 0, -1
		for k := fb - 1; k > fe; k-- {
			count++
			id := els[k].id
			if count > 3 && id >= 0 {
				// The standard takes it off the list and then, as it is off
				// the list, off the stack; x/net v0.62.0 leaves it on the
				// stack (parse.go, inBodyEndTagFormatting), and errata
				// follows the standard, as browsers do.
				unlist(id)
				id = -1
				n.standardOnly = true
				if n.xnet {
					els[k].id = -1 // x/net's way: off the list, on the stack
					els[k].d |= kept
					continue
				}
			}
			if id < 0 {
				drop(k)
				fb--
				continue
			}
			if bookmark < 0 {
				bookmark = id // the bookmark moves after the first one kept
			}
		}
		clone := len(names)
		names = append(names, name)
		place := slices.Index(order, feID)
		if bookmark >= 0 {
			place = slices.Index(order, bookmark) + 1
		}
		order = slices.Insert(order, place, clone)
		unlist(feID)
		els = slices.Delete(els, fe, fe+1)
		fb--
		els = slices.Insert(els, fb+1, element{name, clone, drawsText | els[fb].d&kept}) // a kept block keeps its clone in x/net's tree
		fe, feID = fb+1, clone
	}
	n.work += 2 * len(els) // popped and pushed again
	closed := n.closedLine
	n.popThrough(at)
	n.afe = n.afe[:j]
	index := make(map[int]int, len(order))
	for _, id := range order {
		index[id] = len(n.afe)
		n.afe = append(n.afe, afeEntry{name: names[id], at: -1})
	}
	for _, e := range els {
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
	n.closedLine = closed || lineClosed
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
