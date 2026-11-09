package udev

import (
	"errors"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// Two ways are taken as one only where they are the same, or alike up to
// a last GOTO they share, and taking them is charged to the read's work
// budget, so a read past it stops. Planted breaks: pair not charging;
// halves taking two ways of one length whatever came before their last
// GOTO.
func TestMergeIsCharged(t *testing.T) {
	fl := &flow{meter: &core.Meter{Budget: stepWork}}
	_, err := fl.merge([]way{{{0, true}}, {{0, false}}})
	if !errors.Is(err, core.ErrWork) {
		t.Errorf("merge past the budget: %v, want the work error", err)
	}
	fl = &flow{meter: &core.Meter{Budget: 1 << 20}}
	got, err := fl.merge([]way{{{0, true}}, {{1, false}}, {{0, false}}, {{1, false}}})
	if err != nil || len(got) != 2 || len(got[0]) != 0 || got[1][0] != (step{1, false}) {
		t.Errorf("merge: %v, %v", got, err)
	}
	got, err = fl.merge([]way{{{0, true}, {2, false}}, {{1, true}, {2, true}}})
	if err != nil || len(got) != 2 {
		t.Errorf("two ways that differ before their last GOTO taken as one: %v, %v", got, err)
	}
}
