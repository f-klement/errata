package check

import (
	"strings"
	"testing"
)

// A flat value counts one node and the bytes of its text; a value handed
// over whole counts the nodes and bytes of its Data, which its Text, Data's
// Show, does not add to, since the rule holds that Text apart, to MaxShown.
// Planted breaks: a whole value counted by its text; a flat value counted
// as no node.
func TestValueSize(t *testing.T) {
	if n, b := (Value{Text: "abc"}).Size(); n != 1 || b != 3 {
		t.Errorf("a flat value: %d nodes, %d bytes; want 1 and 3", n, b)
	}
	whole := &Data{Kind: DataMap, Keys: []string{"k"}, Items: []*Data{{Text: "vv"}}}
	if n, b := (Value{Text: whole.Show(ShownBytes), Data: whole}).Size(); n != 2 || b != 3 {
		t.Errorf("a whole value: %d nodes, %d bytes; want 2 and 3", n, b)
	}
}

// Show at ShownBytes writes at most MaxShown bytes, the bound the rule holds
// a whole value's Text to, and a value cut there writes exactly that.
// Planted break: MaxShown without the bytes of the ....
func TestMaxShownHoldsShow(t *testing.T) {
	long := &Data{Kind: DataList, Items: []*Data{{Text: strings.Repeat("a", 4*ShownBytes)}}}
	if got := len(long.Show(ShownBytes)); got != MaxShown {
		t.Errorf("a cut value shows %d bytes, want MaxShown, %d", got, MaxShown)
	}
}

// A description that sets no max_bytes is held to the default, and one
// that sets it to its own. Planted break: 0 read as no bytes.
func TestDescriptionBytes(t *testing.T) {
	if got := (Description{}).Bytes(); got != DefaultMaxBytes {
		t.Errorf("no max_bytes: %d, want %d", got, DefaultMaxBytes)
	}
	if got := (Description{MaxBytes: 7}).Bytes(); got != 7 {
		t.Errorf("max_bytes 7: %d", got)
	}
}
