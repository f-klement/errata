package table

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// written is one row a generator writes: each column's values as
// text@line, in order.
type written map[string][]string

// rowGen writes one dialect's rows: the file, the column whose value is
// unique to each row, a row, and lines the tool passes over.
type rowGen struct {
	file    string
	key     string
	row     func(t *rapid.T, w *extracttest.LineWriter, key string) written
	skipped []string
}

// at is each of texts at line n, as written lists them.
func at(n int, texts ...string) []string {
	var out []string
	for _, s := range texts {
		out = append(out, fmt.Sprintf("%s@%d", s, n))
	}
	return out
}

// sep draws white space between two fields.
func sep(t *rapid.T) string {
	return rapid.SampledFrom([]string{" ", "\t", "  ", " \t "}).Draw(t, "sep")
}

// list draws from 0 or 1 to most words of pattern.
func list(t *rapid.T, label, pattern string, least, most int) []string {
	var out []string
	for range rapid.IntRange(least, most).Draw(t, label+"s") {
		out = append(out, rapid.StringMatching(pattern).Draw(t, label))
	}
	return out
}

// word draws one word of pattern.
func word(t *rapid.T, label, pattern string) string {
	return rapid.StringMatching(pattern).Draw(t, label)
}

const plain = `[A-Za-z0-9=/._-]{1,6}`

var rowGens = map[string]rowGen{
	"fstab": {file: "etc/fstab", key: "file", skipped: []string{"# /dev/a /b c d", "  #x", " ", "/dev/a /b", "/dev/a /b c d 0 x"}, row: func(t *rapid.T, w *extracttest.LineWriter, key string) written {
		spec, vfs := word(t, "spec", plain), word(t, "vfstype", plain)
		opts := list(t, "opt", `[a-z0-9=]{1,4}`, 1, 3)
		trail := list(t, "number", `[0-9]`, 0, 2)
		fields := append([]string{spec, key, vfs, strings.Join(opts, ",")}, trail...)
		if len(trail) == 0 && rapid.Bool().Draw(t, "nooptions") {
			fields, opts = fields[:3], nil
		}
		n := w.Add("%s%s", rapid.SampledFrom([]string{"", " ", "\t"}).Draw(t, "indent"), joinSep(t, fields))
		trail = append(trail, "0", "0")
		return written{"spec": at(n, spec), "file": at(n, key), "vfstype": at(n, vfs), "mntops": at(n, opts...), "freq": at(n, trail[0]), "passno": at(n, trail[1])}
	}},
	"passwd": {file: "etc/passwd", key: "name", skipped: []string{"# a:b", " #x:y", " ", "short:x", "a:b:c"}, row: func(t *rapid.T, w *extracttest.LineWriter, key string) written {
		f := list(t, "field", `[a-z0-9/ #.]{0,5}`, 3, 3)
		uid, uidText := id(t, "uid")
		gid, gidText := id(t, "gid")
		shell := word(t, "shell", `[a-z0-9/:]{0,5}`)
		fields := []string{key, f[0], uidText, gidText, f[1], f[2], shell}
		// glibc reads a row of four fields or more, the rest empty.
		fields = fields[:rapid.IntRange(4, 7).Draw(t, "fields")]
		n := w.Add("%s", strings.Join(fields, ":"))
		for len(fields) < 7 {
			fields = append(fields, "")
		}
		return written{"name": at(n, key), "password": at(n, fields[1]), "uid": at(n, uid), "gid": at(n, gid), "gecos": at(n, fields[4]), "directory": at(n, fields[5]), "shell": at(n, fields[6])}
	}},
	"group": {file: "etc/group", key: "group_name", skipped: []string{"# a:b:c:d", " ", "a:b"}, row: func(t *rapid.T, w *extracttest.LineWriter, key string) written {
		pw := word(t, "password", `[a-z!*]{0,2}`)
		gid, gidText := id(t, "gid")
		members := list(t, "member", `[a-z0-9_]{1,5}`, 0, 3)
		text := fmt.Sprintf("%s:%s:%s", key, pw, gidText)
		if len(members) > 0 || rapid.Bool().Draw(t, "colon") {
			// glibc skips the white space before each member.
			text += ":" + strings.Join(members, rapid.SampledFrom([]string{",", ", ", ",\t"}).Draw(t, "comma"))
		}
		n := w.Add("%s", text)
		return written{"group_name": at(n, key), "password": at(n, pw), "gid": at(n, gid), "user_list": at(n, members...)}
	}},
	"hosts": {file: "etc/hosts", key: "ip_address", skipped: []string{"# 1.2.3.4 a", " ", "1.2.3.4", "  # x y", "1.2.3 a", "web1 a"}, row: func(t *rapid.T, w *extracttest.LineWriter, key string) written {
		canon := word(t, "canonical", `[a-z0-9.-]{1,6}`)
		aliases := list(t, "alias", `[A-Za-z0-9.-]{1,6}`, 0, 3)
		tail := rapid.SampledFrom([]string{"", " # c", "#x y"}).Draw(t, "tail")
		n := w.Add("%s%s", joinSep(t, append([]string{key, canon}, aliases...)), tail)
		return written{"ip_address": at(n, key), "canonical_hostname": at(n, canon), "aliases": at(n, aliases...), "names": at(n, append([]string{canon}, aliases...)...)}
	}},
	"crypttab": {file: "etc/crypttab", key: "target", skipped: []string{"# a b", " ", "lonely"}, row: func(t *rapid.T, w *extracttest.LineWriter, key string) written {
		src := word(t, "source", plain)
		fields := []string{key, src}
		keyFile, opts := "none", []string(nil)
		if rapid.Bool().Draw(t, "keyfile") {
			keyFile = word(t, "key", plain)
			fields = append(fields, keyFile)
			if opts = list(t, "opt", `[a-z0-9=-]{1,4}`, 0, 3); len(opts) > 0 {
				fields = append(fields, strings.Join(opts, ","))
			}
		}
		n := w.Add("%s", joinSep(t, fields))
		if keyFile == "-" {
			keyFile = "none" // as systemd reads it
		}
		if o := strings.Join(opts, ","); o == "-" || o == "none" {
			opts = nil // no option, as systemd reads it
		}
		return written{"target": at(n, key), "source": at(n, src), "key_file": at(n, keyFile), "options": at(n, opts...)}
	}},
	"exports": {file: "etc/exports", key: "path", skipped: []string{"# /a b", " ", "  # x"}, row: func(t *rapid.T, w *extracttest.LineWriter, key string) written {
		var head strings.Builder
		var defaults []string
		head.WriteString(key)
		// A - word sets the defaults of the client after it.
		if rapid.Bool().Draw(t, "dash") {
			defaults = drawOptions(t, "default", 1)
			head.WriteString(sep(t) + "-" + strings.Join(defaults, ","))
		}
		host := word(t, "client", `[a-z0-9.*@/]{0,6}`)
		own := drawOptions(t, "opt", 0)
		clientText := host + "(" + strings.Join(own, ",") + ")"
		n := w.Add("%s", head.String()+" \\")
		m := w.Add("  %s%s", clientText, rapid.SampledFrom([]string{"", " # c"}).Draw(t, "tail"))
		if host == "" {
			host = "*" // as nfs-utils reads (options) alone
		}
		return written{"path": at(n, key), "client": at(m, host), "options": append(at(n, defaults...), at(m, own...)...)}
	}},
}

// id draws a uid or a gid, as glibc reads it and as a file may write it:
// with leading zeros, a + or white space before it.
func id(t *rapid.T, label string) (read, written string) {
	n := rapid.IntRange(0, 99999).Draw(t, label)
	pre := rapid.SampledFrom([]string{"", "0", "00", "+", " "}).Draw(t, label+"pre")
	return strconv.Itoa(n), pre + strconv.Itoa(n)
}

// drawOptions draws from least to 3 options nfs-utils knows.
func drawOptions(t *rapid.T, label string, least int) []string {
	var out []string
	for range rapid.IntRange(least, 3).Draw(t, label+"s") {
		out = append(out, rapid.SampledFrom([]string{"ro", "rw", "sync", "no_subtree_check", "fsid=0", "sec=sys"}).Draw(t, label))
	}
	return out
}

// joinSep is fields joined by drawn white space.
func joinSep(t *rapid.T, fields []string) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteString(sep(t))
		}
		b.WriteString(f)
	}
	return b.String()
}

// Every value a table extractor returns, in each dialect, sits at the line
// its field was written on, inside the file, whatever the white space,
// comments and lines the tool passes over between rows, with lines that
// end in a LF, a CRLF or a lone CR; each column of the row a key selects
// returns that row's values in order, a list's items each one, a column a
// row leaves out its default.
func TestPropertyTableValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.SampledFrom([]string{"crypttab", "exports", "fstab", "group", "hosts", "passwd"}).Draw(t, "dialect")
		g := rowGens[name]
		w := extracttest.LineWriter{End: func() string { return rapid.SampledFrom([]string{"\n", "\r\n", "\r"}).Draw(t, "end") }}
		var rows []written
		var keys []string
		for i := range rapid.IntRange(1, 6).Draw(t, "n") {
			if rapid.Bool().Draw(t, "skip") {
				w.Add("%s", rapid.SampledFrom(g.skipped).Draw(t, "skipped"))
			}
			key := fmt.Sprintf("/k%d", i)
			if name == "hosts" {
				key = fmt.Sprintf("10.0.0.%d", i)
			}
			rows = append(rows, g.row(t, &w, key))
			keys = append(keys, key)
		}
		data := []byte(w.Text.String())
		p := filepath.Join(root, filepath.FromSlash(g.file))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		for i, r := range rows {
			for column, want := range r {
				got := extracttest.MustExtract(t, Build, col(g.file, column, g.key, keys[i]), root)
				var texts []string
				for _, v := range got {
					texts = append(texts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
				}
				if strings.Join(texts, " ") != strings.Join(want, " ") {
					t.Fatalf("%s row %s column %s: got %q, want %q\n%q", name, keys[i], column, texts, want, data)
				}
			}
		}
	})
}
