package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The names errata finds a project's file and its override by are
// compose-go's, in its order, so a release that changes either list fails
// here. With -f, which a rule with overrides: models, compose-go takes
// neither COMPOSE_FILE nor a file it finds by name.
func TestDiscoveryNamesAreComposeGos(t *testing.T) {
	if !slices.Equal(defaultNames, cli.DefaultFileNames) {
		t.Errorf("defaultNames %v, compose-go's %v", defaultNames, cli.DefaultFileNames)
	}
	if !slices.Equal(overrideNames, cli.DefaultOverrideFileNames) {
		t.Errorf("overrideNames %v, compose-go's %v", overrideNames, cli.DefaultOverrideFileNames)
	}
	dir := t.TempDir()
	o := cli.ProjectOptions{ConfigPaths: []string{"a.yaml"}, WorkingDir: dir,
		Environment: types.Mapping{"COMPOSE_FILE": "b.yaml"}}
	if err := cli.WithConfigFileEnv(&o); err != nil {
		t.Fatal(err)
	}
	if err := cli.WithDefaultConfigPath(&o); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.ConfigPaths, []string{"a.yaml"}) {
		t.Errorf("with -f compose-go loads %v", o.ConfigPaths)
	}
}

// readEvery is Every's values of name over files, from file.
func readEvery(t *testing.T, files map[string]string, file, name string) (string, error) {
	t.Helper()
	x, err := Every(file, name, 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), extracttest.Repo(t, files))
	return show(vs), err
}

// Every reads the file docker compose finds by its name as docker compose
// run without -f loads it: with the first override beside it, by
// compose-go's names and order, merged over it, so the project is merged
// and a lookup from the shell anywhere marks it. Planted breaks: alone
// returning the file without its override; extract handing project a read
// as -f names it; project not asking alone; firstThere skipping
// compose.override.yml.
func TestEveryReadsTheOverrideBeside(t *testing.T) {
	main := "services:\n  web:\n    image: w\n    environment: [FOO=1]\n"
	over := "services:\n  web:\n    environment: [FOO=2, BAR=3]\n"
	cases := []struct {
		name  string
		files map[string]string
		file  string
		vr    string
		want  string
	}{
		{"compose.override.yaml", map[string]string{"compose.yaml": main, "compose.override.yaml": over},
			"compose.yaml", "FOO", `=2@compose.override.yaml:3 service "web"`},
		{"the override's own variable", map[string]string{"compose.yaml": main, "compose.override.yaml": over},
			"compose.yaml", "BAR", `=3@compose.override.yaml:3 service "web"`},
		{"compose.override.yml before .yaml", map[string]string{"compose.yaml": main,
			"compose.override.yml": over, "compose.override.yaml": "services:\n  web:\n    environment: [FOO=9]\n"},
			"compose.yaml", "FOO", `=2@compose.override.yml:3 service "web"`},
		{"docker-compose.override.yml", map[string]string{"deploy/docker-compose.yml": main, "deploy/docker-compose.override.yml": over},
			"deploy/docker-compose.yml", "FOO", `=2@deploy/docker-compose.override.yml:3 service "web"`},
		{"an override with a lookup from the shell", map[string]string{"compose.yaml": main,
			"compose.override.yaml": over + "  db:\n    image: \"${X:-d}\"\n"},
			"compose.yaml", "FOO", `=2@compose.override.yaml:3! service "web"`},
		{"no override", map[string]string{"compose.yaml": main, "override.yaml": over},
			"compose.yaml", "FOO", `=1@compose.yaml:4 service "web"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readEvery(t, c.files, c.file, c.vr)
			if err != nil || got != c.want {
				t.Errorf("got %s, %v; want %s", got, err, c.want)
			}
		})
	}
}

// Every refuses a file that docker compose run without -f passes over for
// another default name beside it. A rule naming that file reads it, as -f
// does. Planted breaks: notFound refusing nothing; alone checking no name
// before the file's own.
func TestEveryRefusesAFileDiscoveryPassesOver(t *testing.T) {
	files := map[string]string{
		"compose.yaml":       "services:\n  web:\n    image: w\n    environment: [FOO=1]\n",
		"docker-compose.yml": "services:\n  web:\n    image: w\n    environment: [FOO=2]\n",
	}
	_, err := readEvery(t, files, "docker-compose.yml", "FOO")
	if err == nil || !strings.Contains(err.Error(), "docker compose run without -f loads compose.yaml, not docker-compose.yml") {
		t.Errorf("got %v", err)
	}
	if got, err := readEvery(t, files, "compose.yaml", "FOO"); err != nil || got != `=1@compose.yaml:4 service "web"` {
		t.Errorf("compose.yaml: %s, %v", got, err)
	}
	s := Spec{OneFile: oneFile("docker-compose.yml"), Service: "web", Key: "environment.FOO"}
	if got := mustRead(t, files, s); got != "environment.FOO=2@docker-compose.yml:4" {
		t.Errorf("compose rule on docker-compose.yml: %s", got)
	}
}

// A compose rule with no overrides: names its file as -f does, so where
// docker compose run without -f would merge an override beside it, the
// rule does not say which of the two it means and is refused. With the
// override in overrides: it reads both merged, and a file no name finds,
// or one another default name passes over, reads alone. Planted break:
// alone merging the override for a compose rule rather than refusing.
func TestOverrideBesideRefusesARuleWithoutOverrides(t *testing.T) {
	main := "services:\n  web:\n    image: w\n"
	files := map[string]string{
		"compose.yaml": main, "compose.override.yaml": "services:\n  web:\n    image: o\n",
		"compose.prod.yaml": main, "docker-compose.yml": main,
	}
	refused(t, files, Spec{Service: "web", Key: "image"},
		"docker compose run without -f merges compose.override.yaml over compose.yaml; list it in overrides:")
	cases := []struct {
		s    Spec
		want string
	}{
		{Spec{Overrides: []string{"compose.override.yaml"}, Service: "web", Key: "image"}, "image=o@compose.override.yaml:3"},
		{Spec{OneFile: oneFile("compose.prod.yaml"), Service: "web", Key: "image"}, "image=w@compose.prod.yaml:3"},
		{Spec{OneFile: oneFile("docker-compose.yml"), Service: "web", Key: "image"}, "image=w@docker-compose.yml:3"},
	}
	for _, c := range cases {
		if got := mustRead(t, files, c.s); got != c.want {
			t.Errorf("%s: got %s, want %s", c.s.File, got, c.want)
		}
	}
}

// overrides: takes three forms. Absent, the rule names its file as -f
// does and is refused beside an override, the message naming both ways
// out. An empty list reads the file alone, as docker compose -f does,
// beside an override and with COMPOSE_FILE in the .env file alike. A list
// of files reads them merged. Planted breaks: Build reading an empty list
// as an absent key; Build reading an absent key as an empty list; Build
// dropping the listed overrides; the refusal not naming overrides: [].
func TestOverridesAbsentEmptyOrListed(t *testing.T) {
	main := "services:\n  web:\n    image: w\n"
	files := map[string]string{"compose.yaml": main, "compose.override.yaml": "services:\n  web:\n    image: o\n"}
	refused(t, files, Spec{Service: "web", Key: "image"},
		"list it in overrides: to read the two merged, or write overrides: [] to read compose.yaml alone as -f does")
	if got := mustRead(t, files, Spec{Overrides: []string{}, Service: "web", Key: "image"}); got != "image=w@compose.yaml:3" {
		t.Errorf("overrides: []: %s", got)
	}
	if got := mustRead(t, files, Spec{Overrides: []string{"compose.override.yaml"}, Service: "web", Key: "image"}); got != "image=o@compose.override.yaml:3" {
		t.Errorf("overrides: [compose.override.yaml]: %s", got)
	}
	cf := map[string]string{".env": "COMPOSE_FILE=compose.yaml:o.yaml\n", "compose.yaml": main, "o.yaml": "services:\n  web:\n    image: o\n"}
	refused(t, cf, Spec{Service: "web", Key: "image"}, "or write overrides: [] to read compose.yaml alone as -f does")
	if got := mustRead(t, cf, Spec{Overrides: []string{}, Service: "web", Key: "image"}); got != "image=w@compose.yaml:3" {
		t.Errorf("overrides: [] with COMPOSE_FILE: %s", got)
	}
}

// COMPOSE_FILE in the .env file beside the main file chooses the files
// docker compose run without -f loads, so Every and a compose rule with no
// overrides: are refused, though the .env file sets it from the shell. A
// rule with overrides: reads, as docker compose -f ignores it. Planted
// breaks: alone not refusing COMPOSE_FILE; dotEnv asking the variables it
// keeps rather than every one the file sets.
func TestComposeFileInDotEnvRefused(t *testing.T) {
	main := "services:\n  web:\n    image: w\n    environment: [FOO=1]\n"
	for _, env := range []string{"COMPOSE_FILE=compose.yaml:o.yaml\n", "COMPOSE_FILE=${CF}\n"} {
		files := map[string]string{".env": env, "compose.yaml": main, "o.yaml": "services:\n  web:\n    environment: [FOO=2]\n"}
		const want = ".env sets COMPOSE_FILE, so docker compose run without -f loads the files it names"
		if _, err := readEvery(t, files, "compose.yaml", "FOO"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Every with %q: %v", env, err)
		}
		refused(t, files, Spec{Service: "web", Key: "environment.FOO"}, want)
		s := Spec{Overrides: []string{"o.yaml"}, Service: "web", Key: "environment.FOO"}
		if got := mustRead(t, files, s); got != "environment.FOO=2@o.yaml:3" {
			t.Errorf("with overrides: and %q: %s", env, got)
		}
	}
}

// A link beside the main file that leads out of the repository, at a name
// docker compose looks for, is refused rather than taken for no file:
// docker compose would follow it. Planted break: firstThere taking any
// error for a file not there.
func TestLinkOutBesideRefused(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "o.yaml"), []byte("services:\n  web:\n    environment: [FOO=2]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"compose.override.yaml", "compose.yaml"} {
		root := extracttest.Repo(t, map[string]string{"docker-compose.yml": "services:\n  web:\n    image: w\n    environment: [FOO=1]\n"})
		extracttest.Symlink(t, filepath.Join(outside, "o.yaml"), filepath.Join(root, name))
		x, err := Every("docker-compose.yml", "FOO", 0, yamlParse)
		if err != nil {
			t.Fatal(err)
		}
		if vs, err := x.Extract(t.Context(), root); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: got %s, %v; want the link refused", name, show(vs), err)
		}
	}
}
