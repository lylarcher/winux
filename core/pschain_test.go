package core

// Tests for the PowerShell 5.1 chain rewriter. The rewriter must never touch a
// line it does not fully understand, must preserve every character it moves
// (no re-quoting), and must produce PowerShell whose behaviour matches the
// shell's && / || (that part is checked by evaluating the output in a real
// PowerShell session, see TestPSChainEvaluatesLikeTheShell).

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPSChainRewriteChains(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		changed bool
	}{
		{
			in:      "a && b",
			want:    "a; $__winuxChainOk = $?; if ($__winuxChainOk) { b; $__winuxChainOk = $? }",
			changed: true,
		},
		{
			in:      "a || b",
			want:    "a; $__winuxChainOk = $?; if (-not $__winuxChainOk) { b; $__winuxChainOk = $? }",
			changed: true,
		},
		{
			// left to right, exactly like the shell: c runs if a failed OR b failed
			in: "a && b || c",
			want: "a; $__winuxChainOk = $?; if ($__winuxChainOk) { b; $__winuxChainOk = $? }" +
				"; if (-not $__winuxChainOk) { c; $__winuxChainOk = $? }",
			changed: true,
		},
		{
			// the real-world line that motivated this
			in:      `nohup kill -9 34780 && kill -9 42356 && mv .\RdpAgent.exe .\rdp-agent\ && cd .\rdp-agent`,
			want:    `nohup kill -9 34780; $__winuxChainOk = $?; if ($__winuxChainOk) { kill -9 42356; $__winuxChainOk = $? }; if ($__winuxChainOk) { mv .\RdpAgent.exe .\rdp-agent\; $__winuxChainOk = $? }; if ($__winuxChainOk) { cd .\rdp-agent; $__winuxChainOk = $? }`,
			changed: true,
		},
		{in: "just one command", want: "just one command", changed: false},
		{in: "", want: "", changed: false},
	}
	for _, c := range cases {
		got, changed := psChainRewrite(c.in)
		if changed != c.changed {
			t.Errorf("psChainRewrite(%q) changed=%v, want %v", c.in, changed, c.changed)
			continue
		}
		if got != c.want {
			t.Errorf("psChainRewrite(%q) =\n%q\nwant\n%q", c.in, got, c.want)
		}
	}
}

// Operators inside quotes or brackets, and after a comment, are ordinary text.
func TestPSChainRewriteLeavesQuotedAndNestedTextAlone(t *testing.T) {
	untouched := []string{
		`echo "a && b"`,
		`echo 'a || b'`,
		`echo "&"`, // no operator at all
		`Write-Host "x` + "`" + `& y"`,
		`git commit -m "fix && docs"`,
		`(a && b)`,           // inside parentheses: a group, not our business
		`if ($x) { a && b }`, // inside a block
		`a # && b`,
		`echo "unterminated && b`, // unbalanced quote: never rewritten
		`a && `,                   // empty segment: never rewritten
		`&& b`,                    // ditto
		`a | b`,                   // a pipe is not a chain
	}
	for _, in := range untouched {
		if out, changed := psChainRewrite(in); changed {
			t.Errorf("psChainRewrite(%q) must not rewrite, got %q", in, out)
		}
	}

	// a quoted && plus a real one: only the real one splits, quoted text survives
	got, changed := psChainRewrite(`echo "a && b" && echo c`)
	want := `echo "a && b"; $__winuxChainOk = $?; if ($__winuxChainOk) { echo c; $__winuxChainOk = $? }`
	if !changed || got != want {
		t.Errorf("quoted-then-real = %q (changed %v), want %q", got, changed, want)
	}
}

// The rewritten line must mean what the shell means. PowerShell evaluates it,
// and a failing step must stop (&&) or trigger (||) the next one.
func TestPSChainEvaluatesLikeTheShell(t *testing.T) {
	ps := systemExe(`WindowsPowerShell\v1.0\powershell.exe`)
	if !fileExists(ps) {
		t.Skip("Windows PowerShell is not available")
	}
	cases := []struct {
		line string
		want string // expected stdout (trimmed), "" when nothing should run
	}{
		// ok && ok  -> both run
		{"cmd /c exit 0 && Write-Output first-ran", "first-ran"},
		// fail && ok -> the second must NOT run
		{"cmd /c exit 1 && Write-Output must-not-run", ""},
		// fail || ok -> the second must run
		{"cmd /c exit 1 || Write-Output fallback-ran", "fallback-ran"},
		// ok || ok   -> the second must NOT run
		{"cmd /c exit 0 || Write-Output must-not-run", ""},
		// a && b || c with b failing -> c runs
		{"cmd /c exit 0 && cmd /c exit 1 || Write-Output rescued", "rescued"},
		// a && b || c with both fine -> c does not run
		{"cmd /c exit 0 && cmd /c exit 0 || Write-Output must-not-run", ""},
	}
	for _, c := range cases {
		rewritten, changed := psChainRewrite(c.line)
		if !changed {
			t.Fatalf("psChainRewrite(%q) did not rewrite", c.line)
		}
		out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", rewritten).CombinedOutput()
		if err != nil {
			t.Errorf("rewritten line failed to run: %v (%s)", err, out)
			continue
		}
		got := strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n"))
		if got != c.want {
			t.Errorf("%q -> %q, want %q", c.line, got, c.want)
		}
	}
}

func TestPSChainCommand(t *testing.T) {
	code, out, _ := Execute("__ps-chain", []string{"a", "&&", "b"}, nil)
	if code != 0 {
		t.Fatalf("__ps-chain exit = %d", code)
	}
	if !strings.Contains(out, "if ($__winuxChainOk)") || !strings.Contains(out, "a;") {
		t.Errorf("__ps-chain output = %q", out)
	}
	// a line without operators comes back verbatim, so the hook can compare
	code, out, _ = Execute("__ps-chain", []string{"plain command"}, nil)
	if code != 0 || strings.TrimSpace(out) != "plain command" {
		t.Errorf("__ps-chain plain = %d, %q", code, out)
	}
	// the helper is internal: not in the public command list, no shim
	if strings.Contains(GlobalHelpText(), "__ps-chain") {
		t.Error("__ps-chain must not appear in --help")
	}
	for _, name := range shimNameList() {
		if name == "__ps-chain" {
			t.Error("__ps-chain must not get a .cmd shim")
		}
	}
}
