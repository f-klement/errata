package etc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// item is one line a generator writes: its text, the key a spec selects it
// by, and the value and condition the read gives.
type item struct {
	text, key, value, condition string
	line                        int
}

// lineGen writes one dialect's lines: a directive, and a line the tool
// passes over.
type lineGen struct {
	file      string
	directive func(t *rapid.T) item
	skipped   []string
}

// words is a generator of words joined by one of seps.
func words(t *rapid.T, label, word string, most int, seps ...string) string {
	n := rapid.IntRange(1, most).Draw(t, label+"s")
	var parts []string
	for range n {
		parts = append(parts, rapid.StringMatching(word).Draw(t, label))
	}
	return strings.Join(parts, rapid.SampledFrom(seps).Draw(t, label+"sep"))
}

// keyword draws a keyword from a few, so keys repeat, none of them one the
// dialect reads as a block or an include.
func keyword(t *rapid.T) string {
	return rapid.SampledFrom([]string{"a", "bb", "port", "x1", "Zed"}).Draw(t, "key")
}

// casing writes k in a case drawn for a dialect that matches keywords in any.
func casing(t *rapid.T, k string) string {
	if rapid.Bool().Draw(t, "upper") {
		return strings.ToUpper(k)
	}
	return k
}

var gens = map[string]lineGen{
	"sshd": {file: "sshd_config", skipped: []string{"# Port 1", "   #x", " "}, directive: func(t *rapid.T) item {
		k := keyword(t)
		v := words(t, "word", `[a-z0-9/.,*!-][a-z0-9/.,=*!-]{0,4}`, 3, " ", "\t", "  ")
		sep := rapid.SampledFrom([]string{" ", "=", " = ", "\t"}).Draw(t, "sep")
		tail := rapid.SampledFrom([]string{"", " # c", "\t#", "  "}).Draw(t, "tail")
		return item{text: rapid.SampledFrom([]string{"", "  ", "\t"}).Draw(t, "indent") + casing(t, k) + sep + v + tail, key: k, value: v}
	}},
	"sysctl": {file: "sysctl.d/10-a.conf", skipped: []string{"# a=1", "; a=1", "noequals", " "}, directive: func(t *rapid.T) item {
		k := words(t, "part", `[a-z0-9_]{1,3}`, 3, ".")
		v := words(t, "word", `[a-z0-9=,]{1,4}`, 2, " ")
		text := rapid.SampledFrom([]string{"", "-", " - "}).Draw(t, "ignore") + k + rapid.SampledFrom([]string{"=", " = ", "\t=  "}).Draw(t, "sep") + v
		if rapid.Bool().Draw(t, "slashes") {
			text = strings.ReplaceAll(text, ".", "/")
		}
		return item{text: text, key: k, value: v}
	}},
	"resolv": {file: "resolv.conf", skipped: []string{"; nameserver 1.1.1.1", "#search a", " nameserver 1.1.1.1", "nameserver", " "}, directive: func(t *rapid.T) item {
		k := rapid.SampledFrom(resolvKeys).Draw(t, "key")
		v := words(t, "word", `[a-z0-9.:]{1,6}`, 3, " ")
		if k == "nameserver" || k == "domain" {
			v = rapid.StringMatching(`[a-z0-9.:]{1,6}`).Draw(t, "one")
		}
		return item{text: k + rapid.SampledFrom([]string{" ", "\t", "  "}).Draw(t, "sep") + v, key: k, value: v}
	}},
	"limits": {file: "limits.conf", skipped: []string{"# * soft nofile 1", "* soft nofile", " "}, directive: func(t *rapid.T) item {
		dom := rapid.SampledFrom([]string{"*", "@admin", "root", "%g"}).Draw(t, "domain")
		typ := rapid.SampledFrom([]string{"soft", "hard", "-"}).Draw(t, "type")
		k := keyword(t)
		v := rapid.StringMatching(`[0-9a-z]{1,5}`).Draw(t, "value")
		sep := func() string { return rapid.SampledFrom([]string{" ", "\t", "   "}).Draw(t, "sep") }
		return item{text: dom + sep() + typ + sep() + k + sep() + v + rapid.SampledFrom([]string{"", " # c"}).Draw(t, "tail"), key: k, value: v, condition: dom + " " + typ}
	}},
	"logrotate": {file: "logrotate.d/app", skipped: []string{"# rotate 1", "  "}, directive: func(t *rapid.T) item {
		k := keyword(t)
		v := ""
		if rapid.Bool().Draw(t, "args") {
			v = words(t, "word", `[a-z0-9/.]{1,5}`, 2, " ")
		}
		text := k
		if v != "" {
			text += " " + v
		}
		return item{text: rapid.SampledFrom([]string{"", "  "}).Draw(t, "indent") + text, key: k, value: v}
	}},
	"nsswitch": {file: "nsswitch.conf", skipped: []string{"#hosts: x", "nocolon", " "}, directive: func(t *rapid.T) item {
		k := keyword(t)
		v := words(t, "word", `[a-z\[\]=]{1,6}`, 3, " ")
		return item{text: k + rapid.SampledFrom([]string{":", ": ", ":\t  "}).Draw(t, "sep") + v + rapid.SampledFrom([]string{"", " # c"}).Draw(t, "tail"), key: k, value: v}
	}},
	"modprobe": {file: "modprobe.d/m.conf", skipped: []string{"# options m x=1", " "}, directive: func(t *rapid.T) item {
		mod := rapid.SampledFrom([]string{"m", "snd_x"}).Draw(t, "module")
		v := words(t, "word", `[a-z0-9=,]{1,5}`, 3, " ")
		return item{text: "options " + mod + " " + v, key: "options", value: v, condition: mod}
	}},
	"chrony": {file: "chrony.conf", skipped: []string{"! pool x", "% a", "; a", "# a", " "}, directive: func(t *rapid.T) item {
		k := keyword(t)
		v := words(t, "word", `[a-z0-9.]{1,5}`, 3, " ")
		spaced := strings.ReplaceAll(v, " ", rapid.SampledFrom([]string{" ", "\t", "  "}).Draw(t, "gap"))
		return item{text: casing(t, k) + " " + spaced, key: k, value: v}
	}},
	"ntp": {file: "ntp.conf", skipped: []string{"# server a", " "}, directive: func(t *rapid.T) item {
		k := keyword(t)
		v := words(t, "word", `[a-z0-9.]{1,5}`, 3, " ")
		return item{text: k + " " + v + rapid.SampledFrom([]string{"", " # c", "#c"}).Draw(t, "tail"), key: k, value: v}
	}},
}

// Every value an etc extractor returns, in each dialect, sits at the line
// its keyword was written on, inside the file, whatever its separators,
// case and comments, with lines that end in a LF, a CRLF or a lone CR and
// lines the tool passes over between them; each key returns its values in
// file order. The ssh dialect reads as sshd does outside blocks.
func TestPropertyEtcValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.SampledFrom([]string{"chrony", "limits", "logrotate", "modprobe", "nsswitch", "ntp", "resolv", "sshd", "sysctl"}).Draw(t, "dialect")
		g := gens[name]
		w := extracttest.LineWriter{End: func() string { return rapid.SampledFrom([]string{"\n", "\r\n", "\r"}).Draw(t, "end") }}
		var items []item
		for range rapid.IntRange(1, 10).Draw(t, "n") {
			if rapid.Bool().Draw(t, "skip") {
				w.Add("%s", rapid.SampledFrom(g.skipped).Draw(t, "skipped"))
			}
			it := g.directive(t)
			it.line = w.Add("%s", it.text)
			items = append(items, it)
		}
		data := []byte(w.Text.String())
		p := filepath.Join(root, filepath.FromSlash(g.file))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			checkPropertyKey(t, name, g.file, root, it.key, items, data)
		}
	})
}

// checkPropertyKey holds the values the spec of key returns from file to
// the items written with that key, in order.
func checkPropertyKey(t *rapid.T, name, file, root, key string, items []item, data []byte) {
	got := extracttest.MustExtract(t, Build, Spec{Where: where(file), Key: key}, root)
	var gotTexts, want []string
	for _, v := range got {
		gotTexts = append(gotTexts, fmt.Sprintf("%s@%d[%s]", v.Text, v.At.Line, v.Condition))
	}
	for _, other := range items {
		if strings.EqualFold(other.key, key) {
			want = append(want, fmt.Sprintf("%s@%d[%s]", other.value, other.line, other.condition))
		}
	}
	if strings.Join(gotTexts, " ") != strings.Join(want, " ") {
		t.Fatalf("%s key %q: got %q, want %q\n%q", name, key, gotTexts, want, data)
	}
}
