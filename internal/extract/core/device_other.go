//go:build !linux && !darwin

package core

// statDevice gives no device on an OS whose Stat errata does not read for
// one, so the look for a repository above a root crosses mount points
// there, up to maxAbove directories.
func statDevice(string) (any, bool) { return nil, false }
