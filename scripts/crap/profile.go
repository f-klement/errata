package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// pos is a line and a byte column, both from 1, as a profile gives them.
type pos struct{ line, col int }

func (p pos) before(q pos) bool {
	return p.line < q.line || p.line == q.line && p.col < q.col
}

// span is where a profile block starts and ends.
type span struct{ start, end pos }

// block is a profile block: its statements, and whether a test ran it.
type block struct {
	stmts   int
	covered bool
}

// profile holds each file's blocks, by the file's path from the module root.
// A block listed twice is held once.
type profile map[string]map[span]block

// blockLine is a profile line: file:startLine.startCol,endLine.endCol statements count.
var blockLine = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

// readProfileFile reads a profile file of the module.
func readProfileFile(name, module string) (profile, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readProfile(f, name, module)
}

// readProfile parses a profile whose files are in the module. It refuses one
// without its mode line, with a line it cannot read, with a file outside the
// module, with a block listed twice with two statement counts, or with no block.
func readProfile(r io.Reader, name, module string) (profile, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "mode: ") {
		return nil, fmt.Errorf("%s does not parse: its first line is not a mode line", name)
	}
	p := profile{}
	for n := 2; sc.Scan(); n++ {
		if err := p.add(sc.Text(), module); err != nil {
			return nil, fmt.Errorf("%s does not parse at line %d: %w", name, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", name, err)
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("%s holds no block", name)
	}
	return p, nil
}

// add reads one block line into the profile.
func (p profile) add(line, module string) error {
	file, s, b, err := parseBlock(line)
	if err != nil {
		return err
	}
	rel, ok := strings.CutPrefix(file, module+"/")
	if !ok {
		return fmt.Errorf("%s is not a file of module %s", file, module)
	}
	if p[rel] == nil {
		p[rel] = map[span]block{}
	}
	had, seen := p[rel][s]
	if seen && had.stmts != b.stmts {
		return fmt.Errorf("the block %s is listed with %d statements and with %d", line, had.stmts, b.stmts)
	}
	b.covered = b.covered || had.covered
	p[rel][s] = b
	return nil
}

// parseBlock reads a block line.
func parseBlock(line string) (string, span, block, error) {
	m := blockLine.FindStringSubmatch(line)
	if m == nil {
		return "", span{}, block{}, fmt.Errorf("%q is not a block line", line)
	}
	var n [6]int
	for i := range n {
		v, err := strconv.Atoi(m[i+2])
		if err != nil {
			return "", span{}, block{}, fmt.Errorf("%q: %w", line, err)
		}
		n[i] = v
	}
	s := span{pos{n[0], n[1]}, pos{n[2], n[3]}}
	return m[1], s, block{stmts: n[4], covered: n[5] > 0}, nil
}
