package core

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func init() {
	addCmd("more", "view files one screen at a time", cmdMore)
	addCmd("less", "view files with backward movement and search", cmdLess)
}

// ---------------------------------------------------------------- screen ---

type KeyKind int

const (
	KeyRune KeyKind = iota
	KeyEnter
	KeyEscape
	KeyBackspace
	KeyTab
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
	KeyDelete
	KeyCtrlC
	KeyEOF
)

type keyEvent struct {
	kind KeyKind
	ch   rune
}

// screen owns the console: UTF-8/VT output plus character-at-a-time input.
type Screen struct {
	in      *os.File
	closeIn func()
	restore func()
	Rows    int
	Cols    int
}

// newScreen switches the console into raw mode. It returns false when stdout is
// not a console (redirected output), in which case callers fall back to a plain
// dump, exactly like the GNU tools do.
func newScreen() (*Screen, bool) {
	if !isTerminal(os.Stdout) {
		return nil, false
	}
	in, closeIn := consoleInput()
	restore := enterRawMode(in)
	rows, cols := terminalSize()
	os.Stdout.WriteString("\x1b[?25l")
	return &Screen{in: in, closeIn: closeIn, restore: restore, Rows: rows, Cols: cols}, true
}

func (s *Screen) close() {
	os.Stdout.WriteString(ansiReset + "\x1b[?25h")
	s.restore()
	s.closeIn()
}

func (s *Screen) clear() { os.Stdout.WriteString("\x1b[2J\x1b[H") }

func (s *Screen) moveTo(row, col int) { fmt.Fprintf(os.Stdout, "\x1b[%d;%dH", row, col) }

func utf8SeqLen(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	}
	return 1
}

func (s *Screen) readByte() (byte, bool) {
	var b [1]byte
	for {
		n, err := s.in.Read(b[:])
		if n == 1 {
			return b[0], true
		}
		if err != nil {
			return 0, false
		}
	}
}

func (s *Screen) readKey() keyEvent {
	b, ok := s.readByte()
	if !ok {
		return keyEvent{kind: KeyEOF}
	}
	switch {
	case b == 0x03:
		return keyEvent{kind: KeyCtrlC}
	case b == '\r' || b == '\n':
		return keyEvent{kind: KeyEnter}
	case b == 0x7f || b == 0x08:
		return keyEvent{kind: KeyBackspace}
	case b == '\t':
		return keyEvent{kind: KeyTab}
	case b == 0x1b:
		return s.readEscape()
	case b < 0x20:
		return keyEvent{kind: KeyRune, ch: rune(b + 64)} // Ctrl+A..Ctrl+Z
	default:
		n := utf8SeqLen(b)
		if n == 1 {
			return keyEvent{kind: KeyRune, ch: rune(b)}
		}
		buf := make([]byte, n)
		buf[0] = b
		for i := 1; i < n; i++ {
			c, ok := s.readByte()
			if !ok {
				break
			}
			buf[i] = c
		}
		r, _ := utf8.DecodeRune(buf)
		return keyEvent{kind: KeyRune, ch: r}
	}
}

// readEscape decodes VT escape sequences (arrows, Home/End, PageUp/PageDown).
// A short wait distinguishes a real Escape key press from a sequence.
func (s *Screen) readEscape() keyEvent {
	if !waitInput(s.in, 30) {
		return keyEvent{kind: KeyEscape}
	}
	b, ok := s.readByte()
	if !ok {
		return keyEvent{kind: KeyEscape}
	}
	if b == '[' || b == 'O' {
		var seq []byte
		for len(seq) < 8 {
			if len(seq) > 0 && !waitInput(s.in, 30) {
				break
			}
			c, ok := s.readByte()
			if !ok {
				break
			}
			seq = append(seq, c)
			if c >= 0x40 && c <= 0x7e && !(c >= '0' && c <= '9') {
				break
			}
		}
		return decodeCSI(string(seq))
	}
	return keyEvent{kind: KeyEscape}
}

func decodeCSI(seq string) keyEvent {
	switch seq {
	case "A":
		return keyEvent{kind: KeyUp}
	case "B":
		return keyEvent{kind: KeyDown}
	case "C":
		return keyEvent{kind: KeyRight}
	case "D":
		return keyEvent{kind: KeyLeft}
	case "H":
		return keyEvent{kind: KeyHome}
	case "F":
		return keyEvent{kind: KeyEnd}
	}
	if strings.HasSuffix(seq, "~") {
		switch strings.TrimSuffix(seq, "~") {
		case "1", "7":
			return keyEvent{kind: KeyHome}
		case "4", "8":
			return keyEvent{kind: KeyEnd}
		case "5":
			return keyEvent{kind: KeyPageUp}
		case "6":
			return keyEvent{kind: KeyPageDown}
		case "3":
			return keyEvent{kind: KeyDelete}
		}
	}
	return keyEvent{kind: KeyEscape}
}

// readLinePrompt draws a one-line prompt on the last screen row and reads a
// line of input (used for /search, :commands and vi's ex mode).
func (s *Screen) readLinePrompt(prompt string) (string, bool) {
	var buf []rune
	for {
		s.moveTo(s.Rows, 1)
		text := prompt + string(buf)
		runes := []rune(text)
		if len(runes) > s.Cols-1 {
			text = string(runes[len(runes)-(s.Cols-1):])
		}
		os.Stdout.WriteString("\x1b[K" + text)
		k := s.readKey()
		switch k.kind {
		case KeyEnter:
			return string(buf), true
		case KeyEscape, KeyCtrlC, KeyEOF:
			return "", false
		case KeyBackspace:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case KeyRune:
			if k.ch >= 32 {
				buf = append(buf, k.ch)
			}
		}
	}
}

// ----------------------------------------------------------------- more ----

func cmdMore(args []string) int {
	pageSize := 0
	startLine := 0
	startPattern := ""
	squeeze := false
	verbosePrompt := false
	var files []string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "+/") && len(a) > 2:
			startPattern = a[2:]
		case len(a) > 1 && a[0] == '+' && isAllDigits(a[1:]):
			startLine, _ = strconv.Atoi(a[1:])
		case len(a) > 1 && a[0] == '-' && isAllDigits(a[1:]):
			pageSize, _ = strconv.Atoi(a[1:])
		case a == "-d":
			verbosePrompt = true
		case a == "-s":
			squeeze = true
		case a == "-u" || a == "-f" || a == "-c" || a == "-l" || a == "-p" || a == "-e" || a == "--no-printf":
			// accepted for compatibility
		case a == "--help":
			fmt.Println("Usage: more [OPTION]... [+LINE] [+/PATTERN] [FILE]...")
			fmt.Println("Options:")
			fmt.Println("  -NUM        show NUM lines per screen")
			fmt.Println("  -d          show a help prompt at the bottom of the screen")
			fmt.Println("  -s          squeeze multiple blank lines into one")
			fmt.Println("  +NUM        start displaying at line NUM")
			fmt.Println("  +/PATTERN   start displaying at the first line matching PATTERN")
			fmt.Println()
			fmt.Println("Commands: SPACE next page, ENTER next line, b previous page, /search, q quit")
			return 0
		case a == "--version":
			printVersion("more")
			return 0
		case isOptionLike(a):
			warn("more", "unknown option '%s'", a)
			return 2
		default:
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	lines, _, err := loadPagerInput(files)
	if err != nil {
		warn("more", "%v", err)
		return 1
	}
	if squeeze {
		lines = squeezeBlankLines(lines)
	}
	if !isTerminal(os.Stdout) {
		dumpLines(lines)
		return 0
	}
	scr, ok := newScreen()
	if !ok {
		dumpLines(lines)
		return 0
	}
	defer scr.close()

	page := pageSize
	if page <= 0 || page > scr.Rows-1 {
		page = scr.Rows - 1
	}
	top := startLine - 1
	if top < 0 {
		top = 0
	}
	var lastPattern string
	if startPattern != "" {
		if re, err := compilePattern(startPattern, false); err == nil {
			lastPattern = startPattern
			for i, l := range lines {
				if re.MatchString(l) {
					top = i
					break
				}
			}
		}
	}
	for {
		if top < 0 {
			top = 0
		}
		if top > len(lines)-1 && len(lines) > 0 {
			top = len(lines) - 1
		}
		end := top + page
		if end > len(lines) {
			end = len(lines)
		}
		scr.clear()
		var b strings.Builder
		for i := top; i < end; i++ {
			b.WriteString(lines[i])
			b.WriteString("\r\n")
		}
		os.Stdout.WriteString(b.String())
		if end >= len(lines) {
			return 0 // reached the end of the file: more exits
		}
		pct := 0
		if len(lines) > 0 {
			pct = end * 100 / len(lines)
		}
		prompt := fmt.Sprintf("--More--(%d%%)", pct)
		scr.moveTo(scr.Rows, 1)
		if verbosePrompt {
			os.Stdout.WriteString("\x1b[7m" + prompt + "[Press space to continue, 'q' to quit.]\x1b[0m\x1b[K")
		} else {
			os.Stdout.WriteString("\x1b[7m" + prompt + "\x1b[0m\x1b[K")
		}
		switch k := scr.readKey(); k.kind {
		case KeyRune:
			switch k.ch {
			case 'q', 'Q':
				return 0
			case ' ':
				top = end
			case 'b':
				top -= page
			case 'd':
				top += page / 2
			case '/':
				if pat, ok := scr.readLinePrompt("/"); ok && pat != "" {
					lastPattern = pat
					if re, err := compilePattern(pat, false); err == nil {
						if idx, found := searchForward(lines, re, top); found {
							top = idx
						}
					}
				}
			case 'n':
				if lastPattern != "" {
					if re, err := compilePattern(lastPattern, false); err == nil {
						if idx, found := searchForward(lines, re, top+1); found {
							top = idx
						}
					}
				}
			case '=':
				scr.moveTo(scr.Rows, 1)
				os.Stdout.WriteString(fmt.Sprintf("\x1b[7m%d\x1b[0m\x1b[K", top+1))
				scr.readKey()
			case 'h', '?':
				showMoreHelp(scr)
			}
		case KeyEnter:
			top++
		case KeyDown:
			top++
		case KeyPageDown, KeyRight:
			top = end
		case KeyUp:
			top--
		case KeyPageUp, KeyLeft:
			top -= page
		case KeyHome:
			top = 0
		case KeyEnd:
			top = len(lines) - 1
		case KeyEscape, KeyCtrlC, KeyEOF:
			return 0
		}
	}
}

func showMoreHelp(scr *Screen) {
	help := []string{
		"",
		" most commands optionally preceded by an integer count. Defaults in [].",
		"",
		"  SPACE or z    display next screen of text [1 screen]",
		"  ENTER or j    display next line [1 line]",
		"  b or CTRL-B   skip backwards one screen",
		"  d or CTRL-D   scroll forwards a half screen",
		"  u or CTRL-U   scroll backwards a half screen",
		"  /pattern      search for the pattern, then n repeats it",
		"  =             display current line number",
		"  q or Q        exit",
		"",
		"  Press any key to continue",
	}
	scr.clear()
	var b strings.Builder
	for _, l := range help {
		b.WriteString(l + "\r\n")
	}
	os.Stdout.WriteString(b.String())
	scr.readKey()
}

// ----------------------------------------------------------------- less ----

type LessState struct {
	Scr        *Screen
	Files      []string
	fidx       int
	Lines      []string
	title      string
	top        int
	Left       int
	Number     bool
	ignoreCase bool
	pattern    string
	Re         *regexp.Regexp
	backwards  bool
	Status     string
}

func cmdLess(args []string) int {
	st := &LessState{}
	startLine := 0
	startPattern := ""
	squeeze := false
	quitIfOneScreen := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "+/") && len(a) > 2:
			startPattern = a[2:]
		case len(a) > 1 && a[0] == '+' && isAllDigits(a[1:]):
			startLine, _ = strconv.Atoi(a[1:])
		case a == "-N" || a == "--LINE-NUMBERS":
			st.Number = true
		case a == "-i" || a == "--ignore-case":
			st.ignoreCase = true
		case a == "-I":
			// case is ignored only when the pattern has no upper case letters
			st.ignoreCase = true
		case a == "-s" || a == "--squeeze-blank-lines":
			squeeze = true
		case a == "-F" || a == "--quit-if-one-screen":
			quitIfOneScreen = true
		case a == "-R" || a == "--RAW-CONTROL-CHARS" || a == "-r" || a == "-X" || a == "-M" || a == "-e" ||
			a == "-n" || a == "-S" || a == "-u" || a == "-g" || a == "-w" || a == "-x" || a == "-E":
			// accepted for command line compatibility
		case a == "--help":
			fmt.Println("Usage: less [OPTION]... [+LINE] [+/PATTERN] [FILE]...")
			fmt.Println("Options:")
			fmt.Println("  -N            show line numbers")
			fmt.Println("  -i            ignore case in searches")
			fmt.Println("  -s            squeeze multiple blank lines into one")
			fmt.Println("  -F            quit if the whole file fits on one screen")
			fmt.Println("  +NUM          start at line NUM")
			fmt.Println("  +/PATTERN     start at the first line matching PATTERN")
			fmt.Println()
			fmt.Println("Commands: j/k or arrows scroll, SPACE/f page down, b page up, g/G first/last,")
			fmt.Println("          /pattern search forward, ?pattern backward, n/N repeat, :n next file, q quit")
			return 0
		case a == "--version":
			printVersion("less")
			return 0
		case isOptionLike(a):
			warn("less", "unknown option '%s'", a)
			return 2
		default:
			st.Files = append(st.Files, a)
		}
	}
	if len(st.Files) == 0 {
		st.Files = []string{"-"}
	}
	if !isTerminal(os.Stdout) {
		lines, _, err := loadPagerInput(st.Files)
		if err != nil {
			warn("less", "%v", err)
			return 1
		}
		dumpLines(lines)
		return 0
	}
	scr, ok := newScreen()
	if !ok {
		lines, _, _ := loadPagerInput(st.Files)
		dumpLines(lines)
		return 0
	}
	st.Scr = scr
	defer scr.close()

	first := true
	for {
		lines, title, err := loadPagerInput([]string{st.Files[st.fidx]})
		if err != nil {
			warn("less", "%v", err)
			return 1
		}
		if squeeze {
			lines = squeezeBlankLines(lines)
		}
		st.Lines, st.title, st.top = lines, title, 0
		if first {
			if startLine > 0 {
				st.top = startLine - 1
			}
			if startPattern != "" {
				if re, err := compilePattern(startPattern, st.ignoreCase); err == nil {
					st.pattern, st.Re = startPattern, re
					if idx, found := searchForward(lines, re, 0); found {
						st.top = idx
					}
				}
			}
			first = false
		}
		if quitIfOneScreen && len(lines) <= scr.Rows-1 {
			dumpLines(lines)
			return 0
		}
		if rc := st.run(); rc >= 0 {
			return rc
		}
	}
}

// run returns -1 to continue with the next file, or a process exit status.
func (l *LessState) run() int {
	textRows := l.Scr.Rows - 1
	quitAll := false
	for {
		l.clamp(textRows)
		l.draw(textRows)
		k := l.Scr.readKey()
		moved := true
		switch k.kind {
		case KeyRune:
			switch k.ch {
			case 'q', 'Q':
				return 0
			case 'j':
				l.top++
			case 'k':
				l.top--
			case ' ':
				l.top += textRows
			case 'f':
				l.top += textRows
			case 'b':
				l.top -= textRows
			case 'd':
				l.top += textRows / 2
			case 'u':
				l.top -= textRows / 2
			case 'g':
				l.top = 0
			case 'G':
				l.top = len(l.Lines) - 1
			case '0', '^':
				l.Left = 0
			case '/', '?':
				prompt := "/"
				back := false
				if k.ch == '?' {
					prompt, back = "?", true
				}
				pat, ok := l.Scr.readLinePrompt(prompt)
				if ok && pat != "" {
					l.search(pat, back)
				} else {
					l.Status = ""
				}
			case 'n':
				if l.Re != nil {
					l.search(l.pattern, l.backwards)
				}
			case 'N':
				if l.Re != nil {
					l.search(l.pattern, !l.backwards)
				}
			case '=':
				l.Status = fmt.Sprintf("%s  line %d/%d", l.title, l.top+1, len(l.Lines))
			case 'h', 'H':
				l.showHelp()
			case ':':
				cmd, ok := l.Scr.readLinePrompt(":")
				if !ok {
					l.Status = ""
					break
				}
				switch strings.TrimSpace(cmd) {
				case "q", "q!":
					return 0
				case "n":
					if l.fidx < len(l.Files)-1 {
						l.fidx++
						return -1
					}
					l.Status = "No next file"
				case "p":
					if l.fidx > 0 {
						l.fidx--
						return -1
					}
					l.Status = "No previous file"
				case "N":
					quitAll = true
				default:
					l.Status = "Unknown command: " + strings.TrimSpace(cmd)
				}
				if quitAll {
					return 0
				}
			default:
				moved = false
			}
		case KeyDown, KeyEnter:
			l.top++
		case KeyUp:
			l.top--
		case KeyPageDown:
			l.top += textRows
		case KeyPageUp:
			l.top -= textRows
		case KeyRight:
			l.Left++
		case KeyLeft:
			if l.Left > 0 {
				l.Left--
			}
		case KeyHome:
			l.top = 0
		case KeyEnd:
			l.top = len(l.Lines) - 1
		case KeyEscape:
			l.Status = ""
			moved = false
		case KeyCtrlC, KeyEOF:
			return 0
		default:
			moved = false
		}
		_ = moved
	}
}

func (l *LessState) clamp(textRows int) {
	if l.top < 0 {
		l.top = 0
	}
	maxTop := len(l.Lines) - textRows
	if maxTop < 0 {
		maxTop = 0
	}
	if l.top > maxTop {
		l.top = maxTop
	}
	if l.Left < 0 {
		l.Left = 0
	}
}

func (l *LessState) draw(textRows int) {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	for i := 0; i < textRows; i++ {
		idx := l.top + i
		if idx >= len(l.Lines) {
			b.WriteString("~\x1b[K\r\n")
			continue
		}
		b.WriteString(l.DisplayLine(idx))
		b.WriteString("\x1b[K\r\n")
	}
	end := l.top + textRows
	if end > len(l.Lines) {
		end = len(l.Lines)
	}
	pct := 100
	if len(l.Lines) > 0 {
		pct = end * 100 / len(l.Lines)
	}
	state := ""
	if end >= len(l.Lines) {
		state = "  (END)"
	}
	status := fmt.Sprintf("%s  lines %d-%d/%d  %d%%%s", l.title, l.top+1, end, len(l.Lines), pct, state)
	if l.Status != "" {
		status += "   " + l.Status
	}
	b.WriteString("\x1b[7m" + truncRun(status, l.Scr.Cols) + "\x1b[0m\x1b[K")
	os.Stdout.WriteString(b.String())
}

func (l *LessState) DisplayLine(idx int) string {
	line := []rune(l.Lines[idx])
	prefix := ""
	if l.Number {
		prefix = fmt.Sprintf("%5d ", idx+1)
	}
	width := l.Scr.Cols - len([]rune(prefix))
	if width < 1 {
		width = 1
	}
	if l.Left > len(line) {
		line = nil
	} else {
		line = line[l.Left:]
	}
	if len(line) > width {
		line = line[:width-1]
	}
	text := string(line)
	if l.Re != nil {
		text = highlight(text, l.Re) + ansiReset
	}
	return prefix + text
}

func (l *LessState) search(pat string, backwards bool) {
	re, err := compilePattern(pat, l.ignoreCase)
	if err != nil {
		l.Status = "Invalid pattern: " + pat
		return
	}
	l.pattern, l.Re, l.backwards = pat, re, backwards
	from := l.top + 1
	if backwards {
		from = l.top - 1
	}
	if idx, found := searchFrom(l.Lines, re, from, backwards); found {
		l.top = idx
		l.Status = ""
		return
	}
	l.Status = "Pattern not found: " + pat
}

func (l *LessState) showHelp() {
	help := []string{
		"",
		"  j, ENTER, Down      scroll forward one line",
		"  k, Up               scroll backward one line",
		"  SPACE, f, PageDown  scroll forward one screen",
		"  b, PageUp           scroll backward one screen",
		"  d / u               scroll forward / backward half a screen",
		"  g / G, Home / End   go to first / last line",
		"  /pattern            search forward, ?pattern backward",
		"  n / N               repeat the search / in the other direction",
		"  :n / :p             next / previous file",
		"  =                   show file name and line numbers",
		"  q                   quit",
		"",
		"  Press any key to continue",
	}
	l.Scr.clear()
	var b strings.Builder
	for _, line := range help {
		b.WriteString(line + "\r\n")
	}
	os.Stdout.WriteString(b.String())
	l.Scr.readKey()
}

// ---------------------------------------------------------------- helpers --

func compilePattern(pat string, ignoreCase bool) (*regexp.Regexp, error) {
	src := pat
	if ignoreCase || !strings.ContainsAny(pat, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		src = "(?i)" + src // smart case, as in less
	}
	return regexp.Compile(src)
}

func searchForward(lines []string, re *regexp.Regexp, from int) (int, bool) {
	return searchFrom(lines, re, from, false)
}

func searchFrom(lines []string, re *regexp.Regexp, from int, backwards bool) (int, bool) {
	n := len(lines)
	if n == 0 {
		return 0, false
	}
	if backwards {
		for i := from; i >= 0; i-- {
			if re.MatchString(lines[i]) {
				return i, true
			}
		}
		for i := n - 1; i > from; i-- {
			if re.MatchString(lines[i]) {
				return i, true
			}
		}
		return 0, false
	}
	for i := from; i < n; i++ {
		if i < 0 {
			continue
		}
		if re.MatchString(lines[i]) {
			return i, true
		}
	}
	for i := 0; i < from && i < n; i++ {
		if re.MatchString(lines[i]) {
			return i, true
		}
	}
	return 0, false
}

func loadPagerInput(files []string) ([]string, string, error) {
	var lines []string
	var names []string
	for _, name := range files {
		ls, err := readAllLines(name)
		if err != nil {
			return nil, "", fmt.Errorf("cannot open '%s' for reading: No such file or directory", name)
		}
		lines = append(lines, ls...)
		if name == "-" {
			names = append(names, "(standard input)")
		} else {
			names = append(names, name)
		}
	}
	return lines, strings.Join(names, " "), nil
}

func dumpLines(lines []string) {
	if len(lines) == 0 {
		return
	}
	w := os.Stdout
	buf := make([]byte, 0, 64*1024)
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
		if len(buf) > 32*1024 {
			w.Write(buf)
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		w.Write(buf)
	}
}

func squeezeBlankLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		blank := strings.TrimSpace(l) == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, l)
		prevBlank = blank
	}
	return out
}

func truncRun(s string, cols int) string {
	r := []rune(s)
	if cols <= 0 || len(r) <= cols {
		return s
	}
	return string(r[:cols])
}
