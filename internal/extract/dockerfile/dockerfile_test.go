package dockerfile

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// read is the values spec selects in data, as the file Dockerfile unless
// spec names another, each written key=text@line, with a ! before a value
// marked not literal, and the error.
func read(t *testing.T, s Spec, data string) (string, error) {
	t.Helper()
	if s.File == "" {
		s.File = "Dockerfile"
	}
	x, err := Build(s)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	vs, err := x.(core.EntryExtractor).FromData([]byte(data))
	return show(vs), err
}

// show writes values as read does.
func show(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		s := fmt.Sprintf("%s@%d", v.Text, v.At.Line)
		if v.HasKey {
			s = v.Key + "=" + s
		}
		if v.NotLiteral {
			s = "!" + s
		}
		if v.Origin != "" {
			s += " [" + v.Origin + "]"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// mustRead is read for a read that must succeed.
func mustRead(t *testing.T, s Spec, data string) string {
	t.Helper()
	got, err := read(t, s, data)
	if err != nil {
		t.Fatalf("%+v: %v", s, err)
	}
	return got
}

// cases runs each data of a table with the spec beside it.
type readCase struct {
	name string
	spec Spec
	data string
	want string
}

func runCases(t *testing.T, cases []readCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustRead(t, c.spec, c.data); got != c.want {
				t.Errorf("got  %s\nwant %s\n%s", got, c.want, c.data)
			}
		})
	}
}

// Build refuses a spec it cannot read by, naming why. Planted breaks: the
// instruction compared in its own case, a key let through on FROM, the
// stage pattern taking a leading digit, the length checks dropped.
func TestBuildRefuses(t *testing.T) {
	long := strings.Repeat("a", maxName+1)
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Instruction: "RUN"}, `dockerfile: instruction "RUN" is none errata reads; write one of FROM, EXPOSE, LABEL, ENV and ARG`},
		{Spec{}, `dockerfile: instruction "" is none errata reads`},
		{Spec{Instruction: "FROM", Key: "a"}, "dockerfile: FROM sets no key; leave key out, or select LABEL, ENV or ARG"},
		{Spec{Instruction: "expose", Key: "a"}, "dockerfile: EXPOSE sets no key"},
		{Spec{Instruction: "ENV", Key: long}, "dockerfile: key \"" + long[:64]},
		{Spec{Instruction: "ENV", Key: long}, fmt.Sprintf("is %d bytes long, over the limit of %d", maxName+1, maxName)},
		{Spec{Instruction: "ENV", Stage: long}, fmt.Sprintf("is %d bytes long, over the limit of %d", maxName+1, maxName)},
		{Spec{Instruction: "ENV", Stage: "1build"}, `dockerfile: stage "1build" is no stage name: write the name after AS`},
		{Spec{Instruction: "ENV", Stage: "a b"}, `dockerfile: stage "a b" is no stage name`},
		{Spec{Instruction: "ENV", Stage: "99999999999999999999999"}, `dockerfile: stage "99999999999999999999999" is no index a Dockerfile can reach`},
	} {
		c.spec.File = "Dockerfile"
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	_, err := Build(Spec{OneFile: core.OneFile{File: "../Dockerfile"}, Instruction: "FROM"})
	extracttest.WantError(t, err, "outside the repository")
	for _, name := range []string{"", long} {
		_, err := Every("Dockerfile", name, 0)
		extracttest.WantError(t, err, "dockerfile: the variable's name is empty or longer than 1024 bytes")
	}
	_, err = Every("../Dockerfile", "A", 0)
	extracttest.WantError(t, err, "outside the repository")
}

// Build takes the instruction in any case, a stage by its name in any case
// or by its index, and a key on the instructions that set one, a key or a
// stage of maxName bytes too. Planted break: the stage name not lowered.
func TestBuildTakes(t *testing.T) {
	edge := strings.Repeat("a", maxName)
	for _, s := range []Spec{{Instruction: "ENV", Key: edge}, {Instruction: "ENV", Stage: edge}} {
		s.File = "Dockerfile"
		if _, err := Build(s); err != nil {
			t.Errorf("%d bytes: %v", maxName, err)
		}
	}
	if _, err := Every("Dockerfile", edge, 0); err != nil {
		t.Errorf("Every of %d bytes: %v", maxName, err)
	}
	data := "FROM a AS Build\nENV A=1\nFROM b\nENV A=2\n"
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Instruction: "env", Key: "A", Stage: "BUILD"}, "A=1@2"},
		{Spec{Instruction: "Env", Key: "A", Stage: "1"}, "A=2@4"},
		{Spec{Instruction: "ENV", Key: "A", Stage: "0"}, "A=1@2"},
		{Spec{Instruction: "FROM", Stage: "build"}, "a@1"},
		{Spec{Instruction: "FROM"}, "a@1 b@3"},
	} {
		if got := mustRead(t, c.spec, data); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.spec, got, c.want)
		}
	}
}

// The env kind takes the names docker build and podman read a Dockerfile
// under, and a template of one, but no file whose suffix says it holds
// none. Planted breaks: each of the conditions dropped, the ignore file
// let through, each suffix of notDockerfile let through.
func TestChooses(t *testing.T) {
	for _, f := range []string{"Dockerfile", "a/Dockerfile", "Dockerfile.prod", "api.Dockerfile", "Containerfile",
		"Containerfile.dev", "x.Containerfile", "dockerfile", "api.dockerfile", "Dockerfile.j2", "deploy/Dockerfile.tmpl", "Dockerfile.mdx"} {
		if !chooses(f) {
			t.Errorf("%s is not chosen", f)
		}
	}
	for _, f := range []string{"Dockerfile.dockerignore", ".dockerignore", "Dockerfiles", "MyDockerfile", "Containerfiles", "dockerfile.txt", "a/b", "docker-compose.yml",
		"Dockerfile.md", "docs/Containerfile.md", "Dockerfile.orig", "Dockerfile.rej", "Dockerfile.bak", "Dockerfile.md.j2"} {
		if chooses(f) {
			t.Errorf("%s is chosen", f)
		}
	}
}

// Place writes the file cleaned, the instruction upper and the stage lower,
// without max_bytes; Resolve writes max_bytes out; Describe says a value may
// be not literal. Planted breaks: Place keeping either case, Resolve
// leaving max_bytes 0.
func TestPlaceResolveDescribe(t *testing.T) {
	a := Spec{OneFile: core.OneFile{File: "./x/../Dockerfile", MaxBytes: 9}, Instruction: "env", Key: "A", Stage: "Build"}
	want := Spec{OneFile: core.OneFile{File: "Dockerfile"}, Instruction: "ENV", Key: "A", Stage: "build"}
	if got := Place(a); !reflect.DeepEqual(got, want) {
		t.Errorf("Place: %+v, want %+v", got, want)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "Dockerfile"}}); got.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve: %+v", got)
	}
	if got := Describe(a); !reflect.DeepEqual(got, check.Description{Label: "dockerfile", NotLiteral: true}) {
		t.Errorf("Describe: %+v", got)
	}
	if Kind.Env == nil || Kind.Env.Order != 80 || Kind.EnvRead == nil {
		t.Errorf("the kind is no env reader: %+v", Kind.Env)
	}
	x, err := Kind.EnvRead("Dockerfile", "A", 7, core.Parsers{})
	if err != nil || x.(core.EntryExtractor).Limit != 7 {
		t.Errorf("EnvRead: %+v, %v", x, err)
	}
}

// FROM's image is expanded against the ARGs before the first FROM, and is
// not literal where it names one with no default, one only the build
// knows, or one whose own default is not literal; an ARG in a stage does
// not reach a FROM. A name nothing declares there is unset, as BuildKit
// expands it: empty, or the default an operator gives. The image is
// otherwise as written: no registry, tag or digest is added. Planted
// breaks: the image expanded against the stage's variables, an unmatched
// name let through as literal, a global ARG with no default not declared,
// a built in ARG taken for unset.
func TestFromExpandsGlobalArgs(t *testing.T) {
	from := Spec{Instruction: "FROM"}
	runCases(t, []readCase{
		{"literal", from, "ARG GO=1.22\nFROM golang:${GO}-alpine AS build\n", "golang:1.22-alpine@2"},
		{"bare", from, "ARG R=docker.io/library\nFROM $R/alpine@sha256:abc\n", "docker.io/library/alpine@sha256:abc@2"},
		{"default operator", from, "FROM alpine:${TAG:-3.20}\n", "alpine:3.20@1"},
		{"default of an arg with none", from, "ARG TAG\nFROM alpine:${TAG:-3.20}\n", "!alpine:${TAG:-3.20}@2"},
		{"default of a built in", from, "FROM a:${TARGETARCH:-amd64}\n", "!a:${TARGETARCH:-amd64}@1"},
		{"no default", from, "ARG TAG\nFROM alpine:$TAG\n", "!alpine:$TAG@2"},
		{"built in", from, "FROM --platform=$BUILDPLATFORM golang:1-$TARGETARCH\n", "!golang:1-$TARGETARCH@1"},
		{"platform flag", from, "FROM --platform=$BUILDPLATFORM golang:1 AS b\n", "golang:1@1"},
		{"default not literal", from, "ARG A=$X\nARG B=x${A}\nFROM b:$B\n", "!b:$B@3"},
		{"global chain", from, "ARG A=1\nARG B=${A}.2\nFROM b:$B\n", "b:1.2@3"},
		{"redeclared", from, "ARG A=1\nARG A\nFROM b:$A\n", "b:1@3"},
		{"stage arg", from, "FROM a AS x\nARG V=1\nFROM b:$V\n", "a@1 b:@3"},
		{"quoted", from, "ARG V=\"1 2\"\nFROM 'b:$V'\n", "b:$V@2"},
		{"later global", from, "FROM a:$V\nARG V=1\n", "a:@1"},
		{"bad substitution", from, "ARG V=1\nFROM a:${V\n", "!a:${V@2"},
		{"previous stage", from, "FROM alpine AS build\nFROM build\n", "alpine@1 build@2"},
		{"undeclared in a template", tmplFrom, "{{ build_args }}\nFROM alpine:$TAG\n", "!alpine:$TAG@2"},
		{"default in a template", tmplFrom, "{% include 'args' %}\nFROM alpine:${TAG:-3}\n", "!alpine:${TAG:-3}@2"},
		{"declared in a template", tmplFrom, "ARG TAG=3\nFROM alpine:$TAG\n", "alpine:3@2"},
	})
}

// tmplFrom reads FROM in a template, whose engine may write ARG lines
// errata cannot see.
var tmplFrom = Spec{OneFile: core.OneFile{File: "Dockerfile.j2"}, Instruction: "FROM"}

// A stage FROM an earlier stage starts with the variables that stage ended
// with, matched by name in any case; any other starts with none errata
// knows. Only a template may have two stages of one name (TestRefusals),
// and a stage FROM the name inherits from the first. Planted breaks: no
// stage inherited, the name compared in its case, the copy shared, so the
// later stage's ENV changed the earlier's, the later stage of a name
// taking its place.
func TestStageInherits(t *testing.T) {
	env := Spec{Instruction: "ENV"}
	runCases(t, []readCase{
		{"by name", env, "FROM a AS base\nENV A=1\nFROM BASE AS next\nENV B=$A\n", "A=1@2 B=1@4"},
		{"from an image", env, "FROM a AS base\nENV A=1\nFROM b\nENV B=$A\n", "A=1@2 !B=$A@4"},
		{"copied", env, "FROM a AS base\nENV A=1\nFROM base AS x\nENV A=2\nFROM base\nENV C=$A\n", "A=1@2 A=2@4 C=1@6"},
		{"not literal image", env, "FROM a AS base\nENV A=1\nFROM $IMG\nENV B=$A\n", "A=1@2 !B=$A@4"},
		{"unnamed", env, "FROM a\nENV A=1\nFROM 0\nENV B=$A\n", "A=1@2 !B=$A@4"},
		{"a name twice in a template", Spec{OneFile: core.OneFile{File: "Dockerfile.j2"}, Instruction: "ENV"},
			"FROM a AS s\nENV A=1\nFROM b AS s\nENV A=2\nFROM s\nENV B=$A\n", "A=1@2 A=2@4 B=1@6"},
		{"both stages of the name", Spec{OneFile: core.OneFile{File: "Dockerfile.j2"}, Instruction: "ENV", Stage: "s"},
			"FROM a AS s\nENV A=1\nFROM b AS s\nENV A=2\nFROM c\nENV A=3\n", "A=1@2 A=2@4"},
	})
}

// ENV and LABEL read key=value pairs, several to an instruction, or the
// old form of one key and the rest of the line; each word is expanded as
// BuildKit expands it, against the variables before the instruction, and
// an ENV sets its variables for the instructions after it, a value only
// the build knows shadowing an earlier one. Planted breaks: the pairs
// expanded against what the instruction sets, the ENV not set, LABEL
// setting variables, a not literal key let through, LABEL --unset
// dropping a variable.
func TestEnvAndLabel(t *testing.T) {
	env, label := Spec{Instruction: "ENV"}, Spec{Instruction: "LABEL"}
	runCases(t, []readCase{
		{"pairs", env, "FROM a\nENV A=1 B=\"two words\" C='$A' D=a\\ b\n", "A=1@2 B=two words@2 C=$A@2 D=a b@2"},
		{"old form", env, "FROM a\nENV A  two  words \"q\"\n", `A=two  words q@2`},
		{"same instruction", env, "FROM a\nENV A=1 B=$A\n", "A=1@2 !B=$A@2"},
		{"next instruction", env, "FROM a\nENV A=1\nENV B=${A}x C=${A:+set}\n", "A=1@2 B=1x@3 C=set@3"},
		{"shadowed", env, "FROM a\nENV A=1\nENV A=$X\nENV B=$A\n", "A=1@2 !A=$X@3 !B=$A@4"},
		{"key expanded", env, "FROM a\nENV K=name\nENV ${K}_X=1\n", "K=name@2 name_X=1@3"},
		{"key not literal", env, "FROM a\nENV ${K}=1\n", "!${K}=1@2"},
		{"empty value", env, "FROM a\nENV A= B=\"\"\n", "A=@2 B=@2"},
		{"label pairs", label, "FROM a\nLABEL org.opencontainers.image.version=\"1.2\" \"with space\"=x\n", "org.opencontainers.image.version=1.2@2 with space=x@2"},
		{"label sets nothing", env, "FROM a\nLABEL A=1\nENV B=$A\n", "!B=$A@3"},
		{"label expands", label, "FROM a\nARG V=3\nLABEL v=$V\n", "v=3@3"},
		{"unset", env, "FROM a\nENV A=1\nENV --unset A\nENV B=$A\n", "A=1@2 !B=$A@4"},
		{"unset false", env, "FROM a\nENV --unset=false A=1\n", "A=1@2"},
		{"label unset", label, "FROM a\nLABEL a=1\nLABEL --unset a\n", "a=1@2"},
		{"label unset keeps the variable", env, "FROM a\nENV A=1\nLABEL --unset A\nENV B=$A\n", "A=1@2 B=1@4"},
	})
}

// A spec with a key keeps that key's values alone, compared as the file
// spells it once expanded. Planted break: the query left out.
func TestKeySelects(t *testing.T) {
	data := "FROM a\nENV A=1 a=2\nLABEL A=3\nENV A=4\n"
	if got := mustRead(t, Spec{Instruction: "ENV", Key: "A"}, data); got != "A=1@2 A=4@4" {
		t.Errorf("got %s", got)
	}
}

// ARG's default is expanded against the variables before it; with no
// default, an ARG in a stage takes the value of the ARG before the first
// FROM of its name, and else is an empty value marked not literal, which
// only --build-arg gives. Its value reaches the words after it. Planted
// breaks: a stage ARG with no default not taking the global value, a
// default not expanded, an ARG with no default setting the variable.
func TestArg(t *testing.T) {
	arg, env := Spec{Instruction: "ARG"}, Spec{Instruction: "ENV"}
	runCases(t, []readCase{
		{"defaults", arg, "ARG A=1 B=\"x $A\"\nFROM a\nARG C=$A D\n", "A=1@1 B=x 1@1 !C=$A@3 !D=@3"},
		{"global then", arg, "ARG A=1\nARG B=$A\nFROM a\n", "A=1@1 B=1@2"},
		{"inherited", arg, "ARG V=1.2\nFROM a\nARG V\n", "V=1.2@1 V=1.2@3"},
		{"inherited not literal", arg, "ARG V=$X\nFROM a\nARG V\n", "!V=$X@1 !V=$X@3"},
		{"none", arg, "FROM a\nARG V\n", "!V=@2"},
		{"global none twice", arg, "ARG V=1\nARG V\nFROM a\n", "V=1@1 V=1@2"},
		{"reaches env", env, "ARG G=1\nFROM a\nARG G\nARG L=2\nENV A=$G$L\n", "A=12@5"},
		{"no default keeps env", env, "FROM a\nENV A=1\nARG A\nENV B=$A\n", "A=1@2 B=1@4"},
		{"inherited shadows env", env, "ARG A=$X\nFROM a\nENV A=1\nARG A\nENV B=$A\n", "A=1@3 !B=$A@5"},
		{"default after env", env, "FROM a\nENV E=1\nARG A=$E\nENV B=$A\n", "E=1@2 B=1@4"},
	})
}

// EXPOSE returns each port, expanded. A JSON form, which docker build
// refuses for EXPOSE, is words as BuildKit's lexer reads them, quotes
// dropped, as docs/reference.md says. Planted break: the port not
// expanded.
func TestExpose(t *testing.T) {
	runCases(t, []readCase{
		{"ports", Spec{Instruction: "EXPOSE"}, "FROM a\nARG P=8080\nEXPOSE 80/tcp $P 53/udp ${Q}\n", "80/tcp@3 8080@3 53/udp@3 !${Q}@3"},
		{"stage", Spec{Instruction: "expose", Stage: "1"}, "FROM a\nEXPOSE 1\nFROM b\nEXPOSE 2\n", "2@4"},
		{"json form", Spec{Instruction: "EXPOSE"}, "FROM a\nEXPOSE [\"80\"]\n", "[80]@2"},
	})
}

// The env kind's read returns each ENV and ARG of the variable, with the
// stage it is in as its origin, and nothing of LABEL. Planted break: the
// origin of an unnamed stage counted from 1.
func TestEvery(t *testing.T) {
	x, err := Every("Dockerfile", "V", 0)
	if err != nil {
		t.Fatal(err)
	}
	data := "ARG V=1\nFROM a AS build\nENV V=2\nLABEL V=3\nFROM b\nARG V\nEXPOSE 1\n"
	vs, err := x.(core.EntryExtractor).FromData([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := show(vs), `V=1@1 [global] V=2@3 [stage "build"] V=1@6 [stage 1]`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A stage the spec names that the file does not have gives no value, and
// says so. Planted break: the check of found left out.
func TestMissingStage(t *testing.T) {
	for stage, want := range map[string]string{"build": `Dockerfile: the Dockerfile has no stage "build"`, "2": "Dockerfile: the Dockerfile has no stage 2"} {
		got, err := read(t, Spec{Instruction: "ENV", Stage: stage}, "ARG A=1\nFROM a AS x\nFROM b\n")
		var none *check.NoValue
		if got != "" || !errors.As(err, &none) || err.Error() != want {
			t.Errorf("%s: %q, %v", stage, got, err)
		}
	}
	if got := mustRead(t, Spec{Instruction: "ENV", Stage: "x"}, "FROM a AS x\n"); got != "" {
		t.Errorf("an empty stage gives %q", got)
	}
}

// A file docker build refuses is refused, at the line BuildKit names,
// counted as errata counts lines. Planted breaks: each refusal dropped.
func TestRefusals(t *testing.T) {
	for _, c := range []struct{ data, want string }{
		{"", "file with no instructions"},
		{"# only a comment\n", "file with no instructions"},
		{"FROM a\nENV A=1 B\n", `Dockerfile: line 2: Syntax error - can't find = in "B". Must be of the form`},
		{"FROM a\nENV A\n", "Dockerfile: line 2: ENV must have two arguments"},
		{"FROM a\nRUN <<EOF\nx\n", "Dockerfile: line 2: unterminated heredoc"},
		{"ENV A=1\nFROM a\n", "Dockerfile: line 1: ENV comes before the first FROM, where docker build takes only ARG"},
		{"FROM a\nFROB x\n", `Dockerfile: line 2: "FROB" is no instruction docker build knows`},
		{"FROM a b\n", "Dockerfile: line 1: FROM takes an image, or an image, AS and a name, and docker build refuses this one"},
		{"FROM a AT b\n", "Dockerfile: line 1: FROM takes an image"},
		{"FROM\n", "Dockerfile: line 1: FROM takes an image"},
		{"FROM AS b\n", "Dockerfile: line 1: FROM takes an image"},
		{"FROM a AS b\nFROM c AS B\n", `Dockerfile: line 2: stage name "b" is taken by an earlier stage, which docker build refuses`},
		{"FROM a\nARG =1\n", "Dockerfile: line 2: an ARG with no name, which docker build refuses"},
		{"FROM a\nLABEL =1\n", "Dockerfile: line 2: LABEL sets a key with no name, which docker build refuses"},
		{"# escape=x\nFROM a\n", "invalid escape token"},
		{"FROM a\r\n" + strings.Repeat("x", 70000) + "\n", "Dockerfile: line 2: dockerfile line greater than max allowed size of 65535"},
		{"FROM a\rb\nLABEL \\\n" + strings.Repeat("x", 70000) + "\n", "Dockerfile: line 4: dockerfile line greater than max allowed size of 65535"},
		{"FROM a\n" + strings.Repeat("x", 65536) + "\n", "Dockerfile: line 2: dockerfile line greater than max allowed size of 65535"},
		{"FROM a\n" + strings.Repeat("x", 65535) + "\n", "Dockerfile: line 2: \"xxxx"},
		{"FROM a\n" + strings.Repeat("x", 65535) + "\n" + strings.Repeat("x", 70000) + "\n", "Dockerfile: line 3: dockerfile line greater"},
		{"FROM a\n" + strings.Repeat("x", 70000), "Dockerfile: line 2: dockerfile line greater than max allowed size of 65535"},
	} {
		_, err := read(t, Spec{Instruction: "LABEL"}, c.data)
		extracttest.WantError(t, err, c.want)
	}
}

// parseError names the line of a located error, the first line for one at
// line 0, and an error with no location as it is. Planted break: the
// location read as the line itself.
func TestParseError(t *testing.T) {
	data := []byte("a\rb\nc\n")
	l := newLines(data)
	err := parseError(errors.New("plain"), data, l)
	if err.Error() != "plain" {
		t.Errorf("%v", err)
	}
	src := []byte("x\ry\nFROM a\nRUN <<EOF\n")
	_, perr := parser.Parse(bytes.NewReader(src))
	extracttest.WantError(t, parseError(perr, src, newLines(src)), "line 4: unterminated heredoc")
}

// Values on the lines of a continued instruction sit on their own lines:
// a comment or a blank line inside it is skipped and counted, the escape
// directive's ` continues lines and escapes, a heredoc's lines are not
// instructions, a byte order mark and CRLF line ends leave every line as
// it was, and a lone CR ends a line as errata counts lines. Planted
// breaks: every word at the instruction's first line, the comment line
// not skipped, the blank line not skipped, the first line's blanks not
// dropped, the byte order mark not dropped.
func TestLinesOfContinuedInstructions(t *testing.T) {
	label, env := Spec{Instruction: "LABEL"}, Spec{Instruction: "ENV"}
	runCases(t, []readCase{
		{"continued", label, "FROM a\nLABEL a=1 \\\n      b=2 \\\n      c=3\n", "a=1@2 b=2@3 c=3@4"},
		{"comment and blank", label, "FROM a\nLABEL a=1 \\\n# note\n\n  \t\n      b=2\n", "a=1@2 b=2@6"},
		{"indented", label, "FROM a\n   LABEL a=1 \\\n b=2\n", "a=1@2 b=2@3"},
		{"indented past a lone cr", label, "FROM a\n \r LABEL a=1\n", "a=1@3"},
		{"value continued", env, "FROM a\nENV A=\"x \\\n  y\" B=1\n", "A=x   y@2 B=1@3"},
		{"old form continued", env, "FROM a\nENV A \\\n  x \\\n  y\n", "A=x   y@2"},
		{"escape directive", env, "# escape=`\nFROM a\nENV P=C:\\dir `\n    Q=`$x\n", `P=C:\dir@3 Q=$x@4`},
		{"heredoc", env, "FROM a\nRUN <<EOF\nENV X=no\nEOF\nENV A=1\n", "A=1@5"},
		{"byte order mark", env, "\uFEFFFROM a\nENV A=1 \\\n B=2\n", "A=1@2 B=2@3"},
		{"byte order mark first", Spec{Instruction: "FROM"}, "\uFEFF  FROM \\\n a\n", "a@2"},
		{"crlf", env, "FROM a\r\nENV A=1 \\\r\n B=2\r\n", "A=1@2 B=2@3"},
		{"lone cr", label, "FROM a\nLABEL a=0\rv=1\n", "a=0@2 v=1@3"},
		{"lone cr continued", label, "FROM a\nLABEL a=0 \\\nb=1\rv=1\n", "a=0@2 b=1@3 v=1@4"},
		{"flags", Spec{Instruction: "FROM"}, "FROM --platform=\"a b\" \\\n  --x=`c \\\n  img\n", "img@3"},
		{"escaped escape", label, "FROM a\nLABEL a=\\\\\nLABEL b=1\n", `a=\@2 b=1@3`},
		{"arg continued", Spec{Instruction: "ARG"}, "ARG A=1 \\\n B=2\nFROM a\n", "A=1@1 B=2@2"},
		{"expose continued", Spec{Instruction: "EXPOSE"}, "FROM a\nEXPOSE 1 \\\n 2\n", "1@2 2@3"},
	})
}

// A template's value the engine renders, or inside one of its blocks, is
// not literal, a line of the engine's is no instruction, and an image the
// engine writes in words is those words, not literal. Outside a template
// neither is taken. Planted breaks: TemplateNotLiteral left out of Build,
// the engine's lines refused, the words of an image refused.
func TestTemplate(t *testing.T) {
	tmpl := core.OneFile{File: "Dockerfile.j2"}
	data := "{% set x = 1 %}\nFROM {{ base }} AS b\nENV A={{a}} B=1\n{% if x %}\nENV C=2\n{% endif %}\n"
	if got, want := mustRead(t, Spec{OneFile: tmpl, Instruction: "ENV"}, data), "!A={{a}}@3 B=1@3 !C=2@5"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := mustRead(t, Spec{OneFile: tmpl, Instruction: "FROM", Stage: "b"}, data), "!{{ base }}@2"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	x, err := Every("Dockerfile.j2", "C", 0)
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := x.(core.EntryExtractor).FromData([]byte(data)); err != nil || show(vs) != `!C=2@5 [stage "b"]` {
		t.Errorf("Every: %s, %v", show(vs), err)
	}
	_, err = read(t, Spec{Instruction: "ENV"}, "FROM {{ base }}\n")
	extracttest.WantError(t, err, "FROM takes an image")
}
