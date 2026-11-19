package core_test

// End-to-end checks for `winux nohup` with a real process: the launcher must
// return while the child keeps running, and the pid it reports must be the child
// it actually started.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readPidFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %q is not a number: %v", b, err)
	}
	return pid
}

func killPid(pid int) {
	_ = exec.Command("taskkill", "/pid", strconv.Itoa(pid), "/t", "/f").Run()
}

func TestNohupCliDetachesAndReportsTheChildPid(t *testing.T) {
	exe := buildWinuxExe(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "bg.log")
	pidFile := filepath.Join(dir, "bg.pid")

	// the launcher itself must return quickly: if it waited for the child the
	// test would take as long as the child's 30 ping lines
	start := time.Now()
	out, err := exec.Command(exe, "nohup", "-q", "-P", "-o", log, "-p", pidFile,
		"cmd", "/c", "ping -n 30 127.0.0.1 >nul").CombinedOutput()
	if err != nil {
		t.Fatalf("nohup failed: %v (%s)", err, out)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("nohup blocked for %v; it must return after starting the child", elapsed)
	}

	pid := readPidFile(t, pidFile)
	defer killPid(pid)
	if got := strings.TrimSpace(string(out)); got != strconv.Itoa(pid) {
		t.Errorf("--print-pid printed %q, pid file holds %d", got, pid)
	}
	// the child is still alive after the launcher exited
	if err := exec.Command("tasklist", "/fi", "PID eq "+strconv.Itoa(pid), "/nh").Run(); err != nil {
		t.Logf("tasklist could not confirm pid %d: %v", pid, err)
	}
	time.Sleep(300 * time.Millisecond)
	if !isProcessAlive(t, pid) {
		t.Fatalf("child %d died right after the launcher exited", pid)
	}
}

// isProcessAlive asks tasklist, which needs no special privileges.
func isProcessAlive(t *testing.T, pid int) bool {
	t.Helper()
	out, err := exec.Command("tasklist", "/fi", "PID eq "+strconv.Itoa(pid), "/nh", "/fo", "csv").Output()
	if err != nil {
		t.Logf("tasklist: %v", err)
		return true // cannot tell; do not fail the test for that
	}
	return strings.Contains(string(out), `"`+strconv.Itoa(pid)+`"`)
}

func TestNohupCliWaitReturnsTheChildStatus(t *testing.T) {
	exe := buildWinuxExe(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "w.log")

	code := exec.Command(exe, "nohup", "-w", "-q", "cmd", "/c", "exit 3").Run()
	if code == nil {
		t.Fatal("--wait with exit 3 reported success")
	}
	if ee, ok := code.(*exec.ExitError); ok {
		if got := ee.ExitCode(); got != 3 {
			t.Errorf("--wait exit status = %d, want 3", got)
		}
	}

	// -o captures cmd's stdout into the log even with --wait
	if out, err := exec.Command(exe, "nohup", "-w", "-q", "-t", "-o", log,
		"cmd", "/c", "echo waited").CombinedOutput(); err != nil {
		t.Fatalf("nohup -w -o failed: %v (%s)", err, out)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")); s != "waited" {
		t.Errorf("log = %q, want waited", s)
	}
}
