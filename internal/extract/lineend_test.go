package extract

import (
	"context"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Every kind numbers a value's line at a LF, a CRLF and a lone CR alike, as
// the HTML standard reads a file's line ends (core/lineend). TOML is not
// here: it refuses a lone CR anywhere in a file. Each file puts
// its value on a line that only a lone CR starts, so a reader that counts
// LF alone reports it a line or more too early. The planted breaks: a lone
// CR not counted by lineend.Count, which moves every kind but those that
// fold the file first; a lone CR not folded by lineend.Fold, which moves
// those; and each reader's own count of its lines (ini's dialects,
// properties, etc, toolversion's and packageversion's line reads), each put back
// to count LF alone.
func TestEveryKindNumbersALoneCRsLine(t *testing.T) {
	files := map[string]string{
		"r.txt":          "a\r\rv=1\r",
		"y.yaml":         "a: 0\rb: 1\rv: '1'\r",
		"j.json":         "{\r\"a\": 0,\r\"v\": \"1\"}\r",
		"x.xml":          "<a>\r<b/>\r<v>1</v></a>",
		"p.properties":   "a=0\rb=0\rv=1\r",
		"plain.ini":      "[s]\rx = 0\rv = 1\r",
		"git.config":     "[s]\n\tx = a\rb\n\tv = 1\n",
		"php.ini":        "[s]\rx = 0\rv = 1\r",
		"hosts":          "[web]\rh1\rh2 v=1\r",
		"u.service":      "[s]\rx=0\rv=1\r",
		"q.container":    "[s]\nx=a\rb\nv=1\n",
		"api.vars":       "# c\rx\nV=1\n",
		"s.sh":           "# c\rx\nV=1\n",
		".env":           "# c\rx\nV=1\n",
		"Jenkinsfile":    "// c\rpipeline {\n  agent any // c\renvironment { V = '1' }\n  stages { stage('s') { steps { echo 'x' } } }\n}\n",
		".tool-versions": "# c\rx\nnodejs 20.1.0\r18.0.0\n",
		".sdkmanrc":      "# c\r\n\r\rjava=\r21\n",
		"VERSION":        "\r\r1.2.3\n",
		"c.go":           "x := 1\r// v=1\n",
		"m.md":           "# A\r\r```\rv\r```\r",
		"t.md":           "a\r\rv=1\r",
		"h.html":         "<p>a\r\r<pre>v</pre>",
		"a.adoc":         "= T\r\r----\rv\r----\r",
		"d.rst":          "a\r\r.. v\r",
		"sshd_config":    "# c\rPort 1\rv 1\r",
	}
	root := extracttest.Repo(t, files)
	for raw, want := range map[string]string{
		`{"regex": {"file": "r.txt", "pattern": "v=(\\d)"}}`:                             "1@3",
		`{"yaml": {"file": "y.yaml", "path": "v"}}`:                                      "1@3",
		`{"json": {"file": "j.json", "path": "v"}}`:                                      "1@3",
		`{"xml": {"file": "x.xml", "path": "/a/v"}}`:                                     "1@3",
		`{"properties": {"file": "p.properties", "key": "v"}}`:                           "1@3",
		`{"ini": {"file": "plain.ini", "section": "s", "key": "v"}}`:                     "1@3",
		`{"ini": {"file": "git.config", "dialect": "git", "section": "s", "key": "v"}}`:  "1@4",
		`{"ini": {"file": "php.ini", "dialect": "php", "section": "s", "key": "v"}}`:     "1@3",
		`{"ini": {"file": "hosts", "dialect": "ansible", "section": "web", "key": "v"}}`: "1@3",
		`{"ini": {"file": "u.service", "section": "s", "key": "v"}}`:                     "1@3",
		`{"ini": {"file": "q.container", "section": "s", "key": "v"}}`:                   "1@4",
		`{"systemd": {"file": "api.vars", "variable": "V"}}`:                             "1@3",
		`{"shell": {"file": "s.sh", "name": "V"}}`:                                       "1@3",
		`{"envfile": {"file": ".env", "name": "V"}}`:                                     "1@3",
		`{"jenkins": {"file": "Jenkinsfile", "variable": "V", "stage": "s"}}`:            "1@4",
		`{"tool_version": {"tool": "node", "files": [".tool-versions"]}}`:                "20.1.0@3 18.0.0@4",
		`{"tool_version": {"tool": "java", "files": [".sdkmanrc"]}}`:                     "21@5",
		`{"package_version": {"files": ["VERSION"]}}`:                                    "1.2.3@3",
		`{"comment": {"file": "c.go", "pattern": "v=(\\d)"}}`:                            "1@2",
		`{"markdown": {"file": "m.md", "target": "code"}}`:                               "v@4",
		`{"text": {"file": "t.md", "pattern": "v=(\\d)"}}`:                               "1@3",
		`{"html": {"file": "h.html", "target": "code"}}`:                                 "v@3",
		`{"asciidoc": {"file": "a.adoc", "target": "code"}}`:                             "v@4",
		`{"rst": {"file": "d.rst", "target": "comment"}}`:                                "v@3",
		`{"etc": {"file": "sshd_config", "key": "v"}}`:                                   "1@3",
	} {
		e, err := Build(decode(t, raw))
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		got, err := e.Extract(context.Background(), root)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if extracttest.Texts(got) != want {
			t.Errorf("%s: got %q, want %q", raw, extracttest.Texts(got), want)
		}
	}
}
