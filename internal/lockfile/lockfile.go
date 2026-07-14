// Package lockfile provides a tiny advisory file lock used to serialize
// concurrent shim processes appending to the record journal or mutating the
// replay state. It relies on flock(2), which is available on every Unix
// platform pathshim supports (shims are symlinks, so pathshim is Unix-only
// by construction).
package lockfile

import (
	"os"
	"syscall"
)

// Lock is a held advisory lock; release it with Release.
type Lock struct {
	f *os.File
}

// Acquire blocks until the exclusive lock on path is held. The lock file is
// created on demand and is distinct from the data file it protects, so the
// data file itself can be atomically renamed while locked.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe to call once per Acquire.
func (l *Lock) Release() error {
	// Closing the descriptor releases the flock; unlock first anyway so the
	// intent is explicit and errors surface.
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); err != nil {
		l.f.Close()
		return err
	}
	return l.f.Close()
}

// With runs fn while holding the lock on path.
func With(path string, fn func() error) error {
	l, err := Acquire(path)
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}
