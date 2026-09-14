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

// The built-in winux sshd unit: `systemctl status sshd` aliases to the
// winux-sshd service and resolves even without a unit file on disk
// (read-only commands must work from a non-admin shell).
func TestSystemctlSshdBuiltinAlias(t *testing.T) {
	withUnitDir(t)
	code, out, errOut := core.Execute("systemctl", []string{"status", "sshd"}, strings.NewReader(""))
	if code != 3 { // resolved but not installed -> inactive
		t.Fatalf("status code=%d want 3 out=%q stderr=%q", code, out, errOut)
	}
	if !strings.Contains(out, "winux-sshd.service") || !strings.Contains(out, "winux SSH server") {
		t.Fatalf("status out=%q (want the winux-sshd built-in unit)", out)
	}

	code, out, errOut = core.Execute("systemctl", []string{"cat", "sshd"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("cat code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "ExecStart=") || !strings.Contains(out, "sshd") {
		t.Fatalf("cat out=%q (want the built-in ExecStart)", out)
	}

	code, out, _ = core.Execute("systemctl", []string{"is-active", "sshd"}, strings.NewReader(""))
	if code != 3 || !strings.Contains(out, "inactive") {
		t.Fatalf("is-active code=%d out=%q (want 3/inactive)", code, out)
	}
}

// systemd's lifecycle rules, as opposed to the SCM's: `restart` never needs a
// prior `start`, and `disable` (which only means "do not start at boot") never
// blocks `start`. Both differences show up as specific SCM errors —
// ERROR_SERVICE_NOT_ACTIVE ("has not been started") and ERROR_SERVICE_DISABLED
// ("because it is disabled") — so the test drives a real unit through
// stop -> restart -> disable -> start and asserts those two never appear.
//
// Installing a service needs the SCM, so the test skips unless it runs
// elevated; the payload is a freshly built winux.exe, because a native service
// has to talk to the service control manager.
func TestSystemctlLifecycleLinuxSemantics(t *testing.T) {
	if !core.IsAdmin() {
		t.Skip("installing a Windows service needs administrator rights")
	}
	exe := buildWinuxExe(t)
	dir := t.TempDir()
	unit := "winuxtest"
	unitPath := filepath.Join(dir, unit+".service")
	content := "[Unit]\nDescription=winux lifecycle test\n[Service]\nType=native\nExecStart=\"" +
		exe + "\" sshd --service --port 0\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(unitPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSTEMCTL_UNIT_PATH", dir)
	t.Cleanup(func() {
		// leave no service, task or state behind
		os.Remove(unitPath)
		core.Execute("systemctl", []string{"stop", unit}, strings.NewReader(""))
		core.Execute("systemctl", []string{"mask", unit}, strings.NewReader(""))
		core.Execute("systemctl", []string{"unmask", unit}, strings.NewReader(""))
		core.Execute("systemctl", []string{"daemon-reload"}, strings.NewReader(""))
	})

	code, out, errOut := core.Execute("systemctl", []string{"start", unit}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("start: code=%d out=%q stderr=%q", code, out, errOut)
	}
	if code, out, errOut = core.Execute("systemctl", []string{"stop", unit}, strings.NewReader("")); code != 0 {
		t.Fatalf("stop: code=%d out=%q stderr=%q", code, out, errOut)
	}
	// stopping twice is not an error either
	if code, out, errOut = core.Execute("systemctl", []string{"stop", unit}, strings.NewReader("")); code != 0 {
		t.Fatalf("second stop: code=%d out=%q stderr=%q", code, out, errOut)
	}

	// restart on a *stopped* unit must start it, not complain about the SCM
	code, out, errOut = core.Execute("systemctl", []string{"restart", unit}, strings.NewReader(""))
	if strings.Contains(errOut, "has not been started") {
		t.Fatalf("restart demanded a prior start: stderr=%q", errOut)
	}
	if code != 0 {
		t.Fatalf("restart on a stopped unit: code=%d out=%q stderr=%q", code, out, errOut)
	}

	if code, out, errOut = core.Execute("systemctl", []string{"disable", unit}, strings.NewReader("")); code != 0 {
		t.Fatalf("disable: code=%d out=%q stderr=%q", code, out, errOut)
	}
	if code, out, _ = core.Execute("systemctl", []string{"is-enabled", unit}, strings.NewReader("")); code == 0 {
		t.Fatalf("unit still reports enabled after disable: %q", out)
	}
	// a disabled unit must still start (and stop) on demand
	core.Execute("systemctl", []string{"stop", unit}, strings.NewReader(""))
	code, out, errOut = core.Execute("systemctl", []string{"start", unit}, strings.NewReader(""))
	if strings.Contains(errOut, "disabled") {
		t.Fatalf("start refused because the unit is disabled: stderr=%q", errOut)
	}
	if code != 0 {
		t.Fatalf("start of a disabled unit: code=%d out=%q stderr=%q", code, out, errOut)
	}
	// ... and it must still be disabled afterwards, exactly like systemd
	if code, out, _ = core.Execute("systemctl", []string{"is-enabled", unit}, strings.NewReader("")); code == 0 {
		t.Fatalf("starting a disabled unit changed its enable state: %q", out)
	}
	if code, out, errOut = core.Execute("systemctl", []string{"stop", unit}, strings.NewReader("")); code != 0 {
		t.Fatalf("stop of a disabled unit: code=%d out=%q stderr=%q", code, out, errOut)
	}
}
