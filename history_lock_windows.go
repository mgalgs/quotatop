//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// withHistoryLock serializes appends and snapshot rewrites across processes.
// Windows releases this byte-range lock when the handle is closed on exit.
func withHistoryLock(path string, fn func() error) error {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	handle := windows.Handle(file.Fd())
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		return err
	}
	defer windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
	return fn()
}
