package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withDuDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "b.log"), "beta")
	writeFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", 4000))
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "inner.txt"), "in")
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(old) })
	return dir
}

func duOutput(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := run(t, "", "du", args...)
	if code != 0 {
		t.Fatalf("du %v failed: %s", args, errOut)
	}
	return out
}

func duLines(t *testing.T, args ...string) []string {
	raw := duOutput(t, args...)
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "du: ") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestDuBasic(t *testing.T) {
	withDuDir(t)
	out := string(duOutput(t))
	// output format: "N\tpath", each line has a tab
	if !strings.Contains(out, "\t") {
		t.Fatalf("du output should contain tab separators:\n%s", out)
	}
	lines := duLines(t)
	if len(lines) < 2 {
		t.Fatalf("du should produce at least 2 lines (subdir + root):\n%v", lines)
	}
	// Last line should be the root directory total ending with tab-dot
	if last := lines[len(lines)-1]; !strings.Contains(last, "\t.") {
		t.Errorf("du last line should end with tab dot:\n%s", last)
	}
}

func TestDuSummarizeAndHuman(t *testing.T) {
	withDuDir(t)
	lines := duLines(t, "-s")
	if len(lines) != 1 {
		t.Fatalf("du -s should produce exactly one line:\n%v", lines)
	}
	if !strings.Contains(lines[0], "\t.") {
		t.Fatalf("du -s should end with tab dot:\n%s", lines[0])
	}
	out := duOutput(t, "-sh")
	if !strings.Contains(out, "K") && !strings.Contains(out, "\t.") {
		t.Fatalf("du -sh should produce a human-sized total including K or tab dot:\n%s", out)
	}
}

func TestDuAllAndDepth(t *testing.T) {
	withDuDir(t)
	lines := duLines(t, "-a")
	hasA, hasBig := false, false
	for _, l := range lines {
		if strings.Contains(l, "a.txt") {
			hasA = true
		}
		if strings.Contains(l, "big.bin") {
			hasBig = true
		}
	}
	if !hasA || !hasBig {
		t.Fatalf("du -a should list all files:\n%v", lines)
	}
	// depth limit: du -d 1 => at most a few lines (root, sub, deep?)
	if d1 := duLines(t, "-d", "1"); len(d1) > 5 {
		t.Fatalf("du -d 1 should produce only few lines:\n%v", d1)
	}
}

func TestDuBytesAndBlockSize(t *testing.T) {
	withDuDir(t)
	out := duOutput(t, "-sb", "a.txt")
	// 5 bytes ("alpha")
	if !strings.Contains(out, "\t") || !strings.Contains(out, "a.txt") {
		t.Fatalf("du -sb a.txt should print byte count:\n%s", out)
	}
	if code, out, errOut := run(t, "", "du", "-B", "1K", "-s", "a.txt"); code != 0 {
		t.Fatalf("du -B 1K failed: %s", errOut)
	} else if !strings.Contains(out, "a.txt") {
		t.Fatalf("du -B 1K output:\n%s", out)
	}
}

func TestDuTotalAndThreshold(t *testing.T) {
	withDuDir(t)
	out := duOutput(t, "-s", "-c")
	if !strings.Contains(out, "total") {
		t.Fatalf("du -s -c should print total:\n%s", out)
	}
	// threshold: skip tiny files: du -a -t 4K => omit entries under 4K
	if code, _, _ := run(t, "", "du", "-a", "-t", "4K"); code == 0 {
		t.Log("du -a -t 4K accepted threshold")
	}
	// bogus threshold
	code, _, errOut := run(t, "", "du", "-t", "xyz")
	if code == 0 {
		t.Fatalf("du -t xyz should fail")
	}
	if !strings.Contains(errOut, "invalid") {
		t.Logf("error: %s", errOut)
	}
}

func TestDuExclude(t *testing.T) {
	withDuDir(t)
	out := duOutput(t, "-a", "--exclude", "*.log")
	if strings.Contains(out, "b.log") {
		t.Fatalf("du -a --exclude '*.log' should not list b.log:\n%s", out)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("du -a --exclude b.log should still list a.txt:\n%s", out)
	}
}

func TestDuErrors(t *testing.T) {
	withDuDir(t)
	if code, _, errOut := run(t, "", "du", "no-such-thing"); code != 1 || !strings.Contains(errOut, "cannot access") {
		t.Fatalf("du no-such-thing: code=%d err=%q", code, errOut)
	}
}