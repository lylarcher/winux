package core_test

import (
	"strings"
	"testing"

	"winux/core"
)

func TestCommandTableCoverage(t *testing.T) {
	required := []string{
		"cp", "rm", "touch", "vi", "mkdir", "mv", "sudo", "su", "grep",
		"cat", "more", "less", "tail", "head", "wc", "sort", "uniq", "cut", "tr",
		"ln", "echo", "pwd", "clear", "which", "whoami", "id", "uname", "date", "hostname", "cd",
		"sh", "df",
	}
	have := map[string]bool{}
	for _, n := range core.CommandNames() {
		have[n] = true
	}
	for _, name := range required {
		if !have[name] {
			t.Errorf("command %q is not registered", name)
		}
	}
	// vim is an alias for vi and must be executable as such
	if code, _, _ := run(t, "", "vim", "--version"); code != 0 {
		t.Error("alias vim is not executable")
	}
}

func TestHelpOfEveryCommandExitsZero(t *testing.T) {
	for _, name := range core.CommandNames() {
		code, out, errOut := run(t, "", name, "--help")
		if code != 0 {
			t.Errorf("%s --help exited %d (%s)", name, code, errOut)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%s --help printed no usage line: %q", name, out)
		}
		code, out, _ = run(t, "", name, "--version")
		if code != 0 || !strings.Contains(out, core.Version) {
			t.Errorf("%s --version = %q (code %d)", name, out, code)
		}
	}
}
