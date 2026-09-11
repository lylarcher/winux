//go:build !windows

package core

// The activate/deactivate commands exist everywhere (so --help and the command
// table work on non-Windows CI), but they only make sense on Windows where the
// activation scripts live.

func init() {
	addCmd("activate", "activate winux in the current shell (Windows only)", cmdActivateUnsupported)
	addCmd("deactivate", "deactivate winux in the current shell (Windows only)", cmdDeactivateUnsupported)
}

func cmdActivateUnsupported(args []string) int {
	return activationUnsupported("activate", args)
}

func cmdDeactivateUnsupported(args []string) int {
	return activationUnsupported("deactivate", args)
}

func activationUnsupported(name string, args []string) int {
	f := NewFlags()
	if !parseOrDie(name, f, args) {
		return 2
	}
	if handleHelpVersion(name, "", f) {
		return 0
	}
	warn(name, "only supported on Windows")
	return 1
}
