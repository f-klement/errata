package core

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// TemplateBlocks finds each engine's blocks by its tags: the lines from
// the tag that opens the outermost block through the one that closes it.
// A tag it does not know opens a block, and a closing tag no block opened
// marks every line, as does a block or a tag left open, since the engine
// then renders nothing. A tag whose body holds another engine's tag start, such as a
// Jinja comment around a Go action in a .tmpl, marks its own lines. The
// planted breaks are each engine's openers and closers read as
// nothing, an escape read as a tag, the whole file left unmarked on a
// stray closer, and an empty tag or one left open at the first byte read
// wrong.
func TestTemplateBlocksFindEachEnginesBlocks(t *testing.T) {
	rest := math.MaxInt
	for _, c := range []struct {
		name, file, body string
		want             spans
	}{
		{"jinja if", "a.j2", "a=1\n{% if x %}\nb=2\n{% endif %}\nc=3\n", spans{{2, 4}}},
		{"jinja for and trim marks", "a.j2", "{%- for x in y -%}\nb\n{%+ endfor %}\n", spans{{1, 3}}},
		{"jinja nested", "a.j2", "{% if a %}\n{% for b in c %}\nx\n{% endfor %}\ny\n{% endif %}\nz\n", spans{{1, 6}}},
		{"jinja two blocks", "a.j2", "{% if a %}\nx\n{% endif %}\ny\n{% if b %}\nz\n{% endif %}\n", spans{{1, 3}, {5, 7}}},
		{"jinja else and elif", "a.j2", "{% if a %}\nx\n{% elif b %}\ny\n{% else %}\nz\n{% endif %}\n", spans{{1, 7}}},
		{"jinja statements open nothing", "a.j2", "{% set x = 1 %}\n{% include 'b' %}\n{% import 'm' as m %}\n{% do x %}\na\n", nil},
		{"jinja block set", "a.j2", "{% set x %}\na\n{% endset %}\n", spans{{1, 3}}},
		{"jinja unknown tag", "a.j2", "{% cache 60 %}\na\n{% endcache %}\nb\n", spans{{1, 3}}},
		{"jinja output and comment", "a.j2", "a={{ x }}\n{# {% if %} #}\n{{ '{% if %}' }}\n", nil},
		{"jinja raw", "a.j2", "{% raw %}\n{% endif %}\n{% endraw %}\nb\n", spans{{1, 3}}},
		{"jinja raw empty", "a.j2", "{% raw %}{% endraw %}\na\n", spans{{1, 1}}},
		{"jinja raw with an empty tag", "a.j2", "{% raw %}\n{%%}\n{% endraw %}\nb\n", spans{{1, 3}}},
		{"jinja endraw over lines", "a.j2", "{% raw %}\n{% endraw\n\n%}\nb\n", spans{{1, 4}}},
		{"jinja raw left open", "a.j2", "{% raw %}\n{% endif %}\n", spans{{1, rest}}},
		{"jinja tag over lines", "a.j2", "{% if a\n   and b %}\nx\n{% endif\n%}\ny\n", spans{{1, 5}}},
		{"jinja left open", "a.j2", "a\n{% if x %}\nb\n", spans{{1, rest}}},
		{"jinja tag not closed", "a.j2", "a\n{% if x\nb\n", spans{{1, rest}}},
		{"jinja tag not closed at the start", "a.j2", "{% if x\nb\n", spans{{1, rest}}},
		{"jinja empty output", "a.j2", "a={{}}\nb\n", nil},
		{"jinja stray end", "a.j2", "a\nb\n{% endif %}\n", spans{{1, rest}}},
		{"jinja extends", "a.j2", "{% extends 'base' %}\na\n", spans{{1, rest}}},
		{"go if", "a.tmpl", "a\n{{ if .x }}\nb\n{{ end }}\nc\n", spans{{2, 4}}},
		{"go range with trim", "a.tmpl", "{{- range .xs -}}\nb\n{{- end -}}\n", spans{{1, 3}}},
		{"go with define and else", "a.tpl", "{{ with .x }}\na\n{{ else }}\nb\n{{ end }}\n{{ define \"t\" }}\nc\n{{ end }}\n", spans{{1, 5}, {6, 8}}},
		{"go output and comment", "a.tmpl", "a={{ .x }}\n{{/* if */}}\n", nil},
		{"go hidden in a jinja comment", "a.tmpl", "c = {#fff\n{{ if .y }}\na = 1\n{{ end }}\nd = #}\nb = 2\n", spans{{1, 5}}},
		{"jinja hidden in a go action", "a.tmpl", "a\n{{ \"{% if %}\" }}\nb\n", spans{{2, 2}}},
		{"terraform hidden in a jinja tag", "a.tpl", "{% set x = '%{ if }' %}\na\n", spans{{1, 1}}},
		{"a hidden tag inside a block", "a.tmpl", "{{ if .x }}\n{# {{ end }} #}\na\n{{ end }}\nb\n", spans{{1, 4}}},
		{"go end in jinja", "a.j2", "a={{ end }}\n", nil},
		{"erb if", "a.erb", "a\n<% if x %>\nb\n<% end %>\nc\n", spans{{2, 4}}},
		{"erb do", "a.erb", "<% xs.each do |x| %>\nb\n<%- end -%>\n", spans{{1, 3}}},
		{"erb output with a block", "a.erb", "<%= form_for x do |f| %>\nb\n<% end %>\n", spans{{1, 3}}},
		{"erb braces", "a.erb", "<% xs.each { |x| %>\nb\n<% } %>\n", spans{{1, 3}}},
		{"erb unless, else, one line", "a.erb", "<% unless x %>\na\n<% else %>\nb\n<% end %>\n<% if a then b end %>\nc\n", spans{{1, 5}}},
		{"erb params with no do", "a.erb", "<% |x| %>\n<% x| %>\n<% x # y %>\n", nil},
		{"jinja names", "a.j2", "{% Zx %}\na\n{% endZx %}\n{% _x %}\nb\n{% end_x %}\n{% set=1 %}\n{% else_x %}\nc\n{% endelse_x %}\n", spans{{1, 3}, {4, 6}, {8, 10}}},
		{"erb statements open nothing", "a.erb", "<% x = 1 %>\n<%= y %>\n<%# if %>\n<% z if w %>\n<% undo %>\n", nil},
		{"erb literal", "a.erb", "a=<%% if x %>\nb\n", nil},
		{"erb literal with no close", "a.erb", "a=<%% x\nb\n", nil},
		{"erb in jinja", "a.j2", "<% if x %>\na\n", nil},
		{"terraform if", "a.tftpl", "a\n%{ if x }\nb\n%{ endif }\nc\n", spans{{2, 4}}},
		{"terraform for with strip", "a.tftpl", "%{~ for x in y ~}\nb\n%{~ endfor ~}\n", spans{{1, 3}}},
		{"terraform else", "a.tpl", "%{ if x }\na\n%{ else }\nb\n%{ endif }\n", spans{{1, 5}}},
		{"terraform literal", "a.tftpl", "a=%%{ if x }\nb\n", nil},
		{"terraform in jinja", "a.j2", "%{ if x }\na\n", nil},
		{"engines apart", "a.tpl", "{% if a %}\n%{ endif }\n", spans{{1, rest}}},
		{"lone cr", "a.j2", "a\r{% if x %}\rb\r{% endif %}\rc", spans{{2, 4}}},
	} {
		if got, want := blockLinesOf(t, c.file, c.body), c.want.lines(c.body); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: lines in a block %v; want %v", c.name, got, want)
		}
	}
}

// spans are the lines a test expects in blocks.
type spans []lineSpan

// lines is, for each line of body and one past it, whether a span holds it.
func (sp spans) lines(body string) []bool {
	out := make([]bool, LastLine([]byte(body))+1)
	for i := range out {
		for _, s := range sp {
			out[i] = out[i] || s.first <= i+1 && i+1 <= s.last
		}
	}
	return out
}

// blockLinesOf asks Holds of every line of body and one past it, in file
// order, and again from the last line back, which starts the scan over at
// each line: both must give the same answers.
func blockLinesOf(t *testing.T, file, body string) []bool {
	t.Helper()
	// A budget, so a scan that never ends fails rather than hangs.
	b, err := TemplateBlocks(file, []byte(body), &Meter{Budget: 1 << 22})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	n := LastLine([]byte(body)) + 1
	forward, back := make([]bool, n), make([]bool, n)
	for line := 1; line <= n; line++ {
		if forward[line-1], err = b.Holds(line); err != nil {
			t.Fatalf("%s line %d: %v", file, line, err)
		}
	}
	for line := n; line >= 1; line-- {
		if back[line-1], err = b.Holds(line); err != nil {
			t.Fatalf("%s line %d: %v", file, line, err)
		}
	}
	if !reflect.DeepEqual(forward, back) {
		t.Errorf("%s: asked in file order %v, from the end %v", file, forward, back)
	}
	return forward
}

// TemplateRead marks the entries whose line a block holds, and reads a file
// that is no template as its reader does. Planted break: InBlock left
// false.
func TestTemplateReadMarksTheEntriesInABlock(t *testing.T) {
	read := func(_ []byte, _ *Meter, visit func(*Entry) error) error {
		for line := 1; line <= 5; line++ {
			if err := visit(&Entry{Line: line}); err != nil {
				return err
			}
		}
		return nil
	}
	body := []byte("a\n{% if x %}\nb\n{% endif %}\nc\n")
	for file, want := range map[string][]bool{
		"a.j2": {false, true, true, true, false},
		"a":    {false, false, false, false, false},
	} {
		var got []bool
		err := TemplateRead(file, read)(body, nil, func(e *Entry) error {
			got = append(got, e.InBlock)
			return nil
		})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, %v; want %v", file, got, err, want)
		}
	}
	if !TemplateNotLiteral("a.j2", nil)(&Entry{InBlock: true, Value: []byte("1")}) {
		t.Error("a value inside a block is literal")
	}
}

// The scan charges its bytes and each tag, and a read past the budget is
// refused. Planted break: the charge for a tag left out.
//
// Each byte a tag may start at costs 1, besides the tag's own work.
func TestTemplateBlocksCharge(t *testing.T) {
	body := []byte("{% if x %}{% endif %}")
	scan := int64(len(body)) + 2*(1+tagWork) // the bytes, two tag starts and two tags
	m := &Meter{Budget: 1 << 40}
	b, err := TemplateBlocks("a.j2", body, m)
	if err != nil || m.Spent != scan+int64(len(body)) {
		t.Errorf("the first scan spent %d, %v; want %d", m.Spent, err, scan+int64(len(body)))
	}
	if _, err := b.Holds(2); err != nil || m.Spent != 2*scan {
		t.Errorf("both scans spent %d, %v; want %d", m.Spent, err, 2*scan)
	}
	if _, err := b.Holds(1); err != nil || m.Spent != 3*scan {
		t.Errorf("a line asked again from the start spent %d, %v; want %d", m.Spent, err, 3*scan)
	}
	if _, err := TemplateBlocks("a.j2", body, &Meter{Budget: int64(len(body)) + 1 + tagWork}); !errors.Is(err, ErrWork) {
		t.Errorf("a scan past its budget: %v, want ErrWork", err)
	}
	if b, err := TemplateBlocks("a.ini", body, m); b != nil || err != nil {
		t.Errorf("a file that is no template: %v, %v", b, err)
	}
	if in, err := (*Blocks)(nil).Holds(1); in || err != nil {
		t.Errorf("nil Blocks: %v, %v", in, err)
	}
}
