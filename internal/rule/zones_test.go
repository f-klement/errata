package rule

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The embedded database and its release are the toolchain's, so a Go
// upgrade that brings newer zone rules fails here until they are copied.
func TestZoneDatabaseMatchesGo(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Skipf("go env GOROOT: %v; the toolchain's database cannot be compared", err)
	}
	goroot := strings.TrimSpace(string(out))
	theirs, err := os.ReadFile(filepath.Join(goroot, "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(zoneinfo, theirs) {
		t.Fatalf("internal/rule/zoneinfo.zip differs from %s's; from the repository root, run:\n%s", goroot, zoneCopy)
	}
	script, err := os.ReadFile(filepath.Join(goroot, "lib", "time", "update.bash"))
	if err != nil {
		t.Fatal(err)
	}
	release := ""
	for line := range strings.Lines(string(script)) {
		if v, ok := strings.CutPrefix(line, "DATA="); ok {
			release = strings.TrimSpace(v)
		}
	}
	if release == "" || release != zoneDataVersion {
		t.Fatalf("internal/rule/zoneinfo.version says %q and %s's update.bash says %q; from the repository root, run:\n%s",
			zoneDataVersion, goroot, release, zoneCopy)
	}
}

// zoneVerdicts is what the helper processes check: named zones give the
// embedded database's answer.
func zoneVerdicts(t *testing.T) {
	t.Helper()
	for zone, want := range map[string]string{"Europe/Berlin": "13", "America/New_York": "8"} {
		a, err := Compile("string(timestamp('2020-03-08T12:00:00Z').getHours(v[0])) == '"+want+"'", []string{"v"})
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := a.Eval(context.Background(), map[string][]string{"v": {zone}}, nil); err != nil || !ok {
			t.Errorf("getHours(%q): %v, %v; want %s", zone, ok, err, want)
		}
	}
}

// tokyoAsWestern is a zone database in which Europe/Berlin and
// America/New_York hold Asia/Tokyo's rules, stored uncompressed, as the time
// package reads a ZONEINFO zip.
func tokyoAsWestern(t *testing.T) []byte {
	t.Helper()
	tokyo := zoneData(t, "Asia/Tokyo")
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range []string{"Europe/Berlin", "America/New_York"} {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(tokyo); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// planted fails the test unless the time package now answers Berlin with
// Tokyo's offset, so the planted database is known to be in force.
func planted(t *testing.T) {
	t.Helper()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := time.Date(2020, 3, 8, 12, 0, 0, 0, time.UTC).In(berlin).Zone(); offset != 9*3600 {
		t.Fatalf("the planted database is not in force: the machine's Berlin is %+ds from UTC", offset)
	}
}

// TestZoneHelperBogus runs only as the child of TestZonesIgnoreTheMachine,
// with ZONEINFO pointing at a database in which Berlin is Tokyo.
func TestZoneHelperBogus(t *testing.T) {
	if os.Getenv("ERRATA_ZONE_HELPER") != "bogus" {
		t.Skip("runs as the child of TestZonesIgnoreTheMachine")
	}
	planted(t)
	zoneVerdicts(t)
}

// ZONEINFO and TZ pointing at a wrong database change nothing: the planted
// break is a ZONEINFO zip whose Europe/Berlin is Asia/Tokyo, which
// time.LoadLocation then returns.
func TestZonesIgnoreTheMachine(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "zoneinfo.zip")
	if err := os.WriteFile(bogus, tokyoAsWestern(t), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestZoneHelperBogus$", "-test.v")
	cmd.Env = append(os.Environ(), "ERRATA_ZONE_HELPER=bogus", "ZONEINFO="+bogus, "TZ=Europe/Berlin")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestZoneHelperBogus") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

// zoneData returns a zone's raw data from the embedded database.
func zoneData(t *testing.T, name string) []byte {
	t.Helper()
	archive, err := zoneArchive()
	if err != nil {
		t.Fatal(err)
	}
	f, err := archive.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
