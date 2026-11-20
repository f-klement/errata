package plugin

import (
	"fmt"
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
