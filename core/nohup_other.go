//go:build !windows

package core

// The POSIX half of nohup. A process group leader with its own session has no
// controlling terminal, so the SIGHUP that a terminal sends when it goes away
// cannot be delivered to it — which is exactly what GNU nohup plus the shell's
// `&`/disown achieves. Setsid is the shortest path to that state.

import (
	"os/exec"
	"syscall"
)

// nohupCommandLine is Windows-only machinery: on POSIX the argument vector is
// passed to the kernel as-is, so there is nothing to override.
func nohupCommandLine(c *exec.Cmd, line string) {}

func nohupSpawn(newCmd func() (*exec.Cmd, error), detach bool) (*exec.Cmd, error) {
	cmd, err := newCmd()
	if err != nil {
		return nil, err
	}
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	return cmd, cmd.Start()
}
