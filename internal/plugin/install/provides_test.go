package install

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin"
)

// The line an install prints for a provider names what it provides and
// every variable of errata's environment its read and predict get, and an
// upgrade names those its last pin did not record. A build that provides
// no operation, an extractor, prints nothing.
func TestAnInstallSaysWhatAProviderGets(t *testing.T) {
	pin := plugin.Pin{Name: "kube", Version: "1.2.0"}
	cases := []struct {
		name   string
		before map[string][]string
		desc   plugin.Described
		want   string
	}{
		{"an extractor", nil, plugin.Described{Version: "1.2.0"}, ""},
		{"render alone", nil, plugin.Described{Operations: []string{"render"}},
			"errata-ext-kube 1.2.0 provides render"},
		{"predict alone gets nothing", nil, plugin.Described{Operations: []string{"render", "predict"}},
			"errata-ext-kube 1.2.0 provides render, predict; its read and predict get no variable of errata's environment"},
		{"read and a first pin", nil, plugin.Described{Operations: []string{"read"}, Env: []string{"KUBECONFIG", "HOME"}},
			"errata-ext-kube 1.2.0 provides read; its read and predict get KUBECONFIG, HOME from errata's environment, as errata.lock now records"},
		{"an upgrade that adds one", map[string][]string{"kube": {"HOME"}}, plugin.Described{Operations: []string{"read"}, Env: []string{"KUBECONFIG", "HOME", "TOKEN"}},
			"errata-ext-kube 1.2.0 provides read; its read and predict get KUBECONFIG, HOME, TOKEN from errata's environment, as errata.lock now records; new since its last pin: KUBECONFIG, TOKEN"},
		{"an upgrade that adds none", map[string][]string{"kube": {"HOME", "KUBECONFIG"}}, plugin.Described{Operations: []string{"read"}, Env: []string{"KUBECONFIG"}},
			"errata-ext-kube 1.2.0 provides read; its read and predict get KUBECONFIG from errata's environment, as errata.lock now records"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := &job{before: c.before}
			j.sayProvides(pin, c.desc)
			got := ""
			if len(j.said) > 1 {
				t.Fatalf("said %q, more than one line", j.said)
			}
			if len(j.said) == 1 {
				got = j.said[0]
			}
			if got != c.want {
				t.Errorf("said\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// An install records the names its describe gave for this platform alone
// and leaves another platform's as an install there recorded them, while
// that platform's build is the one pinned; a build the lock did not pin
// before gets this platform's names. So a plugin that names USERPROFILE on
// Windows and HOME elsewhere, installed on each, runs on each. Planted
// breaks: withEnv giving every platform this one's names; withEnv keeping
// an old entry for a build that changed.
func TestAnInstallKeepsAnotherPlatformsEnv(t *testing.T) {
	const linux, windows, mac = "linux/amd64", "windows/amd64", "darwin/arm64"
	old := plugin.Pin{Name: "kube", Version: "1.2.0", Source: plugin.SourceRelease,
		Sums: map[string]string{linux: "a", windows: "b", mac: "c"},
		Env:  map[string][]string{linux: {"HOME"}, windows: {"USERPROFILE"}, mac: {"HOME"}}}
	j := &job{in: &Installer{Platform: linux}, lock: &plugin.Lock{Pins: []plugin.Pin{old}}}
	pin := plugin.Pin{Name: "kube", Version: "1.2.0", Source: plugin.SourceRelease,
		Sums: map[string]string{linux: "a", windows: "b", mac: "new", "linux/arm64": "d"}}
	j.put(pin, plugin.Described{Operations: []string{"read"}, Env: []string{"HOME", "KUBECONFIG"}})
	got, _ := j.lock.Pin("kube")
	want := map[string]string{linux: "HOME KUBECONFIG", windows: "USERPROFILE", mac: "HOME KUBECONFIG", "linux/arm64": "HOME KUBECONFIG"}
	for platform, names := range want {
		if strings.Join(got.Env[platform], " ") != names {
			t.Errorf("%s: env %q, want %s", platform, got.Env[platform], names)
		}
	}
	if strings.Join(j.before["kube"], " ") != "HOME" {
		t.Errorf("the names before, here: %q", j.before["kube"])
	}
}
