// Package config reads errata.yaml into checks. It knows the file format and
// nothing about running a check.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/f-klement/errata/internal/check"
)

// DefaultFile is the config file errata looks for in the repository root.
const DefaultFile = "errata.yaml"

// DefaultTimeout applies to a check that sets no timeout of its own.
const DefaultTimeout = 30 * time.Second

type file struct {
	Checks []checkEntry `json:"checks"`
}

type checkEntry struct {
	ID      string   `json:"id"`
	Reason  string   `json:"reason"`
	Scope   string   `json:"scope"`
	Run     []string `json:"run"`
	Timeout string   `json:"timeout,omitempty"`
}

// Load reads and validates the config at path. It reports every invalid check
// at once, so a user fixes the file in one pass rather than one error a run.
func Load(path string) ([]check.Check, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse decodes raw config bytes. Decoding is strict: an unknown key is an
// error, because a misspelt key that is silently ignored turns a check off
// with no symptom.
func Parse(raw []byte) ([]check.Check, error) {
	var f file
	if err := yaml.UnmarshalStrict(raw, &f); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	var (
		checks []check.Check
		errs   []error
		seen   = map[string]bool{}
	)
	for i, e := range f.Checks {
		c := check.Check{
			ID:      e.ID,
			Reason:  e.Reason,
			Scope:   check.Scope(e.Scope),
			Run:     e.Run,
			Timeout: DefaultTimeout,
		}
		if e.Timeout != "" {
			d, err := time.ParseDuration(e.Timeout)
			if err != nil {
				errs = append(errs, fmt.Errorf("checks[%d] %q: timeout: %w", i, e.ID, err))
				continue
			}
			c.Timeout = d
		}
		if err := c.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("checks[%d]: %w", i, err))
			continue
		}
		if seen[c.ID] {
			errs = append(errs, fmt.Errorf("checks[%d]: duplicate id %q", i, c.ID))
			continue
		}
		seen[c.ID] = true
		checks = append(checks, c)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return checks, nil
}
