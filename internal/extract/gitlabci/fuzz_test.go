package gitlabci

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// Any main file and included file resolve without a crash, into values
// each at a line inside the file it names, for any job and any variable or
// key the spec loads with. The seeds hold each keyword the resolver reads.
func FuzzGitLabCI(f *testing.F) {
	seeds := []struct{ main, inc, job, name string }{
		{"include: a.yml\nvariables:\n  V: top\njob:\n  extends: .t\n  variables:\n    V: job\n", ".t:\n  image: x\n", "job", "V"},
		{"workflow:\n  rules:\n    - if: $A\n      variables: {V: w}\n    - when: always\nvariables: {V: t}\njob:\n  rules: [{if: $B, variables: {V: r}}, {when: never}, {}]\n  script: [x]\n", "", "job", "V"},
		{".s: {script: [a, [b]]}\njob:\n  script: [!reference [.s, script], c]\n  inherit: {default: [image]}\ndefault: {image: i}\n", "", "job", "script"},
		{"include: [{local: /a.yml}, {template: t.yml}]\njob: {image: {name: n}}\n", "variables: {V: {value: $X, expand: false}}\n", "job", "image.name"},
		{".a: &a {variables: {V: 3.10}}\njob:\n  <<: *a\n  trigger: {include: c.yml}\n", "include: a.yml\n", "job", "V"},
		{"spec:\n  inputs: {t: {}}\n---\njob: {image: '$[[ inputs.t ]]'}\n", "", "job", "image"},
	}
	for _, s := range seeds {
		f.Add(s.main, s.inc, s.job, s.name)
	}
	f.Fuzz(func(t *testing.T, mainFile, inc, job, name string) {
		s := Spec{File: ".gitlab-ci.yml", Job: job, Key: name, MaxBytes: 1 << 16}
		if strings.ToUpper(name) == name {
			s.Key, s.Variable = "", name
		}
		x, err := Build(s, yamlParse)
		if err != nil {
			return
		}
		files := map[string]string{".gitlab-ci.yml": mainFile, "a.yml": inc}
		got, err := x.(extractor).resolve(func(src core.Source) ([]byte, error) {
			body, ok := files[filepath.Clean(src.File)]
			switch {
			case !ok:
				return nil, errors.New("no such file")
			case int64(len(body)) > src.Limit:
				return nil, errors.New("too large")
			}
			return []byte(body), nil
		})
		if err != nil {
			return
		}
		for _, v := range got {
			body, ok := files[v.At.File]
			if !ok || v.At.Line < 1 || v.At.Line > strings.Count(body, "\n")+1 {
				t.Fatalf("%q at %s:%d, outside the files read", v.Text, v.At.File, v.At.Line)
			}
		}
	})
}
