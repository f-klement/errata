package core

import (
	"path/filepath"
	"slices"

	"github.com/f-klement/errata/internal/check"
)

// Where is what a spec of a kind that reads file sets says of where it
// reads: one file, or the files a list or a glob names (Files), and the most
// bytes it reads, 0 for DefaultMaxBytes. A spec embeds it, so its keys are
// the spec's own, and the kind builds its reader with BuildOneOrSet.
type Where struct {
	File     string   `json:"file,omitempty"`
	Files    []string `json:"files,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

// Place is w with only what decides where it reads: its file or files as
// PlaceFiles writes them, and no max_bytes, since a limit does not change
// what is read.
func (w Where) Place() Where {
	w.File, w.Files = PlaceFiles(w.File, w.Files)
	w.MaxBytes = 0
	return w
}

// Resolved is w with its max_bytes written out and its files copied, for
// errata list.
func (w Where) Resolved() Where {
	w.Files = slices.Clone(w.Files)
	w.MaxBytes = MaxBytes(w.MaxBytes)
	return w
}

// OneFile is what a spec of a kind that reads one file says of where it
// reads: the file, and the most bytes it reads, 0 for DefaultMaxBytes. A
// spec embeds it, so its keys are the spec's own. It has no files: key, so
// a spec of such a kind that writes one is refused as any unknown key is.
type OneFile struct {
	File     string `json:"file"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// Place is o with only what decides where it reads: the file cleaned, and
// no max_bytes.
func (o OneFile) Place() OneFile {
	return OneFile{File: filepath.Clean(o.File)}
}

// Resolved is o with its max_bytes written out, for errata list.
func (o OneFile) Resolved() OneFile {
	o.MaxBytes = MaxBytes(o.MaxBytes)
	return o
}

// Check is the Source of o for a reader of kind, refused as CheckSource
// refuses it.
func (o OneFile) Check(kind string) (Source, error) {
	return CheckSource(kind, o.File, o.MaxBytes)
}

// ExtractOne reads the file of src under root, with every check Read makes,
// and returns the values r finds in its bytes: the Extract of a reader of
// one file.
func ExtractOne(root string, src Source, r FileReader) ([]check.Value, error) {
	data, err := src.Read(root)
	if err != nil {
		return nil, err
	}
	return r.FromShared(data, &Spent{})
}
