package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadAndTail(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "n.txt", "1\n2\n3\n4\n5\n")

	if _, out, _ := run(t, "", "head", "-n", "2", "n.txt"); strings.TrimSpace(out) != "1\n2" {
		t.Fatalf("head -n 2 = %q", out)
	}
	if _, out, _ := run(t, "", "head", "-2", "n.txt"); strings.TrimSpace(out) != "1\n2" {
		t.Fatalf("head -2 = %q", out)
	}
	if _, out, _ := run(t, "", "head", "-n", "-2", "n.txt"); strings.TrimSpace(out) != "1\n2\n3" {
		t.Fatalf("head -n -2 = %q", out)
	}
	if _, out, _ := run(t, "", "head", "-n", "+3", "n.txt"); strings.TrimSpace(out) != "3\n4\n5" {
		t.Fatalf("head -n +3 = %q", out)
	}
	if _, out, _ := run(t, "", "head", "-c", "3", "n.txt"); out != "1\n2" {
		t.Fatalf("head -c 3 = %q", out)
	}

	if _, out, _ := run(t, "", "tail", "-n", "2", "n.txt"); strings.TrimSpace(out) != "4\n5" {
		t.Fatalf("tail -n 2 = %q", out)
	}
	if _, out, _ := run(t, "", "tail", "-2", "n.txt"); strings.TrimSpace(out) != "4\n5" {
		t.Fatalf("tail -2 = %q", out)
	}
	if _, out, _ := run(t, "", "tail", "-n", "+3", "n.txt"); strings.TrimSpace(out) != "3\n4\n5" {
		t.Fatalf("tail -n +3 = %q", out)
	}
	if _, out, _ := run(t, "", "tail", "-c", "2", "n.txt"); out != "5\n" {
		t.Fatalf("tail -c 2 = %q", out)
	}
	// the default is ten lines
	writeFile(t, "m.txt", strings.Repeat("x\n", 20))
	if _, out, _ := run(t, "", "tail", "m.txt"); strings.Count(out, "\n") != 10 {
		t.Fatalf("tail default should print 10 lines, got %d", strings.Count(out, "\n"))
	}
	// multiple files get GNU headers
	if _, out, _ := run(t, "", "head", "-n", "1", "n.txt", "m.txt"); !strings.Contains(out, "==> n.txt <==") {
		t.Fatalf("head multi-file header missing: %q", out)
	}
}

func TestWc(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "w.txt", "one two\nthree\n")
	if _, out, _ := run(t, "", "wc", "-lwc", "w.txt"); !strings.HasPrefix(strings.TrimSpace(out), "2") {
		t.Fatalf("wc -lwc = %q", out)
	}
	if _, out, _ := run(t, "", "wc", "-l", "w.txt"); strings.Fields(out)[0] != "2" {
		t.Fatalf("wc -l = %q", out)
	}
	if _, out, _ := run(t, "", "wc", "-w", "w.txt"); strings.Fields(out)[0] != "3" {
		t.Fatalf("wc -w = %q", out)
	}
	if _, out, _ := run(t, "", "wc", "-L", "w.txt"); strings.Fields(out)[0] != "7" {
		t.Fatalf("wc -L = %q", out)
	}
	// CRLF line endings must not inflate the max line length
	writeFile(t, "crlf.txt", "one two\r\nthree\r\n")
	if _, out, _ := run(t, "", "wc", "-L", "crlf.txt"); strings.Fields(out)[0] != "7" {
		t.Fatalf("wc -L crlf = %q", out)
	}
	if _, out, _ := run(t, "", "wc", "-l", "w.txt", "crlf.txt"); !strings.Contains(out, "total") {
		t.Fatalf("wc totals missing: %q", out)
	}
}

func TestCat(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "c.txt", "a\n\nb\n")
	if _, out, _ := run(t, "", "cat", "-n", "c.txt"); !strings.Contains(out, "     1\ta") {
		t.Fatalf("cat -n = %q", out)
	}
	if _, out, _ := run(t, "", "cat", "-b", "c.txt"); strings.Count(out, "\t") != 2 || !strings.Contains(out, "\n\n") {
		t.Fatalf("cat -b = %q", out)
	}
	if _, out, _ := run(t, "", "cat", "-s", "c.txt"); strings.Contains(out, "\n\n\n") {
		t.Fatalf("cat -s should squeeze blank lines: %q", out)
	}
	if _, out, _ := run(t, "", "cat", "-E", "c.txt"); !strings.Contains(out, "a$") {
		t.Fatalf("cat -E = %q", out)
	}
	if _, out, _ := run(t, "", "cat", "c.txt", "c.txt"); strings.Count(out, "a") != 2 {
		t.Fatalf("cat of the same file twice = %q", out)
	}
}

func TestSortUniqCutTr(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "s.txt", "10\n2\n33\n")
	if _, out, _ := run(t, "", "sort", "-n", "s.txt"); strings.Fields(out)[0] != "2" {
		t.Fatalf("sort -n = %q", out)
	}
	if _, out, _ := run(t, "", "sort", "-nr", "s.txt"); strings.Fields(out)[0] != "33" {
		t.Fatalf("sort -nr = %q", out)
	}
	if _, out, _ := run(t, "", "sort", "-r", "s.txt"); strings.Fields(out)[0] != "33" {
		t.Fatalf("sort -r = %q", out)
	}

	writeFile(t, "u.txt", "b\nb\na\nb\n")
	if _, out, _ := run(t, "", "uniq", "-c", "u.txt"); !strings.Contains(out, "2 b") {
		t.Fatalf("uniq -c = %q", out)
	}
	if _, out, _ := run(t, "", "uniq", "-d", "u.txt"); strings.TrimSpace(out) != "b" {
		t.Fatalf("uniq -d = %q", out)
	}
	if _, out, _ := run(t, "", "uniq", "-u", "u.txt"); strings.TrimSpace(out) != "a\nb" {
		t.Fatalf("uniq -u = %q", out)
	}

	writeFile(t, "csv.txt", "a,b,c\n1,2,3\n")
	if _, out, _ := run(t, "", "cut", "-d", ",", "-f", "2", "csv.txt"); strings.Fields(out)[0] != "b" {
		t.Fatalf("cut -f 2 = %q", out)
	}
	if _, out, _ := run(t, "", "cut", "-c", "1-3", "csv.txt"); strings.Fields(out)[0] != "a,b" {
		t.Fatalf("cut -c 1-3 = %q", out)
	}

	if _, out, _ := run(t, "hello\n", "tr", "a-z", "A-Z"); strings.TrimSpace(out) != "HELLO" {
		t.Fatalf("tr a-z A-Z = %q", out)
	}
	if _, out, _ := run(t, "aabbcc\n", "tr", "-s", "ab"); strings.TrimSpace(out) != "abcc" {
		t.Fatalf("tr -s ab = %q", out)
	}
	if _, out, _ := run(t, "hello\n", "tr", "-d", "l"); strings.TrimSpace(out) != "heo" {
		t.Fatalf("tr -d l = %q", out)
	}
}

// PathSeparatorsAcrossCommands verifies that every command that touches paths
// accepts forward slashes, back slashes and mixed forms on Windows.
func TestPathSeparatorsAcrossCommands(t *testing.T) {
	chdirTemp(t)
	writeFile(t, filepath.Join("src", "a.txt"), "alpha")
	writeFile(t, filepath.Join("src", "sub", "b.txt"), "beta")

	// file operands accept both separators
	if code, out, _ := run(t, "", "cat", "src/a.txt"); code != 0 || !strings.Contains(out, "alpha") {
		t.Fatalf("cat with a forward slash = %q (code %d)", out, code)
	}
	if code, out, _ := run(t, "", "cat", `src\a.txt`); code != 0 || !strings.Contains(out, "alpha") {
		t.Fatalf("cat with a backslash = %q (code %d)", out, code)
	}
	if code, out, _ := run(t, "", "head", "-n1", "src/sub/b.txt"); code != 0 || !strings.Contains(out, "beta") {
		t.Fatalf("head = %q (code %d)", out, code)
	}
	if code, _, _ := run(t, "", "wc", "-l", "src/a.txt"); code != 0 {
		t.Fatal("wc failed")
	}
	if code, out, _ := run(t, "", "grep", "-n", "alpha", "src/a.txt"); code != 0 || !strings.Contains(out, "1:alpha") {
		t.Fatalf("grep = %q (code %d)", out, code)
	}
	if code, out, _ := run(t, "", "grep", "-r", "beta", "src/"); code != 0 || !strings.Contains(out, "beta") {
		t.Fatalf("grep -r with a trailing slash = %q (code %d)", out, code)
	}

	// mixed separators in directory creation
	if code, _, errOut := run(t, "", "mkdir", "-p", `mix\one/two`); code != 0 {
		t.Fatalf("mkdir with mixed separators failed: %s", errOut)
	}
	if !isDir(filepath.Join("mix", "one", "two")) {
		t.Fatal("mkdir -p mix\\one/two did not create the full path")
	}

	// cp into an existing directory using a trailing forward slash
	if code, _, errOut := run(t, "", "mkdir", "-p", "dst"); code != 0 {
		t.Fatal(errOut)
	}
	if code, _, errOut := run(t, "", "cp", "src/a.txt", "dst/"); code != 0 {
		t.Fatalf("cp into dst/ failed: %s", errOut)
	}
	if readFile(t, filepath.Join("dst", "a.txt")) != "alpha" {
		t.Fatal("cp src/a.txt dst/ did not land in dst\\a.txt")
	}
	// cp -t with a backslash target dir and a backslash source
	if code, _, errOut := run(t, "", "cp", "-t", `dst\`, `src/sub/b.txt`); code != 0 {
		t.Fatalf("cp -t dst\\ failed: %s", errOut)
	}
	if readFile(t, filepath.Join("dst", "b.txt")) != "beta" {
		t.Fatal("cp -t dst\\ did not copy into the directory")
	}
	// cp a b\ where b\ does not exist must be refused like GNU
	if code, _, _ := run(t, "", "cp", "src/a.txt", `missing\`); code == 0 {
		t.Fatal("cp to a nonexistent trailing-slash target should fail")
	}

	// mv with a forward-slash target directory
	if err := os.Mkdir("moved", 0o777); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(t, "", "mv", "src/sub/b.txt", "moved/"); code != 0 {
		t.Fatalf("mv into moved/ failed: %s", errOut)
	}
	if readFile(t, filepath.Join("moved", "b.txt")) != "beta" {
		t.Fatal("mv src/sub/b.txt moved/ did not land in moved\\b.txt")
	}
	if code, _, _ := run(t, "", "mv", "src/a.txt", `gone\`); code == 0 {
		t.Fatal("mv to a nonexistent trailing-slash target should fail")
	}

	// touch -r and date -r with mixed separators
	if code, _, errOut := run(t, "", "touch", "-r", `src\a.txt`, "mix/one/two/stamp.txt"); code != 0 {
		t.Fatalf("touch -r with mixed separators failed: %s", errOut)
	}
	if !fileExists(filepath.Join("mix", "one", "two", "stamp.txt")) {
		t.Fatal("touch -r did not create the target")
	}
	if code, _, errOut := run(t, "", "date", "-r", "mix/one/two/stamp.txt", "+%F"); code != 0 {
		t.Fatalf("date -r with a forward slash failed: %s", errOut)
	}

	// rm accepts both separators and still refuses a forward-slash drive root
	if code, _, errOut := run(t, "", "rm", "-rf", `src\sub`); code != 0 {
		t.Fatalf("rm -rf src\\sub failed: %s", errOut)
	}
	if fileExists(filepath.Join("src", "sub")) {
		t.Fatal("rm -rf src\\sub left the directory behind")
	}
	if code, _, _ := run(t, "", "rm", "-rf", "C:/"); code == 0 {
		t.Fatal("rm -rf C:/ (forward slashes) must be refused")
	}
	if code, _, errOut := run(t, "", "rm", "-f", "mix/one/two/stamp.txt"); code != 0 {
		t.Fatalf("rm -f with a forward slash failed: %s", errOut)
	}

	// less dumps a file referenced with a backslash
	if code, out, _ := run(t, "", "less", `src\a.txt`); code != 0 || !strings.Contains(out, "alpha") {
		t.Fatalf("less = %q (code %d)", out, code)
	}
	// pwd -P still works
	if code, out, _ := run(t, "", "pwd", "-P"); code != 0 || out == "" {
		t.Fatalf("pwd -P failed: code=%d out=%q", code, out)
	}
}
