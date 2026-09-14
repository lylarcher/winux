//go:build windows

package core

// ConPTY (pseudo console) support: gives an ssh shell session a real Windows
// console, so the winux sh REPL (and any console app) runs interactively over
// the ssh channel. Requires Windows 10 1809+.
//
// x/sys/windows does not export the pseudo-console family, so these are
// declared directly against kernel32 (same style as term_windows.go).

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	procCreatePipe                        = kernel32.NewProc("CreatePipe")
	procCreatePseudoConsole               = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole               = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole                = kernel32.NewProc("ClosePseudoConsole")
	procInitializeProcThreadAttributeList = kernel32.NewProc("InitializeProcThreadAttributeList")
	procDeleteProcThreadAttributeList     = kernel32.NewProc("DeleteProcThreadAttributeList")
	procUpdateProcThreadAttribute         = kernel32.NewProc("UpdateProcThreadAttribute")
	procCreateProcessW                    = kernel32.NewProc("CreateProcessW")
	procWriteFile                         = kernel32.NewProc("WriteFile")
	procReadFile                          = kernel32.NewProc("ReadFile")
	procGetLastError                      = kernel32.NewProc("GetLastError")
)

const (
	procThreadAttributePseudoConsole = 0x00020016
	extendedStartupinfoPresent       = 0x00080000
	startfUseStdHandles              = 0x00000100
	createNewProcessGroup            = 0x00000200
	infinite                         = 0xFFFFFFFF
)

type sshCoord struct {
	X, Y int16
}

type sshStartupInfoEx struct {
	Cb             uint32
	Reserved       uintptr
	Desktop, Title *uint16
	X, Y, XSize    uint32
	YSize, XCount  uint32
	YCount, Attr   uint32
	Flags          uint32
	ShowWindow     uint16
	CbReserved2    uint16
	Reserved2      *byte
	StdInput       uintptr
	StdOutput      uintptr
	StdErr         uintptr
	AttributeList  uintptr
}

type sshProcessInformation struct {
	Process, Thread uintptr
	ProcessID       uint32
	ThreadID        uint32
}

// sshConPTY owns one pseudo console and the child process attached to it.
type sshConPTY struct {
	pc       uintptr
	inR      uintptr // child stdin side (pseudo console input)
	inW      uintptr // we write ssh stdin -> inW
	outR     uintptr // we read child output from outR
	outW     uintptr // child stdout side (pseudo console output)
	pid      uint32
	done     chan struct{}
	col, row uint16
}

// conptyAvailable reports whether CreatePseudoConsole exists (Win10 1809+).
func conptyAvailable() bool {
	return procCreatePseudoConsole.Find() == nil
}

// newSSHConPTY spawns winux sh attached to a fresh pseudo console.
func newSSHConPTY(cols, rows uint16) (*sshConPTY, error) {
	if !conptyAvailable() {
		return nil, fmt.Errorf("pseudo console requires Windows 10 1809+")
	}
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	var inR, inW uintptr
	if err := sshCreatePipe(&inR, &inW); err != nil {
		return nil, err
	}
	var outR, outW uintptr
	if err := sshCreatePipe(&outR, &outW); err != nil {
		procCloseHandle.Call(inR)
		procCloseHandle.Call(inW)
		return nil, err
	}
	var pc uintptr
	// CreatePseudoConsole returns an HRESULT: S_OK is 0, failures are negative.
	// MSDN says hInput is read by the console and hOutput written by it, but
	// empirically (and per microsoft/terminal's ConPTY sample) the API wants
	// the ends the SERVER uses: hInput = the pipe write end we push user input
	// into (inW), hOutput = the pipe read end we drain console output from
	// (outR). size is passed BY VALUE.
	if r, _, _ := procCreatePseudoConsole.Call(
		sshCoordBits(cols, rows), inW, outR, 0, uintptr(unsafe.Pointer(&pc))); int32(r) < 0 {
		procCloseHandle.Call(inR)
		procCloseHandle.Call(inW)
		procCloseHandle.Call(outR)
		procCloseHandle.Call(outW)
		return nil, fmt.Errorf("CreatePseudoConsole failed (hresult 0x%x)", uint32(int32(r)))
	}

	// -i forces the REPL: inside a pseudo console the standard handles are
	// plain pipes, so sh would otherwise read stdin to EOF and never exit.
	cmdline := syscall.StringToUTF16Ptr(`"` + sshChildExe() + `" sh -i`)

	// Proc-thread attribute list carrying the pseudo console handle.
	var attrSize uintptr
	procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&attrSize)))
	attrBuf := make([]byte, attrSize)
	attrList := uintptr(unsafe.Pointer(&attrBuf[0]))
	if r, _, _ := procInitializeProcThreadAttributeList.Call(attrList, 1, 0, uintptr(unsafe.Pointer(&attrSize))); r == 0 {
		procClosePseudoConsole.Call(pc)
		procCloseHandle.Call(inR)
		procCloseHandle.Call(inW)
		procCloseHandle.Call(outR)
		procCloseHandle.Call(outW)
		return nil, fmt.Errorf("InitializeProcThreadAttributeList failed (error %d)", sshLastError())
	}

	if r, _, _ := procUpdateProcThreadAttribute.Call(attrList, 0, procThreadAttributePseudoConsole,
		pc, unsafe.Sizeof(pc), 0, 0); r == 0 {
		procDeleteProcThreadAttributeList.Call(attrList)
		procClosePseudoConsole.Call(pc)
		procCloseHandle.Call(inR)
		procCloseHandle.Call(inW)
		procCloseHandle.Call(outR)
		procCloseHandle.Call(outW)
		return nil, fmt.Errorf("UpdateProcThreadAttribute failed (error %d)", sshLastError())
	}

	var si sshStartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = extendedStartupinfoPresent | startfUseStdHandles
	si.AttributeList = attrList
	si.StdInput = inR
	si.StdOutput = outW
	si.StdErr = outW
	var pi sshProcessInformation
	if r, _, _ := procCreateProcessW.Call(0, uintptr(unsafe.Pointer(cmdline)), 0, 0, 1,
		createNewProcessGroup, 0, 0, uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi))); r == 0 {
		procDeleteProcThreadAttributeList.Call(attrList)
		procClosePseudoConsole.Call(pc)
		procCloseHandle.Call(inR)
		procCloseHandle.Call(inW)
		procCloseHandle.Call(outR)
		procCloseHandle.Call(outW)
		return nil, fmt.Errorf("CreateProcess failed (error %d)", sshLastError())
	}
	procCloseHandle.Call(pi.Thread)
	procDeleteProcThreadAttributeList.Call(attrList)

	pt := &sshConPTY{pc: pc, inR: inR, inW: inW, outR: outR, outW: outW, pid: pi.ProcessID, done: make(chan struct{}), col: cols, row: rows}
	go func() {
		procWaitForSingleObject.Call(pi.Process, infinite)
		procCloseHandle.Call(pi.Process)
		procClosePseudoConsole.Call(pc)
		procCloseHandle.Call(inR)
		procCloseHandle.Call(inW)
		procCloseHandle.Call(outR)
		procCloseHandle.Call(outW)
		close(pt.done)
	}()
	return pt, nil
}

func sshCreatePipe(readH, writeH *uintptr) error {
	var sa syscall.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	if r, _, _ := procCreatePipe.Call(uintptr(unsafe.Pointer(readH)), uintptr(unsafe.Pointer(writeH)),
		uintptr(unsafe.Pointer(&sa)), 0); r == 0 {
		return fmt.Errorf("CreatePipe failed (error %d)", sshLastError())
	}
	return nil
}

func sshLastError() uint32 {
	r, _, _ := procGetLastError.Call()
	return uint32(r)
}

// writeStdin forwards ssh channel stdin bytes into the pseudo console.
func (p *sshConPTY) writeStdin(r io.Reader) error {
	buf := make([]byte, 8192)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			debugLogf("conpty: stdin %d bytes: %q", n, buf[:n])
			if e := p.writeInput(buf[:n]); e != nil {
				debugLogf("conpty: writeInput error: %v", e)
				return e
			}
		}
		if err != nil {
			return err
		}
	}
}

func debugLogf(format string, args ...interface{}) {
	if os.Getenv("WINUX_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// writeInput pushes raw bytes into the pseudo console, retrying on partial
// writes (the console can consume the input pipe slowly).
func (p *sshConPTY) writeInput(data []byte) error {
	off := 0
	for off < len(data) {
		var written uint32
		chunk := data[off:]
		if len(chunk) > 65536 {
			chunk = chunk[:65536]
		}
		if r, _, _ := procWriteFile.Call(p.inW, uintptr(unsafe.Pointer(&chunk[0])),
			uintptr(len(chunk)), uintptr(unsafe.Pointer(&written)), 0); r == 0 {
			return fmt.Errorf("conpty input write failed (error %d)", sshLastError())
		}
		if int(written) == 0 {
			return fmt.Errorf("conpty input write made no progress")
		}
		off += int(written)
	}
	return nil
}

// pipe reads child output and writes it to the ssh channel.
func (p *sshConPTY) pipe(w io.Writer) {
	buf := make([]byte, 8192)
	for {
		var n uint32
		if r, _, _ := procReadFile.Call(p.outR, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)), 0); r == 0 {
			return
		}
		if n > 0 {
			w.Write(buf[:n])
		}
	}
}

// sshCoordBits packs a COORD by VALUE (X low 16 bits, Y high 16 bits): the
// pseudo-console APIs take the size struct by value, not by pointer.
func sshCoordBits(cols, rows uint16) uintptr {
	return uintptr(uint32(rows)<<16 | uint32(cols))
}

func (p *sshConPTY) resize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	procResizePseudoConsole.Call(p.pc, sshCoordBits(cols, rows))
	p.col, p.row = cols, rows
}

// kill terminates the child tree.
func (p *sshConPTY) kill() {
	exec.Command("taskkill", "/pid", fmt.Sprint(p.pid), "/t", "/f").Run()
}
