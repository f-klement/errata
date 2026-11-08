package ini

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// container is a spec of web.container in the systemd dialect, which
// reads it with Podman's grammar.
func container(section, key string) Spec { return ini("web.container", section, key) }

// Each case is what Podman 5.4.0's UnitFile.Parse reads from a Quadlet
// file, by its source (pkg/systemd/parser/unitfile.go). Planted breaks: a
// \r read as a line end; a space put for a \; an empty line ending a
// continued value; a \\ at a line's end read as an escaped \.
func TestQuadletReadsWhatPodmanReads(t *testing.T) {
	for _, c := range []struct {
		name, body string
		spec       Spec
		want       string
	}{
		{"a value", "[Container]\nImage=docker.io/x:1\n", container("Container", "Image"), "docker.io/x:1@2"},
		{"a \\r is a byte of the line", "[Container]\nX=a\rb\n", container("Container", "X"), "a\rb@2"},
		{"\\r\\n ends a line, the \\r trimmed", "[Container]\r\nX=a\r\nY=b\r\n", container("Container", "X"), "a@2"},
		{"a NUL is a byte of the line", "[Container]\nX=a\x00b\n", container("Container", "X"), "a\x00b@2"},
		{"\\ continues with no space", "[Container]\nX=a\\\n  b\n", container("Container", "X"), "ab@2"},
		{"the space before a \\ stays", "[Container]\nX=a \\\nb\n", container("Container", "X"), "a b@2"},
		{"\\\\ at the end continues too", "[Container]\nX=a\\\\\nb\n", container("Container", "X"), "a\\b@2"},
		{"an empty line does not end a value", "[Container]\nX=a\\\n\n# c\nb\n", container("Container", "X"), "ab@2"},
		{"a \\ on the last line goes", "[Container]\nX=a\\", container("Container", "X"), "a@2"},
		{"a \\ alone continues nothing", "[Container]\n\\\nX=1\n", container("Container", "X"), "1@3"},
		{"a reset", "[Container]\nX=1\nX=\nX=2\n", container("Container", "X"), "2@4"},
		{"a quote in a group's name", "[Con\"tainer]\nX=1\n", container("Con\"tainer", "X"), "1@2"},
		{"spaces after the ]", "[Container] \t\nX=1\n", container("Container", "X"), "1@2"},
		{"\\xa0 is white space byte by byte", "[Container]\nX\xa0=\xa0v\n", container("Container", "X"), "v@2"},
		{"a drop-in", "[Container]\nX=a\\\n\nb\n", ini("web.container.d/a.conf", "Container", "X"), "ab@2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniValues(t, c.spec.File, c.body, c.spec); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The files Quadlet refuses to generate a unit from. Planted breaks: a
// line with no = ignored, as systemd ignores it; a key before any group
// ignored; an empty group name read.
func TestQuadletRefusesWhatPodmanRefuses(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"a line with no =", "[Container]\nfoo\n", `web.container: line 2: "foo" is no key=value, group or comment, which Quadlet refuses`},
		{"= first", "[Container]\n=x\n", `web.container: line 2: "=x" is no key=value, group or comment`},
		{"text after the ]", "[Container] x\n", `web.container: line 1: "[Container] x" is no key=value`},
		{"a key before any group", "X=1\n[Container]\n", "web.container: line 1: a key before the first group, which Quadlet refuses"},
		{"an empty group", "[]\n", `web.container: line 1: the group name "" is empty or holds [ or a control character, which Quadlet refuses`},
		{"a [ in a group", "[a[b]\n", `web.container: line 1: the group name "a[b" is empty`},
		{"a control character in a group", "[a\x01b]\n", `web.container: line 1: the group name "a\x01b" is empty`},
		{"a key of white space alone", "[Container]\n\xa0=v\n", `web.container: line 2: the key of "\xa0=v" is white space alone, which Quadlet refuses`},
		{"a lone \\r inside a group's line", "[Container]\rX=1\n", "web.container: line 1: a key before the first group, which Quadlet refuses"},
		{"a continued line's start", "[Container]\nfoo\\\nbar\n", `web.container: line 2: "foobar" is no key=value`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := iniError(t, "web.container", c.body, container("Container", "X")); !strings.HasPrefix(got, c.want) {
				t.Errorf("error %q, want %q", got, c.want)
			}
		})
	}
}

// Podman splits a Quadlet Environment= with SplitRelax, SplitUnquote and
// SplitCUnescape (split.go). Planted breaks: an open quote refused; an
// escape written as a byte; the words before a bad escape kept; the words
// after an empty word kept.
func TestPodmanWords(t *testing.T) {
	for in, want := range map[string]string{
		`A=1 "B=2 3"`:  "A=1|B=2 3",
		`A="x`:         "A=x",
		`A=x\`:         "A=x",
		`A=\xff\101`:   "A=ÿA",
		`A=\ud800`:     "A=\ufffd",
		`A=\U0010FFFF`: "A=\U0010ffff",
		`A=1 B=\q`:     "",
		`A=1 B=\x00`:   "",
		`A=\U00110000`: "",
		`A=1 "" B=2`:   "A=1",
		"A=1\x00 B=2":  "A=1",
		`  `:           "",
		`'A=1'\s'B'`:   "A=1 B",
		`A=1	B=2`:      "A=1|B=2",
		`\U0000FDD0`:   "\ufdd0",
		`A=\s`:         "A= ",
		`A=\u0000`:     "",
	} {
		var got []string
		if err := podmanWords([]byte(in), nil, func(w []byte) error { got = append(got, string(w)); return nil }); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, "|") != want {
			t.Errorf("%s: got %q, want %q", in, strings.Join(got, "|"), want)
		}
	}
}

// The systemd kind reads a Quadlet [Container] Environment= as Podman
// splits it, and a [Service] one, which systemd reads in the unit Quadlet
// writes, as systemd does. A word with % or $ is not literal, since the
// value reaches the command line systemd expands. Planted break: the
// [Container] split as systemd splits it.
func TestSystemdReadsQuadletEnvironment(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"web.container": "[Container]\nEnvironment=PORT=1 X=\"open\nEnvironment=PORT=2 Y=\\q\nEnvironment=PORT=$P 1A=3\n" +
			"[Service]\nEnvironment=PORT=4 X=\"open\nEnvironment=PORT=5\n",
	})
	for section, want := range map[string]string{
		"Container": "1@web.container:2; $P@web.container:4*",
		"Service":   "4@web.container:6; 5@web.container:7",
	} {
		if got := systemdValues(t, root, sd("web.container", section, "Environment", "PORT")); got != want {
			t.Errorf("%s: got %s, want %s", section, got, want)
		}
	}
}

// UnsetEnvironment= unsets the variable after every other environment is
// merged, the last assignment of a name winning, as strv_env_delete does:
// a word that is the name unsets it; NAME=VALUE unsets it when VALUE is
// its last value (g, not b), and makes it unknown when an
// EnvironmentFile= may set another (h); a reset drops the words before
// it; a word with a specifier marks the values. In a Quadlet [Container]
// Podman takes Environment= over EnvironmentFile=, so the value stays
// literal, and UnsetEnvironment=, which Quadlet refuses there, is not
// read. A % marks only in the name, or in a word that names the variable
// (j, m), not in OTHER=%i (k). Planted breaks: the name not matched;
// NAME=VALUE matched against every value; the reset not dropping the
// words; the [Container] marked; a % anywhere marking.
func TestSystemdReadsUnsetEnvironment(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"a.service":     "[Service]\nUnsetEnvironment=PORT\nEnvironment=PORT=1\n",
		"b.service":     "[Service]\nEnvironment=PORT=1\nEnvironment=PORT=2\nUnsetEnvironment=PORT=1 X-Y\n",
		"g.service":     "[Service]\nEnvironment=PORT=1\nEnvironment=PORT=2\nUnsetEnvironment=PORT=2\n",
		"h.service":     "[Service]\nEnvironment=PORT=1\nEnvironmentFile=/etc/a.env\nUnsetEnvironment=PORT=1\n",
		"web.container": "[Container]\nEnvironment=PORT=1\nEnvironmentFile=a.env\nUnsetEnvironment=PORT\n",
		"i.service":     "[Service]\nEnvironment=PORT=1\nEnvironmentFile=a.env\n",
		"c.service":     "[Service]\nUnsetEnvironment=PORT\nUnsetEnvironment=\nEnvironment=PORT=1\n",
		"d.service":     "[Service]\nEnvironment=PORT=1\nUnsetEnvironment=PORT%i\n",
		"e.service":     "[Service]\nEnvironment=PORT=1\nUnsetEnvironment=PORTS \"open\n",
		"f.service":     "[Service]\nEnvironment=PORT=1\n[Install]\nUnsetEnvironment=PORT\n",
		"j.service":     "[Service]\nEnvironment=PORT=1\nUnsetEnvironment=OTHER=%i P%i\n",
		"k.service":     "[Service]\nEnvironment=PORT=1\nUnsetEnvironment=OTHER=%i\n",
		"m.service":     "[Service]\nEnvironment=PORT=1\nUnsetEnvironment=PORT=%I\n",
	})
	for file, want := range map[string]string{
		"a.service":     "",
		"b.service":     "1@b.service:2; 2@b.service:3",
		"g.service":     "",
		"h.service":     "1@h.service:2*",
		"web.container": "1@web.container:2",
		"i.service":     "1@i.service:2*",
		"c.service":     "1@c.service:4",
		"d.service":     "1@d.service:2*",
		"e.service":     "1@e.service:2",
		"f.service":     "1@f.service:2",
		"j.service":     "1@j.service:2*",
		"k.service":     "1@k.service:2",
		"m.service":     "1@m.service:2*",
	} {
		section := "Service"
		if file == "web.container" {
			section = "Container"
		}
		if got := systemdValues(t, root, sd(file, section, "Environment", "PORT")); got != want {
			t.Errorf("%s: got %s, want %s", file, got, want)
		}
	}
}

// systemd's UTF-8 refuses the noncharacters Go's utf8.Valid reads: U+FDD0
// to U+FDEF, and U+FFFE and U+FFFF of each plane. Planted break: the
// range left out of unicharValid.
func TestSystemdRefusesNoncharacters(t *testing.T) {
	for _, r := range []rune{0xfdd0, 0xfdef, 0xfffe, 0x1ffff} {
		if systemdUTF8([]byte(string(r))) || unicharValid(uint32(r)) {
			t.Errorf("U+%04X read as valid", r)
		}
	}
	for _, r := range []rune{0xfdcf, 0xfdf0, 0xfffd, 'a'} {
		if !systemdUTF8([]byte(string(r))) {
			t.Errorf("U+%04X refused", r)
		}
	}
	got := iniError(t, "a.service", "[Service]\nX=\ufdd0\n", unit("Service", "X"))
	if want := "a.service: line 2: the line is not UTF-8, which systemd refuses"; got != want {
		t.Errorf("error %q, want %q", got, want)
	}
	root := extracttest.Tracked(t, map[string]string{"a.service": "[Service]\nEnvironment=PORT=1 PORT=\\U0000FDD0\n", "a.env": "PORT=\ufdd0\n"})
	if got := systemdValues(t, root, sd("a.service", "Service", "Environment", "PORT")); got != "1@a.service:2" {
		t.Errorf("Environment=: got %s", got)
	}
	_, err := extracttest.Run(t, BuildSystemd, sd("a.env", "", "", "PORT"), root)
	extracttest.WantError(t, err, `a.env: line 1: the value of "PORT" is not UTF-8, which systemd refuses`)
}

// A unit name of 256 bytes or more is refused, as UNIT_NAME_MAX refuses
// it, and the walk of the drop-in directories of the longest name systemd
// takes lists a number of names linear in its dashes, each at most as
// long as the name: a name of twice the dashes costs at most three times
// the work, where a walk that lists a name again, d*d/2 names, costs eight.
// Planted breaks: the length check dropped; the walk not skipping a name
// it met before.
func TestDropInDirsAreBounded(t *testing.T) {
	name := func(n int) string { return strings.Repeat("a-", n) + "a@x.service" }
	_, err := dropInDirs(strings.Repeat("a", 246)+"@x.service", nil)
	extracttest.WantError(t, err, "is 256 bytes, and systemd reads a name of at most 255")
	spent := func(n int) int64 {
		m := &core.Meter{Budget: 1 << 40}
		if _, err := dropInDirs(name(n), m); err != nil {
			t.Fatal(err)
		}
		return m.Spent
	}
	small, large := spent(60), spent(120)
	if large > 3*small {
		t.Errorf("the walk of %d dashes cost %d, of %d dashes %d: more than linear", 60, small, 120, large)
	}
	root := extracttest.Tracked(t, map[string]string{"q/a.container": "[Container]\nImage=" + strings.Repeat("a-", 130) + "a@x.image\n"})
	_, err = extracttest.Run(t, BuildSystemd, sd("q/a.container", "Container", "Image", ""), root)
	extracttest.WantError(t, err, "and systemd reads a name of at most 255")
}

// A .network file is Quadlet's, but directly in a directory named network
// outside containers/systemd, where systemd-networkd reads its files, with
// no subdirectory. Planted breaks: every .network read as Quadlet's; any
// ancestor named network taken; containers/systemd not checked.
func TestNetworkFiles(t *testing.T) {
	for file, want := range map[string]unitClass{
		"q/web.network": quadletUnit, "etc/systemd/network/10-eth.network": systemdUnit,
		"etc/systemd/network/10-eth.network.d/a.conf": systemdDropIn, "q/web.network.d/a.conf": quadletDropIn,
		"etc/systemd/network/network.d/a.conf": systemdDropIn, "q/network.d/a.conf": quadletDropIn,
		"container.d/a.conf": quadletDropIn, "service.d/a.conf": systemdDropIn,
		"containers/systemd/network/web.network": quadletUnit, "network/podman/web.network": quadletUnit,
		"etc/systemd/network/sub/10.network": quadletUnit, "roles/networkd/files/10-eth.network": quadletUnit,
		"quadlet/network/web.network": systemdUnit, "x/containers/systemd/network/web.network.d/a.conf": quadletDropIn,
	} {
		if got := unitClassOf(file); got != want {
			t.Errorf("%s: %d, want %d", file, got, want)
		}
	}
}

// The words of UnsetEnvironment= are kept in maps, so a unit of n values
// and n words reads in time linear in n: four times the words and values
// take at most ten times as long, the fastest of three reads each,
// where a check of each value against each word takes sixteen. Planted
// breaks: the nested loop of round 1; the lookup of round 2 back inside
// the loop over the values, which hashes the last value for each.
func TestUnsetEnvironmentIsLinear(t *testing.T) {
	body := func(n int) string {
		return "[Service]\nEnvironment=" + strings.Repeat("PORT=1 ", n) + "\nUnsetEnvironment=" + strings.Repeat("Y ", n) + "\n"
	}
	// The review's shape: a last value of 10n bytes, and nine NAME=VALUE
	// words that name the variable, which a map holds past its small form,
	// so each lookup hashes the whole value.
	long := func(n int) string {
		return "[Service]\nEnvironment=" + strings.Repeat("PORT=a ", n) + "\nEnvironment=PORT=" + strings.Repeat("v", 10*n) +
			"\nUnsetEnvironment=PORT=b1 PORT=b2 PORT=b3 PORT=b4 PORT=b5 PORT=b6 PORT=b7 PORT=b8 PORT=b9\n"
	}
	root := extracttest.Tracked(t, map[string]string{"s.service": body(8000), "l.service": body(32000), "sl.service": long(8000), "ll.service": long(32000)})
	fastest := func(file string) time.Duration {
		x, err := BuildSystemd(sd(file, "Service", "Environment", "PORT"))
		if err != nil {
			t.Fatal(err)
		}
		best := time.Duration(1 << 62)
		for range 3 {
			runtime.GC()
			start := time.Now()
			if _, err := x.Extract(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	for _, pair := range [][2]string{{"s.service", "l.service"}, {"sl.service", "ll.service"}} {
		small, large := fastest(pair[0]), fastest(pair[1])
		if large > 10*small {
			t.Errorf("%s read in %v, %s, four times its size, in %v: more than linear", pair[0], small, pair[1], large)
		}
	}
}
