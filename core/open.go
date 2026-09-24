package core

// open hands a path to the Windows shell: a directory is shown in File
// Explorer (资源管理器), a file is opened with its default application. It is
// a custom convenience command, not a Linux command — winux itself does not
// open the directory; it resolves the path (same rules as `cd`: open ., open
// .., open ../, open ~, open C:\ ...) and delegates the launch to the platform
// layer, which on Windows starts explorer.exe detached.
//
//   open .        open the current directory
//   open ../      open the parent directory
//   open C:\work  open an absolute path
//   open ~        open the user profile directory

import (
	"fmt"
	"os"
)

func init() {
	addCmd("open", "open a path with the default application (directories show in File Explorer)", cmdOpen)
}

func cmdOpen(args []string) int {
	f := NewFlags()
	if !parseOrDie("open", f, args) {
		return 2
	}
	if handleHelpVersion("open", "[PATH...]", f) {
		return 0
	}
	targets := f.Positional
	if len(targets) == 0 {
		targets = []string{"."}
	}
	rc := 0
	for _, t := range targets {
		resolved, err := openPath(t)
		if err != nil {
			warn("open", "%v", err)
			rc = 1
			continue
		}
		fmt.Printf("open: opening %s\n", resolved)
	}
	return rc
}

// openPath resolves target like `cd` does, checks that it exists, and asks the
// platform layer to open it. It returns the resolved absolute path.
func openPath(target string) (string, error) {
	resolved, err := resolveOpenPath(target)
	if err != nil {
		return "", err
	}
	if err := openWithDefaultApp(resolved); err != nil {
		return "", fmt.Errorf("%s: %v", target, err)
	}
	return resolved, nil
}

// resolveOpenPath resolves and validates the target without launching anything
// (kept separate so the resolution rules are unit-testable window-free).
func resolveOpenPath(target string) (string, error) {
	resolved, err := ResolveCdTarget(target)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("%s: No such file or directory", target)
	}
	return resolved, nil
}
