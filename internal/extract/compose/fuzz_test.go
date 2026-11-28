package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// Any main file, override, extends file and .env file load without a
// crash, into values each at a line inside the file it names, for any
// service and key the spec loads with. The seeds hold each key the walker
// and locate read.
func FuzzCompose(f *testing.F) {
	seeds := []struct{ main, over, base, env, service, key string }{
		{"services:\n  web:\n    extends: {file: base.yaml, service: b}\n    image: w:${T}\n", "services:\n  web:\n    environment: [A=1]\n", "services:\n  b:\n    environment: {A: 0, B: 1}\n", "T=1\n", "web", "environment"},
		{"include: [base.yaml]\nservices:\n  web:\n    image: w\n    ports: [\"80:80\", \"8000-8001:90\"]\n", "services:\n  web:\n    ports: [{target: 1}]\n", "services:\n  b:\n    image: b\n", "", "web", "ports"},
		{"services:\n  web:\n    image: w\n    labels: [a.b=c]\n    env_file: [.env]\n    profiles: [p]\n", "", "", "COMPOSE_PROFILES=p\nX=${Y:-z}\n", "web", "labels.a.b"},
		{"x-a: &a {image: a}\nservices:\n  web:\n    <<: *a\n    build: {context: ., args: [K]}\n", "services:\n  web: !reset {}\n", "", "", "web", "build"},
		{"include:\n  - path: [base.yaml]\n    env_file: .env\nservices:\n  web:\n    extends: b\n  b: {image: b, command: run}\n", "", "services:\n  c:\n    image: c\n", "", "web", "command.0"},
	}
	for _, s := range seeds {
		f.Add(s.main, s.over, s.base, s.env, s.service, s.key)
	}
	f.Fuzz(func(t *testing.T, mainFile, over, base, env, service, key string) {
		x, err := Build(Spec{OneFile: oneFileMax(1 << 16), Overrides: []string{"override.yaml"}, Service: service, Key: key}, yamlParse)
		if err != nil {
			return
		}
		files := map[string]string{"compose.yaml": mainFile, "override.yaml": over, "base.yaml": base, ".env": env}
		root := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		vs, err := x.Extract(t.Context(), root)
		if err != nil {
			return
		}
		insideFiles(t, vs, files)
	})
}
