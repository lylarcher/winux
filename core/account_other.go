//go:build !windows

package core

// Stubs so the package keeps cross-compiling; account management is Windows
// only in practice.

func isElevated() bool { return false }

func systemExe(name string) string { return name }

func runNative(args ...string) int {
	warn("winux", "not supported on this platform: %s", args[0])
	return 1
}

func queryNative(args ...string) (string, error) {
	return "", &unsupportedError{args[0]}
}

func fileOwner(path string) (string, error) {
	return "", &unsupportedError{"file owner lookup"}
}

type unsupportedError struct{ tool string }

func (e *unsupportedError) Error() string { return e.tool + " is not available on this platform" }
