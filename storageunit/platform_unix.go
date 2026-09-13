//go:build unix

package storageunit

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// diskSpace reports the free and total bytes of the filesystem holding path.
//
// Free is Bavail, the space available to an unprivileged caller - not Bfree,
// which includes the blocks reserved for root. The reserve is not somewhere a
// rip or an install can actually be written, so counting it would promise the
// user room that does not exist.
func diskSpace(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// Bsize and Bavail differ in signedness across the Unixes; widening both to
	// uint64 keeps one implementation for Linux, macOS and the BSDs.
	bsize := uint64(st.Bsize)
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize, nil
}

// samePath reports whether two absolute paths name the same folder, for
// duplicate detection in Add. Unix paths are case-sensitive, so this is a plain
// comparison of the cleaned paths; two different paths reaching the same folder
// through symlinks or bind mounts are deliberately treated as distinct, since
// resolving them would make Add's answer depend on state that can change after
// the fact.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
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
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("storageunit: locking %s: %w", path, err)
	}
	return func() {
		// Closing the descriptor releases the flock; unlocking first makes the
		// release explicit rather than a side effect of the close.
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
