package extract

import (
	"strings"
	"testing"
)

// Each case is what the dialect's own tool reads from the file, run through
// that tool: ansible-core 2.21.5, Python 3.13.5's configparser, tox 4.67.0,
// PHP 8.4.23's parse_ini_string and Temurin 21's Properties.load.
func TestDialectsReadWhatTheirToolReads(t *testing.T) {
	a := func(section, host, key string) Spec {
		return Spec{INI: &INI{File: "hosts", Section: section, Host: host, Key: key}}
	}
	for _, c := range []struct {
		name, file, body string
		spec             Spec
		want             string
	}{
		// ansible splits lines as str.splitlines does, and counts them at \n.
		{"ansible: \\f breaks a line", "hosts", "[web]\nh1 a=b\fh2 c=d\n", a("web", "", ""), "h1@2 h2@2"},
		{"ansible: \\v, \\x1c, NEL and U+2028 break a line", "hosts", "[web]\nh1\vh2\x1ch3\u0085h4 h5 h6\n", a("web", "", ""), "h1@2 h2@2 h3@2 h4@2 h5@2 h6@2"},
		{"ansible: a lone \\r breaks a line", "hosts", "[web]\rh1 a=1\rh2\r", a("web", "", "a"), "1@1"},
		{"ansible: a byte that is not UTF-8 is kept", "hosts", "[web]\nh1 a=\xff\n", a("web", "h1", "a"), "\xff@2"},
		// ansible drops the comment after a literal it evaluates.
		{"ansible: a number and a comment", "hosts", "[web:vars]\nx = 8080 # http\n", a("web:vars", "", "x"), "8080@2"},
		{"ansible: True and a comment", "hosts", "[web:vars]\nx = True#c\n", a("web:vars", "", "x"), "True@2"},
		{"ansible: a host value of a number and a comment", "hosts", "[web]\nh1 a=\"5 #x\"\n", a("web", "h1", "a"), "5@2"},
		{"ansible: no literal before the comment", "hosts", "[web:vars]\nx = pool.org # c\n", a("web:vars", "", "x"), "pool.org # c@2"},
		{"ansible: a NUL keeps a value as written", "hosts", "[web]\nh1 a=\"'x' #\x00\"\n", a("web", "h1", "a"), "'x' #\x00@2"},
		{"ansible: 200 nested brackets are a literal", "hosts", "[web:vars]\nx = " + strings.Repeat("[", 200) + strings.Repeat("]", 200) + "\n", a("web:vars", "", "x"), strings.Repeat("[", 200) + strings.Repeat("]", 200) + "@2"},
		{"ansible: 201 are no literal, and the comment stays", "hosts", "[web:vars]\nx = " + strings.Repeat("[", 201) + strings.Repeat("]", 201) + " # c\n", a("web:vars", "", "x"), strings.Repeat("[", 201) + strings.Repeat("]", 201) + " # c@2"},
		{"ansible: a host with a port is valid", "hosts", "[web]\n[x]:22 b=2\n", a("web", "", "b"), "2@2"},
		// configparser reads in text mode: a lone \r ends a line.
		{"plain: a lone \\r ends a line", "f.ini", "[s]\rk = v\r", ini("f.ini", "s", "k"), "v@1"},
		// tox strips a comment, joins a \ line and lowers keys.
		{"tox: an inline comment", "tox.ini", "[testenv]\ndeps = pytest # pin\n", ini("tox.ini", "testenv", "deps"), "pytest@2"},
		{"tox: an escaped #", "tox.ini", "[testenv]\ndeps = a\\#b # c\n", ini("tox.ini", "testenv", "deps"), "a#b@2"},
		{"tox: a \\ joins lines", "tox.ini", "[testenv]\ncommands = pytest \\\n  -q\n", ini("tox.ini", "testenv", "commands"), "pytest -q@2"},
		{"tox: keys in lower case", "tox.ini", "[testenv]\nDeps = x\n", ini("tox.ini", "testenv", "DEPS"), "x@2"},
		// PHP ends a value at ; outside quotes.
		{"php: an inline comment", "php.ini", "[PHP]\nmemory_limit = 128M ; note\n", ini("php.ini", "PHP", "memory_limit"), "128M@2"},
		{"php: a ; in quotes", "php.ini", "[PHP]\nerror_log = \"/var/log/a;b.log\" ; c\n", ini("php.ini", "PHP", "error_log"), "/var/log/a;b.log@2"},
		{"php: quotes and words joined", "php.ini", "[PHP]\nx = a \"b c\" d\n", ini("php.ini", "PHP", "x"), "ab cd@2"},
		{"php: a single quoted string", "php.ini", "[PHP]\nx = 'a ; b'\n", ini("php.ini", "PHP", "x"), "a ; b@2"},
		// Java's UTF-8 reader puts U+FFFD for each bad sequence.
		{"properties: a byte that is not UTF-8", "a.properties", "a=\xff\\\n b\n", props("a"), "�b@1"},
		{"properties: a cut sequence is one U+FFFD", "a.properties", "a=\xe2\x82x\xf4\x90\x80\x80\n", props("a"), "�x����@1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, c.file, c.body, c.spec); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: files each tool refuses. Each refusal names the line
// and the reason.
func TestDialectsRefuseWhatTheirToolRefuses(t *testing.T) {
	for _, c := range []struct {
		name, file, body string
		spec             Spec
		want             string
	}{
		{"ansible: a host ending in :", "hosts", "[web]\nweb2:\n", Spec{INI: &INI{File: "hosts", Section: "web"}},
			`hosts: line 2: the host "web2:" ends in :, which ansible keeps for a port`},
		{"ansible: a host ---", "hosts", "[web]\n---\n", Spec{INI: &INI{File: "hosts", Section: "web"}},
			`hosts: line 2: the host "---" is refused by ansible, as the start of a YAML file`},
		{"ansible: a range of unequal formats", "hosts", "[web]\nweb[01:3]\n", Spec{INI: &INI{File: "hosts", Section: "web"}},
			`hosts: line 2: the host "web[01:3]" has a range ansible refuses: host range must specify equal-length begin and end formats`},
		{"ansible: a range of step 0", "hosts", "[web]\nweb[1:3:0]\n", Spec{INI: &INI{File: "hosts", Section: "web"}},
			`hosts: line 2: the host "web[1:3:0]" has a range ansible refuses: its step is 0`},
		{"plain: a lone \\r inside a value's line", "f.ini", "[s]\nk = v\nj = a\rb\n", ini("f.ini", "s", "k"),
			`f.ini: line 3: "b" is no section header, comment or key with = or :`},
		{"plain: not UTF-8", "f.ini", "[s]\nk = \xff\n", ini("f.ini", "s", "k"),
			"f.ini: line 2: the file is not UTF-8, which configparser refuses"},
		{"tox: a key set twice", "tox.ini", "[testenv]\ndeps = a\nDeps = b\n", ini("tox.ini", "testenv", "deps"),
			`tox.ini: line 3: the key "deps" is set twice in section "testenv", which tox refuses`},
		{"tox: a section twice", "tox.ini", "[testenv]\n[testenv]\n", ini("tox.ini", "testenv", "deps"),
			`tox.ini: line 2: the section "testenv" is written twice, which tox refuses`},
		{"tox: a key before any section", "tox.ini", "deps = a\n", ini("tox.ini", "testenv", "deps"),
			`tox.ini: line 1: a key before the first section header, which tox refuses`},
		{"php: = in a value", "php.ini", "[PHP]\na = b = c\n", ini("php.ini", "PHP", "a"),
			"php.ini: line 2: PHP refuses this line: "},
		{"php: a boolean as a key", "php.ini", "on = 1\n", ini("php.ini", "", "on"),
			"php.ini: line 1: PHP refuses this line: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniError(t, c.file, c.body, c.spec); !strings.HasPrefix(got, c.want) {
				t.Errorf("error %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: files the dialect's tool reads and errata refuses, as
// the dialect documents, since errata would return neither the tool's value
// nor the text the tool reads. TestAnsibleRefusesLiteralsAndRanges holds
// the ansible ones.
func TestDialectsRefuseByDesign(t *testing.T) {
	for _, c := range []struct {
		name, body string
		spec       Spec
		want       string
	}{
		{"php: a NUL byte", "[PHP]\na = 1\x00\n", ini("php.ini", "PHP", "a"),
			"php.ini: line 2: a NUL byte, at which PHP stops reading the file"},
	} {
		file := "hosts"
		if c.spec.INI.File == "php.ini" {
			file = "php.ini"
		}
		t.Run(c.name, func(t *testing.T) {
			if got := iniError(t, file, c.body, c.spec); !strings.HasPrefix(got, c.want) {
				t.Errorf("error %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: values nested past the depth errata reads, each
// refused at its line as every format refuses one, where a reader that
// recursed into them would exhaust its stack. PHP refuses at 10000 levels,
// errata at maxDepth.
func TestDialectsRefuseDeepValues(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"php: ~ nested past the limit", "[PHP]\na = " + strings.Repeat("~", maxDepth+1) + "1\n"},
		{"php: ( nested past the limit", "[PHP]\na = " + strings.Repeat("(", maxDepth+1) + "1\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := iniError(t, "php.ini", c.body, ini("php.ini", "PHP", "a"))
			if want := "php.ini: line 2: nested more than 1000 levels deep, the limit"; !strings.HasPrefix(got, want) {
				t.Errorf("error %q, want %q", got, want)
			}
		})
	}
	// At the limit the value is read, as written and marked computed.
	body := "[PHP]\na = " + strings.Repeat("~", maxDepth) + "1\n"
	if got := iniValues(t, "php.ini", body, ini("php.ini", "PHP", "a")); got != strings.Repeat("~", maxDepth)+"1@2" {
		t.Errorf("at the limit: got %q", got)
	}
}
