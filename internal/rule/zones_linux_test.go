package rule

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// mountRefused starts the line the child prints when it may not mount, as
// under a container's AppArmor profile; the parent skips on it.
const mountRefused = "ERRATA-ZONE-MOUNT-REFUSED"

// TestZoneHelperPlantedFiles runs only as the child of
// TestZonesWithoutZoneFiles, in a mount namespace of its own. It covers the
// machine's zone files with a database in which Berlin and New York are
// Tokyo, so a read of them in place of the embedded database gives Tokyo's
// hour.
func TestZoneHelperPlantedFiles(t *testing.T) {
	if os.Getenv("ERRATA_ZONE_HELPER") != "planted" {
		t.Skip("runs as the child of TestZonesWithoutZoneFiles")
	}
	tokyo := zoneData(t, "Asia/Tokyo")
	bogusZip := tokyoAsWestern(t)
	goTime := filepath.Join(runtime.GOROOT(), "lib", "time") //nolint:staticcheck // the time package reads its zip from here, so the test covers it
	for _, dir := range []string{"/usr/share/zoneinfo", "/usr/share/lib/zoneinfo", "/usr/lib/locale/TZ", "/etc/zoneinfo", goTime} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		if err := syscall.Mount("tmpfs", dir, "tmpfs", 0, ""); err != nil {
			if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
				t.Skipf("%s %s: %v", mountRefused, dir, err)
			}
			t.Fatalf("cover %s: %v", dir, err)
		}
		if dir == goTime {
			if err := os.WriteFile(filepath.Join(dir, "zoneinfo.zip"), bogusZip, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		for _, name := range []string{"Europe/Berlin", "America/New_York"} {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, tokyo, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	planted(t)
	zoneVerdicts(t)
}

// The machine's zone files are never read: in the child they say Berlin is
// Tokyo, and named zones still give Berlin's hour.
func TestZonesWithoutZoneFiles(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestZoneHelperPlantedFiles$", "-test.v")
	cmd.Env = append(os.Environ(), "ERRATA_ZONE_HELPER=planted", "ZONEINFO=", "TZ=")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	switch {
	case errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC):
		t.Skipf("this machine allows no user namespace, so the zone files cannot be covered: %v", err)
	case strings.Contains(string(out), mountRefused):
		t.Skipf("this machine allows no mount in a user namespace, so the zone files cannot be covered:\n%s", out)
	case err != nil || !strings.Contains(string(out), "--- PASS: TestZoneHelperPlantedFiles"):
		t.Fatalf("child: %v\n%s", err, out)
	}
}
