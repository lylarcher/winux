//go:build !windows

package core

// systemctl / svchost manage Windows services and scheduled tasks, which only
// exist on Windows; these stubs keep the package cross-compiling.

import (
	"fmt"
	"os"
)

func init() {
	addCmd("systemctl", "systemd-style service manager on Windows (Windows-only)", cmdSystemctlStub)
	addCmd("svchost", "built-in Windows service host for systemctl units (Windows-only)", cmdSvchostStub)
}

func cmdSystemctlStub(args []string) int {
	fmt.Fprintln(os.Stderr, "systemctl: this command is only available on Windows")
	return 1
}

func cmdSvchostStub(args []string) int {
	fmt.Fprintln(os.Stderr, "svchost: this command is only available on Windows")
	return 1
}

var _ = os.Stderr
