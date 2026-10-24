package check

import (
	"strings"
	"testing"
)

// KeyNote names a value's key after its location, quoted, and a long key by
// its first 64 bytes cut at a character; the empty key of a reader that
// names keys is key "", and a value with no key gets nothing.
func TestValueKeyNote(t *testing.T) {
	long := strings.Repeat("k", 65)
	for _, c := range []struct {
		v    Value
		want string
	}{
		{Value{Text: "1"}, ""},
		{Value{Text: "1", Key: "API_PORT"}, ` key "API_PORT"`},
		{Value{Text: "1", Key: "A\tB"}, ` key "A\tB"`},
		{Value{Text: "1", Key: long}, ` key "` + long[:64] + `"...`},
		// A character across byte 64 is left out whole, not cut in two.
		{Value{Text: "1", Key: long[:63] + "Éx"}, ` key "` + long[:63] + `"...`},
		{Value{Text: "1", Key: long[:62] + "€x"}, ` key "` + long[:62] + `"...`},
		{Value{Text: "1", Key: long[:64] + "Éx"}, ` key "` + long[:64] + `"...`},
		// The bounds of the cut: 63 and 64 bytes whole, 65 cut to 64, a
		// character that ends at byte 64 kept whole, and a key of 65 bytes
		// that are no character's start cut to nothing.
		{Value{Text: "1", Key: long[:63]}, ` key "` + long[:63] + `"`},
		{Value{Text: "1", Key: long[:64]}, ` key "` + long[:64] + `"`},
		{Value{Text: "1", Key: long[:62] + "É"}, ` key "` + long[:62] + `É"`},
		{Value{Text: "1", Key: long[:62] + "Éx"}, ` key "` + long[:62] + `É"...`},
		{Value{Text: "1", Key: strings.Repeat("\x80", 65)}, ` key ""...`},
		// The empty key of a reader that names keys is not no key.
		{Value{Text: "x", HasKey: true}, ` key ""`},
	} {
		if got := c.v.KeyNote(); got != c.want {
			t.Errorf("%q: got %q, want %q", c.v.Key, got, c.want)
		}
	}
}
