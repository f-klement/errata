package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
)

// texts are the texts of vs.
func texts(vs []check.Value) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Text)
	}
	return out
}

// extract.Build returns a plugin's extractor for an entry of its kind, as
// it returns a built in reader's, and the extractor's values are the
// plugin's, each at its file and line. Planted breaks: Spec.With leaving
// the plugin's kind out, which Build refused as an entry of no kind; the
// extractor sending no files, which found no value.
func TestExtractBuildReturnsThePlugin(t *testing.T) {
	root := newRepo(t, map[string]string{"a.txt": "x\nport: 8080\n", "b.txt": "port: 9090\n"}, "fake")
	set := openSet(t, root)
	s := entries(t, set, "fake", `{"files": ["*.txt"], "word": "port:"}`)[0]
	x, err := extract.Build(extract.Spec{}.With(set.Kind(context.Background(), "fake"), s))
	if err != nil {
		t.Fatal(err)
	}
	got, err := x.Extract(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []check.Value{{Text: "8080", At: check.Location{File: "a.txt", Line: 2}}, {Text: "9090", At: check.Location{File: "b.txt", Line: 1}}}
	if !slices.Equal(texts(got), texts(want)) || got[0].At != want[0].At || got[1].At != want[1].At {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Every entry of a plugin's kind goes in one build request, however many
// there are. The fake writes a line for each build request to the file an
// entry's log tag names. Planted break: Build sending one request for each
// entry, which wrote three lines.
func TestEveryEntryOfAKindIsBuiltInOneRequest(t *testing.T) {
	root := newRepo(t, nil, "fake")
	log := filepath.Join(t.TempDir(), "builds")
	set := openSet(t, root)
	entries(t, set, "fake",
		`{"file": "a", "word": "a", "tags": {"log": "`+log+`"}}`,
		`{"file": "b", "word": "b"}`,
		`{"file": "c", "word": "c"}`)
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "3\n" {
		t.Fatalf("the fake logged %q, %v; want one request of 3 entries", data, err)
	}
}

// A plugin starts with only the Go runtime's variables errata has: a
// token in errata's environment, PATH and HOME do not reach it. The fake
// returns its whole environment. Planted breaks: Env set to os.Environ();
// GOGC left out of the allowlist.
func TestAPluginSeesOnlyTheAllowlist(t *testing.T) {
	t.Setenv("ERRATA_TEST_TOKEN", "a-ci-token")
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("GOGC", "77")
	unset(t, "GOMAXPROCS", "GOMEMLIMIT")
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	got, err := extractOne(t, root, "fake", `{"file": "a", "word": "x", "mode": "env"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(texts(got), []string{"GOGC=77"}) {
		t.Fatalf("the plugin saw %q, want only GOGC=77", texts(got))
	}
}

// With none of the allowlist set, a plugin starts with an empty
// environment. Planted break: environ adding PATH, which the fake saw.
func TestAPluginWithNoAllowlistedVariableHasNone(t *testing.T) {
	unset(t, "GOMAXPROCS", "GOMEMLIMIT", "GOGC")
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	got, err := extractOne(t, root, "fake", `{"file": "a", "word": "x", "mode": "env"}`)
	if err != nil || len(got) != 0 {
		t.Fatalf("the plugin saw %q, %v; want no variable", texts(got), err)
	}
}

// unset unsets each of names for the test, restoring it after.
func unset(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
}

// On Windows the allowlist adds SYSTEMROOT, and nowhere else; each
// variable passes only when errata has it. Planted break: SYSTEMROOT added
// on every OS.
func TestEnvironAddsSystemRootOnWindows(t *testing.T) {
	has := map[string]string{"GOGC": "50", "SYSTEMROOT": `C:\Windows`, "PATH": "/bin"}
	lookup := func(n string) (string, bool) { v, ok := has[n]; return v, ok }
	if got := environ("windows", lookup); !slices.Equal(got, []string{"GOGC=50", `SYSTEMROOT=C:\Windows`}) {
		t.Fatalf("windows: %q", got)
	}
	if got := environ("linux", lookup); !slices.Equal(got, []string{"GOGC=50"}) {
		t.Fatalf("linux: %q", got)
	}
	if got := environ("linux", func(string) (string, bool) { return "", false }); got == nil || len(got) != 0 {
		t.Fatalf("none: %#v, want an empty environment that is not nil", got)
	}
}

// A plugin runs in an empty directory of its own, not errata's. Planted
// break: Dir left empty, so the plugin ran in errata's working directory.
func TestAPluginRunsInAnEmptyDirectory(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	got, err := extractOne(t, root, "fake", `{"file": "a", "word": "x", "mode": "cwd"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Text != "0" || strings.HasPrefix(got[1].Text, root) {
		t.Fatalf("the plugin ran in %s holding %s entries", got[1].Text, got[0].Text)
	}
}

// A build whose checksum is not the lock's is not run, and the entry's
// extractor says why, naming both sums. The fake is put at the path of the
// sum the lock pins, as a build changed after its install would be.
// Planted break: run skipping verify, which ran the build and read the
// file.
func TestABuildWhoseChecksumDiffersDoesNotRun(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"})
	zero := strings.Repeat("0", 64)
	copyFake(t, BuildPath(filepath.Join(root, VendorDir), "fake", "0.1.0", Platform(), zero))
	writeFile(t, root, LockFile, pinLine("fake", zero)+"\n")
	_, err := extractOne(t, root, "fake", `{"file": "a", "word": "x"}`)
	if err == nil || !strings.Contains(err.Error(), "has the checksum sha256:"+sumOf(t, fakeBin)+", and errata.lock pins sha256:"+zero+", so errata does not run it") {
		t.Fatalf("got %v, want the checksum refusal", err)
	}
}

// A build that is a symbolic link is refused in the vendored directory,
// even when the link's target is the build the lock pins. Planted break:
// verify reading the path with Stat, which follows the link and ran the
// target.
func TestABuildThatIsALinkIsRefused(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"})
	mkdir(t, filepath.Join(root, VendorDir))
	linkRefused(t, root, filepath.Join(root, VendorDir))
}

// A build that is a symbolic link is refused in the cache too, which
// shares the vendored directory's checks.
func TestABuildThatIsALinkIsRefusedInTheCache(t *testing.T) {
	dir := xdgCache(t)
	linkRefused(t, newRepo(t, map[string]string{"a": "x\n"}), dir)
}

// xdgCache points XDG_CACHE_HOME at a new directory and returns errata's
// cache below it. Linux reads the cache from XDG_CACHE_HOME, so there a
// cache elsewhere fails the test; another OS skips it.
func xdgCache(t *testing.T) string {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	dir, err := CacheDir()
	if want := filepath.Join(cache, "errata", "plugins"); err != nil || dir != want {
		if runtime.GOOS == "linux" {
			t.Fatalf("CacheDir() = %s, %v, want %s", dir, err, want)
		}
		t.Skipf("the cache is not under XDG_CACHE_HOME on this OS: %s, %v", dir, err)
	}
	return dir
}

// linkRefused places a link to the fake at its build path below base and
// checks that the repository at root refuses it.
func linkRefused(t *testing.T, root, base string) {
	t.Helper()
	sum := sumOf(t, fakeBin)
	path := BuildPath(base, "fake", "0.1.0", Platform(), sum)
	mkdir(t, filepath.Dir(path))
	if err := os.Symlink(fakeBin, path); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	writeFile(t, root, LockFile, pinLine("fake", sum)+"\n")
	if _, err := extractOne(t, root, "fake", `{"file": "a", "word": "x"}`); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("%s: got %v, want the link refused", base, err)
	}
}

// A build below a linked directory is refused, though the file itself is
// a regular file at the sum the lock pins: a directory inside the vendored
// directory, and the repository's .errata, which holds it, alike. Planted
// breaks: verify skipping noLink, which ran the file the link led to;
// noLink starting below .errata.
func TestABuildBehindALinkedDirectoryIsRefused(t *testing.T) {
	// Each link and the base below its target that holds the build.
	for linked, base := range map[string]string{filepath.Join(VendorDir, "fake"): "", ".errata": "plugins"} {
		root := newRepo(t, map[string]string{"a": "x\n"})
		outside := t.TempDir()
		sum := placeFake(t, filepath.Join(outside, base), "fake")
		// .errata/plugins/fake links to the fake's own directory; .errata
		// links to the directory that holds plugins.
		target := filepath.Join(outside, "fake")
		if base != "" {
			target = outside
		}
		mkdir(t, filepath.Dir(filepath.Join(root, linked)))
		if err := os.Symlink(target, filepath.Join(root, linked)); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		writeFile(t, root, LockFile, pinLine("fake", sum)+"\n")
		want := "is below " + filepath.Join(root, linked) + ", which is a symbolic link"
		if _, err := extractOne(t, root, "fake", `{"file": "a", "word": "x"}`); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", linked, err, want)
		}
	}
}

// copyFake writes the fake plugin at path.
func copyFake(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(fakeBin)
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// A build is hashed once a run, at its first request, not at every one.
// Planted break: run hashing at every request, which refused the build
// changed after the first.
func TestABuildIsHashedOnceARun(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x 1\n"}, "fake")
	set := openSet(t, root)
	s := entries(t, set, "fake", `{"file": "a", "word": "x"}`)[0]
	x, err := set.Kind(context.Background(), "fake").Build(s)
	if err != nil {
		t.Fatal(err)
	}
	path := BuildPath(filepath.Join(root, VendorDir), "fake", "0.1.0", Platform(), sumOf(t, fakeBin))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\x00"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Extract(context.Background(), root); err != nil {
		t.Fatalf("the second request was refused: %v", err)
	}
}

// A plugin the lock pins and nothing holds is not installed: its entry
// loads and its extractor names the plugin and where errata looked.
// Planted break: unusable's extractor returning no error, which found no
// value.
func TestAPluginNotInstalledNamesItself(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"})
	writeFile(t, root, LockFile, pinLine("csv", strings.Repeat("a", 64))+"\n")
	mkdir(t, filepath.Join(root, VendorDir))
	_, err := extractOne(t, root, "csv", `{"file": "a", "anything": [1]}`)
	want := "plugin errata-ext-csv 0.1.0 is not installed: errata looks for it at " + BuildPath(filepath.Join(root, VendorDir), "csv", "0.1.0", Platform(), strings.Repeat("a", 64))
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}

// A lock that pins no build for this platform names it. Planted break:
// load looking the sum up under another platform's name.
func TestALockWithNoBuildForThePlatform(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"})
	other := Platforms[0]
	if other == Platform() {
		other = Platforms[1]
	}
	writeFile(t, root, LockFile, "csv 0.1.0 "+other+" sha256:"+strings.Repeat("a", 64)+"\n")
	_, err := extractOne(t, root, "csv", `{"file": "a"}`)
	if err == nil || !strings.Contains(err.Error(), "pins no build of plugin errata-ext-csv 0.1.0 for "+Platform()) {
		t.Fatalf("got %v", err)
	}
}

// The repository's vendored directory takes the place of the cache: with
// it, a build only the cache holds is not installed, and errata names the
// path in the vendored directory, which holds the sum as the cache's does;
// without it, errata runs the cache's. Planted breaks: base always the cache; base the
// vendored directory whether or not it is there.
func TestTheVendoredDirectoryTakesThePlaceOfTheCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := xdgCache(t)
	root := newRepo(t, map[string]string{"a": "x 1\n"})
	sum := placeFake(t, dir, "fake")
	writeFile(t, root, LockFile, pinLine("fake", sum)+"\n")
	if got, err := extractOne(t, root, "fake", `{"file": "a", "word": "x"}`); err != nil || len(got) != 1 {
		t.Fatalf("from the cache: %v, %v", got, err)
	}
	mkdir(t, filepath.Join(root, VendorDir))
	want := BuildPath(filepath.Join(root, VendorDir), "fake", "0.1.0", Platform(), sum)
	if _, err := extractOne(t, root, "fake", `{"file": "a", "word": "x"}`); err == nil || !strings.HasSuffix(err.Error(), "errata looks for it at "+want) {
		t.Fatalf("with a vendored directory: %v, want not installed at %s", err, want)
	}
}

// A plugin that speaks only later versions is refused with both sides and
// what to do. Planted break: noCommonVersion skipped for a reply that
// lists versions, which then failed to decode with another message.
func TestAPluginOfAnotherProtocol(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "newer")
	_, err := extractOne(t, root, "newer", `{"file": "a"}`)
	want := "plugin errata-ext-newer 0.1.0 speaks protocol 2, 3, and this errata speaks 1: install a release of errata-ext-newer for protocol 1, or upgrade errata"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
}

// A describe reply that names another plugin, or holds a member protocol 1
// does not define, is refused. Planted breaks: the name check dropped;
// DescribeReply decoded without refusing an unknown member.
func TestADescribeReplyIsCheckedStrictly(t *testing.T) {
	for name, want := range map[string]string{
		"liar":   `names itself "other" in its describe reply, not liar`,
		"broken": `unknown field "extra"`,
	} {
		root := newRepo(t, map[string]string{"a": "x\n"}, name)
		if _, err := extractOne(t, root, name, `{"file": "a"}`); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", name, err, want)
		}
	}
}

// A describe request that runs past its timeout makes the kind's rules
// unknown with the timeout named. Planted break: describe run with no
// timeout, which waited for the fake's minute.
func TestADescribeThatHangsTimesOut(t *testing.T) {
	defer func(d time.Duration) { describeTimeout = d }(describeTimeout)
	describeTimeout = 200 * time.Millisecond
	root := newRepo(t, map[string]string{"a": "x\n"}, "hang")
	if _, err := extractOne(t, root, "hang", `{"file": "a"}`); err == nil || !strings.Contains(err.Error(), "did not finish within 200ms") {
		t.Fatalf("got %v", err)
	}
}

// A build reply with fewer results than entries fails every entry. Planted
// break: the count check dropped, which indexed past the results.
func TestABuildReplyOfTheWrongLengthFailsEveryEntry(t *testing.T) {
	root := newRepo(t, nil, "short")
	for _, s := range entries(t, openSet(t, root), "short", `{"file": "a", "word": "a"}`, `{"file": "b", "word": "b"}`) {
		if s.res.failed == nil || !strings.Contains(s.res.failed.Error(), "it holds 0 results for 2 entries") {
			t.Fatalf("got %v", s.res.failed)
		}
	}
}

// A build reply's spec that writes a member twice, or a string for an
// integer, fails the entry: errata would send both members, and a number
// held in a string is no integer. Planted breaks: members keeping a
// repeated name; intValue reading a string as a json.Number.
func TestABuildReplysSpecIsRefusedWhenMalformed(t *testing.T) {
	root := newRepo(t, nil, "fake")
	for mode, want := range map[string]string{
		"dupspec": `its spec: the member "word" is written twice`,
		"strint":  `its spec's limit: an integer is required, not a string`,
	} {
		s := entries(t, openSet(t, root), "fake", `{"file": "a", "word": "w", "mode": "`+mode+`"}`)[0]
		if s.res.failed == nil || !strings.Contains(s.res.failed.Error(), want) {
			t.Errorf("%s: got %v, want %s", mode, s.res.failed, want)
		}
	}
}

// A spec the plugin refuses is a config error, its message quoted.
// Planted break: a refused spec built as an unusable extractor, which
// loaded.
func TestASpecThePluginRefusesIsAConfigError(t *testing.T) {
	root := newRepo(t, nil, "fake")
	set := openSet(t, root)
	s := entries(t, set, "fake", `{"file": "a", "word": "w", "mode": "refuse"}`)[0]
	_, err := set.Kind(context.Background(), "fake").Build(s)
	if err == nil || err.Error() != `fake: plugin errata-ext-fake 0.1.0 refuses the entry: "the word w is refused\nin rule r"` {
		t.Fatalf("got %v", err)
	}
}

// The spec a build reply writes out is the entry's place and what errata
// list prints: the fake writes loud out, false by default, so an entry
// that leaves it out and one that writes it false are one place.
// Planted break: place keeping the fields as written.
func TestTheBuiltSpecIsThePlace(t *testing.T) {
	root := newRepo(t, nil, "fake")
	k := openSet(t, root).Kind(context.Background(), "fake")
	ss := entries(t, openSet(t, root), "fake", `{"file": "./a", "word": "w", "max_bytes": 10}`, `{"file": "a", "word": "w", "loud": false}`)
	a := compact(t, extract.Spec{}.With(k, ss[0]).Place())
	b := compact(t, extract.Spec{}.With(k, ss[1]).Place())
	if a != b || a != `{"fake":{"file":"a","word":"w","loud":false}}` {
		t.Fatalf("places %s and %s", a, b)
	}
	if r := compact(t, extract.Spec{}.With(k, ss[0]).Resolved()); r != `{"fake":{"file":"./a","max_bytes":10,"word":"w","loud":false}}` {
		t.Fatalf("resolved %s", r)
	}
}

// compact is v as compact JSON.
func compact(t *testing.T, v json.Marshaler) string {
	t.Helper()
	raw, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Each failure of an extract request makes the extractor fail with what
// went wrong: the plugin's exit status and the end of its stderr quoted,
// its error quoted, a stream past its bound, a reply that is not one JSON
// value, with the end of its stderr when it exited 0; a mapping that
// holds a key twice. Planted breaks: failed leaving stderr out of the
// message; refuse leaving it out; the stderr bound left at 0, which held
// stderr to stdout's bound and let the plugin run to its timeout; keysOf
// keeping a repeated key.
func TestAnExtractThatFailsSaysHow(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	for _, c := range []struct{ raw, want string }{
		{`{"file": "a", "word": "x", "mode": "exit"}`, `plugin errata-ext-fake 0.1.0 failed: exit status 3; its stderr ends "first line\nboom\n"`},
		{`{"file": "a", "word": "x", "mode": "error"}`, `plugin errata-ext-fake 0.1.0 could not read its files: "cannot read\nthis"`},
		{`{"file": "a", "word": "x", "mode": "stderr"}`, `plugin errata-ext-fake 0.1.0 wrote more than 1048576 bytes to stderr, the bound on it, so errata stopped it`},
		{`{"file": "a", "word": "x", "mode": "raw", "names": ["{\"values\": [], \"x\": 1}"]}`, `unknown field "x"`},
		{`{"file": "a", "word": "x", "mode": "raw", "names": ["{\"values\": null}"]}`, `holds null`},
		{`{"file": "a", "word": "x", "mode": "raw", "names": ["{\"values\": []} {}"]}`, `more than one JSON value`},
		{`{"file": "a", "word": "x", "mode": "raw", "names": ["{\"error\": \"a\", \"no_value\": \"b\"}"]}`, `exactly one of values, error and no_value`},
		{`{"file": "a", "word": "x", "mode": "raw", "names": ["", "no config\n"]}`, `replied to extract with a reply errata refuses: the message is empty; its stderr ends "no config\n"`},
		{`{"file": "a", "word": "x", "mode": "dupkey"}`, `a mapping holds the key "word" twice`},
	} {
		if _, err := extractOne(t, root, "fake", c.raw); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %s", c.raw, err, c.want)
		}
	}
}

// The stdout bound stops a plugin at the byte past it: 6 times max_bytes,
// 160 bytes and 6 times the longest path for each of MaxValues values, and
// 64 KiB. Planted break: the bound left at 0, which held all of it.
func TestTheStdoutBoundStopsThePlugin(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	bound := 6*100 + 100_000*(160+6*1) + 64<<10
	_, err := extractOne(t, root, "fake", `{"file": "a", "max_bytes": 100, "word": "x", "mode": "stdout", "limit": `+itoa(bound+1)+`}`)
	if err == nil || !strings.Contains(err.Error(), "wrote more than "+itoa(bound)+" bytes to stdout") {
		t.Fatalf("got %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// A no_value reply is no value, with the plugin's reason quoted. Planted
// break: no_value read as an error.
func TestANoValueReplyIsNoValue(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	_, err := extractOne(t, root, "fake", `{"file": "a", "word": "x", "mode": "novalue"}`)
	var none *check.NoValue
	if !errors.As(err, &none) || none.Reason != `plugin errata-ext-fake 0.1.0 says "nothing here"` {
		t.Fatalf("got %v", err)
	}
}

// Each value is checked: its file is one of the request's, its line is in
// that file, it is marked not literal only when the description allows,
// and its key and condition count to max_bytes. Planted breaks: each check
// dropped in turn; the key and condition left out of the count.
func TestTheValuesOfAReplyAreChecked(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x 12\n"}, "fake")
	for _, c := range []struct{ raw, want string }{
		{`{"file": "a", "word": "x", "mode": "badfile"}`, `its file "elsewhere" is none of the request's files`},
		{`{"file": "a", "word": "x", "mode": "badline"}`, `its line 9999 is not in a, which has 2 lines`},
		{`{"file": "a", "word": "x", "mode": "notliteral"}`, `marked not literal`},
		{`{"file": "a", "max_bytes": 10, "word": "x", "tags": {"key": "kkkkkkkkk"}}`, `more than 10 bytes in all, counting each key and condition`},
		{`{"file": "a", "max_bytes": 10, "word": "x", "tags": {"condition": "ccccccccc"}}`, `more than 10 bytes in all, counting each key and condition`},
	} {
		if _, err := extractOne(t, root, "fake", c.raw); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %s", c.raw, err, c.want)
		}
	}
	got, err := extractOne(t, root, "fake", `{"file": "a", "max_bytes": 10, "word": "x", "tags": {"key": "kkkk", "condition": "cccc"}}`)
	if err != nil || got[0].Key != "kkkk" || !got[0].HasKey || got[0].Condition != "cccc" {
		t.Fatalf("within the bound: %+v, %v", got, err)
	}
}

// A record's value is handed over whole with the description's fields,
// and its text is the record shown. Planted break: shaped leaving the
// text empty.
func TestARecordValueIsWhole(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x 12\n"}, "fake")
	got, err := extractOne(t, root, "fake", `{"file": "a", "word": "x", "mode": "record"}`)
	if err != nil || got[0].Data == nil || got[0].Data.Field("word").Text != "12" || got[0].Text != `{"word": "12"}` {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// An extractor whose description reads another's values gets them in its
// request, and one whose value is not UTF-8 is refused before the plugin
// starts. Planted breaks: values sent empty; the UTF-8 check dropped.
func TestAPluginReadsTheValuesItsDescriptionNames(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	set := openSet(t, root)
	s := entries(t, set, "fake", `{"file": "a", "word": "x", "mode": "reads"}`)[0]
	x, err := set.Kind(context.Background(), "fake").Build(s)
	if err != nil {
		t.Fatal(err)
	}
	other := []check.Value{{Text: "v1", At: check.Location{File: "a", Line: 1}}}
	got, err := x.ExtractWith(context.Background(), root, map[string][]check.Value{"other": other})
	if err != nil || !slices.Equal(texts(got), []string{"v1"}) {
		t.Fatalf("got %q, %v", texts(got), err)
	}
	other[0].Text = "\xff"
	if _, err := x.ExtractWith(context.Background(), root, map[string][]check.Value{"other": other}); err == nil || !strings.Contains(err.Error(), "not UTF-8") {
		t.Fatalf("got %v, want the UTF-8 refusal", err)
	}
}

// A files entry that names no file returns no value without starting the
// plugin, and a set's files hold at most max_bytes in all. Planted breaks:
// the plugin started for no file, which failed on the fake's first file;
// readSet reading each file to max_bytes.
func TestAFileSetReadsAsABuiltInReaderDoes(t *testing.T) {
	root := newRepo(t, map[string]string{"a.txt": "123456", "b.txt": "123456"}, "fake")
	if got, err := extractOne(t, root, "fake", `{"files": ["*.none"], "word": "x"}`); err != nil || len(got) != 0 {
		t.Fatalf("no file: %v, %v", got, err)
	}
	if _, err := extractOne(t, root, "fake", `{"files": ["a.txt", "b.txt"], "max_bytes": 10, "word": "x"}`); err == nil || !strings.Contains(err.Error(), "hold at most max_bytes, 10, in all") {
		t.Fatalf("past max_bytes: %v", err)
	}
}

// A cancelled run says so, and is not read as the plugin failing. Planted
// break: failed passing over Cancelled, which read the kill as a failure.
func TestACancelledExtractSaysCancelled(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "fake")
	set := openSet(t, root)
	s := entries(t, set, "fake", `{"file": "a", "word": "x", "mode": "sleep"}`)[0]
	x, _ := set.Kind(context.Background(), "fake").Build(s)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := x.Extract(ctx, root); err == nil || !strings.HasPrefix(err.Error(), "cancelled: errata's run stopped before plugin errata-ext-fake 0.1.0 finished") {
		t.Fatalf("got %v", err)
	}
}

// mkdir makes dir and its parents.
func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
