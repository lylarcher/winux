//go:build windows

package core

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	ntdll                          = syscall.NewLazyDLL("ntdll.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procSetConsoleCP               = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP         = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procGetConsoleWindow           = kernel32.NewProc("GetConsoleWindow")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procWaitForSingleObject        = kernel32.NewProc("WaitForSingleObject")
	procRtlGetVersion              = ntdll.NewProc("RtlGetVersion")
)

const (
	enableProcessedOutput           = 0x0001
	enableWrapAtEolOutput           = 0x0002
	enableVirtualTerminalProcessing = 0x0004
	enableProcessedInput            = 0x0001
	enableLineInput                 = 0x0002
	enableEchoInput                 = 0x0004
	enableVirtualTerminalInput      = 0x0200
)

// initConsole switches the attached console to UTF-8 and enables ANSI/VT
// escape sequence handling, so the Linux-style tools below render correctly
// both in cmd.exe and in PowerShell (including Windows PowerShell 5.1).
func initConsole() {
	if !hasConsole() {
		return
	}
	procSetConsoleCP.Call(65001)
	procSetConsoleOutputCP.Call(65001)
	enableVTOutput(os.Stdout)
	enableVTOutput(os.Stderr)
}

func hasConsole() bool {
	r, _, _ := procGetConsoleWindow.Call()
	return r != 0
}

func consoleMode(f *os.File) (uint32, bool) {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return 0, false
	}
	return mode, true
}

func enableVTOutput(f *os.File) {
	mode, ok := consoleMode(f)
	if !ok {
		return
	}
	procSetConsoleMode.Call(f.Fd(), uintptr(mode|enableVirtualTerminalProcessing|enableProcessedOutput))
}

// isTerminal reports whether the file is attached to a Windows console.
func isTerminal(f *os.File) bool {
	_, ok := consoleMode(f)
	return ok
}

// consoleInput returns a handle that can read keystrokes even when stdin has
// been redirected, e.g. `type app.log | less`.
func consoleInput() (*os.File, func()) {
	if isTerminal(os.Stdin) {
		return os.Stdin, func() {}
	}
	f, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return os.Stdin, func() {}
	}
	return f, func() { f.Close() }
}

// enterRawMode switches the console to character-at-a-time input with VT
// escape sequences enabled and returns a function restoring the old mode.
func enterRawMode(in *os.File) func() {
	mode, ok := consoleMode(in)
	if !ok {
		return func() {}
	}
	newMode := mode &^ uint32(enableProcessedInput|enableLineInput|enableEchoInput)
	newMode |= enableVirtualTerminalInput
	procSetConsoleMode.Call(in.Fd(), uintptr(newMode))
	enableVTOutput(os.Stdout)
	return func() { procSetConsoleMode.Call(in.Fd(), uintptr(mode)) }
}

type coord struct{ X, Y int16 }
type smallRect struct{ Left, Top, Right, Bottom int16 }
type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

// terminalSize returns the visible window size in characters.
func terminalSize() (rows, cols int) {
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 24, 80
	}
	rows = int(info.Window.Bottom-info.Window.Top) + 1
	cols = int(info.Window.Right-info.Window.Left) + 1
	if rows < 2 || cols < 2 {
		return 24, 80
	}
	return rows, cols
}

// waitInput waits up to ms milliseconds for console input to arrive; it lets
// the pagers tell a bare Escape key apart from an escape sequence.
func waitInput(f *os.File, ms int) bool {
	r, _, _ := procWaitForSingleObject.Call(f.Fd(), uintptr(uint32(ms)))
	return r == 0 // WAIT_OBJECT_0
}

// processAlive reports whether a process id is still running (used by
// `tail -f --pid=PID`).
func processAlive(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(uint32(pid)))
	if h == 0 {
		return false
	}
	procCloseHandle.Call(h)
	return true
}

type osVersionInfoExW struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformID        uint32
	CSDVersion        [128]uint16
	ServicePackMajor  uint16
	ServicePackMinor  uint16
	SuiteMask         uint16
	ProductType       byte
	Reserved          byte
}

// osVersion returns the real NT version, e.g. "10.0.22631".
func osVersion() string {
	var vi osVersionInfoExW
	vi.OSVersionInfoSize = uint32(unsafe.Sizeof(vi))
	r, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi)))
	if r != 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d.%d.%d", vi.MajorVersion, vi.MinorVersion, vi.BuildNumber)
}
