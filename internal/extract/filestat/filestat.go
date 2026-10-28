// Package filestat is the exists and size extractors: whether a file is at
// a name, and how many bytes it holds, from core's Stat. Neither opens the
// file or reads a byte of it.
package filestat

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec names the one file an exists or a size extractor looks at.
type Spec struct {
	File string `json:"file"`
}

// BuildExists checks s and returns the exists extractor. Its one value is
// "true" when a regular file is at the name once its symlinks are followed,
// and "false" when nothing is there or a link there dangles inside the
// repository. A directory, anything else that is there, a link that leads
// out, and a name errata cannot look up are errors.
func BuildExists(s Spec) (check.Extractor, error) {
	src, err := checkSpec("exists", s)
	if err != nil {
		return nil, err
	}
	return exists{src}, nil
}

// BuildSize checks s and returns the size extractor. Its one value is the
// size in bytes of the regular file at the name once its symlinks are
// followed, in decimal. No file there, a directory, anything else and a link
// that leads out are errors.
func BuildSize(s Spec) (check.Extractor, error) {
	src, err := checkSpec("size", s)
	if err != nil {
		return nil, err
	}
	return size{src}, nil
}

// checkSpec refuses what any file refuses, and a glob: a files: entry
// reads *, ? and [ as one, and a glob read here as a name would be no file,
// so a rule that a file stays removed would hold over any file the glob
// matched. No size limit applies, since nothing is read.
func checkSpec(kind string, s Spec) (core.Source, error) {
	src, err := core.CheckSource(kind, s.File, 0)
	if err != nil {
		return core.Source{}, err
	}
	if core.HasGlob(s.File) {
		return core.Source{}, fmt.Errorf("%s: file %s holds *, ? or [, which errata reads as a glob; %s reads the one file it names", kind, core.Quote(s.File), kind)
	}
	return src, nil
}

type exists struct{ src core.Source }

func (e exists) Extract(root string) ([]check.Value, error) {
	info, err := e.src.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return e.value("false"), nil
	}
	if err != nil {
		return nil, err
	}
	if err := fileThere(e.src, info); err != nil {
		return nil, err
	}
	return e.value("true"), nil
}

func (e exists) value(text string) []check.Value {
	return []check.Value{{Text: text, At: check.Location{File: e.src.File}}}
}

type size struct{ src core.Source }

func (e size) Extract(root string) ([]check.Value, error) {
	info, err := e.src.Stat(root)
	if err != nil {
		return nil, err
	}
	if err := fileThere(e.src, info); err != nil {
		return nil, err
	}
	return []check.Value{{Text: strconv.FormatInt(info.Size(), 10), At: check.Location{File: e.src.File}}}, nil
}

// fileThere refuses what is at the name and is no regular file, as a reader
// refuses it: a directory, which a consumer such as kustomize may read in
// the file's place, so neither true nor false is safe for it, and a FIFO, a
// socket or a device, which git never checks out.
func fileThere(src core.Source, info fs.FileInfo) error {
	if info.IsDir() {
		return fmt.Errorf("stat %s: %w", src.File, core.ErrDirectory)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("stat %s: it is not a regular file", src.File)
	}
	return nil
}
