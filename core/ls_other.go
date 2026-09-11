//go:build !windows

package core

// Non-Windows stubs so the package keeps cross-compiling.

import (
	"os"
	"time"
)

func winHidden(string) bool   { return false }
func winReadonly(string) bool { return false }
func fileTimes(fi os.FileInfo) (time.Time, time.Time, bool) {
	return fi.ModTime(), fi.ModTime(), false
}

func fileID(path string) uint64 { return syntheticID(path) }