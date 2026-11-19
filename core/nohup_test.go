//go:build windows

package core

// nohup tests. The command spans three things worth testing separately: the
// option boundary (everything after COMMAND belongs to the child), the exit
// statuses GNU defines (125/126/127 plus the child's own), and the actual
// Windows detachment — a background child must be alive, out of this console,
// and still there after the launcher is gone.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
)

var procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")

// consoleProcessIDs lists the processes attached to this console (empty when
// the test runs without one, e.g. under a service).
func consoleProcessIDs() []uint32 {
	buf := make([]uint32, 256)
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || n > uintptr(len(buf)) {
		return nil
	}
	return buf[:n]
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %q does not hold a number: %v", b, err)
	}
	return pid
}

func killPID(t *testing.T, pid int) {
	t.Helper()
	_ = exec.Command("taskkill", "/pid", strconv.Itoa(pid), "/t", "/f").Run()
}

// The first non-option word starts the child command line, so its own options
// (here -f) are never reported as nohup options.
func TestNohupOptionsStopAtTheCommand(t *testing.T) {
	f := NewFlags(
		&Flag{Name: "output", Short: 'o', Long: "output", HasArg: true, ArgName: "FILE"},
		&Flag{Name: "wait", Short: 'w', Long: "wait"},
		&Flag{Name: "env", Short: 'e', Long: "env", HasArg: true, ArgName: "NAME=VALUE"},
	)
	if err := f.ParseUntilCommand([]string{"-o", "app.log", "-w", "tail", "-f", "-n", "5", "app.log"}); err != nil {
		t.Fatalf("ParseUntilCommand: %v", err)
	}
	if got := f.Str("output"); got != "app.log" {
		t.Errorf("-o = %q, want app.log", got)
	}
	if !f.Has("wait") {
		t.Error("-w was not parsed")
	}
	want := []string{"tail", "-f", "-n", "5", "app.log"}
	if strings.Join(f.Positional, " ") != strings.Join(want, " ") {
		t.Errorf("command line = %q, want %q", f.Positional, want)
	}

	// -- ends the options explicitly, and a bare "-" is a word, not an option
	f2 := NewFlags()
	if err := f2.ParseUntilCommand([]string{"--", "-weird", "cmd"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f2.Positional, " ") != "-weird cmd" {
		t.Errorf("after --: %q", f2.Positional)
	}
	f3 := NewFlags()
	if err := f3.ParseUntilCommand([]string{"-", "x"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f3.Positional, " ") != "- x" {
		t.Errorf("a lone dash must start the command: %q", f3.Positional)
	}

	// an unknown option before the command is still an error
	f4 := NewFlags()
	if err := f4.ParseUntilCommand([]string{"--nope", "cmd"}); err == nil {
		t.Error("an unknown option before the command must be rejected")
	}
}

func TestNohupUsageAndLookupStatuses(t *testing.T) {
	if code, _, errOut := Execute("nohup", nil, nil); code != nohupUsageExit || !strings.Contains(errOut, "missing operand") {
		t.Errorf("nohup without a command = %d, %q (want 125)", code, errOut)
	}
	if code, _, errOut := Execute("nohup", []string{"no-such-command-xyz"}, nil); code != nohupFoundExit ||
		!strings.Contains(errOut, "No such file or directory") {
		t.Errorf("nohup missing command = %d, %q (want 127)", code, errOut)
	}
	if code, _, errOut := Execute("nohup", []string{"-a", "-t", "cmd", "/c", "exit"}, nil); code != nohupUsageExit {
		t.Errorf("-a with -t = %d, %q (want 125)", code, errOut)
	}
	// --help and --version still work with the command-boundary parser
	if code, out, _ := Execute("nohup", []string{"--help"}, nil); code != 0 ||
		!strings.Contains(out, "Usage: nohup [OPTION]... COMMAND [ARG]...") {
		t.Errorf("nohup --help = %d, %q", code, out)
	}
}

// A winux command name is re-entered through this executable, so the whole
// toolbox is available to nohup without going through the .cmd shims.
func TestNohupResolvesBuiltinCommandsToItsOwnExecutable(t *testing.T) {
	target, err := nohupResolve("tail", []string{"-f", "app.log"})
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if !strings.EqualFold(target.exe, self) {
		t.Errorf("builtin resolved to %q, want %q", target.exe, self)
	}
	if strings.Join(target.argv, " ") != "tail -f app.log" {
		t.Errorf("argv = %q", target.argv)
	}
}

// CreateProcess cannot start a batch file, so .cmd/.bat go through cmd.exe /c
// and .ps1 through powershell -File — otherwise `nohup npm start` would fail.
func TestNohupWrapsScriptCommands(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "hello.cmd")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho batch-ok\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := nohupResolve(bat, []string{"a b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.ToLower(target.exe), "cmd.exe") {
		t.Errorf("a .cmd must be run by cmd.exe, got %q", target.exe)
	}
	if target.cmdLine == "" {
		t.Fatal("a batch file needs an exact command line, not an argv")
	}
	line := target.cmdLine
	if !strings.Contains(line, "/d /s /c ") || !strings.Contains(line, bat) || !strings.Contains(line, `"a b"`) {
		t.Errorf("command line = %q (want /d /s /c, the quoted path and the spaced argument)", line)
	}

	// and it really runs, with the argument reaching the batch file
	code, _, errOut := Execute("nohup", []string{"-w", "-o", filepath.Join(dir, "b.log"), bat, "x"}, nil)
	if code != 0 {
		t.Fatalf("nohup <batch> = %d, %q", code, errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, "b.log"))
	if err != nil || !strings.Contains(string(got), "batch-ok") {
		t.Errorf("batch output = %q, %v", got, err)
	}

	ps1 := filepath.Join(dir, "hello.ps1")
	if err := os.WriteFile(ps1, []byte("Write-Output 'ps-ok'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, err = nohupResolve(ps1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.ToLower(target.exe), "powershell.exe") || target.argv[0] != "-NoProfile" ||
		!strings.Contains(strings.Join(target.argv, " "), "-File "+ps1) {
		t.Errorf(".ps1 resolved to %q %q", target.exe, target.argv)
	}
}

func TestNohupWaitPropagatesExitStatusAndOutput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "app.log")

	// cmd's own exit status comes back unchanged
	if code, _, errOut := Execute("nohup", []string{"-w", "-q", "cmd", "/c", "exit 7"}, nil); code != 7 {
		t.Errorf("--wait exit status = %d, %q (want 7)", code, errOut)
	}

	// --wait with -o keeps the readable log, appending across runs like GNU
	for _, line := range []string{"first", "second"} {
		if code, _, errOut := Execute("nohup", []string{"-w", "-q", "-o", log, "cmd", "/c", "echo " + line}, nil); code != 0 {
			t.Fatalf("nohup -w -o = %d, %q", code, errOut)
		}
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.ReplaceAll(string(got), "\r\n", "\n"); s != "first\nsecond\n" {
		t.Errorf("appended log = %q", s)
	}
	// --truncate starts over instead
	if code, _, _ := Execute("nohup", []string{"-w", "-q", "-t", "-o", log, "cmd", "/c", "echo third"}, nil); code != 0 {
		t.Fatal("nohup -w -t failed")
	}
	got, _ = os.ReadFile(log)
	if s := strings.ReplaceAll(string(got), "\r\n", "\n"); s != "third\n" {
		t.Errorf("truncated log = %q", s)
	}
}

func TestNohupDirAndEnvApplyToTheChild(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "where.log")
	code, _, errOut := Execute("nohup", []string{"-w", "-q", "-d", dir, "-e", "WINUX_NOHUP_TEST=abc", "-o", log,
		"cmd", "/c", "echo %WINUX_NOHUP_TEST% & cd"}, nil)
	if code != 0 {
		t.Fatalf("nohup -d -e = %d, %q", code, errOut)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "abc") {
		t.Errorf("the -e variable did not reach the child: %q", out)
	}
	// cmd prints its working directory; compare case-insensitively because the
	// drive letter case is whatever CreateProcess was given
	if !strings.Contains(strings.ToLower(out), strings.ToLower(dir)) {
		t.Errorf("the child did not start in %q: %q", dir, out)
	}
}

// The point of the command: the child must be running, and must NOT be attached
// to this console (that attachment is what kills a process when the window
// closes).
func TestNohupDetachesFromTheConsole(t *testing.T) {
	if len(consoleProcessIDs()) == 0 {
		t.Skip("no console attached to the test process")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "bg.log")
	pidFile := filepath.Join(dir, "bg.pid")

	code, out, errOut := Execute("nohup", []string{"-q", "-o", log, "-p", pidFile,
		"cmd", "/c", "ping -n 30 127.0.0.1 >nul"}, nil)
	if code != 0 {
		t.Fatalf("detached nohup = %d, stdout %q, stderr %q", code, out, errOut)
	}
	pid := readPIDFile(t, pidFile)
	defer killPID(t, pid)

	if !processAlive(pid) {
		t.Fatal("the detached child is not running")
	}
	for _, cpid := range consoleProcessIDs() {
		if int(cpid) == pid {
			t.Fatalf("child %d is still attached to this console; closing the window would kill it", pid)
		}
	}
	// it stays alive while we are still here (i.e. it is not waited on)
	time.Sleep(400 * time.Millisecond)
	if !processAlive(pid) {
		t.Error("the detached child exited immediately")
	}
}

// -p/-P report the same pid, and -q keeps stderr clean.
func TestNohupPidFileAndPrintPid(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "p.pid")
	log := filepath.Join(dir, "p.log")
	code, out, errOut := Execute("nohup", []string{"-o", log, "-p", pidFile, "-P",
		"cmd", "/c", "ping -n 30 127.0.0.1 >nul"}, nil)
	if code != 0 {
		t.Fatalf("nohup = %d, %q", code, errOut)
	}
	pid := readPIDFile(t, pidFile)
	defer killPID(t, pid)
	if strings.TrimSpace(out) != strconv.Itoa(pid) {
		t.Errorf("--print-pid printed %q, pid file has %d", strings.TrimSpace(out), pid)
	}
	// the normal (non-quiet) run explains itself on stderr
	if !strings.Contains(errOut, "appending output to") || !strings.Contains(errOut, "started 'cmd /c") {
		t.Errorf("stderr = %q", errOut)
	}
}

// Without -o, output is only redirected when it would otherwise go to a
// console; a captured/redirected stdout is inherited exactly as GNU nohup does.
func TestNohupKeepsInheritedOutputWhenNotAConsole(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	code, _, errOut := Execute("nohup", []string{"-q", "cmd", "/c", "exit 0"}, nil)
	if code != 0 {
		t.Fatalf("nohup = %d, %q", code, errOut)
	}
	if fileExists(filepath.Join(dir, "nohup.out")) {
		t.Error("nohup.out must not be created when the output is not a console")
	}
}

// nohup must not hang on a console-less child when the caller asked for a file.
func TestNohupDetachedChildWritesIntoTheLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "done.log")
	pidFile := filepath.Join(dir, "done.pid")
	code, _, errOut := Execute("nohup", []string{"-q", "-o", log, "-p", pidFile,
		"cmd", "/c", "ping -n 2 127.0.0.1 >nul & echo finished"}, nil)
	if code != 0 {
		t.Fatalf("nohup = %d, %q", code, errOut)
	}
	pid := readPIDFile(t, pidFile)
	defer killPID(t, pid)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(log); err == nil && strings.Contains(string(b), "finished") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, _ := os.ReadFile(log)
	t.Fatalf("the detached child never wrote its output (log = %q)", b)
}

// A directory is not a command: it must be reported, not started.
func TestNohupRejectsADirectoryAsCommand(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := Execute("nohup", []string{dir}, nil)
	if code != nohupFoundExit || !strings.Contains(errOut, "No such file or directory") {
		t.Errorf("nohup <dir> = %d, %q", code, errOut)
	}
}
