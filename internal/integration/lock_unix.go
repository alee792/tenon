//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package integration

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockStore takes an exclusive advisory lock on the store's .lock file for the
// duration of one mutation, so a second operator process serializes behind it
// rather than racing over the shared content-addressed store. Unlike the
// friction inbox, an operator command may wait, so the lock blocks.
func lockStore(base string) (func(), error) {
	path := filepath.Join(base, ".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// ownedByCaller reports whether the filesystem entry is owned by the invoking
// user, so install never stages content from a directory another user controls.
func ownedByCaller(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true // no ownership metadata: fall back to the store's own perms
	}
	return int(st.Uid) == os.Getuid()
}
