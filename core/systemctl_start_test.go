//go:build windows

package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows/svc/mgr"
)

// `systemctl start` used to print the SCM's bare "Access is denied." — no
// operation, no file, no code. These cover the facts that message now carries.

func TestImagePathOfStripsTheArguments(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"C:\ProgramData\systemctl\hosts\demo.exe" svchost -unit "C:\x\demo.service"`, `C:\ProgramData\systemctl\hosts\demo.exe`},
		{`"C:\Program Files\app\app.exe" -run`, `C:\Program Files\app\app.exe`},
		{`C:\Windows\System32\ping.exe -n 300 127.0.0.1`, `C:\Windows\System32\ping.exe`},
		{`C:\Windows\System32\ping.exe`, `C:\Windows\System32\ping.exe`},
		{``, ``},
		{`  "C:\a.exe"  `, `C:\a.exe`},
		// PowerShell escaping handed to sc.exe as data: the whole registration
		// is unusable, but the message still has to name the intended file
		{`\"C:\ProgramData\systemctl\hosts\demo.exe\" svchost -unit \"C:\x\demo.service\"`, `C:\ProgramData\systemctl\hosts\demo.exe`},
		{`\"C:\a b\demo.exe\" -run`, `C:\a b\demo.exe`},
	}
	for _, c := range cases {
		if got := sysImagePathOf(c.in); got != c.want {
			t.Errorf("sysImagePathOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The escaping is the defect, not the file: the message must say so instead of
// reporting a perfectly present image as missing.
func TestImageFactCallsOutEscapedQuoting(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "demo.exe")
	if err := os.WriteFile(exe, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	escaped := `\"` + exe + `\" svchost -unit \"C:\x\demo.service\"`
	if !sysImageIsEscaped(escaped) {
		t.Fatal("this command line is meant to be the escaped form")
	}
	got := sysImageFact(escaped)
	if !strings.Contains(got, "转义") {
		t.Errorf("an escaped registration must be called out as such: got %q", got)
	}
	if !strings.Contains(got, exe) {
		t.Errorf("the message should still name the intended file: got %q", got)
	}
	// and a clean command line is never reported as escaped
	if sysImageIsEscaped(`"` + exe + `" svchost -unit x`) {
		t.Error("a clean registration must not be flagged as escaped")
	}
}

func TestImageFactNamesTheRealProblem(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "winux.exe")
	if err := os.WriteFile(exe, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a directory is the shape that produces "Access is denied" from the SCM, so
	// the message has to say "directory" rather than repeat the error code
	if got := sysImageFact(`"` + dir + `" svchost -unit x`); !strings.Contains(got, "是目录") {
		t.Errorf("a directory image must be called out: got %q", got)
	}
	if got := sysImageFact(`"` + filepath.Join(dir, "gone.exe") + `"`); !strings.Contains(got, "不存在") {
		t.Errorf("a missing image must be called out: got %q", got)
	}
	if got := sysImageFact(`"` + exe + `"`); !strings.Contains(got, "5 字节") {
		t.Errorf("an existing image must report its size: got %q", got)
	}
	empty := filepath.Join(dir, "empty.exe")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sysImageFact(`"` + empty + `"`); !strings.Contains(got, "0 字节") {
		t.Errorf("a 0-byte image must be called out: got %q", got)
	}
	if got := sysImageFact(""); !strings.Contains(got, "空") {
		t.Errorf("an empty command line must be called out: got %q", got)
	}
}

func TestImageNeedsRepairOnlyWhenTheImageCannotRun(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "winux.exe")
	if err := os.WriteFile(exe, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected := `"` + exe + `" svchost -unit x`

	cases := []struct {
		name    string
		current string
		want    bool
	}{
		{"a directory registration is repaired", `"` + dir + `" svchost -unit x`, true},
		{"a removed host is repaired", `"` + filepath.Join(dir, "gone.exe") + `"`, true},
		{"an empty registration is repaired", "", true},
		{"a 0-byte image is repaired", `"` + filepath.Join(dir, "empty.exe") + `"`, true},
		{"an escaped registration is repaired", `\"` + exe + `\" svchost -unit x`, true},
		{"a working registration is left alone", expected, false},
	}
	// the leftover of an interrupted copy: it exists, it is a file, and it can
	// never run
	if err := os.WriteFile(filepath.Join(dir, "empty.exe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := sysImageNeedsRepair(c.current, expected); got != c.want {
			t.Errorf("%s: sysImageNeedsRepair(%q) = %v, want %v", c.name, c.current, got, c.want)
		}
	}
	// nothing to repair with if the expected image itself is unusable
	if sysImageNeedsRepair("", `"`+filepath.Join(dir, "gone.exe")+`"`) {
		t.Error("repair must not rewrite a registration when the expected image is missing too")
	}
	if sysImageNeedsRepair("", "") {
		t.Error("an empty expectation must never trigger a rewrite")
	}
}

func TestStartHintExplainsTheCommonCodes(t *testing.T) {
	// the exact error the user saw, including through a %w wrapper
	denied := fmt.Errorf("start: %w", syscall.Errno(5))
	hint := sysStartHint(denied)
	for _, want := range []string{"SCM", "映像", "sc.exe qc"} {
		if !strings.Contains(hint, want) {
			t.Errorf("code 5 hint should mention %q: got %q", want, hint)
		}
	}
	cases := []struct {
		code syscall.Errno
		want string
	}{
		{1053, "svchost"},
		{1058, "禁用"},
		{1060, "不存在"},
		{1072, "标记为删除"},
	}
	for _, c := range cases {
		if got := sysStartHint(c.code); !strings.Contains(got, c.want) {
			t.Errorf("code %d hint should mention %q: got %q", c.code, c.want, got)
		}
	}
	if got := sysStartHint(errors.New("some other failure")); got != "" {
		t.Errorf("an unknown error should not get a canned hint: got %q", got)
	}
}

// The message the user reads: it must name the file, say what is wrong with it,
// and keep the original Win32 text so the error stays searchable.
func TestStartFailureMessageCarriesTheFacts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SYSTEMCTL_ROOT", filepath.Join(dir, "root"))
	t.Setenv("SYSTEMCTL_UNIT_PATH", filepath.Join(dir, "no-units"))

	cfg := &mgr.Config{BinaryPathName: `"` + dir + `" svchost -unit "C:\x\demo.service"`}
	msg := sysStartFailureText("demo", cfg, syscall.Errno(5), `映像 "C:\old\demo.exe" → "C:\new\demo.exe"`)

	for _, want := range []string{
		"Access is denied.", // the raw Win32 text stays first
		"服务映像: " + dir,      // and the file it is really about
		"是目录",               // with the reason, not just the code
		"已修正服务注册",           // what the repair did, if it ran
		"权限: ",              // elevation, because 5 means different things
		"提示: ",              // and the next step
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("start failure message should contain %q:\n%s", want, msg)
		}
	}
	// no facts available (Config failed): still the error plus the hint
	bare := sysStartFailureText("demo", nil, syscall.Errno(1053), "")
	if !strings.Contains(bare, "30 秒") || strings.Contains(bare, "服务映像") {
		t.Errorf("a config-less failure must degrade cleanly: %q", bare)
	}
}
