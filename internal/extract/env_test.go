package extract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/env"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The env kind asks each reader through envReaders: one file of each,
// each value named by its reader and its job, step or stage, and a script
// its shebang chooses. Planted breaks: each reader's row given another
// reader's function, which read the file in another format or refused it;
// the shell's test of a shebang dropped.
func TestEnvAsksEachReader(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		// compose reads a : as =; a shell reads the line as a command.
		".env":                     "PORT: 1\n",
		"deploy.sh":                "export PORT=2\n",
		"bin/run":                  "#!/usr/bin/env bash\nPORT=\"$(cat port)\"\n",
		".gitlab-ci.yml":           "build:\n  variables: {PORT: \"3\"}\n  script: [b]\n",
		".github/workflows/ci.yml": "on: push\njobs:\n  test:\n    runs-on: x\n    steps:\n      - id: up\n        env: {PORT: \"4\"}\n        run: up\n",
		"Jenkinsfile":              "pipeline {\n  environment { PORT = '5' }\n  stages { stage('a') { steps { sh 'a' } } }\n}\n",
	})
	x, err := Build(Spec{Env: &env.Spec{Name: "PORT", Files: []string{".env", "deploy.sh", "bin/run", ".gitlab-ci.yml", ".github/workflows/ci.yml", "Jenkinsfile"}}})
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		got = append(got, fmt.Sprintf("%s%s@%s:%d [%s]", v.Text, mark, v.At.File, v.At.Line, v.Origin))
	}
	want := `1@.env:1 [envfile]; 2@deploy.sh:1 [shell]; "$(cat port)"!@bin/run:2 [shell]; 3@.gitlab-ci.yml:2 [gitlab-ci job "build"]; ` +
		`4@.github/workflows/ci.yml:7 [github-actions job "test" step "up"]; 5@Jenkinsfile:2 [jenkins pipeline]`
	if g := strings.Join(got, "; "); g != want {
		t.Errorf("got  %s\nwant %s", g, want)
	}
}

// A GitLab CI file's includes are charged to env's max_bytes: two files
// that each include one of 2050 bytes are refused under 3000, where
// charging each file its own size read 4134 bytes. Planted break: gitlab-ci
// Every's ExtractRead not used, which read them all.
func TestEnvChargesGitLabIncludes(t *testing.T) {
	big := "x:\n  script: [\"" + strings.Repeat("y", 2030) + "\"]\n"
	root := extracttest.Repo(t, map[string]string{
		"big.yml":          big,
		".gitlab-ci.yml":   "include: big.yml\na: {script: [s]}\n",
		"a/.gitlab-ci.yml": "include: big.yml\nb: {script: [s]}\n",
	})
	x, err := Build(Spec{Env: &env.Spec{Name: "PORT", Files: []string{".gitlab-ci.yml", "a/.gitlab-ci.yml"}, MaxBytes: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.Extract(root)
	extracttest.WantError(t, err, "over the limit of")
}
