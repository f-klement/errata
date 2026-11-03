//go:build linux || darwin

package core

import (
	"os"
	"syscall"
)

// statDevice is the device of the file system that holds dir, from Stat.
func statDevice(dir string) (any, bool) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, false
	}
	return st.Dev, true
}
