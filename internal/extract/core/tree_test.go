package core

import (
	"errors"
	"testing"
)

func TestLineAt(t *testing.T) {
	data := []byte("ab\ncd\n\nx")
	l := LineIndex(data)
	for off, want := range map[int]int{0: 1, 2: 1, 3: 2, 5: 2, 6: 3, 7: 4, 8: 4} {
		if got := l.At(off); got != want {
			t.Errorf("at(%d) = %d, want %d", off, got, want)
		}
	}
	// The cursor gives the index's line for offsets forward, the same
	// offset again, and back.
	c := NewLineCursor(data)
	for _, off := range []int{0, 3, 3, 7, 8, 2, 6, 0, 8} {
		if got, want := c.At(off), l.At(off); got != want {
			t.Errorf("cursor at(%d) = %d, want %d", off, got, want)
		}
	}
}

// A step of a path that selects more than maxValues nodes stops the path,
// even when a later step would select fewer.
func TestEvalStopsAtTheValueLimit(t *testing.T) {
	list := &Node{Shape: Sequence}
	for range MaxValues + 1 {
		list.Items = append(list.Items, NewMapping(1))
	}
	p, err := parsePath("[*].absent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(list); !errors.Is(err, ErrTooManyValues) {
		t.Errorf("a step of 100001 nodes: %v", err)
	}
	list.Items = list.Items[:MaxValues]
	if got, err := p.Eval(list); err != nil || len(got) != 0 {
		t.Errorf("a step of 100000 nodes: %d, %v", len(got), err)
	}
}
