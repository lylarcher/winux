//go:build windows

package core

// Regression tests for the batch activation helpers materialized beside
// winux.exe. The bug that motivated them: an LF-only winux-deactivate.cmd made
// cmd.exe read its multi-line ( ... ) block a couple of bytes off, so
// fragments of the script ran as commands ("rem" -> 'm', "doskey" -> 'skey',
// "if defined" -> 'defined', ") else ..." -> 'else'), and the fallback branch
// emptied PATH because `endlocal & set "PATH=%NP%"` expanded NP after the
// setlocal scope had already been popped.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shimCmdFiles returns the .cmd files planMaterialize would write.
func shimCmdFiles(t *testing.T) map[string]string {
	t.Helper()
	files, _, err := planMaterialize()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for rel, content := range files {
		if strings.EqualFold(filepath.Ext(rel), ".cmd") {
			out[rel] = string(content)
		}
	}
	if len(out) == 0 {
		t.Fatal("planMaterialize produced no .cmd files")
	}
	return out
}

func TestShimCmdFilesAreCRLFAndFlat(t *testing.T) {
	for rel, content := range shimCmdFiles(t) {
		for i := 0; i < len(content); i++ {
			if content[i] != '\n' {
				continue
			}
			if i == 0 || content[i-1] != '\r' {
				t.Fatalf("%s has a bare LF at byte %d; cmd.exe mis-tracks line offsets in LF-only batch files", rel, i)
			}
		}
		// A line that ends with an opening parenthesis starts a multi-line
		// block — the exact construct that desynchronised with LF endings.
		for i, line := range strings.Split(content, "\r\n") {
			trimmed := strings.TrimRight(line, " \t")
			if strings.HasSuffix(trimmed, "(") {
				t.Errorf("%s:%d opens a multi-line block (%q); keep batch helpers flat", rel, i+1, trimmed)
			}
		}
	}
}

// probeDeactivate materializes the generated deactivate helper next to a probe
// batch that runs it and echoes the resulting environment, then executes it.
func probeDeactivate(t *testing.T, env []string) string {
	t.Helper()
	dir := t.TempDir()
	files, _, err := planMaterialize()
	if err != nil {
		t.Fatal(err)
	}
	shim := files[filepath.Join("cmd", "winux-deactivate.cmd")]
	if len(shim) == 0 {
		t.Fatal("winux-deactivate.cmd missing from the materialized set")
	}
	if err := os.WriteFile(filepath.Join(dir, "winux-deactivate.cmd"), shim, 0o644); err != nil {
		t.Fatal(err)
	}
	probe := "@echo off\r\n" +
		"call \"%~dp0winux-deactivate.cmd\"\r\n" +
		"echo PATHNOW=%PATH%\r\n" +
		"echo ACTIVENOW=[%WINUX_ACTIVE%]\r\n" +
		"echo PROMPTNOW=%PROMPT%\r\n"
	path := filepath.Join(dir, "probe.cmd")
	if err := os.WriteFile(path, []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/c", path)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestDeactivateShimRestoresPathAndClearsMarkers(t *testing.T) {
	oldPath := `C:\Windows\System32;C:\Windows;D:\tools`
	env := []string{
		"SystemRoot=" + os.Getenv("SystemRoot"),
		"PATH=C:\\shims\\;C:\\winux;" + oldPath,
		"WINUX_ACTIVE=1",
		`WINUX_SHIMS=C:\shims\`,
		`WINUX_HOME=C:\winux`,
		`WINUX_EXE=C:\winux\winux.exe`,
		"WINUX_OLD_PATH=" + oldPath,
		`WINUX_OLD_PROMPT=$P$G`,
		`PROMPT=(winux) $P$G`,
	}
	out := probeDeactivate(t, env)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "PATHNOW="):
			if got := strings.TrimPrefix(line, "PATHNOW="); got != oldPath {
				t.Errorf("deactivate restored PATH=%q, want %q\nfull output:\n%s", got, oldPath, out)
			}
		case line == "ACTIVENOW=[]":
			// expected: WINUX_ACTIVE cleared
		case strings.HasPrefix(line, "ACTIVENOW="):
			t.Errorf("WINUX_ACTIVE was not cleared: %q", line)
		case strings.HasPrefix(line, "PROMPTNOW="):
			if got := strings.TrimPrefix(line, "PROMPTNOW="); got != `$P$G` {
				t.Errorf("PROMPT not restored: %q", got)
			}
		}
	}
	// no fragment of the script may run as a command
	for _, bad := range []string{"'m'", "'skey'", "not recognized", "不是内部"} {
		if strings.Contains(out, bad) {
			t.Errorf("deactivate printed %q — the batch parser lost sync:\n%s", bad, out)
		}
	}
}

// TestDeactivateShimFallbackKeepsPath covers the branch taken when the saved
// PATH is gone: the shims/install entries are stripped and PATH must never end
// up empty (the old `endlocal & set "PATH=%NP%"` bug).
func TestDeactivateShimFallbackKeepsPath(t *testing.T) {
	env := []string{
		"SystemRoot=" + os.Getenv("SystemRoot"),
		`PATH=C:\shims\;C:\winux;C:\Windows\System32;C:\Program Files\Git\cmd;D:\tools`,
		"WINUX_ACTIVE=1",
		`WINUX_SHIMS=C:\shims\`,
		`WINUX_HOME=C:\winux`,
		`WINUX_EXE=C:\winux\winux.exe`,
	}
	out := probeDeactivate(t, env)
	// the shims (with trailing backslash) and install dirs go, everything else
	// stays — including an entry containing spaces
	want := `C:\Windows\System32;C:\Program Files\Git\cmd;D:\tools`
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "PATHNOW=") {
			continue
		}
		found = true
		if got := strings.TrimPrefix(line, "PATHNOW="); got != want {
			t.Errorf("fallback PATH=%q, want %q\nfull output:\n%s", got, want, out)
		}
	}
	if !found {
		t.Fatalf("probe printed no PATHNOW line:\n%s", out)
	}
}
