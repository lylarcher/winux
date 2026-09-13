//go:build windows

package core

// `winux activate` / `winux deactivate` activate or deactivate winux in the
// current terminal from anywhere — the new primary way to use the tool once
// winux.exe is on PATH (no install.ps1 needed). A child process cannot change
// the parent shell's environment, so these commands print the one-liner that
// makes the parent shell do the work:
//
//	PowerShell :  winux activate | iex          (deactivate: winux deactivate | iex)
//	cmd.exe    :  for /f "delims=" %l in ('winux activate') do @%l
//
// The shell is detected from the parent process image name (with a
// PSModulePath fallback); --shell overrides it. The printed line simply
// dot-sources winux-activate.ps1 / calls cmd\winux-activate.cmd, keeping a
// single source of truth for the actual activation logic. The helper files are
// winux- prefixed on purpose: bin\ may sit on the system PATH, and a bare
// activate.ps1/activate.cmd name there could collide with other toolchains.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func init() {
	addCmd("activate", "activate winux in the current shell (prints the one-liner to eval: PowerShell `winux activate | iex`, cmd `for /f \"delims=\" %l in ('winux activate') do @%l`)", cmdActivate)
	addCmd("deactivate", "deactivate winux in the current shell (prints the one-liner to eval)", cmdDeactivate)
}

var (
	procNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
	procQueryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

type processBasicInformation struct {
	Reserved1                    uintptr
	PebBaseAddress               uintptr
	Reserved2                    [2]uintptr
	UniqueProcessId              uintptr
	InheritedFromUniqueProcessId uintptr
}

// parentShellName detects the real shell that launched winux. Because the
// winux.cmd / winux-activate.cmd shims run winux.exe from a transient cmd.exe
// (typing `winux ...` at a PowerShell prompt resolves to cmd\winux.cmd), we
// walk up to the grandparent: a cmd.exe whose own parent is PowerShell is the
// shim wrapper, not the user's shell.
func parentShellName() string {
	parent := parentPID(0) // 0 = current process
	if parent == 0 {
		return ""
	}
	switch name := processImageName(parent); {
	case strings.HasPrefix(name, "powershell"), strings.HasPrefix(name, "pwsh"):
		return "powershell"
	case name == "cmd.exe":
		gp := parentPID(parent)
		if gp != 0 {
			g := processImageName(gp)
			if strings.HasPrefix(g, "powershell") || strings.HasPrefix(g, "pwsh") {
				return "powershell"
			}
		}
		return "cmd"
	}
	return ""
}

// parentPID returns the parent process id of pid (0 = the current process).
func parentPID(pid uintptr) uintptr {
	h := ^uintptr(0) // NtQueryInformationProcess pseudo-handle for self
	if pid != 0 {
		ph, _, _ := procOpenProcess.Call(0x1000, 0, pid) // PROCESS_QUERY_LIMITED_INFORMATION
		if ph == 0 {
			return 0
		}
		defer procCloseHandle.Call(ph)
		h = ph
	}
	var pbi processBasicInformation
	st, _, _ := procNtQueryInformationProcess.Call(
		h,
		0, // ProcessBasicInformation
		uintptr(unsafe.Pointer(&pbi)),
		uintptr(unsafe.Sizeof(pbi)),
		0,
	)
	if st != 0 {
		return 0
	}
	return pbi.InheritedFromUniqueProcessId
}

// processImageName returns the lower-cased image base name of pid.
func processImageName(pid uintptr) string {
	h, _, _ := procOpenProcess.Call(0x1000, 0, pid) // PROCESS_QUERY_LIMITED_INFORMATION
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 512)
	n := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:n])))
}

func detectShell() string {
	if s := parentShellName(); s != "" {
		return s
	}
	// fallback: PowerShell sets PSModulePath for its children, cmd does not
	if os.Getenv("PSModulePath") != "" {
		return "powershell"
	}
	return "cmd"
}

func cmdActivate(args []string) int {
	return emitActivate(args, "activate", "winux-activate.ps1", filepath.Join("cmd", "winux-activate.cmd"))
}

func cmdDeactivate(args []string) int {
	return emitActivate(args, "deactivate", "winux-deactivate.ps1", filepath.Join("cmd", "winux-deactivate.cmd"))
}

func emitActivate(args []string, name, psFile, cmdFile string) int {
	f := NewFlags(
		&Flag{Name: "shell", Long: "shell", HasArg: true, ArgName: "auto|powershell|cmd", Help: "which shell to target (default: detect the parent shell)"},
	)
	if !parseOrDie(name, f, args) {
		return 2
	}
	if handleHelpVersion(name, "[--shell auto|powershell|cmd]", f) {
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		warn(name, "%v", err)
		return 1
	}
	dir := filepath.Dir(exe)
	// The .cmd shims / .ps1 helpers are embedded in winux.exe and materialized
	// here (md5-incremental) so the one-liner below points at files that exist,
	// even if winux.exe was copied somewhere without its cmd\ directory.
	if err := ensureShims(); err != nil {
		warn(name, "%v", err)
		return 1
	}
	shell := f.Str("shell")
	if shell == "" {
		shell = detectShell()
	}
	switch shell {
	case "powershell":
		p := filepath.Join(dir, psFile)
		if !fileExists(p) {
			warn(name, "%s not found - run build.ps1 first", p)
			return 1
		}
		fmt.Printf(". '%s'\n", strings.ReplaceAll(p, "'", "''"))
	case "cmd":
		p := filepath.Join(dir, cmdFile)
		if !fileExists(p) {
			warn(name, "%s not found - run build.ps1 first", p)
			return 1
		}
		fmt.Printf("call \"%s\"\n", p)
	default:
		warn(name, "unknown shell '%s' (use powershell or cmd)", shell)
		return 1
	}
	if isTerminal(os.Stdout) {
		if shell == "powershell" {
			fmt.Fprintf(os.Stderr, "提示: 完成%s请执行  winux %s | iex\n", name, name)
		} else {
			// cmd has no `iex`: the printed `call ...` line can be pasted as-is,
			// and since winux-activate.cmd / winux-deactivate.cmd are also
			// materialized next to winux.exe (the dir on PATH), typing the short
			// name works too.
			fmt.Fprintf(os.Stderr, "提示: 复制上一行执行，或直接输入 winux-%s\n", name)
		}
	}
	return 0
}
