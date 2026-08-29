//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package pluginref

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockCache takes an exclusive advisory lock on the cache's .lock file for
// the duration of one fetch, so a second operator process serializes behind
// it rather than racing over the shared content-addressed cache (mirroring
// internal/integration's store lock).
func lockCache(base string) (func(), error) {
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
