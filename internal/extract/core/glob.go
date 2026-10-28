package core

import "strings"

// HasGlob reports whether a file a spec names holds *, ? or [, which errata
// reads as a glob: a files: entry with one is matched against the files git
// tracks (Files), and exists and size refuse it in file, since read as a
// name it would be no file. It is the one definition of the glob
// characters, which env, file sets and filestat all read.
func HasGlob(file string) bool { return strings.ContainsAny(file, "*?[") }
