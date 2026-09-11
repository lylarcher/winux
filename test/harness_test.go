// Package core_test holds the winux test suite. All tests live in this
// directory (winux/test) and exercise the engine through the exported
// winux/core API: commands run in-process via core.Execute with the standard
// streams captured, exactly as the shell would invoke them.
package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"winux/core"
)

// run executes a registered command in-process with stdin fed from a string.
// It returns (exit code, stdout, stderr).
func run(t *testing.T, stdin string, name string, args ...string) (int, string, string) {
	t.Helper()
	return core.Execute(name, args, strings.NewReader(stdin))
}

// chdirTemp switches into a fresh temporary directory for the test.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
