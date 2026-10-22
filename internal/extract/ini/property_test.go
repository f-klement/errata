package ini

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// checkItems extracts every item's section, host and key from data and
// checks that each comes back with its value, in file order, at the line the
// renderer wrote it at, which lies inside the file and holds the key.
func checkItems(t *rapid.T, root, name string, data []byte, items []extracttest.Item, spec func(extracttest.Item) Spec) {
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLines := strings.Split(string(data), "\n")
	want := map[string][]string{}
	for _, it := range items {
		id := it.Section + "\x00" + it.Host + "\x00" + it.Key
		want[id] = append(want[id], fmt.Sprintf("%s@%d", it.Value, it.Line))
	}
	for _, it := range items {
		got := extracttest.MustExtract(t, Build, spec(it), root)
		id := it.Section + "\x00" + it.Host + "\x00" + it.Key
		var texts []string
		for _, v := range got {
			extracttest.CheckLine(t, v, fileLines, data)
			if !strings.Contains(fileLines[v.At.Line-1], it.Key) {
				t.Fatalf("%q: line %d is %q, which does not hold the key\n%s", it.Key, v.At.Line, fileLines[v.At.Line-1], data)
			}
			texts = append(texts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
		}
		if strings.Join(texts, " ") != strings.Join(want[id], " ") {
			t.Fatalf("%+v: got %q, want %q\n%s", it, texts, want[id], data)
		}
	}
}

func TestPropertyPlainValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.StringMatching(`[a-z][a-z:.-]{0,6}`), rapid.StringMatching(`[a-z][a-z_. -]{0,6}[a-z]`),
			rapid.StringMatching(`[a-z0-9]([a-z0-9.;#=:% -]{0,10}[a-z0-9])?`))
		var w extracttest.LineWriter
		for i := range items {
			if i == 0 || items[i].Section != items[i-1].Section {
				w.Add("[%s]", items[i].Section)
				if rapid.Bool().Draw(t, "comment") {
					w.Add("# k = x")
				}
			}
			items[i].Line = w.Add("%s%s%s", items[i].Key, rapid.SampledFrom([]string{" = ", "=", ": ", " :\t"}).Draw(t, "sep"), items[i].Value)
		}
		checkItems(t, root, "f.ini", []byte(w.Text.String()), items, func(it extracttest.Item) Spec { return ini("f.ini", it.Section, it.Key) })
	})
}

func TestPropertyGitValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.StringMatching(`[a-z]{1,4}( [a-zA-Z "\\./]{0,6})?`), rapid.StringMatching(`[a-z][a-z0-9-]{0,6}`),
			rapid.StringMatching(`[a-z0-9 ;#"\\\t]{0,10}[a-z0-9]`))
		var w extracttest.LineWriter
		for i := range items {
			section, sub, hasSub := strings.Cut(items[i].Section, " ")
			if i == 0 || items[i].Section != items[i-1].Section {
				if hasSub {
					w.Add("[%s \"%s\"]", strings.ToUpper(section), strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(sub))
				} else {
					w.Add("[%s]", section)
				}
			}
			value := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\t", `\t`).Replace(items[i].Value) + `"`
			items[i].Line = w.Add("\t%s = %s ; c", items[i].Key, value)
		}
		checkItems(t, root, ".gitconfig", []byte(w.Text.String()), items, func(it extracttest.Item) Spec {
			section, sub, hasSub := strings.Cut(it.Section, " ")
			if !hasSub {
				return gitSpec(".gitconfig", section, nil, strings.ToUpper(it.Key))
			}
			return gitSpec(".gitconfig", section, &sub, it.Key)
		})
	})
}

func TestPropertyAnsibleValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		groups := rapid.SampledFrom([]string{"web", "db", "web:vars", "db:vars"})
		items := extracttest.DrawItems(t, groups, rapid.StringMatching(`[a-z][a-z_]{0,6}`),
			// A value that starts with a letter is no Python literal, so a # in
			// it is text, as ansible reads it.
			rapid.StringMatching(`[a-z]([a-z0-9.#= -]{0,10}[a-z0-9])?`))
		var w extracttest.LineWriter
		for i := range items {
			if i == 0 || items[i].Section != items[i-1].Section {
				w.Add("[%s]", items[i].Section)
			}
			if strings.HasSuffix(items[i].Section, ":vars") {
				items[i].Line = w.Add("%s = %s", items[i].Key, items[i].Value)
				continue
			}
			items[i].Host = rapid.SampledFrom([]string{"h1", "h2.example.com"}).Draw(t, "host")
			items[i].Line = w.Add("%s other=1 %s=%q # c", items[i].Host, items[i].Key, items[i].Value)
		}
		checkItems(t, root, "hosts", []byte(w.Text.String()), items, func(it extracttest.Item) Spec {
			return Spec{File: "hosts", Section: it.Section, Host: it.Host, Key: it.Key}
		})
	})
}
