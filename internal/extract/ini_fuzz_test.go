package extract

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkEntryValues runs e on data and checks what every extractor promises:
// values at lines inside the file, no more than maxValues of them, an error
// and no values otherwise, and the values the query wants from the
// entries the reader visits, which hold no more bytes than the file but for
// git's true and Java's U+FFFD. The query's wants is the reference for
// fromData's matcher, which compares a section once for each header.
func checkEntryValues(t *testing.T, e entryExtractor, data []byte) {
	t.Helper()
	got, err := e.fromData(data)
	var want []string
	total := 0
	lines := bytes.Count(data, newline) + 1
	rerr := e.read(data, nil, func(en *iniEntry) error {
		if en.line < 1 || en.line > lines {
			t.Fatalf("an entry at line %d of %d", en.line, lines)
		}
		if !en.noValue {
			total += len(en.value)
		}
		if e.query.wants(en) {
			want = append(want, string(en.value))
		}
		return nil
	})
	// Java's U+FFFD for a byte that is not UTF-8 is three bytes for one.
	if bound := javaLen(data); total > bound {
		t.Fatalf("the entries hold %d bytes, more than the file's %d", total, bound)
	}
	switch {
	case err != nil && got != nil:
		t.Fatalf("values %v with error %v", got, err)
	case err != nil && rerr == nil && len(want) <= maxValues:
		t.Fatalf("refused %v, and the reader reads the file", err)
	case err == nil && rerr != nil:
		t.Fatalf("values %v, and the reader refuses the file: %v", got, rerr)
	case err == nil && len(got) != len(want):
		t.Fatalf("%d values, the query wants %d", len(got), len(want))
	}
	for i, v := range got {
		if v.Text != want[i] || v.At.Line < 1 || v.At.Line > lines || v.At.File != e.file {
			t.Fatalf("value %q at %s:%d of %d lines, want %q", v.Text, v.At.File, v.At.Line, lines, want[i])
		}
	}
}

// fuzzFiles are names that choose each dialect.
var fuzzFiles = []string{"setup.cfg", ".gitmodules", "x/.git/config", "hosts", "inventory/a", "tox.ini", "php.ini", "etc/php/conf.d/a.ini"}

// Every dialect reads any file into entries at lines inside it, holding no
// more bytes than the file, and an ini extractor returns exactly the values
// of the entries its query wants.
func FuzzINI(f *testing.F) {
	// The dialect is the index in iniDialects (0 git, 1 ansible, 2 tox, 3 php,
	// 4 plain), and any other byte none, which the file's name, the index in
	// fuzzFiles, chooses.
	f.Add(uint8(4), uint8(0), setupCfg, "options", "", "", "install_requires")
	f.Add(uint8(4), uint8(0), "a = 1\n[s]\n  k = a\n\n  b\n# c\n\tc\n[t]\nk: 2\r\n", "s", "", "", "k")
	f.Add(uint8(0), uint8(0), gitconfig, "remote", "origin", "", "fetch")
	f.Add(uint8(9), uint8(1), gitmodules, "submodule", "Docs", "", "url")
	f.Add(uint8(0), uint8(0), "A", "A", "0", "", "A")
	f.Add(uint8(0), uint8(0), "\xef\xbb\xbf[a \"b\\\"c\"] k = \"x ; y\" \\\n z # c\r\n[A.B]K\n", "a", "b", "", "k")
	f.Add(uint8(9), uint8(3), inventory, "web", "", "web1", "")
	f.Add(uint8(1), uint8(0), inventory, "web:vars", "", "", "ntp")
	f.Add(uint8(1), uint8(0), "h1 a='x y' b=\"c\\\"d\" x=\"5 # c\" # e\fh2\u2028[g:children]\nweb # c\n", "ungrouped", "", "h1", "b")
	f.Add(uint8(9), uint8(5), "[testenv]\ndeps = a # c\n  b\\\n  c\ncommands = x \\#y\n", "testenv", "", "", "DEPS")
	f.Add(uint8(9), uint8(6), "[PHP]\na = x \"y ; z\" 'w' ; c\nb = E_ALL & ~E_DEPRECATED\nc = ${HOME:-x}\nd[] = on\n", "PHP", "", "", "a")
	f.Fuzz(func(t *testing.T, dialect, file uint8, data, section, sub, host, key string) {
		i := INI{File: fuzzFiles[int(file)%len(fuzzFiles)], Section: section, Host: host, Key: key}
		if int(dialect) < len(iniDialects) {
			i.Dialect = iniDialects[dialect].name
		}
		if sub != "" {
			i.Subsection = &sub
		}
		_ = Spec{INI: &i}.Place() // Place takes any spec, built or not
		e, err := Build(Spec{INI: &i})
		if err != nil {
			return
		}
		checkEntryValues(t, e.(entryExtractor), []byte(data))
	})
}

// The properties reader reads any file into keys and values at lines
// inside it, and every key and value it reads, written again as
// Properties.store writes them, reads back as itself.
func FuzzProperties(f *testing.F) {
	f.Add("fruits  apple, \\\n   banana\n# c \\\n\\:\\= = x\n", "fruits")
	f.Add("a=\\u0041\\uD83D\\uDE00\\uD83Dx\r\n\\\n#c\r\\\n  b c\\\r\n d", "a")
	f.Add("\f k \t:= \\t\\n\\r\\f\\z\\\\\n!c\n\\u12", "k")
	f.Fuzz(func(t *testing.T, data, key string) {
		e, err := Build(Spec{Properties: &Properties{File: "f", Key: key}})
		if err != nil {
			return
		}
		checkEntryValues(t, e.(entryExtractor), []byte(data))
		var written strings.Builder
		var keys, values []string
		_ = readProperties([]byte(data), nil, func(en *iniEntry) error {
			if utf8.Valid(en.key) && utf8.Valid(en.value) && !strings.ContainsRune(string(en.key)+string(en.value), utf8.RuneError) {
				keys, values = append(keys, string(en.key)), append(values, string(en.value))
				written.WriteString(javaEscape(string(en.key), true) + "=" + javaEscape(string(en.value), false) + "\n")
			}
			return nil
		})
		i := 0
		err = readProperties([]byte(written.String()), nil, func(en *iniEntry) error {
			if string(en.key) != keys[i] || string(en.value) != values[i] || en.line != i+1 {
				t.Fatalf("%q=%q at line %d read back as %q=%q at line %d", keys[i], values[i], i+1, en.key, en.value, en.line)
			}
			i++
			return nil
		})
		if err != nil || i != len(keys) {
			t.Fatalf("%d of %d read back: %v\n%s", i, len(keys), err, written.String())
		}
	})
}
