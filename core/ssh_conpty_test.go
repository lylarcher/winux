//go:build windows

package core

// Direct ConPTY smoke test: spawn winux sh in a pseudo console, feed it a
// script, and verify the echoed output comes back through the output pipe.

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// tryConPTY builds a pseudo console with the given handle ends for
// (hInput, hOutput) and reports whether creation succeeded.
func tryConPTY(hInput, hOutput uintptr) (uintptr, error) {
	var pc uintptr
	if r, _, _ := procCreatePseudoConsole.Call(
		sshCoordBits(80, 24), hInput, hOutput, 0, uintptr(unsafe.Pointer(&pc))); int32(r) < 0 {
		return 0, fmt.Errorf("CreatePseudoConsole failed (hresult 0x%x)", uint32(int32(r)))
	}
	return pc, nil
}

func TestSSHConPTYHandleDirections(t *testing.T) {
	var inR, inW uintptr
	if err := sshCreatePipe(&inR, &inW); err != nil {
		t.Fatal(err)
	}
	var outR, outW uintptr
	if err := sshCreatePipe(&outR, &outW); err != nil {
		t.Fatal(err)
	}
	defer procCloseHandle.Call(inR)
	defer procCloseHandle.Call(inW)
	defer procCloseHandle.Call(outR)
	defer procCloseHandle.Call(outW)

	for _, combo := range []struct {
		name            string
		hInput, hOutput uintptr
	}{
		{"inR/outW (MSDN)", inR, outW},
		{"inW/outR (sample)", inW, outR},
		{"inR/outR", inR, outR},
		{"inW/outW", inW, outW},
	} {
		pc, err := tryConPTY(combo.hInput, combo.hOutput)
		if err != nil {
			t.Logf("%-18s FAIL: %v", combo.name, err)
		} else {
			t.Logf("%-18s OK", combo.name)
			procClosePseudoConsole.Call(pc)
		}
	}
}

func TestSSHConPTYRoundtrip(t *testing.T) {
	if !conptyAvailable() {
		t.Skip("pseudo console not available (need Windows 10 1809+)")
	}
	// The ConPTY child must be the real winux.exe, not the test binary.
	exe := filepath.Join(t.TempDir(), "winux.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "..").CombinedOutput(); err != nil {
		t.Skipf("cannot build winux.exe: %v (%s)", err, out)
	}
	t.Setenv("WINUX_EXE", exe)
	// Mimic the sshd structure: everything happens in goroutines.
	type res struct {
		pt  *sshConPTY
		err error
	}
	rc := make(chan res, 1)
	go func() {
		pt, err := newSSHConPTY(80, 24)
		rc <- res{pt, err}
	}()
	r := <-rc
	if r.err != nil {
		t.Fatalf("newSSHConPTY: %v", r.err)
	}
	pt := r.pt
	var out bytes.Buffer
	go pt.pipe(&out)

	done := make(chan struct{})
	go func() {
		pt.writeInput([]byte("echo conpty-roundtrip-ok\r\n"))
		time.Sleep(1200 * time.Millisecond)
		pt.writeInput([]byte("exit\r\n"))
		close(done)
	}()
	select {
	case <-pt.done:
	case <-time.After(15 * time.Second):
		pt.kill()
		t.Fatalf("conpty child did not exit; output so far: %q", out.String())
	}
	<-done
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), "conpty-roundtrip-ok") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "conpty-roundtrip-ok") {
		t.Fatalf("output=%q", out.String())
	}
}
