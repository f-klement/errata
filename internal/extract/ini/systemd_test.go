package ini

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// unit is a spec of a.service in the systemd dialect, chosen by its name.
func unit(section, key string) Spec { return ini("a.service", section, key) }

// Each case is what systemd 257's config_parse reads from the file, by its
// source (src/shared/conf-parser.c, src/basic/fileio.c): the values of the
// key in file order, a reset dropping those before it. Planted breaks: the
// comment test after a \ line dropped, so a comment ends the value; a \
// kept rather than read as a space; \n\r read as two line ends; a reset
// read as an empty value; the BOM taken from every line.
func TestSystemdReadsWhatSystemdReads(t *testing.T) {
	for _, c := range []struct {
		name, body string
		spec       Spec
		want       string
	}{
		{"a value", "[Service]\nExecStart=/bin/a -x\n", unit("Service", "ExecStart"), "/bin/a -x@2"},
		{"spaces around = and the value", "[Service]\n  Type  =  simple \t\n", unit("Service", "Type"), "simple@2"},
		{"a key set twice gives both", "[Unit]\nAfter=a\nAfter=b c\n", unit("Unit", "After"), "a@2 b c@3"},
		{"a reset drops what came before", "[Service]\nExecStart=/a\nExecStart=\nExecStart=/b\n", unit("Service", "ExecStart"), "/b@4"},
		{"a reset of another key drops nothing", "[Service]\nExecStart=/a\nExecStop=\n", unit("Service", "ExecStart"), "/a@2"},
		{"a reset in another section drops nothing", "[Service]\nX=/a\n[Unit]\nX=\n", unit("Service", "X"), "/a@2"},
		{"a reset of spaces", "[Service]\nX=/a\nX=  \t\n", unit("Service", "X"), ""},
		{"\\ continues, as a space", "[Service]\nExecStart=/bin/a \\\n  -x \\\n  -y\n", unit("Service", "ExecStart"), "/bin/a    -x    -y@2"},
		{"a comment between continued lines", "[Service]\nExecStart=/a\\\n# no\n; no\n  -x\n", unit("Service", "ExecStart"), "/a   -x@2"},
		{"an empty line ends a continued value", "[Service]\nX=a\\\n\nY=b\n", unit("Service", "X"), "a@2"},
		{"\\\\ at the end continues nothing", "[Service]\nX=a\\\\\nY=b\n", unit("Service", "X"), `a\\@2`},
		{"\\ then a space continues nothing", "[Service]\nX=a\\ \nX=b\n", unit("Service", "X"), `a\@2 b@3`},
		{"a key on the line a \\ continues", "[Service]\n\\\nX=a\n", unit("Service", "X"), "a@3"},
		{"a \\ on the last line", "[Service]\nX=a\\", unit("Service", "X"), "a@2"},
		{"a comment line is no value", "[Service]\n  # X=a\n\t;X=b\nX=c # not a comment\n", unit("Service", "X"), "c # not a comment@4"},
		{"a line with no = is ignored", "[Service]\nnothing here\n=x\nX=a\n", unit("Service", "X"), "a@4"},
		{"a key before any section is ignored", "X=a\n[Service]\nX=b\n", unit("Service", "X"), "b@3"},
		{"= in the value", "[Service]\nEnvironment=A=1 B=2\n", unit("Service", "Environment"), "A=1 B=2@2"},
		{"a section written twice", "[Service]\nX=a\n[Unit]\n[Service]\nX=b\n", unit("Service", "X"), "a@2 b@5"},
		{"a space in a section's name", "[Ser vice]\nX=a\n", unit("Ser vice", "X"), "a@2"},
		{"names compared as written", "[service]\nx=a\n[Service]\nX=b\n", unit("Service", "X"), "b@4"},
		{"a lone \\r ends a line", "[Service]\rX=a\rX=b\r", unit("Service", "X"), "a@2 b@3"},
		{"\\r\\n is one line end", "[Service]\r\nX=a\\\r\n b\r\n", unit("Service", "X"), "a  b@2"},
		{"\\n\\r is one line end to systemd, two to errata", "[Service]\nX=a\\\n\r b\n", unit("Service", "X"), "a  b@2"},
		{"\\n\\n is two", "[Service]\nX=a\\\n\n b\n", unit("Service", "X"), "a@2"},
		{"NUL ends a line", "[Service]\x00X=a\x00\x00X=b\n", unit("Service", "X"), "a@1 b@1"},
		{"a byte order mark", "\xef\xbb\xbf[Service]\nX=a\n", unit("Service", "X"), "a@2"},
		{"the first byte order mark goes, wherever it is", "[Service]\n\xef\xbb\xbfX=a\n", unit("Service", "X"), "a@2"},
		{"a second byte order mark stays", "\xef\xbb\xbf[Service]\n\xef\xbb\xbfX=a\nX=b\n", unit("Service", "X"), "b@3"},
		{"a quadlet file", "[Container]\nImage=docker.io/library/nginx:1.27\n", ini("web.container", "Container", "Image"), "docker.io/library/nginx:1.27@2"},
		{"a drop-in", "[Service]\nX=a\n", ini("a.service.d/10-x.conf", "Service", "X"), "a@2"},
		{"a timer", "[Timer]\nOnCalendar=Mon..Fri 09:00\nOnCalendar=\nOnCalendar=daily\n", ini("backup.timer", "Timer", "OnCalendar"), "daily@4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, c.spec.File, c.body, c.spec); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A value systemd computes is returned as written and marked not literal:
// a specifier, and a $ in a command line. Planted breaks: % not marked; $
// marked outside an Exec key.
func TestSystemdMarksWhatSystemdComputes(t *testing.T) {
	body := "[Service]\nExecStart=/bin/a --home=%h\nExecStart=\nExecStart=/bin/a $PORT\nUser=$x\nDescription=100%% done\nWorkingDirectory=/srv\nExecStop=$X\nGroup=%g\n"
	root := extracttest.Repo(t, map[string]string{"a.service": body})
	for key, want := range map[string]string{"ExecStart": "/bin/a $PORT@4 true", "User": "$x@5 false", "Description": "100%% done@6 true", "WorkingDirectory": "/srv@7 false",
		"ExecStop": "$X@8 true", "Group": "%g@9 true"} {
		var got []string
		for _, v := range mustRun(t, unit("Service", key), root) {
			got = append(got, fmt.Sprintf("%s@%d %t", v.Text, v.At.Line, v.NotLiteral))
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
}

// The planted breaks: files systemd refuses to load. Each refusal names
// the line and the reason.
func TestSystemdRefusesWhatSystemdRefuses(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"a header without ]", "[Service\nX=a\n", `a.service: line 1: the section header "[Service" does not end in ], which systemd refuses`},
		{"a quote in a header", "[Ser\"vice]\n", `a.service: line 1: the section header "[Ser\"vice]" holds a quote, a \ or a control character, which systemd refuses`},
		{"a \\ in a header", "\n[Ser\\vice]\n", `a.service: line 2: the section header "[Ser\\vice]" holds a quote`},
		{"a control character in a header", "[Ser\x01vice]\n", `a.service: line 1: the section header "[Ser\x01vice]" holds a quote`},
		{"a line not UTF-8", "[Service]\nX=\xff\n", "a.service: line 2: the line is not UTF-8, which systemd refuses"},
		{"a line not UTF-8, continued", "[Service]\nX=a\\\n\xff\n", "a.service: line 2: the line is not UTF-8, which systemd refuses"},
		{"a line of 1 MiB", "[Service]\nX=" + strings.Repeat("a", longLine-2) + "\n", "a.service: line 2: a line of 1048576 bytes or more, which systemd refuses"},
		{"a continued line past 1 MiB", "[Service]\nX=" + strings.Repeat("a", longLine/2) + "\\\n" + strings.Repeat("b", longLine/2) + "\n", "a.service: line 3: a line a \\ continues grows past 1048576 bytes, which systemd refuses"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniError(t, "a.service", c.body, unit("Service", "X")); !strings.HasPrefix(got, c.want) {
				t.Errorf("error %q, want %q", got, c.want)
			}
		})
	}
	// Just under the limits, the line is read.
	body := "[Service]\nX=" + strings.Repeat("a", longLine-3) + "\n"
	if got := iniValues(t, "a.service", body, unit("Service", "X")); len(got) != longLine-3+2 {
		t.Errorf("a line of %d bytes: %d bytes of values", longLine-1, len(got))
	}
	body = "[Service]\nX=" + strings.Repeat("a", longLine/2) + "\\\n" + strings.Repeat("b", longLine/2-3) + "\n"
	if got := iniValues(t, "a.service", body, unit("Service", "X")); len(got) != longLine {
		t.Errorf("a continued line of %d bytes: %d bytes of values", longLine, len(got))
	}
}

// The ini kind chooses the systemd dialect by a unit's or a Quadlet file's
// type, and by a .conf in a drop-in directory; a section and a key are
// required. Planted breaks: a .conf outside a .d directory chosen; the
// Quadlet types left out.
func TestSystemdDialectIsChosenByName(t *testing.T) {
	for file, want := range map[string]string{
		"a.service": "systemd", "deploy/web.container": "systemd", "x.timer": "systemd", "a@.service": "systemd",
		"b.network": "systemd", "c.kube.j2": "systemd", "a.service.d/10.conf": "systemd", "service.d/x.conf": "systemd",
		"web.container.d/x.conf": "systemd", "a.conf": "plain", "x.d/a.conf": "plain", "a.service.d/x.ini": "plain",
		".service": "plain", "a.services": "plain",
	} {
		if got := Describe(Spec{File: file}).Label; got != "ini, "+want+" dialect on "+pathBase(file) {
			t.Errorf("%s: %q, want the %s dialect", file, got, want)
		}
	}
	_, err := Build(ini("a.service", "", "X"))
	extracttest.WantError(t, err, "ini: systemd: section is required")
	_, err = Build(ini("a.service", "Service", ""))
	extracttest.WantError(t, err, "ini: systemd: key is required")
	_, err = Build(Spec{File: "a.service", Section: "S", Key: "K", Host: "h"})
	extracttest.WantError(t, err, "host is read only by the ansible dialect")
}

func pathBase(file string) string { _, b := core.FileParts(file); return b }

// systemd's drop-in directories for a unit, in the order
// unit_file_find_dropin_paths lists them. Planted breaks: the template's
// directory left out; the prefixes taken from the shortest; a trailing dash
// chopped twice; a directory listed twice, which a-b@x.service's template
// and prefix both reach.
func TestDropInDirs(t *testing.T) {
	for name, want := range map[string]string{
		"a.service":           "a.service.d service.d",
		"foo-bar-baz.service": "foo-bar-baz.service.d foo-bar-.service.d foo-.service.d service.d",
		"a@x.service":         "a@x.service.d a@.service.d service.d",
		"a-b@x.service":       "a-b@x.service.d a-b@.service.d a-.service.d a-@x.service.d a-@.service.d service.d",
		"a-b@.service":        "a-b@.service.d a-.service.d service.d",
		"a-.service":          "a-.service.d service.d",
		"a--b.service":        "a--b.service.d a--.service.d a-.service.d service.d",
		"-a.service":          "-a.service.d service.d",
		"@x.service":          "@x.service.d @.service.d service.d",
		"@a-b.service":        "@a-b.service.d @.service.d service.d",
		"web-db.container":    "web-db.container.d web-.container.d container.d",
	} {
		dirs, err := dropInDirs(name, nil)
		if got := strings.Join(dirs, " "); err != nil || got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// The drop-ins are applied by name across the directories, a name in two
// read from the first, and a hidden file or one not .conf left out, a
// template taken as the file it renders. Planted breaks: a later
// directory's file of one name winning; a template's name taken with its
// suffix.
func TestOrderDropIns(t *testing.T) {
	files := map[string][]string{
		"a.service.d": {"d/a.service.d/20-b.conf", "d/a.service.d/x.txt", "d/a.service.d/.h.conf"},
		"service.d":   {"d/service.d/10-a.conf", "d/service.d/20-b.conf", "d/service.d/30-c.conf"},
	}
	got := orderDropIns([]string{"a.service.d", "service.d"}, func(d string) []string { return files[d] })
	if want := "d/service.d/10-a.conf d/a.service.d/20-b.conf d/service.d/30-c.conf"; strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A template is read as the drop-in it renders, and masks that name.
	files = map[string][]string{
		"a.service.d": {"d/a.service.d/20-b.conf.j2", "d/a.service.d/x.txt.j2"},
		"service.d":   {"d/service.d/20-b.conf", "d/service.d/30-c.conf"},
	}
	got = orderDropIns([]string{"a.service.d", "service.d"}, func(d string) []string { return files[d] })
	if want := "d/a.service.d/20-b.conf.j2 d/service.d/30-c.conf"; strings.Join(got, " ") != want {
		t.Errorf("templates: got %q, want %q", got, want)
	}
}

// texts lists values as text@file:line, marked * when not literal, and
// with their origin in brackets when they have one.
func texts(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		s := fmt.Sprintf("%s@%s:%d", v.Text, v.At.File, v.At.Line)
		if v.NotLiteral {
			s += "*"
		}
		if v.Origin != "" {
			s += " [" + v.Origin + "]"
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

func systemdValues(t *testing.T, root string, s SystemdSpec) string {
	t.Helper()
	return texts(extracttest.MustRun(t, BuildSystemd, s, root))
}

func sd(file, section, key, variable string) SystemdSpec {
	return SystemdSpec{OneFile: core.OneFile{File: file}, Section: section, Key: key, Variable: variable}
}

// The systemd kind reads the unit and its drop-ins in systemd's order, a
// reset in a later file dropping the values of the files before. Planted
// breaks: the drop-ins left out; read in git's order, not by name; the
// type's directory left out; a reset kept to its own file.
func TestSystemdAppliesDropIns(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"deploy/api-v2.service":                "[Service]\nExecStart=/bin/api\nEnvironment=A=1\n",
		"deploy/api-v2.service.d/20-cmd.conf":  "[Service]\nExecStart=\nExecStart=/bin/api --port 81\n",
		"deploy/api-.service.d/10-env.conf":    "[Service]\nEnvironment=A=2\n",
		"deploy/service.d/30-all.conf":         "[Service]\nEnvironment=A=3 B=4\n",
		"deploy/service.d/20-cmd.conf":         "[Service]\nExecStart=/never\n",
		"deploy/api-v2.service.d/.hidden.conf": "[Service]\nExecStart=/hidden\n",
		"deploy/api-v2.service.d/notes.txt":    "[Service]\nExecStart=/notes\n",
		"deploy/other.service.d/10.conf":       "[Service]\nExecStart=/other\n",
		"api-v2.service.d/10.conf":             "[Service]\nExecStart=/top\n",
	})
	if got, want := systemdValues(t, root, sd("deploy/api-v2.service", "Service", "ExecStart", "")), "/bin/api --port 81@deploy/api-v2.service.d/20-cmd.conf:3"; got != want {
		t.Errorf("ExecStart: got %s\nwant %s", got, want)
	}
	want := "1@deploy/api-v2.service:3; 2@deploy/api-.service.d/10-env.conf:2; 3@deploy/service.d/30-all.conf:2"
	if got := systemdValues(t, root, sd("deploy/api-v2.service", "Service", "Environment", "A")); got != want {
		t.Errorf("Environment A: got %s\nwant %s", got, want)
	}
	// A drop-in named itself is read alone.
	if got, want := systemdValues(t, root, sd("deploy/service.d/20-cmd.conf", "Service", "ExecStart", "")), "/never@deploy/service.d/20-cmd.conf:2"; got != want {
		t.Errorf("a drop-in: got %s, want %s", got, want)
	}
	// The unit and its drop-ins hold max_bytes in all.
	s := sd("deploy/api-v2.service", "Service", "ExecStart", "")
	s.MaxBytes = 100
	_, err := extracttest.Run(t, BuildSystemd, s, root)
	extracttest.WantError(t, err, "the unit and its drop-ins hold at most max_bytes, 100, in all")
}

// Environment= splits into assignments as systemd splits it; variable
// takes the ones of its name. A word with a specifier is not literal, and
// so is every value of a section with an EnvironmentFile= no reset
// dropped, since the file errata cannot read is read after Environment=.
// Planted breaks: quotes kept; escapes not read; a word systemd refuses
// dropping the words before it; EnvironmentFile= not marking; its reset
// not clearing the mark.
func TestSystemdSplitsEnvironment(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.service": "[Service]\nEnvironment=PORT=80 \"HOST=a b\" 'X=y z'\nEnvironment=PORT=\\x38\\061 PORT=%i\nEnvironment=PORT=9 X=\"open\nEnvironment=PORT=1 X=\\q PORT=2\nEnvironment=\"PORT=3\" 1A=2 PORT-B=1\n",
		"b.service": "[Service]\nEnvironment=PORT=1\nEnvironmentFile=-/etc/b.env\n",
		"c.service": "[Service]\nEnvironment=PORT=1\nEnvironmentFile=/etc/c.env\nEnvironmentFile=\n",
		"d.service": "[Service]\nEnvironment=PORT=1\n[Install]\nEnvironmentFile=/etc/d.env\n",
	})
	want := "80@a.service:2; 81@a.service:3; %i@a.service:3*; 9@a.service:4; 1@a.service:5; 3@a.service:6"
	if got := systemdValues(t, root, sd("a.service", "Service", "Environment", "PORT")); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got, want := systemdValues(t, root, sd("a.service", "Service", "Environment", "HOST")), "a b@a.service:2"; got != want {
		t.Errorf("HOST: got %s, want %s", got, want)
	}
	for file, want := range map[string]string{"b.service": "1@b.service:2*", "c.service": "1@c.service:2", "d.service": "1@d.service:2"} {
		if got := systemdValues(t, root, sd(file, "Service", "Environment", "PORT")); got != want {
			t.Errorf("%s: got %s, want %s", file, got, want)
		}
	}
}

// environWords reads each escape cunescape_one reads, and refuses the
// rest; the words before a refused one are kept. Planted breaks: \s not
// read; an octal escape of NUL taken; \U past U+10FFFF taken.
func TestEnvironWords(t *testing.T) {
	for in, want := range map[string]string{
		`a b	c`:                            "a|b|c",
		`  a  `:                            "a",
		`a"b c"d`:                          "ab cd",
		`'a\'b'`:                           "a'b",
		`"a\"b"`:                           `a"b`,
		`\a\b\f\n\r\t\v\\\"\'\s`:           "\a\b\f\n\r\t\v\\\"' ",
		`\x41\101é\U0001F600`:              "AAé😀",
		`\012\177\xff`:                     "\n\x7f\xff",
		`\018`:                             "ERR",
		`\x09\x0a\x0f\xa0\xf0\xA0\xF0\x9F`: "\x09\x0a\x0f\xa0\xf0\xa0\xf0\x9f",
		`\x/0`:                             "ERR",
		`\x:0`:                             "ERR",
		"\\x`0":                            "ERR",
		`\xg0`:                             "ERR",
		`\x@0`:                             "ERR",
		`\xG0`:                             "ERR",
		`A\u007f\u0080߿ࠀ\U00010000`:        "A\x7f\u0080߿ࠀ\U00010000",
		`\U0000D7FF\U0000E000\U0010FFFD`:   "퟿\U0010fffd",
		`\U0000D800`:                       "ERR",
		`\U0000DFFF`:                       "ERR",
		`\U0010FFFF`:                       "ERR",
		`\ud800`:                           "\xed\xa0\x80",
		`""`:                               "",
		`a ""`:                             "a|",
		`a \q b`:                           "a|ERR",
		`a \x00`:                           "a|ERR",
		`\000`:                             "ERR",
		`\400`:                             "ERR",
		`\u0000`:                           "ERR",
		`\U00110000`:                       "ERR",
		`\UFFFFFFFF`:                       "ERR",
		`\U0000FFFE`:                       "ERR",
		`\x4`:                              "ERR",
		`a\`:                               "ERR",
		`"a`:                               "ERR",
		`x 'a`:                             "x|ERR",
	} {
		var got []string
		err := environWords([]byte(in), nil, nil, func(w []byte) error { got = append(got, string(w)); return nil })
		if err != nil {
			got = append(got, "ERR")
		}
		if strings.Join(got, "|") != want {
			t.Errorf("%s: got %q, want %q", in, strings.Join(got, "|"), want)
		}
	}
}

// An environment file is read as systemd reads one EnvironmentFile= names,
// each value at the line its key starts on. Planted breaks: the reference
// doc's list of differences from compose, one at a time.
func TestSystemdReadsAnEnvironmentFile(t *testing.T) {
	for _, c := range []struct{ name, body, variable, want string }{
		{"a value", "A=1\n", "A", "1@1"},
		{"spaces around", "  A  =  1 2  \n", "A", "1 2@1"},
		{"set twice", "A=1\nA=2\n", "A", "1@1 2@2"},
		{"no substitution", "B=x\nA=${B}\n", "A", "${B}@2"},
		{"; and # comments", "; A=1\n  # A=2\nA=3 # kept\n", "A", "3 # kept@3"},
		{"a name alone is ignored", "A\nA: 1\nA=2\n", "A", "2@3"},
		{"export is ignored", "export A=1\n", "A", ""},
		{"a name with . is ignored", "A.B=1\nA-B=2\n1A=3\n", "A", ""},
		{"an unquoted \\", "A=a\\ b\\\nc\n", "A", "a bc@1"},
		{"single quotes read no escape", "A='a\\'b'\n", "A", `a\b'@1`},
		{"double quotes", `A="\"\\\` + "`" + `\$\n"` + "\n", "A", "\"\\`$\\n@1"},
		{"a \\ joins lines in double quotes", "A=\"a\\\nb\"\nB=1\n", "A", "ab@1"},
		{"a quoted value spans lines", "A='a\nb'\nB=1\n", "A", "a\nb@1"},
		{"what follows a quote joins", "E=\"x\" y=2\n", "E", "xy=2@1"},
		{"spaces kept in quotes", "A=\" a \"  \n", "A", " a @1"},
		{"empty", "A=\nA=''\n", "A", "@1 @2"},
		{"at the end with no line end", "A=1", "A", "1@1"},
		{"open quote at the end", "A='x", "A", "x@1"},
		{"a NUL ends the file", "A=1\x00\nA=2\n", "A", "1@1"},
		{"a lone \\r ends a line", "A=1\rA=2\r", "A", "1@1 2@2"},
		{"a comment ending in \\ ends at its line", "# a \\\nA=1\n", "A", "1@2"},
		{"\\r\\n", "A=1\r\nB=2\r\nA=3", "A", "1@1 3@3"},
		{"a NUL first", "\x00A=1\n", "A", ""},
		{"an escaped space at the end is kept", "A=ab\\ \n", "A", "ab @1"},
		{"escapes alone", "A=\\a\\b\n", "A", "ab@1"},
		{"a space before an escape at the end", "A=a \\", "A", "a @1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := extracttest.Repo(t, map[string]string{"deploy/api.env": c.body})
			got := extracttest.MustRun(t, BuildSystemd, sd("deploy/api.env", "", "", c.variable), root)
			if g := extracttest.Texts(got); g != c.want {
				t.Errorf("got %q, want %q", g, c.want)
			}
		})
	}
	for _, c := range []struct{ name, body, want string }{
		{"a key not UTF-8", "A\xff=1\n", `deploy/api.env: line 1: the key "A\xff" is not UTF-8, which systemd refuses`},
		{"a value not UTF-8", "\nA=\xff\n", `deploy/api.env: line 2: the value of "A" is not UTF-8, which systemd refuses`},
		{"a value not UTF-8 at the end", "A=\xff", `deploy/api.env: line 1: the value of "A" is not UTF-8, which systemd refuses`},
	} {
		root := extracttest.Repo(t, map[string]string{"deploy/api.env": c.body})
		_, err := extracttest.Run(t, BuildSystemd, sd("deploy/api.env", "", "", "A"), root)
		extracttest.WantError(t, err, c.want)
	}
}

// A Quadlet Image= that names a .image or .build file beside the unit is
// what that file names, with its drop-ins; any other is kept. Planted
// breaks: the .image not followed; its drop-ins not applied; .build read
// as Image=.
func TestSystemdFollowsQuadletImages(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"q/web.container":           "[Container]\nImage=web.image\nImage=\nImage=web.image\n",
		"q/web.image":               "[Image]\nImage=docker.io/library/nginx:1.26\n",
		"q/web.image.d/10-pin.conf": "[Image]\nImage=\nImage=docker.io/library/nginx:1.27\n",
		"q/app.container":           "[Container]\nImage=app.build\n",
		"q/app.build":               "[Build]\nImageTag=localhost/app:1\n",
		"q/db.container":            "[Container]\nImage=docker.io/library/postgres:17\n",
		"q/lost.container":          "[Container]\nImage=lost.image\n",
		"q/x.service":               "[Service]\nImage=web.image\n",
		"q/tag.container":           "[Container]\nImage=tag.image\n",
		"q/tag.image":               "[Image]\nImage=docker.io/x:1\nImageTag=localhost/x:pinned\n",
		"q/two.container":           "[Container]\nImage=two.build\n",
		"q/two.build":               "[Build]\nImageTag=localhost/two:1\nImageTag=localhost/two:latest\n",
		"q/quoted.container":        "[Container]\nImage=docker.io/x:0\nImage=\"docker.io/x:1\"\n",
		"q/empty.container":         "[Container]\nImage=empty.image\n",
		"q/empty.image":             "[Image]\nImage=docker.io/e:1\nImageTag=\n",
		"q/none.container":          "[Container]\nImage=none.image\n",
		"q/none.image":              "[Image]\nPull=always\n",
		"q/nobuild.container":       "[Container]\nImage=nobuild.build\n",
		"q/nobuild.build":           "[Build]\nFile=Containerfile\n",
		"q/far.container":           "[Container]\nImage=../web.image\n",
	})
	for file, want := range map[string]string{
		"q/web.container":    "docker.io/library/nginx:1.27@q/web.image.d/10-pin.conf:3",
		"q/app.container":    "localhost/app:1@q/app.build:2",
		"q/db.container":     "docker.io/library/postgres:17@q/db.container:2",
		"q/x.service":        "web.image@q/x.service:2",
		"q/tag.container":    "localhost/x:pinned@q/tag.image:3",
		"q/two.container":    "localhost/two:1@q/two.build:2",
		"q/quoted.container": "docker.io/x:1@q/quoted.container:3",
		"q/empty.container":  "docker.io/e:1@q/empty.image:2",
	} {
		section := "Container"
		if strings.HasSuffix(file, ".service") {
			section = "Service"
		}
		if got := systemdValues(t, root, sd(file, section, "Image", "")); got != want {
			t.Errorf("%s: got %s, want %s", file, got, want)
		}
	}
	for file, want := range map[string]string{
		"q/lost.container":    `systemd: q/lost.container:2 sets Image= to the Quadlet file "lost.image" beside it: `,
		"q/none.container":    `"none.image" beside it: it sets no Image=, which Quadlet refuses`,
		"q/nobuild.container": `"nobuild.build" beside it: it sets no ImageTag=, which Quadlet refuses`,
		"q/far.container":     `systemd: q/far.container:2 sets Image= to "../web.image", and Quadlet finds a Quadlet file by its name alone`,
	} {
		_, err := extracttest.Run(t, BuildSystemd, sd(file, "Container", "Image", ""), root)
		extracttest.WantError(t, err, want)
	}
}

// What the kind refuses at load. Planted breaks: each check dropped.
func TestBuildSystemdRefuses(t *testing.T) {
	for _, c := range []struct {
		spec SystemdSpec
		want string
	}{
		{sd("a.service", "", "X", ""), "systemd: section is required"},
		{sd("a.service", "Service", "", ""), "systemd: key is required"},
		{sd("a.service", "Service", "ExecStart", "A"), `systemd: variable reads the assignments of Environment=, and key is "ExecStart"; set key: Environment`},
		{sd("a.service", "Service", "Environment", "A-B"), `systemd: variable "A-B" is no name systemd sets`},
		{sd("a.service", "Service", "Environment", "1A"), `systemd: variable "1A" is no name systemd sets`},
		{sd("a.env", "Service", "", "A"), `systemd: "a.env" is no unit file by its name`},
		{sd("a.env", "", "", ""), "so it is read as an environment file: variable is required"},
		{sd("/etc/a.service", "Service", "X", ""), "systemd: "},
	} {
		_, err := BuildSystemd(c.spec)
		extracttest.WantError(t, err, c.want)
	}
}

// env asks the systemd reader for a .service, .socket, .mount, .swap and
// Quadlet .container, which reads Environment= in the sections of the
// type, each value naming its section; an environment file is read when
// readers: gives it the reader. Planted break: the section left out of
// the origin.
func TestSystemdEnvRead(t *testing.T) {
	for file, want := range map[string]bool{"a.service": true, "a.socket": true, "x/a.mount": true, "a.swap": true, "a.container": true,
		"a.timer": false, "a.env": false, "a.service.d/x.conf": false, "a.service.j2": true} {
		if got := systemdChooses(file); got != want {
			t.Errorf("%s: %t, want %t", file, got, want)
		}
	}
	root := extracttest.Tracked(t, map[string]string{
		"a.container": "[Service]\nEnvironment=A=0\n[Container]\nEnvironment=A=1\n",
		"a.env":       "A=2\n",
	})
	for file, want := range map[string]string{"a.container": `1@a.container:4 [section "Container"]`, "a.env": "2@a.env:1"} {
		x, err := systemdEnvRead(file, "A", 0, core.Parsers{})
		if err != nil {
			t.Fatal(err)
		}
		vs, err := x.Extract(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		if got := texts(vs); got != want {
			t.Errorf("%s: got %s, want %s", file, got, want)
		}
	}
}

// The kind returns at most its value limit, core.MaxValues. Planted
// break: the limit not checked in a file.
func TestSystemdBoundsItsValues(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.service": "[Service]\nX=1\nX=2\nX=3\n",
	})
	for _, s := range []SystemdSpec{sd("a.service", "Service", "X", "")} {
		x, err := BuildSystemd(s)
		if err != nil {
			t.Fatal(err)
		}
		u := x.(unitExtractor)
		u.valueLimit = 2
		_, err = u.Extract(t.Context(), root)
		extracttest.WantError(t, err, core.TooManyValues(s.File).Error())
		u.valueLimit = 3
		if got, err := u.Extract(t.Context(), root); err != nil || len(got) != 3 {
			t.Errorf("%s at a limit of 3: %d values, %v", s.File, len(got), err)
		}
	}
}

// The entries readSystemd visits, with their kinds, as key=value@line, a
// reset as key!@line. Planted breaks: a line with nothing before = read
// as a key; a key's white space kept.
func TestSystemdEntries(t *testing.T) {
	var got []string
	err := readSystemd([]byte("X=0\n[S]\n=x\n K \t= v \nK=\n[T]\nA=1\n"), nil, func(e *core.Entry) error {
		mark := "="
		if e.Kind == core.EntryReset {
			mark = "!"
		}
		got = append(got, fmt.Sprintf("%s:%s%s%s@%d", e.Section, e.Key, mark, e.Value, e.Line))
		return nil
	})
	if want := "S:K=v@4 S:K!@5 T:A=1@7"; err != nil || strings.Join(got, " ") != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
}

// A unit is described as maybe not literal, and an environment file only
// when it is a template. Planted break: every file described as not
// literal.
func TestDescribeSystemd(t *testing.T) {
	for file, want := range map[string]bool{"a.service": true, "q/a.container": true, "a.env": false, "a.env.j2": true} {
		if got := DescribeSystemd(sd(file, "", "", "")).NotLiteral; got != want {
			t.Errorf("%s: %t, want %t", file, got, want)
		}
	}
}

// A unit or a drop-in systemd refuses makes the kind refuse, naming the
// file and the line. Planted break: a drop-in's refusal dropped.
func TestSystemdKindRefusesWhatSystemdRefuses(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.service":          "[Service]\nX=1\n",
		"a.service.d/x.conf": "[Service\n",
	})
	_, err := extracttest.Run(t, BuildSystemd, sd("a.service", "Service", "X", ""), root)
	extracttest.WantError(t, err, `a.service.d/x.conf: line 1: the section header "[Service" does not end in ]`)
}

// An environment file is refused for work at every budget below what its
// read costs. Planted break: a push charged nothing for the bytes before.
func TestEnvironmentFileWorkMeterRefusesAtEveryBudget(t *testing.T) {
	x, err := BuildSystemd(sd("a.env", "", "", "A"))
	if err != nil {
		t.Fatal(err)
	}
	coretest.RefusesAtEveryBudget(t, x.(core.EntryExtractor), "# c\nA=1\nB='x\ny'\nA=\"2\"")
}

// A template drop-in is found beside the unit and read as the drop-in it
// renders, masking a drop-in of that name in a directory after it. Planted
// break: dropInFiles listing *.conf alone.
func TestSystemdReadsATemplateDropIn(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"deploy/api.service":                 "[Service]\nExecStart=/bin/api\n",
		"deploy/api.service.d/10-x.conf.j2":  "[Service]\nExecStart=\nExecStart=/bin/api --port {{ port }}\n",
		"deploy/service.d/10-x.conf":         "[Service]\nExecStart=\nExecStart=/masked\n",
		"deploy/api.service.d/20-y.conf.bak": "[Service]\nExecStart=\nExecStart=/backup\n",
	})
	if got, want := systemdValues(t, root, sd("deploy/api.service", "Service", "ExecStart", "")), "/bin/api --port {{ port }}@deploy/api.service.d/10-x.conf.j2:3*"; got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

// The drop-in directories are listed for .conf files and the templates of
// them alone, so a leftover such as 20-y.conf.bak or 20-y.conf~ is never
// listed: a name it holds with a control character, which a glob that
// lists it refuses, leaves the unit readable. Planted break: dropInFiles
// listing *.conf*.
func TestSystemdListsOnlyDropInNames(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"deploy/api.service":                  "[Service]\nExecStart=/bin/api\n",
		"deploy/api.service.d/10-x.conf.tmpl": "[Service]\nExecStart=\nExecStart=/bin/api -t\n",
		"deploy/api.service.d/20-y.conf~\x01": "[Service]\nExecStart=\nExecStart=/leftover\n",
		"deploy/api.service.d/30-z.conf.bak":  "[Service]\nExecStart=\nExecStart=/backup\n",
	})
	if got, want := systemdValues(t, root, sd("deploy/api.service", "Service", "ExecStart", "")), "/bin/api -t@deploy/api.service.d/10-x.conf.tmpl:3"; got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
