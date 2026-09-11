//go:build windows

package core

// Windows sources of truth for ls: the hidden/read-only NTFS attributes, the
// access/creation times and the stable per-volume file id (our "inode").
// Everything here is read-only. kernel32 is owned by term_windows.go; we only
// take the one extra procedure we need.

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

var procGetFileAttributesW = kernel32.NewProc("GetFileAttributesW")

const (
	fileAttrReadonly = 0x1
	fileAttrHidden   = 0x2
)

// winAttrs returns the FILE_ATTRIBUTE_* bitmask of path, 0 on failure.
func winAttrs(path string) uint32 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	// -1 (0xFFFFFFFF) signals failure; the attribute bits never use that value
	r, _, _ := procGetFileAttributesW.Call(uintptr(unsafe.Pointer(p)))
	if r == 0xFFFFFFFF {
		return 0
	}
	return uint32(r)
}

func winHidden(path string) bool { return winAttrs(path)&fileAttrHidden != 0 }

func winReadonly(path string) bool { return winAttrs(path)&fileAttrReadonly != 0 }

// fileTimes returns the last-access and creation times carried in the file
// attributes (Go's os.FileInfo does not expose them on Windows).
func fileTimes(fi os.FileInfo) (atime, ctime time.Time, ok bool) {
	if w, ok2 := fi.Sys().(*syscall.Win32FileAttributeData); ok2 {
		return fileTimeToTime(w.LastAccessTime), fileTimeToTime(w.CreationTime), true
	}
	return fi.ModTime(), fi.ModTime(), false
}

func fileTimeToTime(ft syscall.Filetime) time.Time {
	// FILETIME: 100 ns intervals since 1601-01-01; Unix epoch is 11644473600 s later.
	// Split before subtracting so the intermediate values stay in range.
	t := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	sec := t / 10_000_000
	nsec := (t % 10_000_000) * 100
	return time.Unix(sec-11644473600, nsec)
}

// fileID resolves the NTFS file index (the closest Windows analog of an inode)
// through GetFileInformationByHandle; it falls back to a stable hash of the
// absolute path when the handle cannot be opened.
func fileID(path string) uint64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syntheticID(path)
	}
	h, err := syscall.CreateFile(p,
		0x80, // FILE_READ_ATTRIBUTES
		0x7,  // FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE
		nil, syscall.OPEN_EXISTING,
		0x02000000, // FILE_FLAG_BACKUP_SEMANTICS (needed for directories)
		0)
	if err != nil {
		return syntheticID(path)
	}
	defer syscall.CloseHandle(h)
	var b syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &b); err != nil {
		return syntheticID(path)
	}
	return uint64(b.FileIndexHigh)<<32 | uint64(b.FileIndexLow)
}