//go:build !windows

package core

import "os"

func winAllocSize(_ string, fi os.FileInfo) int64 { return fi.Size() }