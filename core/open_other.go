//go:build !windows

package core

// Stub so the package keeps cross-compiling: opening a path with the default
// application is a Windows feature (explorer.exe).

func openWithDefaultApp(path string) error {
	return &unsupportedError{"opening paths with the default application"}
}
