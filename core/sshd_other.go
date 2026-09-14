//go:build !windows

package core

// sshd/wssh rely on Windows pseudo consoles and local-user authentication;
// these stubs keep the package cross-compiling (winux is a Windows toolbox).

import (
	"fmt"
	"os"
)

func init() {
	addCmd("sshd", "pure-Go SSH server (Windows-only)", cmdSshdStub)
	addCmd("wssh", "pure-Go SSH client (Windows-only)", cmdWsshStub)
}

func cmdSshdStub(args []string) int {
	fmt.Fprintln(os.Stderr, "sshd: this command is only available on Windows")
	return 1
}

func cmdWsshStub(args []string) int {
	fmt.Fprintln(os.Stderr, "wssh: this command is only available on Windows")
	return 1
}

var _ = os.Stderr
