package core

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

func init() {
	addCmd("vi", "visual editor: vi/vim compatible subset (also available as vim)", cmdVi, "vim")
}

// exHelpLine is assembled at run time so that `go vet` does not read the
// substitution examples as printf format directives.
var exHelpLine = strings.Join([]string{
	"Ex commands:  :w  :q  :q!  :wq  :x  :n  :N  :set nu  :s/pat/rep/[gi]  :%",
	"s/pat/rep/g",
}, "")

type EditorMode int

const (
	ModeNormal EditorMode = iota
	ModeInsert
)

type viAction int

const (
	actNone viAction = iota
	actQuit
)

type Editor struct {
	Scr      *Screen
	Files    []string
	fidx     int
	Path     string
	Lines    [][]rune
	Row, Col int
	top      int
	Left     int
	Mode     EditorMode
	Modified bool
	Number   bool
	readonly bool
	Status   string
	register [][]rune
	undo     [][][]rune
	undoOpen bool // an insert session already pushed one undo snapshot

	SearchSrc  string
	SearchRe   *regexp.Regexp
	searchBack bool
}

func cmdVi(args []string) int {
	readonly := false
	var files []string
	for _, a := range args {
		switch {
		case a == "-R":
			readonly = true
		case a == "--help":
			fmt.Println("Usage: vi [OPTION]... [FILE]...")
			fmt.Println("A vi/vim compatible visual editor. Without a FILE a new buffer is opened.")
			fmt.Println()
			fmt.Println("Normal mode:  h j k l  w b e  0 $ ^  gg G  x X dd dw yy p P u r ~")
			fmt.Println("              i I a A o O insert, / ? search, n N repeat, : ex command")
			fmt.Println(exHelpLine)
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  -R          read-only mode")
			return 0
		case a == "--version":
			printVersion("vi")
			return 0
		case isOptionLike(a):
			warn("vi", "unknown option '%s'", a)
			return 2
		default:
			files = append(files, a)
		}
	}
	if !isTerminal(os.Stdout) {
		warn("vi", "standard output is not a console: vi needs an interactive terminal")
		return 1
	}
	scr, ok := newScreen()
	if !ok {
		warn("vi", "cannot switch the console to raw mode")
		return 1
	}
	e := &Editor{Scr: scr, Files: files, readonly: readonly}
	if len(e.Files) == 0 {
		e.Files = []string{""}
	}
	if err := e.loadFile(0); err != nil {
		warn("vi", "%v", err)
		scr.close()
		return 1
	}
	defer scr.close()
	e.loop()
	// reset the cursor shape to the console default after the session
	os.Stdout.WriteString("\x1b[2 q")
	return 0
}

// ------------------------------------------------------------ main loop ----

func (e *Editor) loop() {
	var op rune
	count := 0
	for {
		e.ClampCursor()
		e.draw()
		k := e.Scr.readKey()
		if e.Mode == ModeInsert {
			if e.handleInsertKey(k) {
				return
			}
			continue
		}
		switch k.kind {
		case KeyRune:
			if e.normalKey(k.ch, &op, &count) == actQuit {
				return
			}
		case KeyLeft:
			e.moveLeft(1)
		case KeyRight:
			e.moveRight(1)
		case KeyUp:
			e.moveUp(1)
		case KeyDown:
			e.moveDown(1)
		case KeyHome:
			e.Col = 0
		case KeyEnd:
			if l := e.curLine(); len(l) > 0 {
				e.Col = len(l) - 1
			}
		case KeyPageDown:
			e.Row += e.Scr.Rows - 2
			e.ClampCursor()
		case KeyPageUp:
			e.Row -= e.Scr.Rows - 2
			e.ClampCursor()
		case KeyDelete:
			e.PushUndo()
			e.delChar()
		case KeyEscape, KeyCtrlC:
			e.Status = ""
		case KeyEOF:
			return
		}
	}
}

func (e *Editor) normalKey(ch rune, op *rune, count *int) viAction {
	if ch >= '1' && ch <= '9' || (ch == '0' && *count > 0) {
		*count = *count*10 + int(ch-'0')
		return actNone
	}
	n := *count
	if n < 1 {
		n = 1
	}
	repeat := func(f func()) {
		for i := 0; i < n; i++ {
			f()
		}
	}
	switch *op {
	case 'd':
		switch ch {
		case 'd':
			e.DeleteLines(n)
		case 'w':
			e.DeleteWord(n)
		case '$', 'D':
			e.deleteToEnd()
		case '0':
			e.deleteToStart()
		default:
			e.Status = "motion not supported after 'd': " + string(ch)
		}
		*op, *count = 0, 0
		return actNone
	case 'y':
		switch ch {
		case 'y':
			e.YankLines(n)
		default:
			e.Status = "motion not supported after 'y': " + string(ch)
		}
		*op, *count = 0, 0
		return actNone
	case 'g':
		if ch == 'g' {
			if *count > 0 {
				e.Row = *count - 1
			} else {
				e.Row = 0
			}
			e.Col = 0
			e.ClampCursor()
		}
		*op, *count = 0, 0
		return actNone
	case 'Z':
		if ch == 'Z' {
			if e.Save("") {
				return actQuit
			}
		}
		*op = 0
		return actNone
	}

	switch ch {
	case 'h':
		repeat(func() { e.moveLeft(1) })
	case 'j':
		repeat(func() { e.moveDown(1) })
	case 'k':
		repeat(func() { e.moveUp(1) })
	case 'l':
		repeat(func() { e.moveRight(1) })
	case 'w':
		repeat(e.WordForward)
	case 'b':
		repeat(e.WordBackward)
	case 'e':
		repeat(e.WordEnd)
	case '0':
		e.Col = 0
	case '^':
		e.Col = e.firstNonBlank()
	case '$':
		if l := e.curLine(); len(l) > 0 {
			e.Col = len(l) - 1
		}
	case 'G':
		if *count > 0 {
			e.Row = *count - 1
		} else {
			e.Row = len(e.Lines) - 1
		}
		e.Col = 0
		e.ClampCursor()
	case 'i':
		e.enterInsert()
	case 'I':
		e.Col = e.firstNonBlank()
		e.enterInsert()
	case 'a':
		if len(e.curLine()) > 0 {
			e.Col++
		}
		e.enterInsert()
	case 'A':
		e.Col = len(e.curLine())
		e.enterInsert()
	case 'o':
		e.openLine(true)
		e.enterInsert()
	case 'O':
		e.openLine(false)
		e.enterInsert()
	case 'x':
		e.PushUndo()
		repeat(e.delChar)
	case 'X':
		e.PushUndo()
		e.Col--
		e.ClampCursor()
		e.delChar()
	case 'd':
		*op = 'd'
	case 'y':
		*op = 'y'
	case 'g', 'Z':
		*op = ch
	case 'p':
		e.Paste(false)
	case 'P':
		e.Paste(true)
	case 'u':
		e.UndoLast()
	case 'r':
		k := e.Scr.readKey()
		if k.kind == KeyRune {
			e.ReplaceChar(k.ch)
		}
	case '~':
		e.ToggleCase()
	case 'J':
		e.joinLines()
	case 'n':
		e.repeatSearch(false)
	case 'N':
		e.repeatSearch(true)
	case '/':
		e.promptSearch(false)
	case '?':
		e.promptSearch(true)
	case ':':
		if e.promptEx() {
			return actQuit
		}
	default:
		if ch >= ' ' && ch != 0 {
			e.Status = "unknown command: " + string(ch)
		}
	}
	*count = 0
	*op = 0
	return actNone
}

func (e *Editor) handleInsertKey(k keyEvent) bool {
	switch k.kind {
	case KeyEscape, KeyCtrlC:
		e.Mode = ModeNormal
		e.undoOpen = false
		if e.Col > 0 {
			e.Col--
		}
	case KeyEnter:
		e.InsertNewline()
	case KeyBackspace:
		e.Backspace()
	case KeyDelete:
		e.delChar()
	case KeyLeft:
		if e.Col > 0 {
			e.Col--
		}
	case KeyRight:
		if e.Col < len(e.curLine()) {
			e.Col++
		}
	case KeyUp:
		e.moveUp(1)
	case KeyDown:
		e.moveDown(1)
	case KeyHome:
		e.Col = 0
	case KeyEnd:
		e.Col = len(e.curLine())
	case KeyPageUp:
		e.Row -= e.Scr.Rows - 2
		e.ClampCursor()
	case KeyPageDown:
		e.Row += e.Scr.Rows - 2
		e.ClampCursor()
	case KeyRune:
		if k.ch >= 32 {
			e.InsertRune(k.ch)
		}
	case KeyEOF:
		return true
	}
	return false
}

func (e *Editor) enterInsert() {
	e.Mode = ModeInsert
	if !e.undoOpen {
		e.PushUndo()
		e.undoOpen = true
	}
}

// ------------------------------------------------------------- movements ---

func (e *Editor) curLine() []rune {
	if e.Row < 0 || e.Row >= len(e.Lines) {
		return nil
	}
	return e.Lines[e.Row]
}

func (e *Editor) ClampCursor() {
	if len(e.Lines) == 0 {
		e.Lines = [][]rune{{}}
	}
	if e.Row < 0 {
		e.Row = 0
	}
	if e.Row >= len(e.Lines) {
		e.Row = len(e.Lines) - 1
	}
	line := e.Lines[e.Row]
	switch {
	case e.Mode == ModeInsert:
		if e.Col > len(line) {
			e.Col = len(line)
		}
	case len(line) == 0:
		e.Col = 0
	case e.Col >= len(line):
		e.Col = len(line) - 1
	}
	if e.Col < 0 {
		e.Col = 0
	}
}

func (e *Editor) moveLeft(n int) {
	e.Col -= n
	if e.Col < 0 {
		e.Col = 0
	}
}

func (e *Editor) moveRight(n int) {
	e.Col += n
	e.ClampCursor()
}

func (e *Editor) moveDown(n int) {
	e.Row += n
	e.ClampCursor()
}

func (e *Editor) moveUp(n int) {
	e.Row -= n
	e.ClampCursor()
}

func (e *Editor) firstNonBlank() int {
	for i, r := range e.curLine() {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return 0
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func (e *Editor) WordForward() {
	for {
		line := e.curLine()
		for e.Col < len(line) && !unicode.IsSpace(line[e.Col]) {
			e.Col++
		}
		for e.Col < len(line) && unicode.IsSpace(line[e.Col]) {
			e.Col++
		}
		if e.Col < len(line) {
			return
		}
		if e.Row < len(e.Lines)-1 {
			e.Row++
			e.Col = 0
			if len(e.curLine()) > 0 {
				return
			}
			continue
		}
		e.Col = 0
		if len(line) > 0 {
			e.Col = len(line) - 1
		}
		return
	}
}

func (e *Editor) WordBackward() {
	for {
		if e.Col > 0 {
			e.Col--
			line := e.curLine()
			for e.Col > 0 && unicode.IsSpace(line[e.Col]) {
				e.Col--
			}
			for e.Col > 0 && isWordRune(line[e.Col-1]) {
				e.Col--
			}
			return
		}
		if e.Row > 0 {
			e.Row--
			e.Col = len(e.curLine())
			if e.Col > 0 {
				e.Col--
				continue
			}
			continue
		}
		return
	}
}

func (e *Editor) WordEnd() {
	line := e.curLine()
	if e.Col < len(line)-1 {
		e.Col++
	}
	for {
		line = e.curLine()
		for e.Col < len(line)-1 && !isWordRune(line[e.Col]) {
			e.Col++
		}
		if e.Col < len(line) && isWordRune(line[e.Col]) {
			for e.Col < len(line)-1 && isWordRune(line[e.Col+1]) {
				e.Col++
			}
			return
		}
		if e.Row < len(e.Lines)-1 {
			e.Row++
			e.Col = 0
			continue
		}
		return
	}
}

// -------------------------------------------------------------- editing ----

func (e *Editor) PushUndo() {
	snap := make([][]rune, len(e.Lines))
	for i, l := range e.Lines {
		snap[i] = append([]rune(nil), l...)
	}
	e.undo = append(e.undo, snap)
	if len(e.undo) > 300 {
		e.undo = e.undo[1:]
	}
}

func (e *Editor) UndoLast() {
	if len(e.undo) == 0 {
		e.Status = "Already at oldest change"
		return
	}
	e.Lines = e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.Modified = true
	e.undoOpen = false
	e.ClampCursor()
}

func (e *Editor) InsertRune(ch rune) {
	line := e.curLine()
	if e.Col > len(line) {
		e.Col = len(line)
	}
	rest := append([]rune{ch}, line[e.Col:]...)
	e.Lines[e.Row] = append(line[:e.Col:e.Col], rest...)
	e.Col++
	e.Modified = true
}

func (e *Editor) InsertNewline() {
	line := e.curLine()
	head := append([]rune(nil), line[:e.Col]...)
	tail := append([]rune(nil), line[e.Col:]...)
	e.Lines[e.Row] = head
	e.Lines = append(e.Lines[:e.Row+1], append([][]rune{tail}, e.Lines[e.Row+1:]...)...)
	e.Row++
	e.Col = 0
	e.Modified = true
}

func (e *Editor) Backspace() {
	line := e.curLine()
	if e.Col > 0 {
		e.Lines[e.Row] = append(line[:e.Col-1:e.Col-1], line[e.Col:]...)
		e.Col--
		e.Modified = true
		return
	}
	if e.Row > 0 {
		prev := e.Lines[e.Row-1]
		e.Lines[e.Row-1] = append(prev, line...)
		e.Lines = append(e.Lines[:e.Row], e.Lines[e.Row+1:]...)
		e.Row--
		e.Col = len(prev)
		e.Modified = true
	}
}

func (e *Editor) delChar() {
	line := e.curLine()
	if e.Col >= len(line) {
		return
	}
	e.Lines[e.Row] = append(line[:e.Col:e.Col], line[e.Col+1:]...)
	e.Modified = true
	e.ClampCursor()
}

func (e *Editor) delCharBefore() {
	if e.Col == 0 {
		return
	}
	e.Col--
	e.delChar()
}

func (e *Editor) openLine(below bool) {
	e.PushUndo()
	at := e.Row
	if below {
		at = e.Row + 1
	}
	e.Lines = append(e.Lines[:at], append([][]rune{{}}, e.Lines[at:]...)...)
	e.Row = at
	e.Col = 0
	e.Modified = true
}

func (e *Editor) DeleteLines(n int) {
	e.PushUndo()
	end := e.Row + n
	if end > len(e.Lines) {
		end = len(e.Lines)
	}
	e.register = make([][]rune, 0, end-e.Row)
	for _, l := range e.Lines[e.Row:end] {
		e.register = append(e.register, append([]rune(nil), l...))
	}
	e.Lines = append(e.Lines[:e.Row], e.Lines[end:]...)
	if len(e.Lines) == 0 {
		e.Lines = [][]rune{{}}
	}
	e.ClampCursor()
	e.Modified = true
}

func (e *Editor) DeleteWord(n int) {
	e.PushUndo()
	for i := 0; i < n; i++ {
		line := e.curLine()
		start := e.Col
		j := e.Col
		for j < len(line) && !unicode.IsSpace(line[j]) {
			j++
		}
		for j < len(line) && unicode.IsSpace(line[j]) {
			j++
		}
		if j == start {
			break
		}
		e.Lines[e.Row] = append(line[:start:start], line[j:]...)
		e.Col = start
		e.Modified = true
		e.ClampCursor()
	}
}

func (e *Editor) deleteToEnd() {
	e.PushUndo()
	line := e.curLine()
	if e.Col < len(line) {
		e.Lines[e.Row] = line[:e.Col:e.Col]
		e.Modified = true
	}
	e.ClampCursor()
}

func (e *Editor) deleteToStart() {
	e.PushUndo()
	line := e.curLine()
	if e.Col > 0 {
		e.Lines[e.Row] = append([]rune(nil), line[e.Col:]...)
		e.Col = 0
		e.Modified = true
	}
}

func (e *Editor) YankLines(n int) {
	end := e.Row + n
	if end > len(e.Lines) {
		end = len(e.Lines)
	}
	e.register = nil
	for _, l := range e.Lines[e.Row:end] {
		e.register = append(e.register, append([]rune(nil), l...))
	}
	e.Status = fmt.Sprintf("%d line yanked", len(e.register))
}

// paste inserts the register, which is always line wise because only `dd` and
// `yy` fill it: `p` pastes below the current line, `P` above it.
func (e *Editor) Paste(before bool) {
	if len(e.register) == 0 {
		e.Status = "Nothing in register"
		return
	}
	e.PushUndo()
	at := e.Row
	if !before {
		at = e.Row + 1
	}
	ins := make([][]rune, len(e.register))
	for i, l := range e.register {
		ins[i] = append([]rune(nil), l...)
	}
	e.Lines = append(e.Lines[:at], append(ins, e.Lines[at:]...)...)
	e.Row = at
	e.Col = 0
	e.Modified = true
	e.ClampCursor()
}

func (e *Editor) ReplaceChar(ch rune) {
	e.PushUndo()
	line := e.curLine()
	if e.Col < len(line) {
		e.Lines[e.Row][e.Col] = ch
		e.Modified = true
	}
}

func (e *Editor) ToggleCase() {
	e.PushUndo()
	line := e.curLine()
	if e.Col >= len(line) {
		return
	}
	r := line[e.Col]
	switch {
	case unicode.IsUpper(r):
		e.Lines[e.Row][e.Col] = unicode.ToLower(r)
	case unicode.IsLower(r):
		e.Lines[e.Row][e.Col] = unicode.ToUpper(r)
	}
	e.Modified = true
	if e.Col < len(e.curLine())-1 {
		e.Col++
	}
}

func (e *Editor) joinLines() {
	if e.Row >= len(e.Lines)-1 {
		return
	}
	e.PushUndo()
	line := e.curLine()
	next := e.Lines[e.Row+1]
	joined := strings.TrimRight(string(line), " ")
	e.Lines[e.Row] = append([]rune(joined+" "), next...)
	e.Lines = append(e.Lines[:e.Row+1], e.Lines[e.Row+2:]...)
	e.Modified = true
}

// -------------------------------------------------------------- search -----

func (e *Editor) promptSearch(back bool) {
	prompt := "/"
	if back {
		prompt = "?"
	}
	pat, ok := e.Scr.readLinePrompt(prompt)
	e.Status = ""
	if !ok || pat == "" {
		return
	}
	src := pat
	if !strings.ContainsAny(pat, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		src = "(?i)" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		e.Status = "E486: " + err.Error()
		return
	}
	e.SearchSrc, e.SearchRe, e.searchBack = pat, re, back
	e.FindNext(back)
}

func (e *Editor) repeatSearch(reverse bool) {
	if e.SearchRe == nil {
		e.Status = "No previous regular expression"
		return
	}
	e.FindNext(e.searchBack != reverse)
}

func (e *Editor) FindNext(back bool) {
	if e.SearchRe == nil {
		return
	}
	n := len(e.Lines)
	for i := 0; i < n; i++ {
		idx := e.Row + i + 1
		if back {
			idx = e.Row - i - 1
		}
		if idx < 0 || idx >= n {
			continue
		}
		if loc := e.SearchRe.FindStringIndex(string(e.Lines[idx])); loc != nil {
			e.Row = idx
			e.Col = len([]rune(string(e.Lines[idx])[:loc[0]]))
			return
		}
	}
	e.Status = "E486: Pattern not found: " + e.SearchSrc
}

// ------------------------------------------------------------------ ex -----

func (e *Editor) promptEx() bool {
	cmd, ok := e.Scr.readLinePrompt(":")
	e.Status = ""
	if !ok {
		return false
	}
	return e.ExecEx(strings.TrimSpace(cmd))
}

func (e *Editor) ExecEx(cmd string) bool {
	switch {
	case cmd == "":
		return false
	case cmd == "w" || cmd == "write":
		e.Save("")
		return false
	case strings.HasPrefix(cmd, "w ") || strings.HasPrefix(cmd, "write "):
		e.Save(strings.TrimSpace(cmd[strings.IndexByte(cmd, ' ')+1:]))
		return false
	case cmd == "q" || cmd == "quit":
		return e.quit(false)
	case cmd == "q!" || cmd == "quit!":
		return e.quit(true)
	case cmd == "wq" || cmd == "x":
		if e.Save("") {
			return e.quit(true)
		}
		return false
	case cmd == "wq!" || cmd == "x!":
		if e.saveForced("") {
			return e.quit(true)
		}
		return false
	case cmd == "n" || cmd == "next":
		e.nextFile(1)
		return false
	case cmd == "N" || cmd == "prev" || cmd == "previous":
		e.nextFile(-1)
		return false
	case cmd == "set nu" || cmd == "set number":
		e.Number = true
		return false
	case cmd == "set nonu" || cmd == "set nonumber":
		e.Number = false
		return false
	case cmd == "help":
		e.Status = "normal: hjkl w b e 0 $ gg G x dd yy p u r i a o / :"
		return false
	case strings.HasPrefix(cmd, "%s") || strings.HasPrefix(cmd, "s"):
		e.Substitute(cmd)
		return false
	default:
		e.Status = "E492: Not an editor command: " + cmd
		return false
	}
}

func (e *Editor) quit(force bool) bool {
	if e.Modified && !force {
		e.Status = "E37: No write since last change (add ! to override)"
		return false
	}
	if e.fidx < len(e.Files)-1 {
		next := e.fidx + 1
		e.Modified = false
		if err := e.loadFile(next); err != nil {
			e.Status = err.Error()
			return false
		}
		return false
	}
	return true
}

func (e *Editor) nextFile(delta int) {
	if e.Modified {
		e.Status = "E37: No write since last change (add ! to override)"
		return
	}
	next := e.fidx + delta
	if next < 0 || next >= len(e.Files) {
		e.Status = "No more files in the argument list"
		return
	}
	if err := e.loadFile(next); err != nil {
		e.Status = err.Error()
	}
}

func (e *Editor) Save(name string) bool {
	if e.readonly && name == "" {
		e.Status = "E45: 'readonly' option is set (add ! to override)"
		return false
	}
	target := name
	if target == "" {
		target = e.Path
	}
	if target == "" || target == "-" {
		e.Status = "E32: No file name"
		return false
	}
	if err := writeLines(target, e.Lines); err != nil {
		e.Status = fmt.Sprintf("E212: can't open file for writing: %v", err)
		return false
	}
	if e.Path == "" {
		e.Path = target
		e.Files[e.fidx] = target
	}
	e.Modified = false
	e.Status = fmt.Sprintf("\"%s\" written", target)
	return true
}

func (e *Editor) saveForced(name string) bool {
	ro := e.readonly
	e.readonly = false
	ok := e.Save(name)
	e.readonly = ro
	return ok
}

func writeLines(path string, lines [][]rune) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		makeWritable(path)
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			return err
		}
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, l := range lines {
		if _, err := w.WriteString(string(l)); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (e *Editor) Substitute(cmd string) {
	global := false
	s := cmd
	if strings.HasPrefix(s, "%") {
		global = true
		s = s[1:]
	}
	if !strings.HasPrefix(s, "s") || len(s) < 2 {
		e.Status = "E486: invalid substitute command"
		return
	}
	delim := s[1]
	parts, flags, err := splitSubst(s[2:], delim)
	if err != nil || len(parts) < 2 {
		e.Status = "E486: invalid substitute command"
		return
	}
	pat, repl := parts[0], parts[1]
	src := pat
	if strings.ContainsAny(flags, "iI") {
		src = "(?i)" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		e.Status = "E486: " + err.Error()
		return
	}
	all := strings.Contains(flags, "g")
	replGo := convertReplacement(repl)
	first, last := e.Row, e.Row
	if global {
		first, last = 0, len(e.Lines)-1
	}
	e.PushUndo()
	changed := false
	for i := first; i <= last && i < len(e.Lines); i++ {
		line := string(e.Lines[i])
		var out string
		if all {
			out = re.ReplaceAllString(line, replGo)
		} else {
			out = replaceFirst(re, line, replGo)
		}
		if out != line {
			e.Lines[i] = []rune(out)
			changed = true
		}
	}
	if !changed {
		e.undo = e.undo[:len(e.undo)-1]
		e.Status = "E486: Pattern not found: " + pat
		return
	}
	e.Modified = true
	e.ClampCursor()
	e.Status = fmt.Sprintf("%d substitution(s)", 1)
}

func splitSubst(body string, delim byte) ([]string, string, error) {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '\\' && i+1 < len(body) {
			cur.WriteByte(c)
			i++
			cur.WriteByte(body[i])
			continue
		}
		if c == delim {
			parts = append(parts, cur.String())
			cur.Reset()
			if len(parts) == 2 {
				return parts, body[i+1:], nil
			}
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	if len(parts) < 2 {
		return nil, "", fmt.Errorf("invalid substitute command")
	}
	return parts, "", nil
}

func replaceFirst(re *regexp.Regexp, line, repl string) string {
	loc := re.FindStringSubmatchIndex(line)
	if loc == nil {
		return line
	}
	return line[:loc[0]] + string(re.ExpandString(nil, repl, line, loc)) + line[loc[1]:]
}

// convertReplacement turns vi-style \1 / & references into Go's ${1} / ${0}.
func convertReplacement(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			n := s[i+1]
			switch {
			case n >= '0' && n <= '9':
				b.WriteString("${" + string(n) + "}")
			case n == '&':
				b.WriteString("${0}")
			case n == '\\':
				b.WriteByte('\\')
			case n == 'n':
				b.WriteByte('\n')
			case n == 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(n)
			}
			i++
			continue
		}
		if c == '&' {
			b.WriteString("${0}")
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ---------------------------------------------------------------- files ----

func (e *Editor) loadFile(i int) error {
	if i < 0 || i >= len(e.Files) {
		return fmt.Errorf("no such file in the argument list")
	}
	name := e.Files[i]
	e.fidx = i
	e.Path = name
	e.Lines = nil
	e.Modified = false
	e.undo = nil
	e.undoOpen = false
	e.Row, e.Col, e.top, e.Left = 0, 0, 0, 0
	e.Status = ""
	e.register = nil
	e.SearchSrc, e.SearchRe = "", nil
	switch {
	case name == "-":
		ls, err := readLinesFrom(os.Stdin)
		if err != nil {
			return fmt.Errorf("cannot read standard input: %v", err)
		}
		for _, l := range ls {
			e.Lines = append(e.Lines, []rune(l))
		}
	case name != "" && fileExists(name):
		if isDir(name) {
			return fmt.Errorf("%s: Is a directory", name)
		}
		ls, err := readAllLines(name)
		if err != nil {
			return fmt.Errorf("cannot read '%s': %v", name, err)
		}
		for _, l := range ls {
			e.Lines = append(e.Lines, []rune(l))
		}
	}
	if len(e.Lines) == 0 {
		e.Lines = [][]rune{{}}
	}
	return nil
}

// --------------------------------------------------------------- drawing ---

func (e *Editor) ensureVisible(textRows int) {
	if e.Row < e.top {
		e.top = e.Row
	}
	if e.Row >= e.top+textRows {
		e.top = e.Row - textRows + 1
	}
	if e.top < 0 {
		e.top = 0
	}
	// Horizontal scrolling is cell-based and lives in draw() where the
	// terminal width is known: rune indices alone cannot express the column
	// of a line containing wide (CJK) characters.
}

// ------------------------------------------------------- cell width (CJK) ---

// runewidth returns the number of terminal cells rune r occupies: 2 for East
// Asian wide/fullwidth glyphs (CJK, Hangul Jamo/syllables, fullwidth forms,
// emoji and CJK extension planes), 1 for everything else. Horizontal cursor
// placement, segment truncation and scrolling all use these cells, otherwise
// the cursor lands half way through a line of Chinese text.
func runewidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x9fff, // CJK Radicals .. CJK Unified Ideographs
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe4f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1fbff, // emoji and symbols (incl. modifiers)
		r >= 0x20000 && r <= 0x2fffd, // CJK extension B and beyond
		r >= 0x30000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// cellWidth returns the total terminal cells occupied by the runes in s.
func cellWidth(s []rune) int {
	n := 0
	for _, r := range s {
		n += runewidth(r)
	}
	return n
}

// truncToCells cuts seg to at most maxCells terminal cells without splitting
// a wide (2-cell) rune in half.
func truncToCells(seg []rune, maxCells int) []rune {
	if maxCells < 1 {
		return nil
	}
	cells := 0
	for i, r := range seg {
		w := runewidth(r)
		if cells+w > maxCells {
			return seg[:i]
		}
		cells += w
	}
	return seg
}

func (e *Editor) DisplayLine(idx, cols int) string {
	line := e.Lines[idx]
	prefix := ""
	if e.Number {
		prefix = fmt.Sprintf("%5d ", idx+1)
	}
	width := cols - len([]rune(prefix))
	if width < 1 {
		width = 1
	}
	seg := line
	if e.Left < len(seg) {
		seg = seg[e.Left:]
	} else {
		seg = nil
	}
	// cut by terminal cells, never by rune count, so wide (CJK) glyphs do
	// not overflow the right edge (and never get split in half)
	seg = truncToCells(seg, width)
	text := string(seg)
	if e.SearchRe != nil {
		text = highlight(text, e.SearchRe) + ansiReset
	}
	return prefix + text
}

func (e *Editor) StatusLine(cols int) string {
	name := e.Path
	if name == "" {
		name = "[No Name]"
	}
	flags := ""
	if e.Modified {
		flags += " [modified]"
	}
	if e.readonly {
		flags += " [readonly]"
	}
	modeStr := ""
	if e.Mode == ModeInsert {
		modeStr = "  -- INSERT --"
	}
	total := len(e.Lines)
	pct := 0
	if total > 0 {
		pct = (e.Row + 1) * 100 / total
	}
	status := fmt.Sprintf("%s%s   %d,%d   %d%%%s", name, flags, e.Row+1, e.Col+1, pct, modeStr)
	if e.Status != "" {
		status += "   " + e.Status
	}
	return truncRun(status, cols)
}

func (e *Editor) draw() {
	rows, cols := e.Scr.Rows, e.Scr.Cols
	textRows := rows - 1
	if textRows < 1 {
		textRows = 1
	}
	e.ensureVisible(textRows)
	width := cols
	if e.Number {
		width -= 6
	}
	if width < 1 {
		width = 1
	}
	// Cell-accurate horizontal scrolling: advance e.Left until the cursor's
	// terminal cell column fits inside the window (rune indices alone would
	// leave the cursor stranded in the middle of wide/CJK text).
	line := e.curLine()
	if e.Col < e.Left {
		e.Left = e.Col
	}
	colRel := cellWidth(line[e.Left:e.Col])
	for colRel >= width && e.Left < e.Col {
		colRel -= runewidth(line[e.Left])
		e.Left++
	}
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	for i := 0; i < textRows; i++ {
		idx := e.top + i
		if idx >= len(e.Lines) {
			b.WriteString("~\x1b[K\r\n")
			continue
		}
		b.WriteString(e.DisplayLine(idx, cols))
		b.WriteString("\x1b[K\r\n")
	}
	b.WriteString("\x1b[7m" + e.StatusLine(cols) + "\x1b[0m")
	row := e.Row - e.top + 1
	col := colRel + 1
	if e.Number {
		col += 6
	}
	if row < 1 {
		row = 1
	}
	if row > textRows {
		row = textRows
	}
	if col < 1 {
		col = 1
	}
	// newScreen() hides the console cursor (\x1b[?25l); vi re-shows it on
	// every frame and switches its shape per mode (block in normal mode,
	// bar in insert mode), so the insertion point is always visible even at
	// the end of a line where the cell is empty.
	if e.Mode == ModeInsert {
		b.WriteString("\x1b[6 q\x1b[?25h")
	} else {
		b.WriteString("\x1b[2 q\x1b[?25h")
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH", row, col)
	os.Stdout.WriteString(b.String())
}
