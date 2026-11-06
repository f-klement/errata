package jsonc

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

const deepRefused = "f: line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"

// The inputs the json reader replays, in both dialects, and the shapes
// only these dialects add: a file of comments, and of trailing commas.
// None replays a quadratic pass, so each is bounded by
// extracttest.OtherBound. Planted break: a /* comment searched for its end
// in a copy of the rest of the file, which is quadratic in the comments.
func replays(dialect string) []extracttest.Replay[Spec] {
	return []extracttest.Replay[Spec]{
		{Name: dialect + " 1000 levels deep", Spec: Spec{File: "f", Path: "$" + strings.Repeat("[0]", 1000)},
			Body: func() string { return extracttest.Nest(1000, `"x"`) }, Values: 1, Last: "x"},
		{Name: dialect + " 1001 levels deep", Spec: Spec{File: "f", Path: "$"},
			Body: func() string { return extracttest.Nest(1001, "1") }, Refused: deepRefused},
		{Name: "100 KB of " + dialect + " [", Spec: Spec{File: "f", Path: "$"},
			Body: func() string { return strings.Repeat("[", 100000) }, Refused: deepRefused},
		{Name: dialect + " 999 levels around 20000 items", Spec: Spec{File: "f", Path: "$" + strings.Repeat("[0]", 998) + "[-1]"},
			Body: func() string {
				return strings.Repeat("[", 999) + extracttest.List(20000, "1") + strings.Repeat("]", 999)
			}, Values: 1, Last: "1"},
		{Name: dialect + " 200000 block comments", Spec: Spec{File: "f", Path: "$[-1]"},
			Body: func() string { return "[" + strings.Repeat("/**/1,", 200000) + "]" }, Values: 1, Last: "1"},
		{Name: dialect + " 200000 line comments", Spec: Spec{File: "f", Path: "$[-1]"},
			Body: func() string { return "[" + strings.Repeat("1, //\n", 200000) + "]" }, Values: 1, Last: "1"},
	}
}

func TestReplayMeasuredInputs(t *testing.T) {
	extracttest.RunReplays(t, buildC, replays(JSONC), false)
	extracttest.RunReplays(t, build5, replays(JSON5), false)
}
