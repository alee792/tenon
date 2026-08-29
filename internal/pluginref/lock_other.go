//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package pluginref

import "errors"

// lockCache refuses on platforms without the advisory file lock the shared
// cache depends on. A fetch fails closed there rather than racing a second
// operator process over content-addressed state.
func lockCache(string) (func(), error) {
	return nil, errors.New("the plugin reference cache requires an advisory file lock this platform does not provide")
}
