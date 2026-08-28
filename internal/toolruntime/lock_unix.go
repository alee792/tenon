//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package toolruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// acquireRuntimeLock blocks until it holds an exclusive advisory lock on
// path, or ctx is done. Unlike internal/integration's lockStore, which
// blocks the OS thread unconditionally on syscall.LOCK_EX, Prepare's own
// contract is that ctx bounds the whole preparation including every
// subprocess — an unconditional blocking flock could never honor a
// cancelled or expired ctx — so this polls a non-blocking flock instead.
func acquireRuntimeLock(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(flockErr, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, flockErr
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(runtimeLockPollInterval):
		}
	}
}
