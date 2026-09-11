package core_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPsFormats(t *testing.T) {
	code, out, errOut := run(t, "", "ps", "aux")
	if code != 0 {
		t.Fatalf("ps aux failed: %s", errOut)
	}
	if !strings.Contains(out, "USER       PID  %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND") {
		t.Fatalf("ps aux header wrong:\n%s", out)
	}
	if strings.Count(out, "\n") < 3 {
		t.Fatalf("ps aux should list several processes:\n%s", out)
	}
	code, out, _ = run(t, "", "ps", "-ef")
	if code != 0 || !strings.Contains(out, "UID        PID  PPID  C STIME TTY          TIME     CMD") {
		t.Fatalf("ps -ef header wrong:\n%s", out)
	}
	code, out, _ = run(t, "", "ps")
	if code != 0 || !strings.Contains(out, "  PID TTY          TIME CMD") {
		t.Fatalf("default ps header wrong:\n%s", out)
	}
}

func TestPsSelectors(t *testing.T) {
	// our own process id must be listed with -p
	me := os.Getpid()
	code, out, errOut := run(t, "", "ps", "-p", strconv.Itoa(me))
	if code != 0 {
		t.Fatalf("ps -p failed: %s", errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], strconv.Itoa(me)) {
		t.Fatalf("ps -p %d did not list the process:\n%s", me, out)
	}
	// PID 4 is the System process on every Windows machine
	code, out, _ = run(t, "", "ps", "-o", "pid,comm", "-p", "4")
	if code != 0 || !strings.Contains(out, "4 System") {
		t.Fatalf("ps -p 4 should show the System process:\n%s", out)
	}
	// a pid list works
	code, out, _ = run(t, "", "ps", "-p", "4,"+strconv.Itoa(me))
	if code != 0 || !strings.Contains(out, "System") || !strings.Contains(out, strconv.Itoa(me)) {
		t.Fatalf("ps -p LIST failed:\n%s", out)
	}
	// -N negates: excluding a nonexistent pid yields every process
	code, out, _ = run(t, "", "ps", "-N", "-p", "99999999", "-e")
	if code != 0 || strings.Count(out, "\n") < 3 {
		t.Fatalf("ps -N -p 99999999 -e should list everything:\n%s", out)
	}
	// unknown pids yield no rows (header only)
	code, out, _ = run(t, "", "ps", "-p", "99999999")
	if code != 0 || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("ps -p 99999999 should print only the header:\n%q", out)
	}
}

func TestPsOutputAndSort(t *testing.T) {
	code, out, errOut := run(t, "", "ps", "-o", "pid,comm,%cpu", "-p", "4")
	if code != 0 {
		t.Fatalf("ps -o failed: %s", errOut)
	}
	if !strings.Contains(out, "PID COMMAND %CPU") || !strings.Contains(out, "4 System") {
		t.Fatalf("ps -o pid,comm,pct-cpu columns wrong:\n%s", out)
	}
	// -h suppresses the header
	code, out, _ = run(t, "", "ps", "-h", "-p", "4")
	if code != 0 || strings.Contains(out, "PID") || !strings.Contains(out, "System") {
		t.Fatalf("ps -h should drop the header:\n%q", out)
	}
	// --sort with a valid key works, an invalid key fails
	if code, _, _ := run(t, "", "ps", "--sort=-%cpu", "-e"); code != 0 {
		t.Fatal("ps --sort descending percent-cpu failed")
	}
	if code, _, errOut := run(t, "", "ps", "--sort=bogus", "-e"); code != 1 || !strings.Contains(errOut, "sort") {
		t.Fatalf("ps --sort=bogus: code=%d err=%q", code, errOut)
	}
	// -C selects by image name; the test binary's name always matches itself
	base := ""
	if len(os.Args) > 0 {
		base = os.Args[0]
		if i := strings.LastIndexAny(base, `\/`); i >= 0 {
			base = base[i+1:]
		}
		base = strings.TrimSuffix(base, ".exe")
	}
	if base != "" {
		code, out, _ = run(t, "", "ps", "-C", base, "-h", "-o", "pid,comm")
		if code != 0 || !strings.Contains(out, base) {
			t.Fatalf("ps -C %s wrong:\n%s", base, out)
		}
	}
}
