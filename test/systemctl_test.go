package core_test

// systemctl is exercised on its read-only paths (unit parsing, listing, cat,
// status) against a temporary unit directory injected through
// SYSTEMCTL_UNIT_PATH. Mutating paths need administrator rights and are
// asserted to refuse cleanly when the test is not elevated.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"winux/core"
)

const demoUnit = `[Unit]
Description=Demo service for tests
[Service]
Type=simple
ExecStart=C:\tools\demo.exe --serve --port 8080
WorkingDirectory=C:\tools
Restart=on-failure
Environment=A=1 B="two words"
[Install]
WantedBy=multi-user.target
`

func withUnitDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.service"), []byte(demoUnit), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSTEMCTL_UNIT_PATH", dir)
	return dir
}

func TestSystemctlHelpAndVersion(t *testing.T) {
	code, out, errOut := core.Execute("systemctl", []string{"--help"}, strings.NewReader(""))
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = core.Execute("systemctl", []string{"--version"}, strings.NewReader(""))
	if code != 0 || !strings.Contains(out, core.Version) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestSystemctlCat(t *testing.T) {
	withUnitDir(t)
	code, out, errOut := core.Execute("systemctl", []string{"cat", "demo"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "ExecStart=C:\\tools\\demo.exe --serve --port 8080") {
		t.Fatalf("out=%q", out)
	}
}

func TestSystemctlCatUnknown(t *testing.T) {
	withUnitDir(t)
	code, _, errOut := core.Execute("systemctl", []string{"cat", "nope"}, strings.NewReader(""))
	if code != 4 {
		t.Fatalf("code=%d want 4 stderr=%q", code, errOut)
	}
}

func TestSystemctlListUnitFiles(t *testing.T) {
	withUnitDir(t)
	code, out, errOut := core.Execute("systemctl", []string{"list-unit-files"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "demo.service") || !strings.Contains(out, "static") {
		t.Fatalf("out=%q", out)
	}
}

func TestSystemctlStatusNotInstalled(t *testing.T) {
	withUnitDir(t)
	code, out, errOut := core.Execute("systemctl", []string{"status", "demo"}, strings.NewReader(""))
	if code != 3 {
		t.Fatalf("code=%d want 3 stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "not installed") || !strings.Contains(out, "Demo service for tests") {
		t.Fatalf("out=%q", out)
	}
}

func TestSystemctlStatusUnknownUnit(t *testing.T) {
	withUnitDir(t)
	code, _, errOut := core.Execute("systemctl", []string{"status", "nope"}, strings.NewReader(""))
	if code != 4 {
		t.Fatalf("code=%d want 4 stderr=%q", code, errOut)
	}
}

func TestSystemctlMutatingRefusesWithoutAdmin(t *testing.T) {
	withUnitDir(t)
	// start / stop / enable / disable / daemon-reload need an elevated token;
	// in a non-admin test run they must refuse with exit 1 before touching SCM.
	for _, cmd := range []string{"daemon-reload", "start", "stop", "restart", "enable", "disable", "mask", "unmask"} {
		args := []string{cmd}
		if cmd != "daemon-reload" {
			args = append(args, "demo")
		}
		code, _, errOut := core.Execute("systemctl", args, strings.NewReader(""))
		if code == 0 {
			t.Fatalf("%s succeeded in a non-admin run (code 0); mutating commands must refuse", cmd)
		}
		if !strings.Contains(errOut, "administrator") {
			t.Fatalf("%s stderr=%q (want an administrator hint)", cmd, errOut)
		}
	}
}

func TestSystemctlUnknownCommand(t *testing.T) {
	code, _, errOut := core.Execute("systemctl", []string{"frobnicate"}, strings.NewReader(""))
	if code != 1 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestSystemctlIsActiveUnknown(t *testing.T) {
	code, out, errOut := core.Execute("systemctl", []string{"is-active", "nope"}, strings.NewReader(""))
	if code != 4 {
		t.Fatalf("code=%d want 4 out=%q stderr=%q", code, out, errOut)
	}
}
