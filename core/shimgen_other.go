//go:build !windows

package core

// The shims materializer is Windows-only (it writes the cmd\ helpers beside
// winux.exe); register the command name everywhere so --help and the command
// table work on non-Windows CI.

func init() {
	addCmd("shims", "recreate/refresh the cmd\\ shim files beside this winux.exe (Windows only)", cmdShimsUnsupported)
}

func cmdShimsUnsupported(args []string) int {
	f := NewFlags()
	if !parseOrDie("shims", f, args) {
		return 2
	}
	if handleHelpVersion("shims", "", f) {
		return 0
	}
	warn("shims", "only supported on Windows")
	return 1
}
