package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"pgregory.net/rapid"
)

// iniItem is one value a generated file sets: under a section (a group for
// ansible), a host for an ansible host line, and a key.
type iniItem struct {
	section, host, key, value string
	line                      int // where the renderer wrote the key
}

// checkItems extracts every item's section, host and key from data and
// checks that each comes back with its value, in file order, at the line the
// renderer wrote it at, which lies inside the file and holds the key.
func checkItems(t *rapid.T, root, name string, data []byte, items []iniItem, spec func(iniItem) Spec) {
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLines := strings.Split(string(data), "\n")
	want := map[string][]string{}
	for _, it := range items {
		id := it.section + "\x00" + it.host + "\x00" + it.key
		want[id] = append(want[id], fmt.Sprintf("%s@%d", it.value, it.line))
	}
	for _, it := range items {
		got := mustExtract(t, spec(it), root)
		id := it.section + "\x00" + it.host + "\x00" + it.key
		var texts []string
		for _, v := range got {
			checkLine(t, v, fileLines, data)
			if !strings.Contains(fileLines[v.At.Line-1], it.key) {
				t.Fatalf("%q: line %d is %q, which does not hold the key\n%s", it.key, v.At.Line, fileLines[v.At.Line-1], data)
			}
			texts = append(texts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
		}
		if strings.Join(texts, " ") != strings.Join(want[id], " ") {
			t.Fatalf("%+v: got %q, want %q\n%s", it, texts, want[id], data)
		}
	}
}

// drawItems draws up to n items over a few sections and keys, so keys repeat.
func drawItems(t *rapid.T, sections, keys, values *rapid.Generator[string]) []iniItem {
	n := rapid.IntRange(1, 12).Draw(t, "n")
	items := make([]iniItem, n)
	for i := range items {
		items[i] = iniItem{section: sections.Draw(t, "section"), key: keys.Draw(t, "key"), value: values.Draw(t, "value")}
	}
	return items
}

// lineWriter writes lines and counts them.
type lineWriter struct {
	b    strings.Builder
	line int
}

func (w *lineWriter) add(format string, args ...any) int {
	w.line++
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteString("\n")
	return w.line
}

func TestPropertyPlainValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := drawItems(t, rapid.StringMatching(`[a-z][a-z:.-]{0,6}`), rapid.StringMatching(`[a-z][a-z_. -]{0,6}[a-z]`),
			rapid.StringMatching(`[a-z0-9]([a-z0-9.;#=:% -]{0,10}[a-z0-9])?`))
		var w lineWriter
		for i := range items {
			if i == 0 || items[i].section != items[i-1].section {
				w.add("[%s]", items[i].section)
				if rapid.Bool().Draw(t, "comment") {
					w.add("# k = x")
				}
			}
			items[i].line = w.add("%s%s%s", items[i].key, rapid.SampledFrom([]string{" = ", "=", ": ", " :\t"}).Draw(t, "sep"), items[i].value)
		}
		checkItems(t, root, "f.ini", []byte(w.b.String()), items, func(it iniItem) Spec { return ini("f.ini", it.section, it.key) })
	})
}

func TestPropertyGitValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := drawItems(t, rapid.StringMatching(`[a-z]{1,4}( [a-zA-Z "\\./]{0,6})?`), rapid.StringMatching(`[a-z][a-z0-9-]{0,6}`),
			rapid.StringMatching(`[a-z0-9 ;#"\\\t]{0,10}[a-z0-9]`))
		var w lineWriter
		for i := range items {
			section, sub, hasSub := strings.Cut(items[i].section, " ")
			if i == 0 || items[i].section != items[i-1].section {
				if hasSub {
					w.add("[%s \"%s\"]", strings.ToUpper(section), strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(sub))
				} else {
					w.add("[%s]", section)
				}
			}
			value := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\t", `\t`).Replace(items[i].value) + `"`
			items[i].line = w.add("\t%s = %s ; c", items[i].key, value)
		}
		checkItems(t, root, ".gitconfig", []byte(w.b.String()), items, func(it iniItem) Spec {
			section, sub, hasSub := strings.Cut(it.section, " ")
			if !hasSub {
				return gitSpec(".gitconfig", section, nil, strings.ToUpper(it.key))
			}
			return gitSpec(".gitconfig", section, &sub, it.key)
		})
	})
}

func TestPropertyAnsibleValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		groups := rapid.SampledFrom([]string{"web", "db", "web:vars", "db:vars"})
		items := drawItems(t, groups, rapid.StringMatching(`[a-z][a-z_]{0,6}`),
			// A value that starts with a letter is no Python literal, so a # in
			// it is text, as ansible reads it.
			rapid.StringMatching(`[a-z]([a-z0-9.#= -]{0,10}[a-z0-9])?`))
		var w lineWriter
		for i := range items {
			if i == 0 || items[i].section != items[i-1].section {
				w.add("[%s]", items[i].section)
			}
			if strings.HasSuffix(items[i].section, ":vars") {
				items[i].line = w.add("%s = %s", items[i].key, items[i].value)
				continue
			}
			items[i].host = rapid.SampledFrom([]string{"h1", "h2.example.com"}).Draw(t, "host")
			items[i].line = w.add("%s other=1 %s=%q # c", items[i].host, items[i].key, items[i].value)
		}
		checkItems(t, root, "hosts", []byte(w.b.String()), items, func(it iniItem) Spec {
			return Spec{INI: &INI{File: "hosts", Section: it.section, Host: it.host, Key: it.key}}
		})
	})
}

// javaEscape writes s as Properties.store writes a key (key set) or a
// value: \ = : # ! escaped, white space escaped in a key and at the start of
// a value, \t \n \r \f, and every other character outside printable ASCII
// as \uXXXX, in UTF-16.
func javaEscape(s string, key bool) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == ' ' && (key || i == 0):
			b.WriteString(`\ `)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\f':
			b.WriteString(`\f`)
		case strings.ContainsRune(`\=:#!`, r):
			b.WriteString(`\` + string(r))
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04x`, u)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestPropertyPropertiesValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := drawItems(t, rapid.Just(""), rapid.StringMatching(`[a-z][a-z.:= é]{0,6}`), rapid.StringMatching(`[a-z0-9 =:#!\\\té]{0,12}`))
		var w lineWriter
		for i := range items {
			if rapid.Bool().Draw(t, "comment") {
				w.add("%s comment \\", rapid.SampledFrom([]string{"#", "!", "  #"}).Draw(t, "mark"))
			}
			value := javaEscape(items[i].value, false)
			// A line break, which a \ escapes, inside the value, at a
			// point that splits no escape.
			if cut := rapid.IntRange(0, len(value)).Draw(t, "cut"); cut > 0 && !strings.HasSuffix(value[:cut], `\`) && !strings.HasPrefix(value[cut:], " ") {
				value = value[:cut] + "\\\n    " + value[cut:]
			}
			items[i].line = w.add("%s%s%s", javaEscape(items[i].key, true), rapid.SampledFrom([]string{"=", " = ", ":", " "}).Draw(t, "sep"), value)
			w.line += strings.Count(value, "\n")
		}
		checkItemsEscaped(t, root, []byte(w.b.String()), items)
	})
}

// checkItemsEscaped is checkItems for a properties file, whose lines hold
// each key escaped.
func checkItemsEscaped(t *rapid.T, root string, data []byte, items []iniItem) {
	escaped := make([]iniItem, len(items))
	copy(escaped, items)
	for i := range escaped {
		escaped[i].key = javaEscape(items[i].key, true)
	}
	if err := os.WriteFile(filepath.Join(root, "a.properties"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLines := strings.Split(string(data), "\n")
	for i, it := range items {
		got := mustExtract(t, Spec{Properties: &Properties{File: "a.properties", Key: it.key}}, root)
		var gotTexts, want []string
		for _, v := range got {
			checkLine(t, v, fileLines, data)
			gotTexts = append(gotTexts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
		}
		for j, other := range items {
			if other.key == it.key {
				if !strings.HasPrefix(fileLines[other.line-1], escaped[j].key) {
					t.Fatalf("line %d is %q, which does not start with the key %q", other.line, fileLines[other.line-1], escaped[j].key)
				}
				want = append(want, fmt.Sprintf("%s@%d", other.value, other.line))
			}
		}
		if strings.Join(gotTexts, " ") != strings.Join(want, " ") {
			t.Fatalf("item %d %+v: got %q, want %q\n%s", i, it, gotTexts, want, data)
		}
	}
}
