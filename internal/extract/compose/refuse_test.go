package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/consts"
	"github.com/mattn/go-shellwords"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// refused fails the test unless reading s in files is refused with an
// error that holds want.
func refused(t *testing.T, files map[string]string, s Spec, want string) {
	t.Helper()
	got, err := read(t, files, s)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, %v; want a refusal naming %q", got, err, want)
	}
}

// A path an include:, an extends: file or an env_file: names outside the
// repository is refused before compose-go runs, and so is a remote one,
// an absolute one, a path under ~ and an include's project_directory.
// Planted breaks: inside not refusing a path that climbs out with ..;
// envFilePath not refusing it.
func TestPathsOutsideRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: w\n"
	cases := []struct {
		name, main, want string
	}{
		{"include climbs out", "include: [../x.yaml]\n" + svc, `names "../x.yaml", which leaves the repository`},
		{"include via a variable", "include: [\"${UP}/x.yaml\"]\n" + svc, `names "../x.yaml", which leaves the repository`},
		{"extends climbs out", "services:\n  web:\n    extends: {file: ../b.yaml, service: b}\n", `names "../b.yaml", which leaves the repository`},
		{"absolute", "include: [/etc/x.yaml]\n" + svc, "an absolute path, outside the repository"},
		{"home", "include: [\"~/x.yaml\"]\n" + svc, "an absolute path, outside the repository"},
		{"remote", "include: [\"oci://example.org/x\"]\n" + svc, "a remote resource"},
		{"url", "include: [\"https://example.org/x.yaml\"]\n" + svc, "a remote resource"},
		{"git over ssh", "include: [\"git@github.com:a/b.git\"]\n" + svc, `names "git@github.com:a/b.git", a remote resource`},
		{"project_directory", "include:\n  - path: x.yaml\n    project_directory: ..\n" + svc, "project_directory"},
		{"include env_file", "include:\n  - path: x.yaml\n    env_file: ../.env\n" + svc, `names "../.env", which leaves the repository`},
		{"service env_file", "services:\n  web:\n    image: w\n    env_file: ../w.env\n", "the env file \"../w.env\" lies outside the repository"},
		{"extends of an extends file climbs out", "services:\n  web:\n    extends: {file: x.yaml, service: b}\n", `names "../c.yaml", which leaves the repository`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refused(t, map[string]string{"compose.yaml": c.main, "x.yaml": svc + "  b:\n    extends: {file: ../c.yaml, service: c}\n", ".env": "UP=..\n"}, Spec{Service: "web", Key: "environment"}, c.want)
		})
	}
}

// A link that leads out of the repository is refused as the file it
// stands for, a project's .env too, before compose-go reads it. Planted
// break: dotEnv taking any error, the link's among them, as a file not
// there.
func TestLinkOutRefused(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.env"), []byte("TAG=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := extracttest.Repo(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: w:${TAG}\n"})
	if err := os.Symlink(filepath.Join(outside, "secret.env"), filepath.Join(root, ".env")); err != nil {
		t.Fatal(err)
	}
	x, err := Build(Spec{OneFile: oneFile("compose.yaml"), Service: "web", Key: "image"}, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), root)
	if err == nil || strings.Contains(show(vs), "leaked") {
		t.Errorf("got %s, %v; want the link refused", show(vs), err)
	}
}

// The guard, compose-go's one resource loader, gives the file the walker
// routed the path to in the load of the file compose-go is loading, and
// refuses any other, whatever compose-go asks: the same path in the load
// of another file is not routed. Planted break: Load joining an unrouted
// path to the stage, which gave /r/etc/passwd.
func TestGuardRefusesWhatWasNotRouted(t *testing.T) {
	g := &guard{stage: "/r", routes: map[route]string{{"/r/a/compose.yaml", "b/x.yaml"}: "a/b/x.yaml"}}
	ctx := context.WithValue(t.Context(), consts.ComposeFileKey{}, "/r/a/compose.yaml")
	if got, err := g.Load(ctx, "b/x.yaml"); err != nil || got != "/r/a/b/x.yaml" || g.Dir("b/x.yaml") != "/r/a/b" || g.Dir(got) != "/r/a/b" {
		t.Errorf("Load b/x.yaml: %q, %v, Dir %q", got, err, g.Dir("b/x.yaml"))
	}
	for _, p := range []string{"/etc/passwd", "../../etc/passwd", "c.yaml", "/r/a/b/x.yaml"} {
		if got, err := g.Load(ctx, p); err == nil || !strings.Contains(err.Error(), "is no file errata read and routed") {
			t.Errorf("Load %s: %q, %v; want it refused", p, got, err)
		}
	}
	other := context.WithValue(t.Context(), consts.ComposeFileKey{}, "/r/a/b/x.yaml")
	if got, err := g.Load(other, "b/x.yaml"); err == nil {
		t.Errorf("Load b/x.yaml in the load of another file: %q, want it refused", got)
	}
	if got, err := g.Load(t.Context(), "b/x.yaml"); err == nil {
		t.Errorf("Load with no file loading: %q, want it refused", got)
	}
	if !g.Accept("anything") {
		t.Error("the guard turned a path away, which compose-go's own loader would then read")
	}
}

// An included project's file, whose paths compose-go resolves, may not
// name a path under ~, which it expands from the process's HOME, nor a
// develop.watch path that climbs out; the main project's file, whose paths
// it leaves as written, may. Planted break: checkHome checking no file.
func TestHomeInResolvedFilesRefused(t *testing.T) {
	main := "include: [sub/compose.yaml]\nservices:\n  web:\n    image: w\n    volumes: [\"~/data:/data\"]\n"
	refused(t, map[string]string{"compose.yaml": main, "sub/compose.yaml": "services:\n  db:\n    image: d\n    volumes: [\"~/db:/db\"]\n"},
		Spec{Service: "web", Key: "image"}, `"~/db:/db" starts with ~`)
	refused(t, map[string]string{"compose.yaml": main, "sub/compose.yaml": "services:\n  db:\n    image: d\n    volumes: [\"${H}/db:/db\"]\n", "sub/.env": "H=~\n"},
		Spec{Service: "web", Key: "image"}, `"${H}/db:/db" starts with ~`)
	refused(t, map[string]string{"compose.yaml": main, "sub/compose.yaml": "services:\n  db:\n    image: d\n    develop:\n      watch:\n        - {path: ../../x, action: sync, target: /x}\n"},
		Spec{Service: "web", Key: "image"}, "the develop.watch path")
	got := mustRead(t, map[string]string{"compose.yaml": main, "sub/compose.yaml": "services:\n  db:\n    image: d\n"}, Spec{Service: "web", Key: "volumes.0.source"})
	if got != "volumes.0.source=~/data@compose.yaml:5" {
		t.Errorf("the main file's ~ path: %s", got)
	}
}

// A file that does not parse, that holds two documents, whose top level is
// no mapping, or that compose-go refuses, makes the value unknown; so does
// a service the project does not have, a variable it requires and does
// not set, a file errata reads before compose-go that is not there, an
// include it cannot read, a file over max_bytes and files over it in all,
// which alone say "in all". Planted breaks: the walker going on past a
// missing extends file, or an include's missing env file, which compose-go
// then refused with another message.
func TestUnreadableRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: w\n"
	cases := []struct {
		name  string
		files map[string]string
		s     Spec
		want  string
	}{
		{"yaml", map[string]string{"compose.yaml": "services: [\n"}, Spec{Service: "web", Key: "image"}, "compose.yaml"},
		{"two documents", map[string]string{"compose.yaml": "services: {}\n---\nservices: {}\n"}, Spec{Service: "web", Key: "image"}, "holds 2 YAML documents"},
		{"no mapping", map[string]string{"compose.yaml": "- a\n"}, Spec{Service: "web", Key: "image"}, "the top level of a Compose file is a mapping"},
		{"schema", map[string]string{"compose.yaml": "services:\n  web:\n    image: [1]\n"}, Spec{Service: "web", Key: "image"}, "compose.yaml: validating"},
		{"no service", map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n"}, Spec{Service: "api", Key: "image"}, `the project has no service "api"`},
		{"required", map[string]string{"compose.yaml": "services:\n  web:\n    image: ${TAG:?set TAG}\n"}, Spec{Service: "web", Key: "image"}, "set TAG"},
		{"include missing", map[string]string{"compose.yaml": "include: [x.yaml]\nservices:\n  web:\n    image: w\n"}, Spec{Service: "web", Key: "image"}, "read x.yaml: no such file"},
		{"extends file missing", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: b.yaml, service: b}\n"}, Spec{Service: "web", Key: "image"}, "read b.yaml: no such file"},
		{"include env_file missing", map[string]string{"compose.yaml": "include:\n  - path: x.yaml\n    env_file: x.env\n" + svc, "x.yaml": svc}, Spec{Service: "web", Key: "image"}, "read x.env: no such file"},
		{"include no path", map[string]string{"compose.yaml": "include:\n  - env_file: x.env\n" + svc}, Spec{Service: "web", Key: "image"}, "an include entry names no path"},
		{"include path a mapping", map[string]string{"compose.yaml": "include:\n  - path: {a: b}\n" + svc}, Spec{Service: "web", Key: "image"}, "write a path or a list of paths"},
		{"include path a nested list", map[string]string{"compose.yaml": "include:\n  - path: [[a]]\n" + svc}, Spec{Service: "web", Key: "image"}, "write a path or a list of paths"},
		{"one file too large", map[string]string{"compose.yaml": svc}, Spec{OneFile: oneFileMax(20), Service: "web", Key: "image"}, "set max_bytes: on the extractor to read a larger file"},
		{"max_bytes", map[string]string{"compose.yaml": "include: [x.yaml]\nservices:\n  web:\n    image: w\n", "x.yaml": "services:\n  db:\n    image: " + strings.Repeat("d", 64) + "\n"},
			Spec{OneFile: oneFileMax(80), Service: "web", Key: "image"}, "hold at most max_bytes, 80, in all"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { refused(t, c.files, c.s, c.want) })
	}
}

// A key the service does not hold gives no value, which the rule decides
// on, and an index past a list's end is no key it holds.
func TestAbsentKeyGivesNone(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    command: [a]\n"}
	for _, key := range []string{"user", "command.1", "command.x", "image.name", "command.01"} {
		if got := mustRead(t, files, Spec{Service: "web", Key: key}); got != "" {
			t.Errorf("%s: %s, want none", key, got)
		}
	}
}

// A load refuses to start when go-shellwords, which compose-go splits a
// command with, is set to run a backtick or read the process's
// environment. Planted break: the test asking for both switches at once.
func TestShellwordsSwitchesRefused(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    command: echo `id`\n"}
	for _, set := range []*bool{&shellwords.ParseBacktick, &shellwords.ParseEnv} {
		*set = true
		refused(t, files, Spec{Service: "web", Key: "command"}, "go-shellwords is set to run backticks or read the environment")
		*set = false
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "command"}); got != "command.0=echo@compose.yaml:4; command.1=`id`@compose.yaml:4" {
		t.Errorf("the command split: %s", got)
	}
}

// A path that chooses a file, an include's, an extends file's or a service
// env_file's, and an extends service name, are refused when they look up a
// variable no file errata read sets, in any form and nested, naming the
// text and the variable: docker compose would take it from its shell, and
// so read another file than the default names. A variable an included
// project's env file sets from the shell counts as unset. Planted breaks:
// follow, chain and envFilesFromShell not refusing; shellVar recording no
// variable.
func TestPathFromTheShellRefused(t *testing.T) {
	ext := func(file string) string {
		return "services:\n  web:\n    extends: {file: \"" + file + "\", service: s}\n"
	}
	cases := []struct {
		name, main, key, want string
	}{
		{"include", "include: [\"${ENV:-dev}/c.yaml\"]\n", "image", `the path "${ENV:-dev}/c.yaml" looks up ENV`},
		{":-", ext("${X:-a}.yaml"), "image", `the path "${X:-a}.yaml" looks up X`},
		{"-", ext("${X-a}.yaml"), "image", `looks up X`},
		{":+", ext("${X:+b}a.yaml"), "image", `looks up X`},
		{"+", ext("${X+b}a.yaml"), "image", `looks up X`},
		{"?", ext("${X?no}a.yaml"), "image", `looks up X`},
		{"nested", ext("${SET:+${B}}a.yaml"), "image", `the path "${SET:+${B}}a.yaml" looks up B`},
		{"extends service", "services:\n  web:\n    extends: {file: a.yaml, service: \"s${S-x}\"}\n", "image", `line 3: compose.yaml: the extends service "s${S-x}" looks up S`},
		{"env_file", "services:\n  web:\n    image: w\n    env_file: \"${EF:-a.env}\"\n", "environment", `the env_file path "${EF:-a.env}" looks up EF`},
		{"env_file mapping", "services:\n  web:\n    image: w\n    env_file:\n      - a.env\n      - path: \"${EF:-a.env}\"\n", "environment.A", `line 6: compose.yaml: the env_file path "${EF:-a.env}" looks up EF`},
		{"include env from the shell", "include:\n  - path: i/c.yaml\n    env_file: i/e.env\n", "image", `i/c.yaml: the path "${TAG-a}.yaml" looks up TAG`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refused(t, map[string]string{
				"compose.yaml": c.main, ".env": "SET=1\n", "a.env": "A=1\n",
				"a.yaml":     "services:\n  s:\n    image: from-a\n  sx:\n    image: from-sx\n",
				"dev/c.yaml": "services:\n  web:\n    image: from-dev\n",
				"i/e.env":    "TAG=${NOPE}\n",
				"i/c.yaml":   ext("${TAG-a}.yaml"), "i/a.yaml": "services:\n  s:\n    image: from-a\n",
			}, Spec{Service: "web", Key: c.key}, c.want)
		})
	}
}

// A build arg whose name looks up a variable no file sets, an item with no
// = or with a $ before it, is refused when the rule reads the build args:
// docker compose would pass the arg the shell names, so errata can say of
// no arg that it is absent. A rule that reads another key, and an item
// whose $ stands in its value, still read. Planted break: namedFromShell
// not refusing.
func TestBareArgFromTheShellRefused(t *testing.T) {
	svc := func(item string) map[string]string {
		return map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      args:\n        - " + item + "\n"}
	}
	for _, item := range []string{"${X}", "${X:-NPM_TOKEN}", "${X:-FOO}=1"} {
		refused(t, svc(item), Spec{Service: "web", Key: "build.args"}, `line 7: compose.yaml: the build.args item "`+item+`" looks up X`)
		refused(t, svc(item), Spec{Service: "web", Key: "build"}, `looks up X`)
		if got := mustRead(t, svc(item), Spec{Service: "web", Key: "image"}); got != "image=w@compose.yaml:3" {
			t.Errorf("%s, image: %s", item, got)
		}
	}
	if got := mustRead(t, svc("A=${X}"), Spec{Service: "web", Key: "build.args"}); got != "build.args.A=A=${X}@compose.yaml:7!" {
		t.Errorf("a $ in the value: %s", got)
	}
}

// An item of a keyed list (keyedLists), such as environment:, whose name
// looks up a variable no file sets is refused when the rule reads that
// list or a key that holds it, naming the file and line that write it:
// docker compose would set the key the shell names, so errata can say of
// no key that it is absent. So is a list item of a hook, a pre_start
// hook's list the service writes, which the hook inherits, and an extra
// host whose host looks one up, since the hosts order the list. A read of another key, an item
// whose $ stands in its value, and a key of the mapping form, which
// compose-go does not interpolate, still read. Planted breaks:
// keysFromShell matching no key that holds the list; keyedLists without
// environment; nameOf ending a name at = alone; covers taking a [] step as
// a name; namedFromShell not checking the service's list for a hook.
func TestKeyFromTheShellRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: w\n"
	cases := []struct{ body, key, want string }{
		{"    environment:\n      - \"${X:-A}=1\"\n", "environment.B", `line 5: compose.yaml: the environment item "${X:-A}=1" looks up X`},
		{"    environment: [\"${X:-A}\"]\n", "environment", `the environment item "${X:-A}" looks up X`},
		{"    labels: [\"${X:-a}=1\"]\n", "labels.b", `the labels item "${X:-a}=1" looks up X`},
		{"    annotations: [\"${X:-a}=1\"]\n", "annotations", `the annotations item "${X:-a}=1" looks up X`},
		{"    sysctls: [\"${X:-a}=1\"]\n", "sysctls", `the sysctls item "${X:-a}=1" looks up X`},
		{"    extra_hosts: [\"${X:-h}:10.0.0.1\"]\n", "extra_hosts", `the extra_hosts item "${X:-h}:10.0.0.1" looks up X`},
		{"    build: {context: ., labels: [\"${X:-a}=1\"]}\n", "build", `the build.labels item "${X:-a}=1" looks up X`},
		{"    build: {context: ., extra_hosts: [\"${X:-h}:10.0.0.1\"]}\n", "build.extra_hosts", `the build.extra_hosts item "${X:-h}:10.0.0.1" looks up X`},
		{"    deploy: {labels: [\"${X:-a}=1\"]}\n", "deploy", `the deploy.labels item "${X:-a}=1" looks up X`},
		{"    networks: [\"${X:-a}\"]\nnetworks: {a: {}, b: {}}\n", "networks.b", `the networks item "${X:-a}" looks up X`},
		{"    depends_on: [\"${X:-db}\"]\n  db:\n    image: d\n", "depends_on", `the depends_on item "${X:-db}" looks up X`},
		{"    build: {context: ., additional_contexts: [\"${X:-a}=/p\"]}\n", "build.additional_contexts.b", `the build.additional_contexts item "${X:-a}=/p" looks up X`},
		{"    build: {context: ., ssh: [\"${X:-a}=/p\"]}\n", "build", `the build.ssh item "${X:-a}=/p" looks up X`},
		{"    post_start: [{command: x, environment: [\"${X:-A}=1\"]}]\n", "post_start.0.environment.B", `the post_start.environment item "${X:-A}=1" looks up X`},
		{"    environment: [\"${X:-A}=1\"]\n    pre_start: [{command: x}]\n", "pre_start.0.environment", `the pre_start.environment item "${X:-A}=1" looks up X`},
		{"    extra_hosts: [\"${X:-h}=10.0.0.1\"]\n", "extra_hosts.0", `the extra_hosts item "${X:-h}=10.0.0.1" looks up X`},
	}
	for _, c := range cases {
		files := map[string]string{"compose.yaml": svc + c.body}
		refused(t, files, Spec{Service: "web", Key: c.key}, c.want)
		if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w@compose.yaml:3" {
			t.Errorf("%s, image: %s", c.key, got)
		}
	}
	base := map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: b.yaml, service: b}\n", "b.yaml": "services:\n  b:\n    image: w\n    environment: [\"${X:-A}=1\"]\n"}
	refused(t, base, Spec{Service: "web", Key: "environment"}, `line 4: b.yaml: the environment item "${X:-A}=1" looks up X`)
	files := map[string]string{"compose.yaml": svc + "    environment:\n      - A=${X}\n      - ${B}=2\n", ".env": "B=C\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "environment"}); got != "environment.A=A=${X}@compose.yaml:5!; environment.C=2@compose.yaml:6" {
		t.Errorf("a $ in the value, a name the files set: %s", got)
	}
	files = map[string]string{"compose.yaml": svc + "    environment:\n      \"${X}\": 1\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "environment"}); got != "environment.${X}=1@compose.yaml:5" {
		t.Errorf("a mapping key: %s", got)
	}
}

// The env reader refuses a service whose environment: names a variable
// through one no file sets, whichever variable it reads: the shell might
// name that one. Planted break: variable not asking keysFromShell.
func TestEveryKeyFromTheShellRefused(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    environment: [\"${X:-A}=1\"]\n  db:\n    image: d\n    environment: [PORT=1]\n"}
	x, err := Every("compose.yaml", "PORT", 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), extracttest.Repo(t, files))
	if err == nil || !strings.Contains(err.Error(), `the environment item "${X:-A}=1" looks up X`) {
		t.Errorf("got %s, %v; want the name from the shell refused", show(vs), err)
	}
}

// A build arg item with no = whose name the files' variables give is the
// arg of that name, not literal, at the item's line, with the item as its
// text: compose-go drops it and docker compose takes it from its shell. A
// $$ is a $ of the name. With the arg set in .env it takes that value. An
// item whose name the files make empty names no arg. Planted breaks:
// bareArg not interpolating the item; itemNamed matching the item as
// written; bareArg taking an empty name.
func TestBareArgNamedByTheFiles(t *testing.T) {
	svc := func(item, env string) map[string]string {
		return map[string]string{".env": env, "compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      args:\n        - " + item + "\n"}
	}
	cases := []struct{ item, env, key, want string }{
		{"${A}", "A=FOO\n", "build.args", "build.args.FOO=${A}@compose.yaml:7!"},
		{"${A:-BAR}", "A=FOO\n", "build.args.FOO", "build.args.FOO=${A:-BAR}@compose.yaml:7!"},
		{"${A}", "A=FOO\nFOO=1\n", "build.args", "build.args.FOO=1@compose.yaml:7"},
		{"$${A}", "", "build.args", "build.args.${A}=$${A}@compose.yaml:7!"},
		{"${A}", "A=\n", "build.args", ""},
	}
	for _, c := range cases {
		if got := mustRead(t, svc(c.item, c.env), Spec{Service: "web", Key: c.key}); got != c.want {
			t.Errorf("%s with %q: got %s, want %s", c.item, c.env, got, c.want)
		}
	}
}

// An x- key of a service reads as any other key, merged over by an
// override, and not literal where it names a variable no file sets, or
// where the project has an override and a file of it names one (the
// taint):
// compose-go keeps such keys aside from the configuration its JSON writes.
// Planted break: serviceTree leaving the extensions out.
func TestExtensionKeysRead(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: w\n    x-team: a\n    x-meta: {tier: \"${T:-1}\"}\n",
		"o.yaml":       "services:\n  web:\n    x-team: b\n",
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "x-team"}); got != "x-team=a@compose.yaml:4" {
		t.Errorf("x-team: %s", got)
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "x-team", Overrides: []string{"o.yaml"}}); got != "x-team=b@o.yaml:3!" {
		t.Errorf("x-team overridden: %s", got)
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "x-meta"}); got != "x-meta.tier=${T:-1}@compose.yaml:5!" {
		t.Errorf("x-meta: %s", got)
	}
}

// An x- key below the top of a service, in build:, deploy:, healthcheck:
// or an item of a list, reads as the service's own x- keys do, and a read
// of the key that holds it gives it too: compose-go keeps each part's x-
// keys aside from the JSON it writes, beside the part's other keys. With
// the override, compose.yaml's ${F} marks the override's value too.
// Planted breaks: structExtensions not descending into a field;
// listExtensions not descending into an item; addExtensions writing the
// x- keys into a new mapping in place of the part's.
func TestNestedExtensionKeysRead(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      x-foo: \"${F:-1}\"\n    deploy: {x-foo: 2}\n    healthcheck:\n      test: [CMD, x]\n      x-h: 3\n    ports:\n      - {target: 80, x-p: 4}\n",
		"o.yaml":       "services:\n  web:\n    build:\n      x-foo: 5\n",
	}
	cases := []struct {
		key       string
		overrides []string
		want      string
	}{
		{"build.x-foo", nil, "build.x-foo=${F:-1}@compose.yaml:6!"},
		{"build.x-foo", []string{"o.yaml"}, "build.x-foo=5@o.yaml:4!"},
		{"build", nil, "build.context=.@compose.yaml:5; build.dockerfile=Dockerfile@compose.yaml:4!; build.x-foo=${F:-1}@compose.yaml:6!"},
		{"deploy", nil, "deploy.x-foo=2@compose.yaml:7"},
		{"healthcheck.x-h", nil, "healthcheck.x-h=3@compose.yaml:10"},
		{"ports.0.x-p", nil, "ports.0.x-p=4@compose.yaml:12"},
	}
	for _, c := range cases {
		if got := mustRead(t, files, Spec{Service: "web", Key: c.key, Overrides: c.overrides}); got != c.want {
			t.Errorf("%s %v: got %s, want %s", c.key, c.overrides, got, c.want)
		}
	}
}

// A build ssh key reads as a mapping of its id to its path, and a service
// that writes one reads at all: compose-go's JSON of an ssh key is not
// JSON. Planted break: serviceTree keeping build.ssh in what tree
// marshals.
func TestBuildSSHReads(t *testing.T) {
	files := map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    build:\n      context: .\n      ssh: [k=/p]\n"}
	if got := mustRead(t, files, Spec{Service: "web", Key: "build.ssh"}); got != "build.ssh.k=/p@compose.yaml:6" {
		t.Errorf("build.ssh: %s", got)
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w@compose.yaml:3" {
		t.Errorf("image: %s", got)
	}
}

// A read of labels, of a service or of a pre_start hook, is refused when
// the service or the hook names a label_file:, and a read of a hook's
// environment when it names an env_file:, which a hook also inherits from
// its service: compose-go would read those files itself, and errata reads
// neither, so a label or variable one sets would read as absent. Another
// key, and the service's own environment, still read. Planted breaks:
// unreadFiles not checking the service's label_file:; not checking a
// hook's env_file:.
func TestLabelFileRefused(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: w\n    label_file: a.labels\n    env_file: a.env\n    pre_start: [{command: x}]\n",
		"a.labels":     "a=1\n", "a.env": "E=1\n",
	}
	for _, key := range []string{"labels", "labels.a", "pre_start.0.labels", "pre_start"} {
		refused(t, files, Spec{Service: "web", Key: key}, `the label_file "a.labels", which errata does not read`)
	}
	refused(t, files, Spec{Service: "web", Key: "pre_start.0.environment.E"}, `pre_start.0.environment may be set by the env_file "a.env"`)
	if got := mustRead(t, files, Spec{Service: "web", Key: "environment"}); got != "environment.E=1@a.env:1" {
		t.Errorf("environment: %s", got)
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w@compose.yaml:3" {
		t.Errorf("image: %s", got)
	}
}

// A service whose profiles a file writes with a variable no file sets is
// refused, under the profile its default names too: docker compose takes
// the profile from its shell, and with it whether the service is active.
// An override's profile is refused the same way. Planted break: unread not
// asking profilesFromShell.
func TestProfileFromTheShellRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: w\n"
	refused(t, map[string]string{"compose.yaml": svc + "    profiles: [\"${P:-debug}\"]\n"}, Spec{Service: "web", Key: "image", Profiles: []string{"debug"}}, `line 4: compose.yaml: the profile "${P:-debug}" looks up P`)
	over := map[string]string{"compose.yaml": svc + "    profiles: [debug]\n", "o.yaml": "services:\n  web:\n    profiles: [\"${P:-debug}\"]\n"}
	refused(t, over, Spec{Service: "web", Key: "image", Profiles: []string{"debug"}, Overrides: []string{"o.yaml"}}, `o.yaml: the profile "${P:-debug}" looks up P`)
}
