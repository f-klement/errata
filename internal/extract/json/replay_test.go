package json

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

const deepRefused = "f: line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"

// The JSON inputs that the earlier limits refused though they are ordinary,
// and that got past those limits: depth, at the limit and past it, and
// 100 KB of [. None replays a quadratic pass, so each is bounded by
// extracttest.OtherBound.
var replays = []extracttest.Replay[Spec]{
	{Name: "JSON 1000 levels deep", Spec: Spec{File: "f", Path: "$" + strings.Repeat("[0]", 1000)},
		Body: func() string { return extracttest.Nest(1000, `"x"`) }, Values: 1, Last: "x"},
	{Name: "JSON 1001 levels deep", Spec: Spec{File: "f", Path: "$"},
		Body: func() string { return extracttest.Nest(1001, "1") }, Refused: deepRefused},
	{Name: "100 KB of JSON [", Spec: Spec{File: "f", Path: "$"},
		Body: func() string { return strings.Repeat("[", 100000) }, Refused: deepRefused},
	{Name: "JSON 999 levels around 20000 items", Spec: Spec{File: "f", Path: "$" + strings.Repeat("[0]", 998) + "[-1]"},
		Body: func() string {
			return strings.Repeat("[", 999) + extracttest.List(20000, "1") + strings.Repeat("]", 999)
		}, Values: 1, Last: "1"},
}

func TestReplayMeasuredInputs(t *testing.T) {
	extracttest.RunReplays(t, build, replays, false)
}
