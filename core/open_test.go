package core

// open resolves paths like `cd` does and hands them to the Windows shell. The
// resolution/validation half is pure and tested here window-free; the actual
// explorer.exe launch is verified manually and by the integration tests.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenResolvesTargets(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		target string
		want   string // exact absolute path
	}{
		{".", cwd},
		{"./", cwd},
		{"..", filepath.Dir(cwd)},
		{"../", filepath.Dir(cwd)},
		{filepath.Dir(cwd), filepath.Dir(cwd)},
	}
	for _, c := range cases {
		got, err := resolveOpenPath(c.target)
		if err != nil {
			t.Errorf("resolveOpenPath(%q): %v", c.target, err)
			continue
		}
		if !strings.EqualFold(got, c.want) {
			t.Errorf("resolveOpenPath(%q) = %q, want %q", c.target, got, c.want)
		}
	}
}

func TestOpenResolvesTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	for _, target := range []string{"~", "~/"} {
		got, err := resolveOpenPath(target)
		if err != nil {
			t.Errorf("resolveOpenPath(%q): %v", target, err)
			continue
		}
		if !strings.EqualFold(got, home) {
			t.Errorf("resolveOpenPath(%q) = %q, want %q", target, got, home)
		}
	}
}

func TestOpenRejectsMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-entry-xyz")
	_, err := resolveOpenPath(missing)
	if err == nil {
		t.Fatal("expected an error for a missing path")
	}
	if !strings.Contains(err.Error(), "No such file or directory") {
		t.Errorf("error = %q, want a 'No such file or directory' message", err)
	}
}
