package mdparse

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark/v2/ast"
	east "github.com/yuin/goldmark/v2/extension/ast"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/htmlparse"
)

// The guarded parse is goldmark's with parseRun (goldmarkRuns), node for
// node and offset for offset, or a refusal by one of its bounds, with
// GitHub's tables when tables is set. The guard counts at least the nodes
// the tree holds, and a parse with a node budget of budget more than the
// lines take either makes the same tree or is refused by the budget. Every
// byte of the prose is the file's byte at the offset its piece maps it to,
// but for a space or a newline errata wrote, and the prose is never over
// twice the file.
func FuzzParse(f *testing.F) {
	for i, s := range markdownSamples {
		f.Add(s, false, uint16(i*7), i%2 == 0)
	}
	f.Add("---\na\n---\n- \tx\n\n\t\t\tcode\n", true, uint16(3), false)
	f.Add("> ```\n> \tcode\n>\n\t> x\n", true, uint16(0), false)
	f.Add("a\r\nb\r\n\r\n    c\r\n", true, uint16(1), false)
	f.Add("\xef\xbb\xbf---\na\n...\n<!-- x --> y <b>z</b>\n", false, uint16(2), false)
	// Seeds that cross each bound: definitions, continuations under the
	// budget, depth at a block's opening and after the parse, runs of * and
	// _, runs that close nothing under the budget, a line of HTML, and folds
	// of case that change a rune's length.
	f.Add(strings.Repeat("[x]: y\n", 3163), false, uint16(500), false)
	f.Add(strings.Repeat("> > > a\n", 11), false, uint16(4), false)
	f.Add(strings.Repeat("> ", 1000)+"a\n", false, uint16(9), false)
	f.Add("x\n"+strings.Repeat("*a ", 999)+"b"+strings.Repeat(" a*", 999)+"\n", false, uint16(9), false)
	f.Add(strings.Repeat("_a ", 5001)+"\n", false, uint16(9), false)
	f.Add("x\n"+strings.Repeat("*"+strings.Repeat("a", 98)+" ", 4)+"\n", false, uint16(9), false)
	f.Add("<pre>\n"+strings.Repeat("b", MaxHTMLLine+1)+"\n", false, uint16(9), false)
	f.Add("<!-- ȺȺ -->x\n<!-- \u212a\u212a -->y <i>z</i>\n<SCRIPT>\xff</script> w\n", false, uint16(5), false)
	f.Add(strings.Repeat("a\t", 40), false, uint16(1), false)
	// HTML blocks the HTML parse reads (#86): a div in a block quote, its
	// lines joined without the markers, and a pre whose lines stay lines.
	f.Add("> <div><p>a\n> b</p>\n> <!-- c --><script>d</script>\n\n<pre>\n x\n\ty</pre> z\n", false, uint16(1), false)
	f.Add("| a | b |\n|---|:-:|\n| `x\\|y` | *z* \\| w |\n"+strings.Repeat("|", 9)+"\n", false, uint16(5), true)
	f.Add("x\n|-|-|\n|"+strings.Repeat("a|", 40)+"\n", false, uint16(2), true)
	f.Fuzz(func(t *testing.T, data string, code bool, budget uint16, tables bool) {
		parse, goldmark := Parse, goldmarkRuns
		if tables {
			parse, goldmark = ParseTables, goldmarkTables
		}
		doc, err := parse("f", []byte(data), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			if !guardRefused(err) {
				t.Fatalf("refused: %v", err)
			}
			return
		}
		tree := treeString(doc.Root, doc.Src)
		if want := treeString(goldmark.Parse(doc.Src), doc.Src); tree != want {
			t.Fatalf("guarded\n%s\ngoldmark\n%s", tree, want)
		}
		limit := markdownBytesPerNode * (2*int64(strings.Count(data, "\n")) + 3 + int64(budget))
		small, err := parse("f", []byte(data), limit, time.Minute)
		switch {
		case err != nil:
			if msg := err.Error(); !strings.Contains(msg, "nodes, the limit") && !strings.Contains(msg, "open blocks from line to line, the limit") {
				t.Fatalf("budget %d: refused: %v", budget, err)
			}
		case treeString(small.Root, small.Src) != tree:
			t.Fatalf("budget %d: the tree differs", budget)
		default:
			// The same budget bounds the prose's pieces, and the tokens the
			// HTML parse reads of its HTML blocks.
			full, err := proseOf(doc, code, tables)
			if err != nil {
				if !htmlRefused(err) {
					t.Fatal(err)
				}
				return
			}
			switch p, err := proseOf(small, code, tables); {
			case err != nil:
				if !strings.Contains(err.Error(), "pieces of the file, the limit") && !strings.Contains(err.Error(), "tokens, the limit") {
					t.Fatalf("budget %d: prose refused: %v", budget, err)
				}
			case !bytes.Equal(p.Text, full.Text):
				t.Fatalf("budget %d: the prose differs", budget)
			}
		}
		// A short time limit, from 0, which has passed at the first look,
		// to 3 microseconds: the time refusal, or the same tree.
		quick, err := parse("f", []byte(data), core.DefaultMaxBytes, time.Duration(budget%4)*time.Microsecond)
		switch {
		case err != nil:
			if !strings.Contains(err.Error(), "parsing the Markdown took more than") {
				t.Fatalf("quick: refused: %v", err)
			}
		case treeString(quick.Root, quick.Src) != tree:
			t.Fatalf("quick: the tree differs")
		}
		if n := countTree(doc.Root); int64(n) > doc.nodes {
			t.Fatalf("the tree holds %d nodes and the guard counted %d", n, doc.nodes)
		}
		p, err := proseOf(doc, code, tables)
		if err != nil {
			if !htmlRefused(err) {
				t.Fatal(err)
			}
			return
		}
		if len(p.Text) > 2*len(data) {
			t.Fatalf("prose of %d bytes from %d", len(p.Text), len(data))
		}
		orig := bytes.TrimPrefix(bytes.ReplaceAll([]byte(data), []byte("\r\n"), []byte("\n")), []byte(core.BOM))
		for i, pc := range p.pieces {
			end := len(p.Text)
			if i+1 < len(p.pieces) {
				end = p.pieces[i+1].at
			}
			if pc.wrote {
				if end-pc.at != 1 || (p.Text[pc.at] != ' ' && p.Text[pc.at] != '\n') || pc.from < 0 || pc.from > len(orig) {
					t.Fatalf("written piece %d: %q for %d", i, p.Text[pc.at:end], pc.from)
				}
				continue
			}
			got, want := p.Text[pc.at:end], orig[pc.from:pc.from+end-pc.at]
			for j := range got {
				if got[j] != want[j] && (got[j] != ' ' || want[j] != '\n') {
					t.Fatalf("piece %d is %q, the file has %q", i, got, want)
				}
			}
		}
	})
}

// proseOf is the document's prose, or with tables the prose of every cell
// of its tables, a line each.
func proseOf(doc *Doc, code, tables bool) (*Prose, error) {
	if !tables {
		return doc.Prose(code)
	}
	p := doc.NewProse()
	_ = ast.Walk(doc.Root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Kind() == east.KindTableCell {
			p.Cell(doc.Src, n)
			p.EndBlock()
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return p, doc.Err(p)
}

// guardRefused reports whether err is a refusal by one of the parse's
// bounds.
func guardRefused(err error) bool {
	for _, s := range []string{"levels deep, the limit", "runs of * and _, the limit", "link reference definitions, and the two multiplied",
		"nodes, the limit", "open blocks from line to line, the limit", "that close emphasis, the limit", "a line of HTML of", "parsing the Markdown took more than", "empty cells, more than its"} {
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

// htmlRefused reports whether err is the HTML parse's refusal of an HTML
// block's nesting inside a pre (htmlparse.NestError).
func htmlRefused(err error) bool {
	var nest *htmlparse.NestError
	return errors.As(err, &nest)
}
