package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/plugin/wire"
	"github.com/f-klement/errata/internal/provider"
)

// kubeProvider is the provider the fake gives under the name kube, pinned
// and vendored in a new repository.
func kubeProvider(t *testing.T) *Provider {
	t.Helper()
	root := newRepo(t, nil, "kube")
	ps := openSet(t, root).Providers(context.Background(), []string{"kube"})
	if len(ps) != 1 {
		t.Fatalf("Providers = %v, want kube", ps)
	}
	return ps[0].(*Provider)
}

// fileText is the text of the file at path among files, and whether one is.
func fileText(files []wire.File, path string) (string, bool) {
	for _, f := range files {
		if f.Path == path {
			return string(f.Data), true
		}
	}
	return "", false
}

// A protocol 2 plugin's capabilities are the operations its describe
// names, and each request is sent in the version it chose, with the bound
// the caller gave or 32 MiB. Planted breaks: Capabilities reporting read
// for render; the request written in protocol 1.
func TestAProviderPluginSpeaksProtocol2(t *testing.T) {
	v := kubeProvider(t)
	if got := v.Capabilities(); got != (provider.Capabilities{Render: true, Read: true, Predict: true}) {
		t.Fatalf("Capabilities = %+v", got)
	}
	if v.Name() != "kube" || v.Label() != "errata-ext-kube 0.1.0" || v.Fault() != nil {
		t.Fatalf("Name %q, Label %q, Fault %v", v.Name(), v.Label(), v.Fault())
	}
	ctx := context.Background()
	files, err := v.Render(ctx, nil, []wire.File{{Path: "chart/a.yaml", Data: []byte("a: 1")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if op, _ := fileText(files, "op"); op != "render 2 33554432" {
		t.Errorf("render's op file is %q", op)
	}
	if in, _ := fileText(files, "in/chart/a.yaml"); in != "a: 1" {
		t.Errorf("render's input file is %q", in)
	}
	files, gap, err := v.Read(ctx, json.RawMessage(`{}`), 1000)
	if err != nil || gap != "" {
		t.Fatal(err, gap)
	}
	if op, _ := fileText(files, "op"); op != "read 2 1000" {
		t.Errorf("read's op file is %q", op)
	}
	files, _, err = v.Predict(ctx, nil, nil, 0)
	if op, _ := fileText(files, "op"); err != nil || op != "predict 2 33554432" {
		t.Errorf("predict's op file is %q, %v", op, err)
	}
}

// Read and predict get the variables the plugin's env names that errata
// has, and render gets none of them; no other variable reaches any of
// them. Planted breaks: lim.env set for render too; passed adding every
// variable of errata's environment.
func TestOnlyReadAndPredictGetTheVariablesTheirPluginNames(t *testing.T) {
	t.Setenv("FAKE_KUBE", "/abs/kubeconfig")
	t.Setenv("CI_JOB_TOKEN", "secret")
	unset(t, "FAKE_ABSENT")
	v := kubeProvider(t)
	ctx := context.Background()
	render, _ := v.Render(ctx, nil, nil, 0)
	read, _, _ := v.Read(ctx, nil, 0)
	predict, _, _ := v.Predict(ctx, nil, nil, 0)
	for op, files := range map[string][]wire.File{"render": render, "read": read, "predict": predict} {
		env, ok := fileText(files, "env")
		if !ok {
			t.Fatalf("%s returned no env file: %v", op, files)
		}
		vars := strings.Split(env, "\n")
		if strings.Contains(env, "CI_JOB_TOKEN") || strings.Contains(env, "FAKE_ABSENT") {
			t.Errorf("%s saw %q", op, vars)
		}
		if has := slices.Contains(vars, "FAKE_KUBE=/abs/kubeconfig"); has != (op != "render") {
			t.Errorf("%s: FAKE_KUBE passed is %v, in %q", op, has, vars)
		}
	}
}

// A reply's error makes the request fail quoting it, a gap is returned
// for read and refused for render, and files that break the protocol's
// rules are refused. Planted breaks: the gap returned for render; the
// path check dropped; the duplicate check dropped; the bound compared to
// the bytes alone, without the paths.
func TestAProviderReplyIsChecked(t *testing.T) {
	v := kubeProvider(t)
	ctx := context.Background()
	spec := func(s string) json.RawMessage { return json.RawMessage(s) }
	if _, err := v.Render(ctx, spec(`{"mode": "error"}`), nil, 0); err == nil || err.Error() != `plugin errata-ext-kube 0.1.0 could not render: "no cluster\nhere"` {
		t.Errorf("error: %v", err)
	}
	if _, gap, err := v.Read(ctx, spec(`{"mode": "gap"}`), 0); err != nil || gap != `plugin errata-ext-kube 0.1.0 says "no cluster\nhere"` {
		t.Errorf("gap: %q, %v", gap, err)
	}
	if _, err := v.Render(ctx, spec(`{"mode": "gap"}`), nil, 0); err == nil || !strings.Contains(err.Error(), "a render reply holds no gap") {
		t.Errorf("render's gap: %v", err)
	}
	for mode, want := range map[string]string{
		`{"mode": "badpath"}`:           `the path "a/../b" is not names joined by /`,
		`{"mode": "dup"}`:               `the path "a" appears twice`,
		`{"mode": "big", "size": 1000}`: "the files' paths and bytes come to 1001 bytes, more than max_bytes, 1000",
	} {
		if _, _, err := v.Predict(ctx, spec(mode), nil, 1000); err == nil || !strings.Contains(err.Error(), "replied to predict with a reply errata refuses: "+want) {
			t.Errorf("%s: %v", mode, err)
		}
	}
	if _, _, err := v.Predict(ctx, spec(`{"mode": "big", "size": 999}`), nil, 1000); err != nil {
		t.Errorf("a reply at the bound: %v", err)
	}
	// A reply of 1 MiB, base64 in JSON, is past 1 MiB of stdout and within
	// the bound.
	if _, _, err := v.Predict(ctx, spec(`{"mode": "big", "size": 1048000}`), nil, 1<<20); err != nil {
		t.Errorf("a reply of 1 MiB: %v", err)
	}
}

// The longest reply whose files errata admits, every path byte one JSON
// writes in 6 and the most files, is read whole with the newline after
// it, and one byte more is past the stdout bound. A plugin's files of nil
// data, which wire.File writes as "", give the same reply. 970000 bytes
// leave the fake's last path 2854 bytes (testdata/fake, longest). Planted
// breaks: the bound of twice max_bytes and 1 MiB this replaced; fileCost
// one byte short; wire.File writing nil data as null.
func TestTheStdoutBoundIsTheLongestReplyErrataAdmits(t *testing.T) {
	v := kubeProvider(t)
	const maxBytes = 970000
	for _, spec := range []string{`{"mode": "longest", "size": 0, "null": true}`, `{"mode": "longest", "size": 0}`} {
		files, _, err := v.Read(context.Background(), json.RawMessage(spec), maxBytes)
		if err != nil || len(files) != wire.MaxFiles {
			t.Fatalf("the longest reply %s: %d files, %v", spec, len(files), err)
		}
	}
	want := fmt.Sprintf("wrote more than %d bytes to stdout", replyStdout(maxBytes))
	if _, _, err := v.Read(context.Background(), json.RawMessage(`{"mode": "longest", "size": 1, "null": true}`), maxBytes); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("one byte past the bound: %v, want %s", err, want)
	}
}

// Each bound of a reply lets its limit through and refuses one more:
// the files, the length of a path, and a max_bytes of 1, which is not the
// default. Planted breaks, each from the mutation run: > made >= in
// checkFiles and checkPath, and < 1 made <= 1 in requestOf.
func TestAProviderReplysBoundsAreExact(t *testing.T) {
	if r := requestOf(2, wire.OpRead, nil, nil, 1); r.MaxBytes != 1 {
		t.Errorf("max_bytes 1 became %d", r.MaxBytes)
	}
	files := make([]wire.File, wire.MaxFiles+1)
	for i := range files {
		files[i].Path = strconv.Itoa(i)
	}
	if err := checkFiles(files[:wire.MaxFiles], 1<<30); err != nil {
		t.Errorf("%d files: %v", wire.MaxFiles, err)
	}
	if err := checkFiles(files, 1<<30); err == nil {
		t.Errorf("%d files passed", len(files))
	}
	if err := checkPath(strings.Repeat("a", maxPath)); err != nil {
		t.Errorf("a path of %d bytes: %v", maxPath, err)
	}
	if err := checkPath(strings.Repeat("a", maxPath+1)); err == nil {
		t.Errorf("a path of %d bytes passed", maxPath+1)
	}
}

// A request past its timeout is stopped and named. Planted break: the
// provider's request run with the extract timeout.
func TestAProviderRequestTimesOut(t *testing.T) {
	defer func(d time.Duration) { providerTimeout = d }(providerTimeout)
	providerTimeout = 200 * time.Millisecond
	v := kubeProvider(t)
	if _, _, err := v.Read(context.Background(), json.RawMessage(`{"mode": "sleep"}`), 0); err == nil || !strings.Contains(err.Error(), "did not finish within 200ms") {
		t.Fatalf("got %v", err)
	}
}

// A provider that lacks an operation does not run it, and a plugin that
// does not provide extract reads no entry of its kind. Planted breaks: the
// Provides check in call dropped; noKind left at unusable.
func TestAProviderOfOneOperation(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "x\n"}, "renderer")
	v := openSet(t, root).Providers(context.Background(), []string{"renderer"})[0].(*Provider)
	if got := v.Capabilities(); got != (provider.Capabilities{Render: true}) {
		t.Fatalf("Capabilities = %+v", got)
	}
	if _, _, err := v.Read(context.Background(), nil, 0); err == nil || err.Error() != "plugin errata-ext-renderer 0.1.0 does not provide read" {
		t.Errorf("read: %v", err)
	}
	if _, err := extractOne(t, root, "renderer", `{"file": "a"}`); err == nil || err.Error() != "plugin errata-ext-renderer 0.1.0 provides render, not extract, so it reads no entry of an extractor" {
		t.Errorf("extract: %v", err)
	}
}

// A plugin that chose protocol 2 and provides extract reads its entries as
// a protocol 1 plugin does, in requests written in protocol 2; the fake
// fails one written in another. Planted break: build and extract sent in
// wire.Protocol.
func TestAnExtractorThatChoseProtocol2(t *testing.T) {
	root := newRepo(t, map[string]string{"a": "port 8080\n"}, "both")
	vals, err := extractOne(t, root, "both", `{"file": "a", "word": "port"}`)
	if err != nil || len(vals) != 1 || vals[0].Text != "8080" {
		t.Fatalf("got %v, %v", vals, err)
	}
}

// Providers keeps a plugin the lock does not pin and one that is not
// installed as missing, and one errata cannot run for another reason with
// its fault, each with no capability, once each and in order. Planted
// breaks: a missing plugin given a fault, which made its checks unknown;
// every unusable plugin taken as missing, which made a build that differs
// from its pin a skip.
func TestProvidersKeepWhatIsMissingApartFromAFault(t *testing.T) {
	root := newRepo(t, nil, "kube")
	zero := strings.Repeat("0", 64)
	copyFake(t, BuildPath(filepath.Join(root, VendorDir), "kube", "0.1.0", Platform(), zero))
	writeFile(t, root, LockFile, pinLine("kube", zero)+"\n"+pinLine("gone", strings.Repeat("a", 64))+"\n")
	ps := openSet(t, root).Providers(context.Background(), []string{"absent", "", "gone", "kube", "kube"})
	var names []string
	for _, p := range ps {
		names = append(names, p.Name())
		if p.Capabilities() != (provider.Capabilities{}) {
			t.Errorf("%s: Capabilities %+v", p.Name(), p.Capabilities())
		}
	}
	if strings.Join(names, " ") != "absent gone kube" {
		t.Fatalf("Providers = %v", names)
	}
	absent, gone, kube := ps[0].(*Provider), ps[1].(*Provider), ps[2].(*Provider)
	if absent.Fault() != nil || gone.Fault() != nil {
		t.Fatalf("a missing plugin has a fault: %v, %v", absent.Fault(), gone.Fault())
	}
	if kube.Fault() == nil || !strings.Contains(kube.Fault().Error(), "so errata does not run it") || !strings.Contains(kube.Lacks(wire.OpRead), "so errata does not run it") {
		t.Fatalf("Fault %v", kube.Fault())
	}
	for _, v := range []*Provider{absent, gone, kube} {
		if _, _, err := v.Read(context.Background(), nil, 0); err == nil {
			t.Errorf("read of %s ran", v.Name())
		}
	}
}

// The gap of a check whose provider is missing gives the command that
// installs it where errata looks: in a repository that vendors, the
// install that vendors; for a pin from a file, the install of that file;
// for a plugin the lock does not pin, the install of its name. Planted
// breaks: the gap written by package provider again, which told a
// vendored repository to install into the cache; the pin's install hint
// left off.
func TestAMissingProvidersGapIsTheHostsInstallHint(t *testing.T) {
	root := newRepo(t, nil)
	writeFile(t, root, LockFile, pinLine("gone", strings.Repeat("a", 64))+"\n"+pinLine("filed", strings.Repeat("b", 64))+" source=file\n")
	mkdir(t, filepath.Join(root, VendorDir))
	ps := openSet(t, root).Providers(context.Background(), []string{"gone", "filed", "absent"})
	for name, want := range map[string]string{
		"gone":   "no installed plugin provides read: errata-ext-gone 0.1.0 is not installed; errata looks in .errata/plugins alone while the repository has it: vendor what errata.lock pins with errata plugin install --vendor",
		"filed":  "no installed plugin provides read: errata-ext-filed 0.1.0 is not installed; its entry in errata.lock came from a file, which errata cannot fetch, and errata looks in .errata/plugins alone while the repository has it: install that file with errata plugin install --file, which vendors it",
		"absent": "no installed plugin provides read: errata-ext-absent is not installed; install it with errata plugin install absent",
	} {
		if gap, fault := provider.Reach(ps, name, "live"); gap != want || fault != nil {
			t.Errorf("%s: gap %q, %v\nwant %q", name, gap, fault, want)
		}
	}
}

// A plugin that provides other operations, or speaks protocol 1, says so
// in the gap of a check that names it. Planted break: the protocol 1 case
// dropped, which listed no operation.
func TestAProviderLackingTheOperationSaysWhatItIs(t *testing.T) {
	root := newRepo(t, nil, "renderer", "fake")
	ps := openSet(t, root).Providers(context.Background(), []string{"renderer", "fake"})
	for name, want := range map[string]string{
		"renderer": "no installed plugin provides predict: errata-ext-renderer 0.1.0 provides render",
		"fake":     "no installed plugin provides predict: errata-ext-fake 0.1.0 is an extractor plugin, which speaks protocol 1 and provides none of render, read and predict",
	} {
		if gap, fault := provider.Reach(ps, name, "applied"); gap != want || fault != nil {
			t.Errorf("%s: gap %q, %v", name, gap, fault)
		}
	}
}

// A describe reply that names an operation the protocol lacks is refused.
// Planted break: checkOperations not called.
func TestADescribeOfAnUnknownOperationIsRefused(t *testing.T) {
	root := newRepo(t, nil, "badops")
	ps := openSet(t, root).Providers(context.Background(), []string{"badops"})
	if len(ps) != 1 || ps[0].(*Provider).Fault() == nil || !strings.Contains(ps[0].(*Provider).Fault().Error(), `operations names "apply"`) {
		t.Fatalf("got %v", ps)
	}
}

// passed adds each name lookup finds, and none it does not. Planted
// break: a name added with an empty value when lookup misses it.
func TestPassedAddsOnlyWhatErrataHas(t *testing.T) {
	lookup := func(n string) (string, bool) { return "v", n == "A" }
	if got := passed([]string{"A", "B"}, lookup); !slices.Equal(got, []string{"A=v"}) {
		t.Fatalf("passed = %q", got)
	}
}

// A plugin whose describe names a variable the lock does not record for it
// does not run, and the fault names the variable and the command that
// records it; a variable the lock records that the plugin does not name is
// not passed. Planted breaks: recorded not called; passes returning the
// lock's names rather than those both name.
func TestTheLockRecordsEveryVariableAProviderGets(t *testing.T) {
	root := newRepo(t, nil, "kube")
	writeFile(t, root, LockFile, "kube 0.1.0 "+Platform()+" sha256:"+sumOf(t, fakeBin)+" env=FAKE_KUBE\n")
	ps := openSet(t, root).Providers(context.Background(), []string{"kube"})
	want := "plugin errata-ext-kube 0.1.0 names FAKE_ABSENT for its read and predict requests, and errata.lock does not record it for the plugin: errata plugin install --vendor records the variables its build names"
	if f := ps[0].(*Provider).Fault(); f == nil || !strings.HasPrefix(f.Error(), want) {
		t.Fatalf("Fault = %v, want %s", f, want)
	}
	t.Setenv("CI_JOB_TOKEN", "secret")
	t.Setenv("FAKE_KUBE", "/k")
	writeFile(t, root, LockFile, "kube 0.1.0 "+Platform()+" sha256:"+sumOf(t, fakeBin)+" env=FAKE_KUBE,FAKE_ABSENT,CI_JOB_TOKEN\n")
	v := openSet(t, root).Providers(context.Background(), []string{"kube"})[0].(*Provider)
	files, _, err := v.Read(context.Background(), nil, 0)
	env, _ := fileText(files, "env")
	if err != nil || strings.Contains(env, "CI_JOB_TOKEN") || !strings.Contains(env, "FAKE_KUBE=/k") {
		t.Fatalf("read's environment %q, %v", env, err)
	}
}

// A run reads the env the lock records for its own platform's build, so
// a plugin whose names differ by OS runs on each: another platform's
// line naming other variables neither refuses it nor adds to what it
// gets. Planted break: start taking another platform's entry.
func TestARunReadsItsOwnPlatformsEnv(t *testing.T) {
	other := "windows/arm64"
	if Platform() == other {
		other = "linux/amd64"
	}
	root := newRepo(t, nil, "kube")
	line := func(platform, env string) string {
		return "kube 0.1.0 " + platform + " sha256:" + sumOf(t, fakeBin) + " env=" + env + "\n"
	}
	writeFile(t, root, LockFile, line(Platform(), "FAKE_KUBE,FAKE_ABSENT")+line(other, "OTHER_ONLY"))
	if f := openSet(t, root).Providers(context.Background(), []string{"kube"})[0].(*Provider).Fault(); f != nil {
		t.Fatalf("another platform's env refused the plugin here: %v", f)
	}
	writeFile(t, root, LockFile, line(Platform(), "FAKE_KUBE")+line(other, "FAKE_KUBE,FAKE_ABSENT"))
	if f := openSet(t, root).Providers(context.Background(), []string{"kube"})[0].(*Provider).Fault(); f == nil || !strings.Contains(f.Error(), "names FAKE_ABSENT") {
		t.Fatalf("another platform's env passed for this one: %v", f)
	}
}

// The command that records a build's variables is an install from where
// the lock says the build came from. Planted break: the file case left
// out, which told a pin from a file to run an install that cannot fetch it.
func TestAcceptHint(t *testing.T) {
	for _, c := range []struct {
		pin      Pin
		vendored bool
		want     string
	}{
		{Pin{Name: "k", Source: SourceRelease}, false, "errata plugin install"},
		{Pin{Name: "k", Source: SourceURL}, true, "errata plugin install --vendor"},
		{Pin{Name: "k", Source: SourceFile}, true, "errata plugin install --file <path to errata-ext-k>"},
	} {
		if got := acceptHint(c.pin, c.vendored); got != c.want {
			t.Errorf("acceptHint(%+v, %v) = %q, want %q", c.pin, c.vendored, got, c.want)
		}
	}
}
