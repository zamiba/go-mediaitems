//go:build windows

package storageunit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// diskSpace reports the free and total bytes of the volume holding path.
//
// GetDiskFreeSpaceEx takes a directory rather than a drive letter and works for
// UNC paths, so a storage unit on a network share measures like any other. The
// free figure is freeToCaller, which honours per-user quotas - the Windows
// analogue of preferring Bavail over Bfree, and for the same reason.
func diskSpace(path string) (free, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &totalBytes, &totalFree); err != nil {
		return 0, 0, err
	}
	return freeToCaller, totalBytes, nil
}

// samePath reports whether two absolute paths name the same folder, for
// duplicate detection in Add. Windows paths are case-insensitive, so a folder
// added as C:\Media and again as c:\media is one folder and the second Add is
// the duplicate it looks like.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// acquireLock takes an exclusive advisory lock on the sibling lock file and
// returns the function that releases it. It blocks until the lock is available:
// the critical section is one read-modify-write of a small file, so waiting is
// measured in milliseconds and is what makes a concurrent Add safe.
func acquireLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("storageunit: opening lock %s: %w", path, err)
	}
	// Lock the whole theoretical range rather than the file's current length,
	// which is zero - a byte-range lock over an empty file would guard nothing.
	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, ^uint32(0), ^uint32(0),
		&overlapped,
	)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("storageunit: locking %s: %w", path, err)
	}
	return func() {
		var overlapped windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
		_ = f.Close()
	}, nil
}
