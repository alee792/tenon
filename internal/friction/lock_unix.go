//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package friction

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive advisory lock on the inbox's .lock file for the
// duration of one capacity check and write, so concurrent managed servers
// cannot exceed the retention cap. A contended lock fails rather than waits:
// a managed call never blocks on the inbox.
func lockDir(dir string) (func(), error) {
	path := filepath.Join(dir, ".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
