package ini

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// notLiterals extracts from body with s and writes each value as text@line,
// with a ! after the value when it is marked not literal.
func notLiterals(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	var out []string
	for _, v := range mustRun(t, s, extracttest.Repo(t, map[string]string{file: body})) {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, v.Text+mark+"@"+strconv.Itoa(v.At.Line))
	}
	return strings.Join(out, " ")
}

// Each dialect marks the values its tool computes, and only those: each
// marked form is next to one the tool reads as written. The planted breaks
// leave each form unmarked, and mark what is next to it.
func TestINIMarksWhatTheToolComputes(t *testing.T) {
	for _, c := range []struct {
		name, file, body string
		spec             Spec
		want             string
	}{
		// php computes its boolean words, constants, ${} references and
		// expressions; a number, a size such as 128M (which a directive
		// reads, not the parser) and a string are what PHP's parser returns.
		{"php boolean word", "php.ini", "a = On\na = 1\n", ini("php.ini", "", "a"), "On!@1 1@2"},
		{"php constants and operators", "php.ini", "a = E_ALL & ~E_DEPRECATED\na = 128M\n", ini("php.ini", "", "a"), "E_ALL & ~E_DEPRECATED!@1 128M@2"},
		{"php references", "php.ini", "a = ${HOME:-x}\na = \"x\"\n", ini("php.ini", "", "a"), "${HOME:-x}!@1 x@2"},
		{"php constant names", "php.ini", "a = PHP_EOL\na = UTC\na = Neutral\na = INF\na = Foo Bar\na = E_ALL\n", ini("php.ini", "", "a"), "PHP_EOL!@1 UTC@2 Neutral@3 INF@4 Foo Bar@5 E_ALL!@6"},
		{"php.ini as PHP ships it", "php.ini-production", "a = E_ALL & ~E_DEPRECATED\na = UTC\n", ini("php.ini-production", "", "a"), "E_ALL & ~E_DEPRECATED!@1 UTC@2"},
		{"php.ini.dist", "php.ini.dist", "a = On\n", ini("php.ini.dist", "", "a"), "On!@1"},
		// tox computes a value with a factor condition or a substitution;
		// its comments and joined lines errata reads as tox does.
		{"tox factor condition", "tox.ini", "[testenv]\ndeps =\n    pytest\n    py311: mock\n", ini("tox.ini", "testenv", "deps"), "\npytest\npy311: mock!@2"},
		{"tox no factor", "tox.ini", "[testenv]\ncommands = python -c \"print('a: b')\"\n", ini("tox.ini", "testenv", "commands"), "python -c \"print('a: b')\"@2"},
		{"tox escape", "tox.ini", "[testenv]\na = x\\{y\nb = https://x\n", ini("tox.ini", "testenv", "a"), "x\\{y!@2"},
		{"tox a url", "tox.ini", "[testenv]\na = x\\{y\nb = https://x\n", ini("tox.ini", "testenv", "b"), "https://x@3"},
		{"tox posargs shorthand", "tox.ini", "[testenv]\ncommands = pytest []\n", ini("tox.ini", "testenv", "commands"), "pytest []!@2"},
		// setup.cfg: configparser's interpolation and setuptools' attr: and
		// file: directives. Another plain file has no interpolation.
		{"setup.cfg interpolation", "setup.cfg", "[a]\nurl = %(base)s/api\nname = pkg\n", ini("setup.cfg", "a", "url"), "%(base)s/api!@2"},
		{"setup.cfg plain value", "setup.cfg", "[a]\nurl = %(base)s/api\nname = pkg\n", ini("setup.cfg", "a", "name"), "pkg@3"},
		{"setup.cfg %%", "setup.cfg", "[a]\nx = 100%%\n", ini("setup.cfg", "a", "x"), "100%%!@2"},
		{"setup.cfg directives", "setup.cfg", "[metadata]\nversion = attr: pkg.__version__\nlong_description = file: README.md\n", ini("setup.cfg", "metadata", "version"), "attr: pkg.__version__!@2"},
		{"setup.cfg file directive", "setup.cfg", "[metadata]\nlong_description = file: README.md\ndescription = a file: b\n", ini("setup.cfg", "metadata", "long_description"), "file: README.md!@2"},
		{"setup.cfg text with a colon", "setup.cfg", "[metadata]\ndescription = a file: b\n", ini("setup.cfg", "metadata", "description"), "a file: b@2"},
		{"another plain file", "flake8.ini", "[a]\nformat = %(path)s:%(row)d\n", ini("flake8.ini", "a", "format"), "%(path)s:%(row)d@2"},
		{"pytest.ini", "pytest.ini", "[pytest]\naddopts = -p no:%(x)s $HOME {posargs}\n", ini("pytest.ini", "pytest", "addopts"), "-p no:%(x)s $HOME {posargs}@2"},
		{"mypy.ini", "mypy.ini", "[mypy]\nmypy_path = $MYPY_CONFIG_FILE_DIR/stubs\nmypy_path = stubs\n", ini("mypy.ini", "mypy", "mypy_path"), "$MYPY_CONFIG_FILE_DIR/stubs!@2 stubs@3"},
		{"setup.cfg mypy", "setup.cfg", "[mypy]\nmypy_path = $MYPY_CONFIG_FILE_DIR/stubs\n[mypy-pkg.*]\nx = $y\n", ini("setup.cfg", "mypy", "mypy_path"), "$MYPY_CONFIG_FILE_DIR/stubs!@2"},
		{"setup.cfg mypy module", "setup.cfg", "[mypy-pkg.*]\nx = $y\n", ini("setup.cfg", "mypy-pkg.*", "x"), "$y@2"},
		{"mypy cache_dir", "mypy.ini", "[mypy]\ncache_dir = ~/.cache/mypy\ncache_dir = .cache\n", ini("mypy.ini", "mypy", "cache_dir"), "~/.cache/mypy!@2 .cache@3"},
		{"mypy files", "mypy.ini", "[mypy]\nfiles = a, ~/b\nfiles = a,b:c\nfiles = ${SRC}\n", ini("mypy.ini", "mypy", "files"), "a, ~/b!@2 a,b:c@3 ${SRC}!@4"},
		{"mypy other options", "mypy.ini", "[mypy]\nplugins = $HOME/p.py\nexclude = ^build/.*$\n", ini("mypy.ini", "mypy", "plugins"), "$HOME/p.py@2"},
		{"mypy exclude", "mypy.ini", "[mypy]\nexclude = ^build/.*$\n", ini("mypy.ini", "mypy", "exclude"), "^build/.*$@2"},
		{"mypy key spelling", "setup.cfg", "[mypy]\nCache-Dir = ~/x\n", ini("setup.cfg", "mypy", "Cache-Dir"), "~/x!@2"},
		{"coverage $ alone", ".coveragerc", "[report]\nexclude_lines = raise NotImplementedError$\nx = $ 5\nx = $$\nx = ${A-b}\n", ini(".coveragerc", "report", "x"), "$ 5@3 $$!@4 ${A-b}!@5"},
		{"coverage exclude_lines", ".coveragerc", "[report]\nexclude_lines = raise NotImplementedError$\n", ini(".coveragerc", "report", "exclude_lines"), "raise NotImplementedError$@2"},
		{"setup.cfg tox comment", "setup.cfg", "[testenv]\ndeps = pytest # pin\n", ini("setup.cfg", "testenv", "deps"), "pytest # pin!@2"},
		{"setup.cfg tox join", "setup.cfg", "[testenv]\ncommands = pytest \\\n  -q\n", ini("setup.cfg", "testenv", "commands"), "pytest \\\n-q!@2"},
		{"tox.ini comment and join", "tox.ini", "[testenv]\ndeps = pytest # pin\ncommands = pytest \\\n  -q\n", ini("tox.ini", "testenv", "commands"), "pytest -q@3"},
		{"php.ini template", "php.ini.j2", "[PHP]\nmemory_limit = {{ php_memory_limit }}\nerror_reporting = E_ALL\n", ini("php.ini.j2", "PHP", "memory_limit"), "{{ php_memory_limit }}!@2"},
		// #120: a php.ini template is read as php.ini, so PHP's marks hold
		// beside the engine's.
		{"a php.ini template's constant", "php.ini.j2", "[PHP]\nerror_reporting = E_ALL\nx = UTC\n", ini("php.ini.j2", "PHP", "error_reporting"), "E_ALL!@2"},
		{"a php.ini template's text", "php.ini.j2", "[PHP]\nx = UTC\n", ini("php.ini.j2", "PHP", "x"), "UTC@2"},
		{"a setup.cfg template", "setup.cfg.j2", "[a]\nurl = %(base)s\n", ini("setup.cfg.j2", "a", "url"), "%(base)s!@2"},
		{"an ini template", "app.ini.tmpl", "[a]\nb = {% if x %}1{% endif %}\nc = d\n", ini("app.ini.tmpl", "a", "b"), "{% if x %}1{% endif %}!@2"},
		{"an erb template", "app.ini.erb", "[a]\nb = <%= @b %>\n", ini("app.ini.erb", "a", "b"), "<%= @b %>!@2"},
		{"<% outside erb", "app.ini.j2", "[a]\nb = <%= @b %>\n", ini("app.ini.j2", "a", "b"), "<%= @b %>@2"},
		{"a jinja comment", "app.ini.j2", "[a]\na = 128M {# note #}\nb = 128M\n", ini("app.ini.j2", "a", "a"), "128M {# note #}!@2"},
		{"a terraform template", "php.ini.tpl", "[PHP]\nb = ${memory_limit}\nc = %{ if x }y%{ endif }\nd = $5\n", ini("php.ini.tpl", "PHP", "b"), "${memory_limit}!@2"},
		{"a terraform directive", "php.ini.tftpl", "[PHP]\nc = %{ if x }y%{ endif }\nd = $5\n", ini("php.ini.tftpl", "PHP", "c"), "%{ if x }y%{ endif }!@2"},
		{"a terraform template's text", "php.ini.tftpl", "[PHP]\nd = $5 50%\n", ini("php.ini.tftpl", "PHP", "d"), "$5 50%@2"},
		{"${ outside terraform", "app.ini.j2", "[a]\nb = ${x}\n", ini("app.ini.j2", "a", "b"), "${x}@2"},
		{"mypy files globs", "mypy.ini", "[mypy]\nfiles = src/*.py, tests\nfiles = a[0].py\nfiles = a?.py\nfiles = src, tests\nfiles = a:~b\n", ini("mypy.ini", "mypy", "files"), "src/*.py, tests!@2 a[0].py!@3 a?.py!@4 src, tests@5 a:~b@6"},
		{"mypy_path splits at :", "mypy.ini", "[mypy]\nmypy_path = a:~b\nmypy_path = a*b\n", ini("mypy.ini", "mypy", "mypy_path"), "a:~b!@2 a*b@3"},
		{"coverage unicode number", ".coveragerc", "[run]\nx = $\u00b2x\nx = $\u00e9\nx = $-\n", ini(".coveragerc", "run", "x"), "$\u00b2x!@2 $\u00e9!@3 $-@4"},
		{"php lower case after _", "php.ini", "a = PHP_FLOAT_EPSILON_x\na = Ab_c\n", ini("php.ini", "", "a"), "PHP_FLOAT_EPSILON_x!@1 Ab_c@2"},
		// #118: PHP reads php.ini before any extension defines its
		// constants, and keeps such a word as written (php -c, PHP 8.4).
		{"php extension constants", "php.ini", "a = CODESET\na = M_PI\na = SIGTERM\na = SOMAXCONN\na = CURL_SSLVERSION_TLSv1_2\na = STREAM_CRYPTO_METHOD_TLSv1_2_CLIENT\n", ini("php.ini", "", "a"), "CODESET@1 M_PI@2 SIGTERM@3 SOMAXCONN@4 CURL_SSLVERSION_TLSv1_2@5 STREAM_CRYPTO_METHOD_TLSv1_2_CLIENT@6"},
		// PHP reads a .user.ini for each request, and any file but php.ini
		// and its scan directory's later than startup, when an extension's
		// constant is defined.
		{"php .user.ini", "php/.user.ini", "[PHP]\na = M_PI\na = UTC\n", ini("php/.user.ini", "PHP", "a"), "M_PI!@2 UTC@3"},
		{"php conf.d", "etc/php/8.3/fpm/conf.d/x.ini", "a = M_PI\n", ini("etc/php/8.3/fpm/conf.d/x.ini", "", "a"), "M_PI@1"},
		// PHP reads a conf.d file at startup wherever the repository keeps
		// it; only the name, not a php directory, says so.
		{"php .ini outside a php directory", "docker/opcache.ini", "a = M_PI\n", Spec{File: "docker/opcache.ini", Dialect: "php", Key: "a"}, "M_PI@1"},
		{"php named pool file", "www.conf", "a = M_PI\n", Spec{File: "www.conf", Dialect: "php", Key: "a"}, "M_PI!@1"},
		{"php.ini template", "php.ini.j2", "a = M_PI\n", ini("php.ini.j2", "", "a"), "M_PI@1"},
		{"php startup constants", "php.ini", "a = E_STRICT\na = PHP_INT_MAX\na = DEFAULT_INCLUDE_PATH\na = PEAR_INSTALL_DIR\na = ZEND_THREAD_SAFE\na = UPLOAD_ERR_OK\na = DEBUG_BACKTRACE_IGNORE_ARGS\na = M_PI | 1\n", ini("php.ini", "", "a"), "E_STRICT!@1 PHP_INT_MAX!@2 DEFAULT_INCLUDE_PATH!@3 PEAR_INSTALL_DIR!@4 ZEND_THREAD_SAFE!@5 UPLOAD_ERR_OK!@6 DEBUG_BACKTRACE_IGNORE_ARGS!@7 M_PI | 1!@8"},
		{"setup.cfg $ elsewhere", "setup.cfg", "[metadata]\nmypy_path = $x\n", ini("setup.cfg", "metadata", "mypy_path"), "$x@2"},
		{".coveragerc", ".coveragerc", "[run]\ndata_file = ${COVERAGE_FILE-.coverage}\nbranch = True\n", ini(".coveragerc", "run", "data_file"), "${COVERAGE_FILE-.coverage}!@2"},
		// #118: coverage's pattern needs ${word}, ${word?} or ${word-x}.
		{"coverage braces", ".coveragerc", "[run]\nb = ${ x }\nb = ${x}\nb = ${x\nb = ${x?}\nb = ${x?y}\nb = ${x-a b}\nb = ${}\nb = ${x-\nb = ${\u00e9}\n", ini(".coveragerc", "run", "b"),
			"${ x }@2 ${x}!@3 ${x@4 ${x?}!@5 ${x?y}@6 ${x-a b}!@7 ${}@8 ${x-@9 ${\u00e9}!@10"},
		{".coveragerc literal", ".coveragerc", "[run]\nbranch = True\n", ini(".coveragerc", "run", "branch"), "True@2"},
		{"setup.cfg coverage", "setup.cfg", "[coverage:run]\nomit = $HOME/x\n", ini("setup.cfg", "coverage:run", "omit"), "$HOME/x!@2"},
		{"tox.ini coverage", "tox.ini", "[coverage:run]\nomit = $HOME/x\n[testenv]\nomit = $HOME/x\n", ini("tox.ini", "coverage:run", "omit"), "$HOME/x!@2"},
		{"tox.ini $ elsewhere", "tox.ini", "[testenv]\nomit = $HOME/x\n", ini("tox.ini", "testenv", "omit"), "$HOME/x@2"},
		{"setup.cfg tox envlist", "setup.cfg", "[tox:tox]\nenvlist = py{38,39}\n", ini("setup.cfg", "tox:tox", "envlist"), "py{38,39}!@2"},
		{"setup.cfg testenv deps", "setup.cfg", "[testenv]\ndeps = {posargs}\ncommands = pytest\n", ini("setup.cfg", "testenv", "deps"), "{posargs}!@2"},
		{"setup.cfg testenv commands", "setup.cfg", "[testenv]\ncommands = pytest\n", ini("setup.cfg", "testenv", "commands"), "pytest@2"},
		{"setup.cfg testenv factor", "setup.cfg", "[testenv:lint]\ndeps =\n    py311: mock\n", ini("setup.cfg", "testenv:lint", "deps"), "\npy311: mock!@2"},
		{"setup.cfg braces elsewhere", "setup.cfg", "[metadata]\nname = py{38,39}\n", ini("setup.cfg", "metadata", "name"), "py{38,39}@2"},
		// ansible: a literal Python writes otherwise, a container, and a
		// Jinja2 template; a decimal number, True and a string are literal.
		{"ansible numbers", "hosts", "[g:vars]\na=0x10\na=22\na=1_000\na=-5\na=+5\na=-0\n", Spec{File: "hosts", Section: "g:vars", Key: "a"}, "0x10!@2 22@3 1_000!@4 -5@5 +5!@6 -0!@7"},
		{"ansible floats", "hosts", "[g:vars]\na=1.5\na=1.50\na=1e3\na=1.0\na=1e-05\na=.5\n", Spec{File: "hosts", Section: "g:vars", Key: "a"}, "1.5@2 1.50!@3 1e3!@4 1.0@5 1e-05@6 .5!@7"},
		{"ansible words and containers", "hosts", "[g:vars]\na=True\na=[80,443]\na=None\na=...\na=set()\na=server\na=[80, 443]\n", Spec{File: "hosts", Section: "g:vars", Key: "a"}, "True@2 [80,443]!@3 None@4 ...@5 set()!@6 server@7 [80, 443]@8"},
		{"ansible complex numbers", "hosts", "[g:vars]\na=1+2j\na=1j\na=-1j\na=2.0j\na=1.5j\na=1 - 2j\na=1j+2j\na=1+2\n", Spec{File: "hosts", Section: "g:vars", Key: "a"}, "1+2j!@2 1j@3 -1j!@4 2.0j!@5 1.5j@6 1 - 2j!@7 1j+2j@8 1+2@9"},
		// Literals joined by commas are a tuple, which ansible holds as a list.
		{"ansible tuples", "hosts", "[g:vars]\na=1, 2\na=1,\na='a', 'b' # c\na=1 , 2 ,\na=1,,2\na=1, x\n", Spec{File: "hosts", Section: "g:vars", Key: "a"}, "1, 2!@2 1,!@3 'a', 'b'!@4 1 , 2 ,!@5 1,,2@6 1, x@7"},
		{"ansible strings", "hosts", "[g:vars]\na='0x10'\na=0x10 # c\na=007\n", Spec{File: "hosts", Section: "g:vars", Key: "a"}, "0x10@2 0x10!@3 007@4"},
		{"ansible hosts", "hosts", "[web]\nweb[01:03].example.com\ndb1:2222\n[10.0.0.1]:22\nplain a=1\n", Spec{File: "hosts", Section: "web"}, "web[01:03].example.com!@2 db1:2222!@3 [10.0.0.1]:22!@4 plain@5"},
		// #118: parse_address takes a port only off an address or a host
		// name; ansible keeps fe80::1 and we$b:22 as written.
		{"ansible unicode port", "hosts", "[web]\ndb1:\u0662\u0662\ndb2:\u00b2\n", Spec{File: "hosts", Section: "web"}, "db1:\u0662\u0662!@2 db2:\u00b2@3"},
		{"ansible range first", "hosts", "[web]\n[1:3]x\nplain\n", Spec{File: "hosts", Section: "web"}, "[1:3]x!@2 plain@3"},
		{"ansible ports kept", "hosts", "[web]\nfe80::1\nwe$b:22\nh_:22\ndb.example.com:22\n[::1]:22\n\u00e9t\u00e9:22\n", Spec{File: "hosts", Section: "web"}, "fe80::1@2 we$b:22@3 h_:22@4 db.example.com:22!@5 [::1]:22!@6 \u00e9t\u00e9:22!@7"},
		{"ansible children", "hosts", "[g:children]\nweb\ndb\n", Spec{File: "hosts", Section: "g:children"}, "web@2 db@3"},
	} {
		if got := notLiterals(t, c.file, c.body, c.spec); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	host := func(key string) Spec {
		return Spec{File: "hosts", Section: "ungrouped", Host: "h", Key: key}
	}
	jinja := "h a=\"{{ lookup('env', 'IP') }}\" b=\"{x}\" c='{% if x %}y{% endif %}' d=\"{# c #}\"\n"
	for key, want := range map[string]string{
		"a": "{{ lookup('env', 'IP') }}!@1", "b": "{x}@1", "c": "{% if x %}y{% endif %}!@1", "d": "{# c #}!@1",
	} {
		if got := notLiterals(t, "hosts", jinja, host(key)); got != want {
			t.Errorf("ansible jinja %s: got %q, want %q", key, got, want)
		}
	}
	for section, want := range map[string]string{"testenv": "pytest {posargs}!@2", "x": "pytest -q@4"} {
		body := "[testenv]\ncommands = pytest {posargs}\n[x]\ncommands = pytest -q # c\n"
		if got := notLiterals(t, "tox.ini", body, ini("tox.ini", section, "commands")); got != want {
			t.Errorf("tox %s: got %q, want %q", section, got, want)
		}
	}
}

// #119: in a template, a value inside one of the engine's blocks may not be
// in the rendered file, so it is not literal, beside one outside the block
// that stays literal; one row for each engine. The planted break is
// TemplateRead left out of Build.
func TestINIMarksAValueInsideATemplateBlock(t *testing.T) {
	for _, c := range []struct {
		name, file, body string
		key, want        string
	}{
		// A {, a %{ or a {% line is one no INI reader takes, so the read is
		// refused there, but a tag in a comment line still renders.
		{"jinja", "app.ini.j2", "[a]\n# {% if x %}\nb = 1\n# {% endif %}\nb = 2\n", "b", "1!@3 2@5"},
		{"erb", "php.ini.erb", "<% if @x %>\na = 1\n<% end %>\na = 2\n", "a", "1!@2 2@4"},
		{"go", "php.ini.tmpl", "; {{ if .x }}\na = 1\n; {{ end }}\na = 2\n", "a", "1!@2 2@4"},
		{"terraform", "php.ini.tftpl", "; %{ if x }\na = 1\n; %{ endif }\na = 2\n", "a", "1!@2 2@4"},
		{"no template", "app.ini", "[a]\n# {% if x %}\nb = 1\n", "b", "1@3"},
	} {
		section := ""
		if c.key == "b" {
			section = "a"
		}
		if got := notLiterals(t, c.file, c.body, ini(c.file, section, c.key)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// An extractor may list in skip_not_literal only where its dialect's tool
// computes values: php, tox, ansible, and plain for setup.cfg.
func TestINIDescribedNotLiteral(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want bool
	}{
		{ini("php.ini", "", "a"), true},
		{ini("tox.ini", "testenv", "a"), true},
		{ini("setup.cfg", "metadata", "a"), true},
		{ini("pkg/setup.cfg", "metadata", "a"), true},
		{ini("flake8.ini", "a", "b"), false},
		{ini(".flake8", "flake8", "b"), false},
		{ini("pytest.ini", "pytest", "b"), false},
		{ini("mypy.ini", "mypy", "b"), true},
		{ini(".mypy.ini", "mypy", "b"), true},
		{ini(".coveragerc", "run", "b"), true},
		{ini("php.ini-development", "", "a"), true},
		{ini("app.ini.j2", "a", "b"), true},
		{Spec{File: "hosts", Section: "g"}, true},
		{gitSpec(".gitconfig", "a", nil, "k"), false},
	} {
		if _, err := Build(c.spec); err != nil {
			t.Fatal(err)
		}
		if got := Describe(c.spec).NotLiteral; got != c.want {
			t.Errorf("%+v: %v, want %v", c.spec, got, c.want)
		}
	}
}

// toxFactorLine and toxSubstitutes against tox 4.67.0: whether
// expand_factors finds factors in the value, and whether
// find_replace_expr finds a substitution or an escape in it.
func TestToxComputesAsToxDoes(t *testing.T) {
	for _, c := range []struct {
		value         string
		factors, subs bool
	}{
		{"pytest", false, false}, {"pytest {posargs}", false, true}, {"py311: pytest", true, false},
		{"pytest\npy311: mock", true, false}, {"python -c \"print('a: b')\"", false, false},
		{"https://x", false, false}, {"a\\{b", false, true}, {"{", false, false},
		{"{py39,py38}-x: y", true, true}, {"run: the tests", true, false}, {"x:y", false, false},
		{"a :", true, false}, {"deps []", false, true}, {"a\\\\b", false, false},
		{"py 311: x", false, false}, {"!pypy: x", true, false}, {"py-!x: y", true, false},
		{"{}: x", false, true}, {"{a: b", false, false}, {"a}{b", false, false}, {"\\:", false, true},
		{"x: ", true, false}, {": foo", true, false}, {"py.*?: x", true, false}, {"a,b: c", true, false},
		{"a, b: c", true, false}, {"{a,b}-{c,d}: e", true, true}, {"é: x", true, false},
		{"a\tb: c", false, false}, {"py311:\tx", true, false}, {"x\\}", false, true}, {"{a\\}", false, true},
	} {
		factors := false
		for _, line := range strings.Split(c.value, "\n") {
			factors = factors || toxFactorLine([]byte(line))
		}
		if factors != c.factors || toxSubstitutes([]byte(c.value)) != c.subs {
			t.Errorf("%q: factors %v, substitutes %v; tox %v, %v", c.value, factors, toxSubstitutes([]byte(c.value)), c.factors, c.subs)
		}
	}
}

// pyComputed against Python 3.13 and ansible-core 2.21.5: a literal is
// computed when the str of what ansible holds, ast.literal_eval(text) with
// ... and a complex number turned back into str, is not the text, or when
// it is a container.
func TestPyComputedAsPythonWritesIt(t *testing.T) {
	for text, want := range map[string]bool{
		"22": false, "0x10": true, "1_000": true, "True": false, "[80, 443]": false, "1.5": false, "0j": false, "1.5j": false, "1e16j": true, "1J": true, "1e3j": true, "0.0j": true, "1_0j": true,
		"1.50": true, "1e3": true, "-0": true, "+5": true, "1.0": false, "...": false, "-0.0": false,
		"0.0001": false, "0.00001": true, "1e16": true, "1e15": true, "1e999": true, "00": true,
		"-5": false, "- 5": true, "1j": false, "-1j": true, "2.0j": true, ".5": true, "5.": true, "3.14159": false, "1e-7": true,
		"123456789012345678.0": true, "set()": true, "(1,)": true, "{}": false, "None": false,
		"0o17": true, "0b1": true, "-1.5": false, "2.5e-5": true, "100000000000000000000": false,
		"1e-05": false, "1e+16": false, "1000000000000000.0": false, "1.2345678901234568e+17": false,
		// #118: a list or a dict is literal when repr writes it as the text
		// is; ansible holds a tuple as a list.
		"[]": false, "()": true, "[1,2]": true, "[1, 2,]": true, "[1,  2]": true, "(1)": true, "(1, 2)": true,
		"(1, 2,)": true, "(1,2)": true, "[[1, [2]], (3,)]": true, "[[1, [2]], [3]]": false, "['a', 'b']": false, `["a"]`: true,
		`["it's"]`: false, `['it"s']`: false, `['a\'b']`: true, `['\n']`: true, "['\u00e9']": true,
		"{'a': 1}": false, "{'a':1}": true, "{'a': 1, 'a': 2}": true, "{1: 2}": true, "{'a': 1,}": true,
		"{1, 2}": true, "[True, False, None]": false, "[...]": true, "[0x10]": true, "[-1, -1.5, 1j]": false,
		"[-0]": true, "[- 1]": true, "[1.50]": true, "[b'a']": true, "[Truex]": true, "[1] ": true,
		"{'a': {'b': [1]}}": false, "{'a': 1, 'b': 2}": false, "[1, 2": true, "[-]": true, "[None, ]": true,
		"['']": false, "{'': 1}": false, "['a b']": false, "['~']": false, `["'a"]`: false, "['\x7f']": true, "['\x1f']": true,
	} {
		if got := pyComputed([]byte(text)); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}
