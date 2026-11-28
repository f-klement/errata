package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var sumA = "sha256:" + strings.Repeat("a", 64)

// A lock pins each plugin's version and a checksum for each platform, and
// records where the builds came from: a release when the line says
// nothing, a file, or a URL for each platform. Planted breaks: the source
// field ignored, which read every pin as a release; addURL keeping only
// the first platform's URL.
func TestALockPinsVersionSumsAndSource(t *testing.T) {
	l, err := ParseLock([]byte("# plugins\n\ncsv 0.1.0 linux/amd64 " + sumA + "\ncsv 0.1.0 darwin/arm64 sha256:" + strings.Repeat("b", 64) + "\n" +
		"toml 1.2.3 linux/amd64 " + sumA + " source=file\n" +
		"ini 0.2.0 linux/amd64 " + sumA + " source=url:https://example.com/errata-ext-ini_linux_amd64\n" +
		"ini 0.2.0 darwin/arm64 " + sumA + " source=url:https://example.com/errata-ext-ini_darwin_arm64\n"))
	if err != nil {
		t.Fatal(err)
	}
	csv, ok := l.Pin("csv")
	if !ok || csv.Version != "0.1.0" || csv.Sums["linux/amd64"] != strings.Repeat("a", 64) || csv.Sums["darwin/arm64"] != strings.Repeat("b", 64) || csv.Source != SourceRelease {
		t.Fatalf("csv: %+v", csv)
	}
	if toml, _ := l.Pin("toml"); toml.Source != SourceFile {
		t.Fatalf("toml: %+v", toml)
	}
	ini, _ := l.Pin("ini")
	if ini.Source != SourceURL || ini.URLs["linux/amd64"] != "https://example.com/errata-ext-ini_linux_amd64" || ini.URLs["darwin/arm64"] != "https://example.com/errata-ext-ini_darwin_arm64" {
		t.Fatalf("ini: %+v", ini)
	}
	if _, ok := l.Pin("yaml"); ok {
		t.Fatal("a name the lock does not hold is pinned")
	}
}

// Each malformed line is refused with its line number and what is wrong.
// Planted breaks: each check of add dropped in turn; checkURL taking any
// scheme; checkURL taking user info; the line number counted from 0.
func TestALockRefusesAMalformedLine(t *testing.T) {
	for line, want := range map[string]string{
		"csv 0.1.0 linux/amd64":                                                                          "holds 3 fields",
		"Csv 0.1.0 linux/amd64 " + sumA:                                                                  `the plugin name "Csv"`,
		"csv ../.. linux/amd64 " + sumA:                                                                  `csv: the version "../.." is no semantic version`,
		"csv 0.1.0 linux " + sumA:                                                                        `the platform "linux" is none errata ships for`,
		"csv 0.1.0 linux/amd65 " + sumA:                                                                  `the platform "linux/amd65" is none errata ships for`,
		"csv 0.1.0 linux/amd64 " + sumA + " source=url:http://example.com/x":                             `the source URL "http://example.com/x" is no https URL`,
		"csv 0.1.0 linux/amd64 " + sumA + " source=url:https://tok:x@example.com/x":                      `the source URL for example.com holds a user name or password`,
		"csv 0.1.0 linux/amd64 " + sumA + " source=url:https://tok@example.com/x":                        `the source URL for example.com holds a user name or password`,
		"csv 0.1.0 linux/amd64 " + sumA + " source=url:file:///etc/x":                                    `the source URL "file:///etc/x" is no https URL`,
		"csv 0.1.0 linux/amd64 " + sumA + "\ncsv 0.1.0 linux/arm64 " + sumA + " source=url:":             `the source URL "" is no https URL`,
		"csv 0.1.0 linux/amd64 " + sumA + " source=file\ncsv 0.1.0 linux/arm64 " + sumA + " source=file": "for more than one platform",
		"csv 0.1.0 linux/amd64 sha256:" + strings.Repeat("A", 64):                                        "is no checksum",
		"csv 0.1.0 linux/amd64 md5:" + strings.Repeat("a", 64):                                           "is no checksum",
		"csv 0.1.0 linux/amd64 " + sumA + " source=git":                                                  `the field "source=git" is no source`,
		"csv 0.1.0 linux/amd64 " + sumA + "\ncsv 0.2.0 linux/arm64 " + sumA:                              "pinned at 0.1.0 on an earlier line and at 0.2.0 here",
		"csv 0.1.0 linux/amd64 " + sumA + "\ncsv 0.1.0 linux/arm64 " + sumA + " source=file":             "another source here",
		"csv 0.1.0 linux/amd64 " + sumA + "\ncsv 0.1.0 linux/amd64 " + sumA:                              "pins linux/amd64 twice",
		"k 0.1.0 linux/amd64 " + sumA + " env=HOME source=file":                                          `the field "env=HOME" is neither a source nor an env`,
		"k 0.1.0 linux/amd64 " + sumA + " env=":                                                          `the env field: env lists ""`,
		"k 0.1.0 linux/amd64 " + sumA + " env=HOME,github_token":                                         "the env field: env lists github_token, which is GITHUB_TOKEN",
		"k 0.1.0 linux/amd64 " + sumA + " source=file env=A x":                                           "holds 7 fields",
	} {
		_, err := ParseLock([]byte(line + "\n"))
		at := fmt.Sprintf("%s:%d: ", LockFile, strings.Count(line, "\n")+1)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.HasPrefix(err.Error(), at) {
			t.Errorf("%q: got %v, want %s", line, err, want)
		}
	}
}

// A version is semantic versioning, with no v, before it reaches a path.
// Planted breaks: CheckVersion's pattern the lenient one it replaced, which
// let v1 and 1.0 through; the 64 byte bound taken as exclusive.
func TestALockVersionIsSemver(t *testing.T) {
	for v, ok := range map[string]bool{
		"0.1.0": true, "1.2.0-rc.1+build.5": true, "10.0.0": true,
		"v1.0.0": false, "1.0": false, "01.0.0": false, "../..": false, "1.0.0/..": false, "": false,
		"1.0.0-" + strings.Repeat("a", 58): true, "1.0.0-" + strings.Repeat("a", 59): false,
	} {
		if err := CheckVersion(v); (err == nil) != ok {
			t.Errorf("%q: got %v, want ok %v", v, err, ok)
		}
	}
}

// A repository with no lock pins no plugin; a lock past the bound is
// refused, and one at it read. Planted breaks: a missing lock read as an
// error; the bound taken as exclusive.
func TestReadLock(t *testing.T) {
	root := t.TempDir()
	if l, err := ReadLock(root); err != nil || len(l.Pins) != 0 {
		t.Fatalf("no lock: %+v, %v", l, err)
	}
	writeFile(t, root, LockFile, strings.Repeat("#\n", maxLockBytes/2))
	if _, err := ReadLock(root); err != nil {
		t.Fatalf("at the bound: %v", err)
	}
	writeFile(t, root, LockFile, strings.Repeat("#\n", maxLockBytes/2)+"#")
	if _, err := ReadLock(root); err == nil || !strings.Contains(err.Error(), "the largest lock file errata reads") {
		t.Fatalf("too large: %v", err)
	}
}

// A plugin may not take a built in kind's key. Planted break: Open not
// checking the built in keys.
func TestALockMayNotPinABuiltInKey(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, LockFile, "yaml 0.1.0 linux/amd64 "+sumA+"\n")
	if _, err := Open(root, []string{"yaml"}); err == nil || !strings.Contains(err.Error(), "yaml") {
		t.Fatalf("got %v", err)
	}
}

// A build lives under its name, version, platform and sum, with .exe on
// Windows, so two builds of one version never share a file. Planted
// breaks: the platform left out of the path; the sum left out.
func TestBuildPath(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if got := BuildPath("/c", "csv", "0.1.0", "windows/amd64", a); got != filepath.Join("/c", "csv", "0.1.0", "windows_amd64", a, "errata-ext-csv.exe") {
		t.Fatal(got)
	}
	if got := BuildPath("/c", "csv", "0.1.0", "linux/arm64", a); got != filepath.Join("/c", "csv", "0.1.0", "linux_arm64", a, "errata-ext-csv") {
		t.Fatal(got)
	}
	if BuildPath("/c", "csv", "0.1.0", "linux/arm64", a) == BuildPath("/c", "csv", "0.1.0", "linux/arm64", b) {
		t.Fatal("two builds of one version share a path")
	}
}

// Format writes a line for each build, each plugin's platforms in the
// order of Platforms and its source and env as ParseLock reads them, each
// platform's env its own, and Put sets a pin in its place or after the
// others. Planted breaks: sourceField writing no URL; envField writing
// one platform's env on every line; Put appending a pin the lock holds
// already, or the first pin it holds.
func TestALockIsWrittenAsItIsRead(t *testing.T) {
	text := "csv 0.1.0 darwin/arm64 " + sumA + "\ncsv 0.1.0 linux/amd64 sha256:" + strings.Repeat("b", 64) + "\n" +
		"toml 1.2.3 linux/amd64 " + sumA + " source=file\n" +
		"ini 0.2.0 darwin/arm64 " + sumA + " source=url:https://example.com/a\n" +
		"ini 0.2.0 linux/amd64 " + sumA + " source=url:https://example.com/b\n" +
		"kube 0.1.0 linux/amd64 " + sumA + " env=KUBECONFIG,HOME\n" +
		"kube 0.1.0 windows/amd64 " + sumA + " env=KUBECONFIG,USERPROFILE\n" +
		"kube 0.1.0 windows/arm64 " + sumA + "\n" +
		"ocp 0.1.0 linux/amd64 " + sumA + " source=file env=KUBECONFIG\n"
	l, err := ParseLock([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(l.Format()); got != text {
		t.Fatalf("Format wrote\n%s\nwant\n%s", got, text)
	}
	if kube, _ := l.Pin("kube"); len(kube.Env) != 2 || strings.Join(kube.Env["linux/amd64"], " ") != "KUBECONFIG HOME" || strings.Join(kube.Env["windows/amd64"], " ") != "KUBECONFIG USERPROFILE" {
		t.Fatalf("kube's env is %q", kube.Env)
	}
	l.Put(Pin{Name: "toml", Version: "1.3.0", Sums: map[string]string{"linux/amd64": strings.Repeat("c", 64)}, Source: SourceFile})
	l.Put(Pin{Name: "zip", Version: "0.1.0", Sums: map[string]string{"linux/amd64": strings.Repeat("d", 64)}})
	l.Put(Pin{Name: "csv", Version: "0.1.0", Sums: map[string]string{"darwin/arm64": strings.Repeat("a", 64), "linux/amd64": strings.Repeat("b", 64)}})
	want := strings.Replace(text, "toml 1.2.3 linux/amd64 "+sumA, "toml 1.3.0 linux/amd64 sha256:"+strings.Repeat("c", 64), 1) +
		"zip 0.1.0 linux/amd64 sha256:" + strings.Repeat("d", 64) + "\n"
	if got := string(l.Format()); got != want {
		t.Fatalf("after Put, Format wrote\n%s\nwant\n%s", got, want)
	}
}

// WriteLock renames a whole file over the lock, keeping its mode, and
// refuses a pin ParseLock would not read back, such as a version that is
// no semantic version, writing nothing. Planted breaks: WriteLock skipping
// the read back; writeRenamed leaving the temporary file's 0600.
func TestWriteLockWritesAWholeFileItReadsBack(t *testing.T) {
	root := t.TempDir()
	good := Lock{Pins: []Pin{{Name: "csv", Version: "0.1.0", Sums: map[string]string{"linux/amd64": strings.Repeat("a", 64)}, Source: SourceRelease}}}
	if err := WriteLock(root, good); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, LockFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("the lock is %v, %v; want mode 0644", info, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	bad := Lock{Pins: []Pin{{Name: "csv", Version: "../..", Sums: map[string]string{"linux/amd64": strings.Repeat("b", 64)}, Source: SourceRelease}}}
	if err := WriteLock(root, bad); err == nil {
		t.Fatal("a pin of version ../.. was written")
	}
	if err := WriteLock(root, good); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("the lock's mode is %v, want the 0640 it had", info.Mode())
	}
	if l, err := ReadLock(root); err != nil || !l.Pins[0].same(good.Pins[0]) {
		t.Errorf("read back %+v, %v", l, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("the root holds %v", entries)
	}
}

// The blank and comment lines before the first pin are the lock's header,
// which Format writes back as the file had them; a comment between pins is
// dropped, and WriteLock refuses a header that would not read back as one
// (owner, 2026-11-20). Planted breaks: keepHeader keeping comments after a
// pin; Format leaving the header out; WriteLock not comparing headers.
func TestALockKeepsItsLeadingComments(t *testing.T) {
	text := "# Plugins this repository runs.\r\n\n# Update with errata plugin install.\n" +
		"csv 0.1.0 linux/amd64 " + sumA + "\n# dropped\ntoml 1.2.3 linux/amd64 " + sumA + "\n"
	l, err := ParseLock([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(text, "# dropped\n", "", 1)
	if got := string(l.Format()); got != want {
		t.Fatalf("Format wrote\n%q\nwant\n%q", got, want)
	}
	if l, err := ParseLock([]byte("# only a comment\n")); err != nil || string(l.Format()) != "# only a comment\n" {
		t.Fatalf("a lock of a comment alone: %q, %v", l.Format(), err)
	}
	if l, err := ParseLock(nil); err != nil || len(l.Header) != 0 {
		t.Fatalf("an empty lock has the header %q, %v", l.Header, err)
	}
	root := t.TempDir()
	l.Header = []string{"# two lines\n# in one"}
	if err := WriteLock(root, l); err == nil {
		t.Fatal("WriteLock wrote a header that reads back as another")
	}
}
