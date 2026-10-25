package githubactions

import (
	"strings"
	"testing"
)

// Any workflow resolves without a crash, into values each at a line of the
// file, for any job, step and variable or key the spec loads with. The
// seeds hold each keyword the resolver reads.
func FuzzGitHubActions(f *testing.F) {
	seeds := []struct{ body, job, step, name string }{
		{"env: {V: w}\njobs:\n  t:\n    env: {V: j}\n    steps:\n      - id: s\n        env: {V: s}\n", "t", "s", "V"},
		{"jobs:\n  t:\n    strategy:\n      matrix:\n        a: [1, 2]\n        exclude: [{a: 1}]\n        include: [{a: 3, b: x}, {b: y}]\n    runs-on: ${{ matrix.a }}-${{ matrix.b }}\n", "t", "", "runs-on"},
		{"jobs:\n  t:\n    container: {env: {V: c}}\n    steps:\n      - run: echo >> $GITHUB_ENV\n      - parallel: [{name: p, with: {x: 3.10}}]\n", "t", "p", "with.x"},
		{"jobs:\n  t:\n    uses: ./.github/workflows/b.yml\n    with: {u: m}\n", "t", "", "with.u"},
		{"x: &a {V: 1}\njobs:\n  t:\n    env: *a\n    strategy: {matrix: {n: [{v: 1}]}}\n    steps: [{id: s, env: {V: '${{ matrix.n.v }}'}}]\n", "t", "s", "V"},
	}
	for _, s := range seeds {
		f.Add(s.body, s.job, s.step, s.name)
	}
	f.Fuzz(func(t *testing.T, body, job, step, name string) {
		s := spec(job, step, name)
		s.MaxBytes = 1 << 16
		x, err := build(s)
		if err != nil || int64(len(body)) > s.MaxBytes {
			return
		}
		got, err := x.(extractor).fromData([]byte(body))
		if err != nil {
			return
		}
		lines := strings.Count(body, "\n") + 1
		for _, v := range got {
			if v.At.File != wf || v.At.Line < 1 || v.At.Line > lines {
				t.Fatalf("%q at %s:%d, outside the file", v.Text, v.At.File, v.At.Line)
			}
		}
	})
}
