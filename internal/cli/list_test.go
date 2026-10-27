package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
)

// loadCall is one call of a loader: the root and the config path it got.
type loadCall struct{ root, path string }

// recordingLoader returns a loader that records each call and answers with
// checks and err, whatever the file holds.
func recordingLoader(calls *[]loadCall, checks []check.Check, err error) loader {
	return func(root, path string) (config.Config, error) {
		*calls = append(*calls, loadCall{root, path})
		return config.Config{Checks: checks}, err
	}
}

// errata list and errata check read the config through the one loader the
// CLI is given, with the same root and path for the same flags, and print a
// load error the same way. The loader here holds a check no file on disk
// does, so a list that read the config by any other way, such as by calling
// config.Load itself, would not print it. The planted break: newListCmd
// calling config.Load in place of load, which fails here with exit 2 on
// the absent planted.yaml.
func TestListAndCheckShareTheLoader(t *testing.T) {
	root := t.TempDir()
	planted := []check.Check{{ID: "from-the-loader", Reason: "r", Scope: check.ScopeRepo, Run: []string{"true"}, Timeout: 7 * time.Second}}
	for _, flags := range [][]string{
		{"--root", root, "-c", "planted.yaml"},
		{"--root", root},
	} {
		var calls []loadCall
		load := recordingLoader(&calls, planted, nil)
		var out, errOut bytes.Buffer
		if code := execute(context.Background(), append([]string{"list", "-f", "json"}, flags...), &out, &errOut, load); code != ExitOK {
			t.Fatalf("list %v: exit %d, stderr %q", flags, code, errOut.String())
		}
		var doc struct {
			Version int
			Checks  []struct {
				ID      string
				Run     []string
				Timeout string
			}
		}
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("list %v: %v in %s", flags, err, out.String())
		}
		if len(doc.Checks) != 1 || doc.Checks[0].ID != "from-the-loader" || doc.Checks[0].Timeout != "7s" || !slices.Equal(doc.Checks[0].Run, []string{"true"}) {
			t.Errorf("list %v printed %s, not the loader's check", flags, out.String())
		}
		if code := execute(context.Background(), append([]string{"check"}, flags...), &out, &errOut, load); code != ExitOK {
			t.Fatalf("check %v: exit %d, stderr %q", flags, code, errOut.String())
		}
		if len(calls) != 2 || calls[0] != calls[1] {
			t.Errorf("flags %v: list and check called the loader with %v", flags, calls)
		}
	}

	var calls []loadCall
	load := recordingLoader(&calls, nil, errors.New("planted\x1b[2J"))
	var stderrs []string
	for _, cmd := range [][]string{{"check"}, {"list"}, {"list", "-f", "json"}} {
		var out, errOut bytes.Buffer
		if code := execute(context.Background(), append(cmd, "--root", root), &out, &errOut, load); code != ExitUsage {
			t.Errorf("%v on a load error: exit %d, want %d", cmd, code, ExitUsage)
		}
		if out.Len() > 0 {
			t.Errorf("%v on a load error wrote %q to stdout", cmd, out.String())
		}
		stderrs = append(stderrs, errOut.String())
	}
	want := "errata: load " + filepath.Join(root, "errata.yaml") + ": planted\\x1b[2J\n"
	for i, got := range stderrs {
		if got != want {
			t.Errorf("load error %d: stderr %q, want %q", i, got, want)
		}
	}
}

// errata list prints the run a program check starts: the default run with
// the check's id put in, not the placeholders as the file spells them. The
// planted break: config's runOf returning the check's own empty run.
func TestListPrintsTheDefaultRunExpanded(t *testing.T) {
	root := t.TempDir()
	yaml := "defaults:\n  run: [python3, scripts/check-invariants.py, --only, <errata.id_underscored>]\n" +
		"checks:\n- {id: no-latest-tags, reason: r, scope: repo}\n"
	if err := os.WriteFile(filepath.Join(root, config.DefaultFile), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := execute(context.Background(), []string{"list", "--root", root}, &out, &errOut, config.Load); code != ExitOK {
		t.Fatalf("list: exit %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "python3 scripts/check-invariants.py --only no_latest_tags") || strings.Contains(out.String(), "<errata.") {
		t.Errorf("list printed\n%s\nwithout the expanded run", out.String())
	}
	out.Reset()
	if code := execute(context.Background(), []string{"list", "-f", "json", "--root", root}, &out, &errOut, config.Load); code != ExitOK {
		t.Fatalf("list -f json: exit %d, stderr %q", code, errOut.String())
	}
	var doc struct{ Checks []struct{ Run []string } }
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{"python3", "scripts/check-invariants.py", "--only", "no_latest_tags"}
	if len(doc.Checks) != 1 || !slices.Equal(doc.Checks[0].Run, want) {
		t.Errorf("list -f json printed %s, want run %q", out.String(), want)
	}
}
