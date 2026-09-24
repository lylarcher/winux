package core_test

// open hands a path to the Windows shell. These tests stay window-free: the
// resolution/validation half is covered here via the exit code and messages,
// and the explorer.exe launch itself is a manual smoke (it pops a real File
// Explorer window).

import (
	"strings"
	"testing"

	"winux/core"
)

func TestOpenMissingPathFails(t *testing.T) {
	code, out, errOut := core.Execute("open", []string{`Z:\winux-no-such-dir-xyz`}, nil)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errOut, "No such file or directory") {
		t.Errorf("stderr=%q, want a 'No such file or directory' message", errOut)
	}
	if strings.Contains(out, "opening") {
		t.Errorf("stdout=%q, must not claim to have opened anything", out)
	}
}

func TestOpenBadOptionFails(t *testing.T) {
	code, _, errOut := core.Execute("open", []string{"--bogus"}, nil)
	if code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	if !strings.Contains(errOut, "bogus") {
		t.Errorf("stderr=%q, want the unrecognized option to be named", errOut)
	}
}

func TestOpenMultipleMissingReportsEach(t *testing.T) {
	// every target is processed independently: two missing paths must both be
	// reported (the loop does not stop at the first failure)
	code, _, errOut := core.Execute("open",
		[]string{`Z:\winux-no-such-a`, `Z:\winux-no-such-b`}, nil)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if n := strings.Count(errOut, "No such file or directory"); n != 2 {
		t.Errorf("stderr=%q, want two 'No such file or directory' messages, got %d", errOut, n)
	}
}
