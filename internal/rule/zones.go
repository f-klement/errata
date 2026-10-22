package rule

import (
	"archive/zip"
	"bytes"
	_ "embed" // the time zone database
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// zoneinfo is the IANA time zone database errata reads named zones from,
// so a verdict never depends on the zone files, ZONEINFO or TZ of the
// machine it runs on. Zone rules change only with an errata release.
//
// It is lib/time/zoneinfo.zip from the Go toolchain errata is built with,
// and zoneinfo.version is the DATA= line of that toolchain's
// lib/time/update.bash, which names the IANA release. A test fails when
// either differs from the toolchain's; zoneCopy is the command that copies
// both.
//
//go:embed zoneinfo.zip
var zoneinfo []byte

//go:embed zoneinfo.version
var zoneinfoVersion string

// zoneDataVersion is the IANA release in zoneinfo, such as 2026c.
var zoneDataVersion = strings.TrimSpace(zoneinfoVersion)

// zoneCopy copies the toolchain's database and its release into this
// package, from the repository root.
const zoneCopy = `cp "$(go env GOROOT)/lib/time/zoneinfo.zip" internal/rule/zoneinfo.zip && ` +
	`sed -n 's/^DATA=//p' "$(go env GOROOT)/lib/time/update.bash" > internal/rule/zoneinfo.version`

// maxZoneFile bounds one zone's data; the largest in the database is a few
// kilobytes.
const maxZoneFile = 64 << 10

var (
	zoneArchive = sync.OnceValues(func() (*zip.Reader, error) {
		return zip.NewReader(bytes.NewReader(zoneinfo), int64(len(zoneinfo)))
	})
	zones sync.Map // name to *time.Location
)

// zone returns the named zone from the embedded database, never from the
// machine.
func zone(name string) (*time.Location, error) {
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), nil
	}
	notFound := fmt.Errorf("the time zone %q is not in errata's time zone database (IANA %s)", name, zoneDataVersion)
	archive, err := zoneArchive()
	if err != nil {
		return nil, fmt.Errorf("the time zone database: %w", err)
	}
	f, err := archive.Open(name)
	if err != nil {
		return nil, notFound
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		return nil, notFound
	}
	data, err := io.ReadAll(io.LimitReader(f, maxZoneFile))
	if err != nil {
		return nil, fmt.Errorf("the time zone %q: %w", name, err)
	}
	loc, err := time.LoadLocationFromTZData(name, data)
	if err != nil {
		return nil, fmt.Errorf("the time zone %q: %w", name, err)
	}
	zones.Store(name, loc)
	return loc, nil
}
