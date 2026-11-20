package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With no name, errata installs every plugin the lock pins at its pinned
// version: a release from the plugin source and a URL entry from its URL,
// each checked against the pin, and with --vendor every platform a URL
// entry pins. Planted breaks: reinstall installing a URL entry as a
// release; vendorPin skipping the other platforms.
func TestNoNameInstallsWhatTheLockPins(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this}, "")
	w.publish("0.2.0", []string{this}, "")
	r := release{name: "nginx", version: "0.3.0"}
	there := []byte("the build of nginx for another platform")
	w.putMirror(r, assetName("nginx", this), fake(t, "nginx", "0.3.0"))
	w.putMirror(r, assetName("nginx", other()), there)
	here := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", this)
	away := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", other())
	nginx := sumOf(fake(t, "nginx", "0.3.0"))
	lock := lockOf(sums) +
		"nginx 0.3.0 " + this + " sha256:" + nginx + " source=url:" + here + "\n" +
		"nginx 0.3.0 " + other() + " sha256:" + sumOf(there) + " source=url:" + away + "\n"
	if other() < this {
		lock = lockOf(sums) +
			"nginx 0.3.0 " + other() + " sha256:" + sumOf(there) + " source=url:" + away + "\n" +
			"nginx 0.3.0 " + this + " sha256:" + nginx + " source=url:" + here + "\n"
	}
	w.writeLock(lock)
	w.install(Request{Vendor: true})
	if w.lock() != lock {
		t.Errorf("the lock is\n%s\nwant\n%s", w.lock(), lock)
	}
	for _, want := range [][2]string{{w.cached("csv", "0.1.0", this, sums[this]), sums[this]}, {w.cached("nginx", "0.3.0", this, nginx), nginx}, {w.vendored("nginx", "0.3.0", other(), sumOf(there)), sumOf(there)}} {
		if !holds(want[0], want[1]) {
			t.Errorf("%s is not installed", want[0])
		}
	}
}

// A URL entry's pinned URL that now serves other bytes is refused. Planted
// break: reinstallURL checking against no sum.
func TestAURLEntryIsCheckedAgainstItsPin(t *testing.T) {
	w := newWorld(t)
	r := release{name: "nginx", version: "0.3.0"}
	w.putMirror(r, assetName("nginx", this), fake(t, "nginx", "0.4.0"))
	at := "https://mirror.example/errata-ext-nginx/v0.3.0/" + assetName("nginx", this)
	pinned := sumOf(fake(t, "nginx", "0.3.0"))
	w.writeLock("nginx 0.3.0 " + this + " sha256:" + pinned + " source=url:" + at + "\n")
	if err := w.refused(Request{}); !strings.Contains(err, "and errata.lock gives "+pinned) {
		t.Fatal(err)
	}
}

// A file entry cannot be fetched again: with no name errata names it and
// stops, unless the cache holds its build. Planted break: fileEntry
// returning nil when nothing holds the build.
func TestAFileEntryIsNotFetched(t *testing.T) {
	w := newWorld(t)
	path := filepath.Join(t.TempDir(), "errata-ext-nginx")
	writeExe(t, path, fake(t, "-", "0.5.0"))
	w.install(Request{File: path})
	lock := w.lock()
	w.out.Reset()
	w.install(Request{})
	if !strings.Contains(w.out.String(), "from a file, is in the cache") {
		t.Errorf("printed %q", w.out.String())
	}
	if err := os.RemoveAll(w.cache); err != nil {
		t.Fatal(err)
	}
	if err := w.refused(Request{}); !strings.Contains(err, "pins errata-ext-nginx 0.5.0 from a file, which errata cannot fetch") {
		t.Fatal(err)
	}
	if w.lock() != lock {
		t.Error("the lock changed")
	}
}

// An empty lock installs nothing and writes no lock. Planted break:
// nothing returning false.
func TestAnEmptyLockInstallsNothing(t *testing.T) {
	w := newWorld(t)
	w.install(Request{})
	if w.lock() != "" || !strings.Contains(w.out.String(), "nothing to install") {
		t.Fatalf("the lock is %q; printed %q", w.lock(), w.out.String())
	}
}
