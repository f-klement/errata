package extract

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The properties, plain, tox, php and ansible readers are compared with the
// readers they port (java.util.Properties.load, Python's configparser, tox,
// PHP's parse_ini_string and ansible-core's ini inventory plugin) through
// corpora of generated files and what those readers made of each. None of
// them is in the CI image, so the corpora are committed, and
// TestRegenerateOracleCorpora writes them again where they are installed:
//
//	ERRATA_JAVA=/path/to/jdk/bin/java ERRATA_PYTHON=python3 ERRATA_TOOL_PYTHON=/path/to/venv/bin/python \
//	ERRATA_PHP=/path/to/php go test ./internal/extract -run TestRegenerateOracleCorpora
//
// ERRATA_TOOL_PYTHON is a Python with ansible-core and tox installed. The
// corpora of this commit came from Temurin 21.0.12.1, Python 3.13.5,
// ansible-core 2.21.5, tox 4.67.0 and PHP 8.4.23.

// oracleCase is one generated file and what the oracle read from it.
type oracleCase struct {
	Input    string     `json:"input,omitempty"`
	InputHex string     `json:"input_hex,omitempty"` // an input that is not UTF-8, which JSON cannot hold
	Entries  [][]string `json:"entries,omitempty"`
	Refused  string     `json:"refused,omitempty"`
	// What ansible read, by group and by host: a str value as the hex of its
	// bytes, any other as {"repr": ...}.
	Hosts     map[string][]string                   `json:"hosts,omitempty"`
	Children  map[string][]string                   `json:"children,omitempty"`
	GroupVars map[string]map[string]json.RawMessage `json:"group_vars,omitempty"`
	HostVars  map[string]map[string]json.RawMessage `json:"host_vars,omitempty"`
	// The hosts ansible expands a host pattern to.
	Expanded []string `json:"expanded,omitempty"`
	// What PHP read, by section: a string, or an object of key[] values.
	Sections map[string]map[string]json.RawMessage `json:"sections,omitempty"`
}

// input is the case's file.
func (c oracleCase) input() []byte {
	if c.InputHex != "" {
		b, _ := hex.DecodeString(c.InputHex)
		return b
	}
	return []byte(c.Input)
}

const (
	javaCorpus    = "testdata/oracle/properties-java.json"
	pythonCorpus  = "testdata/oracle/plain-configparser.json"
	toxCorpus     = "testdata/oracle/tox.json"
	phpCorpus     = "testdata/oracle/php.json"
	ansibleCorpus = "testdata/oracle/ansible-inventory.json"
	rangesCorpus  = "testdata/oracle/ansible-ranges.json"
	oracleCases   = 800
)

// propertiesPieces are what generated .properties files are made of: every
// separator, white space, line break, comment character and escape, and the
// escapes Java refuses.
var propertiesPieces = []string{
	"a", "B", "k.x", "=", ":", " ", "\t", "\f", "\\", "\\\\", "\\\n", "\\\r\n", "\\\r", "\n", "\n", "\r\n", "\r",
	"#", "!", "\\u0041", "\\u00e9", "\\uD83D\\uDE00", "\\uD83D", "\\uDE00", "\\u12", "é", "\\t", "\\n",
	"\\z", "\\ ", "\\=", "\\:", "\\#", " \\\n   ", "\n  # c\n", "\n! c\n",
	// White space and line breaks Java does not take, and bytes that are not
	// UTF-8, which Java reads as U+FFFD.
	"\v", "\u0085", "\u2028", "\xff", "\xe2\x82", "\xed\xa0\x80", "\xf0\x9f\x98",
}

// plainLine draws one line of a generated INI file.
func plainLine(r *rand.Rand) string {
	pick := func(from ...string) string { return from[r.IntN(len(from))] }
	indent := pick("", "", "", " ", "\t", "  ", "\u00a0")
	var line string
	switch r.IntN(10) {
	case 0, 1:
		line = pick("[s]", "[t]", "[ s ]", "[a]b]", "[]", "[s] ; c", "[DEFAULT]", "[x:y]")
	case 2:
		line = pick("# c", "; c", "#", ";", "  # k = v")
	case 3:
		line = ""
	case 4:
		line = pick("junk", "[s", "= v", " : v")
	default:
		var v strings.Builder
		for range r.IntN(4) {
			v.WriteString(pick("v", " x ", ";", "#", "=", ":", "%(a)s", "é", "\u00a0", "\x1c", " ; c"))
		}
		line = pick("k", "K", "key name", "a") + pick(" = ", "=", ": ", ":", " =", "\t=\t") + v.String()
	}
	if r.IntN(20) == 0 {
		line += pick("\xff", "\v", "\f", "\u0085", "\u2028") // a byte that is not UTF-8 is refused, the rest are blanks
	}
	return indent + line + pick("\n", "\n", "\n", "\r\n", "\r")
}

// toxLine draws one line of a generated tox.ini.
func toxLine(r *rand.Rand) string {
	pick := func(from ...string) string { return from[r.IntN(len(from))] }
	var line string
	switch r.IntN(10) {
	case 0:
		line = pick("[testenv]", "[testenv:py311]", "[tox]", "[ s ]")
	case 1:
		line = pick("# c", "; c", "", "  # inside")
	case 2:
		line = pick("    more", "    pytest # c", "  \\#x", "  a \\", "    py311: x", "\tb")
	default:
		var v strings.Builder
		for range r.IntN(4) {
			v.WriteString(pick("pytest", " # pin", "\\#x", "a#b", "{posargs}", "py311: x", " \\", ";", " -q", "é", "\t#c"))
		}
		line = pick("deps", "commands", "setenv", "description", "basepython", "skip_install", "allowlist_externals", "passenv", "Deps") +
			pick("", "", "_1", "_2", "_3", "_4") + pick(" = ", "=", ": ") + v.String()
	}
	if r.IntN(150) == 0 {
		line += "\xff"
	}
	return line + pick("\n", "\n", "\n", "\r\n", "\r")
}

// phpTestLine draws one line of a generated php.ini. No key is named as a
// section is, since PHP keeps the two in one array, only arr takes [], and
// no section is written twice in one file, since parse_ini_string then
// drops the first, where PHP reading php.ini applies every key in order.
func phpTestLine(r *rand.Rand, used map[string]bool) string {
	pick := func(from ...string) string { return from[r.IntN(len(from))] }
	var line string
	switch r.IntN(10) {
	case 0, 1:
		line = pick("[s]", "[PHP]", "[ s ]", "[x y]", "[s] ; c", "[s] k = 1", "  [s]", "[s", "[a;b]", "['q']", "[\"q r\"]", "[s]]", "[s] x", "[t]", "[u v]")
		if name, _, _ := strings.Cut(strings.TrimLeft(line, " \t["), "]"); used[name] {
			line = "; " + line
		} else {
			used[name] = true
		}
	case 2:
		line = pick("; c", "  ; c", "#c", "", "\tk", "bare")
	default:
		var v strings.Builder
		for range r.IntN(5) {
			piece := pick("v", "x y", "1", "-2", "1.5", "a.b", "/var/log/x", "'s q'", "\"d q\"", "\"a\\\"b\"", "\"C:\\p\\\"",
				" ", "\t", "$x", "${HOME}", "${HOME:-d}", "E_ALL", " ;c", "é", "\\")
			if r.IntN(12) == 0 { // the pieces PHP's grammar mostly refuses, or reads alone
				piece = pick("${H:-d}", "~", "(", ")", "=", "'", "''", "\"", "{", "}", "on", "|", " & E_X")
			}
			v.WriteString(piece)
		}
		key := pick("k", "a", "b c", "arr[]", "arr[ ]", "\tk", "k ", "c1", "c2")
		if r.IntN(20) == 0 {
			key = pick("on", "no", "null")
		}
		line = key + pick(" = ", "=", "\t=\t", " =") + v.String()
	}
	return line + pick("\n", "\n", "\n", "\r\n", "\r")
}

func generateCorpus(seed uint64, gen func(*rand.Rand) string) []string {
	r := rand.New(rand.NewPCG(seed, seed))
	out := make([]string, oracleCases)
	for i := range out {
		out[i] = gen(r)
	}
	return out
}

func propertiesInputs() []string {
	return generateCorpus(41, func(r *rand.Rand) string {
		var b strings.Builder
		for range r.IntN(30) {
			b.WriteString(propertiesPieces[r.IntN(len(propertiesPieces))])
		}
		return b.String()
	})
}

func plainInputs() []string {
	return generateCorpus(40, func(r *rand.Rand) string {
		var b strings.Builder
		for range r.IntN(10) {
			b.WriteString(plainLine(r))
		}
		return b.String()
	})
}

func toxInputs() []string {
	return generateCorpus(43, func(r *rand.Rand) string {
		var b strings.Builder
		if r.IntN(10) == 0 {
			b.Write(bom)
		}
		if r.IntN(10) > 0 {
			b.WriteString("[testenv]\n")
		}
		for range r.IntN(8) {
			b.WriteString(toxLine(r))
		}
		return b.String()
	})
}

func phpInputs() []string {
	return generateCorpus(44, func(r *rand.Rand) string {
		var b strings.Builder
		if r.IntN(10) == 0 {
			b.Write(bom)
		}
		used := map[string]bool{}
		for range r.IntN(10) {
			b.WriteString(phpTestLine(r, used))
		}
		return b.String()
	})
}

// TestRegenerateOracleCorpora writes the corpora again, from the Java and
// the Python named in ERRATA_JAVA and ERRATA_PYTHON, and is skipped without
// them. It does not compare: the two tests below do, from the corpora.
func TestRegenerateOracleCorpora(t *testing.T) {
	java, python, tools, php := os.Getenv("ERRATA_JAVA"), os.Getenv("ERRATA_PYTHON"), os.Getenv("ERRATA_TOOL_PYTHON"), os.Getenv("ERRATA_PHP")
	if java == "" || python == "" || tools == "" || php == "" {
		t.Skip("set ERRATA_JAVA, ERRATA_PYTHON, ERRATA_TOOL_PYTHON and ERRATA_PHP to write the oracle corpora again")
	}
	writeCorpus(t, javaCorpus, propertiesInputs(), java, "testdata/oracle/PropertiesOracle.java")
	writeCorpus(t, pythonCorpus, plainInputs(), python, "-I", "testdata/oracle/configparser_oracle.py")
	writeCorpus(t, toxCorpus, toxInputs(), tools, "-I", "testdata/oracle/tox_oracle.py")
	// One PHP for each file: PHP 8.4.23 crashes when one process parses
	// many of them, which errata's reader does not depend on.
	writeCorpusEach(t, phpCorpus, phpInputs(), php, "-n", "testdata/oracle/php_oracle.php")
	writeCorpus(t, ansibleCorpus, ansibleInputs(), tools, "-I", "testdata/oracle/ansible_oracle.py")
	writeCorpus(t, rangesCorpus, rangeInputs(), tools, "-I", "testdata/oracle/ansible_ranges_oracle.py")
}

// writeCorpus runs the oracle command over every input and writes what it
// read, case by case, to corpus.
func writeCorpus(t *testing.T, corpus string, inputs []string, command ...string) {
	t.Helper()
	files := writeInputs(t, inputs)
	writeResults(t, corpus, inputs, runOracle(t, append(command, files...)))
}

// writeCorpusEach is writeCorpus with one process for each input.
func writeCorpusEach(t *testing.T, corpus string, inputs []string, command ...string) {
	t.Helper()
	var out strings.Builder
	for _, f := range writeInputs(t, inputs) {
		out.WriteString(runOracle(t, append(slices.Clone(command), f)))
	}
	writeResults(t, corpus, inputs, out.String())
}

func writeInputs(t *testing.T, inputs []string) []string {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for i, in := range inputs {
		p := filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	return files
}

func runOracle(t *testing.T, command []string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), command[0], command[1:]...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v: %s", command[0], err, stderr.String())
	}
	return string(out)
}

// writeResults writes the oracle's lines of JSON, one for each input, to
// corpus with the inputs.
func writeResults(t *testing.T, corpus string, inputs []string, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(inputs) {
		t.Fatalf("%d results for %d inputs", len(lines), len(inputs))
	}
	cases := make([]oracleCase, len(inputs))
	for i, l := range lines {
		if err := json.Unmarshal([]byte(l), &cases[i]); err != nil {
			t.Fatalf("result %d: %v: %s", i, err, l)
		}
		if utf8.ValidString(inputs[i]) {
			cases[i].Input = inputs[i]
		} else {
			cases[i].InputHex = hex.EncodeToString([]byte(inputs[i]))
		}
	}
	// One case to a line, so a diff of the corpus names the cases it changes.
	lines = lines[:0]
	for _, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(line))
	}
	data := "[\n" + strings.Join(lines, ",\n") + "\n]\n"
	if err := os.WriteFile(corpus, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCorpus(t *testing.T, corpus string, inputs []string) []oracleCase {
	t.Helper()
	data, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var cases []oracleCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	// The corpus is of the inputs the generators draw now, so a change to
	// a generator without the corpus written again fails here.
	if len(cases) != len(inputs) {
		t.Fatalf("%s holds %d cases, the generator draws %d; write it again", corpus, len(cases), len(inputs))
	}
	for i := range cases {
		if string(cases[i].input()) != inputs[i] {
			t.Fatalf("%s: case %d is not the generator's input; write it again", corpus, i)
		}
	}
	return cases
}

// compareOracle reads each case with read and compares the entries, made
// into strings by entry, or the refusal, with the oracle's.
func compareOracle(t *testing.T, cases []oracleCase, read readFunc, entry func(*iniEntry) []string) (read_, refused int) {
	t.Helper()
	for i, c := range cases {
		var got [][]string
		err := read(c.input(), nil, func(e *iniEntry) error {
			got = append(got, entry(e))
			return nil
		})
		switch {
		case c.Refused != "" && err == nil:
			t.Errorf("case %d %q: the oracle refuses it (%s) and errata reads %q", i, c.Input, c.Refused, got)
		case c.Refused == "" && err != nil:
			t.Errorf("case %d %q: the oracle reads %q and errata refuses it: %v", i, c.Input, c.Entries, err)
		case c.Refused == "" && !reflect.DeepEqual(got, c.Entries):
			t.Errorf("case %d %q: errata reads %q, the oracle %q", i, c.Input, got, c.Entries)
		case c.Refused != "":
			refused++
		default:
			read_ += len(got)
		}
	}
	return read_, refused
}

// The properties reader reads every file of the corpus as Properties.load
// read it: the same keys and values in order, and a refusal where Java threw.
func TestPropertiesAgreesWithJava(t *testing.T) {
	cases := readCorpus(t, javaCorpus, propertiesInputs())
	entries, refused := compareOracle(t, cases, readProperties, func(e *iniEntry) []string {
		return []string{string(e.key), string(e.value)}
	})
	// The corpus must hold both outcomes in number, or it compares little.
	if entries < oracleCases || refused < oracleCases/20 {
		t.Errorf("the corpus holds %d entries and %d refusals", entries, refused)
	}
	if testing.Verbose() {
		t.Logf("%d entries, %d refusals", entries, refused)
	}
}

// The plain reader reads every file of the corpus as configparser read it.
func TestPlainAgreesWithConfigparser(t *testing.T) {
	cases := readCorpus(t, pythonCorpus, plainInputs())
	entries, refused := compareOracle(t, cases, plainDialect.read, func(e *iniEntry) []string {
		return []string{string(e.section), string(e.key), string(e.value)}
	})
	if entries < oracleCases || refused < oracleCases/20 {
		t.Errorf("the corpus holds %d entries and %d refusals", entries, refused)
	}
	if testing.Verbose() {
		t.Logf("%d entries, %d refusals", entries, refused)
	}
}

// ansibleLine draws one line of a generated inventory, mostly one that
// fits the section it is in, which tag names: the groups are web, db and
// all, the hosts h1 to h3. No host has a port or a range ansible expands,
// which errata keeps as written; the ranges drawn are ones ansible refuses.
// No host is named as a group is: ansible gives such a host's variables to
// the group.
func ansibleLine(r *rand.Rand, tag *string) string {
	pick := func(from ...string) string { return from[r.IntN(len(from))] }
	fits := r.IntN(8) > 0
	var line string
	switch kind := r.IntN(8); {
	case kind < 2:
		line = pick("[web]", "[db]", "[web:hosts]", "[db:vars]", "[web:vars]", "[all:vars]", "[web:children]", "[db] # c", "[db:children]")
		if !fits {
			line = pick("[db] ; c", "[web:other]", "[web group]")
		}
		_, *tag, _ = strings.Cut(strings.TrimSuffix(strings.Fields(line)[0], "]"), ":")
	case kind == 2:
		line = pick("# c", "; c", "", "  ")
	case (*tag == "vars") == fits && *tag != "children":
		line = pick("a = 1", "b=two words # c", "c = \"q\"", "d = 'x\"y'", "f = 0x10", "g = [1, 2]", " h = ' s '", "i = 'q' # c", "j = 'q' x", "e",
			"k = 8080 # http", "l = 0x1F # c", "m = True#c", "n = None", "o = [1, 2] # c", "p = [1, # c", "q = 'a' 'b' # c", "r = -1_000 # c",
			"s = 1.5 # c", "t = pool.org # c", "u = \xff", "v = \"a\\nb\" # c", "w = 017 # c", "x = (1,) # c", "y = {1: 2}",
			// String literals as Python decodes them, and ones errata refuses.
			`a = 'x\ty'`, `b = 'a' "b"`, `c = r'a\tb'`, `e = b'\x41\101'`, `f = u'\1234'`, `g = '\q'`, `h = '\U0001F600'`,
			`i = f'x'`, `j = '\x4'`, `l = '\N{BULLET}'`, `m = '\ud800'`, `n = "a#b" "c" # d`, `o = b'\xff'`, `p = '''t'''`, "q = 'x\xff'")
	case *tag == "children" && fits:
		line = pick("db", "web", "db # c", "web db")
	default:
		var b strings.Builder
		b.WriteString(pick("h1", "h2", "h3"))
		if !fits {
			b.Reset()
			b.WriteString(pick("h1", "web2:", "---", "h[01:3]", "h[3:1]", "h[a:c:0]", "h[x]", "h[2:1]", "h[1:3:0]"))
		}
		for range r.IntN(3) {
			b.WriteString(pick(" a=1", " b=\"x y\"", " c='q'", " a=\"'z'\"", " i=\"'z' # c\"", " d=0x10", " e=x\\ y", " h=2",
				" j=\"5 #x\"", " k=\"[1, 2] # c\"", " l=\"True # c\"", " m=\xff", ` n="'x\\ty'"`, ` o="'a' 'b'"`, ` p="b'\\x41'"`))
		}
		if !fits {
			b.WriteString(pick(" f", " g=\"open", " k=\\"))
		}
		line = b.String() + pick("", "", " # c")
	}
	return pick("", "", " ", "\t") + line + pick("\n", "\n", "\r\n", "\r", "\f", "\v", "\x1c", "\u0085", "\u2028")
}

func ansibleInputs() []string {
	return generateCorpus(42, func(r *rand.Rand) string {
		var b strings.Builder
		tag := ""
		for range r.IntN(12) {
			b.WriteString(ansibleLine(r, &tag))
		}
		return b.String()
	})
}

// ansibleVar is a variable as errata reads it, for the comparison.
type ansibleVar = []byte

// ansibleRead is what errata's entries say ansible reads, folded as ansible
// folds them: a group's hosts and children once each in the order first
// written, and the last value of each variable.
func ansibleRead(data []byte) (got struct {
	hosts, children     map[string][]string
	groupVars, hostVars map[string]map[string]ansibleVar
}, err error) {
	got.hosts, got.children = map[string][]string{}, map[string][]string{}
	got.groupVars, got.hostVars = map[string]map[string]ansibleVar{}, map[string]map[string]ansibleVar{}
	set := func(m map[string]map[string]ansibleVar, name, key string, value []byte) {
		if m[name] == nil {
			m[name] = map[string]ansibleVar{}
		}
		m[name][key] = slices.Clone(value)
	}
	add := func(m map[string][]string, name, item string) {
		if !slices.Contains(m[name], item) {
			m[name] = append(m[name], item)
		}
	}
	err = readAnsible(data, nil, func(e *iniEntry) error {
		group, tag, _ := strings.Cut(string(e.section), ":")
		switch {
		case tag == "vars":
			set(got.groupVars, group, string(e.key), e.value)
		case tag == "children":
			add(got.children, group, string(e.value))
		case e.kind == entryName:
			if group != "ungrouped" {
				add(got.hosts, group, string(e.host))
			}
		default:
			set(got.hostVars, string(e.host), string(e.key), e.value)
		}
		return nil
	})
	return got, err
}

// errataRefusesByDesign are the refusals errata makes where ansible reads
// the file, as the ansible dialect documents.
var errataRefusesByDesign = []string{"is a Python string errata cannot decode exactly"}

// ansibleRefusesBeyond are the refusals ansible makes from its group graph,
// which errata does not check.
var ansibleRefusesBeyond = []string{"undefined group", "add group to itself"}

// The ansible reader reads every inventory of the corpus as ansible-core
// read it: each group's hosts, children and variables, each host's
// variables, and a refusal where ansible refused. Left out: the group graph
// refusals ansible makes and errata does not check, and the values errata
// refuses by design. A variable ansible evaluated to an int, True, False or
// None is compared with errata's text as Python reads it; any other that is
// no str is counted and not compared, since errata keeps its text.
func TestAnsibleAgreesWithAnsible(t *testing.T) {
	cases := readCorpus(t, ansibleCorpus, ansibleInputs())
	var compared, refused, skipped, values int
	for i, want := range cases {
		got, err := ansibleRead(want.input())
		switch {
		case containsAny(want.Refused, ansibleRefusesBeyond), err != nil && containsAny(err.Error(), errataRefusesByDesign):
			skipped++
			continue
		case want.Refused != "" && err == nil:
			t.Errorf("case %d %q: ansible refuses it (%s) and errata reads it", i, want.input(), want.Refused)
		case want.Refused == "" && err != nil:
			t.Errorf("case %d %q: ansible reads it and errata refuses it: %v", i, want.input(), err)
		case want.Refused != "":
			refused++
		default:
			compared++
			n, diff := ansibleDiff(got.hosts, got.children, got.groupVars, got.hostVars, want)
			values += n
			if diff != "" {
				t.Errorf("case %d %q: %s", i, want.input(), diff)
			}
		}
	}
	if compared < oracleCases/4 || refused < oracleCases/20 || values < oracleCases/2 {
		t.Errorf("the corpus compares %d inventories, %d values and %d refusals", compared, values, refused)
	}
	if testing.Verbose() {
		t.Logf("%d inventories, %d values, %d refusals, %d left out", compared, values, refused, skipped)
	}
}

func containsAny(s string, subs []string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}

// ansibleDiff describes where errata's reading differs from ansible's, and
// counts the values it compared.
func ansibleDiff(hosts, children map[string][]string, groupVars, hostVars map[string]map[string]ansibleVar, want oracleCase) (int, string) {
	nonEmpty := func(m map[string][]string, skip ...string) map[string][]string {
		out := map[string][]string{}
		for k, v := range m {
			if len(v) > 0 && !slices.Contains(skip, k) {
				out[k] = v
			}
		}
		return out
	}
	if g, w := nonEmpty(hosts), nonEmpty(want.Hosts); !reflect.DeepEqual(g, w) {
		return 0, fmt.Sprintf("hosts %v, ansible %v", g, w)
	}
	if g, w := nonEmpty(children), nonEmpty(want.Children, "all"); !reflect.DeepEqual(g, w) {
		return 0, fmt.Sprintf("children %v, ansible %v", g, w)
	}
	n := 0
	for _, m := range []struct {
		name string
		got  map[string]map[string]ansibleVar
		want map[string]map[string]json.RawMessage
	}{{"group", groupVars, want.GroupVars}, {"host", hostVars, want.HostVars}} {
		for name, vars := range m.want {
			for k, raw := range vars {
				g, ok := m.got[name][k]
				if !ok {
					return n, fmt.Sprintf("%s %s: no %s, ansible %s", m.name, name, k, raw)
				}
				same, compared := sameAnsibleValue(g, raw)
				if !same {
					return n, fmt.Sprintf("%s %s: %s is %q, ansible %s", m.name, name, k, g, raw)
				}
				if compared {
					n++
				}
			}
			for k := range m.got[name] {
				if _, ok := vars[k]; !ok {
					return n, fmt.Sprintf("%s %s: %s, which ansible does not set", m.name, name, k)
				}
			}
		}
		for name, vars := range m.got {
			if _, ok := m.want[name]; !ok && len(vars) > 0 {
				return n, fmt.Sprintf("%s %s, which ansible does not have", m.name, name)
			}
		}
	}
	return n, ""
}

// sameAnsibleValue compares errata's text with ansible's value: a str byte
// for byte, an int by its value as Python reads errata's text, True, False
// and None as the text. It reports whether it compared.
func sameAnsibleValue(got []byte, raw json.RawMessage) (same, compared bool) {
	var hexText string
	if json.Unmarshal(raw, &hexText) == nil {
		want, _ := hex.DecodeString(hexText)
		return string(got) == string(want), true
	}
	var v struct {
		Repr string `json:"repr"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return false, true
	}
	switch v.Repr {
	case "True", "False", "None":
		return string(got) == v.Repr, true
	}
	if w, ok := new(big.Int).SetString(v.Repr, 10); ok {
		text := strings.ReplaceAll(strings.TrimSpace(string(got)), "_", "")
		g, ok := new(big.Int).SetString(text, 0)
		return ok && g.Cmp(w) == 0, true
	}
	return true, false // a float, a list or another value errata keeps as written
}

// The tox reader reads every file of the corpus as tox read it.
func TestToxAgreesWithTox(t *testing.T) {
	cases := readCorpus(t, toxCorpus, toxInputs())
	entries, refused := compareOracle(t, cases, toxDialect.read, func(e *iniEntry) []string {
		return []string{string(e.section), string(e.key), string(e.value)}
	})
	if entries < oracleCases || refused < oracleCases/20 {
		t.Errorf("the corpus holds %d entries and %d refusals", entries, refused)
	}
	if testing.Verbose() {
		t.Logf("%d entries, %d refusals", entries, refused)
	}
}

// The php reader reads every file of the corpus as PHP's parse_ini_string
// read it, in the normal mode PHP reads php.ini in: the last value of each
// key in each section, and key[] as the list of its values. A value PHP
// evaluates (a boolean word, a constant, ${}, an operator), which errata
// returns as written and marks computed, is counted and not compared.
func TestPHPAgreesWithPHP(t *testing.T) {
	cases := readCorpus(t, phpCorpus, phpInputs())
	var values, computed, refused int
	for i, c := range cases {
		got := map[string]map[string][]*iniEntry{}
		err := readPHP(c.input(), nil, func(e *iniEntry) error {
			if got[string(e.section)] == nil {
				got[string(e.section)] = map[string][]*iniEntry{}
			}
			x := *e
			x.value = slices.Clone(e.value)
			got[string(e.section)][string(e.key)] = append(got[string(e.section)][string(e.key)], &x)
			return nil
		})
		switch {
		case c.Refused != "" && err == nil:
			t.Errorf("case %d %q: PHP refuses it (%s) and errata reads it", i, c.Input, c.Refused)
			continue
		case c.Refused == "" && err != nil:
			t.Errorf("case %d %q: PHP reads %v and errata refuses it: %v", i, c.Input, c.Sections, err)
			continue
		case c.Refused != "":
			refused++
			continue
		}
		for section, keys := range c.Sections {
			for key, raw := range keys {
				es := got[section][key]
				switch {
				case len(es) == 0:
					t.Errorf("case %d %q: [%s] %s is %s to PHP, and errata has none", i, c.Input, section, key, raw)
				case slices.ContainsFunc(es, func(e *iniEntry) bool { return e.computed }):
					computed++
				default:
					want := map[string]string{}
					var text string
					if json.Unmarshal(raw, &text) == nil {
						if last := es[len(es)-1]; string(last.value) != text {
							t.Errorf("case %d %q: [%s] %s is %q, PHP %q", i, c.Input, section, key, last.value, text)
						}
					} else if json.Unmarshal(raw, &want) == nil {
						var list []string
						for j := range len(want) {
							list = append(list, want[strconv.Itoa(j)])
						}
						var gotList []string
						for _, e := range es {
							gotList = append(gotList, string(e.value))
						}
						if !slices.Equal(list, gotList) {
							t.Errorf("case %d %q: [%s] %s[] is %q, PHP %q", i, c.Input, section, key, gotList, list)
						}
					}
					values++
				}
			}
		}
		for section, keys := range got {
			for key := range keys {
				if _, ok := c.Sections[section][key]; !ok {
					t.Errorf("case %d %q: errata reads [%s] %s, which PHP does not", i, c.Input, section, key)
				}
			}
		}
	}
	if values < oracleCases/2 || refused < oracleCases/20 {
		t.Errorf("the corpus compares %d values, %d computed, and %d refusals", values, computed, refused)
	}
	if testing.Verbose() {
		t.Logf("%d values, %d computed, %d refusals", values, computed, refused)
	}
}

// rangeInputs are host patterns of one to three ranges, numeric with and
// without zeros and steps up and down, of letters, empty, refused, between
// literal text and with a port, each expanding to a few hundred hosts at
// most.
func rangeInputs() []string {
	return generateCorpus(45, func(r *rand.Rand) string {
		pick := func(from ...string) string { return from[r.IntN(len(from))] }
		var b strings.Builder
		b.WriteString(pick("web", "db-", "h", "", "node.", "a1"))
		for range 1 + r.IntN(3) {
			switch r.IntN(6) {
			case 0, 1, 2:
				lo, hi := r.IntN(13), r.IntN(13)
				begin, end := strconv.Itoa(lo), strconv.Itoa(hi)
				if r.IntN(3) == 0 {
					begin, end = fmt.Sprintf("%02d", lo), fmt.Sprintf("%02d", hi)
				}
				b.WriteString("[" + begin + ":" + end + pick("", "", ":2", ":3", ":-1", ":-2", ":0", ":x") + "]")
			case 3, 4:
				b.WriteString(pick("[a:f]", "[a:c:2]", "[x:z]", "[A:C]", "[c:a]", "[ab:cd]", "[Y:b]", "[a:e:-1]", "[a:b:x]"))
			default:
				b.WriteString(pick("[1:3", "[]", "[:4]", "[1:]", "[1:2:3:4]", "[0001:12]", "[ 1: 2]"))
			}
			b.WriteString(pick("", "", "-", "x", ".", "0"))
		}
		b.WriteString(pick("", "", ".example.com", ":22", ":"))
		return b.String()
	})
}

// hostIn decides, without expanding, exactly the hosts ansible expands a
// pattern to: every one of them, and none of the names a step away from
// them (a byte more, a byte less, a byte changed, a number past the range).
// checkHost refuses the patterns ansible refuses and finds the empty ones.
func TestHostInAgreesWithAnsible(t *testing.T) {
	cases := readCorpus(t, rangesCorpus, rangeInputs())
	var hosts, probes, refused, empty int
	for i, c := range cases {
		pattern := []byte(c.Input)
		isEmpty, err := checkHost(pattern, nil)
		switch {
		case c.Refused != "" && err == nil:
			t.Errorf("case %d %q: ansible refuses it (%s) and errata reads it", i, c.Input, c.Refused)
			continue
		case c.Refused == "" && err != nil:
			t.Errorf("case %d %q: ansible expands it to %d hosts and errata refuses it: %v", i, c.Input, len(c.Expanded), err)
			continue
		case c.Refused != "":
			refused++
			continue
		case isEmpty != (len(c.Expanded) == 0):
			t.Errorf("case %d %q: errata reads it as empty %v, ansible expands it to %d hosts", i, c.Input, isEmpty, len(c.Expanded))
		}
		if isEmpty {
			empty++
			continue
		}
		in := map[string]bool{}
		for _, h := range c.Expanded {
			in[h] = true
		}
		for _, h := range c.Expanded {
			hosts++
			if ok, err := hostIn(pattern, []byte(h), nil); !ok || err != nil {
				t.Errorf("case %d %q: %q is one of ansible's hosts, and hostIn says no: %v", i, c.Input, h, err)
			}
			for _, probe := range nearNames(h) {
				probes++
				if got, err := hostIn(pattern, []byte(probe), nil); got != in[probe] || err != nil {
					t.Errorf("case %d %q: hostIn(%q) is %v, ansible's hosts hold it: %v", i, c.Input, probe, got, in[probe])
				}
			}
		}
	}
	if hosts < oracleCases || probes < 4*oracleCases || refused < oracleCases/20 || empty < oracleCases/50 {
		t.Errorf("the corpus compares %d hosts, %d near names, %d refusals and %d empty patterns", hosts, probes, refused, empty)
	}
	if testing.Verbose() {
		t.Logf("%d hosts, %d near names, %d refusals, %d empty patterns", hosts, probes, refused, empty)
	}
}

// nearNames are names a step away from h: a byte more, a byte less, each
// digit changed, and each number one more and one less, written without
// its zeros too.
func nearNames(h string) []string {
	out := []string{h + "0", "0" + h, h + "a"}
	if len(h) > 0 {
		out = append(out, h[1:], h[:len(h)-1])
	}
	for i := range len(h) {
		c := h[i]
		switch {
		case isDigit(c):
			for _, d := range []byte{'0', '1', '9', c + 1, c - 1} {
				if isDigit(d) && d != c {
					out = append(out, h[:i]+string(d)+h[i+1:])
				}
			}
			if i+1 < len(h) && isDigit(h[i+1]) && c == '0' {
				out = append(out, h[:i]+h[i+1:]) // the zero dropped
			}
		case isAlpha(c):
			out = append(out, h[:i]+string(c^0x20)+h[i+1:], h[:i]+string(c+1)+h[i+1:])
		}
	}
	return out
}
