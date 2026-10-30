package extract

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/env"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/shell"
)

// fakeSpec is the spec of fakeKind, which only this test binary registers.
type fakeSpec struct {
	File string `json:"file"`
}

func (fakeSpec) Bound() int64 { return 0 }

// fakeEnv is an env reader of a file named *.fake, or of one that starts
// with #!, declared as a reader package declares one: its value of a
// variable names the variable, the file, and whether the facade handed it
// the YAML parser it hands the CI readers.
var fakeEnv = check.EnvReader{
	Order:      60,
	Chooses:    func(file string) bool { return path.Ext(file) == ".fake" },
	Sniffs:     func(head []byte) bool { return string(head) == "#!" },
	SniffBytes: 2,
}

// fakeEnvRead is the fake env reader's Read.
func fakeEnvRead(file, name string, _ int64, p core.Parsers) (check.Extractor, error) {
	return fakeRead{text: fmt.Sprintf("%s in %s, parsed %t", name, file, p.Noted != nil), file: file}, nil
}

type fakeRead struct{ text, file string }

func (f fakeRead) Extract(context.Context, string) ([]check.Value, error) {
	return []check.Value{{Text: f.text, At: check.Location{File: f.file, Line: 1}}}, nil
}

func (f fakeRead) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return f.Extract(ctx, root)
}

// fakeKinds are kinds with a fake kind registered after them, which
// declares e and read as its env reader.
func fakeKinds(e *check.EnvReader, read func(string, string, int64, core.Parsers) (check.Extractor, error)) table {
	k := core.Kind[fakeSpec]{Key: "fake", Env: e, EnvRead: read}
	return registry(append(slices.Clone(kinds.order), register(k, nil))...)
}

// A kind that declares itself an env reader is one env asks, with nothing
// else edited: env chooses it by its file name, or by its first bytes when
// no reader's name test takes the file, reads with it through the parsers
// the facade hands, and lists it last, by its Order. A file that starts
// with #!/bin/sh is sniffed by shell and the fake both, and shell, of lower
// Order, reads it; one named *.fake that starts so is the fake's by its
// name, which env asks before any sniff. Planted breaks: register not handing a kind's Env on;
// readersOf stopping at the first kind that is no env reader; the parsers
// not handed to Read; the sniffing dropped; the sniffer of higher Order
// taken; the sniff asked before the name test.
func TestEnvAsksARegisteredReader(t *testing.T) {
	rs := readersOf(fakeKinds(&fakeEnv, fakeEnvRead))
	if got := rs.Choose("conf/app.fake"); got != "fake" {
		t.Errorf("conf/app.fake chose %q, want fake", got)
	}
	root := extracttest.Repo(t, map[string]string{".env": "A=1\n", "conf/app.fake": "x\n", "bin/run": "#!/bin/sh\nA=2\n", "conf/sniffed": "#!fake\n", "conf/b.fake": "#!/bin/sh\nA=3\n"})
	x, err := env.Build(env.Spec{Name: "A", Files: []string{".env", "conf/app.fake", "bin/run", "conf/sniffed", "conf/b.fake"}}, rs)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, fmt.Sprintf("%s@%s [%s]", v.Text, v.At.File, v.Origin))
	}
	if g, want := strings.Join(got, "; "), "1@.env [envfile]; A in conf/app.fake, parsed true@conf/app.fake [fake]; 2@bin/run [shell]; A in conf/sniffed, parsed true@conf/sniffed [fake]; A in conf/b.fake, parsed true@conf/b.fake [fake]"; g != want {
		t.Errorf("got  %s\nwant %s", g, want)
	}
	_, err = env.Build(env.Spec{Name: "A", Files: []string{"x"}, Readers: map[string]string{"x": "nope"}}, rs)
	extracttest.WantError(t, err, "write one of envfile, shell, gitlab-ci, github-actions, jenkins, fake")
}

// Every registered env reader's name test, as env asks them: a name
// chooses its reader, and a name that chooses none is "". Two names are
// taken by two readers each, and Order settles them as the readers were
// once tried in turn: .github/workflows/.env.yml by envfile before
// github-actions, and .github/workflows/.gitlab-ci.yml by gitlab-ci before
// github-actions. A new kind's reader runs here with no list to join.
// Planted breaks: .envrc read as an env file; a workflow in a directory
// below .github/workflows taken, which GitHub does not run; prod.env not
// read as an env file; envfile's test taking a .sh name.
func TestEnvChoosesByName(t *testing.T) {
	rs := readersOf(kinds)
	for file, want := range map[string]string{
		".env": "envfile", ".env.local": "envfile", "app/.env.production": "envfile", "./.env": "envfile",
		".envrc": "shell", "deploy.sh": "shell", "bin/x.sh": "shell", ".env.sh": "shell", "a.env.sh": "shell",
		".gitlab-ci.yml": "gitlab-ci", "sub/.gitlab-ci.yml": "gitlab-ci",
		".github/workflows/ci.yml": "github-actions", ".github/workflows/ci.yaml": "github-actions",
		"./.github/workflows/a.yml":  "github-actions",
		".github/workflows/x/ci.yml": "", ".github/ci.yml": "", ".github/workflows/ci.json": "",
		".github/workflows/.env.yml": "envfile", ".github/workflows/.gitlab-ci.yml": "gitlab-ci",
		"Jenkinsfile": "jenkins", "ci/Jenkinsfile": "jenkins",
		"a.env": "envfile", "config/prod.env": "envfile",
		"env": "", "bin/deploy": "", "a.envx": "", "Jenkinsfile.groovy": "",
	} {
		if got := rs.Choose(file); got != want {
			t.Errorf("%s: %q, want %q", file, got, want)
		}
	}
}

// env lists the readers by their Order, envfile, shell, gitlab-ci,
// github-actions, jenkins, and not in the order kinds lists them, where
// gitlab-ci comes before shell. Planted break: shell's Order past
// gitlab-ci's.
func TestEnvListsItsReadersInOrder(t *testing.T) {
	_, err := Build(Of(env.Kind, env.Spec{Name: "A", Files: []string{".env"}, Readers: map[string]string{".env": "yaml"}}))
	extracttest.WantError(t, err, `readers gives ".env" the reader "yaml"; write one of envfile, shell, gitlab-ci, github-actions, jenkins`)
}

// readersOf refuses two env readers at one Order, one with no Chooses or
// no EnvRead, an EnvRead with no Env, and one that sniffs no bytes, by its
// panic. Planted breaks: each check dropped.
func TestReadersOfRefuses(t *testing.T) {
	same, noChooses, noBytes := fakeEnv, fakeEnv, fakeEnv
	same.Order = shell.Kind.Env.Order
	noChooses.Chooses = nil
	noBytes.SniffBytes = 0
	for name, c := range map[string]struct {
		e    *check.EnvReader
		read func(string, string, int64, core.Parsers) (check.Extractor, error)
		want string
	}{
		"order":   {&same, fakeEnvRead, `extract: the env readers "shell" and "fake" are both at order 20`},
		"chooses": {&noChooses, fakeEnvRead, `extract: the env reader "fake" has no Chooses or no EnvRead`},
		"read":    {&fakeEnv, nil, `extract: the env reader "fake" has no Chooses or no EnvRead`},
		"no env":  {nil, fakeEnvRead, `extract: the kind "fake" has an EnvRead and no Env`},
		"sniff":   {&noBytes, fakeEnvRead, `extract: the env reader "fake" sniffs no bytes: its SniffBytes is 0`},
	} {
		func() {
			defer func() {
				if r := recover(); r != c.want {
					t.Errorf("%s: recovered %v, want %s", name, r, c.want)
				}
			}()
			readersOf(fakeKinds(c.e, c.read))
		}()
	}
}

// register hands on a kind's env reader declaration as the kind wrote it,
// the same value and not a copy of its fields, so a field added to
// check.EnvReader reaches env with no copy to extend. Planted break:
// register copying the declaration field by field.
func TestRegisterHandsOnTheEnvDeclaration(t *testing.T) {
	if got := fakeKinds(&fakeEnv, fakeEnvRead).byKey["fake"].Env; got != &fakeEnv {
		t.Errorf("the registry holds %p, not the declaration %p", got, &fakeEnv)
	}
}
