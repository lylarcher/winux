//go:build windows

package core

// openWithDefaultApp starts the Windows shell for a resolved path: explorer
// shows directories in File Explorer (资源管理器) and opens files with their
// default application. The launch is detached — explorer is a GUI process that
// must not be waited on, exactly like the ncpa.cpl / ms-settings panels that
// nmtui opens.

func openWithDefaultApp(path string) error {
	return launchDetached("explorer.exe", path)
}
