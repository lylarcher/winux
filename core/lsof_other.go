//go:build !windows

package core

// Stubs so the package keeps cross-compiling: the process list and the socket
// tables behind lsof are Windows-only, like netstat/ss.

func lsofPIDs() ([]int, error) { return nil, &unsupportedError{"process list"} }

func lsofProcInfo(pid int) (name, path, user string) { return "", "", "" }
