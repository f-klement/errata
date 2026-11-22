package install

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin"
)

// An install by name downloads this platform's build, checks it against
// the release's checksums file, keeps it in the cache under its sum, read
// only and executable, and pins every platform's sum the file lists; a run
// then starts it. Planted breaks: fetchRelease pinning this platform's sum
// alone; keep renaming nothing into the cache; fetch giving the build mode
// 0o755.
func TestAReleaseIsKeptAndEveryPlatformPinned(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this, other()}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	if got, want := w.lock(), lockOf(sums); got != want {
		t.Fatalf("errata.lock is\n%s\nwant\n%s", got, want)
	}
	info, err := os.Stat(w.cached("csv", "0.1.0", this, sums[this]))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Errorf("the build's mode is %v, want read only and executable", info.Mode())
	}
	want := "errata-ext-csv 0.1.0 " + this + ": sha256 " + sums[this] + " matches errata-ext-csv_checksums.txt, and errata.lock now pins it\n"
	if w.out.String() != want {
		t.Errorf("printed %q, want %q", w.out.String(), want)
	}
	noTemp(t, w.cache)
	t.Setenv("XDG_CACHE_HOME", w.cache)
	set, err := plugin.Open(w.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if k := set.Kind(context.Background(), "csv"); k.Spec == reflect.TypeFor[map[string]any]() {
		t.Error("the run cannot use the build the install kept")
	}
}

// A release's assets are fetched from <source>/<tag>/<asset> with the
// tag's / escaped as %2F, as GitHub's own links write it. Planted break:
// release.url joining the tag unescaped.
func TestTheTagIsEscapedInTheURL(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	want := "/f-klement/errata/releases/download/errata-ext-csv%2Fv0.1.0/errata-ext-csv_checksums.txt"
	if !slices.Contains(w.requests, seen{host: "github.com", path: want}) {
		t.Fatalf("requests %v hold no %s", w.requests, want)
	}
}

// The newest release is the highest version by semantic versioning among
// the plugin's releases that are neither drafts nor prereleases, read from
// every page of the API. Planted breaks: newest reading the first page
// alone; highest comparing versions as strings; highest keeping drafts;
// highest keeping prereleases; isPrerelease taking a version with a
// prerelease part, which GitHub did not flag, for a release.
func TestTheNewestReleaseIsTheHighestOfEveryPage(t *testing.T) {
	old := perPage
	perPage = 3
	t.Cleanup(func() { perPage = old })
	w := newWorld(t)
	w.publish("0.2.0", []string{this}, "")
	w.list(apiRelease{Tag: "v0.4.2"}, apiRelease{Tag: "errata-ext-csv/v0.11.0", Draft: true})
	w.publish("0.10.0", []string{this}, "")
	w.list(apiRelease{Tag: "errata-ext-csv/v1.0.0-rc.1", Prerelease: true}, apiRelease{Tag: "errata-ext-csv/v2.0.0", Prerelease: true}, apiRelease{Tag: "errata-ext-csv/v1.1.0-rc.1+b.1"})
	w.publish("0.1.0", []string{this}, "")
	w.install(Request{Plugins: []string{"csv"}})
	if !strings.HasPrefix(w.lock(), "csv 0.10.0 ") {
		t.Fatalf("errata.lock is\n%s\nwant csv at 0.10.0", w.lock())
	}
}

// A plugin with no release that is neither a draft nor a prerelease is
// refused, and the lock is not written. Planted break: found returning "".
func TestNoReleaseOfThePluginIsRefused(t *testing.T) {
	w := newWorld(t)
	w.list(apiRelease{Tag: "v0.4.2"}, apiRelease{Tag: "errata-ext-csv/v0.1.0", Draft: true})
	if err := w.refused(Request{Plugins: []string{"csv"}}); !strings.Contains(err, "hold no release of errata-ext-csv that is neither a draft nor a prerelease") {
		t.Fatal(err)
	}
	if w.lock() != "" {
		t.Error("a lock was written")
	}
}

// The checksums file must list this platform's build. Planted break:
// fetchRelease not checking ok.
func TestAReleaseWithNoBuildForThisPlatformIsRefused(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{other()}, "")
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0"}}); !strings.Contains(err, "lists no build for "+this) {
		t.Fatal(err)
	}
}

// The checksums file is parsed strictly. Planted breaks: checksumLine
// accepting upper case; parseChecksums keeping a name listed twice with
// the same sum; add accepting an asset of no platform.
func TestTheChecksumsFileIsParsedStrictly(t *testing.T) {
	sum := strings.Repeat("a", 64)
	good := sum + "  errata-ext-csv_linux_amd64\n" + sum + "  errata-ext-csv_windows_arm64.exe\n" + strings.Repeat("b", 64) + "  errata-ext-csv_NOTICES.txt\n"
	c, err := parseChecksums("csv", []byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"linux/amd64": sum, "windows/arm64": sum}; !reflect.DeepEqual(c.platforms, want) || c.notices != strings.Repeat("b", 64) {
		t.Fatalf("got %+v", c)
	}
	for _, bad := range []string{
		strings.ToUpper(sum) + "  errata-ext-csv_linux_amd64\n",
		sum + " errata-ext-csv_linux_amd64\n",
		sum + "  errata-ext-csv_linux_amd64\r\n",
		sum[1:] + "  errata-ext-csv_linux_amd64\n",
		sum + "  errata-ext-csv_linux_amd64\n" + sum + "  errata-ext-csv_linux_amd64\n",
		sum + "  errata-ext-csv_plan9_amd64\n",
		sum + "  errata-ext-csv_windows_amd64\n",
		sum + "  errata-ext-other_linux_amd64\n",
		sum + "  errata-ext-csv_linux_amd64\n\n",
		sum + "  \n",
	} {
		if _, err := parseChecksums("csv", []byte(bad)); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// A checksums file over 64 KiB is refused. Planted break: checksums
// reading with maxDownload as the limit.
func TestAChecksumsFileOver64KiBIsRefused(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.put(release{name: "csv", version: "0.1.0"}, checksumsName("csv"), []byte(strings.Repeat("#", maxChecksums+1)))
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0"}}); !strings.Contains(err, "is over 65536 bytes") {
		t.Fatal(err)
	}
}

// When the lock pins the version already, the checksums file must list
// exactly its platforms, each at its sum; errata refuses a difference,
// names it, and leaves the lock as it was. Planted breaks: fetchRelease
// skipping differs; differs comparing only the platforms the file lists.
func TestAReleaseThatChangedAfterItsPinIsRefused(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this, other()}, "")
	pinned := map[string]string{this: sums[this], other(): strings.Repeat("c", 64)}
	w.writeLock(lockOf(pinned))
	err := w.refused(Request{Plugins: []string{"csv"}})
	if !strings.Contains(err, other()+": the lock pins sha256:"+strings.Repeat("c", 64)+", and the file lists sha256:"+sums[other()]) {
		t.Fatal(err)
	}
	more := map[string]string{this: sums[this], other(): sums[other()], "windows/arm64": strings.Repeat("d", 64)}
	if this == "windows/arm64" {
		more["linux/arm64"] = strings.Repeat("d", 64)
		delete(more, "windows/arm64")
	}
	w.writeLock(lockOf(more))
	if err := w.refused(Request{Plugins: []string{"csv"}}); !strings.Contains(err, "and the file lists none") {
		t.Fatal(err)
	}
	if w.lock() != lockOf(more) {
		t.Error("the lock changed")
	}
}

// A download whose sha256 is not the checksums file's line is refused,
// and nothing of it is kept. Planted break: matches returning nil.
func TestADownloadWhoseSumDiffersIsRefused(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	w.put(release{name: "csv", version: "0.1.0"}, assetName("csv", this), fake(t, "csv", "0.2.0"))
	err := w.refused(Request{Plugins: []string{"csv@0.1.0"}})
	if !strings.Contains(err, "and errata-ext-csv_checksums.txt gives "+sums[this]+", so errata keeps nothing of it") {
		t.Fatal(err)
	}
	if exists(filepath.Join(w.cache, "errata", "plugins", "csv")) || w.lock() != "" {
		t.Error("something of the download was kept")
	}
	noTemp(t, w.cache)
}

// The build is sent describe before it is kept: one that names another
// plugin, or gives another version than the tag's, is removed and the
// install refused. Planted breaks: keep skipping Describe; keep passing
// "" as the version.
func TestABuildIsDescribedBeforeItIsKept(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	r := release{name: "csv", version: "0.1.0"}
	for _, liar := range [][]byte{fake(t, "other", "0.1.0"), fake(t, "csv", "0.2.0")} {
		w.put(r, assetName("csv", this), liar)
		w.put(r, checksumsName("csv"), []byte(sumOf(liar)+"  "+assetName("csv", this)+"\n"))
		err := w.refused(Request{Plugins: []string{"csv@0.1.0"}})
		if !strings.Contains(err, "names itself \"other\"") && !strings.Contains(err, "describes itself as release \"0.2.0\"") {
			t.Fatal(err)
		}
		if exists(w.cached("csv", "0.1.0", this, sumOf(liar))) || w.lock() != "" {
			t.Error("a build that failed describe was kept")
		}
	}
	noTemp(t, w.cache)
}

// Every download is at most 256 MiB, whether the server announces its
// length or streams it. Planted breaks: answered not checking the
// announced length; copyHashed not checking what it copied.
func TestADownloadOverTheBoundIsRefused(t *testing.T) {
	old := maxDownload
	maxDownload = 1 << 10
	t.Cleanup(func() { maxDownload = old })
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0"}}); !strings.Contains(err, "it is ") || !strings.Contains(err, "over 1024,") {
		t.Fatal(err)
	}
	w.writeLock(lockOf(sums))
	if err := w.refused(Request{Source: "file://" + filepath.ToSlash(w.mirror)}); !strings.Contains(err, "the download is over 1024 bytes") {
		t.Fatal(err)
	}
	noTemp(t, w.cache)
}

// A good file at the build's place in the cache is left as it is, and a
// damaged one is replaced by the checked build, a link there replaced and
// not followed. Planted breaks: keepInCache renaming without asking holds;
// keepInCache never renaming over a file; replace following a link to make
// its target writable.
//
// On Windows a damaged file errata kept read only has the read only
// attribute, which replace clears before the rename; no test here runs on
// Windows, so that step is reasoned from the API alone.
func TestTheCacheKeepsAGoodFileAndReplacesADamagedOne(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	path := w.cached("csv", "0.1.0", this, sums[this])
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fake(t, "csv", "0.1.0"), 0o555); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("the install renamed over a good file")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	if !holds(path, sums[this]) {
		t.Error("the damaged file is still there")
	}
	outside := filepath.Join(t.TempDir(), "a file of the user's")
	if err := os.WriteFile(outside, []byte("theirs"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || !holds(path, sums[this]) {
		t.Errorf("the link in the cache was not replaced: %v, %v", info, err)
	}
	if info, _ := os.Stat(outside); info.Mode().Perm() != 0o444 {
		t.Errorf("the file the link named is %v now, want the 0444 it had", info.Mode())
	}
	file := filepath.Join(t.TempDir(), plugin.Executable("nginx", osOf(this)))
	writeExe(t, file, fake(t, "-", "0.5.0"))
	w.install(Request{File: file})
	sum := sumOf(fake(t, "-", "0.5.0"))
	before, _ = os.Stat(w.cached("nginx", "0.5.0", this, sum))
	w.install(Request{File: file})
	if after, _ := os.Stat(w.cached("nginx", "0.5.0", this, sum)); !os.SameFile(before, after) {
		t.Error("an install of the same file renamed over the good file in the cache")
	}
}

// A good build the cache holds already is described, not downloaded again.
// Planted break: keepRelease downloading whatever the cache holds.
func TestAGoodCachedBuildIsNotDownloadedAgain(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	before := len(w.hosts())
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	for _, r := range w.requestsSince(before) {
		if strings.HasSuffix(r.path, assetName("csv", this)) {
			t.Error("the build the cache holds was downloaded again")
		}
	}
}

// errata.lock keeps the comment lines at its top through an install
// (owner, 2026-11-20). Planted break: Format leaving the header out.
func TestAnInstallKeepsTheLocksLeadingComments(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.writeLock("# Plugins of this repository.\n\n")
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	if !strings.HasPrefix(w.lock(), "# Plugins of this repository.\n\ncsv 0.1.0 ") {
		t.Errorf("errata.lock is\n%s", w.lock())
	}
}

// The releases API refusing a client with no token for its rate limit
// says how to get past it. Planted break: answered giving the status
// alone.
func TestARateLimitNamesTheToken(t *testing.T) {
	w := newWorld(t)
	w.apiStatus = http.StatusForbidden
	if err := w.refused(Request{Plugins: []string{"csv"}}); !strings.Contains(err, "403 Forbidden; GitHub limits the requests of a client with no token: set GITHUB_TOKEN, or give the version") {
		t.Fatal(err)
	}
}

// errata writes the lock last, and only when every plugin installed: a
// second plugin that fails leaves the lock as it was, with the first one's
// build in the cache. Planted break: release putting the lock to disk as
// it pins each plugin.
func TestTheLockIsWrittenOnlyWhenEveryPluginInstalled(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	w.writeLock("# the lock as it was\n")
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0", "nginx@0.3.0"}}); !strings.Contains(err, "404") {
		t.Fatal(err)
	}
	if w.lock() != "# the lock as it was\n" {
		t.Errorf("the lock is\n%s", w.lock())
	}
	if !exists(w.cached("csv", "0.1.0", this, sums[this])) {
		t.Error("the first plugin's build is not in the cache")
	}
	entries, _ := os.ReadDir(w.root)
	if len(entries) != 1 {
		t.Errorf("the root holds %v", entries)
	}
}

// A name or version that reaches a path is refused before errata fetches
// anything: ../.. as a version, a name the protocol does not allow, the
// key of a built in kind, and one name twice. Planted breaks: parseTarget
// skipping CheckVersion; checkName skipping the built in keys.
func TestANameAndVersionAreCheckedBeforeAnyFetch(t *testing.T) {
	w := newWorld(t)
	for arg, want := range map[string]string{
		"csv@../..":  "no semantic version",
		"csv@v0.1.0": "no semantic version",
		"../csv":     "is not 1 to 64",
		"yaml":       "the key of a kind errata has built in",
	} {
		if err := w.refused(Request{Plugins: []string{arg}}); !strings.Contains(err, want) {
			t.Errorf("%s: %s", arg, err)
		}
	}
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0", "csv@0.2.0"}}); !strings.Contains(err, "named twice") {
		t.Error(err)
	}
	if len(w.hosts()) > 0 {
		t.Errorf("errata fetched from %v", w.hosts())
	}
}

// GITHUB_TOKEN goes to api.github.com alone: not to github.com's
// downloads, a mirror, or a URL. Planted break: get sending the token
// on every request.
func TestTheTokenGoesToTheAPIAlone(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.install(Request{Plugins: []string{"csv"}})
	w.install(Request{Plugins: []string{"csv"}, Source: "https://mirror.example"})
	w.install(Request{Plugins: []string{"csv"}, URL: "https://mirror.example/errata-ext-csv/v0.1.0/" + assetName("csv", this), SHA256: sumOf(fake(t, "csv", "0.1.0")), Replace: true})
	api := 0
	for _, r := range w.requests {
		switch {
		case r.host == apiHost && r.auth == "Bearer a-ci-token":
			api++
		case r.auth != "":
			t.Errorf("%s%s was sent %q", r.host, r.path, r.auth)
		}
	}
	if api != 1 {
		t.Errorf("the API was sent the token %d times, want 1: %v", api, w.requests)
	}
}

// A redirect of a request to the API to another host carries no token,
// which Go's client drops. Planted break: httpsOnly copying the token onto
// every hop.
func TestARedirectedAPIRequestCarriesNoToken(t *testing.T) {
	w := newWorld(t)
	w.apiRedirect = "https://elsewhere.example/releases"
	w.refused(Request{Plugins: []string{"csv"}})
	hops := 0
	for _, r := range w.requests {
		if r.host == "elsewhere.example" {
			hops++
			if r.auth != "" {
				t.Errorf("the redirect to %s carried %q", r.host, r.auth)
			}
		}
	}
	if hops == 0 {
		t.Fatalf("no request followed the redirect: %v", w.requests)
	}
}

// A redirect to anything but https is refused. Planted break: client
// keeping the given client's own redirect policy.
func TestARedirectToPlainHTTPIsRefused(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.redirect = "http://github.com/elsewhere"
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0"}}); !strings.Contains(err, "which is not https, is refused") {
		t.Fatal(err)
	}
}

// An install makes the cache directory and its parents when they are not
// there, as a CI job's XDG_CACHE_HOME of a fresh checkout is not: v0.5.0
// stopped on "mkdir <cache>/errata: no such file or directory". Planted
// break: makeDir that leaves the cache's own directory to os.Mkdir.
func TestAnInstallMakesAMissingCacheDirectory(t *testing.T) {
	w := newWorld(t)
	w.cache = filepath.Join(w.cache, "not", "there")
	w.in.Cache = w.cache
	sums := w.publish("0.1.0", []string{this}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	if _, err := os.Stat(w.cached("csv", "0.1.0", this, sums[this])); err != nil {
		t.Fatal(err)
	}
}
