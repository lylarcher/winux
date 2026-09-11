package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShExpand(t *testing.T) {
	os.Setenv("SH_TEST_VAR", "world")
	t.Cleanup(func() { os.Unsetenv("SH_TEST_VAR") })
	ctx := &shCtx{lastStatus: 7}
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"", ""},
		{"$SH_TEST_VAR", "world"},
		{"x$SH_TEST_VAR", "xworld"},
		{"${SH_TEST_VAR}y", "worldy"},
		{"$UNSET_VAR_XYZ", ""},
		{"a $UNSET_VAR_XYZ b", "a  b"},
		{"$? done", "7 done"},
		{"cost $5", "cost $5"}, // $ followed by a digit is literal
		{"$$", "$$"},           // no PID expansion
	}
	for _, c := range cases {
		if got := shExpand(ctx, c.in); got != c.want {
			t.Errorf("shExpand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func shTokDesc(toks []shTok) string {
	var parts []string
	for _, tk := range toks {
		switch tk.op {
		case 0:
			if tk.quoted {
				parts = append(parts, "q:"+tk.text)
			} else {
				parts = append(parts, "w:"+tk.text)
			}
		case shOpPipe:
			parts = append(parts, "|")
		case shOpSemi:
			parts = append(parts, ";")
		case shOpAnd:
			parts = append(parts, "&&")
		case shOpOr:
			parts = append(parts, "||")
		case shOpBg:
			parts = append(parts, "&")
		case shOpRedir:
			name := map[int]string{
				shRedirIn: "<", shRedirOut: ">", shRedirApp: ">>",
				shRedirHere: "<<", shRedirErrOut: "2>", shRedirErrApp: "2>>",
				shRedirErrMirror: "2>&1",
			}[tk.kind]
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, " ")
}

func TestShTokenize(t *testing.T) {
	ctx := &shCtx{lastStatus: 0}
	cases := []struct {
		in, want string
	}{
		{"echo hello world", "w:echo w:hello w:world"},
		{"echo 'a b' c", "w:echo q:a b w:c"},
		{`echo "a \"b\""`, `w:echo q:a "b"`},
		{`echo a\ b`, "w:echo w:a b"},
		{"a | b", "w:a | w:b"},
		{"a && b || c ; d", "w:a && w:b || w:c ; w:d"},
		{"cat < f > o", "w:cat < w:f > w:o"},
		{"x >>log", "w:x >> w:log"},
		{"cmd 2>err.txt", "w:cmd 2> w:err.txt"},
		{"cmd 2>>err.txt", "w:cmd 2>> w:err.txt"},
		{"cmd 2>&1", "w:cmd 2>&1"},
		{"cat <<EOF", "w:cat << w:EOF"},
		{"cat <<'EOF'", "w:cat << q:EOF"},
		{"grep # comment", "w:grep"},
		{"echo $(not-substitution)", "w:echo w:$(not-substitution)"},
		{`cat < C:\Users\bob\a.txt`, "w:cat < w:C:\\Users\\bob\\a.txt"},
		{`cat < \\server\share\a.txt`, "w:cat < w:\\\\server\\share\\a.txt"},
		{`echo a\ b`, "w:echo w:a b"},
	}
	for _, c := range cases {
		got := shTokDesc(shTokenize(c.in, ctx))
		if got != c.want {
			t.Errorf("shTokenize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShParseLine(t *testing.T) {
	ctx := &shCtx{}
	get := func(line string) []shUnit {
		toks := shTokenize(line, ctx)
		units, err := shParseLine(toks)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return units
	}
	t.Run("simple command", func(t *testing.T) {
		u := get("echo hello world")
		if len(u) != 1 || len(u[0].cmds) != 1 {
			t.Fatalf("got %+v", u)
		}
		c := u[0].cmds[0]
		if c.name != "echo" || len(c.args) != 2 {
			t.Fatalf("cmd = %+v", c)
		}
	})
	t.Run("assignment before command", func(t *testing.T) {
		u := get("FOO=bar echo hi")
		c := u[0].cmds[0]
		if c.name != "echo" || len(c.env) != 1 || c.env[0] != "FOO=bar" {
			t.Fatalf("cmd = %+v", c)
		}
	})
	t.Run("standalone assignment", func(t *testing.T) {
		u := get("X=winux")
		c := u[0].cmds[0]
		if c.name != "" || len(c.env) != 1 {
			t.Fatalf("cmd = %+v", c)
		}
	})
	t.Run("redirections", func(t *testing.T) {
		u := get("cat < in.txt > out.txt 2>err.txt")
		c := u[0].cmds[0]
		if c.in.kind != shRedirIn || c.in.path != "in.txt" {
			t.Fatalf("in = %+v", c.in)
		}
		if c.out.kind != shRedirOut || c.out.path != "out.txt" {
			t.Fatalf("out = %+v", c.out)
		}
		if c.err2.kind != shRedirErrOut || c.err2.path != "err.txt" {
			t.Fatalf("err2 = %+v", c.err2)
		}
	})
	t.Run("append and mirror", func(t *testing.T) {
		u := get("x >>log 2>&1")
		c := u[0].cmds[0]
		if c.out.kind != shRedirApp {
			t.Fatalf("out = %+v", c.out)
		}
		if c.err2.kind != shRedirErrMirror {
			t.Fatalf("err2 = %+v", c.err2)
		}
	})
	t.Run("heredoc specs", func(t *testing.T) {
		u := get("cat <<EOF")
		c := u[0].cmds[0]
		if len(c.heredocs) != 1 || c.heredocs[0].delim != "EOF" || c.heredocs[0].quoted {
			t.Fatalf("heredocs = %+v", c.heredocs)
		}
		u2 := get("cat <<'EOF'")
		if len(u2[0].cmds[0].heredocs) != 1 || !u2[0].cmds[0].heredocs[0].quoted {
			t.Fatalf("heredocs = %+v", u2[0].cmds[0].heredocs)
		}
	})
	t.Run("pipeline", func(t *testing.T) {
		u := get("a | b | c")
		if len(u) != 1 || len(u[0].cmds) != 3 {
			t.Fatalf("units = %+v", u)
		}
	})
	t.Run("conditionals", func(t *testing.T) {
		u := get("a ; b && c || d")
		if len(u) != 4 {
			t.Fatalf("units = %+v", u)
		}
		wantConds := []int{shCondSeq, shCondSeq, shCondAnd, shCondOr}
		for i, w := range wantConds {
			if u[i].cond != w {
				t.Errorf("unit %d cond = %d, want %d", i, u[i].cond, w)
			}
		}
	})
}

func TestShCollectHeredocs(t *testing.T) {
	ctx := &shCtx{}
	toks := shTokenize("cat <<EOF", ctx)
	units, err := shParseLine(toks)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{"line one", "line two", "EOF", "next line"}
	idx := 0
	rl := func() (string, bool) {
		if idx >= len(lines) {
			return "", false
		}
		l := lines[idx]
		idx++
		return l, true
	}
	shCollectHeredocs(units, rl)
	c := units[0].cmds[0]
	if len(c.heredocs) != 1 || c.heredocs[0].body != "line one\nline two" {
		t.Fatalf("heredoc body = %q", c.heredocs[0].body)
	}
	// the reader must have consumed exactly the body lines
	if idx != 3 {
		t.Fatalf("reader consumed %d lines, want 3", idx)
	}
	if next, _ := rl(); next != "next line" {
		t.Fatalf("next line = %q", next)
	}
}

func TestShHeredocEndToEnd(t *testing.T) {
	// Run through Execute with the shim path (-c), like a real invocation.
	code, out, errOut := Execute("sh", []string{"-c", "cat <<EOF\nhello 世界\nEOF\n"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if out != "hello 世界\n" {
		t.Fatalf("stdout = %q, want %q", out, "hello 世界\n")
	}
}

func TestShQuotedHeredocEndToEnd(t *testing.T) {
	code, out, _ := Execute("sh", []string{"-c", "cat <<'EOF'\n$HOME not expanded\nEOF\n"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out != "$HOME not expanded\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestShConditionalsEndToEnd(t *testing.T) {
	code, out, _ := Execute("sh", []string{"-c", "true && echo ok"}, strings.NewReader(""))
	if code != 0 || out != "ok\n" {
		t.Fatalf("true && echo ok => code %d out %q", code, out)
	}
	code, out, _ = Execute("sh", []string{"-c", "false && echo nope"}, strings.NewReader(""))
	if code != 1 || out != "" {
		t.Fatalf("false && echo nope => code %d out %q", code, out)
	}
	code, out, _ = Execute("sh", []string{"-c", "false || echo yes"}, strings.NewReader(""))
	if code != 0 || out != "yes\n" {
		t.Fatalf("false || echo yes => code %d out %q", code, out)
	}
	code, _, _ = Execute("sh", []string{"-c", "exit 7"}, strings.NewReader(""))
	if code != 7 {
		t.Fatalf("exit 7 => code %d", code)
	}
}

func TestShVariableEndToEnd(t *testing.T) {
	code, out, _ := Execute("sh", []string{"-c", "X=winux\necho $X"}, strings.NewReader(""))
	if code != 0 || out != "winux\n" {
		t.Fatalf("variable => code %d out %q", code, out)
	}
	code, _, errOut := Execute("sh", []string{"-c", "no-such-command-xyz"}, strings.NewReader(""))
	if code != 127 || !strings.Contains(errOut, "not found") {
		t.Fatalf("unknown => code %d stderr %q", code, errOut)
	}
}

func TestShRedirectEndToEnd(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	code, _, errOut := Execute("sh", []string{"-c", "echo hello > out.txt"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d stderr %q", code, errOut)
	}
	b, err := os.ReadFile("out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("out.txt = %q", string(b))
	}

	code, out, _ := Execute("sh", []string{"-c", "cat < out.txt"}, strings.NewReader(""))
	if code != 0 || out != "hello\n" {
		t.Fatalf("cat < out.txt => code %d out %q", code, out)
	}

	// append
	code, _, _ = Execute("sh", []string{"-c", "echo world >> out.txt"}, strings.NewReader(""))
	b, _ = os.ReadFile("out.txt")
	if code != 0 || string(b) != "hello\nworld\n" {
		t.Fatalf("append => code %d out.txt %q", code, string(b))
	}
}

func TestShCdBuiltin(t *testing.T) {
	a := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(a)
	defer os.Chdir(old)
	t.Setenv("WINUX_OLDPWD_FILE", filepath.Join(a, "oldpwd"))
	t.Setenv("WINUX_ACTIVE", "1")
	b := t.TempDir()

	// cd <b> really changes the sh process directory (silent), then cd - goes
	// back to a, printing the target like bash does
	code, out, errOut := Execute("sh", []string{"-c", "cd " + b + "\ncd -"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("cd builtin: exit %d stderr %q", code, errOut)
	}
	if wd, _ := os.Getwd(); wd != a {
		t.Fatalf("cwd after `cd -` = %s, want %s", wd, a)
	}
	if strings.TrimSpace(out) != a {
		t.Fatalf("cd - output = %q, want %q", out, a)
	}

	// toggling back: `cd -` again returns to b
	code, out, _ = Execute("sh", []string{"-c", "cd -"}, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("second cd -: exit %d", code)
	}
	if wd, _ := os.Getwd(); wd != b {
		t.Fatalf("cwd after second `cd -` = %s, want %s", wd, b)
	}
	if strings.TrimSpace(out) != b {
		t.Fatalf("second cd - output = %q, want %q", out, b)
	}

	// cd to a missing directory fails with exit 1 and keeps the oldpwd record
	code, _, _ = Execute("sh", []string{"-c", "cd no-such-dir-xyz"}, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("cd to a missing dir: exit %d, want 1", code)
	}
}
