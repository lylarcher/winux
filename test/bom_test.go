package core_test

// Windows PowerShell 5.1 prepends a UTF-8 BOM to native-process stdin when the
// console code page is 65001 (which winux switches the console to), so
// line-based tools must not let the BOM glue itself to the first line.
// These tests pin that behavior down with BOM-prefixed stdin.

import (
	"strings"
	"testing"

	"winux/core"
)

func TestStdinBOMStripped(t *testing.T) {
	bom := "\ufeff"
	cases := []struct {
		name        string
		args        []string
		stdin, want string
	}{
		{"sort -n", []string{"sort", "-n"}, bom + "10\n2\n33\n", "2\n10\n33\n"},
		{"uniq -c dedups first line", []string{"uniq", "-c"}, bom + "a\na\nb\n", "      2 a\n      1 b\n"},
		{"grep matches first line", []string{"grep", "hello"}, bom + "hello\nworld\n", "hello\n"},
		{"head shows first line clean", []string{"head", "-n", "1"}, bom + "first\n", "first\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := core.Execute(tc.args[0], tc.args[1:], strings.NewReader(tc.stdin))
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, errOut)
			}
			if out != tc.want {
				t.Fatalf("out=%q want %q", out, tc.want)
			}
		})
	}
}

func TestShStripsBOMFromScript(t *testing.T) {
	code, out, errOut := core.Execute("sh", nil, strings.NewReader("\ufeffecho bom-ok\n"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if strings.TrimSpace(out) != "bom-ok" {
		t.Fatalf("out=%q", out)
	}
}

// A BOM mid-stream is data, not an encoding marker: only the first line of
// the whole stream may be stripped.
func TestBOMOnlyStrippedOnce(t *testing.T) {
	code, out, errOut := core.Execute("uniq", []string{"-c"}, strings.NewReader("a\n\ufeffa\nb\n"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "      1 a\n      1 \ufeffa\n      1 b\n" {
		t.Fatalf("out=%q", out)
	}
}
