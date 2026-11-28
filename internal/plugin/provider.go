package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/plugin/wire"
	"github.com/f-klement/errata/internal/provider"
)

// Provider is a plugin a live or applied check names with provider:, which
// speaks protocol 2 and provides render, read or predict (ADR 0023). It is
// a provider.Provider, and its Render, Read and Predict send the plugin
// those requests. missing, when set, is why the plugin is not there to
// run: the lock does not pin it, or its build is not installed.
type Provider struct {
	p       *Plugin
	missing string
}

// A Provider says itself why it cannot run or lacks an operation, so the
// gap of a check that names it is the plugin host's (provider.Explained).
var _ provider.Explained = (*Provider)(nil)

// The protocol's bounds on a render, read or predict request
// (docs/plugin-protocol.md, Protocol 2). providerTimeout is a variable so
// a test can shorten it.
var providerTimeout = wire.ProviderTimeout

// maxPath is the longest path of a file a render, read or predict reply
// holds.
const maxPath = 4096

// The parts of replyStdout, from what checkFiles admits.
const (
	// byteCost is the most a byte of a file's path or data costs in a
	// reply as Go's encoding/json writes it: a path's <, > or & is
	// written \u003c and the like, 6 bytes for 1, and base64 writes a
	// file's data in 4 bytes for every 3, and 4 for 1 or 2.
	byteCost = 6
	// fileCost is what a file costs besides its path's and its data's
	// bytes: its members and the comma after it. No data is "", which
	// wire.File writes for nil data too; null is refused (wire.Decode).
	fileCost = int64(len(`{"path":"","data":""},`))
	// replyCost is what the reply costs besides its files: its member, no
	// comma after its last file, and a newline after it, as
	// json.Encoder writes.
	replyCost = int64(len(`{"files":[]}`)) - 1 + 1
)

// replyStdout is the bound on stdout of a render, read or predict request
// whose max_bytes is maxBytes: the longest reply whose files checkFiles
// admits, wire.MaxFiles of them, every byte of their paths and data
// costing byteCost, as Go's encoding/json writes it, and a newline. So a
// reply the files bound admits is never cut at stdout. At the default
// max_bytes that is about 194 MiB, which a request's run holds in memory,
// so a caller that sends many read or predict requests at once bounds
// how many run together.
func replyStdout(maxBytes int64) int64 {
	return byteCost*maxBytes + fileCost*wire.MaxFiles + replyCost
}

// Providers are the providers names name, each the name of a plugin a
// check names with provider:, in the order of names, each once, "" left
// out. A plugin the lock does not pin, or whose build is not installed, is
// missing: Lacks says so with the command that installs it (installHint),
// and a check that names it is skipped. One errata cannot run for another
// reason, such as a build that differs from its pin, has a Fault that
// says why, and a check that names it is unknown.
func (s *Set) Providers(ctx context.Context, names []string) []provider.Provider {
	var out []provider.Provider
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || name == "" {
			continue
		}
		seen[name] = true
		out = append(out, s.provider(ctx, name))
	}
	return out
}

// provider is the provider name names.
func (s *Set) provider(ctx context.Context, name string) *Provider {
	if !s.Has(name) {
		missing := notPinned(name)
		return &Provider{p: &Plugin{pin: Pin{Name: name}, exe: exe{label: "errata-ext-" + name}, unusable: errors.New(missing)}, missing: missing}
	}
	p := s.plugin(ctx, name)
	if errors.Is(p.unusable, errNotInstalled) {
		return &Provider{p: p, missing: p.exe.label + " is not installed" + p.exe.install}
	}
	return &Provider{p: p}
}

// plugin is the plugin the lock pins under name, which Has reports,
// described on the first call.
func (s *Set) plugin(ctx context.Context, name string) *Plugin {
	p, ok := s.plugins[name]
	if !ok {
		pin, _ := s.lock.Pin(name)
		p = s.load(ctx, pin)
		s.plugins[name] = p
	}
	return p
}

// Name is the plugin's name, the one a check's provider: gives.
func (v *Provider) Name() string { return v.p.pin.Name }

// Label is the plugin as a message names it, such as errata-ext-kubernetes
// 0.1.0.
func (v *Provider) Label() string { return v.p.exe.label }

// Capabilities are the provider operations the plugin's describe reply
// names, and none for a plugin errata cannot run or that is missing.
func (v *Provider) Capabilities() provider.Capabilities {
	if v.p.unusable != nil {
		return provider.Capabilities{}
	}
	d := v.p.desc
	return provider.Capabilities{Render: d.Provides(wire.OpRender), Read: d.Provides(wire.OpRead), Predict: d.Provides(wire.OpPredict)}
}

// Reconciler is the zero model: protocol 2 carries no reconciler model.
func (v *Provider) Reconciler() provider.ReconcilerModel { return provider.ReconcilerModel{} }

// Fault is why errata cannot run the plugin, or nil for one it can run or
// that is missing, which Lacks says.
func (v *Provider) Fault() error {
	if v.missing != "" {
		return nil
	}
	return v.p.unusable
}

// Lacks is why the plugin cannot do op, which a gap gives after the
// operation, or "" when it provides op: it is missing, it is an extractor
// plugin, or it provides other operations.
func (v *Provider) Lacks(op string) string {
	switch {
	case v.missing != "":
		return v.missing
	case v.p.unusable != nil:
		return v.p.unusable.Error()
	case v.p.desc.Provides(op):
		return ""
	case v.p.desc.Protocol != wire.Protocol2:
		return v.p.exe.label + " is an extractor plugin, which speaks protocol 1 and provides none of render, read and predict"
	}
	return v.p.exe.label + " provides " + strings.Join(v.p.desc.Operations, ", ")
}

// Render sends the plugin a render request: spec, a JSON object of the
// plugin's members, nil for an empty one; files, the sources; and
// maxBytes, the bound on the reply's files, 0 for the default of 32 MiB.
// It returns the files the plugin rendered.
func (v *Provider) Render(ctx context.Context, spec json.RawMessage, files []wire.File, maxBytes int64) ([]wire.File, error) {
	out, _, err := v.call(ctx, wire.OpRender, spec, files, maxBytes)
	return out, err
}

// Read sends the plugin a read request, with spec and maxBytes as Render
// takes them. It returns the files the plugin read, or the gap it gave:
// why the live system cannot be reached at all.
func (v *Provider) Read(ctx context.Context, spec json.RawMessage, maxBytes int64) (files []wire.File, gap string, err error) {
	return v.call(ctx, wire.OpRead, spec, nil, maxBytes)
}

// Predict sends the plugin a predict request, with spec, files, the
// objects an apply would send, and maxBytes as Render takes them. It
// returns the files the plugin predicted, or the gap it gave.
func (v *Provider) Predict(ctx context.Context, spec json.RawMessage, files []wire.File, maxBytes int64) ([]wire.File, string, error) {
	return v.call(ctx, wire.OpPredict, spec, files, maxBytes)
}

// call sends the plugin op's request and reads its reply. Read and predict
// run with the variables the plugin's env names that the lock records for
// it (passes); render runs with a plugin's usual environment.
func (v *Provider) call(ctx context.Context, op string, spec json.RawMessage, files []wire.File, maxBytes int64) ([]wire.File, string, error) {
	if err := v.p.unusable; err != nil {
		return nil, "", err
	}
	if !v.p.desc.Provides(op) {
		return nil, "", fmt.Errorf("plugin %s does not provide %s", v.p.exe.label, op)
	}
	req := requestOf(v.p.desc.Protocol, op, spec, files, maxBytes)
	lim := limits{timeout: providerTimeout, stdout: replyStdout(req.MaxBytes)}
	if op != wire.OpRender {
		lim.env = v.p.passes()
	}
	out, stderr, err := v.p.exe.run(ctx, req, lim)
	if err != nil {
		return nil, "", err
	}
	return v.reply(op, out, stderr, req.MaxBytes)
}

// passes is the variables p's read and predict get: each its describe
// names that the lock records for it. A plugin whose describe names one
// the lock lacks does not run (recorded), so this is every name it
// declares; a name the lock records and the plugin no longer names is
// not passed.
func (p *Plugin) passes() []string {
	var out []string
	for _, n := range p.desc.Env {
		if slices.Contains(p.env, n) {
			out = append(out, n)
		}
	}
	return out
}

// requestOf is op's request, with an empty spec for nil, no file for nil
// and the default bound for a maxBytes below 1.
func requestOf(protocol int, op string, spec json.RawMessage, files []wire.File, maxBytes int64) wire.ProviderRequest {
	if spec == nil {
		spec = json.RawMessage(`{}`)
	}
	if files == nil {
		files = []wire.File{}
	}
	if maxBytes < 1 {
		maxBytes = check.DefaultMaxBytes
	}
	return wire.ProviderRequest{Protocol: protocol, Op: op, Spec: spec, Files: files, MaxBytes: maxBytes}
}

// reply reads the reply to op, out, and refuses one errata does not read,
// a gap in reply to render, and files that break the protocol's rules
// (checkFiles). The plugin's error and gap are quoted, since the plugin
// wrote them.
func (v *Provider) reply(op string, out, stderr []byte, maxBytes int64) ([]wire.File, string, error) {
	var r wire.ProviderReply
	if err := wire.Decode(out, &r); err != nil {
		return nil, "", v.p.refuse(op, err, stderr)
	}
	switch {
	case r.Error != nil:
		return nil, "", fmt.Errorf("plugin %s could not %s: %s", v.p.exe.label, op, strconv.Quote(*r.Error))
	case r.Gap != nil && op == wire.OpRender:
		return nil, "", v.p.refuse(op, errors.New("a render reply holds no gap"), stderr)
	case r.Gap != nil:
		return nil, "plugin " + v.p.exe.label + " says " + strconv.Quote(*r.Gap), nil
	}
	if err := checkFiles(r.Files, maxBytes); err != nil {
		return nil, "", v.p.refuse(op, err, stderr)
	}
	return r.Files, "", nil
}

// checkFiles refuses files that are more than wire.MaxFiles, whose paths
// and bytes together pass maxBytes, or one of whose paths is not a
// relative path of names joined by / (checkPath) or appears twice.
func checkFiles(files []wire.File, maxBytes int64) error {
	if len(files) > wire.MaxFiles {
		return fmt.Errorf("the reply holds %d files, more than %d", len(files), wire.MaxFiles)
	}
	var total int64
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if err := checkPath(f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("the path %s appears twice", strconv.Quote(f.Path))
		}
		seen[f.Path] = true
		total += int64(len(f.Path) + len(f.Data))
	}
	if total > maxBytes {
		return fmt.Errorf("the files' paths and bytes come to %d bytes, more than max_bytes, %d", total, maxBytes)
	}
	return nil
}

// checkPath refuses a path longer than maxPath, one with a \ or a control
// character, and one that is not names joined by /, each neither empty,
// . nor ...
func checkPath(path string) error {
	if len(path) > maxPath {
		return fmt.Errorf("a path is %d bytes, longer than %d", len(path), maxPath)
	}
	if strings.ContainsFunc(path, func(r rune) bool { return r == '\\' || unicode.IsControl(r) }) {
		return fmt.Errorf("the path %s holds a \\ or a control character", strconv.Quote(path))
	}
	for name := range strings.SplitSeq(path, "/") {
		if name == "" || name == "." || name == ".." {
			return fmt.Errorf("the path %s is not names joined by /, with no name empty, . or .. alone", strconv.Quote(path))
		}
	}
	return nil
}
