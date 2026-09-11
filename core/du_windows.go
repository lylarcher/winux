//go:build windows

package core

import (
	"os"
	"syscall"
	"unsafe"
)

var procGetFileInformationByHandleEx = kernel32.NewProc("GetFileInformationByHandleEx")

// winAllocSize returns the on-disk allocation (in bytes) of the file or
// directory at path. Falls back to fi.Size() when the handle cannot be opened
// (e.g. due to permissions or a non-NTFS filesystem).
func winAllocSize(path string, fi os.FileInfo) int64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fi.Size()
	}
	h, err := syscall.CreateFile(p,
		0x80, // FILE_READ_ATTRIBUTES
		0x7,  // FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE
		nil, syscall.OPEN_EXISTING,
		0x02000000, // FILE_FLAG_BACKUP_SEMANTICS
		0)
	if err != nil {
		return fi.Size()
	}
	defer syscall.CloseHandle(h)
	var info [24]byte
	// FileStandardInfo = 1; struct { AllocationSize (8), EndOfFile (8), … }
	r, _, _ := procGetFileInformationByHandleEx.Call(
		uintptr(h), 1, uintptr(unsafe.Pointer(&info[0])), 24)
	if r == 0 {
		return fi.Size()
	}
	alloc := int64(*(*uint64)(unsafe.Pointer(&info[0])))
	if alloc <= 0 {
		return fi.Size()
	}
	return alloc
}