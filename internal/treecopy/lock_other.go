//go:build !unix

package treecopy

// lock takes no lock where flock is missing, so each copy is made in a
// directory of its own.
func lock(string) (release func() error, ok bool) {
	return nil, false
}

// locking is whether lock can take a lock here.
const locking = false
