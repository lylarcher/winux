package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// setHidden marks a file with the NTFS hidden attribute (via kernel32), so the
// hidden filtering of ls can be tested against a real Windows attribute.
func setHidden(t *testing.T, p string) {
	t.Helper()
	k32 := syscall.NewLazyDLL("kernel32.dll")
	proc := k32.NewProc("SetFileAttributesW")
	pw, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(pw)), 0x2)
	if r == 0 {
		t.Fatalf("SetFileAttributesW(%s) failed", p)
	}
}

// withLsDir builds a fixture directory and makes it the working directory for
// the rest of the test (run/execute does not change directories itself).
func withLsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "b.log"), "beta")
	writeFile(t, filepath.Join(dir, "run.exe"), "tool")
	writeFile(t, filepath.Join(dir, ".dot"), "hidden")
	writeFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", 4000))
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "inner.txt"), "in")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	return dir
}

// lsLines runs ls with the given args in the ls fixture dir and returns the
// non-empty output lines.
func lsLines(t *testing.T, args ...string) []string {
	t.Helper()
	code, out, errOut := run(t, "", "ls", args...)
	if code != 0 {
		t.Fatalf("ls %v failed: %s", args, errOut)
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func shortOwner() string {
	u := os.Getenv("USERNAME")
	if u == "" {
		u = os.Getenv("USER")
	}
	if u == "" {
		return "unknown"
	}
	if i := strings.LastIndexAny(u, `\/`); i >= 0 {
		return u[i+1:]
	}
	return u
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func TestLsBasic(t *testing.T) {
	withLsDir(t)
	want := []string{"a.txt", "b.log", "big.bin", "run.exe", "sub"}
	lines := lsLines(t)
	if len(lines) != len(want) {
		t.Fatalf("ls: got %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("ls line %d: got %q, want %q (sorted, hidden files excluded)", i, lines[i], want[i])
		}
	}
	// operand form lists the directory's contents (file and subdirectory)
	if sub := lsLines(t, "sub"); len(sub) != 2 || sub[0] != "deep" || sub[1] != "inner.txt" {
		t.Fatalf("ls sub: %v", sub)
	}
	// wildcard operand expands
	if ok := lsLines(t, "*.txt"); len(ok) != 1 || ok[0] != "a.txt" {
		t.Fatalf("ls *.txt: %v", ok)
	}
}

func TestLsHidden(t *testing.T) {
	withLsDir(t)
	hidden := filepath.Join(".", "attr-hidden.txt")
	writeFile(t, hidden, "x")
	setHidden(t, hidden)

	for _, l := range lsLines(t) {
		if l == ".dot" || l == "attr-hidden.txt" {
			t.Fatalf("ls without -a listed hidden entry %q", l)
		}
	}
	joined := strings.Join(lsLines(t, "-a"), "\n")
	if !strings.Contains(joined, ".dot") || !strings.Contains(joined, "attr-hidden.txt") {
		t.Fatalf("ls -a should list dotfiles and the hidden attribute file:\n%s", joined)
	}
	if !strings.Contains(joined, "\n.\n..\n") && !strings.HasPrefix(joined, ".\n..\n") {
		t.Fatalf("ls -a should list . and .. first:\n%s", joined)
	}
	joinedA := strings.Join(lsLines(t, "-A"), "\n")
	if !strings.Contains(joinedA, ".dot") {
		t.Fatalf("ls -A should list dotfiles:\n%s", joinedA)
	}
	if strings.Contains(joinedA, "\n.\n") || strings.Contains(joinedA, "\n..\n") {
		t.Fatalf("ls -A must not list . and ..:\n%s", joinedA)
	}
}

func TestLsDirectoryItself(t *testing.T) {
	withLsDir(t)
	if lines := lsLines(t, "-d"); len(lines) != 1 || lines[0] != "." {
		t.Fatalf("ls -d .: %v", lines)
	}
	if lines := lsLines(t, "a.txt"); len(lines) != 1 || lines[0] != "a.txt" {
		t.Fatalf("ls a.txt: %v", lines)
	}
	if lines := lsLines(t, "-d", "sub"); len(lines) != 1 || lines[0] != "sub" {
		t.Fatalf("ls -d sub: %v", lines)
	}
}

func TestLsLong(t *testing.T) {
	withLsDir(t)
	joined := strings.Join(lsLines(t, "-l"), "\n")
	if !strings.HasPrefix(joined, "total ") {
		t.Fatalf("ls -l should start with a total line:\n%s", joined)
	}
	for _, want := range []string{"drwxr-xr-x", "-rw-r--r--", "-rwxr-xr-x", "None", " a.txt", " b.log", " run.exe", " sub"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ls -l missing %q:\n%s", want, joined)
		}
	}
	// human sizes + real (numeric) file ids + long-iso time style
	joined = strings.Join(lsLines(t, "-l", "-h", "-i"), "\n")
	if !strings.Contains(joined, "3.9K") {
		t.Errorf("ls -l -h should show 3.9K for big.bin:\n%s", joined)
	}
	for _, l := range strings.Split(joined, "\n")[1:] {
		fields := strings.Fields(l)
		if len(fields) > 4 && !isNumeric(fields[0]) {
			t.Errorf("ls -i should prefix every line with a numeric file id: %q", l)
		}
	}
	joined = strings.Join(lsLines(t, "-l", "-n"), "\n")
	if !strings.Contains(joined, "1000 1000") {
		t.Errorf("ls -l -n should print numeric uid/gid 1000 1000:\n%s", joined)
	}
	joined = strings.Join(lsLines(t, "-l", "--time-style=long-iso"), "\n")
	if !strings.Contains(joined, "-09-") && !strings.Contains(joined, "-0") {
		t.Errorf("long-iso style should print a YYYY-MM-DD date:\n%s", joined)
	}
	// -o hides the group, -g hides the owner
	if joined := strings.Join(lsLines(t, "-l", "-o"), "\n"); strings.Contains(joined, "None") {
		t.Errorf("ls -l -o should hide the group column:\n%s", joined)
	}
	if joined := strings.Join(lsLines(t, "-l", "-g"), "\n"); strings.Contains(joined, shortOwner()) {
		t.Errorf("ls -l -g should hide the owner column:\n%s", joined)
	}
}

func TestLsSortAndClassify(t *testing.T) {
	withLsDir(t)
	if lines := lsLines(t, "-S"); lines[0] != "big.bin" {
		t.Fatalf("ls -S should sort big.bin first: %v", lines)
	}
	if lines := lsLines(t, "-S", "-r"); lines[len(lines)-1] != "big.bin" {
		t.Fatalf("ls -S -r should put big.bin last: %v", lines)
	}
	if lines := lsLines(t, "-r"); lines[0] != "sub" && lines[0] != "run.exe" {
		t.Fatalf("ls -r should reverse the name order: %v", lines)
	}
	time.Sleep(20 * time.Millisecond)
	writeFile(t, "newest.txt", "n")
	if lines := lsLines(t, "-t"); lines[0] != "newest.txt" {
		t.Fatalf("ls -t should sort newest.txt first: %v", lines)
	}
	if lines := lsLines(t, "-t", "-r"); lines[len(lines)-1] != "newest.txt" {
		t.Fatalf("ls -t -r should put newest.txt last: %v", lines)
	}
	joined := strings.Join(lsLines(t, "-F"), "\n")
	if !strings.Contains(joined, "sub/") || !strings.Contains(joined, "run.exe*") {
		t.Fatalf("ls -F should append / and *: %v", joined)
	}
	joined = strings.Join(lsLines(t, "-m"), "\n")
	if len(joined) == 0 || !strings.Contains(joined, ", ") {
		t.Fatalf("ls -m should join entries with commas: %s", joined)
	}
}

func TestLsRecursive(t *testing.T) {
	withLsDir(t)
	joined := strings.Join(lsLines(t, "-R"), "\n")
	for _, want := range []string{"sub:", "inner.txt", "deep"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ls -R should recurse with headers; missing %q:\n%s", want, joined)
		}
	}
}

func TestLsColor(t *testing.T) {
	withLsDir(t)
	if code, out, _ := run(t, "", "ls", "--color=always"); code != 0 || !strings.Contains(out, "\x1b[") {
		t.Fatalf("ls --color=always should emit ANSI codes, got %q", out)
	}
	if code, out, _ := run(t, "", "ls", "--color=never"); code != 0 || strings.Contains(out, "\x1b[") {
		t.Fatalf("ls --color=never should not emit ANSI codes, got %q", out)
	}
}

func TestLsErrors(t *testing.T) {
	withLsDir(t)
	if code, _, errOut := run(t, "", "ls", "no-such-thing"); code != 2 || !strings.Contains(errOut, "cannot access") {
		t.Fatalf("ls missing operand: code=%d err=%q", code, errOut)
	}
	if code, _, _ := run(t, "", "ls", "--bogus"); code != 2 {
		t.Fatalf("ls --bogus should fail with exit 2")
	}
}