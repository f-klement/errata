package compose

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/dotenv"
	cg "github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// boundaryTree is a project that reaches every file compose-go opens
// itself: an include, an included project's .env, an include of the main
// project with an env_file, a nested include with an env_file, an extends
// file that extends another, an extends file in an included project, and a
// develop.watch path compose-go resolves.
var boundaryTree = map[string]string{
	"compose.yaml": "include:\n  - i/c.yaml\n  - path: t/t.yaml\n    env_file: top.env\nservices:\n  web:\n    extends: {file: b.yaml, service: b}\n",
	"top.env":      "U=topenv\n",
	"t/t.yaml":     "services:\n  top:\n    image: t:${U}\n",
	"b.yaml":       "services:\n  b:\n    extends: {file: c.yaml, service: c}\n",
	"c.yaml":       "services:\n  c:\n    image: c:${TAG}\n",
	".env":         "TAG=root\n",
	"i/c.yaml": "include:\n  - path: n/n.yaml\n    env_file: e.env\nservices:\n  inc:\n    image: i:${V}\n    build: .\n" +
		"    develop:\n      watch:\n        - {path: data/x, action: rebuild}\n  ext:\n    extends: {file: base.yaml, service: b}\n",
	"i/.env":      "V=dotenv\n",
	"i/e.env":     "W=envfile\n",
	"i/n/n.yaml":  "services:\n  nested:\n    image: n:${W}\n",
	"i/base.yaml": "services:\n  b:\n    image: ib\n    volumes: [./v:/v]\n",
	"i/data/x":    "",
}

// boundarySpecs are the keys of boundaryTree that each route reaches.
var boundarySpecs = []Spec{
	{Service: "web", Key: "image"},
	{Service: "inc", Key: "image"},
	{Service: "inc", Key: "build.context"},
	{Service: "inc", Key: "develop.watch.0.path"},
	{Service: "nested", Key: "image"},
	{Service: "ext", Key: "volumes.0.source"},
	{Service: "top", Key: "image"},
}

// compose-go opens no file of the repository, and none in the working
// directory: once the walk is done, the repository is removed and the
// process moves to a directory that holds the same tree with other
// values, and every value comes out as before. So each file compose-go
// opens itself, which no resource loader routes, is the copy on the stage
// of a file errata read. A test that intercepted the opens would need
// strace or a seccomp filter, which the runner's pods do not grant; a
// removed tree fails any open of it, with no permission a root user would
// pass. Planted breaks: absPath giving the path in the repository rather
// than on the stage; the main project's working directory in the
// repository, where compose-go opens its include's env_file.
func TestComposeGoReadsOnlyTheStage(t *testing.T) {
	want := []string{"image=c:root@c.yaml:3", "image=i:dotenv@i/c.yaml:6", "build.context=i@i/c.yaml:7",
		"develop.watch.0.path=i/data/x@i/c.yaml:10", "image=n:envfile@i/n/n.yaml:3", "volumes.0.source=i/v@i/base.yaml:4",
		"image=t:topenv@t/t.yaml:3"}
	decoy := map[string]string{}
	for name, body := range boundaryTree {
		decoy[name] = strings.NewReplacer("root", "leaked", "dotenv", "leaked", "envfile", "leaked", "topenv", "leaked", "./v", "./leaked").Replace(body)
	}
	t.Chdir(extracttest.Repo(t, decoy))
	for i, s := range boundarySpecs {
		s.File = "compose.yaml"
		if got := mustRead(t, boundaryTree, s); got != want[i] {
			t.Errorf("%s %s read whole: %s, want %s", s.Service, s.Key, got, want[i])
		}
		x, err := Build(s, yamlParse)
		if err != nil {
			t.Fatal(err)
		}
		e := x.(extractor)
		root := extracttest.Repo(t, boundaryTree)
		w, err := newWalker(t.Context(), root, yamlParse, e.Limit)
		if err != nil {
			t.Fatal(err)
		}
		p, err := w.project(e.files, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		vs, err := newLoader(w).values(p, e.service, e.key, nil)
		w.close()
		if got := show(vs); err != nil || got != want[i] {
			t.Errorf("%s %s with the repository gone: %s, %v; want %s", s.Service, s.Key, got, err, want[i])
		}
	}
}

// Each image of a tree is what compose-go gives loading the tree itself,
// with its own loader, from the disk: the walker reads the files compose-go
// reads, an included project's variables come from the same places in the
// same order, the including project's winning, and an extends file's
// extends file is read beside it, a file one project extends and another
// includes resolves its includes in the project that includes it, and a
// service of an extends file no chain reaches is not followed. Planted
// breaks: includeEnv letting the included project's variables win, which
// read a/y/n.yaml where compose-go reads a/x/n.yaml; the guard taking a
// path by the file alone, which read n.yaml where compose-go reads
// i/n.yaml.
func TestAgreesWithComposeGoAlone(t *testing.T) {
	trees := []map[string]string{
		boundaryTree,
		{
			"compose.yaml": "include: [a/c.yaml]\nservices:\n  top:\n    image: t:${V}\n",
			".env":         "V=parent\nD=x\n",
			"a/.env":       "V=child\nD=y\n",
			"a/c.yaml":     "include: [\"${D}/n.yaml\"]\nservices:\n  web:\n    image: w:${V}\n",
			"a/x/n.yaml":   "services:\n  n:\n    image: from-x\n",
			"a/y/n.yaml":   "services:\n  n:\n    image: from-y\n",
		},
		{
			"compose.yaml": "include:\n  - path: a/c.yaml\n    env_file: [a/one.env, a/two.env]\n",
			"a/one.env":    "A=1\nB=${A}\n",
			"a/two.env":    "A=2\nC=${A}${B}\n",
			"a/c.yaml":     "services:\n  web:\n    image: w:${A}-${B}-${C}\n",
		},
		// a file the main project extends and the project i includes
		{
			"compose.yaml": "include: [i/c.yaml]\nservices:\n  web:\n    extends: {file: i/c.yaml, service: inc}\n  top:\n    extends: {file: n.yaml, service: other}\n",
			"i/c.yaml":     "include: [n.yaml]\nservices:\n  inc:\n    image: inc\n",
			"i/n.yaml":     "services:\n  nested:\n    image: from-i\n",
			"n.yaml":       "services:\n  nested:\n    image: from-root\n  other:\n    image: o\n",
		},
		// an included project's extends file extends one beside it
		{
			"compose.yaml": "include: [i/c.yaml]\n",
			"i/c.yaml":     "services:\n  web:\n    extends: {file: base.yaml, service: b}\n",
			"i/base.yaml":  "services:\n  b:\n    extends: {file: z.yaml, service: z}\n",
			"i/z.yaml":     "services:\n  z:\n    image: from-i\n",
			"z.yaml":       "services:\n  z:\n    image: from-root\n",
		},
		// an extends file elsewhere: the service extended needs no file, a
		// service beside it names one that is not there
		{
			"compose.yaml":  "services:\n  web:\n    extends: {file: sub/base.yaml, service: b}\n",
			"sub/base.yaml": "services:\n  b:\n    image: b\n  other:\n    extends: {file: x.yaml, service: c}\n",
			"sub/x.yaml":    "services:\n  c:\n    image: c\n",
		},
		// an extends file elsewhere, extending within itself
		{
			"compose.yaml":  "services:\n  web:\n    extends: {file: sub/base.yaml, service: b}\n",
			"sub/base.yaml": "services:\n  b:\n    extends: c\n  c:\n    image: same-file\n",
		},
		// a base in the root extends sub/x.yaml, which extends y.yaml
		{
			"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n",
			"base.yaml":    "services:\n  b:\n    extends: {file: sub/x.yaml, service: c}\n",
			"sub/x.yaml":   "services:\n  c:\n    extends: {file: y.yaml, service: d}\n",
			"sub/y.yaml":   "services:\n  d:\n    image: sub-y\n",
			"y.yaml":       "services:\n  d:\n    image: root-y\n",
		},
		// an included project extends ../shared/b.yaml, which extends c.yaml
		{
			"compose.yaml":  "include: [i/c.yaml]\n",
			"i/c.yaml":      "services:\n  web:\n    extends: {file: ../shared/b.yaml, service: b}\n",
			"shared/b.yaml": "services:\n  b:\n    extends: {file: c.yaml, service: c}\n",
			"shared/c.yaml": "services:\n  c:\n    image: shared-c\n",
			"c.yaml":        "services:\n  c:\n    image: root-c\n",
		},
	}
	for i, files := range trees {
		root := extracttest.Repo(t, files)
		// compose-go reads an env_file of an include in an included
		// project relative to the working directory of the process, which
		// is the project's when docker compose runs there.
		t.Chdir(root)
		env := map[string]string{}
		var err error
		if data, ok := files[".env"]; ok {
			if env, err = dotenv.UnmarshalBytesWithLookup([]byte(data), nil); err != nil {
				t.Fatal(err)
			}
		}
		proj, err := cg.LoadWithContext(t.Context(), types.ConfigDetails{WorkingDir: root,
			ConfigFiles: []types.ConfigFile{{Filename: filepath.Join(root, "compose.yaml")}}, Environment: env},
			func(o *cg.Options) { o.SetProjectName("x", true) })
		if err != nil {
			t.Fatalf("tree %d: compose-go: %v", i, err)
		}
		for name, svc := range proj.Services {
			got := mustRead(t, files, Spec{Service: name, Key: "image"})
			if !strings.HasPrefix(got, "image="+svc.Image+"@") {
				t.Errorf("tree %d, %s: errata %s, compose-go %s", i, name, got, svc.Image)
			}
		}
	}
}

// A root .env that names a file outside the repository, used by an
// include's env_file in an included project, is refused and no variable of
// that file reaches a value: compose-go opens an include's env_file
// itself, so its path may not name a variable. Planted break: the $ test
// left out, with includeEnv letting the included project's variables win,
// which gave image=w:leaked.
func TestIncludeEnvFileOutsideRefused(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "secret.env"), []byte("TAG=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"compose.yaml": "include: [a/c.yaml]\n",
		".env":         "E=" + filepath.Join(out, "secret.env") + "\n",
		"a/.env":       "E=inside.env\n",
		"a/inside.env": "TAG=harmless\n",
		"a/c.yaml":     "include:\n  - path: n.yaml\n    env_file: ${E}\n",
		"a/n.yaml":     "services:\n  web:\n    image: w:${TAG}\n",
	}
	got, err := read(t, files, Spec{Service: "web", Key: "image"})
	if err == nil || strings.Contains(got, "leaked") || !strings.Contains(err.Error(), `the include env_file "${E}" holds a $`) {
		t.Errorf("got %s, %v; want the env_file refused", got, err)
	}
	files["a/c.yaml"] = "include:\n  - path: n.yaml\n    env_file: inside.env\n"
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w:harmless@a/n.yaml:3" {
		t.Errorf("the env_file as written: %s", got)
	}
}

// A file that includes itself, or a file that includes it, is refused
// with the files of the cycle, before the walk recurses without end; an
// env_file of /dev/null, which reads no .env, does not hide it. Planted
// break: cycle finding none.
func TestIncludeCycleRefused(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20))
	refused(t, map[string]string{"compose.yaml": "include:\n  - path: compose.yaml\n    env_file: /dev/null\nservices:\n  web:\n    image: w\n"},
		Spec{Service: "web", Key: "image"}, "compose.yaml: include cycle: compose.yaml includes compose.yaml")
	refused(t, map[string]string{"compose.yaml": "include: [a/b.yaml]\nservices:\n  web:\n    image: w\n", "a/b.yaml": "include: [../compose.yaml]\n"},
		Spec{Service: "web", Key: "image"}, "a/b.yaml: include cycle: compose.yaml includes a/b.yaml includes compose.yaml")
}

// Each include the walk visits counts toward the 150 files, so files that
// each include the next two, eight deep, are refused rather than walked
// 510 times. Planted break: include not counting a visit.
func TestIncludeVisitsBounded(t *testing.T) {
	files := map[string]string{"compose.yaml": "include:\n  - {path: l1a.yaml, env_file: /dev/null}\n  - {path: l1b.yaml, env_file: /dev/null}\nservices:\n  web:\n    image: w\n"}
	for k := 1; k <= 8; k++ {
		next := "include:\n  - {path: l" + string(rune('0'+k+1)) + "a.yaml, env_file: /dev/null}\n  - {path: l" + string(rune('0'+k+1)) + "b.yaml, env_file: /dev/null}\n"
		if k == 8 {
			next = "services:\n  s8:\n    image: s\n"
		}
		files["l"+string(rune('0'+k))+"a.yaml"] = next
		files["l"+string(rune('0'+k))+"b.yaml"] = next
	}
	refused(t, files, Spec{Service: "web", Key: "image"}, "the project reads and includes more than 150 files")
}

// An extends file in another directory that names its own extends file by
// a relative path reads it beside itself, sub/x.yaml here, as docker
// compose does, and a service of an extends file that no chain reaches
// names nothing errata reads or refuses. Planted breaks: chain resolving
// every path against the project's directory, and extends following every
// service of an extends file.
func TestNestedExtendsBesideItsFile(t *testing.T) {
	files := map[string]string{
		"compose.yaml":  "services:\n  web:\n    extends: {file: sub/base.yaml, service: b}\n",
		"sub/base.yaml": "services:\n  b:\n    extends: {file: x.yaml, service: c}\n",
		"sub/x.yaml":    "services:\n  c:\n    image: from-sub\n",
		"x.yaml":        "services:\n  c:\n    image: from-root\n",
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=from-sub@sub/x.yaml:3" {
		t.Errorf("an extends file's extends file beside it: %s", got)
	}
	files = map[string]string{
		"compose.yaml":  "services:\n  web:\n    extends: {file: sub/base.yaml, service: b}\n",
		"sub/base.yaml": "services:\n  b:\n    image: b\n  other:\n    extends: {file: ../../x.yaml, service: c}\n",
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=b@sub/base.yaml:3" {
		t.Errorf("a service no chain reaches: %s", got)
	}
}

// One path that leads to two files in what one file loads is refused,
// naming the path and both files: compose-go asks the guard for it by the
// file and the path alone, so errata cannot tell which it loads. Written
// differently, it reads. A file two projects of different directories
// load is refused so when it names a path. Planted break: follow keeping
// the first route.
func TestOnePathToTwoFilesRefused(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n  top:\n    extends: {file: y.yaml, service: d}\n",
		"base.yaml":    "services:\n  b:\n    extends: {file: sub/x.yaml, service: c}\n",
		"sub/x.yaml":   "services:\n  c:\n    extends: {file: y.yaml, service: d}\n",
		"sub/y.yaml":   "services:\n  d:\n    image: sub-y\n",
		"y.yaml":       "services:\n  d:\n    image: root-y\n",
	}
	refused(t, files, Spec{Service: "web", Key: "image"}, `compose.yaml: the path "y.yaml" leads to y.yaml here and to sub/y.yaml elsewhere in what compose.yaml loads`)
	files["sub/x.yaml"] = "services:\n  c:\n    extends: {file: ./y.yaml, service: d}\n"
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=sub-y@sub/y.yaml:3" {
		t.Errorf("written differently: %s", got)
	}
	// a/c.yaml is a file of the project a and of the project b, whose
	// include of n.yaml is a/n.yaml in the one and b/n.yaml in the other
	refused(t, map[string]string{
		"compose.yaml": "include:\n  - a/c.yaml\n  - path: [b/x.yaml, a/c.yaml]\n",
		"a/c.yaml":     "include: [n.yaml]\n",
		"a/n.yaml":     "services:\n  na:\n    image: a\n",
		"b/x.yaml":     "services:\n  bx:\n    image: b\n",
		"b/n.yaml":     "services:\n  nb:\n    image: b\n",
	}, Spec{Service: "na", Key: "image"}, `a/c.yaml: the path "n.yaml" leads to b/n.yaml here and to a/n.yaml elsewhere in what a/c.yaml loads`)
}

// A develop.watch path through a link in the repository is the path as the
// file writes it, below the root: compose-go resolves links on the stage,
// which holds none, so no path of the machine outside the repository
// reaches a value. Planted break: absPath giving the path in the
// repository.
func TestWatchPathThroughALink(t *testing.T) {
	out := t.TempDir()
	root := extracttest.Repo(t, map[string]string{"compose.yaml": "include: [i/c.yaml]\n",
		"i/c.yaml": "services:\n  web:\n    image: w\n    build: .\n    develop:\n      watch:\n        - {path: data/x, action: rebuild}\n"})
	extracttest.Symlink(t, out, filepath.Join(root, "i", "data"))
	vs := extracttest.MustRun(t, func(s Spec) (check.Extractor, error) { return Build(s, yamlParse) },
		Spec{OneFile: oneFile("compose.yaml"), Service: "web", Key: "develop.watch.0.path"}, root)
	if got := show(vs); got != "develop.watch.0.path=i/data/x@i/c.yaml:7" {
		t.Errorf("got %s, want the path as written", got)
	}
}

// The walk reads and includes up to 150 files, and refuses the 151st.
func TestCountAtTheLimit(t *testing.T) {
	w := &walker{read: maxFiles - 1}
	if err := w.count(); err != nil {
		t.Errorf("file %d refused: %v", maxFiles, err)
	}
	if err := w.count(); err == nil {
		t.Errorf("file %d taken", maxFiles+1)
	}
}

// A relative TMPDIR reads every value as an absolute one does, and leaves
// no stage behind. Planted break: newStage keeping the directory relative,
// which refused every rule naming i/c.yaml.
func TestRelativeTmpdir(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "rel"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo := extracttest.Repo(t, boundaryTree)
	t.Chdir(base)
	t.Setenv("TMPDIR", "rel")
	for _, s := range boundarySpecs[:2] {
		s.File = "compose.yaml"
		x, err := Build(s, yamlParse)
		if err != nil {
			t.Fatal(err)
		}
		vs, err := x.Extract(t.Context(), repo)
		if err != nil || len(vs) != 1 {
			t.Errorf("%s %s: %s, %v", s.Service, s.Key, show(vs), err)
		}
	}
	if left, _ := os.ReadDir(filepath.Join(base, "rel")); len(left) != 0 {
		t.Errorf("stages left: %d", len(left))
	}
}

// A variable an include's env_file or the .env file sets from one no file
// sets, such as TAG=${HOME}, takes its value from the shell under docker
// compose, so a value that names it is not literal, never a literal empty
// string, even through a default. One set from a variable a file sets is
// literal in one file; in a project that includes a file looking up TAG,
// the taint marks it too. Planted breaks: fromFiles keeping every
// variable; looksUp asking only with lookups that fail.
func TestEnvFromTheShellNotLiteral(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "include:\n  - path: a/c.yaml\n    env_file: a/e.env\n",
		"a/e.env":      "TAG=${HOME}\nOK=${SET}\nW=${SET:+${USER}}\n",
		"a/c.yaml":     "services:\n  web:\n    image: w:${TAG}\n  ok:\n    image: o:${OK}\n  w:\n    image: x:${W}\n",
		".env":         "SET=yes\n",
	}
	if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w:${TAG}@a/c.yaml:3!" {
		t.Errorf("an include env_file: %s", got)
	}
	// The project includes a file that looks up TAG, so the taint marks
	// every value; the same variable in one file reads literal below.
	if got := mustRead(t, files, Spec{Service: "ok", Key: "image"}); got != "image=o:yes@a/c.yaml:5!" {
		t.Errorf("from a variable a file sets, included: %s", got)
	}
	one := map[string]string{".env": "SET=yes\nOK=${SET}\n", "compose.yaml": "services:\n  ok:\n    image: o:${OK}\n"}
	if got := mustRead(t, one, Spec{Service: "ok", Key: "image"}); got != "image=o:yes@compose.yaml:3" {
		t.Errorf("from a variable a file sets: %s", got)
	}
	if got := mustRead(t, files, Spec{Service: "w", Key: "image"}); got != "image=x:${W}@a/c.yaml:7!" {
		t.Errorf("asked for only when SET is set: %s", got)
	}
	for _, env := range []string{"TAG=${HOME:-x}\n", "TAG=${HOME:+x}\n", "A=${HOME}\nTAG=${A}\n"} {
		files := map[string]string{".env": env, "compose.yaml": "services:\n  web:\n    image: w:${TAG}\n"}
		if got := mustRead(t, files, Spec{Service: "web", Key: "image"}); got != "image=w:${TAG}@compose.yaml:3!" {
			t.Errorf(".env %q: %s", env, got)
		}
	}
}
