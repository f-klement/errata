package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/envfile"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/githubactions"
	"github.com/f-klement/errata/internal/extract/gitlabci"
	"github.com/f-klement/errata/internal/extract/jenkins"
	"github.com/f-klement/errata/internal/extract/shell"
)

// call is one build of a fake reader.
type call struct {
	reader, file string
	maxBytes     int64
}

// declared are the env readers the reader packages declare, in the order
// of the facade's kinds, which is not env's: Readers takes them in any
// order.
var declared = []struct {
	name string
	env  *check.EnvChoice
}{
	{envfile.Kind.Key, envfile.Kind.Env},
	{gitlabci.Kind.Key, gitlabci.Kind.Env},
	{shell.Kind.Key, shell.Kind.Env},
	{githubactions.Kind.Key, githubactions.Kind.Env},
	{jenkins.Kind.Key, jenkins.Kind.Env},
}

// fakes are the declared readers, each choosing files by its own name
// test, that read each line NAME=value of a file, in any
// format, as a value of NAME, with the reader's name and the line as its
// origin for a CI reader, and record each build. A reader refuses the name
// BAD, and github-actions also GITHUB_X, as the real one does; a file that
// holds FAIL fails, one that holds NONE is check.NoValue, and REPEAT
// repeats the first value ten times, as a CI reader repeats a value for
// each job.
func fakes(calls *[]call) Readers {
	var r Readers
	for _, d := range declared {
		name := d.name
		read := func(file, variable string, maxBytes int64) (check.Extractor, error) {
			src, err := core.CheckSource(name, file, maxBytes)
			if err != nil {
				return nil, err
			}
			if variable == "BAD" || (name == "github-actions" && variable == "GITHUB_X") {
				return nil, fmt.Errorf("%s: variable %s refused", name, variable)
			}
			*calls = append(*calls, call{name, file, maxBytes})
			f := fake{src: src, name: variable, ci: name != "envfile" && name != "shell"}
			if name == "gitlab-ci" {
				return includer{f}, nil
			}
			return f, nil
		}
		e := *d.env
		if name == "shell" {
			e.Sniffs, e.SniffBytes = func(b []byte) bool { return string(b) == "#!/bin/sh" }, 9
		}
		r.List = append(r.List, Reader{Name: name, EnvChoice: e, Read: read})
	}
	return r
}

type fake struct {
	src  core.Source
	name string
	ci   bool
}

func (f fake) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return f.Extract(ctx, root)
}

func (f fake) Extract(_ context.Context, root string) ([]check.Value, error) {
	data, err := f.src.Read(root)
	if err != nil {
		return nil, err
	}
	var out []check.Value
	for i, line := range strings.Split(string(data), "\n") {
		switch line {
		case "FAIL":
			return nil, errors.New(f.src.File + ": it fails")
		case "NONE":
			return nil, &check.NoValue{Reason: "none"}
		case "REPEAT":
			for range 10 {
				out = append(out, out[0])
			}
		}
		if v, ok := strings.CutPrefix(line, f.name+"="); ok {
			cv := check.Value{Text: v, At: check.Location{File: f.src.File, Line: i + 1}, Key: f.name, HasKey: true}
			if f.ci {
				cv.Origin = fmt.Sprintf("job %q", "j")
			}
			out = append(out, cv)
		}
	}
	return out, nil
}

// includer is a fake gitlab-ci reader: it reads its file and, as if it
// included them, twice its bytes more, all within its max_bytes.
type includer struct{ fake }

func (f includer) ExtractRead(ctx context.Context, root string) ([]check.Value, int64, error) {
	vs, err := f.Extract(ctx, root)
	data, _ := f.src.Read(root)
	read := 3 * int64(len(data))
	if read > f.src.Limit {
		return nil, read, fmt.Errorf("%s: the file and its includes are over the limit of %d bytes", f.src.File, f.src.Limit)
	}
	return vs, read, err
}

// run builds s with the fakes and extracts it in root, each value as
// text@file:line [origin].
func run(t *testing.T, s Spec, root string) (string, []call, error) {
	t.Helper()
	var calls []call
	x, err := Build(s, fakes(&calls))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	calls = nil
	vs, err := x.Extract(t.Context(), root)
	var out []string
	for _, v := range vs {
		if v.HasKey || v.Key != "" {
			t.Errorf("%s:%d keeps its key %q", v.At.File, v.At.Line, v.Key)
		}
		out = append(out, fmt.Sprintf("%s@%s:%d [%s]", v.Text, v.At.File, v.At.Line, v.Origin))
	}
	return strings.Join(out, " "), calls, err
}

// git runs git in dir with no user or system config.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// tracked is a repository of files, of which git tracks those of add.
func tracked(t *testing.T, files map[string]string, add ...string) string {
	t.Helper()
	root := extracttest.Repo(t, files)
	git(t, root, "init", "-q")
	git(t, root, append([]string{"add", "--"}, add...)...)
	return root
}

// Of the readers a name chooses, the one of lowest Order reads it, in
// whatever order Readers lists them, and each Chooses sees the name
// cleaned, with slashes; a message lists the readers by Order. Planted
// breaks: the highest Order taken; the first reader that takes a name
// taken; the name not cleaned; the names listed as Readers lists them.
func TestChooseTakesTheLowestOrder(t *testing.T) {
	var seen []string
	all := func(file string) bool { seen = append(seen, file); return true }
	only := func(file string) bool { return file == "x.c" }
	r := Readers{List: []Reader{
		{Name: "b", EnvChoice: check.EnvChoice{Order: 2, Chooses: all}},
		{Name: "c", EnvChoice: check.EnvChoice{Order: 3, Chooses: only}},
		{Name: "a", EnvChoice: check.EnvChoice{Order: 1, Chooses: all}},
	}}
	if got := r.Choose("./sub/../x.c"); got != "a" {
		t.Errorf("chose %q, want a", got)
	}
	if len(seen) == 0 || slices.ContainsFunc(seen, func(f string) bool { return f != "x.c" }) {
		t.Errorf("Chooses saw %q, want x.c, cleaned", seen)
	}
	if got := r.names(); got != "a, b, c" {
		t.Errorf("names %q, want a, b, c", got)
	}
	if got := (Readers{List: r.List[1:2]}).Choose("y"); got != "" {
		t.Errorf("chose %q for a name no reader takes", got)
	}
}

// Of the readers whose Sniffs takes a file's first bytes, the one of
// lowest Order reads it, in whatever order Readers lists them; each Sniffs
// sees at most its own SniffBytes, and a reader that declares none is not
// asked. Planted breaks: the sniffer of highest Order taken; the readers
// asked in List's order.
func TestSniffTakesTheLowestOrder(t *testing.T) {
	var seen []string
	hash := func(head []byte) bool { seen = append(seen, string(head)); return strings.HasPrefix(string(head), "#") }
	r := Readers{List: []Reader{
		{Name: "c", EnvChoice: check.EnvChoice{Order: 3, Sniffs: hash, SniffBytes: 1}},
		{Name: "none", EnvChoice: check.EnvChoice{Order: 0}},
		{Name: "b", EnvChoice: check.EnvChoice{Order: 2, Sniffs: hash, SniffBytes: 3}},
	}}
	if got := r.Sniff([]byte("#!/bin/sh")); got != "b" {
		t.Errorf("sniffed %q, want b", got)
	}
	if !slices.Contains(seen, "#!/") {
		t.Errorf("Sniffs saw %q, want #!/, cut at b's SniffBytes", seen)
	}
	if got := r.Sniff([]byte("x")); got != "" {
		t.Errorf("sniffed %q for bytes no reader takes", got)
	}
}

// Each refusal of a spec, by its reason. Planted breaks: the name test
// dropped; a file named twice as a and ./a let through; a readers: entry
// for a file files does not list let through; the reader a glob's name
// chooses not built, which let GITHUB_X load.
func TestBuildRefuses(t *testing.T) {
	ok := []string{".env"}
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Files: ok}, "env: name is required"},
		{Spec{Name: "a-b", Files: ok}, `env: name "a-b" is no variable name every reader reads`},
		{Spec{Name: "1A", Files: ok}, "is no variable name"},
		{Spec{Name: strings.Repeat("A", maxName+1), Files: ok}, "is no variable name"},
		{Spec{Name: strings.Repeat("A", maxName), Files: []string{"*"}}, ""},
		{Spec{Name: "A"}, "env: files is required"},
		{Spec{Name: "A", Files: ok, MaxBytes: -1}, "env: max_bytes is -1"},
		{Spec{Name: "A", Files: []string{"../x"}}, `env: file "../x" is outside the repository`},
		{Spec{Name: "A", Files: []string{"a.sh", "./a.sh"}}, `env: files names "./a.sh" twice`},
		{Spec{Name: "A", Files: []string{"a[.sh"}}, `env: files entry "a[.sh" is no glob`},
		{Spec{Name: "A", Files: ok, Readers: map[string]string{"b.sh": "shell"}}, `env: readers names "b.sh", which files does not list`},
		{Spec{Name: "A", Files: ok, Readers: map[string]string{".env": "yaml"}}, `env: readers gives ".env" the reader "yaml"; write one of envfile, shell, gitlab-ci, github-actions, jenkins`},
		{Spec{Name: "BAD", Files: []string{"Jenkinsfile"}}, "env: jenkins: variable BAD refused"},
		{Spec{Name: "GITHUB_X", Files: []string{".github/workflows/*.yml"}}, "env: github-actions: variable GITHUB_X refused"},
		{Spec{Name: "GITHUB_X", Files: []string{"ci.yml"}, Readers: map[string]string{"./ci.yml": "github-actions"}}, "variable GITHUB_X refused"},
	} {
		var calls []call
		_, err := Build(c.s, fakes(&calls))
		if c.want == "" {
			if err != nil {
				t.Errorf("%+v: %v", c.s, err)
			}
			continue
		}
		extracttest.WantError(t, err, c.want)
	}
	// A name the readers env would choose all accept loads, and a file
	// whose reader its shebang may choose is not built until it is read.
	var calls []call
	if _, err := Build(Spec{Name: "GITHUB_X", Files: []string{".env", "bin/deploy", "*.sh"}}, fakes(&calls)); err != nil {
		t.Errorf("Build: %v", err)
	}
}

// Each file is read by the reader its name chooses, or its readers:
// entry, or its shebang, in the order files lists them, to the bytes the
// files before left; each value names its reader, and the job a CI
// reader names, and drops its key. A file that sets no value, or is
// check.NoValue, adds none. Planted breaks: the override not used; the
// shebang not read; the reader left out of the origin; the budget not
// passed down, which built each reader with the whole max_bytes; a
// gitlab-ci file charged its own size alone, without its includes.
func TestExtract(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		".env":           "A=1\nB=2\n",
		"deploy.sh":      "A=2\n",
		"bin/deploy":     "#!/bin/sh\nA=3\n",
		"ci.yml":         "A=4\n",
		".gitlab-ci.yml": "NONE\n",
		"Jenkinsfile":    "B=1\n",
	})
	got, calls, err := run(t, Spec{Name: "A", MaxBytes: 1000,
		Files:   []string{".env", "deploy.sh", "bin/deploy", "ci.yml", ".gitlab-ci.yml", "Jenkinsfile"},
		Readers: map[string]string{"ci.yml": "gitlab-ci"},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	want := `1@.env:1 [envfile] 2@deploy.sh:1 [shell] 3@bin/deploy:2 [shell] 4@ci.yml:1 [gitlab-ci job "j"]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// A gitlab-ci file is charged what it read with its includes, three
	// times its 4 and 5 bytes.
	wantCalls := []call{{"envfile", ".env", 1000}, {"shell", "deploy.sh", 992}, {"shell", "bin/deploy", 988},
		{"gitlab-ci", "ci.yml", 974}, {"gitlab-ci", ".gitlab-ci.yml", 962}, {"jenkins", "Jenkinsfile", 947}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("calls %v\nwant  %v", calls, wantCalls)
	}
}

// What makes one file unknown makes the extractor unknown: a file no
// reader reads, a file that is not there, and a reader's error. Planted
// break: a file no reader reads passed over.
func TestExtractRefuses(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"run": "A=1\n", "fail.sh": "FAIL\n"})
	_, _, err := run(t, Spec{Name: "A", Files: []string{"run"}}, root)
	extracttest.WantError(t, err, `env: no reader reads "run": its name is none the readers' are, and it starts with no #! line of a shell; give it one under readers:`)
	_, _, err = run(t, Spec{Name: "A", Files: []string{"gone.sh"}}, root)
	extracttest.WantError(t, err, "read gone.sh:")
	_, _, err = run(t, Spec{Name: "A", Files: []string{"fail.sh"}}, root)
	extracttest.WantError(t, err, "fail.sh: it fails")
}

// A glob matches the files git tracks, a segment at a time, in git's
// order; a file it does not track is not read, and a glob that matches
// none adds no value, an empty list and no error. A file named twice, by a
// file and a glob, is read once, by the first entry's reader. Planted
// breaks: the glob walked over the directory, which read the untracked
// b.sh; * across a /, which read sub/c.sh; the dedupe dropped.
func TestGlob(t *testing.T) {
	root := tracked(t, map[string]string{"a.sh": "A=1\n", "b.sh": "A=2\n", "sub/c.sh": "A=3\n", "z.sh": "A=4\n"}, "a.sh", "sub/c.sh", "z.sh")
	got, _, err := run(t, Spec{Name: "A", Files: []string{"z.sh", "*.sh", "sub/*.sh"}, Readers: map[string]string{"z.sh": "envfile"}}, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := "4@z.sh:1 [envfile] 1@a.sh:1 [shell] 3@sub/c.sh:1 [shell]"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	var calls []call
	x, err := Build(Spec{Name: "A", Files: []string{"*.env"}}, fakes(&calls))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), root)
	if err != nil || vs == nil || len(vs) != 0 {
		t.Errorf("a glob that matches none: %v, %v; want an empty list", vs, err)
	}
}

// A glob needs git's index, and a root with none is refused; a spec of
// files alone reads without it. Planted break: the index read for every
// spec, which refused the files alone.
func TestGlobNeedsGit(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.sh": "A=1\n"})
	if got, _, err := run(t, Spec{Name: "A", Files: []string{"a.sh"}}, root); err != nil || got != "1@a.sh:1 [shell]" {
		t.Errorf("files alone: %q, %v", got, err)
	}
	_, _, err := run(t, Spec{Name: "A", Files: []string{"*.sh"}}, root)
	extracttest.WantError(t, err, `env: files lists the glob "*.sh": the repository root holds no .git`)
}

// A file over the bytes the files before it left is known by
// core.ErrTooLarge, which the note keeps, and not by the words of core's
// message, so the note survives a reworded message (#197). Planted break:
// Extract's errors.Is replaced by a match of "bytes, past the limit of",
// which is the old text match after core rewords its message.
func TestAFileOverTheBytesLeftIsKnownByItsSentinel(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.sh": "A=1\n", "b.sh": "A=2\n"})
	_, _, err := run(t, Spec{Name: "A", Files: []string{"a.sh", "b.sh"}, MaxBytes: 6}, root)
	if !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("%v: is not core.ErrTooLarge", err)
	}
	if !strings.HasSuffix(err.Error(), "; the files env reads, and a GitLab CI file's includes, hold at most max_bytes, 6, in all") {
		t.Fatalf("%v: lacks the note on the files' budget", err)
	}
}

// The files hold at most max_bytes in all, a GitLab CI file's includes
// too, and the values too, and at most core.MaxValues values; values of
// exactly max_bytes are kept. A file past what the files before left is
// refused, naming max_bytes, and an empty file after them is read.
// Planted breaks: each bound dropped; the values' bound taken as >=; the
// budget not charged, which read b.sh to the whole max_bytes; a gitlab-ci
// file charged its own size, which let the second read twice its budget;
// the limit of 1 after a spent budget dropped, which built the reader
// with 0, the default.
func TestBounds(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.sh": "A=1\n", "b.sh": "A=2\n", "empty.sh": "",
		"c.sh": "A=" + strings.Repeat("x", 20) + "\nREPEAT\n", "1/.gitlab-ci.yml": "A=1\n", "2/.gitlab-ci.yml": "A=1\n"})
	_, _, err := run(t, Spec{Name: "A", Files: []string{"a.sh", "b.sh"}, MaxBytes: 6}, root)
	extracttest.WantError(t, err, "read b.sh: the file is 4 bytes, over the limit of 2 bytes")
	extracttest.WantError(t, err, "hold at most max_bytes, 6, in all")
	// The reader of a file after the budget is spent is built with a limit
	// of 1, not 0, which every reader reads as the default of 32 MiB.
	if _, calls, err := run(t, Spec{Name: "A", Files: []string{"a.sh", "empty.sh"}, MaxBytes: 4}, root); err != nil || len(calls) != 2 || calls[1].maxBytes != 1 {
		t.Errorf("an empty file after the budget is spent: %v, %v; want its reader built with 1", calls, err)
	}
	_, _, err = run(t, Spec{Name: "A", Files: []string{"1/.gitlab-ci.yml", "2/.gitlab-ci.yml"}, MaxBytes: 20}, root)
	extracttest.WantError(t, err, "2/.gitlab-ci.yml: the file and its includes are over the limit of 8 bytes")
	if _, _, err := run(t, Spec{Name: "A", Files: []string{"c.sh"}, MaxBytes: 220}, root); err != nil {
		t.Errorf("values of exactly max_bytes: %v", err)
	}
	_, _, err = run(t, Spec{Name: "A", Files: []string{"c.sh"}, MaxBytes: 219}, root)
	extracttest.WantError(t, err, "env: the values of A hold more than 219 bytes in all")

	many := strings.Repeat("A=\n", core.MaxValues+1)
	_, _, err = run(t, Spec{Name: "A", Files: []string{"m.sh"}}, extracttest.Repo(t, map[string]string{"m.sh": many}))
	extracttest.WantError(t, err, fmt.Sprintf("env: the files hold more than %d values of A, the limit", core.MaxValues))
}

// The entries of files are held to 64, and the files a spec reads to
// maxFiles, empty ones too, witnessed at 2, a limit the test sets; the
// refusal still names maxFiles. Planted
// breaks: either bound dropped; the files' bound one more.
func TestFileBounds(t *testing.T) {
	var calls []call
	entries := make([]string, maxEntries+1)
	for i := range entries {
		entries[i] = fmt.Sprintf("f%d.sh", i)
	}
	if _, err := Build(Spec{Name: "A", Files: entries[:maxEntries]}, fakes(&calls)); err != nil {
		t.Errorf("%d entries: %v", maxEntries, err)
	}
	_, err := Build(Spec{Name: "A", Files: entries}, fakes(&calls))
	extracttest.WantError(t, err, "env: files lists 65 entries, over the limit of 64")

	root := tracked(t, map[string]string{"a.sh": "", "b.sh": "", "c.sh": ""}, ".")
	x, err := Build(Spec{Name: "A", Files: []string{"*.sh"}}, fakes(&calls))
	if err != nil {
		t.Fatal(err)
	}
	limited := x.(extractor)
	limited.files.FileLimit = 3
	if _, err := limited.Extract(t.Context(), root); err != nil {
		t.Errorf("3 files under a limit of 3: %v", err)
	}
	limited.files.FileLimit = 2
	_, err = limited.Extract(t.Context(), root)
	extracttest.WantError(t, err, `env: files names more than 10000 files, the limit, at "*.sh"`)
}

// The globs, each with a wildcard first, are matched against the paths of
// every tracked file, and the bytes matched, the globs times the paths'
// bytes, are held to maxMatchBytes, witnessed at the exact count, a limit
// the test sets: 3 globs over the 3 paths of 4, 4 and 8 bytes match 48
// bytes. The refusal still names maxMatchBytes, and comes before any glob is
// matched: under a file limit of 1, which the first glob's two files pass,
// the bytes are refused, not the files. Planted breaks: the bound dropped;
// each path counted once whatever the globs, which matched 16; the bound one
// more; the bytes charged as each glob is matched, which refused the files.
func TestMatchBound(t *testing.T) {
	root := tracked(t, map[string]string{"a.sh": "", "b.sh": "", "sub/c.sh": ""}, ".")
	var calls []call
	x, err := Build(Spec{Name: "A", Files: []string{"*.sh", "*/*.sh", "x*"}}, fakes(&calls))
	if err != nil {
		t.Fatal(err)
	}
	limited := x.(extractor)
	limited.files.MatchLimit = 48
	if _, err := limited.Extract(t.Context(), root); err != nil {
		t.Errorf("48 bytes matched under a limit of 48: %v", err)
	}
	limited.files.MatchLimit = 47
	limited.files.FileLimit = 1
	_, err = limited.Extract(t.Context(), root)
	extracttest.WantError(t, err, fmt.Sprintf(`env: files lists 3 globs, and matching each against the paths git tracks, below its directory when it starts with one, reads 48 bytes of path, more than %d, the limit; list fewer globs, or start each with its directory`, maxMatchBytes))
}

// A file two entries name takes the reader one of them gives it, whichever
// comes first, and two readers for it refuse it, naming both entries.
// Planted breaks: the first entry's reader kept, which read .env.ci with
// envfile when the glob came first; the conflict let through.
func TestReaderOfAFileNamedTwice(t *testing.T) {
	root := tracked(t, map[string]string{".env.ci": "A=1\n"}, ".")
	for _, files := range [][]string{{".env*", ".env.ci"}, {".env.ci", ".env*"}} {
		got, _, err := run(t, Spec{Name: "A", Files: files, Readers: map[string]string{".env.ci": "shell"}}, root)
		if err != nil || got != "1@.env.ci:1 [shell]" {
			t.Errorf("%v: %q, %v; want shell", files, got, err)
		}
	}
	_, _, err := run(t, Spec{Name: "A", Files: []string{".env*", ".env.ci"}, Readers: map[string]string{".env*": "envfile", ".env.ci": "shell"}}, root)
	extracttest.WantError(t, err, `env: files entries ".env*" and ".env.ci" both name ".env.ci", and readers: gives them envfile and shell`)
}

// Place cleans each file and each readers: entry, sorts the files and
// leaves max_bytes out, so ./a and a, in either order, are one place, and
// another reader for a file is another place. Resolve writes max_bytes out
// and copies the files and the readers, so a change to it does not reach
// the spec. Planted breaks: the readers left out of Place; the sort
// dropped; Resolve sharing the spec's map.
func TestPlaceAndResolve(t *testing.T) {
	a := Spec{Name: "A", Files: []string{"./b.sh", "a.sh"}, Readers: map[string]string{"./b.sh": "envfile"}, MaxBytes: 5}
	b := Spec{Name: "A", Files: []string{"a.sh", "b.sh"}, Readers: map[string]string{"b.sh": "envfile"}}
	if !reflect.DeepEqual(Place(a), Place(b)) {
		t.Errorf("one place: %+v and %+v", Place(a), Place(b))
	}
	for _, c := range []Spec{
		{Name: "A", Files: []string{"a.sh", "b.sh"}},
		{Name: "A", Files: []string{"a.sh", "b.sh"}, Readers: map[string]string{"b.sh": "shell"}},
		{Name: "B", Files: []string{"a.sh", "b.sh"}, Readers: map[string]string{"b.sh": "envfile"}},
	} {
		if reflect.DeepEqual(Place(a), Place(c)) {
			t.Errorf("another place reads as one: %+v", c)
		}
	}
	r := Resolve(b)
	if r.MaxBytes != core.DefaultMaxBytes || Resolve(a).MaxBytes != 5 {
		t.Errorf("max_bytes %d and %d, want the default and 5", r.MaxBytes, Resolve(a).MaxBytes)
	}
	r.Files[0], r.Readers["b.sh"] = "x", "shell"
	if b.Files[0] != "a.sh" || b.Readers["b.sh"] != "envfile" {
		t.Errorf("Resolve shares the spec's files or readers: %+v", b)
	}
	if got := Resolve(Spec{Name: "A", Files: []string{"a"}}); got.Readers != nil {
		t.Errorf("Resolve made readers %v from none", got.Readers)
	}
	var calls []call
	if _, err := Build(b, fakes(&calls)); err != nil || !Describe(b).NotLiteral {
		t.Errorf("Build: %v, or no not literal value", err)
	}
}
