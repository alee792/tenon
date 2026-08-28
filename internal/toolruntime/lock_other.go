//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package toolruntime

import (
	"context"
	"errors"
)

// acquireRuntimeLock refuses on platforms without the advisory file lock
// the shared runtime cache depends on. Preparation fails closed there
// rather than racing a second tenon process over shared, machine-wide
// runtime state.
func acquireRuntimeLock(context.Context, string) (func(), error) {
	return nil, errors.New("the shared runtime cache requires an advisory file lock this platform does not provide")
}
