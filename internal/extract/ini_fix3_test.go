package extract

import (
	"reflect"
	"strings"
	"testing"
)

// The reviews' shapes past the per feature caps, each now refused by the
// one work meter of its read, at 64 KiB: lines of ranges of 17 digit
// bounds, each value of which a long name can start with at every length,
// and a long name against long bounds.
func TestWorkMeterRefusesTheReviewsShapes(t *testing.T) {
	fill := func(unit string) string { return strings.Repeat(unit, (64<<10)/len(unit)) }
	for _, c := range []struct {
		name, body string
		spec       Spec
	}{
		{"ten 17 digit ranges a line, a name of 1023 digits", "[g]\n" + fill("h"+strings.Repeat("[0:99999999999999999]", 10)+" v=1\n"),
			Spec{INI: &INI{File: "hosts", Section: "g", Host: "h" + strings.Repeat("1", 1023), Key: "v"}}},
		{"a bound of 4300 digits, a name of 1000", "[g]\nh" + strings.Repeat("[0:"+strings.Repeat("9", 4300)+"]", 3) + " v=1\n",
			Spec{INI: &INI{File: "hosts", Section: "g", Host: "h" + strings.Repeat("1", 1000), Key: "v"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := iniError(t, "hosts", c.body, c.spec)
			if want := "hosts: reading the file and matching the spec take more than 128 units of work for each byte of the two and 1048576 more, the limit"; !strings.HasPrefix(got, want) {
				t.Errorf("error %q", clip(got))
			}
		})
	}
}

// A spec's host over maxHostMatch bytes is refused against a line whose
// host has a range or a port, which ansible may read it from, and compared
// as written with any other.
func TestLongSpecHostIsRefusedAgainstARange(t *testing.T) {
	long := strings.Repeat("x", 1100)
	for body, want := range map[string]string{
		"[g]\n" + long + "[1:3] v=1\n": `hosts: line 2: the host "xxxx`,
		"[g]\n" + long + "2:22 v=1\n":  `hosts: line 2: the host "xxxx`,
	} {
		got := iniError(t, "hosts", body, Spec{INI: &INI{File: "hosts", Section: "g", Host: long + "2", Key: "v"}})
		if !strings.HasPrefix(got, want) || !strings.Contains(got, "has a range or a port, and the spec's host is over 1024 bytes") {
			t.Errorf("error %q", clip(got))
		}
	}
	if got := iniValues(t, "hosts", "[g]\n"+long+"2 v=1\n", Spec{INI: &INI{File: "hosts", Section: "g", Host: long + "2", Key: "v"}}); got != "1@2" {
		t.Errorf("a plain host written as it is: %q", got)
	}
}

// tox gives each section a key map of its own: a map does not shrink, and
// clearing the map of a large section at every header after it cost its
// size each time (90 s at 32 MiB). The planted break is one map, cleared.
func TestToxGivesEachSectionAMap(t *testing.T) {
	var maps []uintptr
	defer func(f func() map[string]bool) { newToxKeys = f }(newToxKeys)
	inner := newToxKeys
	newToxKeys = func() map[string]bool {
		m := inner()
		maps = append(maps, reflect.ValueOf(m).Pointer())
		return m
	}
	body := "[a]\n" + keyLines(1000, " = ") + "[b]\nk = 1\n[c]\nk = 1\n"
	if got := iniValues(t, "tox.ini", body, ini("tox.ini", "c", "k")); got != "1@1005" {
		t.Fatalf("got %q", got)
	}
	if len(maps) != 3 || maps[0] == maps[1] || maps[1] == maps[2] {
		t.Errorf("maps %v, want three of their own", maps)
	}
}

// The address patterns, some 400 ns a byte, run only where they decide
// something, and are charged for each byte: never for a key read with no
// host; for a host only when the name is a host of the line's host with its
// port and not without it, or the other way round. The planted breaks are
// running them on every host with a port, and running them uncharged.
func TestAddressPatternsRunOnlyWhereTheyMatter(t *testing.T) {
	spent := func(data []byte, host string) int64 {
		e, err := Build(Spec{INI: &INI{File: "hosts", Section: "g", Host: host, Key: "v"}})
		if err != nil {
			t.Fatal(err)
		}
		x := e.(entryExtractor)
		m := newMeter(len(data))
		match := newMatcher(&x.query, m)
		if err := x.read(data, m, func(en *iniEntry) error { match.wants(en); return match.err }); err != nil {
			t.Fatal(err)
		}
		return m.spent
	}
	long := []byte("[g]\nh[0:1]" + strings.Repeat("a", 64<<10) + ":22 v=1\n")
	if got := spent(long, ""); got > 8*int64(len(long)) {
		t.Errorf("no host: %d units for %d bytes", got, len(long))
	}
	if got := spent(long, "h0"); got > 8*int64(len(long)) {
		t.Errorf("a host of neither: %d units for %d bytes", got, len(long))
	}
	short := []byte("[g]\nh[0:1]" + strings.Repeat("a", 900) + ":22 v=1\n")
	if got := spent(short, "h0"+strings.Repeat("a", 900)); got < addressWork*900 {
		t.Errorf("a host the port decides: %d units, want the patterns charged", got)
	}
}

// Ordinary files of each dialect, the tests' own, short and so denser than
// real files, charge a few units a byte, under a quarter of workPerByte,
// without the floor: the budget leaves them room.
func TestWorkMeterLeavesRoomForOrdinaryFiles(t *testing.T) {
	for _, c := range []struct {
		body string
		spec Spec
	}{
		{setupCfg, ini("setup.cfg", "options", "install_requires")},
		{gitconfig, gitSpec(".gitconfig", "remote", ptr("origin"), "fetch")},
		{gitmodules, gitSpec(".gitmodules", "submodule", ptr("Docs"), "url")},
		{inventory, Spec{INI: &INI{File: "inventory/hosts", Section: "web", Host: "web3.example.com", Key: "x"}}},
		{"[testenv]\ndeps = pytest # pin\n  coverage\ncommands = pytest \\\n  -q {posargs}\n", ini("tox.ini", "testenv", "commands")},
		{"[PHP]\nmemory_limit = 128M ; note\nerror_log = \"/var/log/a;b.log\"\nextension=curl\nextension=gd\n", ini("php.ini", "PHP", "extension")},
		{"server.port=8080\nspring.datasource.url=jdbc:x \\\n  y\n# c\n", props("server.port")},
	} {
		e, err := Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(entryExtractor)
		data := []byte(c.body)
		m := newMeter(len(data) + len(x.query.section) + len(x.query.host) + len(x.query.key))
		match := newMatcher(&x.query, m)
		if err := x.read(data, m, func(en *iniEntry) error { match.wants(en); return match.err }); err != nil {
			t.Fatalf("%q: %v", clip(c.body), err)
		}
		if limit := workPerByte / 4 * int64(len(data)); m.spent > limit {
			t.Errorf("%q...: %d units for %d bytes, over %d", clip(c.body), m.spent, len(data), limit)
		}
	}
}

// Each position a range reaches is kept once: ranges that take one or two
// digits reach a position many ways, 2^20 for 20 ranges, and kept once
// each, the walk costs the ranges times the positions. The planted break is
// keeping each way.
func TestHostInKeepsEachPositionOnce(t *testing.T) {
	body := "[g]\nh" + strings.Repeat("[0:99]", 20) + " v=1\n"
	if got := iniValues(t, "hosts", body, Spec{INI: &INI{File: "hosts", Section: "g", Host: "h" + strings.Repeat("1", 30), Key: "v"}}); got != "1@2" {
		t.Errorf("got %q", clip(got))
	}
}

// Every reader charges each line it reads, a line that holds no entry too:
// 1000 comment lines, then an entry, cost at least lineWork a line in every
// dialect, and so do properties lines ended by a lone \r. The planted
// breaks are a line loop that charges nothing, a reader that charges the
// comments before an entry as one line, and one that counts \n alone.
func TestWorkMeterChargesEveryLine(t *testing.T) {
	for _, c := range []struct {
		comment, entry string
		spec           Spec
	}{
		{"# c\n", "[s]\nk = 1\n", ini("setup.cfg", "s", "k")},
		{"# c\n", "[s]\nk = 1\n", ini("tox.ini", "s", "k")},
		{"; c\n", "k = 1\n", ini("php.ini", "", "k")},
		{"# c\n", "[s]\nk = 1\n", gitSpec(".gitconfig", "s", nil, "k")},
		{"# c\n", "[g]\nh k=1\n", Spec{INI: &INI{File: "hosts", Section: "g", Key: "k"}}},
		{"# c\n", "k=1\n", props("k")},
		{"# c\r", "k=1\n", props("k")},
		{"k=1\r\r", "k=1\n", props("k")},
	} {
		e, err := Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(entryExtractor)
		data := []byte(strings.Repeat(c.comment, 1000) + c.entry)
		m := newMeter(len(data))
		match := newMatcher(&x.query, m)
		if err := x.read(data, m, func(en *iniEntry) error { match.wants(en); return match.err }); err != nil {
			t.Fatal(err)
		}
		// A lone \r ends a line in every dialect but git, which reads it
		// as white space, as git does.
		lines := 1000 * strings.Count(c.comment, "\n")
		if c.spec.INI == nil || c.spec.INI.File != ".gitconfig" {
			lines += 1000 * strings.Count(strings.ReplaceAll(c.comment, "\r\n", ""), "\r")
		}
		if want := int64(lines) * lineWork; m.spent < want {
			t.Errorf("%s %q: %d units for %d lines, want at least %d", x.file, c.comment, m.spent, lines, want)
		}
	}
}

// inPattern charges each literal it compares at each position it reached:
// a 17 digit range reaches 17 positions of a name of ones, and an 800 byte
// literal of ones after it is compared at each, 17 times 800 bytes more
// than an empty literal. The planted break charges the positions nothing.
func TestHostInChargesEachLiteralItCompares(t *testing.T) {
	name := []byte("h" + strings.Repeat("1", 1000))
	spent := func(lit int) int64 {
		m := newMeter(1 << 20)
		if _, err := hostIn([]byte("h[0:99999999999999999]"+strings.Repeat("1", lit)+"[0:9]"), name, m); err != nil {
			t.Fatal(err)
		}
		return m.spent
	}
	if d := spent(800) - spent(0); d < 17*800 {
		t.Errorf("an 800 byte literal costs %d units more, want at least %d", d, 17*800)
	}
}
