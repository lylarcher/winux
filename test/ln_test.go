package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withLnDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(old) })
	writeFile(t, "src.txt", "hello")
	writeFile(t, "exist.txt", "old")
	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func fileContent(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// canSymlink probes whether the current user may create symbolic links
// (requires Developer Mode or an elevated shell on Windows).
func canSymlink(t *testing.T) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.Symlink("target", probe); err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

func TestLnHardlink(t *testing.T) {
	withLnDir(t)
	if code, _, errOut := run(t, "", "ln", "src.txt", "hard"); code != 0 {
		t.Fatalf("ln hard failed: %s", errOut)
	}
	if !fileExists("hard") || isDir("hard") {
		t.Fatal("hard link not created")
	}
	if fileContent(t, "hard") != "hello" {
		t.Fatal("hard link content wrong")
	}
	// verbose goes to stdout
	code, out, _ := run(t, "", "ln", "-v", "src.txt", "hard2")
	if code != 0 || !strings.Contains(out, "->") {
		t.Fatalf("ln -v: code=%d out=%q", code, out)
	}
}

func TestLnSymlink(t *testing.T) {
	withLnDir(t)
	if !canSymlink(t) {
		t.Skip("symlink creation needs Developer Mode / elevated shell")
	}
	if code, _, errOut := run(t, "", "ln", "-s", "src.txt", "slink"); code != 0 {
		t.Fatalf("ln -s failed: %s", errOut)
	}
	if fi, err := os.Lstat("slink"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink not created")
	}
	// -r computes a relative target automatically
	if code, _, errOut := run(t, "", "ln", "-s", "-r", "src.txt", "sub/relink"); code != 0 {
		t.Fatalf("ln -s -r failed: %s", errOut)
	}
	if fi, err := os.Lstat("sub/relink"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("relative symlink not created")
	}
	if target, err := os.Readlink("sub/relink"); err != nil || strings.Contains(target, `:\`) {
		t.Fatalf("ln -s -r should store a relative target, got %q", target)
	}
}

func TestLnTargetDirectory(t *testing.T) {
	withLnDir(t)
	if !canSymlink(t) {
		t.Skip("symlink creation needs Developer Mode / elevated shell")
	}
	if code, _, errOut := run(t, "", "ln", "-s", "-t", "sub", "src.txt"); code != 0 {
		t.Fatalf("ln -t failed: %s", errOut)
	}
	if fi, err := os.Lstat(filepath.Join("sub", "src.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("ln -t did not create link inside sub/")
	}
	// single target + directory operand
	if code, _, errOut := run(t, "", "ln", "-s", "src.txt", "sub"); code != 0 {
		t.Fatalf("ln -s src.txt sub failed: %s", errOut)
	}
}

func TestLnForceBackupInteractive(t *testing.T) {
	withLnDir(t)
	// -f overwrites
	if code, _, errOut := run(t, "", "ln", "-f", "src.txt", "exist.txt"); code != 0 {
		t.Fatalf("ln -f failed: %s", errOut)
	}
	if fileContent(t, "exist.txt") != "hello" {
		t.Fatal("ln -f did not overwrite")
	}
	// reset makes exist.txt an independent file again (a hard link would
	// share its inode with src.txt, so truncation would alter src.txt too)
	reset := func() {
		os.Remove("exist.txt")
		writeFile(t, "exist.txt", "old")
	}
	// -i answering "n": exit 0 but nothing replaced
	reset()
	if code, _, _ := run(t, "n\n", "ln", "-i", "src.txt", "exist.txt"); code != 0 {
		t.Fatalf("ln -i n should exit 0 (no replacement): code=%d", code)
	}
	if fileContent(t, "exist.txt") != "old" {
		t.Fatal("ln -i with 'n' must not replace")
	}
	// -i answering "y": replaced
	if code, _, _ := run(t, "y\n", "ln", "-i", "src.txt", "exist.txt"); code != 0 {
		t.Fatalf("ln -i y should succeed: code=%d", code)
	}
	if fileContent(t, "exist.txt") != "hello" {
		t.Fatal("ln -i with 'y' should replace")
	}
	// -b backs up instead of failing on existing destination
	reset()
	if code, _, errOut := run(t, "", "ln", "-b", "src.txt", "exist.txt"); code != 0 {
		t.Fatalf("ln -b failed: %s", errOut)
	}
	if !fileExists("exist.txt~") {
		t.Fatal("ln -b did not create backup 'exist.txt~'")
	}
	if fileContent(t, "exist.txt~") != "old" {
		t.Fatalf("ln -b backup content should be the old content, got %q", fileContent(t, "exist.txt~"))
	}
	if fileContent(t, "exist.txt") != "hello" {
		t.Fatal("ln -b did not replace original")
	}
}

func TestLnNoTargetDirAndDirectory(t *testing.T) {
	withLnDir(t)
	// -T: treat dest as file even if it is a directory → refuse to clobber a dir
	code, _, errOut := run(t, "", "ln", "-T", "src.txt", "sub")
	if code != 1 || !strings.Contains(errOut, "directory") {
		t.Fatalf("ln -T into a dir: code=%d err=%q", code, errOut)
	}
	// -d: refuse on Windows
	code, _, errOut = run(t, "", "ln", "-d", "src.txt", "dir-hard")
	if code != 1 || !strings.Contains(errOut, "Windows") {
		t.Fatalf("ln -d: code=%d err=%q", code, errOut)
	}
}

func TestLnErrors(t *testing.T) {
	withLnDir(t)
	if code, _, _ := run(t, "", "ln", "src.txt"); code != 1 {
		t.Fatal("ln single operand should fail")
	}
	if code, _, _ := run(t, "", "ln", "nonexistent", "link"); code != 1 {
		t.Fatal("ln nonexistent source should fail")
	}
	// multi-sources require a directory destination
	writeFile(t, "second.txt", "two")
	if code, _, errOut := run(t, "", "ln", "src.txt", "second.txt", "not-a-dir"); code != 1 || !strings.Contains(errOut, "not a directory") {
		t.Fatalf("ln multi-source into file: code=%d err=%q", code, errOut)
	}
}

func TestLnNoDereference(t *testing.T) {
	withLnDir(t)
	if !canSymlink(t) {
		t.Skip("symlink creation needs Developer Mode / elevated shell")
	}
	// sub-link is a symlink pointing at the directory "sub"
	if code, _, errOut := run(t, "", "ln", "-s", "sub", "sub-link"); code != 0 {
		t.Fatalf("ln -s sub-link: %s", errOut)
	}
	// Without -n, the link would be created inside sub/ via the symlink.
	// With -n -f the symlink itself is replaced by a hard link to src.txt.
	if code, _, errOut := run(t, "", "ln", "-f", "-n", "src.txt", "sub-link"); code != 0 {
		t.Fatalf("ln -fn: %s", errOut)
	}
	if fi, err := os.Lstat("sub-link"); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("ln -fn should have replaced the symlink with a hard link")
	}
	if fileContent(t, "sub-link") != "hello" {
		t.Fatal("ln -fn content wrong")
	}
}