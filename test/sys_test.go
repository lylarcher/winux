package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"winux/core"
)

func TestEchoPwdWhichDate(t *testing.T) {
	if _, out, _ := run(t, "", "echo", "hello", "world"); strings.TrimSpace(out) != "hello world" {
		t.Fatalf("echo = %q", out)
	}
	if _, out, _ := run(t, "", "echo", "-n", "x"); out != "x" {
		t.Fatalf("echo -n = %q", out)
	}
	if _, out, _ := run(t, "", "echo", "-e", `a\tb\n`); !strings.Contains(out, "a\tb") {
		t.Fatalf("echo -e = %q", out)
	}

	dir := chdirTemp(t)
	if _, out, _ := run(t, "", "pwd"); !strings.Contains(strings.TrimSpace(out), filepath.Base(dir)) {
		t.Fatalf("pwd = %q", out)
	}
	if _, out, _ := run(t, "", "uname", "-a"); !strings.Contains(out, "Windows") {
		t.Fatalf("uname -a = %q", out)
	}
	if _, out, _ := run(t, "", "date", "+%Y"); strings.TrimSpace(out) != time.Now().Format("2006") {
		t.Fatalf("date +%%Y = %q", out)
	}
	if code, out, _ := run(t, "", "which", "cmd.exe"); code != 0 || !strings.Contains(strings.ToLower(out), "cmd.exe") {
		t.Fatalf("which cmd.exe = %q (code %d)", out, code)
	}
	if code, _, _ := run(t, "", "which", "definitely-not-a-command-xyz"); code == 0 {
		t.Fatal("which of a nonexistent command should fail")
	}
	if _, out, _ := run(t, "", "whoami"); strings.TrimSpace(out) == "" {
		t.Fatal("whoami printed nothing")
	}
	if _, out, _ := run(t, "", "id"); !strings.Contains(out, "uid=") {
		t.Fatalf("id = %q", out)
	}
}

func TestCdResolveAndPrint(t *testing.T) {
	dir := chdirTemp(t)
	vol := filepath.VolumeName(dir)

	// absolute targets survive the round trip; forward slashes become back slashes
	if got, err := core.ResolveCdTarget(vol + `\x\y`); err != nil || got != vol+`\x\y` {
		t.Fatalf("resolve %q = %q, %v", vol+`\x\y`, got, err)
	}
	if got, err := core.ResolveCdTarget(vol + `/x/y`); err != nil || got != vol+`\x\y` {
		t.Fatalf("resolve forward slashes = %q, %v", got, err)
	}
	// "/" means the root of the current drive
	if got, _ := core.ResolveCdTarget("/"); got != vol+`\` {
		t.Fatalf("resolve / = %q, want %q", got, vol+`\`)
	}
	// "\" is the cmd.exe spelling of the same thing
	if got, _ := core.ResolveCdTarget(`\`); got != vol+`\` {
		t.Fatalf("resolve \\ = %q, want %q", got, vol+`\`)
	}
	// "~" expands to the home directory
	if got, err := core.ResolveCdTarget("~"); err != nil || got == "" {
		t.Fatalf("resolve ~ = %q, %v", got, err)
	}
	// a bare drive letter maps to its root
	drive := strings.ToUpper(vol[:1])
	if got, err := core.ResolveCdTarget(drive + ":"); err != nil || got != drive+`:\` {
		t.Fatalf("resolve %s: = %q, %v", drive, got, err)
	}

	// `winux cd` with no arguments prints the current directory
	code, out, _ := run(t, "", "cd")
	if code != 0 || strings.TrimSpace(out) != dir {
		t.Fatalf("cd printed %q (code %d), want %q", out, code, dir)
	}
	// relative target, including one with a space and forward slashes
	writeFile(t, filepath.Join("sub dir", "f"), "x")
	code, out, _ = run(t, "", "cd", "sub dir")
	if code != 0 || strings.TrimSpace(out) != filepath.Join(dir, "sub dir") {
		t.Fatalf("cd sub dir = %q (code %d)", out, code)
	}
	code, out, _ = run(t, "", "cd", "sub dir/..")
	if code != 0 || strings.TrimSpace(out) != dir {
		t.Fatalf("cd with forward slashes = %q (code %d)", out, code)
	}
	// a missing directory fails with exit 1
	if code, _, _ := run(t, "", "cd", "no-such-dir-xyz"); code != 1 {
		t.Fatalf("cd to a missing dir should exit 1, got %d", code)
	}
	// "cd -" uses the directory recorded in WINUX_PREVDIR (it must exist)
	prev := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(prev, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINUX_PREVDIR", prev)
	if code, out, _ := run(t, "", "cd", "-"); code != 0 || strings.TrimSpace(out) != prev {
		t.Fatalf("cd - = %q (code %d)", out, code)
	}
}
