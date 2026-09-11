//go:build windows

package core

// The ps backend snapshots processes through the native Win32_Process class
// (PowerShell is part of Windows, so no third-party tool is involved) and
// resolves each owner account from the process token. Command lines come
// straight from the CIM provider, which is far more reliable than reading the
// remote PEB for 32-bit and protected processes.

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var (
	physOnce sync.Once
	physKB   uint64
)

// physMemoryKB returns the total installed physical memory in KB (cached).
func physMemoryKB() uint64 {
	physOnce.Do(func() {
		var st memoryStatusEx
		st.Length = uint32(unsafe.Sizeof(st))
		if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st))); r != 0 && st.TotalPhys > 0 {
			physKB = st.TotalPhys / 1024
		}
	})
	return physKB
}

// cimProc mirrors the Win32_Process fields we request.
type cimProc struct {
	ProcessId       int32  `json:"ProcessId"`
	ParentProcessId int32  `json:"ParentProcessId"`
	Name            string `json:"Name"`
	CommandLine     string `json:"CommandLine"`
	CreationDate    string `json:"CreationDate"`
	WorkingSetSize  uint64 `json:"WorkingSetSize"`
	VirtualSize     uint64 `json:"VirtualSize"`
	UserModeTime    int64  `json:"UserModeTime"`
	KernelModeTime  int64  `json:"KernelModeTime"`
	SessionId       int32  `json:"SessionId"`
	Priority        int32  `json:"Priority"`
	ThreadCount     int32  `json:"ThreadCount"`
}

func snapshotProcesses() ([]procInfo, error) {
	ps := systemExe("WindowsPowerShell\\v1.0\\powershell.exe")
	script := "Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine,CreationDate,WorkingSetSize,VirtualSize,UserModeTime,KernelModeTime,SessionId,Priority,ThreadCount | ConvertTo-Json -Compress"
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cannot query processes: %v", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, fmt.Errorf("cannot query processes: empty response from the CIM provider")
	}
	if !strings.HasPrefix(s, "[") {
		s = "[" + s + "]"
	}
	var raw []cimProc
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("cannot parse process data: %v", err)
	}
	procs := make([]procInfo, 0, len(raw))
	for _, r := range raw {
		p := procInfo{
			pid:      int(r.ProcessId),
			ppid:     int(r.ParentProcessId),
			name:     r.Name,
			cmdline:  strings.TrimSpace(r.CommandLine),
			userTime: time.Duration(r.UserModeTime) * 100, // CIM reports 100 ns ticks
			kernTime: time.Duration(r.KernelModeTime) * 100,
			virtKB:   r.VirtualSize / 1024,
			rssKB:    r.WorkingSetSize / 1024,
			threads:  int(r.ThreadCount),
			prio:     int(r.Priority),
		}
		if t, err := parseCimDate(r.CreationDate); err == nil {
			p.create = t
		}
		p.user = processOwner(p.pid)
		procs = append(procs, p)
	}
	return procs, nil
}

// parseCimDate parses WMI's CreationDate (e.g. 20250102030405.123456+480);
// the wall time is local, so the offset suffix is irrelevant here.
func parseCimDate(s string) (time.Time, error) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return time.ParseInLocation("20060102150405", s, time.Local)
}

// processOwner resolves the account that owns a process from its token,
// returning "?" when the process cannot be opened or the lookup fails.
func processOwner(pid int) string {
	h, err := syscall.OpenProcess(0x1000 /* PROCESS_QUERY_LIMITED_INFORMATION */, false, uint32(pid))
	if err != nil {
		return "?"
	}
	defer syscall.CloseHandle(h)
	var tok syscall.Handle
	r, _, _ := procOpenProcessToken.Call(uintptr(h), 0x0008 /* TOKEN_QUERY */, uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		return "?"
	}
	defer syscall.CloseHandle(tok)
	var sz uint32
	procGetTokenInformation.Call(uintptr(tok), 1 /* TokenUser */, 0, 0, uintptr(unsafe.Pointer(&sz)))
	if sz == 0 {
		return "?"
	}
	buf := make([]byte, sz)
	r, _, _ = procGetTokenInformation.Call(uintptr(tok), 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&sz)))
	if r == 0 {
		return "?"
	}
	// TOKEN_USER begins with SID_AND_ATTRIBUTES{ PSID Sid; ... }
	sid := *(*uintptr)(unsafe.Pointer(&buf[0]))
	if sid == 0 {
		return "?"
	}
	return sidToName(sid)
}

// sidToName resolves a SID pointer to DOMAIN\name (name alone for the local
// built-in accounts).
func sidToName(sid uintptr) string {
	var name, domain [256]uint16
	nameLen, domainLen := uint32(len(name)), uint32(len(domain))
	var sidType uint32
	r, _, _ := procLookupAccountSid.Call(
		0, sid, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&nameLen)),
		uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(&domainLen)), uintptr(unsafe.Pointer(&sidType)))
	if r == 0 {
		return "?"
	}
	n := syscall.UTF16ToString(name[:nameLen])
	d := syscall.UTF16ToString(domain[:domainLen])
	if d != "" && !strings.EqualFold(d, "BUILTIN") {
		return d + `\` + n
	}
	return n
}
