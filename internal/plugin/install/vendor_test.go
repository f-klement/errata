package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/plugin"
)

// --vendor writes every platform's build the checksums file lists into the
// vendored directory under its sum, with the notices file and errata's
// LICENSE beside them, so a run with no cache starts the vendored build.
// Planted breaks: vendorRelease skipping the other platforms; vendorRelease
// skipping the notices; vendorRelease skipping the LICENSE.
func TestVendorWritesEveryPlatformTheNoticesAndTheLicense(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this, other()}, "notices of the code csv links\n")
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	for _, p := range []string{this, other()} {
		if !holds(w.vendored("csv", "0.1.0", p, sums[p]), sums[p]) {
			t.Errorf("the build for %s is not vendored", p)
		}
	}
	dir := filepath.Join(w.root, ".errata", "plugins", "csv", "0.1.0")
	if data, _ := os.ReadFile(filepath.Join(dir, noticesName("csv"))); string(data) != "notices of the code csv links\n" {
		t.Errorf("the notices are %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "LICENSE")); !bytes.Equal(data, license) {
		t.Errorf("the LICENSE is %q", data)
	}
	noTemp(t, filepath.Join(w.root, ".errata"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.RemoveAll(w.cache); err != nil {
		t.Fatal(err)
	}
	set, err := plugin.Open(w.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Describe(t.Context(), w.root, w.vendored("csv", "0.1.0", this, sums[this]), "csv", "0.1.0", sums[this]); err != nil || !set.Has("csv") {
		t.Errorf("the vendored build does not run: %v", err)
	}
}

// --vendor never writes over a file in the vendored directory: one that
// matches is left as it is, and one that does not is refused, named, and
// left for git to restore. Planted break: keepVendored renaming over a
// file that differs.
func TestVendorNeverWritesOverAVendoredFile(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this, other()}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	path := w.vendored("csv", "0.1.0", other(), sums[other()])
	before, _ := os.Stat(path)
	w.install(Request{Vendor: true})
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Error("a vendored file that matched was renamed over")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := w.refused(Request{Vendor: true})
	if !strings.Contains(err, path+" is not the file whose sha256 is "+sums[other()]+", and errata never writes over a vendored file") {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "edited" {
		t.Error("the vendored file was written over")
	}
}

// errata writes nothing through a symbolic link in the vendored directory,
// so a committed link cannot send a build outside the repository. Planted
// break: makeOne accepting a link.
func TestVendorWritesNothingThroughALink(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(w.root, ".errata", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(w.root, ".errata", "plugins", "csv")); err != nil {
		t.Fatal(err)
	}
	err := w.refused(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	if !strings.Contains(err, "is a symbolic link or no directory, and errata writes no plugin below one") {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) > 0 {
		t.Errorf("errata wrote %v through the link", entries)
	}
	if w.lock() != "" {
		t.Error("a lock was written")
	}
}

// The vendored LICENSE is errata's own. Planted break: the embedded copy
// edited.
func TestTheVendoredLicenseIsErratas(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, license) {
		t.Error("internal/plugin/install/LICENSE differs from the repository's LICENSE: copy it again")
	}
}

// While the repository has the vendored directory, where a run looks for
// plugins alone, an install vendors what it installs as --vendor does, and
// says so: a release by name, the plugins the lock pins, and a file
// (owner, 2026-11-20). Planted breaks: vendorsAnyway never vendoring; file
// not vendoring its pin.
func TestAnInstallVendorsWhenTheRepositoryDoes(t *testing.T) {
	w := newWorld(t)
	sums := w.publish("0.1.0", []string{this, other()}, "")
	if err := os.MkdirAll(filepath.Join(w.root, ".errata", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	for _, p := range []string{this, other()} {
		if !holds(w.vendored("csv", "0.1.0", p, sums[p]), sums[p]) {
			t.Errorf("the build for %s is not vendored", p)
		}
	}
	if !strings.Contains(w.out.String(), ".errata/plugins is there, and a run looks for plugins in it alone, so errata vendors what it installs") {
		t.Errorf("the install did not say it vendors: %s", w.out.String())
	}
	if err := os.RemoveAll(filepath.Join(w.root, ".errata", "plugins", "csv")); err != nil {
		t.Fatal(err)
	}
	w.install(Request{})
	if !holds(w.vendored("csv", "0.1.0", this, sums[this]), sums[this]) {
		t.Error("the install of what the lock pins did not vendor it")
	}
	file := filepath.Join(t.TempDir(), plugin.Executable("nginx", osOf(this)))
	writeExe(t, file, fake(t, "-", "0.5.0"))
	w.install(Request{File: file})
	if sum := sumOf(fake(t, "-", "0.5.0")); !holds(w.vendored("nginx", "0.5.0", this, sum), sum) {
		t.Error("the file was not vendored")
	}
}

// A --vendor that fails removes the directories of the repository it made,
// so no empty .errata/plugins is left for every later run to look in; a
// vendored directory that was there stays. Planted breaks: unmake removing
// nothing; makeDir recording a directory it did not make.
func TestAFailedVendorLeavesNoDirectoryItMade(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this, other()}, "")
	w.put(release{source: DefaultSource, name: "csv", version: "0.1.0"}, assetName("csv", other()), []byte("not the bytes the checksums file lists"))
	if err := w.refused(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true}); !strings.Contains(err, "so errata keeps nothing of it") {
		t.Fatal(err)
	}
	if exists(filepath.Join(w.root, ".errata")) {
		t.Error("a failed --vendor left .errata in the repository")
	}
	if err := os.MkdirAll(filepath.Join(w.root, ".errata", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.refused(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	if !exists(filepath.Join(w.root, ".errata", "plugins")) {
		t.Error("a failed --vendor removed the vendored directory that was there")
	}
}

// A vendored LICENSE or notices file that differs from the one errata
// would write, such as one git checked out with CRLF line ends, is left as
// it is and said, since a run never reads it. Planted break: vendorText
// refusing it.
func TestADifferentVendoredLicenseIsLeftAndSaid(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	path := filepath.Join(w.root, ".errata", "plugins", "csv", "0.1.0", "LICENSE")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	crlf := bytes.ReplaceAll(license, []byte("\n"), []byte("\r\n"))
	if err := os.WriteFile(path, crlf, 0o644); err != nil {
		t.Fatal(err)
	}
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	if data, _ := os.ReadFile(path); !bytes.Equal(data, crlf) {
		t.Error("the vendored LICENSE was written over")
	}
	if !strings.Contains(w.out.String(), ".errata/plugins/csv/0.1.0/LICENSE differs from the one errata would write, and errata left it as it is") {
		t.Errorf("the install did not say so: %s", w.out.String())
	}
}

// A build vendored already is not fetched again. Planted break:
// vendorBuild fetching whatever is there.
func TestAVendoredBuildIsNotFetchedAgain(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this, other()}, "")
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	before := len(w.hosts())
	w.install(Request{Vendor: true})
	for _, r := range w.requestsSince(before) {
		if strings.HasSuffix(r.path, assetName("csv", other())) {
			t.Errorf("the vendored build for %s was fetched again", other())
		}
	}
}

// An install removes a temporary directory a killed install left over an
// hour ago, and leaves a newer one, which an install may be writing.
// Planted breaks: fetch not sweeping; sweep removing every temporary
// directory.
func TestAnOldTemporaryDirectoryIsRemoved(t *testing.T) {
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	base := filepath.Join(w.root, ".errata", "plugins")
	old, fresh := filepath.Join(base, ".install-old"), filepath.Join(base, ".install-new")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	w.install(Request{Plugins: []string{"csv@0.1.0"}, Vendor: true})
	if exists(old) || !exists(fresh) {
		t.Errorf("after the install, the old one is there: %v; the new one: %v", exists(old), exists(fresh))
	}
}
