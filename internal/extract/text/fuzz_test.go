package text

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/f-klement/errata/internal/extract/core"
)

// The guarded parse is goldmark's, node for node and offset for offset, or
// a refusal by one of its bounds. The guard counts at least the nodes the
// tree holds, and a parse with a node budget of budget more than the lines
// take either makes the same tree or is refused by the budget. Every byte of the prose is the file's byte at the offset its
// piece maps it to, but for a space or a newline errata wrote, and the prose
// is never over maxProseRatio times the file. Every value of the text
// extractor is in the prose, at a line of the file.
func FuzzText(f *testing.F) {
	for i, s := range markdownSamples {
		f.Add(s, false, uint16(i*7))
	}
	f.Add("---\na\n---\n- \tx\n\n\t\t\tcode\n", true, uint16(3))
	f.Add("> ```\n> \tcode\n>\n\t> x\n", true, uint16(0))
	f.Add("a\r\nb\r\n\r\n    c\r\n", true, uint16(1))
	f.Add("\xef\xbb\xbf---\na\n...\n<!-- x --> y <b>z</b>\n", false, uint16(2))
	// Seeds that cross each bound: definitions, continuations under the
	// budget, depth at a block's opening and after the parse, runs of * and
	// _, a line of HTML, and folds of case that change a rune's length.
	f.Add(strings.Repeat("[x]: y\n", 3163), false, uint16(500))
	f.Add(strings.Repeat("> > > a\n", 11), false, uint16(4))
	f.Add(strings.Repeat("> ", 1000)+"a\n", false, uint16(9))
	f.Add("x\n"+strings.Repeat("*a ", 999)+"b"+strings.Repeat(" a*", 999)+"\n", false, uint16(9))
	f.Add(strings.Repeat("_a ", 5001)+"\n", false, uint16(9))
	f.Add("<pre>\n"+strings.Repeat("b", maxHTMLLine+1)+"\n", false, uint16(9))
	f.Add("<!-- ȺȺ -->x\n<!-- \u212a\u212a -->y <i>z</i>\n<SCRIPT>\xff</script> w\n", false, uint16(5))
	f.Add(strings.Repeat("a\t", 40), false, uint16(1))
	f.Fuzz(func(t *testing.T, data string, code bool, budget uint16) {
		doc, err := parseMarkdown("f", []byte(data), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			if !guardRefused(err) {
				t.Fatalf("refused: %v", err)
			}
			return
		}
		tree := treeString(doc.root, doc.src)
		if want := treeString(goldmark.DefaultParser().Parse(text.NewReader(doc.src)), doc.src); tree != want {
			t.Fatalf("guarded\n%s\ngoldmark\n%s", tree, want)
		}
		limit := markdownBytesPerNode * (2*int64(strings.Count(data, "\n")) + 3 + int64(budget))
		small, err := parseMarkdown("f", []byte(data), limit, time.Minute)
		switch {
		case err != nil:
			if msg := err.Error(); !strings.Contains(msg, "nodes, the limit") && !strings.Contains(msg, "open blocks from line to line, the limit") {
				t.Fatalf("budget %d: refused: %v", budget, err)
			}
		case treeString(small.root, small.src) != tree:
			t.Fatalf("budget %d: the tree differs", budget)
		default:
			// The same budget bounds the prose's pieces.
			full, err := doc.prose(code)
			if err != nil {
				t.Fatal(err)
			}
			switch p, err := small.prose(code); {
			case err != nil:
				if !strings.Contains(err.Error(), "pieces of the file, the limit") {
					t.Fatalf("budget %d: prose refused: %v", budget, err)
				}
			case !bytes.Equal(p.text, full.text):
				t.Fatalf("budget %d: the prose differs", budget)
			}
		}
		// A short time limit, from 0, which has passed at the first look,
		// to 3 microseconds: the time refusal, or the same tree.
		quick, err := parseMarkdown("f", []byte(data), core.DefaultMaxBytes, time.Duration(budget%4)*time.Microsecond)
		switch {
		case err != nil:
			if !strings.Contains(err.Error(), "parsing the Markdown took more than") {
				t.Fatalf("quick: refused: %v", err)
			}
		case treeString(quick.root, quick.src) != tree:
			t.Fatalf("quick: the tree differs")
		}
		if n := countTree(doc.root); int64(n) > doc.nodes {
			t.Fatalf("the tree holds %d nodes and the guard counted %d", n, doc.nodes)
		}
		p, err := doc.prose(code)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.text) > maxProseRatio*len(data) {
			t.Fatalf("prose of %d bytes from %d", len(p.text), len(data))
		}
		orig := bytes.TrimPrefix(bytes.ReplaceAll([]byte(data), []byte("\r\n"), []byte("\n")), bom)
		for i, pc := range p.pieces {
			end := len(p.text)
			if i+1 < len(p.pieces) {
				end = p.pieces[i+1].at
			}
			if pc.wrote {
				if end-pc.at != 1 || (p.text[pc.at] != ' ' && p.text[pc.at] != '\n') || pc.from < 0 || pc.from > len(orig) {
					t.Fatalf("written piece %d: %q for %d", i, p.text[pc.at:end], pc.from)
				}
				continue
			}
			got, want := p.text[pc.at:end], orig[pc.from:pc.from+end-pc.at]
			for j := range got {
				if got[j] != want[j] && (got[j] != ' ' || want[j] != '\n') {
					t.Fatalf("piece %d is %q, the file has %q", i, got, want)
				}
			}
		}
		e, err := Build(Spec{File: "f", Pattern: `(\S+)`, Code: code})
		if err != nil {
			t.Fatal(err)
		}
		values, err := e.(textExtractor).FromData([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Count(orig, newline) + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > lines || !bytes.Contains(p.text, []byte(v.Text)) {
				t.Fatalf("value %q at line %d of %d", v.Text, v.At.Line, lines)
			}
		}
	})
}

// guardRefused reports whether err is a refusal by one of the parse's
// bounds.
func guardRefused(err error) bool {
	for _, s := range []string{"levels deep, the limit", "runs of * and _, the limit", "link reference definitions, and the two multiplied",
		"nodes, the limit", "open blocks from line to line, the limit", "a line of HTML of", "parsing the Markdown took more than"} {
		if strings.Contains(err.Error(), s) {
			return true
		}
	}
	return false
}

// countTree counts the nodes of the tree from n.
func countTree(n ast.Node) int {
	count := 0
	_ = ast.Walk(n, func(ast.Node, bool) (ast.WalkStatus, error) {
		count++
		return ast.WalkContinue, nil
	})
	return count / 2 // Walk visits each node on entry and on exit
}
