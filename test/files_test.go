package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------------- cp ---

func TestCpRecursiveGlobAndNoClobber(t *testing.T) {
	chdirTemp(t)
	writeFile(t, filepath.Join("src", "a.txt"), "A")
	writeFile(t, filepath.Join("src", "sub", "b.txt"), "B")
	writeFile(t, filepath.Join("src", "sub", "c.log"), "C")

	if code, _, errOut := run(t, "", "cp", "-r", "src", "dst"); code != 0 {
		t.Fatalf("cp -r failed: code=%d err=%s", code, errOut)
	}
	if got := readFile(t, filepath.Join("dst", "sub", "b.txt")); got != "B" {
		t.Fatalf("recursive copy content = %q", got)
	}

	// glob expansion: cp *.txt out\ must pick up the .txt files only
	writeFile(t, "x.txt", "X")
	writeFile(t, "y.log", "Y")
	if err := os.Mkdir("out", 0o777); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(t, "", "cp", "*.txt", "out"); code != 0 {
		t.Fatalf("cp glob failed: code=%d err=%s", code, errOut)
	}
	if got := readFile(t, filepath.Join("out", "x.txt")); got != "X" {
		t.Fatalf("globbed copy content = %q", got)
	}
	if fileExists(filepath.Join("out", "y.log")) {
		t.Fatal("*.txt should not have matched y.log")
	}

	// -n must not overwrite, -f with a fresh source must
	writeFile(t, "keep.txt", "new")
	if code, _, _ := run(t, "", "cp", "-n", "keep.txt", "out"); code != 0 {
		t.Fatal("cp -n reported an error")
	}
	if got := readFile(t, filepath.Join("out", "keep.txt")); got != "new" {
		t.Fatalf("cp -n copied into the directory wrongly: %q", got)
	}
	if code, _, _ := run(t, "", "cp", "-f", "keep.txt", "out"); code != 0 {
		t.Fatal("cp -f reported an error")
	}
	if got := readFile(t, filepath.Join("out", "keep.txt")); got != "new" {
		t.Fatalf("cp -f content = %q", got)
	}

	// copying a directory into itself must fail
	if code, _, _ := run(t, "", "cp", "-r", "src", filepath.Join("src", "inner")); code == 0 {
		t.Fatal("copying a directory into itself should fail")
	}
}

func TestCpPreserveAndVerbose(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "a.txt", "hello")
	past := time.Date(2020, 3, 4, 5, 6, 7, 0, time.Local)
	if err := os.Chtimes("a.txt", past, past); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "", "cp", "-pv", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("cp -pv failed: %s", errOut)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") {
		t.Fatalf("verbose output missing: %q", out)
	}
	st, err := os.Stat("b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(past) {
		t.Fatalf("timestamp not preserved: %v", st.ModTime())
	}
	if got := readFile(t, "b.txt"); got != "hello" {
		t.Fatalf("content = %q", got)
	}
}

func TestCpTargetDirectory(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "one.txt", "1")
	writeFile(t, "two.txt", "2")
	if err := os.Mkdir("dest", 0o777); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(t, "", "cp", "-t", "dest", "one.txt", "two.txt"); code != 0 {
		t.Fatalf("cp -t failed: %s", errOut)
	}
	if readFile(t, filepath.Join("dest", "one.txt")) != "1" || readFile(t, filepath.Join("dest", "two.txt")) != "2" {
		t.Fatal("cp -t did not copy both files")
	}
}

// --------------------------------------------------------------------- rm ---

func TestRmRecursiveForceAndSafety(t *testing.T) {
	chdirTemp(t)
	writeFile(t, filepath.Join("tree", "sub", "f.txt"), "x")
	if code, _, errOut := run(t, "", "rm", "-rf", "tree"); code != 0 {
		t.Fatalf("rm -rf failed: %s", errOut)
	}
	if fileExists("tree") {
		t.Fatal("rm -rf left the directory behind")
	}
	// -f on a missing file is not an error, without -f it is
	if code, _, _ := run(t, "", "rm", "-f", "missing.txt"); code != 0 {
		t.Fatal("rm -f on a missing file should succeed")
	}
	if code, _, _ := run(t, "", "rm", "missing.txt"); code == 0 {
		t.Fatal("rm on a missing file should fail")
	}
	// a directory needs -r/-d
	if err := os.Mkdir("dir", 0o777); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := run(t, "", "rm", "dir"); code == 0 {
		t.Fatal("rm on a directory should fail without -r/-d")
	}
	writeFile(t, filepath.Join("dir", "f.txt"), "x")
	if code, _, _ := run(t, "", "rm", "-d", "dir"); code == 0 {
		t.Fatal("rm -d on a non-empty directory should fail")
	}
	// read-only files are removed (NTFS attribute is cleared first)
	writeFile(t, "ro.txt", "ro")
	if err := os.Chmod("ro.txt", 0o444); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(t, "", "rm", "-f", "ro.txt"); code != 0 {
		t.Fatalf("rm -f on a read-only file failed: %s", errOut)
	}
	// the root of a drive is never removed
	if code, _, errOut := run(t, "", "rm", "-rf", `C:\`); code == 0 {
		t.Fatalf("rm -rf C:\\ must be refused (%s)", errOut)
	}
	if code, _, _ := run(t, "", "rm", "-rf", "."); code == 0 {
		t.Fatal("rm -rf . must be refused")
	}
}

func TestRmVerboseAndGlob(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "a.log", "a")
	writeFile(t, "b.log", "b")
	writeFile(t, "c.txt", "c")
	code, out, errOut := run(t, "", "rm", "-v", "*.log")
	if code != 0 {
		t.Fatalf("rm -v failed: %s", errOut)
	}
	if !strings.Contains(out, "a.log") || !strings.Contains(out, "b.log") {
		t.Fatalf("rm -v did not mention the removed files: %q", out)
	}
	if strings.Contains(out, "c.txt") {
		t.Fatalf("rm -v should not mention the non-matching file: %q", out)
	}
	if fileExists("a.log") || fileExists("b.log") {
		t.Fatal("rm -v left a .log file behind")
	}
	if !fileExists("c.txt") {
		t.Fatal("c.txt should not have been removed")
	}
}

// --------------------------------------------------------------------- mv ---

func TestMvRenameOverwriteAndGlob(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "one.txt", "1")
	writeFile(t, "two.txt", "2")
	if err := os.Mkdir("dir", 0o777); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(t, "", "mv", "one.txt", "renamed.txt"); code != 0 {
		t.Fatalf("mv rename failed: %s", errOut)
	}
	if readFile(t, "renamed.txt") != "1" || fileExists("one.txt") {
		t.Fatal("mv did not rename one.txt -> renamed.txt")
	}
	if code, _, errOut := run(t, "", "mv", "two.txt", "dir"); code != 0 {
		t.Fatalf("mv into dir failed: %s", errOut)
	}
	if readFile(t, filepath.Join("dir", "two.txt")) != "2" {
		t.Fatal("mv two.txt dir did not land in dir\\two.txt")
	}
	// -n never overwrites, then a plain mv does
	writeFile(t, "src.txt", "new")
	if code, _, _ := run(t, "", "mv", "-n", "src.txt", "renamed.txt"); code != 0 {
		t.Fatal("mv -n reported an error")
	}
	if readFile(t, "renamed.txt") != "1" {
		t.Fatal("mv -n overwrote the destination")
	}
	if code, _, errOut := run(t, "", "mv", "src.txt", "renamed.txt"); code != 0 {
		t.Fatalf("mv overwrite failed: %s", errOut)
	}
	if readFile(t, "renamed.txt") != "new" {
		t.Fatal("mv did not overwrite renamed.txt")
	}
	// moving a whole tree
	writeFile(t, filepath.Join("tree", "sub", "f"), "x")
	if code, _, errOut := run(t, "", "mv", "tree", "moved"); code != 0 {
		t.Fatalf("mv tree failed: %s", errOut)
	}
	if readFile(t, filepath.Join("moved", "sub", "f")) != "x" || fileExists("tree") {
		t.Fatal("mv tree moved did not move the whole tree")
	}
}

// ---------------------------------------------------------------- mkdir ----

func TestMkdirAndTouch(t *testing.T) {
	chdirTemp(t)
	if code, _, errOut := run(t, "", "mkdir", "-p", "a/b/c"); code != 0 {
		t.Fatalf("mkdir -p failed: %s", errOut)
	}
	if !isDir(filepath.Join("a", "b", "c")) {
		t.Fatal("mkdir -p a/b/c did not create the full path")
	}
	// -pv on an existing tree is silent but successful
	if code, _, errOut := run(t, "", "mkdir", "-pv", "a/b/c"); code != 0 {
		t.Fatalf("mkdir -pv on an existing tree failed: %s", errOut)
	}
	// without -p an existing dir and a missing parent both fail
	if code, _, _ := run(t, "", "mkdir", "a/b/c"); code == 0 {
		t.Fatal("mkdir on an existing directory should fail without -p")
	}
	if code, _, _ := run(t, "", "mkdir", "d/e"); code == 0 {
		t.Fatal("mkdir with a missing parent should fail without -p")
	}
	// touch -t sets an exact local time
	if code, _, errOut := run(t, "", "touch", "-t", "202401020304.05", "stamp.txt"); code != 0 {
		t.Fatalf("touch -t failed: %s", errOut)
	}
	st, err := os.Stat("stamp.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local)
	if !st.ModTime().Equal(want) {
		t.Fatalf("touch -t mtime = %v, want %v", st.ModTime(), want)
	}
	// -c must not create a missing file
	if code, _, _ := run(t, "", "touch", "-c", "nope.txt"); code != 0 {
		t.Fatal("touch -c on a missing file should succeed")
	}
	if fileExists("nope.txt") {
		t.Fatal("touch -c created a missing file")
	}
	// -d parses a free-form date
	if code, _, errOut := run(t, "", "touch", "-d", "2022-05-06 07:08:09", "d.txt"); code != 0 {
		t.Fatalf("touch -d failed: %s", errOut)
	}
	st, err = os.Stat("d.txt")
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2022, 5, 6, 7, 8, 9, 0, time.Local)
	if !st.ModTime().Equal(want) {
		t.Fatalf("touch -d mtime = %v, want %v", st.ModTime(), want)
	}
}
