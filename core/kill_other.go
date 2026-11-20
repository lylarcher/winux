//go:build !windows

package core

// The POSIX implementation: the signals winux maps onto Windows mechanisms have
// exact counterparts here, so they are sent as themselves. That keeps `kill`
// usable (and testable) when the toolbox is built for Linux or macOS.

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func killSend(pid int, act killAction, tree, escalate bool) error {
	var sig syscall.Signal
	switch act {
	case actCheck:
		sig = 0
	case actForce:
		sig = syscall.SIGKILL
	case actGraceful:
		sig = syscall.SIGTERM
	case actSuspend:
		sig = syscall.SIGSTOP
	case actResume:
		sig = syscall.SIGCONT
	default:
		return fmt.Errorf("unsupported action")
	}
	err := syscall.Kill(pid, sig)
	if err == nil {
		return nil
	}
	switch {
	case err == syscall.ESRCH:
		return fmt.Errorf("No such process")
	case err == syscall.EPERM:
		return fmt.Errorf("Operation not permitted")
	}
	return err
}

// processExists is used by tests and by the graceful path.
func killExists(pid int) error {
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.EPERM {
			return nil
		}
		return fmt.Errorf("No such process")
	}
	return nil
}

func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if killExists(pid) != nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var _ = os.Getpid
