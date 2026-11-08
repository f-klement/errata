package ini

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// quadletImage is the image a Quadlet file's Image= values vs give, as
// Podman 5.4.0's Quadlet takes it (quadlet.go): the last value, through
// Lookup (lookupLast). One that ends in .image is the resource name of the
// .image file beside the unit (imageName), one that ends in .build the
// name of the image the .build file beside it builds (builtName), each
// read with its drop-ins; Quadlet reads that file once, so errata does.
// Any other is the reference as written.
func (x unitExtractor) quadletImage(ctx context.Context, root string, index *core.Tracked, vs []check.Value, left int64) ([]check.Value, int64, error) {
	if len(vs) == 0 {
		return vs, left, nil
	}
	v := lookupLast(vs)
	read := x.imageName
	switch {
	case strings.HasSuffix(v.Text, ".build"):
		read = x.builtName
	case !strings.HasSuffix(v.Text, ".image"):
		return []check.Value{v}, left, nil
	}
	if strings.Contains(v.Text, "/") {
		return nil, 0, fmt.Errorf("systemd: %s:%d sets Image= to %s, and Quadlet finds a Quadlet file by its name alone, with no directory", v.At.File, v.At.Line, core.Quote(v.Text))
	}
	got, rest, err := read(ctx, root, index, v, left)
	if err != nil {
		return nil, 0, fmt.Errorf("systemd: %s:%d sets Image= to the Quadlet file %s beside it: %w", v.At.File, v.At.Line, core.Quote(v.Text), err)
	}
	return []check.Value{got}, rest, nil
}

// lookupLast is UnitFile.Lookup of values: the last, without white space at
// its end and without " at either end.
func lookupLast(vs []check.Value) check.Value {
	v := vs[len(vs)-1]
	v.Text = strings.Trim(strings.TrimRightFunc(v.Text, unicode.IsSpace), `"`)
	return v
}

// imageName is ConvertImage's resource name of the .image file v names:
// its ImageTag= when one is set and not empty, and its Image= otherwise,
// each the last, through Lookup.
func (x unitExtractor) imageName(ctx context.Context, root string, index *core.Tracked, v check.Value, left int64) (check.Value, int64, error) {
	tags, left, err := x.sibling(v.Text, "Image", "ImageTag").readValues(ctx, root, index, left)
	if err != nil {
		return check.Value{}, 0, err
	}
	if len(tags) > 0 && lookupLast(tags).Text != "" {
		return lookupLast(tags), left, nil
	}
	images, left, err := x.sibling(v.Text, "Image", "Image").readValues(ctx, root, index, left)
	if err != nil {
		return check.Value{}, 0, err
	}
	if len(images) == 0 || lookupLast(images).Text == "" {
		return check.Value{}, 0, errors.New("it sets no Image=, which Quadlet refuses")
	}
	return lookupLast(images), left, nil
}

// builtName is GetBuiltImageName of the .build file v names: its first
// ImageTag=, through LookupAll, as written.
func (x unitExtractor) builtName(ctx context.Context, root string, index *core.Tracked, v check.Value, left int64) (check.Value, int64, error) {
	tags, left, err := x.sibling(v.Text, "Build", "ImageTag").readValues(ctx, root, index, left)
	if err != nil {
		return check.Value{}, 0, err
	}
	if len(tags) == 0 {
		return check.Value{}, 0, errors.New("it sets no ImageTag=, which Quadlet refuses")
	}
	return tags[0], left, nil
}

// sibling is the extractor of key in section of the Quadlet file name,
// beside the unit, with its drop-ins.
func (x unitExtractor) sibling(name, section, key string) unitExtractor {
	file := path.Join(path.Dir(path.Clean(filepath.ToSlash(x.File))), name)
	return unitExtractor{Source: core.Source{File: file, Limit: x.Limit}, sections: []string{section}, key: key,
		class: quadletUnit, base: name, valueLimit: x.valueLimit}
}
