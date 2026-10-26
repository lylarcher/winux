//go:build windows

package core

// WINUX_EXE has carried two different meanings, and that collision broke
// `systemctl start`:
//
//	winux-activate.cmd   sets it to the INSTALLATION DIRECTORY, because the same
//	                     value is prepended to PATH
//	systemctl / sshd     read it as the winux EXECUTABLE, because they materialize
//	                     the per-unit service host from it or spawn child shells
//
// Starting a service from an activated cmd window therefore tried to copy the
// directory `D:\my-tools\winux` into %ProgramData%\systemctl\hosts\<unit>.exe and
// failed with the distinctly unhelpful "read D:\my-tools\winux: Incorrect
// function" (Windows answers ERROR_INVALID_FUNCTION when a directory handle is
// read).
//
// The scripts now spell the two meanings explicitly (WINUX_HOME for the
// directory, WINUX_EXE for the executable), but an installed winux keeps running
// shims that were materialized by an older release, so every reader goes through
// sysWinuxBinary: a WINUX_EXE that names a directory is resolved to the winux.exe
// inside it instead of being copied as if it were a program.

import (
	"os"
	"path/filepath"
	"strings"
)

// sysWinuxBinary returns the path of the winux executable: WINUX_EXE when it
// names one (directly, or as the installation directory holding winux.exe),
// otherwise the running executable. The returned path is not required to exist —
// callers report that themselves, keeping a genuinely broken WINUX_EXE as loud as
// it was before.
func sysWinuxBinary() string {
	if v := strings.TrimSpace(os.Getenv("WINUX_EXE")); v != "" {
		return sysWinuxExeFrom(v)
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	// last resort, the same thing sshd used before: let the child lookup fail
	// with its own message instead of panicking here
	return "winux.exe"
}

// sysWinuxExeFrom maps a WINUX_EXE value to an executable path. A value naming a
// regular file is used as is; a value naming a directory (the historical spelling)
// becomes <dir>\winux.exe when that file exists. Anything else is returned
// unchanged so the caller still reports it verbatim.
func sysWinuxExeFrom(v string) string {
	if fi, err := os.Stat(v); err == nil && !fi.IsDir() {
		return v
	}
	if filepath.Ext(v) == "" {
		if p := filepath.Join(v, "winux.exe"); p != v {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return v
}
