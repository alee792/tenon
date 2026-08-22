//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package integration

import (
	"errors"
	"os"
)

// lockStore refuses on platforms without the advisory file lock the shared
// store depends on. A mutation fails closed there rather than racing a second
// operator process over content-addressed state.
func lockStore(string) (func(), error) {
	return nil, errors.New("the integration store requires an advisory file lock this platform does not provide")
}

// ownedByCaller cannot read ownership metadata on these platforms; the store's
// own owner-only permissions remain the containment boundary.
func ownedByCaller(os.FileInfo) bool { return true }
