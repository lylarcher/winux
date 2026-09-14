//go:build windows

package core

// systemUptime and activeSessionCount on Windows: the boot clock comes from
// GetTickCount64 (no admin needed); the "N user(s)" column counts the active
// console/RDP sessions via the Terminal Services enumeration API.

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	procGetTickCount64        = kernel32.NewProc("GetTickCount64")
	procWTSEnumerateSessionsW = wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSFreeMemory         = wtsapi32.NewProc("WTSFreeMemory")
)

// wtsSessionInfoW mirrors WTS_SESSION_INFOW (session id, station name, state).
type wtsSessionInfoW struct {
	SessionID      uint32
	WinStationName [32]uint16
	State          uint32
}

// WTS_CONNECTSTATE_CLASS values we count as "logged in".
const (
	wtsActive    = 0
	wtsConnected = 1
)

func systemUptime() time.Duration {
	ms, _, _ := procGetTickCount64.Call()
	return time.Duration(ms) * time.Millisecond
}

// activeSessionCount returns the number of active console/RDP sessions
// (session 0, the services session, is excluded like GNU counts real users).
func activeSessionCount() int {
	var pp *wtsSessionInfoW
	var count uint32
	r, _, _ := procWTSEnumerateSessionsW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		0, // reserved
		1, // WTS_CURRENT_SESSION_VERSION_1
		uintptr(unsafe.Pointer(&pp)),
		uintptr(unsafe.Pointer(&count)),
	)
	if r == 0 || pp == nil {
		return 0
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(pp)))
	n := 0
	size := unsafe.Sizeof(wtsSessionInfoW{})
	for i := uint32(0); i < count; i++ {
		s := (*wtsSessionInfoW)(unsafe.Add(unsafe.Pointer(pp), uintptr(i)*size))
		if s.SessionID != 0 && (s.State == wtsActive || s.State == wtsConnected) {
			n++
		}
	}
	return n
}

var wtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")
