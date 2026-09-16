//go:build aix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// withHistoryLock serializes appends and snapshot rewrites across processes.
// AIX does not provide flock, so use an exclusive advisory record lock over
// the entire lock file instead.
func withHistoryLock(path string, fn func() error) error {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Len: 0}
	if err := unix.FcntlFlock(file.Fd(), unix.F_SETLKW, &lock); err != nil {
		return err
	}
	defer func() {
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLKW, &lock)
	}()
	return fn()
}
