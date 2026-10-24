package shell

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

func entryExtractor(t *testing.T, s Spec) core.EntryExtractor {
	t.Helper()
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	return e.(core.EntryExtractor)
}

// The extractor returns at most its value limit, and refuses past it, as
// every extractor does; the walker keeps no more than that.
func TestShellValueLimit(t *testing.T) {
	x := entryExtractor(t, spec("X"))
	x.ValueLimit = 3
	if got, err := x.FromData([]byte(strings.Repeat("X=1\n", 3))); err != nil || len(got) != 3 {
		t.Fatalf("%d values, %v", len(got), err)
	}
	_, err := x.FromData([]byte(strings.Repeat("X=1\n", 4)))
	extracttest.WantError(t, err, "deploy.sh: the extractor selects more than 100000 values, the limit")
}

// Nested defaults return each default whole, so their values can hold more
// than the file; past max_bytes in all they are refused.
func TestShellValueBytesLimit(t *testing.T) {
	body := ": " + strings.Repeat("${X:-", 8) + "a" + strings.Repeat("}", 8) + "\n"
	x := entryExtractor(t, Spec{File: "deploy.sh", Name: "X", MaxBytes: int64(len(body))})
	_, err := x.FromData([]byte(body))
	extracttest.WantError(t, err, "deploy.sh: the values the extractor selects hold more than 52 bytes in all, the extractor's max_bytes")
}

// Each read has a budget of work, which every node walked and every byte of
// a value kept is charged: below its cost a read is refused, at it the read
// returns what it reads with no meter.
func TestShellWorkMeterRefusesAtEveryBudget(t *testing.T) {
	x := entryExtractor(t, spec("X"))
	coretest.RefusesAtEveryBudget(t, x, "X=1\nexport X=\"a b\"\n: ${X:-${X:-c}}\ncat <<E\n${X:=d}\nE\n")
}

// Ordinary scripts spend under a quarter of the budget a byte.
func TestShellWorkMeterLeavesRoomForOrdinaryFiles(t *testing.T) {
	for _, body := range []string{
		"#!/bin/sh\nset -eu\nPORT=${PORT:-8080}\nexport HOST=localhost\nexec server --port \"$PORT\" --host \"$HOST\"\n",
		"#!/usr/bin/env bash\nreadonly VERSION=1.2.3\nmain() {\n  local X=1\n  for f in *.txt; do\n    echo \"$f ${X}\"\n  done\n}\nmain \"$@\"\n",
	} {
		coretest.LeavesRoom(t, entryExtractor(t, Spec{File: "a.sh", Name: "X"}), body)
	}
}
