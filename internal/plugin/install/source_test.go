package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin"
)

// A version the lock pins installs through a mirror with no flag, over
// https from a static server over the nested layout and from a file URL,
// whose %2F errata decodes as the nested directory; the mirror is not
// written into the lock. Planted breaks: open reading a file URL's raw
// path; release refusing every source but the default.
func TestAPinnedVersionInstallsThroughAMirror(t *testing.T) {
	for _, source := range []string{"https://mirror.example", "file:///MIRROR"} {
		w := newWorld(t)
		sums := w.publish("0.1.0", []string{this, other()}, "")
		w.writeLock(lockOf(sums))
		w.install(Request{Source: strings.Replace(source, "/MIRROR", filepath.ToSlash(w.mirror), 1)})
		if w.lock() != lockOf(sums) {
			t.Errorf("%s: the lock is\n%s", source, w.lock())
		}
		if !exists(w.cached("csv", "0.1.0", this, sums[this])) {
			t.Errorf("%s: the build is not in the cache", source)
		}
		for _, h := range w.hosts() {
			if h == "github.com" {
				t.Errorf("%s: errata fetched from github.com", source)
			}
		}
	}
}

// A mirror that serves other bytes than the lock pins is refused, though
// its checksums file and its build agree with each other and the build
// describes itself as the pinned version. Planted break: fetchRelease
// skipping differs.
func TestAMirrorThatSwapsTheBytesIsRefused(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	w.writeLock(lockOf(sums))
	r := release{name: "csv", version: "0.1.0"}
	swapped := fake(t, "-", "0.1.0")
	w.putMirror(r, assetName("csv", this), swapped)
	w.putMirror(r, checksumsName("csv"), []byte(sumOf(swapped)+"  "+assetName("csv", this)+"\n"))
	err := w.refused(Request{Source: "https://mirror.example"})
	if !strings.Contains(err, "the lock pins sha256:"+sums[this]+", and the file lists sha256:"+sumOf(swapped)) {
		t.Fatal(err)
	}
	if exists(filepath.Join(w.cache, "errata", "plugins", "csv")) {
		t.Error("the swapped build was kept")
	}
}

// A version's first pin through a source other than the default is
// refused without --trust-source, before anything is fetched, and taken
// with it; a source that is the default needs no flag. Planted breaks:
// release ignoring TrustSource; isDefault true for every source.
func TestAFirstPinThroughAMirrorNeedsTrustSource(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	err := w.refused(Request{Plugins: []string{"csv@0.1.0"}, Source: "https://mirror.example"})
	if !strings.Contains(err, "errata.lock pins no errata-ext-csv 0.1.0, and the plugin source https://mirror.example is not errata's releases") {
		t.Fatal(err)
	}
	if len(w.hosts()) > 0 || w.lock() != "" {
		t.Fatalf("errata fetched %v or wrote a lock", w.hosts())
	}
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Source: "https://mirror.example", TrustSource: true})
	if !strings.HasPrefix(w.lock(), "csv 0.1.0 ") {
		t.Fatalf("the lock is\n%s", w.lock())
	}
	w = newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Source: DefaultSource + "/"})
}

// A mirror has no API, so a plugin the lock does not pin needs a version
// through one. Planted break: version asking the API whatever the source.
func TestAMirrorNeedsAVersion(t *testing.T) {
	w := newWorld(t)
	err := w.refused(Request{Plugins: []string{"csv"}, Source: "https://mirror.example", TrustSource: true})
	if !strings.Contains(err, "a mirror has no releases API") {
		t.Fatal(err)
	}
}

// --file copies the file into the cache's directory, describes the copy,
// keeps it under its sum and pins it for this platform as a file, at the
// version it gives; the file must be a regular file named
// errata-ext-<name>, and a version that is no semantic version, such as
// ../.., is refused before it reaches a path. Planted breaks: file not
// refusing a link; fileName not requiring the prefix; Describe's
// anyVersion not checking the version.
func TestAFileIsCopiedDescribedAndPinned(t *testing.T) {
	w := newWorld(t)
	dir := t.TempDir()
	path := filepath.Join(dir, plugin.Executable("nginx", osOf(this)))
	writeExe(t, path, fake(t, "-", "0.5.0"))
	w.install(Request{File: path})
	sum := sumOf(fake(t, "-", "0.5.0"))
	if want := "nginx 0.5.0 " + this + " sha256:" + sum + " source=file\n"; w.lock() != want {
		t.Fatalf("the lock is\n%s\nwant\n%s", w.lock(), want)
	}
	if !holds(w.cached("nginx", "0.5.0", this, sum), sum) {
		t.Error("the build is not in the cache")
	}
	link := filepath.Join(dir, "errata-ext-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := w.refused(Request{File: link}); !strings.Contains(err, "is not a regular file") {
		t.Error(err)
	}
	plain := filepath.Join(dir, "nginx")
	writeExe(t, plain, fake(t, "-", "0.5.0"))
	if err := w.refused(Request{File: plain}); !strings.Contains(err, "is not named errata-ext-<name>") {
		t.Error(err)
	}
	escape := filepath.Join(dir, "errata-ext-up")
	writeExe(t, escape, fake(t, "-", "../.."))
	if err := w.refused(Request{File: escape}); !strings.Contains(err, "no semantic version") {
		t.Error(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.cache, "errata")); len(entries) != 1 {
		t.Errorf("the cache's errata directory holds %v, want plugins alone", entries)
	}
	noTemp(t, w.cache)
}

// A file or a URL takes the place of a release the lock pins only with
// --replace. Planted break: mayReplace ignoring Replace.
func TestAFileTakesTheReleasesPlaceOnlyWithReplace(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	w.writeLock(lockOf(sums))
	path := filepath.Join(t.TempDir(), plugin.Executable("csv", osOf(this)))
	writeExe(t, path, fake(t, "csv", "0.2.0"))
	if err := w.refused(Request{File: path}); !strings.Contains(err, "pass --replace") {
		t.Fatal(err)
	}
	w.install(Request{File: path, Replace: true})
	if !strings.HasSuffix(w.lock(), " source=file\n") {
		t.Fatalf("the lock is\n%s", w.lock())
	}
	if err := w.refused(Request{Plugins: []string{"csv"}}); !strings.Contains(err, "errata installs no release over it") {
		t.Fatal(err)
	}
}

// --url downloads the build, checks it against --sha256 before anything is
// kept, describes it and pins it with its URL; a sum that differs names
// both and keeps nothing. Planted breaks: url checking the download
// against the lock's sum; urlName not trimming the platform.
func TestAURLIsCheckedAgainstItsSum(t *testing.T) {
	w := newWorld(t)
	r := release{name: "nginx", version: "0.3.0"}
	w.putMirror(r, assetName("nginx", this), fake(t, "nginx", "0.3.0"))
	at := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", this)
	sum := sumOf(fake(t, "nginx", "0.3.0"))
	wrong := strings.Repeat("0", 64)
	if err := w.refused(Request{URL: at, SHA256: wrong}); !strings.Contains(err, "has the sha256 "+sum+", and --sha256 gives "+wrong) {
		t.Fatal(err)
	}
	if exists(filepath.Join(w.cache, "errata", "plugins", "nginx")) || w.lock() != "" {
		t.Error("something of a download whose sum differs was kept")
	}
	w.install(Request{URL: at, SHA256: strings.ToUpper(sum)})
	if want := "nginx 0.3.0 " + this + " sha256:" + sum + " source=url:" + at + "\n"; w.lock() != want {
		t.Fatalf("the lock is\n%s\nwant\n%s", w.lock(), want)
	}
	noTemp(t, w.cache)
}

// --platform adds another platform's build to a URL entry, checked and not
// run; it needs the entry first. Planted break: urlPlatform skipping the
// entry check.
func TestAnotherPlatformIsAddedToAURLEntry(t *testing.T) {
	w := newWorld(t)
	r := release{name: "nginx", version: "0.3.0"}
	there := []byte("not a program at all")
	w.putMirror(r, assetName("nginx", other()), there)
	at := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", other())
	req := Request{Plugins: []string{"nginx"}, URL: at, SHA256: sumOf(there), Platform: other()}
	if err := w.refused(req); !strings.Contains(err, "pins errata-ext-nginx from no URL") {
		t.Fatal(err)
	}
	w.putMirror(r, assetName("nginx", this), fake(t, "nginx", "0.3.0"))
	here := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", this)
	w.install(Request{URL: here, SHA256: sumOf(fake(t, "nginx", "0.3.0"))})
	w.install(req)
	if !strings.Contains(w.lock(), "nginx 0.3.0 "+other()+" sha256:"+sumOf(there)+" source=url:"+at+"\n") {
		t.Fatalf("the lock is\n%s", w.lock())
	}
	if !holds(w.cached("nginx", "0.3.0", other(), sumOf(there)), sumOf(there)) {
		t.Error("the other platform's build is not in the cache")
	}
}

// A reinstall of a URL entry into a repository that vendors fails when
// another platform's URL serves other bytes than its pin, and writes no
// lock. Planted breaks: reinstallURL or vendorPin passing over the error.
func TestAVendoredURLEntryFailsOnAnotherPlatformsBytes(t *testing.T) {
	w := newWorld(t)
	r := release{name: "nginx", version: "0.3.0"}
	there := []byte("not a program at all")
	w.putMirror(r, assetName("nginx", other()), there)
	w.putMirror(r, assetName("nginx", this), fake(t, "nginx", "0.3.0"))
	at := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", other())
	here := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", this)
	w.install(Request{URL: here, SHA256: sumOf(fake(t, "nginx", "0.3.0"))})
	w.install(Request{Plugins: []string{"nginx"}, URL: at, SHA256: sumOf(there), Platform: other()})
	lock := w.lock()
	w.putMirror(r, assetName("nginx", other()), []byte("other bytes"))
	if err := os.MkdirAll(filepath.Join(w.root, ".errata", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.refused(Request{}); !strings.Contains(err, sumOf(there)) {
		t.Fatal(err)
	}
	if w.lock() != lock {
		t.Errorf("the lock changed to\n%s", w.lock())
	}
}

// writeExe writes data at path as an executable.
func writeExe(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
