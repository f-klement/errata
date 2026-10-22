package ini

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

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
		i := Spec{File: fuzzFiles[int(file)%len(fuzzFiles)], Section: section, Host: host, Key: key}
		if int(dialect) < len(iniDialects) {
			i.Dialect = iniDialects[dialect].name
		}
		if sub != "" {
			i.Subsection = &sub
		}
		_ = Place(i) // Place takes any spec, built or not
		e, err := Build(i)
		if err != nil {
			return
		}
		coretest.CheckEntryValues(t, e.(core.EntryExtractor), []byte(data), len(data))
	})
}
