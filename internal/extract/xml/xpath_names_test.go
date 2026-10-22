package xml

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// nameStartRanges is NameStartChar of XML 1.0 fifth edition, section 2.3,
// without the colon, which XML Namespaces keeps out of a name's parts.
var nameStartRanges = [][2]rune{
	{'A', 'Z'}, {'_', '_'}, {'a', 'z'},
	{0xC0, 0xD6}, {0xD8, 0xF6}, {0xF8, 0x2FF}, {0x370, 0x37D}, {0x37F, 0x1FFF},
	{0x200C, 0x200D}, {0x2070, 0x218F}, {0x2C00, 0x2FEF}, {0x3001, 0xD7FF},
	{0xF900, 0xFDCF}, {0xFDF0, 0xFFFD}, {0x10000, 0xEFFFF},
}

// isXMLNameStart is the production: each range is tried at both its ends
// and one past each, which is a gap of the production every time, so a
// range cut short, widened or dropped fails here. The planted break is
// ending the range from 0x37F at 0x1FFE.
func TestXMLNameStartIsTheProduction(t *testing.T) {
	in := func(r rune) bool {
		for _, rg := range nameStartRanges {
			if rg[0] <= r && r <= rg[1] {
				return true
			}
		}
		return false
	}
	for _, rg := range nameStartRanges {
		for _, r := range []rune{rg[0] - 1, rg[0], rg[1], rg[1] + 1} {
			if got, want := isXMLNameStart(r), in(r); got != want {
				t.Errorf("isXMLNameStart(%U) = %v, want %v", r, got, want)
			}
		}
	}
	if isXMLNameStart(':') {
		t.Error("isXMLNameStart(':') = true; a colon separates a prefix and is no part of a name")
	}
}

// A path selects elements whose names start outside ASCII, one from each of
// four ranges of the production. The planted break is refusing every name
// that starts at or above 0xC0, which refuses each of these paths.
func TestXPathSelectsNamesOutsideASCII(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.xml": "<ÿ>\n<é>1</é>\n<ж>2</ж>\n<中>3</中>\n</ÿ>"})
	for path, want := range map[string]string{
		"/ÿ/é": "1@2",
		"/ÿ/ж": "2@3",
		"/ÿ/中": "3@4",
	} {
		if got := extracttest.Texts(mustRun(t, xmlSpec(path, nil), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
