//go:build windows

package core

// Tests for kill/pkill/pgrep. Everything that can be decided from data (signal
// spellings, the pid operand, the selector) is tested directly; the few tests
// that must touch real processes spawn their own child so they can never kill
// anything that belongs to the machine's other work.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLookupSignalSpellings(t *testing.T) {
	cases := []struct {
		spec   string
		num    int
		name   string
		action killAction
	}{
		{"9", 9, "KILL", actForce},
		{"KILL", 9, "KILL", actForce},
		{"kill", 9, "KILL", actForce},
		{"SIGKILL", 9, "KILL", actForce},
		{"sigterm", 15, "TERM", actGraceful},
		{"15", 15, "TERM", actGraceful},
		{"TERM", 15, "TERM", actGraceful},
		{"0", 0, "0", actCheck},
		{"19", 19, "STOP", actSuspend},
		{"STOP", 19, "STOP", actSuspend},
		{"TSTP", 20, "TSTP", actSuspend},
		{"CONT", 18, "CONT", actResume},
		{"USR1", 10, "USR1", actUnsupported},
		{"34", 34, "", actUnsupported},
	}
	for _, c := range cases {
		d, ok := lookupSignal(c.spec)
		if !ok {
			t.Errorf("lookupSignal(%q) rejected a valid spelling", c.spec)
			continue
		}
		if d.num != c.num || d.name != c.name || d.action != c.action {
			t.Errorf("lookupSignal(%q) = {%d %q %v}, want {%d %q %v}",
				c.spec, d.num, d.name, d.action, c.num, c.name, c.action)
		}
	}
	for _, bad := range []string{"", "BOGUS", "SIG", "9x", "-"} {
		if _, ok := lookupSignal(bad); ok {
			t.Errorf("lookupSignal(%q) must be rejected", bad)
		}
	}
}

func TestParseKillPidOperands(t *testing.T) {
	if pid, group, err := parseKillPid("1234"); err != nil || pid != 1234 || group {
		t.Errorf("parseKillPid(1234) = %d %v %v", pid, group, err)
	}
	// a negative operand is how POSIX names a process group; on Windows it is
	// taken as "the tree rooted at that pid"
	if pid, group, err := parseKillPid("-1234"); err != nil || pid != 1234 || !group {
		t.Errorf("parseKillPid(-1234) = %d %v %v", pid, group, err)
	}
	for _, bad := range []string{"", "abc", "0", "12x"} {
		if _, _, err := parseKillPid(bad); err == nil {
			t.Errorf("parseKillPid(%q) must fail", bad)
		}
	}
}

func TestKillListAndTable(t *testing.T) {
	code, out, _ := Execute("kill", []string{"-l", "KILL"}, nil)
	if code != 0 || strings.TrimSpace(out) != "9" {
		t.Errorf("kill -l KILL = %d, %q (want 9)", code, out)
	}
	code, out, _ = Execute("kill", []string{"-l", "9"}, nil)
	if code != 0 || strings.TrimSpace(out) != "KILL" {
		t.Errorf("kill -l 9 = %d, %q (want KILL)", code, out)
	}
	code, out, _ = Execute("kill", []string{"-L"}, nil)
	if code != 0 || !strings.Contains(out, "SIGKILL") || !strings.Contains(out, "TerminateProcess") {
		t.Errorf("kill -L = %d, %q", code, out)
	}
	code, out, _ = Execute("kill", []string{"-l"}, nil)
	if code != 0 || !strings.Contains(out, "SIGHUP") || !strings.Contains(out, "SIGTERM") {
		t.Errorf("kill -l = %d, %q", code, out)
	}
	// unsupported signal spellings are refused with an explanation, not silently
	// turned into something else
	code, _, errOut := Execute("kill", []string{"-l", "BOGUS"}, nil)
	if code != 1 || !strings.Contains(errOut, "invalid signal") {
		t.Errorf("kill -l BOGUS = %d, %q", code, errOut)
	}
}

func TestKillOptionParsing(t *testing.T) {
	// no pid at all is a usage error (2), and so is a bad signal
	if code, _, _ := Execute("kill", nil, nil); code != 2 {
		t.Errorf("kill without operands = %d, want 2", code)
	}
	if code, _, errOut := Execute("kill", []string{"-s", "BOGUS", "1"}, nil); code != 2 || !strings.Contains(errOut, "invalid signal") {
		t.Errorf("kill -s BOGUS = %d, %q", code, errOut)
	}
	if code, _, errOut := Execute("kill", []string{"--nope", "1"}, nil); code != 2 || !strings.Contains(errOut, "unknown option") {
		t.Errorf("kill --nope = %d, %q", code, errOut)
	}
	// a non-numeric operand is reported as such
	if code, _, errOut := Execute("kill", []string{"abc"}, nil); code != 1 || !strings.Contains(errOut, "process ids") {
		t.Errorf("kill abc = %d, %q", code, errOut)
	}
	// an unsupported signal on an existing process fails with 1
	if code, _, errOut := Execute("kill", []string{"-USR1", strconv.Itoa(os.Getpid())}, nil); code != 1 ||
		!strings.Contains(errOut, "not supported on Windows") {
		t.Errorf("kill -USR1 = %d, %q", code, errOut)
	}
}

// A child of this test process, so nothing else on the machine is affected.
func spawnPing(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("cmd.exe", "/c", "ping -n 40 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start the fixture child: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	time.Sleep(300 * time.Millisecond)
	return cmd
}

func TestKillCheckAndForce(t *testing.T) {
	child := spawnPing(t)
	pid := child.Process.Pid
	if code, _, errOut := Execute("kill", []string{"-0", strconv.Itoa(pid)}, nil); code != 0 {
		t.Fatalf("kill -0 <live pid> = %d, %q", code, errOut)
	}
	if code, _, errOut := Execute("kill", []string{"-9", strconv.Itoa(pid)}, nil); code != 0 {
		t.Fatalf("kill -9 = %d, %q", code, errOut)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("the child survived kill -9")
	}
	if code, _, _ := Execute("kill", []string{"-0", strconv.Itoa(pid)}, nil); code != 1 {
		t.Errorf("kill -0 <dead pid> = %d, want 1", code)
	}
	if code, _, errOut := Execute("kill", []string{"-0", "999999"}, nil); code != 1 ||
		!strings.Contains(errOut, "No such process") {
		t.Errorf("kill -0 999999 = %d, %q", code, errOut)
	}
}

// TERM asks politely when it can and forces when Windows cannot ask (console
// processes); --graceful-only keeps the strict POSIX behaviour instead.
func TestKillTermEscalatesUnlessGracefulOnly(t *testing.T) {
	child := spawnPing(t)
	pid := child.Process.Pid
	if code, _, errOut := Execute("kill", []string{"--graceful-only", strconv.Itoa(pid)}, nil); code != 1 ||
		!strings.Contains(errOut, "--graceful-only") {
		t.Errorf("kill --graceful-only = %d, %q (a console child cannot be asked)", code, errOut)
	}
	if !processAlive(pid) {
		t.Fatal("--graceful-only must not have terminated the process")
	}
	if code, _, errOut := Execute("kill", []string{strconv.Itoa(pid)}, nil); code != 0 {
		t.Fatalf("plain TERM = %d, %q (it escalates for console processes)", code, errOut)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("the child survived TERM with escalation")
	}
}

func TestKillSuspendAndResume(t *testing.T) {
	child := spawnPing(t)
	pid := child.Process.Pid
	defer func() { _ = exec.Command("taskkill", "/pid", strconv.Itoa(pid), "/f").Run() }()
	if code, _, errOut := Execute("kill", []string{"-STOP", strconv.Itoa(pid)}, nil); code != 0 {
		t.Fatalf("kill -STOP = %d, %q", code, errOut)
	}
	if !processAlive(pid) {
		t.Fatal("a suspended process must still exist")
	}
	if code, _, errOut := Execute("kill", []string{"-CONT", strconv.Itoa(pid)}, nil); code != 0 {
		t.Fatalf("kill -CONT = %d, %q", code, errOut)
	}
}

// -t (and a negative pid, POSIX's process-group spelling) must take the
// descendants down with the target.
func TestKillTreeTakesDescendants(t *testing.T) {
	// cmd.exe /c ping starts ping.exe as its child, so there are two levels
	outer := exec.Command("cmd.exe", "/c", "ping -n 40 127.0.0.1 >nul")
	if err := outer.Start(); err != nil {
		t.Fatalf("cannot start the fixture child: %v", err)
	}
	go func() { _ = outer.Wait() }()
	time.Sleep(500 * time.Millisecond)
	inner := -1
	if procs, err := snapshotProcesses(); err == nil {
		for _, p := range procs {
			if p.ppid == outer.Process.Pid && strings.EqualFold(procComm(p.name), "ping") {
				inner = p.pid
			}
		}
	}
	if inner < 0 {
		_ = exec.Command("taskkill", "/pid", strconv.Itoa(outer.Process.Pid), "/f").Run()
		t.Skip("could not find the child ping process")
	}
	code, _, errOut := Execute("kill", []string{"-9", "-t", strconv.Itoa(outer.Process.Pid)}, nil)
	if code != 0 {
		t.Fatalf("kill -9 -t = %d, %q", code, errOut)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (processAlive(outer.Process.Pid) || processAlive(inner)) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(outer.Process.Pid) {
		t.Error("the outer process survived kill -t")
	}
	if processAlive(inner) {
		t.Error("the descendant survived kill -t")
	}
}

// ---------------------------------------------------------------- selector --

func synthProc(pid, ppid int, name, cmdline, user string, age time.Duration) procInfo {
	return procInfo{
		pid: pid, ppid: ppid, name: name, cmdline: cmdline, user: user,
		create: time.Now().Add(-age),
	}
}

func TestSelectorMatching(t *testing.T) {
	procs := []procInfo{
		synthProc(1, 0, "CHROME.EXE", `"C:\chrome\CHROME.EXE" --type=renderer`, `DESKTOP\lyl`, 2*time.Hour),
		synthProc(2, 1, "chrome.exe", `"C:\chrome\chrome.exe" --type=gpu`, `DESKTOP\lyl`, time.Hour),
		synthProc(3, 100, "notepad.exe", `notepad.exe notes.txt`, `DESKTOP\other`, 30*time.Minute),
		synthProc(4, 1, "node.exe", `node server.js --port 80`, `DESKTOP\lyl`, 10*time.Minute),
	}
	// default: image name, case-insensitively (Windows reports stored case)
	sel := &procSelector{pattern: "chrome"}
	if !sel.compilePattern("pgrep") {
		t.Fatal("pattern did not compile")
	}
	got := sel.selectProcs(procs)
	if len(got) != 2 || got[0].pid != 1 || got[1].pid != 2 {
		t.Errorf("chrome match = %v", pids(got))
	}
	// -x: whole string only, so "chrome.exe" (comm chrome) still matches but
	// "chrom" does not
	sel = &procSelector{pattern: "chrom", exact: true}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 0 {
		t.Errorf("-x chrom matched %v", pids(got))
	}
	// -f: the command line, where --port 80 lives
	sel = &procSelector{pattern: "port 80", full: true}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 1 || got[0].pid != 4 {
		t.Errorf("-f 'port 80' = %v", pids(got))
	}
	// -f falls back to the image name when the command line is unreadable
	sel = &procSelector{pattern: "notepad", full: true}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 1 || got[0].pid != 3 {
		t.Errorf("-f notepad = %v", pids(got))
	}
	// -v inverts
	sel = &procSelector{pattern: "chrome", inverse: true}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 2 {
		t.Errorf("-v chrome = %v", pids(got))
	}
	// -u owner, bare name and DOMAIN\name both work
	sel = &procSelector{pattern: ".", user: "lyl"}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 3 {
		t.Errorf("-u lyl = %v", pids(got))
	}
	sel = &procSelector{pattern: ".", user: `DESKTOP\other`}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 1 || got[0].pid != 3 {
		t.Errorf("-u DESKTOP\\other = %v", pids(got))
	}
	// -P parent
	sel = &procSelector{pattern: "", parent: 1}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 2 {
		t.Errorf("-P 1 = %v", pids(got))
	}
	// -n newest / -o oldest keep one row
	sel = &procSelector{pattern: "chrome", newest: true}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 1 || got[0].pid != 2 {
		t.Errorf("-n chrome = %v", pids(got))
	}
	sel = &procSelector{pattern: "chrome", oldest: true}
	sel.compilePattern("pgrep")
	if got := sel.selectProcs(procs); len(got) != 1 || got[0].pid != 1 {
		t.Errorf("-o chrome = %v", pids(got))
	}
	// an invalid regexp is rejected instead of matching nothing silently
	sel = &procSelector{pattern: "("}
	if sel.compilePattern("pgrep") {
		t.Error("an unbalanced ( must be reported as an invalid pattern")
	}
}

func pids(procs []procInfo) []int {
	got := make([]int, 0, len(procs))
	for _, p := range procs {
		got = append(got, p.pid)
	}
	return got
}

// cmdPgrep formats the way pgrep does, and its exit status says whether
// anything matched.
func TestPgrepOutputAndStatus(t *testing.T) {
	if code, out, _ := Execute("pgrep", []string{"-l", "-x", "nope-xyz"}, nil); code != 1 || out != "" {
		t.Errorf("pgrep nope = %d, %q (want 1 and no output)", code, out)
	}
	if code, out, _ := Execute("pgrep", []string{"-c", "-x", "nope-xyz"}, nil); code != 1 || strings.TrimSpace(out) != "0" {
		t.Errorf("pgrep -c nope = %d, %q (want 0)", code, out)
	}
	// a selector alone is a valid invocation (procps allows it)
	if code, out, _ := Execute("pgrep", []string{"-c", "-P", strconv.Itoa(os.Getpid())}, nil); code != 1 && code != 0 {
		t.Errorf("pgrep -c -P self = %d, %q", code, out)
	}
	// and --help is answered without touching the process table
	if code, out, _ := Execute("pgrep", []string{"--help"}, nil); code != 0 || !strings.Contains(out, "Usage: pgrep") {
		t.Errorf("pgrep --help = %d, %q", code, out)
	}
	if code, out, _ := Execute("pkill", []string{"--help"}, nil); code != 0 || !strings.Contains(out, "Usage: pkill") {
		t.Errorf("pkill --help = %d, %q", code, out)
	}
}

// pkill really signals what it matched — using -P so only this test's own
// children can ever be affected.
func TestPkillSignalsMatchedChildrenOnly(t *testing.T) {
	outer := exec.Command("cmd.exe", "/c", "ping -n 40 127.0.0.1 >nul")
	if err := outer.Start(); err != nil {
		t.Fatalf("cannot start the fixture child: %v", err)
	}
	go func() { _ = outer.Wait() }()
	time.Sleep(500 * time.Millisecond)
	parent := strconv.Itoa(outer.Process.Pid)

	if code, out, _ := Execute("pgrep", []string{"-c", "-P", parent}, nil); code != 0 || strings.TrimSpace(out) != "1" {
		_ = exec.Command("taskkill", "/pid", parent, "/f").Run()
		t.Skipf("the fixture child has no visible ping child (%d, %q)", code, out)
	}
	// --dry-run must not touch anything
	if code, out, _ := Execute("pkill", []string{"--dry-run", "-P", parent}, nil); code != 0 || !strings.Contains(out, "would signal") {
		t.Errorf("pkill --dry-run = %d, %q", code, out)
	}
	if !processAlive(outer.Process.Pid) {
		t.Fatal("--dry-run terminated a process")
	}
	// and the real thing must take the ping child down, leaving the test alone
	code, out, errOut := Execute("pkill", []string{"-9", "-c", "-P", parent}, nil)
	if code != 0 || strings.TrimSpace(out) != "1" {
		_ = exec.Command("taskkill", "/pid", parent, "/f").Run()
		t.Fatalf("pkill -9 -P %s = %d, %q, %q", parent, code, out, errOut)
	}
	time.Sleep(500 * time.Millisecond)
	if got := selCount(t, parent); got != 0 {
		_ = exec.Command("taskkill", "/pid", parent, "/f").Run()
		t.Fatalf("the ping child survived pkill (%d children left)", got)
	}
	_ = exec.Command("taskkill", "/pid", parent, "/f").Run()
}

// selCount reports how many children the given pid still has in the snapshot.
func selCount(t *testing.T, parent string) int {
	t.Helper()
	code, out, _ := Execute("pgrep", []string{"-c", "-P", parent}, nil)
	if code != 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return n
}
