//go:build windows

package core

// The Windows half of nohup: what "immune to hangup" means here.
//
// Closing a console (or logging off the SSH/RDP session) delivers
// CTRL_CLOSE_EVENT to every process attached to that console and kills them a
// few seconds later; there is no SIGHUP to ignore. The only way to survive is to
// not be attached in the first place, so the child is started with
// DETACHED_PROCESS: no console at all, which also means it cannot steal the
// user's keystrokes and Ctrl+C in the parent reaches only the parent.
//
// CREATE_NEW_PROCESS_GROUP additionally puts the child in its own process group,
// so a Ctrl+Break aimed at our group misses it.
//
// CREATE_BREAKAWAY_FROM_JOB covers the other half of "keeps running": a
// supervising job object (Windows Terminal, an IDE task runner, a CI agent) is
// killed as a whole when the supervisor exits, taking the child with it. A job
// that was not created with JOB_OBJECT_LIMIT_BREAKAWAY_OK refuses the flag with
// ERROR_ACCESS_DENIED, so that attempt is retried without it — the console
// detachment, which is what nohup is about, still happens.

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// nohupCommandLine hands cmd.exe an exact command line. Go quotes arguments with
// the CommandLineToArgvW rules, but cmd.exe does not parse that way: inner quotes
// would arrive backslash-escaped and the batch call would fail. SysProcAttr.CmdLine
// bypasses argv entirely, which is why the line is built in nohup.go instead.
func nohupCommandLine(c *exec.Cmd, line string) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CmdLine = line
}

func nohupSpawn(newCmd func() (*exec.Cmd, error), detach bool) (*exec.Cmd, error) {
	if !detach {
		cmd, err := newCmd()
		if err != nil {
			return nil, err
		}
		return cmd, cmd.Start()
	}

	attempts := []uint32{
		windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB,
		windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		windows.CREATE_NEW_PROCESS_GROUP, // last resort: at least immune to Ctrl+C
	}
	var lastErr error
	for i, flags := range attempts {
		cmd, err := newCmd()
		if err != nil {
			return nil, err
		}
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.CreationFlags = flags
		if err = cmd.Start(); err == nil {
			return cmd, nil
		}
		lastErr = err
		// Step down only when the previous attempt failed for a reason a weaker
		// flag can fix. A missing executable or a bad working directory is the
		// caller's problem and must be reported as-is.
		if i == 0 && !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil, err
		}
	}
	return nil, lastErr
}
