package extract

import (
	"strings"
	"testing"
	"time"
)

// What ansible reads from string literals, containers with a comment, and a
// range of no host; each case was run through ansible-core 2.21.5.
func TestAnsibleReadsLiteralsAndEmptyRanges(t *testing.T) {
	a := func(section, host, key string) Spec {
		return Spec{INI: &INI{File: "hosts", Section: section, Host: host, Key: key}}
	}
	for _, c := range []struct {
		name, body string
		spec       Spec
		want       string
	}{
		{"an escape", "[g:vars]\nx = 'x\\ty'\n", a("g:vars", "", "x"), "x\ty@2"},
		{"two strings joined", "[g:vars]\nx = 'a' \"b\"\n", a("g:vars", "", "x"), "ab@2"},
		{"two strings and a comment", "[g:vars]\nx = 'a' 'b' # c\n", a("g:vars", "", "x"), "ab@2"},
		{"a raw string", "[g:vars]\nx = r'a\\tb'\n", a("g:vars", "", "x"), "a\\tb@2"},
		{"bytes", "[g:vars]\nx = b'\\x41\\101'\n", a("g:vars", "", "x"), "AA@2"},
		{"u and an octal escape", "[g:vars]\nx = u'\\1234'\n", a("g:vars", "", "x"), "S4@2"},
		{"an unknown escape stays", "[g:vars]\nx = '\\q'\n", a("g:vars", "", "x"), "\\q@2"},
		{"a \\U escape", "[g:vars]\nx = '\\U0001F600'\n", a("g:vars", "", "x"), "\U0001F600@2"},
		{"an f-string is no literal", "[g:vars]\nx = f'x'\n", a("g:vars", "", "x"), "f'x'@2"},
		{"a cut \\x is no literal", "[g:vars]\nx = '\\x4'\n", a("g:vars", "", "x"), "'\\x4'@2"},
		{"a host value with an escape", "[g]\nh c=\"'x\\\\ty'\"\n", a("g", "h", "c"), "x\ty@2"},
		{"a list and a comment", "[g:vars]\nx = [1, 2] # c\n", a("g:vars", "", "x"), "[1, 2]@2"},
		{"a tuple and a comment", "[g:vars]\nx = (1,) # c\n", a("g:vars", "", "x"), "(1,)@2"},
		{"a range of no host reads none", "[g]\nh[3:1] v=1\nh2\n", a("g", "", ""), "h2@3"},
		{"nor its variables", "[g]\nh[3:1] v=1\n", a("g", "", "v"), ""},
		{"a range bound of 4300 digits", "[g]\nh[0:" + strings.Repeat("9", 4300) + "]\n", a("g", "", ""), "h[0:" + strings.Repeat("9", 4300) + "]@2"},
		{"4301 digits with _ are 4300", "[g]\nh[0:" + strings.Repeat("9", 2150) + "_" + strings.Repeat("9", 2150) + "]\n", a("g", "", ""),
			"h[0:" + strings.Repeat("9", 2150) + "_" + strings.Repeat("9", 2150) + "]@2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, "hosts", c.body, c.spec); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The planted breaks: what ansible refuses, and what errata cannot decode
// exactly.
func TestAnsibleRefusesLiteralsAndRanges(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"a range bound of 4301 digits", "[g]\nh[0:" + strings.Repeat("9", 4301) + "]\n",
			`hosts: line 2: the host "h[0:` + strings.Repeat("9", 60) + `"... has a range ansible refuses: a bound of more than 4300 digits, which Python's int() refuses`},
		{"leading zeros count", "[g]\nh[" + strings.Repeat("0", 4301) + ":" + strings.Repeat("9", 4301) + "]\n",
			`hosts: line 2: the host "h[` + strings.Repeat("0", 62) + `"... has a range ansible refuses: a bound of more than 4300 digits`},
		{"a \\N escape", "[g:vars]\nx = '\\N{BULLET}'\n",
			`hosts: line 2: the value "'\\N{BULLET}'" is a Python string errata cannot decode exactly`},
		{"a surrogate escape", "[g:vars]\nx = '\\ud800'\n",
			`hosts: line 2: the value "'\\ud800'" is a Python string errata cannot decode exactly`},
		{"a range of no host still checks its variables", "[g]\nh[3:1] v\n",
			`hosts: line 2: expected key=value after the host, got "v"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := Spec{INI: &INI{File: "hosts", Section: "g", Key: "v"}}
			if strings.Contains(c.body, ":vars") {
				spec = Spec{INI: &INI{File: "hosts", Section: "g:vars", Key: "x"}}
			}
			got := iniError(t, "hosts", c.body, spec)
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("error %q, want %q", got, c.want)
			}
		})
	}
}

// The planted break: ${ nested past the limit, which PHP reads to about
// 10000 levels and which cost errata time quadratic in the depth.
func TestPHPRefusesDeepReferences(t *testing.T) {
	for _, body := range []string{
		"a = " + strings.Repeat("${ab:-", maxDepth+1) + "x" + strings.Repeat("}", maxDepth+1) + "\n",
		"a = \"" + strings.Repeat("${ab:-\"", maxDepth+1) + "x" + strings.Repeat("\"}", maxDepth+1) + "\"\n",
	} {
		got := iniError(t, "php.ini", body, ini("php.ini", "", "a"))
		if want := "php.ini: line 1: nested more than 1000 levels deep, the limit"; !strings.HasPrefix(got, want) {
			t.Errorf("error %q, want %q", clip(got), want)
		}
	}
	body := "a = " + strings.Repeat("${ab:-", maxDepth) + "x" + strings.Repeat("}", maxDepth) + "\n"
	if got := iniValues(t, "php.ini", body, ini("php.ini", "", "a")); !strings.HasPrefix(got, "${ab:-${ab:-") {
		t.Errorf("at the limit: got %q", clip(got))
	}
}

// The matcher compares a section once for each header: an entry with the
// header's number keeps the answer of the entry before, whatever its bytes,
// so a long name costs its length once. The planted break is comparing for
// each entry, which reads the second entry's section.
func TestMatcherComparesEachSectionOnce(t *testing.T) {
	q := iniQuery{section: []byte("a"), key: []byte("k"), hasKey: true, host: []byte("h"), hasHost: true}
	m := newMatcher(&q, nil)
	first := iniEntry{section: []byte("a"), sectionSeq: 1, host: []byte("h"), hostSeq: 1, key: []byte("k")}
	if !m.wants(&first) {
		t.Fatal("the first entry is not wanted")
	}
	same := iniEntry{section: []byte("b"), sectionSeq: 1, host: []byte("x"), hostSeq: 1, key: []byte("k")}
	if !m.wants(&same) {
		t.Error("an entry of the same header and host line was compared again")
	}
	next := iniEntry{section: []byte("b"), sectionSeq: 2, host: []byte("h"), hostSeq: 2, key: []byte("k")}
	if m.wants(&next) {
		t.Error("an entry of the next header was not compared")
	}
}

// checkRanges walks a host's ranges by offset and copies nothing, so a host
// of many ranges costs its bytes, not its ranges times its bytes: 1000
// ranges before 32 MiB took 38 s when each range copied the host. The
// planted break is that copy, a string for each range.
func TestCheckRangesCopiesNothing(t *testing.T) {
	host := strings.Repeat("[0:1]", 999) + strings.Repeat("a", 1<<16)
	allocs := testing.AllocsPerRun(5, func() {
		if empty, err := checkRanges(host, nil); empty || err != nil {
			t.Fatalf("empty %v, %v", empty, err)
		}
	})
	if allocs > 2 {
		t.Errorf("%.0f allocations for 999 ranges", allocs)
	}
}

// A spec's host selects a host line when it is one of the hosts ansible
// reads from the line, decided without expanding: the range line's values,
// at the range line's line. Each case was run through ansible-core 2.21.5.
func TestAnsibleHostSelectsWithinARange(t *testing.T) {
	body := "[g]\nweb[01:20] v=1\ndb[0:20:5].x v=2\nn[a:f]-[1:2] v=3\ndb1:2222 v=4\nweb07 v=5\n"
	for host, want := range map[string]string{
		"web05": "1@2", "web20": "1@2", "web21": "", "web5": "", "web00": "",
		"db15.x": "2@3", "db14.x": "", "db20.x": "2@3",
		"nc-2": "3@4", "ng-1": "",
		"db1": "4@5", "db1:2222": "4@5",
		"web07":      "1@2 5@6",
		"web[01:20]": "1@2",
	} {
		got := iniValues(t, "hosts", body, Spec{INI: &INI{File: "hosts", Section: "g", Host: host, Key: "v"}})
		if got != want {
			t.Errorf("host %s: got %q, want %q", host, got, want)
		}
	}
}

// Bounds past int64 take the big.Int path, which checks a value's zeros
// as zfill writes them; each case is ansible's expansion of the pattern.
func TestHostInPastInt64(t *testing.T) {
	zeros := strings.Repeat("0", 20)
	pattern := "h[" + zeros + "1:" + zeros + "9:2]"
	for name, want := range map[string]bool{
		"h" + zeros + "5": true, "h" + zeros + "9": true, "h" + zeros + "4": false, "h5": false, "h0" + zeros + "5": false,
	} {
		if got, err := hostIn([]byte(pattern), []byte(name), nil); got != want || err != nil {
			t.Errorf("%s: %v, %v, want %v", name, got, err, want)
		}
	}
}

// A php reference nested 1000 deep around a long text copies the text
// once: the fallbacks inside keep no text of their own. The planted break
// is keeping it, which copies the text once for each level: 8 GB here.
func TestPHPDeepReferenceCopiesItsTextOnce(t *testing.T) {
	slowOnly(t)
	body := "a = " + strings.Repeat("${ab:-", maxDepth) + strings.Repeat("x", 8<<20) + strings.Repeat("}", maxDepth) + "\n"
	e, err := Build(ini("php.ini", "", "a"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := e.(entryExtractor).fromData([]byte(body))
	if took := time.Since(start); took > otherBound {
		t.Errorf("took %v, over %v", took, otherBound)
	}
	if err != nil || len(got) != 1 {
		t.Errorf("%d values, %v", len(got), err)
	}
}
