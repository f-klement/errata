package compose

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// A stage is the tree compose-go loads from: a new temporary directory
// that holds a copy of each file the walker read, at its path below the
// root, and nothing else. compose-go opens some files itself, outside
// any resource loader: an included file, an extends file, an included
// project's .env and an include's env_file. Under the stage each such
// open finds the bytes errata read, through core, within the
// repository, or no file at all, never a file of the repository read a
// second time, and never one errata did not read.
type stage struct {
	dir  string   // the stage's directory, with no link in its path
	root *os.Root // dir, which the copies are written through
}

// newStage makes an empty stage.
func newStage() (*stage, error) {
	dir, err := os.MkdirTemp("", "errata-compose-")
	if err != nil {
		return nil, fmt.Errorf("compose: a directory to hand compose-go the files read: %w", err)
	}
	// A relative TMPDIR gives a relative dir, which compose-go would make
	// absolute in some places and not in others.
	if dir, err = absolute(dir); err != nil {
		return nil, err
	}
	// compose-go follows a link in a develop.watch path from the top of
	// the file system down, so the stage's own path holds none: on a
	// system whose temporary directory is a link, a value would name its
	// target.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("compose: %w", err)
	}
	dir = resolved
	root, err := os.OpenRoot(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("compose: %w", err)
	}
	return &stage{dir: dir, root: root}, nil
}

// absolute is dir, a new temporary directory, as an absolute path; it is
// removed when that cannot be had.
func absolute(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("compose: TMPDIR %s is relative, and the directory to read it from: %w", os.Getenv("TMPDIR"), err)
	}
	return abs, nil
}

// put copies data, the file rel below the root, to the stage. rel is a
// clean path that does not climb, as core.Source reads it; os.Root keeps
// the write inside the stage whatever it holds.
func (s *stage) put(rel string, data []byte) error {
	if dir := path.Dir(rel); dir != "." {
		if err := s.root.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("compose: copying %s for compose-go: %w", rel, err)
		}
	}
	if err := s.root.WriteFile(rel, data, 0o600); err != nil {
		return fmt.Errorf("compose: copying %s for compose-go: %w", rel, err)
	}
	return nil
}

// close removes the stage and everything in it.
func (s *stage) close() {
	_ = s.root.Close()
	_ = os.RemoveAll(s.dir)
}
