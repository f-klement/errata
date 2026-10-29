package core_test

import (
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// The tests of package core_test read files through the extractors Build
// returns, since what they test, the source checks and the path's filters,
// is the same for each kind.

// run builds s and extracts from root.
func run(t *testing.T, s extract.Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, extract.Build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s extract.Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, extract.Build, s, root)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

func yamlSpec(path string) extract.Spec {
	return extract.Of(yaml.Kind, yaml.Spec{File: "f.yaml", Path: path})
}

// fromData is what every extractor runs on the bytes of its file.
type fromData interface {
	FromData(data []byte) ([]check.Value, error)
}

func iniSpec(file, section, key string) extract.Spec {
	return extract.Of(ini.Kind, ini.Spec{File: file, Section: section, Key: key})
}
