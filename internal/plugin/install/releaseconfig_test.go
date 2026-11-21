package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/plugin"
)

// releaseConfig is what TestReleaseConfigNamesWhatInstallFetches reads of a
// plugin's GoReleaser configuration, cmd/errata-ext-<name>/goreleaser.yaml.
type releaseConfig struct {
	Builds []struct {
		Binary string
		Goos   []string
		Goarch []string
	}
	Archives []struct {
		Formats      []string
		NameTemplate string `yaml:"name_template"`
	}
	Checksum struct {
		NameTemplate string        `yaml:"name_template"`
		ExtraFiles   []releaseFile `yaml:"extra_files"`
	}
	Release struct {
		MakeLatest *bool         `yaml:"make_latest"`
		ExtraFiles []releaseFile `yaml:"extra_files"`
	}
}

// releaseFile is an entry of extra_files: a file of the tree, published
// under a name of its own.
type releaseFile struct {
	Glob         string
	NameTemplate string `yaml:"name_template"`
}

// Every plugin's release publishes the names install fetches (ADR 0021):
// its executable for each platform errata ships for, as assetName names it,
// the checksums file checksumsName names, and, for a plugin whose directory
// holds NOTICES.txt, that file as noticesName names it, listed in the
// checksums file; and the release is never marked latest. So the
// configuration and install cannot drift apart. The snapshot build of each
// plugin shows that GoReleaser renders the names as this test does
// (ci/README.md, Plugin releases). Planted breaks: the csv name template
// without its _ before the architecture, darwin left out of goos, the hcl
// notices left out of the checksums file, and make_latest: true.
func TestReleaseConfigNamesWhatInstallFetches(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "..", "cmd", "errata-ext-*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no cmd/errata-ext-* found three directories up: %v", err)
	}
	for _, dir := range dirs {
		name := strings.TrimPrefix(filepath.Base(dir), "errata-ext-")
		t.Run(name, func(t *testing.T) { checkReleaseConfig(t, dir, name) })
	}
}

// checkReleaseConfig holds dir's goreleaser.yaml to the names install
// fetches for plugin name.
func checkReleaseConfig(t *testing.T, dir, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "goreleaser.yaml"))
	if err != nil {
		t.Fatalf("every plugin is released from its own configuration: %v", err)
	}
	var c releaseConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Builds) != 1 || c.Builds[0].Binary != "errata-ext-"+name {
		t.Fatalf("the configuration builds %+v, want the one program errata-ext-%s", c.Builds, name)
	}
	if got := buildPlatforms(c.Builds[0].Goos, c.Builds[0].Goarch); !slices.Equal(got, plugin.Platforms) {
		t.Errorf("the configuration builds for %v, want the platforms errata ships for, %v", got, plugin.Platforms)
	}
	if len(c.Archives) != 1 || !slices.Equal(c.Archives[0].Formats, []string{"binary"}) {
		t.Fatalf("the configuration's archives are %+v, want the executables themselves, formats [binary]", c.Archives)
	}
	for _, p := range plugin.Platforms {
		if got, want := binaryAsset(c.Archives[0].NameTemplate, p), assetName(name, p); got != want {
			t.Errorf("the release publishes %s for %s, and install fetches %s", got, p, want)
		}
	}
	if got, want := c.Checksum.NameTemplate, checksumsName(name); got != want {
		t.Errorf("the release's checksums file is %s, and install fetches %s", got, want)
	}
	if c.Release.MakeLatest == nil || *c.Release.MakeLatest {
		t.Errorf("the plugin's release may be marked latest; set release.make_latest: false, so errata's own release stays the latest")
	}
	notices := noticesFiles(t, dir, name)
	if !slices.Equal(c.Checksum.ExtraFiles, notices) {
		t.Errorf("the checksums file lists the extra files %+v, want %+v", c.Checksum.ExtraFiles, notices)
	}
	if !slices.Equal(c.Release.ExtraFiles, notices) {
		t.Errorf("the release publishes the extra files %+v, want %+v", c.Release.ExtraFiles, notices)
	}
}

// buildPlatforms is every goos/goarch pair of a build, in the order of
// plugin.Platforms.
func buildPlatforms(goos, goarch []string) []string {
	var out []string
	for _, o := range goos {
		for _, a := range goarch {
			out = append(out, o+"/"+a)
		}
	}
	slices.Sort(out)
	return out
}

// binaryAsset is the name GoReleaser uploads a binary archive under for
// platform: its name template with the platform's os and architecture, and
// the executable's extension, .exe on Windows. A template that reads more
// than those two is left as it is, which names no asset install fetches.
func binaryAsset(tmpl, platform string) string {
	goos, arch, _ := strings.Cut(platform, "/")
	n := strings.NewReplacer("{{ .Os }}", goos, "{{ .Arch }}", arch).Replace(tmpl)
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// noticesFiles is the one extra file a plugin whose directory holds
// NOTICES.txt publishes, under noticesName, and nothing for one without.
func noticesFiles(t *testing.T, dir, name string) []releaseFile {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "NOTICES.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return []releaseFile{{Glob: "./cmd/errata-ext-" + name + "/NOTICES.txt", NameTemplate: noticesName(name)}}
}
