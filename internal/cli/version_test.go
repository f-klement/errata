package cli

import (
	"runtime/debug"
	"testing"
)

// A binary built by `go install module@vX.Y.Z` carries no -ldflags, so its
// version must come from the module version Go records in the binary.
func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name, stamped string
		info          *debug.BuildInfo
		ok            bool
		want          string
	}{
		{"release build stamps the version", "v0.1.1", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true, "v0.1.1"},
		{"go install falls back to the module version", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.1"}}, true, "v0.1.1"},
		{"a local build stays dev", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "dev"},
		{"no build info stays dev", "dev", nil, false, "dev"},
		{"an empty module version stays dev", "dev", &debug.BuildInfo{}, true, "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveVersion(tc.stamped, func() (*debug.BuildInfo, bool) { return tc.info, tc.ok })
			if got != tc.want {
				t.Fatalf("resolveVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
