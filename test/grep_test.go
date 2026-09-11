package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepBasicsAndContext(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "f.txt", "alpha\nbeta\ngamma\ndelta\nepsilon\n")

	code, out, _ := run(t, "", "grep", "-n", "gamma", "f.txt")
	if code != 0 || !strings.Contains(out, "3:gamma") {
		t.Fatalf("grep -n = %q (code %d)", out, code)
	}
	if code, _, _ := run(t, "", "grep", "zzz", "f.txt"); code != 1 {
		t.Fatal("no match should exit 1")
	}
	if code, _, _ := run(t, "", "grep", "x", "nope.txt"); code != 2 {
		t.Fatal("a missing file should exit 2")
	}
	if _, out, _ := run(t, "", "grep", "-c", "a", "f.txt"); strings.TrimSpace(out) != "4" {
		t.Fatalf("grep -c = %q", out)
	}
	if _, out, _ := run(t, "", "grep", "-vc", "a", "f.txt"); strings.TrimSpace(out) != "1" {
		t.Fatalf("grep -vc = %q", out)
	}
	if _, out, _ := run(t, "", "grep", "-i", "GAMMA", "f.txt"); !strings.Contains(out, "gamma") {
		t.Fatalf("grep -i = %q", out)
	}
	if _, out, _ := run(t, "", "grep", "-C", "1", "gamma", "f.txt"); !strings.Contains(out, "beta") || !strings.Contains(out, "delta") {
		t.Fatalf("grep -C 1 = %q", out)
	}
	if _, out, _ := run(t, "", "grep", "-o", "alp", "f.txt"); strings.TrimSpace(out) != "alp" {
		t.Fatalf("grep -o = %q", out)
	}
	if _, out, _ := run(t, "", "grep", "-w", "beta", "f.txt"); !strings.Contains(out, "beta") {
		t.Fatalf("grep -w = %q", out)
	}
	if code, _, _ := run(t, "", "grep", "-x", "beta", "f.txt"); code != 0 {
		t.Fatal("grep -x on a full line should match")
	}
	if code, _, _ := run(t, "", "grep", "-x", "bet", "f.txt"); code != 1 {
		t.Fatal("grep -x on a partial line should not match")
	}
	if _, out, _ := run(t, "", "grep", "-E", "alpha|delta", "f.txt"); !strings.Contains(out, "alpha") || !strings.Contains(out, "delta") {
		t.Fatalf("grep -E = %q", out)
	}
	if code, _, _ := run(t, "", "grep", "-F", "a|b", "f.txt"); code != 1 {
		t.Fatal("grep -F must treat | literally")
	}
	if _, out, _ := run(t, "", "grep", "-m", "1", "a", "f.txt"); strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("grep -m 1 printed more than one line: %q", out)
	}
	if code, out, _ := run(t, "", "grep", "-q", "beta", "f.txt"); code != 0 || out != "" {
		t.Fatalf("grep -q = %q (code %d)", out, code)
	}
}

func TestGrepRecursiveIncludeExclude(t *testing.T) {
	chdirTemp(t)
	writeFile(t, filepath.Join("p", "a.go"), "package main // TODO fix\n")
	writeFile(t, filepath.Join("p", "b.txt"), "TODO in text\n")
	writeFile(t, filepath.Join("p", "sub", "c.go"), "// todo lower\n")
	writeFile(t, filepath.Join("p", "vendor", "d.go"), "TODO vendored\n")

	code, out, _ := run(t, "", "grep", "-rin", "todo", "--include=*.go", "--exclude-dir=vendor", "p")
	if code != 0 {
		t.Fatalf("grep -rin = %q (code %d)", out, code)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "c.go") {
		t.Fatalf("grep -rin missed .go files: %q", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Fatalf("grep -rin matched a .txt file: %q", out)
	}
	if strings.Contains(out, "vendor") {
		t.Fatalf("grep -rin walked into vendor: %q", out)
	}
	// -rl prints only file names, never the matching lines
	if _, out, _ := run(t, "", "grep", "-rl", "todo", "p"); strings.Contains(out, "TODO fix") {
		t.Fatalf("grep -rl printed content: %q", out)
	}
	// a directory without -r is an error (exit 2)
	if code, _, _ := run(t, "", "grep", "todo", "p"); code != 2 {
		t.Fatalf("grep on a directory without -r should exit 2, got %d", code)
	}
}

func TestGrepStdin(t *testing.T) {
	code, out, _ := run(t, "keep\nDROP\nkeep two\n", "grep", "keep")
	if code != 0 {
		t.Fatalf("grep from stdin failed (code %d)", code)
	}
	if !strings.Contains(out, "keep") || !strings.Contains(out, "keep two") || strings.Contains(out, "DROP") {
		t.Fatalf("grep from stdin = %q", out)
	}
}

func TestGrepTextMode(t *testing.T) {
	chdirTemp(t)
	// a UTF-16LE style file: 'h',0,'i',0,newline,0
	data := []byte{'h', 0, 'i', 0, '\n', 0}
	if err := os.WriteFile("utf16.txt", data, 0o666); err != nil {
		t.Fatal(err)
	}
	// without -a a NUL containing file is binary: a raw match is reported as
	// "Binary file ... matches" instead of printing the line
	code, out, _ := run(t, "", "grep", "h.i", "utf16.txt")
	if code != 0 || !strings.Contains(out, "Binary file") {
		t.Fatalf("grep on binary: code=%d out=%q", code, out)
	}
	// with -a the line is processed as text and printed
	code, out, _ = run(t, "", "grep", "-a", "h.i", "utf16.txt")
	if code != 0 || strings.Contains(out, "Binary file") || !strings.Contains(out, "h") {
		t.Fatalf("grep -a: code=%d out=%q", code, out)
	}
	// -a still exits 1 when the pattern does not match
	if code, _, _ := run(t, "", "grep", "-a", "zzz", "utf16.txt"); code != 1 {
		t.Fatalf("grep -a with no match should exit 1, got %d", code)
	}
}
