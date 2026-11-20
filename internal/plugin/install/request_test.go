package install

import (
	"path/filepath"
	"strings"
	"testing"
)

// Flags that do not go together, a URL that is not https or holds a user
// name, a sum that is not 64 hex digits, and a source that is neither an
// https nor a file URL are refused before errata reads the lock or opens a
// connection. Planted breaks: checkURL skipping checkHTTPS; checkHTTPS
// accepting user info; checkSource accepting http; checkRelease accepting
// --replace; checkSource accepting a query; sourceFrom ignoring SourceFrom.
func TestARequestIsCheckedBeforeAnyFetch(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, c := range []struct {
		req  Request
		want string
	}{
		{Request{URL: "http://p.example/x", SHA256: sum}, "is no https URL"},
		{Request{URL: "https://tok:x@p.example/x", SHA256: sum}, "holds a user name or password"},
		{Request{URL: "https://p.example/x"}, "--url goes with --sha256"},
		{Request{SHA256: sum}, "--sha256 goes with --url"},
		{Request{URL: "https://p.example/x", SHA256: "abc"}, "is not 64 hex digits"},
		{Request{URL: "https://p.example/x", SHA256: sum, Platform: "plan9/amd64"}, "none errata ships for"},
		{Request{URL: "https://p.example/x", SHA256: sum, Platform: other()}, "name the plugin"},
		{Request{URL: "https://p.example/x", SHA256: sum, Plugins: []string{"a", "b"}}, "at most one name"},
		{Request{URL: "https://p.example/x", SHA256: sum, Source: "https://m.example"}, "--source does not apply to --url"},
		{Request{URL: "https://p.example/x", SHA256: sum, Vendor: true}, "--vendor does not go with --url"},
		{Request{File: "x", URL: "https://p.example/x"}, "give one of them"},
		{Request{File: "x", Plugins: []string{"a"}}, "give no name"},
		{Request{File: "x", Platform: this}, "--platform goes with --url"},
		{Request{File: "x", TrustSource: true}, "--trust-source does not apply to --file"},
		{Request{Replace: true}, "--replace goes with --file or --url"},
		{Request{Platform: this}, "--platform goes with --url"},
		{Request{Source: "http://m.example"}, "is no https URL"},
		{Request{Source: "https://u:p@m.example"}, "holds a user name or password"},
		{Request{Source: "file://host/srv"}, "is no file URL of this machine"},
		{Request{Source: "https://m.example/r?key=secret"}, "--source for m.example/r has a query or a fragment"},
		{Request{Source: "https://m.example/r#top"}, "has a query or a fragment"},
		{Request{Source: "file:///srv/r?x"}, "has a query or a fragment"},
		{Request{Source: "http://m.example", SourceFrom: "ERRATA_PLUGIN_SOURCE"}, `ERRATA_PLUGIN_SOURCE "http://m.example" is no https URL`},
	} {
		w := newWorld(t)
		if err := w.refused(c.req); !strings.Contains(err, c.want) || strings.Contains(err, "secret") {
			t.Errorf("%+v: %s, want %s, and no query", c.req, err, c.want)
		}
		if len(w.hosts()) > 0 {
			t.Errorf("%+v: errata fetched from %v", c.req, w.hosts())
		}
	}
}

// A file URL's path names the local file: on Windows /C:/m is C:\m, and
// any other path, or any path elsewhere, is the path itself. Planted
// break: localPath trimming the slash on every system.
func TestAFileURLNamesTheLocalFile(t *testing.T) {
	for _, c := range []struct{ path, goos, want string }{
		{"/C:/m/x", "windows", "C:/m/x"},
		{"/C:/m/x", "linux", "/C:/m/x"},
		{"/m/x", "windows", "/m/x"},
		{"/C", "windows", "/C"},
		{"/Cx/m", "windows", "/Cx/m"},
	} {
		if got := localPath(c.path, c.goos); got != filepath.FromSlash(c.want) {
			t.Errorf("localPath(%q, %s) = %q, want %q", c.path, c.goos, got, filepath.FromSlash(c.want))
		}
	}
}
