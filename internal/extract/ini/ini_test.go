package ini

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, Build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, Build, s, root)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// iniValues extracts from body, written to file, with s, and returns the
// values as texts writes them.
func iniValues(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	return extracttest.Texts(mustRun(t, s, extracttest.Repo(t, map[string]string{file: body})))
}

// iniError extracts from body with s and returns the error's message.
func iniError(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	got, err := run(t, s, extracttest.Repo(t, map[string]string{file: body}))
	if err == nil {
		t.Fatalf("no error, values %s", extracttest.Texts(got))
	}
	return err.Error()
}

func ini(file, section, key string) Spec {
	return Spec{File: file, Section: section, Key: key}
}

func ptr(s string) *string { return &s }

const setupCfg = `[metadata]
name = errata-demo
version = 1.4.2
url = https://example.com/x;y

[options]
python_requires = >=3.9
install_requires =
    requests>=2.31
    # a comment inside the value

    click ; python_version >= "3.8"

[tool:pytest]
addopts: -q
`

func TestPlainReadsSetupCfg(t *testing.T) {
	for _, c := range []struct{ section, key, want string }{
		{"metadata", "version", "1.4.2@3"},
		{"metadata", "url", "https://example.com/x;y@4"},
		{"options", "python_requires", ">=3.9@7"},
		{"options", "install_requires", "\nrequests>=2.31\n\nclick ; python_version >= \"3.8\"@8"},
		{"tool:pytest", "addopts", "-q@15"},
		{"metadata", "Version", ""},
		{"Metadata", "version", ""},
	} {
		if got := iniValues(t, "setup.cfg", setupCfg, ini("setup.cfg", c.section, c.key)); got != c.want {
			t.Errorf("[%s] %s: got %q, want %q", c.section, c.key, got, c.want)
		}
	}
}

// configparser's rules for a line: a comment only at the start, the first
// = or : splits, an indented line continues, a header ends at the last ].
func TestPlainLines(t *testing.T) {
	for _, c := range []struct{ name, body, section, key, want string }{
		{"keys before any header", "a = 1\n[s]\na = 2\n", "", "a", "1@1"},
		{"a key repeated", "[s]\na = 1\nb = 0\na = 2\n[t]\na = 3\n[s]\na = 4\n", "s", "a", "1@2 2@4 4@8"},
		{"the first of = and :", "[s]\na: b = c\nd = e: f\n", "s", "a", "b = c@2"},
		{"the first of : and =", "[s]\na: b = c\nd = e: f\n", "s", "d", "e: f@3"},
		{"an empty value", "[s]\na =\n", "s", "a", "@2"},
		{"white space in a key", "[s]\nlong key name = 1\n", "s", "long key name", "1@2"},
		{"comment prefixes", "[s]\n# a = 1\n  ; a = 2\na = 3 # kept\n", "s", "a", "3 # kept@4"},
		{"a header ends at the last ]", "[a]b]\nk = 1\n", "a]b", "k", "1@2"},
		{"header with white space kept", "[ s ]\nk = 1\n", " s ", "k", "1@2"},
		{"text after the header's ]", "[s] ; c\nk = 1\n", "s", "k", "1@2"},
		{"an indented header continues", "[s]\nk = 1\n  [t]\n", "s", "k", "1\n[t]@2"},
		{"indent deeper than the key", "[s]\n  k = 1\n  j = 2\n    more\n", "s", "j", "2\nmore@3"},
		{"a blank line inside a value", "[s]\nk = a\n\n\n  b\n\n", "s", "k", "a\n\n\nb@2"},
		{"the value's end is stripped", "[s]\nk = a  \n  b \t\n\n", "s", "k", "a\nb@2"},
		{"CRLF", "[s]\r\nk = a\r\n  b\r\n", "s", "k", "a\nb@2"},
		{"a header ends a value", "[s]\nk = a\n[s]\n  b = 1\n", "s", "b", "1@4"},
		{"tab indent", "[s]\nk = a\n\tb\n", "s", "k", "a\nb@2"},
		{"DEFAULT is a section", "[DEFAULT]\nk = 1\n[s]\n", "s", "k", ""},
		{"no interpolation", "[s]\nk = %(x)s\n", "s", "k", "%(x)s@2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, "f.ini", c.body, ini("f.ini", c.section, c.key)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: a line that is no header, comment or key, and a key
// that is empty. Each refusal names the line.
func TestPlainRefusesWhatConfigparserRefuses(t *testing.T) {
	for body, want := range map[string]string{
		"[s]\nk = 1\njust words\n": `f.ini: line 3: "just words" is no section header, comment or key with = or :`,
		"[s]\n= 1\n":               `f.ini: line 2: "= 1" sets a value with no key`,
		"[s]\n  : 1\n":             `f.ini: line 2: ": 1" sets a value with no key`,
		"[]\nk = 1\n":              `f.ini: line 1: "[]" is no section header, comment or key with = or :`,
	} {
		if got := iniError(t, "f.ini", body, ini("f.ini", "s", "k")); got != want {
			t.Errorf("%q: error %q, want %q", body, got, want)
		}
	}
}

const inventory = `# the lab
jumpbox ansible_host=192.0.2.1

[web]
web1 ansible_host=10.0.0.1 http_port=8080
web2   ansible_host="10.0.0.2"  # the second
web[3:4].example.com
db1:2222 ansible_user='deploy' note="two words"

[web:vars]
ntp = time.example.com # kept, as ansible keeps it
quoted = "x"
mixed = 'a"b'
list = [1, 2]

[prod:children]
web
db   # the databases

[web:hosts]
web5 ansible_host=10.0.0.5
`

func TestAnsibleReadsAnInventory(t *testing.T) {
	a := func(section, host, key string) Spec {
		return Spec{File: "inventory/hosts", Section: section, Host: host, Key: key}
	}
	for _, c := range []struct {
		name string
		spec Spec
		want string
	}{
		{"every host of a group", a("web", "", ""), "web1@5 web2@6 web[3:4].example.com@7 db1:2222@8 web5@21"},
		{"one host", a("web", "web2", ""), "web2@6"},
		{"a variable of every host", a("web", "", "ansible_host"), "10.0.0.1@5 10.0.0.2@6 10.0.0.5@21"},
		{"a variable of one host", a("web", "web1", "http_port"), "8080@5"},
		{"shell quotes", a("web", "db1:2222", "note"), "two words@8"},
		{"a quoted string", a("web", "db1:2222", "ansible_user"), "deploy@8"},
		{"[group:hosts] is [group]", a("web:hosts", "web5", "ansible_host"), "10.0.0.5@21"},
		{"ungrouped", a("ungrouped", "", "ansible_host"), "192.0.2.1@2"},
		{"group variables", a("web:vars", "", "ntp"), "time.example.com # kept, as ansible keeps it@11"},
		{"a quoted group variable", a("web:vars", "", "quoted"), "x@12"},
		{"a quote of the other kind inside", a("web:vars", "", "mixed"), `a"b@13`},
		{"a list as written", a("web:vars", "", "list"), "[1, 2]@14"},
		{"children", a("prod:children", "", ""), "web@17 db@18"},
		{"a group's hosts are not its vars", a("web", "", "ntp"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, "inventory/hosts", inventory, c.spec); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// pyString strips the quotes of the Python string literals that
// ast.literal_eval reads as the text between them, and keeps every other
// value as written, those it reads otherwise too; Python 3.13 gave each
// value's literal_eval.
func TestPyString(t *testing.T) {
	for in, want := range map[string]string{
		`"x"`: "x", `'x'`: "x", `''`: "", `"a'b"`: "a'b", `'q' # c`: "q", "'q'\t#": "q", `"a#b"`: "a#b", `'q'  `: "q",
		`"a\"b"`: `"a\"b"`, `'a\nb'`: `'a\nb'`, `"x'`: `"x'`, `"`: `"`, `'a' 'b'`: `'a' 'b'`, `u'x'`: `u'x'`, `x`: "x", `'q' c`: `'q' c`,
	} {
		got, ok := pyString([]byte(in))
		if !ok {
			got = []byte(in)
		}
		if string(got) != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// Words split as Python's shlex.split(line, comments=True) splits them; the
// cases were run through Python 3.13's shlex.
func TestShellWords(t *testing.T) {
	for in, want := range map[string][]string{
		`a b`:          {"a", "b"},
		`a  "b c"d`:    {"a", "b cd"},
		`a 'b\c'`:      {"a", `b\c`},
		`a "b\c\"\\"`:  {"a", `b\c"\`},
		`a\ b c`:       {"a b", "c"},
		`a#b c`:        {"a"},
		`a "#" #c`:     {"a", "#"},
		`'' x`:         {"", "x"},
		"a\tb\rc":      {"a", "b", "c"},
		`x=" a " y=''`: {"x= a ", "y="},
	} {
		var w shellWords
		if err := w.split([]byte(in)); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		var got []string
		for i := range w.count() {
			got = append(got, string(w.word(i)))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]error{`a "b`: errOpenQuote, `a 'b`: errOpenQuote, `a "b\`: errEndEscape, `a \`: errEndEscape, `#`: errNoHostWord} {
		var w shellWords
		if err := w.split([]byte(in)); !errors.Is(err, want) {
			t.Errorf("%s: error %v, want %v", in, err, want)
		}
	}
}

// The planted breaks: each line ansible's plugin refuses, and a type it does
// not know.
func TestAnsibleRefusesWhatAnsibleRefuses(t *testing.T) {
	for body, want := range map[string]string{
		"[web:other]\n":              `hosts: line 1: section [web] has the type "other", which ansible does not know; write hosts, vars or children`,
		"[web group]\n":              `hosts: line 1: "[web group]" is no section header: a group's name holds no :, ] or white space, and a type after : is a word`,
		"[web:a-b]\n":                `hosts: line 1: "[web:a-b]" is no section header`,
		"[web]\nweb1 port\n":         `hosts: line 2: expected key=value after the host, got "port"`,
		"[web]\nweb1 a=\"b\n":        `hosts: line 2: the host line does not split as shell words: a quote is not closed`,
		"[web]\nweb1 a=b\\\n":        `hosts: line 2: the host line does not split as shell words: a \ ends the line`,
		"[web:vars]\nntp\n":          `hosts: line 2: expected key=value in a vars section, got "ntp"`,
		"[web:children]\na b\n":      `hosts: line 2: expected a group's name in a children section, got "a b"`,
		"[web:children]\na:b\n":      `hosts: line 2: expected a group's name in a children section, got "a:b"`,
		"[web]\n[web:vars] ; c\nx\n": `hosts: line 2: the host "[web:vars]" has a range ansible refuses: its begin, end or step is no integer`,
		"[web]\n[web:vars] # c\nx\n": `hosts: line 3: expected key=value in a vars section, got "x"`,
		"[web:children]\n[a] x=1\nb": `hosts: line 2: expected a group's name in a children section, got "[a] x=1"`,
	} {
		if got := iniError(t, "hosts", body, Spec{File: "hosts", Section: "web"}); !strings.HasPrefix(got, want) {
			t.Errorf("%q: error %q, want %q", body, got, want)
		}
	}
}

// The planted breaks: each name the dialect cannot hold, and each field
// another dialect reads. Build names the dialect and the field.
func TestBuildRefusesINIFields(t *testing.T) {
	for want, x := range map[string]Spec{
		"ini: file is required":       {},
		"ini: plain: key is required": {File: "f.ini", Section: "s"},
		"ini: dialect \"toml\" is not one errata reads; write one of git, ansible, tox, php, plain, or leave it out to choose by the file's name": {File: "f.ini", Dialect: "toml", Key: "k"},
		"ini: plain: subsection is read only by the git dialect; set dialect: git to read \"f.ini\" as a git config file":                         {File: "f.ini", Subsection: ptr("x"), Key: "k"},
		"ini: plain: host is read only by the ansible dialect; set dialect: ansible to read \"f.ini\" as an inventory":                            {File: "f.ini", Host: "h", Key: "k"},
		"ini: git: host is read only by the ansible dialect; set dialect: ansible to read \".gitconfig\" as an inventory":                         {File: ".gitconfig", Section: "s", Host: "h", Key: "k"},
		"ini: ansible: subsection is read only by the git dialect; set dialect: git to read \"hosts\" as a git config file":                       {File: "hosts", Section: "s", Subsection: ptr(""), Key: "k"},
		"ini: git: section is required": {File: ".gitconfig", Key: "k"},
		"ini: git: key is required":     {File: ".gitconfig", Section: "core"},
		"ini: git: \"core.1bare\" is no name git config reads: a section and a key are letters, digits and -, and a key starts with a letter":  {File: ".gitconfig", Section: "core", Key: "1bare"},
		"ini: git: \"co_re.bare\" is no name git config reads: a section and a key are letters, digits and -, and a key starts with a letter":  {File: ".gitconfig", Section: "co_re", Key: "bare"},
		"ini: git: \"a.b\\nc.k\" is no name git config reads: a subsection holds no newline and no NUL":                                        {File: ".gitconfig", Section: "a", Subsection: ptr("b\nc"), Key: "k"},
		"ini: ansible: section is required: a group's name, group:vars or group:children, and ungrouped for the hosts before the first header": {File: "hosts"},
		"ini: ansible: section \"web:other\" is no group's name, group:vars or group:children; a group's name holds no :, ] or white space":    {File: "hosts", Section: "web:other"},
		"ini: ansible: section \"a b\" is no group's name, group:vars or group:children; a group's name holds no :, ] or white space":          {File: "hosts", Section: "a b"},
		"ini: ansible: section \"web:\" is no group's name, group:vars or group:children; a group's name holds no :, ] or white space":         {File: "hosts", Section: "web:"},
		"ini: ansible: key is required in a vars section":                                                         {File: "hosts", Section: "web:vars"},
		"ini: ansible: host selects a host line, and section \"web:vars\" holds none":                             {File: "hosts", Section: "web:vars", Host: "h", Key: "k"},
		"ini: ansible: host selects a host line, and section \"web:children\" holds none":                         {File: "hosts", Section: "web:children", Host: "h"},
		"ini: ansible: section \"web:children\" lists groups and sets no key; leave key out to select the groups": {File: "hosts", Section: "web:children", Key: "k"},
	} {
		_, err := Build(x)
		if err == nil || err.Error() != want {
			t.Errorf("%+v: error %v, want %q", x, err, want)
		}
	}
}
