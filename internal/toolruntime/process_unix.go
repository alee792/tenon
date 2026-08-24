//go:build unix

package toolruntime

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the host in its own process group before it starts,
// so tenon can signal the whole tree at once. A host is often a wrapper that
// execs a child — `uv run` starts a Python interpreter, a shell `-c` execs its
// command — and that child inherits the host's stdout pipe. Killing only the
// host leaves such a child alive holding the pipe open, so the read tenon is
// blocked on never sees EOF until the child exits on its own. Its own group
// lets killProcessGroup reach the child too.
func isolateProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the host's whole process group. Because the host
// leads its own group (isolateProcessGroup set Setpgid, so its group id equals
// its pid), signaling the negative pid reaches the host and every descendant
// that stayed in the group. It falls back to killing just the host when the
// group id cannot be resolved.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}
