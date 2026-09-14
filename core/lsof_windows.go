//go:build windows

package core

// lsof needs two native facts per process: the image path (the "txt" entry)
// and the owner account. Everything comes from Win32 directly — psapi's
// EnumProcesses for the pid list and QueryFullProcessImageName plus the
// process token for the details — because lsof touches every process on the
// machine and the PowerShell/CIM snapshot `ps` uses would be far too slow.

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	psapi             = syscall.NewLazyDLL("psapi.dll")
	procEnumProcesses = psapi.NewProc("EnumProcesses")
)

// lsofPIDs returns every live process id (pid 0, the idle process, is left out).
func lsofPIDs() ([]int, error) {
	const maxPIDs = 16384
	buf := make([]uint32, maxPIDs)
	var needed uint32
	r, _, err := procEnumProcesses.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)*4),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r == 0 {
		return nil, fmt.Errorf("EnumProcesses failed: %v", err)
	}
	n := int(needed / 4)
	if n > len(buf) {
		n = len(buf)
	}
	out := make([]int, 0, n)
	for _, pid := range buf[:n] {
		if pid != 0 {
			out = append(out, int(pid))
		}
	}
	return out, nil
}

// lsofProcInfo returns the image base name, the full image path and the owner
// account of pid. The path is empty when the process cannot be opened (System,
// protected and elevated-other-user processes); the name is then a placeholder
// so the row still carries something meaningful.
func lsofProcInfo(pid int) (name, path, user string) {
	user = processOwner(pid)
	if user == "?" && pid == 4 {
		user = "SYSTEM" // the System process cannot be opened, but it is SYSTEM
	}
	h, err := syscall.OpenProcess(0x1000 /* PROCESS_QUERY_LIMITED_INFORMATION */, false, uint32(pid))
	if err != nil {
		return lsofSpecialName(pid), "", user
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := procQueryFullProcessImageName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r != 0 && n > 0 {
		path = syscall.UTF16ToString(buf[:n])
	}
	if path == "" {
		return lsofSpecialName(pid), "", user
	}
	return filepath.Base(path), path, user
}

// lsofSpecialName names the processes Windows never lets a user-mode tool open.
func lsofSpecialName(pid int) string {
	switch pid {
	case 0:
		return "System Idle Process"
	case 4:
		return "System"
	}
	return "?"
}
