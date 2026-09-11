package core_test

import (
	"strings"
	"testing"

	"winux/core"
)

func TestLessDumpsWhenNotATerminal(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "p.txt", "line one\nline two\n")
	code, out, _ := run(t, "", "less", "p.txt")
	if code != 0 || !strings.Contains(out, "line one") {
		t.Fatalf("less dump = %q (code %d)", out, code)
	}
}

func TestViNeedsAConsole(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "e.txt", "x\n")
	// stdout is a pipe in tests, so vi must refuse instead of hanging.
	code, _, errOut := run(t, "", "vi", "e.txt")
	if code == 0 {
		t.Fatalf("vi should refuse to run without a console (err=%q)", errOut)
	}
}

func TestDecodeCSI(t *testing.T) {
	cases := []struct {
		seq  string
		want core.KeyKind
	}{
		{"A", core.KeyUp},
		{"B", core.KeyDown},
		{"C", core.KeyRight},
		{"D", core.KeyLeft},
		{"H", core.KeyHome},
		{"F", core.KeyEnd},
		{"1~", core.KeyHome},
		{"7~", core.KeyHome},
		{"4~", core.KeyEnd},
		{"8~", core.KeyEnd},
		{"5~", core.KeyPageUp},
		{"6~", core.KeyPageDown},
		{"3~", core.KeyDelete},
		{"z", core.KeyEscape},
		{"", core.KeyEscape},
	}
	for _, c := range cases {
		if got := core.DecodeCSI(c.seq); got != c.want {
			t.Errorf("DecodeCSI(%q) = %v, want %v", c.seq, got, c.want)
		}
	}
	if core.UTF8SeqLen('a') != 1 || core.UTF8SeqLen(0xC3) != 2 || core.UTF8SeqLen(0xE4) != 3 || core.UTF8SeqLen(0xF0) != 4 {
		t.Fatal("UTF8SeqLen mis-detects sequence lengths")
	}
	if core.TruncRun("abcdef", 3) != "abc" || core.TruncRun("abc", 10) != "abc" {
		t.Fatal("TruncRun does not truncate by runes")
	}
	if core.TruncRun("文档编辑器", 3) != "文档编" {
		t.Fatal("TruncRun must count runes, not bytes")
	}
}

func TestLessSearchAndDisplay(t *testing.T) {
	lines := []string{"one", "two", "three", "two again"}
	re, err := core.CompilePattern("two", false)
	if err != nil {
		t.Fatal(err)
	}
	if idx, ok := core.SearchForward(lines, re, 0); !ok || idx != 1 {
		t.Fatalf("forward search = %d,%v", idx, ok)
	}
	if idx, ok := core.SearchForward(lines, re, 2); !ok || idx != 3 {
		t.Fatalf("forward search from 2 = %d,%v", idx, ok)
	}
	if idx, ok := core.SearchFrom(lines, re, 0, true); !ok || idx != 3 {
		t.Fatalf("backwards search should wrap to the last match, got %d,%v", idx, ok)
	}
	// smart case: a lower case pattern is matched case-insensitively
	re2, err := core.CompilePattern("TWO", false)
	if err != nil {
		t.Fatal(err)
	}
	if re2.MatchString("two") {
		t.Fatal("an upper case pattern must stay case sensitive")
	}
	// display helpers
	l := &core.LessState{Lines: lines, Scr: &core.Screen{Rows: 10, Cols: 20}, Number: true, Re: re}
	out := l.DisplayLine(2)
	if !strings.Contains(out, "3") || !strings.Contains(out, "three") {
		t.Fatalf("DisplayLine = %q", out)
	}
	l.Number = false
	l.Re = nil
	l.Left = 1
	if got := l.DisplayLine(0); got != "ne" {
		t.Fatalf("horizontal scroll = %q", got)
	}
	if got := core.SqueezeBlankLines([]string{"a", "", "", "b"}); len(got) != 3 {
		t.Fatalf("SqueezeBlankLines = %v", got)
	}
}

func TestEditorMotionsAndEdits(t *testing.T) {
	mk := func(text string) *core.Editor {
		var lines [][]rune
		for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			lines = append(lines, []rune(l))
		}
		return &core.Editor{Lines: lines, Files: []string{""}}
	}
	e := mk("hello world\nsecond line\nthird\n")
	e.Mode = core.ModeNormal

	e.WordForward()
	if e.Col != 6 {
		t.Fatalf("w moved to column %d, want 6", e.Col)
	}
	e.WordBackward()
	if e.Col != 0 {
		t.Fatalf("b moved to column %d, want 0", e.Col)
	}
	e.WordEnd()
	if e.Col != 4 {
		t.Fatalf("e moved to column %d, want 4", e.Col)
	}
	e.Col = 0
	e.Row = 0
	e.DeleteWord(1)
	if got := string(e.Lines[0]); got != "world" {
		t.Fatalf("dw = %q", got)
	}
	e.YankLines(1)
	e.Paste(false)
	if len(e.Lines) != 4 || string(e.Lines[1]) != "world" {
		t.Fatalf("yy/p produced %v", e.Lines)
	}
	e.Row = 0
	e.DeleteLines(1)
	if string(e.Lines[0]) != "world" {
		t.Fatalf("dd = %q", string(e.Lines[0]))
	}
	e.Col = 0
	e.ReplaceChar('W')
	if string(e.Lines[0]) != "World" {
		t.Fatalf("r = %q", string(e.Lines[0]))
	}
	e.ToggleCase()
	if string(e.Lines[0]) != "world" {
		t.Fatalf("~ = %q", string(e.Lines[0]))
	}
	e.UndoLast()
	if string(e.Lines[0]) != "World" {
		t.Fatalf("u = %q", string(e.Lines[0]))
	}
	// insert mode pieces
	e.Mode = core.ModeInsert
	e.Row, e.Col = 0, 5
	e.InsertRune('!')
	if string(e.Lines[0]) != "World!" {
		t.Fatalf("insert = %q", string(e.Lines[0]))
	}
	e.InsertNewline()
	if len(e.Lines) != 4 || string(e.Lines[1]) != "" {
		t.Fatalf("Enter did not split the line: %v", e.Lines)
	}
	e.Backspace()
	if len(e.Lines) != 3 || string(e.Lines[0]) != "World!" {
		t.Fatalf("backspace did not join the lines: %v", e.Lines)
	}
	// 'G' style navigation stays inside the buffer
	e.Mode = core.ModeNormal
	e.Row = 99
	e.ClampCursor()
	if e.Row != len(e.Lines)-1 {
		t.Fatalf("ClampCursor left the row at %d", e.Row)
	}
}

func TestEditorSearchAndExErrors(t *testing.T) {
	e := &core.Editor{Files: []string{""}, Lines: [][]rune{[]rune("alpha"), []rune("beta"), []rune("gamma")}}
	pat, err := core.CompilePattern("ga", false)
	if err != nil {
		t.Fatal(err)
	}
	e.SearchSrc, e.SearchRe = "ga", pat
	e.FindNext(false)
	if e.Row != 2 || e.Col != 0 {
		t.Fatalf("FindNext landed at %d,%d", e.Row, e.Col)
	}
	e.FindNext(false) // wraps around to the same match
	if e.Row != 2 {
		t.Fatalf("FindNext should wrap, row=%d", e.Row)
	}
	e.Status = ""
	if e.ExecEx("bogus-command") {
		t.Fatal("an unknown ex command must not quit")
	}
	if !strings.Contains(e.Status, "E492") {
		t.Fatalf("status = %q", e.Status)
	}
	// an unwritable path reports E212 instead of panicking
	e2 := &core.Editor{Files: []string{""}, Lines: [][]rune{[]rune("x")}}
	if e2.Save("") {
		t.Fatal("saving without a file name should fail")
	}
	if !strings.Contains(e2.Status, "E32") {
		t.Fatalf("status = %q", e2.Status)
	}
}

func TestViExPieces(t *testing.T) {
	chdirTemp(t)
	e := &core.Editor{Files: []string{"t.txt"}, Lines: [][]rune{{'a', 'b', 'c'}, {'d', 'e'}}}
	e.Path = "t.txt"
	if ok := e.Save(""); !ok {
		t.Fatalf("save failed: %s", e.Status)
	}
	if got := readFile(t, "t.txt"); got != "abc\nde\n" {
		t.Fatalf("saved content = %q", got)
	}
	// %s/old/new/g
	e.Substitute("%s/b/X/g")
	if string(e.Lines[0]) != "aXc" {
		t.Fatalf("substitute result = %q", string(e.Lines[0]))
	}
	// :set nu and the status line
	e.Number = true
	e.Row, e.Col = 1, 1
	if line := e.StatusLine(80); !strings.Contains(line, "2,2") {
		t.Fatalf("status line = %q", line)
	}
	// undo restores the previous buffer
	e.PushUndo()
	e.Lines[0] = []rune("changed")
	e.PushUndo()
	e.Lines[0] = []rune("changed again")
	e.UndoLast()
	if string(e.Lines[0]) != "changed" {
		t.Fatalf("undo = %q", string(e.Lines[0]))
	}
	// a modified buffer refuses :q
	e.Modified = true
	if e.ExecEx("q") {
		t.Fatal(":q should refuse when the buffer is modified")
	}
	if got := readFile(t, "t.txt"); got != "abc\nde\n" {
		t.Fatalf("file changed unexpectedly: %q", got)
	}
}
