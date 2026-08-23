//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package schedule

import "errors"

// acquireLock refuses on platforms without the advisory file lock the exclusive
// clock ownership depends on. A clock fails closed there rather than racing a
// second clock over the same schedules.
func acquireLock(string) (func(), error) {
	return nil, errors.New("the schedule clock requires an advisory file lock this platform does not provide")
}
