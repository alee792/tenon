//go:build !unix

package toolruntime

import "os/exec"

// isolateProcessGroup is a no-op on platforms without POSIX process groups.
// Tenon's supported runtimes are unix; this keeps the package buildable
// elsewhere without promising the same whole-tree termination.
func isolateProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills just the host process on platforms without POSIX
// process groups.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
