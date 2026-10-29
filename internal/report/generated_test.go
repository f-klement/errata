package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// generatedCheck is a generated check as the loader builds one.
func generatedCheck() check.Check {
	return check.Check{ID: "g", Reason: "r", Scope: check.ScopeRepo, Timeout: 30 * time.Second, At: check.Location{File: "errata.yaml", Line: 9},
		Kind: &check.Generated{File: "architecture.md", Run: []string{"python3", "scripts/build architecture.py", "--stdout"}, MaxBytes: 1 << 20}}
}

// errata list prints the generator's argv, so a reader sees what will run:
// in JSON as generated.run beside the file, the bound and the timeout, with
// no run or extract of its own, and in text under the run: label a program
// check's command takes, an argument with a space between backquotes. The
// count names the generated checks apart. The planted breaks: the listing
// reading a generated check as a program check, which listed run [] and no
// file, and the count leaving generated checks among the program checks.
func TestListGenerated(t *testing.T) {
	doc := listDocument(t, []check.Check{generatedCheck()})
	g := doc["checks"].([]any)[0].(map[string]any)
	spec, _ := g["generated"].(map[string]any)
	run, _ := spec["run"].([]any)
	if g["type"] != "generated" || g["timeout"] != "30s" || g["run"] != nil || g["extract"] != nil ||
		spec["file"] != "architecture.md" || spec["max_bytes"] != float64(1<<20) || len(run) != 3 || run[1] != "scripts/build architecture.py" {
		t.Errorf("generated check: %v", g)
	}

	checks := listChecks()
	var b bytes.Buffer
	if err := (ListText{}).WriteList(&b, checks, nil); err != nil {
		t.Fatal(err)
	}
	want := "g (repo) generated check\n  why: r\n  file: architecture.md\n  run: python3 `scripts/build architecture.py` --stdout\n  max_bytes: 1048576\n  timeout: 30s\n\n" +
		"4 checks: 2 rules, 1 program check, 1 generated check.\n"
	if !strings.HasSuffix(b.String(), want) {
		t.Errorf("text listing ends\n%s\nwant\n%s", b.String(), want)
	}
}

// A stale generated file is an erratum whose report names the line where
// it first differs: the text and JUnit reports in the message, which starts
// with file:line, and SARIF as the result's physical location, the file and
// its line, not the check in the config. A generated check whose file is
// missing points at the check. The planted break: sarifResultOf leaving
// res.At out, which placed the result at errata.yaml:9.
func TestGeneratedErratumNamesTheFirstDifferingLine(t *testing.T) {
	stale := check.Result{Check: generatedCheck(), Verdict: check.VerdictFalse,
		Message: `architecture.md:12, column 3, differs from what its generator prints: the file has "ab\n", the generator printed "ac\n"`,
		At:      check.Location{File: "architecture.md", Line: 12}}
	rep := check.Report{Results: []check.Result{stale}}

	var text, junit bytes.Buffer
	if err := (Text{}).Write(&text, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "said: architecture.md:12, column 3") {
		t.Errorf("text report:\n%s", text.String())
	}
	if err := (JUnit{}).Write(&junit, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(junit.String(), `<failure message="architecture.md:12, column 3`) {
		t.Errorf("JUnit report:\n%s", junit.String())
	}

	region := func(res check.Result) (string, float64) {
		t.Helper()
		var buf bytes.Buffer
		if err := (SARIF{Version: "test"}).Write(&buf, check.Report{Results: []check.Result{res}}); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Runs []struct {
				Results []struct {
					Locations []struct {
						Physical struct {
							Artifact struct {
								URI string `json:"uri"`
							} `json:"artifactLocation"`
							Region struct {
								StartLine float64 `json:"startLine"`
							} `json:"region"`
						} `json:"physicalLocation"`
					} `json:"locations"`
				} `json:"results"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		p := doc.Runs[0].Results[0].Locations[0].Physical
		return p.Artifact.URI, p.Region.StartLine
	}
	if uri, line := region(stale); uri != "architecture.md" || line != 12 {
		t.Errorf("SARIF places the erratum at %s:%v, want architecture.md:12", uri, line)
	}
	missing := stale
	missing.At = check.Location{}
	if uri, line := region(missing); uri != "errata.yaml" || line != 9 {
		t.Errorf("SARIF places a missing file's erratum at %s:%v, want the check at errata.yaml:9", uri, line)
	}
}
