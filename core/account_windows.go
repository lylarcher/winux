//go:build windows

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	advapi32                     = syscall.NewLazyDLL("advapi32.dll")
	procOpenProcessToken         = advapi32.NewProc("OpenProcessToken")
	procGetTokenInformation      = advapi32.NewProc("GetTokenInformation")
	procGetFileSecurity          = advapi32.NewProc("GetFileSecurityW")
	procGetSecurityDescriptorOwner = advapi32.NewProc("GetSecurityDescriptorOwner")
	procLookupAccountSid         = advapi32.NewProc("LookupAccountSidW")
	procLookupAccountName        = advapi32.NewProc("LookupAccountNameW")
)

const (
	tokenQuery              = 0x0008
	tokenElevation          = 20 // TokenElevation
	ownerSecurityInformation = 0x00000001
)

// isElevated reports whether the current process runs with an elevated
// (Administrator) token. Account-management commands use this to decide
// whether they can act directly or must re-run themselves through UAC.
func isElevated() bool {
	h, _ := syscall.GetCurrentProcess()
	var tok syscall.Handle
	r, _, _ := procOpenProcessToken.Call(
		uintptr(h), tokenQuery, uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		return false
	}
	defer syscall.CloseHandle(tok)
	var elev uint32
	var n uint32
	r, _, _ = procGetTokenInformation.Call(
		uintptr(tok), tokenElevation, uintptr(unsafe.Pointer(&elev)), unsafe.Sizeof(elev), uintptr(unsafe.Pointer(&n)))
	return r != 0 && elev != 0
}

// systemExe returns the full path of an executable shipped with Windows.
func systemExe(name string) string {
	p := filepath.Join(os.Getenv("SystemRoot"), "System32", name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return name
}

// runNative runs an external Windows tool (net.exe, icacls.exe, ...) with its
// standard streams attached, returning the exit code.
func runNative(args ...string) int {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		warn(args[0], "%v", err)
		return 1
	}
	return 0
}

// queryNative runs an external tool and returns its combined output; the error
// is non-nil when the exit code is non-zero (used for existence checks such as
// `net user NAME`, which exits 0 only for existing accounts).
func queryNative(args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// fileOwner returns the account name that owns path, in DOMAIN\name form when
// the account has a domain. Used by `chown --reference`.
func fileOwner(path string) (string, error) {
	pp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var size uint32
	procGetFileSecurity.Call(
		uintptr(unsafe.Pointer(pp)), ownerSecurityInformation, 0, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return "", syscall.GetLastError()
	}
	buf := make([]byte, size)
	r, _, e := procGetFileSecurity.Call(
		uintptr(unsafe.Pointer(pp)), ownerSecurityInformation,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return "", e
	}
	var owner uintptr
	var defaulted bool
	r, _, e = procGetSecurityDescriptorOwner.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&owner)), uintptr(unsafe.Pointer(&defaulted)))
	if r == 0 || owner == 0 {
		return "", e
	}
	var name, domain [256]uint16
	nameLen, domainLen := uint32(len(name)), uint32(len(domain))
	var sidType uint32
	r, _, e = procLookupAccountSid.Call(
		0, owner, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&nameLen)),
		uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(&domainLen)), uintptr(unsafe.Pointer(&sidType)))
	if r == 0 {
		return "", e
	}
	n := syscall.UTF16ToString(name[:nameLen])
	d := syscall.UTF16ToString(domain[:domainLen])
	if d != "" && !strings.EqualFold(d, "BUILTIN") {
		return d + `\` + n, nil
	}
	return n, nil
}

// accountLookup resolves an account name (local or domain) to the RID — the
// last subauthority of its SID, the closest thing Windows has to a uid — and
// the account's canonical short name (case-corrected via SID->name lookup).
// Local accounts get RIDs starting at 1000 (the first-created account is
// 1000), so this is both real and matches the conventional value winux reports
// for the current user. The lookup is read-only and needs no elevation.
func accountLookup(name string) (rid uint32, canonical string, ok bool) {
	uname, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, name, false
	}
	var sidSize, domainSize, sidType uint32
	// First call with empty buffers asks for the required sizes; it returns
	// FALSE with ERROR_INSUFFICIENT_BUFFER, which is the expected path.
	procLookupAccountName.Call(
		0, uintptr(unsafe.Pointer(uname)), 0, uintptr(unsafe.Pointer(&sidSize)),
		0, uintptr(unsafe.Pointer(&domainSize)), uintptr(unsafe.Pointer(&sidType)))
	if sidSize == 0 {
		return 0, name, false
	}
	sid := make([]byte, sidSize)
	domain := make([]uint16, domainSize)
	if len(domain) == 0 {
		domain = make([]uint16, 1)
	}
	r, _, _ := procLookupAccountName.Call(
		0, uintptr(unsafe.Pointer(uname)), uintptr(unsafe.Pointer(&sid[0])), uintptr(unsafe.Pointer(&sidSize)),
		uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(&domainSize)), uintptr(unsafe.Pointer(&sidType)))
	if r == 0 {
		return 0, name, false
	}
	// SID layout: [0] revision, [1] subauthority count, [2..7] 48-bit
	// authority, then count 32-bit little-endian subauthorities; the RID is
	// the last one.
	count := int(sid[1])
	if count == 0 || len(sid) < 8+count*4 {
		return 0, name, false
	}
	off := 8 + (count-1)*4
	rid = uint32(sid[off]) | uint32(sid[off+1])<<8 | uint32(sid[off+2])<<16 | uint32(sid[off+3])<<24
	// Best effort: reverse the SID to the canonical name (correct case).
	canonical = name
	if n, err := sidAccountName(uintptr(unsafe.Pointer(&sid[0]))); err == nil && n != "" {
		canonical = n
	}
	return rid, canonical, true
}

// sidAccountName resolves a SID back to its short account name, mirroring the
// domain stripping fileOwner applies (a local account comes back as
// MACHINE\name, which is shortened to name).
func sidAccountName(sid uintptr) (string, error) {
	var name, domain [256]uint16
	nameLen, domainLen := uint32(len(name)), uint32(len(domain))
	var sidType uint32
	r, _, e := procLookupAccountSid.Call(
		0, sid, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&nameLen)),
		uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(&domainLen)), uintptr(unsafe.Pointer(&sidType)))
	if r == 0 {
		return "", e
	}
	n := syscall.UTF16ToString(name[:nameLen])
	d := syscall.UTF16ToString(domain[:domainLen])
	if d != "" && !strings.EqualFold(d, "BUILTIN") && !strings.EqualFold(d, "NT AUTHORITY") {
		return shortUserName(d + `\` + n), nil
	}
	return n, nil
}
