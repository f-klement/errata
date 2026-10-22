package extracttest

// The readers that port another tool's reader are compared with it through
// corpora of generated files and what that tool made of each. None of the
// tools is in the CI image, so the corpora are committed beside each
// reader's tests, and its TestRegenerateOracleCorpora writes them again
// where the tools are installed.

import (
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// OracleCase is one generated file and what the oracle read from it.
type OracleCase struct {
	Input    string     `json:"input,omitempty"`
	InputHex string     `json:"input_hex,omitempty"` // an input that is not UTF-8, which JSON cannot hold
	Entries  [][]string `json:"entries,omitempty"`
	Refused  string     `json:"refused,omitempty"`
	// What ansible read, by group and by host: a str value as the hex of its
	// bytes, any other as {"repr": ...}.
	Hosts     map[string][]string                   `json:"hosts,omitempty"`
	Children  map[string][]string                   `json:"children,omitempty"`
	GroupVars map[string]map[string]json.RawMessage `json:"group_vars,omitempty"`
	HostVars  map[string]map[string]json.RawMessage `json:"host_vars,omitempty"`
	// The hosts ansible expands a host pattern to.
	Expanded []string `json:"expanded,omitempty"`
	// What PHP read, by section: a string, or an object of key[] values.
	Sections map[string]map[string]json.RawMessage `json:"sections,omitempty"`
}

// File is the case's file, Input or the bytes of InputHex.
func (c OracleCase) File() []byte {
	if c.InputHex != "" {
		b, _ := hex.DecodeString(c.InputHex)
		return b
	}
	return []byte(c.Input)
}

// OracleCases is how many files a corpus holds.
const OracleCases = 800

// GenerateCorpus is OracleCases files gen draws, from a generator seeded
// with seed, so a corpus is of the same inputs on every run.
func GenerateCorpus(seed uint64, gen func(*rand.Rand) string) []string {
	r := rand.New(rand.NewPCG(seed, seed))
	out := make([]string, OracleCases)
	for i := range out {
		out[i] = gen(r)
	}
	return out
}

// WriteCorpus runs the oracle command over every input and writes what it
// read, case by case, to corpus.
func WriteCorpus(t *testing.T, corpus string, inputs []string, command ...string) {
	t.Helper()
	files := writeInputs(t, inputs)
	writeResults(t, corpus, inputs, runOracle(t, append(command, files...)))
}

// WriteCorpusEach is writeCorpus with one process for each input.
func WriteCorpusEach(t *testing.T, corpus string, inputs []string, command ...string) {
	t.Helper()
	var out strings.Builder
	for _, f := range writeInputs(t, inputs) {
		out.WriteString(runOracle(t, append(slices.Clone(command), f)))
	}
	writeResults(t, corpus, inputs, out.String())
}

func writeInputs(t *testing.T, inputs []string) []string {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for i, in := range inputs {
		p := filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	return files
}

func runOracle(t *testing.T, command []string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), command[0], command[1:]...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v: %s", command[0], err, stderr.String())
	}
	return string(out)
}

// writeResults writes the oracle's lines of JSON, one for each input, to
// corpus with the inputs.
func writeResults(t *testing.T, corpus string, inputs []string, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(inputs) {
		t.Fatalf("%d results for %d inputs", len(lines), len(inputs))
	}
	cases := make([]OracleCase, len(inputs))
	for i, l := range lines {
		if err := json.Unmarshal([]byte(l), &cases[i]); err != nil {
			t.Fatalf("result %d: %v: %s", i, err, l)
		}
		if utf8.ValidString(inputs[i]) {
			cases[i].Input = inputs[i]
		} else {
			cases[i].InputHex = hex.EncodeToString([]byte(inputs[i]))
		}
	}
	// One case to a line, so a diff of the corpus names the cases it changes.
	lines = lines[:0]
	for _, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(line))
	}
	data := "[\n" + strings.Join(lines, ",\n") + "\n]\n"
	if err := os.WriteFile(corpus, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ReadCorpus is the cases of corpus, which must be of inputs.
func ReadCorpus(t *testing.T, corpus string, inputs []string) []OracleCase {
	t.Helper()
	data, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var cases []OracleCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	// The corpus is of the inputs the generators draw now, so a change to
	// a generator without the corpus written again fails here.
	if len(cases) != len(inputs) {
		t.Fatalf("%s holds %d cases, the generator draws %d; write it again", corpus, len(cases), len(inputs))
	}
	for i := range cases {
		if string(cases[i].File()) != inputs[i] {
			t.Fatalf("%s: case %d is not the generator's input; write it again", corpus, i)
		}
	}
	return cases
}

// CompareOracle reads each case with read, a reader of entries given no
// meter, and compares the entries, made into strings by entry, or the
// refusal, with the oracle's. It counts the entries it read and the cases
// both refused.
func CompareOracle[M, E any](t *testing.T, cases []OracleCase, read func([]byte, *M, func(E) error) error, entry func(E) []string) (read_, refused int) {
	t.Helper()
	for i, c := range cases {
		var got [][]string
		err := read(c.File(), nil, func(e E) error {
			got = append(got, entry(e))
			return nil
		})
		switch {
		case c.Refused != "" && err == nil:
			t.Errorf("case %d %q: the oracle refuses it (%s) and errata reads %q", i, c.Input, c.Refused, got)
		case c.Refused == "" && err != nil:
			t.Errorf("case %d %q: the oracle reads %q and errata refuses it: %v", i, c.Input, c.Entries, err)
		case c.Refused == "" && !reflect.DeepEqual(got, c.Entries):
			t.Errorf("case %d %q: errata reads %q, the oracle %q", i, c.Input, got, c.Entries)
		case c.Refused != "":
			refused++
		default:
			read_ += len(got)
		}
	}
	return read_, refused
}
