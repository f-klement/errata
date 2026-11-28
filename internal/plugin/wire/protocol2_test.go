package wire

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// describeOf decodes raw as a describe reply and checks it.
func describeOf(raw string) (DescribeReply, error) {
	var d DescribeReply
	if err := Decode([]byte(raw), &d); err != nil {
		return d, err
	}
	return d, d.Check()
}

// A describe reply holds the members its version requires and none it
// does not define: protocol 1 has no operations or env, and in protocol 2
// where and fields come with extract alone. Its operations and env are
// checked. Planted breaks: each case of versionMembers dropped in turn;
// checkEnv not called; > made >= at MaxEnv.
func TestADescribeReplyHoldsItsVersionsMembers(t *testing.T) {
	const head = `"name": "k", "version": "0.1.0"`
	for raw, want := range map[string]string{
		`{"protocol": 1, ` + head + `, "where": "file", "fields": [], "operations": ["extract"]}`: "protocol 1 has no member operations or env",
		`{"protocol": 1, ` + head + `, "fields": []}`:                                             `the member "where" is missing`,
		`{"protocol": 2, ` + head + `}`:                                                           `the member "operations" is missing`,
		`{"protocol": 2, ` + head + `, "operations": ["extract"], "where": "file"}`:               `the member "fields" is missing`,
		`{"protocol": 2, ` + head + `, "operations": ["read"], "where": "file", "fields": []}`:    "where and fields are for a plugin that provides extract",
		`{"protocol": 3, ` + head + `}`:                                                           "protocol 3 is not one this errata speaks",
		`{"protocol": 2, ` + head + `, "operations": []}`:                                         "operations names no operation",
		`{"protocol": 2, ` + head + `, "operations": ["apply"]}`:                                  `operations names "apply", which is none of`,
		`{"protocol": 2, ` + head + `, "operations": ["read", "read"]}`:                           "operations names read twice",
		`{"protocol": 2, ` + head + `, "operations": ["render"], "env": ["HOME"]}`:                "env is for read and predict, and the plugin provides neither",
		`{"protocol": 2, ` + head + `, "operations": ["read"], "env": ["1HOME"]}`:                 `env lists "1HOME", which is not`,
		`{"protocol": 2, ` + head + `, "operations": ["read"], "env": ["A", "A"]}`:                "env lists A twice",
		`{"protocol": 2, ` + head + `, "operations": ["extract"], "where": "x", "fields": []}`:    `where is "x"`,
	} {
		if _, err := describeOf(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", raw, err, want)
		}
	}
	names := make([]string, MaxEnv+1)
	for i := range names {
		names[i] = `"E` + strings.Repeat("x", i) + `"`
	}
	many := strings.Join(names, ", ")
	if _, err := describeOf(`{"protocol": 2, ` + head + `, "operations": ["predict"], "env": [` + many + `]}`); err == nil || !strings.Contains(err.Error(), "more than 32") {
		t.Errorf("33 names: %v", err)
	}
	if _, err := describeOf(`{"protocol": 2, ` + head + `, "operations": ["predict"], "env": [` + strings.Join(names[:MaxEnv], ", ") + `]}`); err != nil {
		t.Errorf("32 names: %v", err)
	}
	d, err := describeOf(`{"protocol": 2, ` + head + `, "operations": ["render", "predict"], "env": ["KUBECONFIG", "_x9"]}`)
	if err != nil || !d.Provides(OpPredict) || d.Provides(OpRead) || d.Provides(OpExtract) {
		t.Fatalf("a good provider reply: %+v, %v", d, err)
	}
}

// A protocol 1 plugin provides extract alone, and is written with where
// and fields, even when it declares none; a provider is written without
// them. Planted breaks: Provides reading operations in protocol 1;
// MarshalJSON leaving out an empty fields.
func TestADescribeReplyIsWrittenAsItsVersionReadsIt(t *testing.T) {
	v1 := DescribeReply{Protocol: Protocol, Name: "k", Version: "0.1.0", Where: WhereFile, Operations: []string{OpRead}}
	if !v1.Provides(OpExtract) || v1.Provides(OpRead) {
		t.Fatal("a protocol 1 plugin provides extract alone")
	}
	v1.Operations = nil
	for _, d := range []DescribeReply{v1, {Protocol: Protocol2, Name: "k", Version: "0.1.0", Operations: []string{OpRead}, Env: []string{"HOME"}}} {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := describeOf(string(raw)); err != nil {
			t.Errorf("%s does not read back: %v", raw, err)
		}
	}
}

// A render, read or predict request is strict about its op, spec and
// bound, and a reply holds exactly one of files, error and gap; files
// null is refused, and a file of nil data is written as an empty one.
// Planted breaks: the op check dropped; the reply's count check dropped;
// File.MarshalJSON leaving nil data nil.
func TestProviderRequestsAndReplies(t *testing.T) {
	for raw, want := range map[string]string{
		`{"protocol": 2, "op": "apply", "spec": {}, "files": [], "max_bytes": 1}`: `the op "apply" is none of`,
		`{"protocol": 2, "op": "read", "spec": [], "files": [], "max_bytes": 1}`:  "the spec: an object is required",
		`{"protocol": 2, "op": "read", "spec": {}, "files": [], "max_bytes": 0}`:  "max_bytes is 0",
		`{"protocol": 2, "op": "read", "spec": {}, "max_bytes": 1}`:               `the member "files" is missing`,
	} {
		var r ProviderRequest
		if err := Decode([]byte(raw), &r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", raw, err, want)
		}
	}
	var req ProviderRequest
	if err := Decode([]byte(`{"protocol": 2, "op": "predict", "spec": {"a": 1}, "files": [], "max_bytes": 1}`), &req); err != nil {
		t.Errorf("a good request: %v", err)
	}
	for raw, ok := range map[string]bool{
		`{"files": []}`:              true,
		`{"gap": "g"}`:               true,
		`{"error": "e"}`:             true,
		`{"files": [], "gap": "g"}`:  false,
		`{}`:                         false,
		`{"files": [{"path": "a"}]}`: false,
	} {
		var r ProviderReply
		if err := Decode([]byte(raw), &r); (err == nil) != ok {
			t.Errorf("%s: got %v, want ok %v", raw, err, ok)
		}
	}
	gap := "g"
	for _, r := range []ProviderReply{{}, {Gap: &gap}, {Error: &gap}} {
		raw, _ := json.Marshal(r)
		var back ProviderReply
		if err := Decode(raw, &back); err != nil {
			t.Errorf("%s does not read back: %v", raw, err)
		}
	}
	var empty ProviderReply
	if err := Decode([]byte(`{"files": []}`), &empty); err != nil || empty.Files == nil {
		t.Errorf("an empty files reply: %+v, %v", empty, err)
	}
	if err := Decode([]byte(`{"files": null}`), &empty); err == nil || !strings.Contains(err.Error(), "holds null") {
		t.Errorf("files null: %v", err)
	}
	raw, err := json.Marshal(ProviderReply{Files: []File{{Path: "a"}}})
	var back ProviderReply
	if err != nil || string(raw) != `{"files":[{"path":"a","data":""}]}` || Decode(raw, &back) != nil || back.Files[0].Data == nil {
		t.Errorf("a file of nil data is written %s, %v, and reads back as %+v", raw, err, back)
	}
}

// describe's env names none of the variables errata itself reads, in any
// case, and the message says why; a job token errata does not read stays
// the plugin's choice. Planted breaks: errataOwn comparing in one case
// alone; checkEnv not asking it.
func TestEnvNamesNoneOfErratasOwn(t *testing.T) {
	const head = `"protocol": 2, "name": "k", "version": "0.1.0", "operations": ["read"]`
	for _, name := range append(slices.Clone(ErrataEnv), "github_token", "Errata_Plugin_Source") {
		_, err := describeOf(`{` + head + `, "env": ["HOME", "` + name + `"]}`)
		if err == nil || !strings.Contains(err.Error(), "a variable errata itself reads: errata passes its own credential and settings to no plugin") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := describeOf(`{` + head + `, "env": ["CI_JOB_TOKEN", "GITHUB_TOKENS"]}`); err != nil {
		t.Errorf("names errata does not read: %v", err)
	}
}
