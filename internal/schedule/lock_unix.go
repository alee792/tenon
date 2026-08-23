//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock takes a non-blocking exclusive advisory lock on path, so a second
// schedule clock for the same workspace/agent/harness fails fast rather than
// running a duplicate clock. A contended lock returns errClockHeld; the caller
// renders the operator-facing message.
func acquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errClockHeld
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
