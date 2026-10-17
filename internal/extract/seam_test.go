package extract

import (
	"errors"
	"strings"
	"testing"
)

// The stub's refusals until the extractors land. The extractors replace
// Build, and this file goes with the stub.
func TestBuildRefusesNoKindOrTwo(t *testing.T) {
	for name, s := range map[string]Spec{
		"none": {},
		"two":  {YAML: &YAML{File: "a.yaml"}, JSON: &JSON{File: "a.json"}},
	} {
		_, err := Build(s)
		if err == nil || !strings.Contains(err.Error(), "exactly one of yaml, json, toml, regex") {
			t.Errorf("%s: err = %v, want the exactly one refusal", name, err)
		}
	}
}

func TestBuildOneKindIsPending(t *testing.T) {
	for name, s := range map[string]Spec{
		"yaml":  {YAML: &YAML{File: "a.yaml"}},
		"json":  {JSON: &JSON{File: "a.json"}},
		"toml":  {TOML: &TOML{File: "a.toml"}},
		"regex": {Regex: &Regex{File: "a.txt"}},
	} {
		x, err := Build(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := x.Extract("."); !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s: Extract err = %v, want ErrNotImplemented", name, err)
		}
	}
}
