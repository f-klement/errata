// Package provider defines how errata talks to a live system. No provider is
// implemented yet; the engine uses Capabilities to decide which scopes a run
// can reach, so a live or applied check is skipped by name instead of
// passing silently.
package provider

import (
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

// Provider is one kind of live system. The render, read and predict methods
// arrive with the first implementation (Kubernetes), when their signatures
// can be shaped by a real caller.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Reconciler() ReconcilerModel
}

// Reaches reports whether caps can serve scope, and if not, the gap.
func Reaches(caps Capabilities, scope check.Scope) (ok bool, gap string) {
	switch scope {
	case check.ScopeRepo:
		return true, ""
	case check.ScopeLive:
		if !caps.Read {
			return false, "no provider can read a live system"
		}
	case check.ScopeApplied:
		if !caps.Read || !caps.Predict {
			return false, "no provider can read and predict a live system"
		}
	default:
		return false, "unknown scope " + string(scope)
	}
	return true, ""
}

// Union merges the capabilities of several providers.
func Union(ps []Provider) Capabilities {
	var c Capabilities
	for _, p := range ps {
		pc := p.Capabilities()
		c.Render = c.Render || pc.Render
		c.Read = c.Read || pc.Read
		c.Predict = c.Predict || pc.Predict
	}
	return c
}
