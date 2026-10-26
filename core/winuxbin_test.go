//go:build windows

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WINUX_EXE used to mean "installation directory" to winux-activate.cmd and
// "winux executable" to systemctl/sshd. The collision made `systemctl start` fail
// in an activated cmd window with "read D:\my-tools\winux: Incorrect function",
// because the service host was materialized by copying a directory.
func TestWinuxExeFromResolvesBothSpellings(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "winux.exe")
	if err := os.WriteFile(exe, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a directory that does NOT hold winux.exe
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		in, want, why string
	}{
		{exe, exe, "a file path is used as is"},
		{dir, exe, "the installation directory resolves to winux.exe inside it"},
		{empty, empty, "a directory without winux.exe is left for the caller to report"},
		{filepath.Join(dir, "gone.exe"), filepath.Join(dir, "gone.exe"), "a missing file path is reported verbatim"},
		{filepath.Join(dir, "gone"), filepath.Join(dir, "gone"), "a missing directory stays as it was"},
	}
	for _, c := range cases {
		if got := sysWinuxExeFrom(c.in); got != c.want {
			t.Errorf("sysWinuxExeFrom(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

func TestWinuxBinaryHonoursTheOverride(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "winux.exe")
	if err := os.WriteFile(exe, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINUX_EXE", dir)
	if got := sysWinuxBinary(); got != exe {
		t.Fatalf("an activated session (WINUX_EXE=<dir>) must still yield the executable: got %q", got)
	}
}

func TestWinuxBinaryFallsBackToTheRunningExecutable(t *testing.T) {
	t.Setenv("WINUX_EXE", "")
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot resolve the test binary: %v", err)
	}
	if got := sysWinuxBinary(); got != self {
		t.Fatalf("with WINUX_EXE unset the running executable must be used: got %q, want %q", got, self)
	}
}

// The scripts are the other half of the fix: they must keep the two meanings
// apart, or the next installed winux re-creates the bug for every service start.
func TestActivateScriptSeparatesHomeFromExe(t *testing.T) {
	body, err := shimFS.ReadFile("shimdata/winux-activate.cmd")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		`set "WINUX_HOME=%%~fE"`,                 // the directory
		`set "WINUX_EXE=%WINUX_HOME%\winux.exe"`, // the executable
		`set "PATH=%WINUX_SHIMS%;%WINUX_HOME%;%PATH%"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("winux-activate.cmd must keep the install dir and the executable apart; missing %q", want)
		}
	}
	// and it must never put a bare directory into WINUX_EXE again
	if strings.Contains(s, `set "WINUX_EXE=%%~fE"`) {
		t.Error("winux-activate.cmd still sets WINUX_EXE to a directory")
	}

	deact, err := shimFS.ReadFile("shimdata/winux-deactivate.cmd")
	if err != nil {
		t.Fatal(err)
	}
	d := string(deact)
	if !strings.Contains(d, `set "WINUX_HOME="`) {
		t.Error("winux-deactivate.cmd must clear WINUX_HOME")
	}
	if !strings.Contains(d, `"%%~p"=="%WINUX_HOME%"`) {
		t.Error("the PATH fallback must strip the install dir (WINUX_HOME), not only WINUX_EXE")
	}
}
