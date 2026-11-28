package compose

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// yamlParse is the parser the facade hands Build.
var yamlParse Parser = func(data []byte, expanded int) ([]*core.Node, int, error) {
	return yaml.ParseNoted(data, expanded, nil)
}

// show writes each value as text@file:line, key=... before it, ! after a
// value not literal, and its origin after it.
func show(vs []check.Value) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		s := fmt.Sprintf("%s=%s@%s:%d", v.Key, v.Text, v.At.File, v.At.Line)
		if v.NotLiteral {
			s += "!"
		}
		if v.Origin != "" {
			s += " " + v.Origin
		}
		out[i] = s
	}
	return strings.Join(out, "; ")
}

// read is what the extractor of s reads in a repository of files.
func read(t *testing.T, files map[string]string, s Spec) (string, error) {
	t.Helper()
	if s.File == "" {
		s.File = "compose.yaml"
	}
	x, err := Build(s, yamlParse)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	vs, err := x.Extract(t.Context(), extracttest.Repo(t, files))
	return show(vs), err
}

// mustRead is read, failing the test on an error.
func mustRead(t *testing.T, files map[string]string, s Spec) string {
	t.Helper()
	got, err := read(t, files, s)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A value of the effective configuration stands at the line of the file
// that set it: an override's over the main file's, the main file's over
// the service it extends from another file, and an included project's
// service at its own file; a value two files set alike stands at the later
// one. Planted breaks: winner taking the first contributor rather than the
// last, which put C at base.yaml; the
// extends chain left out of contributors, which put B at the service's
// line in compose.yaml.
func TestEffectiveValueAtItsLine(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `include:
  - sub/compose.yaml
services:
  web:
    extends:
      file: base.yaml
      service: base
    image: web:${TAG}
    environment:
      A: "1"
      C: "4"
`,
		"base.yaml": `services:
  base:
    image: base
    environment: [B=2, C=4]
`,
		"compose.override.yaml": "services:\n  web:\n    environment:\n      A: \"3\"\n",
		"sub/compose.yaml":      "services:\n  db:\n    image: db:${DBV}\n",
		"sub/.env":              "DBV=16\n",
		".env":                  "TAG=1.2\n",
	}
	cases := []struct {
		service, key, want string
	}{
		{"web", "image", "image=web:1.2@compose.yaml:8"},
		{"web", "environment", "environment.A=3@compose.override.yaml:4; environment.B=2@base.yaml:4; environment.C=4@compose.yaml:11"},
		{"web", "environment.B", "environment.B=2@base.yaml:4"},
		{"db", "image", "image=db:16@sub/compose.yaml:3"},
	}
	for _, c := range cases {
		got := mustRead(t, files, Spec{Overrides: []string{"compose.override.yaml"}, Service: c.service, Key: c.key})
		if got != c.want {
			t.Errorf("%s %s:\n got %s\nwant %s", c.service, c.key, got, c.want)
		}
	}
}

// A list compose-go appends across files puts each item at the file that
// wrote it, whatever its place in the merged list, and a short form, such
// as a port "8080:80", puts every value below it at its scalar. Planted
// break: ownStep matching an item by its index, which put the override's
// port at the main file's.
func TestListItemsAtTheirFile(t *testing.T) {
	files := map[string]string{
		"compose.yaml":  "services:\n  web:\n    image: w\n    ports:\n      - \"8080:80\"\n",
		"override.yaml": "services:\n  web:\n    ports:\n      - target: 443\n        published: \"8443\"\n",
	}
	got := mustRead(t, files, Spec{Overrides: []string{"override.yaml"}, Service: "web", Key: "ports"})
	want := "ports.0.mode=ingress@compose.yaml:5; ports.0.protocol=tcp@compose.yaml:5; ports.0.published=8080@compose.yaml:5; ports.0.target=80@compose.yaml:5; " +
		"ports.1.mode=ingress@override.yaml:4; ports.1.protocol=tcp@override.yaml:4; ports.1.published=8443@override.yaml:5; ports.1.target=443@override.yaml:4"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A key of a label holds dots, and a key reads it whole, the longest key
// the mapping holds first. Planted break: mapKey trying the shortest run
// first, which took the label "com" and found nothing below it.
func TestKeyWithDots(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    labels:\n      - com=x\n      - com.example.team=core\n"}
	got := mustRead(t, files, Spec{Service: "web", Key: "labels.com.example.team"})
	if want := "labels.com.example.team=core@compose.yaml:6"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// A variable the .env files do not set makes the value not literal, with
// the text the file wrote, even when the process's environment sets it and
// even with a default: compose-go never sees the process's environment.
// One the .env file sets is literal, with its value. Planted break: the
// project's Environment given TAG=env beside the .env file's variables.
func TestInterpolationFromDotEnvOnly(t *testing.T) {
	t.Setenv("TAG", "env")
	t.Setenv("PORT", "1")
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: web:${TAG:-latest}\n    command: run ${KNOWN}\n    environment:\n      - FROM_SHELL\n",
		".env":         "KNOWN=yes\n",
	}
	cases := map[string]string{
		"image":       "image=web:${TAG:-latest}@compose.yaml:3!",
		"command":     "command.0=run@compose.yaml:4; command.1=yes@compose.yaml:4",
		"environment": "environment.FROM_SHELL=FROM_SHELL@compose.yaml:6!",
	}
	for key, want := range cases {
		if got := mustRead(t, files, Spec{Service: "web", Key: key}); got != want {
			t.Errorf("%s: got %s, want %s", key, got, want)
		}
	}
}

// A service a profile gates is refused until the rule names the profile,
// or the .env file's COMPOSE_PROFILES does. Planted break: Profiles not
// passed to compose-go, which refused the service under every profile.
func TestProfiles(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  debug:\n    image: dbg\n    profiles: [tools]\n"}
	_, err := read(t, files, Spec{Service: "debug", Key: "image"})
	if err == nil || !strings.Contains(err.Error(), "not active under the profiles given; its profiles are tools") {
		t.Errorf("no profile: %v", err)
	}
	if got := mustRead(t, files, Spec{Service: "debug", Key: "image", Profiles: []string{"tools"}}); got != "image=dbg@compose.yaml:3" {
		t.Errorf("profile tools: %s", got)
	}
	files[".env"] = "COMPOSE_PROFILES=other,tools\n"
	if got := mustRead(t, files, Spec{Service: "debug", Key: "image"}); got != "image=dbg@compose.yaml:3" {
		t.Errorf("COMPOSE_PROFILES: %s", got)
	}
}

// environment: takes what env_file: sets beneath it, each variable at the
// env file's line, and a variable environment: sets, even with no value,
// over it. Planted break: envFileValues not skipping what environment:
// sets, which gave A twice.
func TestEnvFileUnderEnvironment(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: w\n    env_file: [web.env, missing.env]\n    environment:\n      A: \"1\"\n",
		"web.env":      "# web\nA=0\nexport B=${UNSET}\nC: three\nC=3\n",
	}
	files["compose.yaml"] = strings.Replace(files["compose.yaml"], "missing.env]", "{path: missing.env, required: false}]", 1)
	got := mustRead(t, files, Spec{Service: "web", Key: "environment"})
	want := "environment.A=1@compose.yaml:6; environment.B=${UNSET}@web.env:3!; environment.C=3@web.env:5"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "environment.C"}); got != "environment.C=3@web.env:5" {
		t.Errorf("environment.C: %s", got)
	}
}

// Every gives each service's value of the variable, active or not, by the
// services' names, from environment: or else env_file:, with the service
// as its origin; a service that sets none gives none.
func TestEvery(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: w\n    environment: [PORT=80]\n  api:\n    image: a\n    env_file: api.env\n    profiles: [x]\n  db:\n    image: d\n",
		"api.env":      "PORT=81\n",
	}
	x, err := Every("compose.yaml", "PORT", 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), extracttest.Repo(t, files))
	if err != nil {
		t.Fatal(err)
	}
	want := `=81@api.env:1 service "api"; =80@compose.yaml:4 service "web"`
	if got := show(vs); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func oneFile(file string) core.OneFile { return core.OneFile{File: file} }

func oneFileMax(n int64) core.OneFile { return core.OneFile{File: "compose.yaml", MaxBytes: n} }

// A scalar stands at its own line, which may be below its key's. Planted
// break: lineOf giving a scalar its key's line.
func TestScalarAtItsOwnLine(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image:\n      w\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w@compose.yaml:4" {
		t.Errorf("got %s", got)
	}
}

// The variables of an env file come in the order of their lines, not of
// their names. Planted break: the comparison of envFileLines turned round.
func TestEnvFileInLineOrder(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: w\n    env_file: web.env\n",
		"web.env":      "D=1\nC=2\nB=3\nA=4\nE=5\n",
	}
	want := "environment.D=1@web.env:1; environment.C=2@web.env:2; environment.B=3@web.env:3; environment.A=4@web.env:4; environment.E=5@web.env:5"
	if got := mustRead(t, files, Spec{Service: "web", Key: "environment"}); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// ExtractRead gives the bytes of every file the project read, for env's
// max_bytes. Planted break: it giving what was left.
func TestExtractReadCountsBytes(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n", ".env": "A=1\n"}
	x, err := Build(Spec{OneFile: oneFileMax(1000), Service: "web", Key: "image"}, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	if _, n, err := x.(extractor).ExtractRead(t.Context(), extracttest.Repo(t, files)); err != nil || n != int64(len(files["compose.yaml"])+len(files[".env"])) {
		t.Errorf("read %d bytes, %v", n, err)
	}
}

// A port range writes a port for each number in it, and each stands at the
// item that wrote it, as does every port after it. Planted break: rawStep
// not asking portItem, which put all three ports at the key's line.
func TestPortRangeItemsAtTheirLines(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    ports:\n      - \"8000-8001:80-81\"\n      - \"9000:90\"\n      - target: 100\n", ".env": "P=1\n"}
	want := "ports.0.target=80@compose.yaml:5; ports.1.target=81@compose.yaml:5; ports.2.target=90@compose.yaml:6; ports.3.target=100@compose.yaml:7"
	got := []string{}
	for i := range 4 {
		got = append(got, mustRead(t, files, Spec{Service: "web", Key: fmt.Sprintf("ports.%d.target", i)}))
	}
	if strings.Join(got, "; ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "; "), want)
	}
	files["compose.yaml"] = "services:\n  web:\n    image: w\n    ports:\n      - \"${P}000-${P}001:80-81\"\n      - \"9000:90\"\n"
	if got := mustRead(t, files, Spec{Service: "web", Key: "ports.2.target"}); got != "ports.2.target=90@compose.yaml:6" {
		t.Errorf("a range with a variable: %s", got)
	}
}

// A build arg written without a value, which the .env files do not set,
// is not literal, at its line, with its name as its text, as an
// environment: name alone is; compose-go drops it. One the .env file sets
// takes its value. Planted break: values not adding the bare args.
func TestBareBuildArgNotLiteral(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      args: [TAG, K=v]\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "build.args"}); got != "build.args.K=v@compose.yaml:6; build.args.TAG=TAG@compose.yaml:6!" {
		t.Errorf("a list: %s", got)
	}
	files["compose.yaml"] = "services:\n  web:\n    image: w\n    build:\n      context: .\n      args:\n        TAG:\n"
	if got := mustRead(t, files, Spec{Service: "web", Key: "build.args.TAG"}); got != "build.args.TAG=@compose.yaml:7!" {
		t.Errorf("a mapping: %s", got)
	}
	files[".env"] = "TAG=dot\n"
	if got := mustRead(t, files, Spec{Service: "web", Key: "build.args"}); got != "build.args.TAG=dot@compose.yaml:7" {
		t.Errorf("set by .env: %s", got)
	}
}

// A bare arg is one compose-go's merge keeps: an item that holds a $ is
// the text it interpolates to, ${X} with X=A=1 the arg A, and invents no
// arg ${X}; an override's !reset takes a bare arg away, as compose-go
// does. Planted break: addBare adding every contributor's name without
// asking compose-go.
func TestBareArgsAsComposeGoMerges(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      args:\n        - ${X}\n", ".env": "X=A=1\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "build.args"}); got != "build.args.A=1@compose.yaml:7" {
		t.Errorf("an interpolated item: %s", got)
	}
	files = map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      args: [TAG]\n",
		"o.yaml": "services:\n  web:\n    build:\n      args: !reset []\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "build.args", Overrides: []string{"o.yaml"}}); got != "" {
		t.Errorf("reset in an override: %s", got)
	}
}

// A value an included project's file writes with a variable its env file
// sets from the shell is not literal and shows the text the file wrote,
// though compose-go sets the variable empty there and so takes no default.
// Planted break: ownServices loading an included file with errata's
// variables, not compose-go's.
func TestIncludedNotLiteralAsWritten(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "include:\n  - path: i/c.yaml\n    env_file: i/e.env\n",
		"i/e.env":      "TAG=${NOPE}\n",
		"i/c.yaml":     "services:\n  web:\n    image: w:${TAG-unset}\n",
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w:${TAG-unset}@i/c.yaml:3!" {
		t.Errorf("got %s", got)
	}
}
