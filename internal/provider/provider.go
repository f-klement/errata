// Package provider defines how errata talks to a live system. A provider is
// a plugin that speaks protocol 2 (ADR 0023), and a live or applied check
// names the one it needs. The engine asks Reach whether that provider is
// installed and has the operation the check's scope needs, so a check the
// run cannot reach is skipped by name instead of passing silently.
package provider

import (
	"strings"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// Capabilities lists the operations a provider implements. See GLOSSARY.md
// for render, read and predict.
type Capabilities struct {
	Render  bool
	Read    bool
	Predict bool
}

// ReconcilerModel holds the four facts that turn drift into a verdict.
type ReconcilerModel struct {
	Exists    bool
	Interval  time.Duration
	Prunes    bool
	SelfHeals bool
}

// Provider is one kind of live system. Render, read and predict are methods
// of the plugin host's own type (plugin.Provider), which sends a plugin
// those requests; they join this interface when a caller outside the host
// shapes their signatures.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Reconciler() ReconcilerModel
}

// Explained is a provider that says itself why it cannot run or what it
// lacks, as a plugin the lock pins or a check names does (plugin.Provider,
// which asserts that it is one). Reach takes its word over Capabilities,
// so the plugin host alone names plugins and the commands that install
// them.
type Explained interface {
	// Fault is why errata found the provider and cannot run it, or nil.
	Fault() error
	// Lacks is why the provider cannot do op, which the gap gives after
	// the operation, or "" when it can.
	Lacks(op string) string
}

// operations names each operation the scope of a check needs: none for
// repo, read for live, and predict for applied, which asks what an apply
// would produce and never applies.
var operations = map[check.Scope]string{
	check.ScopeRepo:    "",
	check.ScopeLive:    "read",
	check.ScopeApplied: "predict",
}

// has reports whether caps holds op, one of operations' values.
func (caps Capabilities) has(op string) bool {
	switch op {
	case "read":
		return caps.Read
	case "predict":
		return caps.Predict
	}
	return true
}

// names lists the operations caps holds, or says it holds none.
func (caps Capabilities) names() string {
	var ops []string
	for _, op := range []struct {
		name string
		has  bool
	}{{"render", caps.Render}, {"read", caps.Read}, {"predict", caps.Predict}} {
		if op.has {
			ops = append(ops, op.name)
		}
	}
	if len(ops) == 0 {
		return "none of render, read and predict"
	}
	return strings.Join(ops, ", ")
}

// Reach says whether a check of scope, which names the provider name
// (check.Check.Provider), can run with ps, the providers installed
// (ADR 0023). It returns the gap when it cannot: the operation the scope
// needs and the plugin that would provide it, which the check is skipped
// with. fault is set instead when the check names a provider errata found
// and cannot run, such as a plugin whose build differs from its pin, whose
// Fault says so (Explained): the check is then unknown, since the run did
// not lack the system, and errata could not tell.
func Reach(ps []Provider, name string, scope check.Scope) (gap string, fault error) {
	op, known := operations[scope]
	if !known {
		return "unknown scope " + string(scope), nil
	}
	if op == "" {
		return "", nil
	}
	for _, p := range ps {
		if p.Name() == name {
			return reach(p, op)
		}
	}
	return gapOf(op, "no provider named "+name+" was loaded"), nil
}

// reach is Reach for p, the provider a check names, and op, the operation
// its scope needs: an Explained provider's own word, else what its
// Capabilities hold.
func reach(p Provider, op string) (string, error) {
	if e, ok := p.(Explained); ok {
		if f := e.Fault(); f != nil {
			return "", f
		}
		if why := e.Lacks(op); why != "" {
			return gapOf(op, why), nil
		}
		return "", nil
	}
	if caps := p.Capabilities(); !caps.has(op) {
		return gapOf(op, "provider "+p.Name()+" provides "+caps.names()), nil
	}
	return "", nil
}

// gap is the gap of a check whose scope needs op, which no provider gives
// for why.
func gapOf(op, why string) string {
	return "no installed plugin provides " + op + ": " + why
}
