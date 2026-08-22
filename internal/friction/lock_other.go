//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package friction

import "errors"

// lockDir refuses on platforms without the advisory file lock the inbox's
// capacity check depends on. Recording fails closed there rather than racing
// past the retention cap.
func lockDir(string) (func(), error) {
	return nil, errors.New("the friction inbox requires an advisory file lock this platform does not provide")
}
