//go:build unix

package treecopy

import (
	"os"
	"syscall"
)

// lock takes the lock file name, made if it is missing, for this process
// alone, and release gives it back. The kernel gives it back too when the
// process ends, however it ends, so a run that was killed holds nothing.
// ok is false when another run holds it, or it cannot be taken.
func lock(name string) (release func() error, ok bool) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return f.Close, true
}

// locking is whether lock can take a lock here.
const locking = true
