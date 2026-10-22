package core

import (
	"math"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The read cannot overflow whatever its limit: a source with the largest
// int64 as its limit, which Build refuses, still reads the whole file. The
// planted break is reading limit+1 bytes, which wraps to a negative count
// and reads nothing.
func TestReadDoesNotOverflowAtTheLargestLimit(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "a: 1\n"})
	data, err := Source{File: "f", Limit: math.MaxInt64}.Read(root)
	if err != nil || string(data) != "a: 1\n" {
		t.Errorf("read %q, %v", data, err)
	}
}
