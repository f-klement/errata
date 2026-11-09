package main

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// finding is one thing for the reviewer to weigh. base says the line is the
// base's: what is gone has no line at the head.
type finding struct {
	pos  position
	base bool
	text string
}

func (f finding) String() string {
	where := f.pos.file
	if f.pos.line > 0 {
		where = fmt.Sprintf("%s:%d", where, f.pos.line)
	}
	if f.base {
		where += " (base)"
	}
	return where + ": " + f.text
}

// check compares the two sides of a change and returns its findings, by file
// and line.
func check(in input) []finding {
	base, head := collect(in.base, "base"), collect(in.head, "head")
	found := slices.Concat(base.broken, head.broken, goneTests(base, head), goneRows(base, head),
		addedSkips(base, head, in.added), helperSkips(base, head, in.added), fewerAsserts(base, head), lostHelperAsserts(base, head),
		builds(base, head))
	if in.code != "" {
		found = slices.Concat(found, changedWants(base, head, in.code), goldens(in.golden, in.code))
	}
	slices.SortFunc(found, func(a, b finding) int {
		return cmp.Or(strings.Compare(a.pos.file, b.pos.file), cmp.Compare(a.pos.line, b.pos.line), strings.Compare(a.text, b.text))
	})
	return found
}

// goneTests: each test at the base that the head has under no name, nor its
// body under another.
func goneTests(base, head *facts) []finding {
	var found []finding
	for key, t := range base.tests {
		if _, kept := head.tests[key]; kept || head.bodies[t.body] {
			continue
		}
		found = append(found, finding{pos: t.pos, base: true,
			text: nameOf(key) + " is gone, and no test at the head has its body"})
	}
	return found
}

// nameOf is the declaration's name in a key of the facts.
func nameOf(key string) string {
	_, name, _ := strings.Cut(key, " ")
	return name
}

// goneRows: the rows of each table at the base that the head's table does not
// hold and no table at the head has the text of. A table whose declaration is
// gone is left to goneTests.
func goneRows(base, head *facts) []finding {
	var found []finding
	for key, bt := range base.tables {
		dir, _, _ := strings.Cut(key, " ")
		if !head.decls[dir+" "+bt.decl] {
			continue
		}
		ht := cmp.Or(head.tables[key], head.bySeq[dir+" "+bt.seq], &table{})
		for _, name := range bt.names {
			if _, kept := ht.named[name]; !kept && !head.rows[bt.named[name].text] {
				found = append(found, finding{pos: bt.named[name].pos, base: true,
					text: fmt.Sprintf("row %s of a table in %s is gone", name, bt.decl)})
			}
		}
		if len(ht.unnamed) < len(bt.unnamed) && lost(bt.unnamed, head.rows) > 0 {
			found = append(found, finding{pos: bt.pos, base: true,
				text: fmt.Sprintf("a table in %s has %d rows with no name, against %d at the base", bt.decl, len(ht.unnamed), len(bt.unnamed))})
		}
	}
	return found
}

// lost counts the rows whose text no table at the head has.
func lost(rows []row, head map[string]bool) int {
	n := 0
	for _, r := range rows {
		if !head[r.text] {
			n++
		}
	}
	return n
}

// addedSkips: each call that can skip a test whose line, or a line of a
// condition that guards it, the change added, unless its declaration had as
// many of the same call under the same conditions at the base. A reworded
// message keeps its key and skips no more; a skip whose if was dropped, or
// whose init or condition changed, has a key of its own.
func addedSkips(base, head *facts, added map[string]map[int]bool) []finding {
	more := moreSkips(base, head)
	var found []finding
	for _, s := range head.skips {
		if touched(added[s.pos.file], s.lines) && more[s.key()] {
			found = append(found, finding{pos: s.pos, text: fmt.Sprintf("%s in %s, %s", s.call, s.decl, where(added[s.pos.file], s.pos.line, s.guard))})
		}
	}
	return found
}

// helperSkips: each call, on a line the change added, of a helper of the
// package that can skip the test that calls it, unless its declaration had as
// many calls of that helper under the same conditions at the base. A call
// that gained an argument, as goBin's did in 8e78bec4, skips no more.
func helperSkips(base, head *facts, added map[string]map[int]bool) []finding {
	more := moreSkips(base, head)
	var found []finding
	for _, c := range head.calls {
		if touched(added[c.pos.file], c.lines) && more[c.key()] && head.skipper(c) {
			found = append(found, finding{pos: c.pos, text: fmt.Sprintf("%s in %s, %s, calls a helper that skips", c.name, c.decl, where(added[c.pos.file], c.pos.line, c.guard))})
		}
	}
	return found
}

// touched: the change added one of lines, a call's or a condition's that
// guards it.
func touched(added map[int]bool, lines []int) bool {
	for _, l := range lines {
		if added[l] {
			return true
		}
	}
	return false
}

// where says why a skip is reported: its own line is added, or else a
// condition that guards it was rewritten.
func where(added map[int]bool, line int, guard string) string {
	if added[line] {
		return "on an added line"
	}
	return "under a condition the change rewrote: " + clip(guard)
}

// skipper: c calls a helper of its package that can skip.
func (f *facts) skipper(c call) bool {
	return f.helpers[path.Dir(c.pos.file)+" "+c.name].skips
}

// key is a skip's package directory, declaration, call and conditions.
func (s skip) key() string {
	return strings.Join([]string{path.Dir(s.pos.file), s.decl, s.call, s.guard}, "\x00")
}

// key is a helper call's package directory, declaration, helper and
// conditions.
func (c call) key() string {
	return strings.Join([]string{path.Dir(c.pos.file), c.decl, c.name + "()", c.guard}, "\x00")
}

// skipCounts is how many calls that can skip a test, direct or of a helper
// that skips, a side has of each key.
func (f *facts) skipCounts() map[string]int {
	n := map[string]int{}
	for _, s := range f.skips {
		n[s.key()]++
	}
	for _, c := range f.calls {
		if f.skipper(c) {
			n[c.key()]++
		}
	}
	return n
}

// moreSkips is each key the head has more calls of than the base.
func moreSkips(base, head *facts) map[string]bool {
	was := base.skipCounts()
	more := map[string]bool{}
	for key, n := range head.skipCounts() {
		more[key] = n > was[key]
	}
	return more
}

// fewerAsserts: each test that makes fewer assertions than at the base,
// counting those of each helper of its package it calls. A helper counts at
// both sides as it was at the base, so a helper that lost assertions is
// named once, by lostHelperAsserts, and not again in every test that calls
// it, and a test whose assertions moved into a new helper keeps its count.
func fewerAsserts(base, head *facts) []finding {
	var found []finding
	for key, bt := range base.tests {
		ht, kept := head.tests[key]
		if !kept {
			continue
		}
		dir, _, _ := strings.Cut(key, " ")
		was, now := total(bt, dir, base.helpers, base.helpers), total(ht, dir, base.helpers, head.helpers)
		if now < was {
			found = append(found, finding{pos: ht.pos, text: fmt.Sprintf("%s makes %d assertions, against %d at the base", nameOf(key), now, was)})
		}
	}
	return found
}

// total is a test's assertions and those of the helpers it calls, each
// helper as first has it, or else as second does.
func total(t test, dir string, first, second map[string]helper) int {
	n := t.asserts
	for _, name := range t.calls {
		h, ok := first[dir+" "+name]
		if !ok {
			h = second[dir+" "+name]
		}
		n += h.asserts
	}
	return n
}

// lostHelperAsserts: each helper that makes fewer assertions than at the
// base.
func lostHelperAsserts(base, head *facts) []finding {
	var found []finding
	for key, bh := range base.helpers {
		if hh, kept := head.helpers[key]; kept && hh.asserts < bh.asserts {
			found = append(found, finding{pos: hh.pos, text: fmt.Sprintf("helper %s makes %d assertions, against %d at the base", nameOf(key), hh.asserts, bh.asserts)})
		}
	}
	return found
}

// builds: each //go:build line added to, or changed in, a test file the
// base had. The file's tests then run only where the line holds.
func builds(base, head *facts) []finding {
	var found []finding
	for file, hb := range head.builds {
		if bb := base.builds[file]; base.files[file] && bb.text != hb.text {
			found = append(found, finding{pos: hb.pos, text: fmt.Sprintf("%s, against %s at the base: the file's tests run only where it holds", hb.text, cmp.Or(bb.text, "none"))})
		}
	}
	return found
}

// changedWants: each want value whose text changed, in a change that also
// edits code.
func changedWants(base, head *facts, code string) []finding {
	var found []finding
	for key, bw := range base.wants {
		if hw, kept := head.wants[key]; kept && hw.text != bw.text {
			was, now := clipPair(bw.text, hw.text)
			found = append(found, finding{pos: hw.pos,
				text: fmt.Sprintf("%s in %s changed from %s to %s, in a change that also edits %s", hw.what, hw.decl, was, now, code)})
		}
	}
	return found
}

// clip is a text on one line, cut to 60 bytes.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

// clipPair clips two values for one line. When either is longer than a clip
// keeps and they first differ past byte 50, near where a clip cuts, it first
// drops the text they share before that difference but its last 10 bytes, so
// the two values read apart.
func clipPair(a, b string) (string, string) {
	a, b = strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " ")
	if n := shared(a, b); n > 50 && max(len(a), len(b)) > 60 {
		a, b = "..."+a[n-10:], "..."+b[n-10:]
	}
	return clip(a), clip(b)
}

// shared is the bytes a and b share from their start, ended where a rune
// starts 10 bytes or more before their end, so a cut 10 bytes back from it
// lands on a rune's start.
func shared(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	for n > 10 && !utf8.RuneStart(a[n-10]) {
		n--
	}
	return n
}

// goldens: each testdata file the change edited or deleted, in a change that
// also edits code.
func goldens(files []string, code string) []finding {
	found := make([]finding, len(files))
	for i, f := range files {
		found[i] = finding{pos: position{file: f}, text: "a file under testdata changed, in a change that also edits " + code}
	}
	return found
}
