package core_test

import (
	"slices"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// A place writes a file cleaned, files cleaned and sorted, and a list of
// one file that is no glob as that file. Planted breaks: the files left
// unsorted; a list of one file not read as file:; a glob of one read as
// a file.
func TestPlaceFiles(t *testing.T) {
	for _, c := range []struct {
		file      string
		files     []string
		wantFile  string
		wantFiles []string
	}{
		{"a/../f", nil, "f", nil},
		{"", []string{"./b/*.yaml", "a/**"}, "", []string{"a/**", "b/*.yaml"}},
		{"", []string{"./f"}, "f", nil},
		{"", []string{"*.y"}, "", []string{"*.y"}},
	} {
		file, files := core.PlaceFiles(c.file, c.files)
		if file != c.wantFile || !slices.Equal(files, c.wantFiles) || (files == nil) != (c.wantFiles == nil) {
			t.Errorf("PlaceFiles(%q, %q) = %q, %q; want %q, %q", c.file, c.files, file, files, c.wantFile, c.wantFiles)
		}
	}
}

// as: map is a whole mapping, as: record a record of the fields sorted by
// name, none for a record without fields, and anything else flat.
// Planted breaks: each condition negated.
func TestTreeShape(t *testing.T) {
	for _, c := range []struct {
		as     string
		fields map[string]string
		shape  check.Shape
		want   []string
	}{
		{"", map[string]string{"a": "a"}, check.ShapeFlat, nil},
		{"map", map[string]string{"a": "a"}, check.ShapeMap, nil},
		{"record", map[string]string{"b": "x", "a": "y"}, check.ShapeRecord, []string{"a", "b"}},
		{"record", nil, check.ShapeRecord, nil},
		{"list", nil, check.ShapeFlat, nil},
	} {
		shape, fields := core.TreeShape(c.as, c.fields)
		if shape != c.shape || !slices.Equal(fields, c.want) {
			t.Errorf("TreeShape(%q, %v) = %v, %q; want %v, %q", c.as, c.fields, shape, fields, c.shape, c.want)
		}
	}
}
