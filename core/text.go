package core

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func init() {
	addCmd("cat", "concatenate files and print on standard output", cmdCat)
	addCmd("head", "output the first part of files", cmdHead)
	addCmd("tail", "output the last part of files (supports -f)", cmdTail)
	addCmd("wc", "print newline, word and byte counts", cmdWc)
	addCmd("sort", "sort lines of text files", cmdSort)
	addCmd("uniq", "report or omit repeated lines", cmdUniq)
	addCmd("cut", "remove sections from each line of files", cmdCut)
	addCmd("tr", "translate or delete characters", cmdTr)
}

// countSpec models the "[+]N" / "-N" argument forms shared by head and tail.
type countSpec struct {
	n         int
	fromStart bool // +N
	negative  bool // -N
}

func parseCount(s string, def int) (countSpec, error) {
	if s == "" {
		return countSpec{n: def}, nil
	}
	c := countSpec{}
	body := strings.TrimSpace(s)
	if body == "" {
		return c, fmt.Errorf("invalid number")
	}
	switch body[0] {
	case '+':
		c.fromStart = true
		body = body[1:]
	case '-':
		c.negative = true
		body = body[1:]
	}
	if !isAllDigits(body) {
		return c, fmt.Errorf("invalid number")
	}
	n, err := strconv.Atoi(body)
	if err != nil {
		return c, fmt.Errorf("invalid number")
	}
	c.n = n
	return c, nil
}

// ----------------------------------------------------------------- cat -----

func cmdCat(args []string) int {
	f := NewFlags(
		&Flag{Name: "number-nonblank", Short: 'b', Long: "number-nonblank", Help: "number nonempty output lines"},
		&Flag{Name: "number", Short: 'n', Long: "number", Help: "number all output lines"},
		&Flag{Name: "squeeze-blank", Short: 's', Long: "squeeze-blank", Help: "suppress repeated empty output lines"},
		&Flag{Name: "show-ends", Short: 'E', Long: "show-ends", Help: "display $ at end of each line"},
		&Flag{Name: "show-tabs", Short: 'T', Long: "show-tabs", Help: "display TAB characters as ^I"},
		&Flag{Name: "show-nonprinting", Short: 'v', Long: "show-nonprinting", Help: "use ^ and M- notation for non printing characters"},
		&Flag{Name: "show-all", Short: 'A', Long: "show-all", Help: "equivalent to -vET"},
		&Flag{Name: "show-all", Short: 'e', Help: "equivalent to -vE"},
		&Flag{Name: "show-all", Short: 't', Help: "equivalent to -vT"},
	)
	if !parseOrDie("cat", f, args) {
		return 2
	}
	if handleHelpVersion("cat", "[OPTION]... [FILE]...", f) {
		return 0
	}
	files := f.Positional
	if len(files) == 0 {
		files = []string{"-"}
	}
	showAll := f.Has("show-all")
	showEnds := f.Has("show-ends") || showAll
	showTabs := f.Has("show-tabs") || showAll
	showNP := f.Has("show-nonprinting") || showAll
	number := f.Has("number") || f.Has("number-nonblank")
	numberNonBlank := f.Has("number-nonblank")
	squeeze := f.Has("squeeze-blank")
	plain := !showEnds && !showTabs && !showNP

	exit := 0
	lineNo := 1
	prevBlank := false
	for _, name := range files {
		r, err := openInput(name)
		if err != nil {
			warn("cat", "cannot open '%s' for reading: No such file or directory", name)
			exit = 1
			continue
		}
		if plain && !number && !squeeze {
			if _, err := io.Copy(os.Stdout, r); err != nil {
				warn("cat", "error reading '%s': %v", name, err)
				exit = 1
			}
			r.Close()
			continue
		}
		sc := newScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if squeeze {
				if line == "" {
					if prevBlank {
						continue
					}
					prevBlank = true
				} else {
					prevBlank = false
				}
			}
			out := line
			if showTabs {
				out = strings.ReplaceAll(out, "\t", "^I")
			}
			if showNP {
				out = showNonPrinting(out)
			}
			if showEnds {
				out += "$"
			}
			switch {
			case numberNonBlank:
				if line != "" {
					fmt.Printf("%6d\t%s\n", lineNo, out)
					lineNo++
				} else {
					fmt.Println(out)
				}
			case number:
				fmt.Printf("%6d\t%s\n", lineNo, out)
				lineNo++
			default:
				fmt.Println(out)
			}
		}
		if err := sc.Err(); err != nil {
			warn("cat", "error reading '%s': %v", name, err)
			exit = 1
		}
		r.Close()
	}
	return exit
}

// ---------------------------------------------------------------- head -----

func cmdHead(args []string) int {
	f := NewFlags(
		&Flag{Name: "lines", Short: 'n', Long: "lines", HasArg: true, ArgName: "[-]N", Help: "print the first N lines (default 10); -N means all but the last N"},
		&Flag{Name: "bytes", Short: 'c', Long: "bytes", HasArg: true, ArgName: "[-]N", Help: "print the first N bytes"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "never print headers giving file names"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "always print headers giving file names"},
	)
	if !parseOrDie("head", f, preScanShortNumber(args, "-n", "-c", "--lines", "--bytes")) {
		return 2
	}
	if handleHelpVersion("head", "[OPTION]... [FILE]...", f) {
		return 0
	}
	lineSpec, err := parseCount(f.Str("lines"), 10)
	if err != nil {
		warn("head", "invalid number of lines: '%s'", f.Str("lines"))
		return 1
	}
	byteSpec, err := parseCount(f.Str("bytes"), 0)
	if err != nil {
		warn("head", "invalid number of bytes: '%s'", f.Str("bytes"))
		return 1
	}
	byteMode := f.Has("bytes")
	files := f.Positional
	if len(files) == 0 {
		files = []string{"-"}
	}
	header := (len(files) > 1 && !f.Has("quiet")) || f.Has("verbose")
	exit := 0
	for i, name := range files {
		r, err := openInput(name)
		if err != nil {
			warn("head", "cannot open '%s' for reading: No such file or directory", name)
			exit = 1
			continue
		}
		if header {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("==> %s <==\n", name)
		}
		if byteMode {
			err = headBytes(r, os.Stdout, byteSpec)
		} else {
			err = headLines(r, os.Stdout, lineSpec)
		}
		if err != nil {
			warn("head", "error reading '%s': %v", name, err)
			exit = 1
		}
		r.Close()
	}
	return exit
}

func headLines(r io.Reader, w io.Writer, spec countSpec) error {
	switch {
	case spec.fromStart:
		idx := 1
		sc := newScanner(r)
		for sc.Scan() {
			if idx >= spec.n {
				fmt.Fprintln(w, sc.Text())
			}
			idx++
		}
		return sc.Err()
	case spec.negative:
		lines, err := readLinesFrom(r)
		if err != nil {
			return err
		}
		end := len(lines) - spec.n
		if end < 0 {
			end = 0
		}
		for i := 0; i < end; i++ {
			fmt.Fprintln(w, lines[i])
		}
		return nil
	default:
		if spec.n <= 0 {
			return nil
		}
		sc := newScanner(r)
		for i := 0; i < spec.n && sc.Scan(); i++ {
			fmt.Fprintln(w, sc.Text())
		}
		return sc.Err()
	}
}

func headBytes(r io.Reader, w io.Writer, spec countSpec) error {
	switch {
	case spec.fromStart:
		start := int64(spec.n - 1)
		if start < 0 {
			start = 0
		}
		if start > 0 {
			if _, err := io.CopyN(io.Discard, r, start); err != nil && err != io.EOF {
				return err
			}
		}
		_, err := io.Copy(w, r)
		return err
	case spec.negative:
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		end := int64(len(data)) - int64(spec.n)
		if end < 0 {
			end = 0
		}
		_, err = w.Write(data[:end])
		return err
	default:
		if spec.n <= 0 {
			return nil
		}
		_, err := io.CopyN(w, r, int64(spec.n))
		if err == io.EOF {
			return nil
		}
		return err
	}
}

// ---------------------------------------------------------------- tail -----

type tailOpts struct {
	linesSet bool
	Lines    countSpec
	bytesSet bool
	bytes    countSpec
	follow   bool
	retry    bool
	quiet    bool
	verbose  bool
	sleep    time.Duration
	pid      int
}

func cmdTail(args []string) int {
	// `tail +N`, `tail -N` and `head -N` style shorthands, without touching the
	// value that belongs to a preceding option (`tail -n +3`).
	valueOpts := map[string]bool{
		"-n": true, "-c": true, "-s": true,
		"--lines": true, "--bytes": true, "--sleep-interval": true, "--pid": true,
	}
	pre := make([]string, 0, len(args))
	expectValue := false
	for _, a := range args {
		if expectValue {
			expectValue = false
			pre = append(pre, a)
			continue
		}
		if len(a) > 1 && a[0] == '+' && isAllDigits(a[1:]) {
			pre = append(pre, "-n", a)
			continue
		}
		if len(a) > 1 && a[0] == '-' && isAllDigits(a[1:]) {
			pre = append(pre, "-n", a[1:])
			continue
		}
		if valueOpts[a] {
			expectValue = true
		}
		pre = append(pre, a)
	}

	f := NewFlags(
		&Flag{Name: "lines", Short: 'n', Long: "lines", HasArg: true, ArgName: "[+]N", Help: "output the last N lines (default 10); +N starts at line N"},
		&Flag{Name: "bytes", Short: 'c', Long: "bytes", HasArg: true, ArgName: "[+]N", Help: "output the last N bytes; +N starts at byte N"},
		&Flag{Name: "follow", Short: 'f', Long: "follow", OptArg: true, ArgName: "[=name]", Help: "output appended data as the file grows"},
		&Flag{Name: "retry", Short: 'F', Help: "same as -f --follow=name: keep retrying a vanished file"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "never output headers giving file names"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "always output headers giving file names"},
		&Flag{Name: "sleep-interval", Short: 's', Long: "sleep-interval", HasArg: true, ArgName: "SEC", Help: "sleep SEC seconds between polls with -f (default 1)"},
		&Flag{Name: "pid", Long: "pid", HasArg: true, ArgName: "PID", Help: "with -f, terminate after process PID dies"},
	)
	if !parseOrDie("tail", f, pre) {
		return 2
	}
	if handleHelpVersion("tail", "[OPTION]... [FILE]...", f) {
		return 0
	}
	o := &tailOpts{
		linesSet: f.Has("lines"),
		bytesSet: f.Has("bytes"),
		quiet:    f.Has("quiet"),
		verbose:  f.Has("verbose"),
		sleep:    time.Second,
	}
	o.Lines, _ = parseCount(f.Str("lines"), 10)
	o.bytes, _ = parseCount(f.Str("bytes"), 10)
	o.follow = f.Has("follow") || f.Has("retry")
	o.retry = f.Has("retry") || strings.EqualFold(f.Str("follow"), "name")
	if s := f.Str("sleep-interval"); s != "" {
		sec, err := strconv.ParseFloat(s, 64)
		if err != nil || sec < 0 {
			warn("tail", "invalid number of seconds: '%s'", s)
			return 1
		}
		o.sleep = time.Duration(sec * float64(time.Second))
	}
	if p := f.Str("pid"); p != "" {
		pid, err := strconv.Atoi(p)
		if err != nil || pid <= 0 {
			warn("tail", "invalid PID: '%s'", p)
			return 1
		}
		o.pid = pid
	}
	files := f.Positional
	if len(files) == 0 {
		files = []string{"-"}
	}
	header := (len(files) > 1 && !o.quiet) || o.verbose
	exit := 0
	for i, name := range files {
		if header && name != "-" {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("==> %s <==\n", name)
		}
		if err := tailFile(name, o); err != nil {
			warn("tail", "%v", err)
			exit = 1
		}
	}
	if exit != 0 {
		return exit
	}
	if o.follow {
		return tailFollow(files, o, header)
	}
	return 0
}

func tailFile(name string, o *tailOpts) error {
	if name == "-" {
		if o.bytesSet {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			return writeTailBytes(data, o.bytes)
		}
		return tailLines(os.Stdin, os.Stdout, o.Lines)
	}
	fh, err := os.Open(name)
	if err != nil {
		return fmt.Errorf("cannot open '%s' for reading: No such file or directory", name)
	}
	defer fh.Close()
	if o.bytesSet {
		fi, err := fh.Stat()
		if err != nil {
			return err
		}
		if o.bytes.fromStart {
			start := int64(o.bytes.n - 1)
			if start < 0 {
				start = 0
			}
			if _, err := fh.Seek(start, io.SeekStart); err != nil {
				return err
			}
			_, err = io.Copy(os.Stdout, fh)
			return err
		}
		start := fi.Size() - int64(o.bytes.n)
		if start < 0 {
			start = 0
		}
		if _, err := fh.Seek(start, io.SeekStart); err != nil {
			return err
		}
		_, err = io.Copy(os.Stdout, fh)
		return err
	}
	return tailLines(fh, os.Stdout, o.Lines)
}

func writeTailBytes(data []byte, spec countSpec) error {
	if spec.fromStart {
		start := spec.n - 1
		if start < 0 {
			start = 0
		}
		if start > len(data) {
			start = len(data)
		}
		_, err := os.Stdout.Write(data[start:])
		return err
	}
	start := len(data) - spec.n
	if start < 0 {
		start = 0
	}
	_, err := os.Stdout.Write(data[start:])
	return err
}

func tailLines(r io.Reader, w io.Writer, spec countSpec) error {
	if spec.fromStart {
		idx := 1
		sc := newScanner(r)
		for sc.Scan() {
			if idx >= spec.n {
				fmt.Fprintln(w, sc.Text())
			}
			idx++
		}
		return sc.Err()
	}
	n := spec.n
	if n <= 0 {
		return nil
	}
	buf := make([]string, n)
	count := 0
	sc := newScanner(r)
	for sc.Scan() {
		buf[count%n] = sc.Text()
		count++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	shown := count
	if shown > n {
		shown = n
	}
	start := 0
	if count > n {
		start = count % n
	}
	for i := 0; i < shown; i++ {
		fmt.Fprintln(w, buf[(start+i)%n])
	}
	return nil
}

func tailFollow(files []string, o *tailOpts, header bool) int {
	files = filterFollowable(files)
	if len(files) == 0 {
		return 0
	}
	sizes := make([]int64, len(files))
	gone := make([]bool, len(files))
	for i, name := range files {
		fi, err := os.Stat(name)
		if err != nil {
			sizes[i] = -1
			gone[i] = true
			if !o.retry {
				warn("tail", "cannot open '%s' for reading: No such file or directory", name)
				return 1
			}
			warn("tail", "'%s' has become inaccessible: No such file or directory", name)
			continue
		}
		sizes[i] = fi.Size()
	}
	for {
		if o.pid > 0 && !processAlive(o.pid) {
			return 0
		}
		time.Sleep(o.sleep)
		for i, name := range files {
			fi, err := os.Stat(name)
			if err != nil {
				if !gone[i] {
					gone[i] = true
					sizes[i] = -1
					if o.retry {
						warn("tail", "'%s' has become inaccessible: No such file or directory", name)
					} else {
						warn("tail", "cannot open '%s' for reading: No such file or directory", name)
						return 1
					}
				}
				continue
			}
			if gone[i] {
				gone[i] = false
				sizes[i] = fi.Size()
				if o.retry {
					warn("tail", "'%s' has appeared; following new file", name)
				}
				continue
			}
			if fi.Size() < sizes[i] {
				warn("tail", "%s: file truncated", name)
				sizes[i] = 0
			}
			if fi.Size() == sizes[i] {
				continue
			}
			fh, err := os.Open(name)
			if err != nil {
				continue
			}
			if header {
				fmt.Printf("\n==> %s <==\n", name)
			}
			if _, err := fh.Seek(sizes[i], io.SeekStart); err == nil {
				io.Copy(os.Stdout, fh)
			}
			sizes[i] = fi.Size()
			fh.Close()
		}
	}
}

func filterFollowable(files []string) []string {
	var out []string
	for _, f := range files {
		if f == "-" {
			continue // a pipe cannot be followed
		}
		out = append(out, f)
	}
	return out
}

// ------------------------------------------------------------------ wc -----

type wcCounts struct {
	Lines, words, chars, bytes, maxLine int64
}

func cmdWc(args []string) int {
	f := NewFlags(
		&Flag{Name: "lines", Short: 'l', Long: "lines", Help: "print the newline count"},
		&Flag{Name: "words", Short: 'w', Long: "words", Help: "print the word count"},
		&Flag{Name: "bytes", Short: 'c', Long: "bytes", Help: "print the byte count"},
		&Flag{Name: "chars", Short: 'm', Long: "chars", Help: "print the character count"},
		&Flag{Name: "max-line-length", Short: 'L', Long: "max-line-length", Help: "print the maximum line length"},
	)
	if !parseOrDie("wc", f, args) {
		return 2
	}
	if handleHelpVersion("wc", "[OPTION]... [FILE]...", f) {
		return 0
	}
	showL := f.Has("lines")
	showW := f.Has("words")
	showC := f.Has("bytes")
	showM := f.Has("chars")
	showMax := f.Has("max-line-length")
	if !showL && !showW && !showC && !showM && !showMax {
		showL, showW, showC = true, true, true
	}
	files := f.Positional
	if len(files) == 0 {
		files = []string{"-"}
	}
	var total wcCounts
	exit := 0
	for _, name := range files {
		r, err := openInput(name)
		if err != nil {
			warn("wc", "%s: No such file or directory", name)
			exit = 1
			continue
		}
		c, err := countStream(r)
		r.Close()
		if err != nil {
			warn("wc", "%s: %v", name, err)
			exit = 1
			continue
		}
		printCounts(os.Stdout, c, showL, showW, showM, showC, showMax, name)
		total.Lines += c.Lines
		total.words += c.words
		total.chars += c.chars
		total.bytes += c.bytes
		if c.maxLine > total.maxLine {
			total.maxLine = c.maxLine
		}
	}
	if len(files) > 1 {
		printCounts(os.Stdout, total, showL, showW, showM, showC, showMax, "total")
	}
	return exit
}

func printCounts(w io.Writer, c wcCounts, showL, showW, showM, showC, showMax bool, name string) {
	var parts []string
	if showL {
		parts = append(parts, fmt.Sprintf("%7d", c.Lines))
	}
	if showW {
		parts = append(parts, fmt.Sprintf("%7d", c.words))
	}
	if showM {
		parts = append(parts, fmt.Sprintf("%7d", c.chars))
	}
	if showC {
		parts = append(parts, fmt.Sprintf("%7d", c.bytes))
	}
	if showMax {
		parts = append(parts, fmt.Sprintf("%7d", c.maxLine))
	}
	line := strings.Join(parts, "")
	if name != "" {
		line += " " + name
	}
	fmt.Fprintln(w, line)
}

// countStream counts lines, words, characters, bytes and the longest line.
// A trailing CR before LF is not counted as part of the line length so that
// Windows files report the same numbers as their Unix counterparts.
func countStream(r io.Reader) (wcCounts, error) {
	var c wcCounts
	br := bufio.NewReaderSize(r, 64*1024)
	inWord := false
	var lineChars int64
	pendingCR := false
	for {
		ru, size, err := br.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return c, err
		}
		c.bytes += int64(size)
		c.chars++
		if ru == '\n' {
			c.Lines++
			if lineChars > c.maxLine {
				c.maxLine = lineChars
			}
			lineChars = 0
			pendingCR = false
			inWord = false
			continue
		}
		if ru == '\r' {
			pendingCR = true
			inWord = false
			continue
		}
		if pendingCR {
			lineChars++
			pendingCR = false
		}
		lineChars++
		if unicode.IsSpace(ru) {
			inWord = false
		} else if !inWord {
			inWord = true
			c.words++
		}
	}
	if lineChars > c.maxLine {
		c.maxLine = lineChars
	}
	return c, nil
}

// ---------------------------------------------------------------- sort -----

type sortKey struct {
	start, end int
	numeric    bool
	reverse    bool
	fold       bool
	sep        string
}

func cmdSort(args []string) int {
	f := NewFlags(
		&Flag{Name: "ignore-leading-blanks", Short: 'b', Long: "ignore-leading-blanks", Help: "ignore leading blanks"},
		&Flag{Name: "check", Short: 'c', Long: "check", Help: "check whether input is sorted; do not sort"},
		&Flag{Name: "ignore-case", Short: 'f', Long: "ignore-case", Help: "fold lower case to upper case characters"},
		&Flag{Name: "key", Short: 'k', Long: "key", HasArg: true, ArgName: "KEYDEF", Help: "sort by key: -k2 or -k2,3 (a trailing n/r/f tunes the key)"},
		&Flag{Name: "numeric-sort", Short: 'n', Long: "numeric-sort", Help: "compare according to string numerical value"},
		&Flag{Name: "reverse", Short: 'r', Long: "reverse", Help: "reverse the result of comparisons"},
		&Flag{Name: "field-separator", Short: 't', Long: "field-separator", HasArg: true, ArgName: "SEP", Help: "use SEP instead of blank to split fields"},
		&Flag{Name: "unique", Short: 'u', Long: "unique", Help: "output only the first of an equal run"},
		&Flag{Name: "stable", Short: 's', Long: "stable", Help: "stabilise sort by disabling last-resort comparison"},
	)
	if !parseOrDie("sort", f, args) {
		return 2
	}
	if handleHelpVersion("sort", "[OPTION]... [FILE]...", f) {
		return 0
	}
	numeric := f.Has("numeric-sort")
	reverse := f.Has("reverse")
	fold := f.Has("ignore-case")
	unique := f.Has("unique")
	sep := f.Str("field-separator")
	if s := f.Str("field-separator"); strings.EqualFold(s, `\t`) {
		sep = "\t"
	}
	var key *sortKey
	if kd := f.Str("key"); kd != "" {
		k, err := parseKeyDef(kd)
		if err != nil {
			warn("sort", "%v", err)
			return 2
		}
		k.sep = sep
		k.numeric = k.numeric || numeric
		k.reverse = k.reverse || reverse
		k.fold = k.fold || fold
		key = k
	}

	files := f.Positional
	if len(files) == 0 {
		files = []string{"-"}
	}
	var lines []string
	exit := 0
	for _, name := range files {
		ls, err := readAllLines(name)
		if err != nil {
			warn("sort", "cannot read: %s: No such file or directory", name)
			exit = 1
			continue
		}
		lines = append(lines, ls...)
	}
	if exit != 0 {
		return exit
	}
	keyOf := func(s string) string {
		if key == nil {
			if f.Has("ignore-leading-blanks") {
				return strings.TrimLeft(s, " \t")
			}
			return s
		}
		return key.extract(s)
	}
	if f.Has("check") {
		for i := 1; i < len(lines); i++ {
			if compareKeys(keyOf(lines[i-1]), keyOf(lines[i]), key, numeric, fold) > 0 {
				warn("sort", "disorder: %s", lines[i])
				return 1
			}
		}
		return 0
	}
	sort.SliceStable(lines, func(i, j int) bool {
		c := compareKeys(keyOf(lines[i]), keyOf(lines[j]), key, numeric, fold)
		if c == 0 && key == nil {
			c = strings.Compare(lines[i], lines[j])
		}
		if reverse {
			return c > 0
		}
		return c < 0
	})
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for i, l := range lines {
		if unique && i > 0 {
			if compareKeys(keyOf(lines[i-1]), keyOf(l), key, numeric, fold) == 0 {
				continue
			}
		}
		fmt.Fprintln(out, l)
	}
	return 0
}

func parseKeyDef(s string) (*sortKey, error) {
	k := &sortKey{}
	body := strings.TrimSpace(s)
	for len(body) > 0 {
		last := body[len(body)-1]
		switch last {
		case 'n':
			k.numeric = true
		case 'r':
			k.reverse = true
		case 'f':
			k.fold = true
		default:
			goto fields
		}
		body = body[:len(body)-1]
	}
fields:
	if body == "" {
		return nil, fmt.Errorf("invalid key specification '%s'", s)
	}
	parts := strings.SplitN(body, ",", 2)
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start < 1 {
		return nil, fmt.Errorf("invalid key specification '%s'", s)
	}
	k.start = start
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || end < start {
			return nil, fmt.Errorf("invalid key specification '%s'", s)
		}
		k.end = end
	}
	return k, nil
}

func (k *sortKey) extract(line string) string {
	var fields []string
	if k.sep != "" {
		fields = strings.Split(line, k.sep)
	} else {
		fields = strings.Fields(line)
	}
	if k.start > len(fields) {
		return ""
	}
	end := k.end
	if end == 0 || end > len(fields) {
		end = len(fields)
	}
	sep := k.sep
	if sep == "" {
		sep = " "
	}
	return strings.Join(fields[k.start-1:end], sep)
}

func compareKeys(a, b string, k *sortKey, numeric, fold bool) int {
	useNum := numeric
	useFold := fold
	if k != nil {
		useNum = useNum || k.numeric
		useFold = useFold || k.fold
	}
	if useNum {
		fa, fb := leadingNumber(a), leadingNumber(b)
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		default:
			return 0
		}
	}
	if useFold {
		a, b = strings.ToUpper(a), strings.ToUpper(b)
	}
	return strings.Compare(a, b)
}

func leadingNumber(s string) float64 {
	s = strings.TrimLeft(s, " \t")
	end := 0
	for end < len(s) {
		ch := s[end]
		if (ch >= '0' && ch <= '9') || ch == '-' || ch == '+' || ch == '.' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0
	}
	return v
}

// ---------------------------------------------------------------- uniq -----

func cmdUniq(args []string) int {
	f := NewFlags(
		&Flag{Name: "count", Short: 'c', Long: "count", Help: "prefix lines by the number of occurrences"},
		&Flag{Name: "repeated", Short: 'd', Long: "repeated", Help: "only print duplicate lines, one for each group"},
		&Flag{Name: "all-repeated", Short: 'D', Long: "all-repeated", Help: "print all duplicate lines"},
		&Flag{Name: "ignore-case", Short: 'i', Long: "ignore-case", Help: "ignore differences in case when comparing"},
		&Flag{Name: "skip-fields", Short: 'f', Long: "skip-fields", HasArg: true, ArgName: "N", Help: "avoid comparing the first N fields"},
		&Flag{Name: "unique", Short: 'u', Long: "unique", Help: "only print unique lines"},
	)
	if !parseOrDie("uniq", f, args) {
		return 2
	}
	if handleHelpVersion("uniq", "[OPTION]... [INPUT [OUTPUT]]", f) {
		return 0
	}
	op := f.Positional
	if len(op) > 2 {
		warn("uniq", "extra operand '%s'", op[2])
		return 1
	}
	in := "-"
	if len(op) > 0 {
		in = op[0]
	}
	skipFields, _ := f.Int("skip-fields", 0)
	lines, err := readAllLines(in)
	if err != nil {
		warn("uniq", "%s: No such file or directory", in)
		return 1
	}
	out := os.Stdout
	if len(op) > 1 {
		fh, err := os.Create(op[1])
		if err != nil {
			warn("uniq", "cannot open '%s' for writing: %v", op[1], err)
			return 1
		}
		defer fh.Close()
		out = fh
	}
	key := func(s string) string {
		if skipFields > 0 {
			fields := strings.Fields(s)
			if skipFields < len(fields) {
				s = strings.Join(fields[skipFields:], " ")
			} else {
				s = ""
			}
		}
		if f.Has("ignore-case") {
			s = strings.ToUpper(s)
		}
		return s
	}
	flush := func(line string, count int) {
		if f.Has("repeated") && count < 2 {
			return
		}
		if f.Has("unique") && count > 1 {
			return
		}
		if f.Has("all-repeated") && count < 2 {
			return
		}
		if f.Has("count") {
			fmt.Fprintf(out, "%7d %s\n", count, line)
			return
		}
		reps := 1
		if f.Has("all-repeated") {
			reps = count
		}
		for i := 0; i < reps; i++ {
			fmt.Fprintln(out, line)
		}
	}
	started := false
	prevKey := ""
	cur := ""
	count := 0
	for _, l := range lines {
		k := key(l)
		if !started {
			started, prevKey, cur, count = true, k, l, 1
			continue
		}
		if k == prevKey {
			count++
			continue
		}
		flush(cur, count)
		prevKey, cur, count = k, l, 1
	}
	if started {
		flush(cur, count)
	}
	return 0
}

// ----------------------------------------------------------------- cut -----

func cmdCut(args []string) int {
	f := NewFlags(
		&Flag{Name: "characters", Short: 'c', Long: "characters", HasArg: true, ArgName: "LIST", Help: "select these characters"},
		&Flag{Name: "delimiter", Short: 'd', Long: "delimiter", HasArg: true, ArgName: "DELIM", Help: "use DELIM instead of TAB for the field delimiter"},
		&Flag{Name: "fields", Short: 'f', Long: "fields", HasArg: true, ArgName: "LIST", Help: "select these fields"},
		&Flag{Name: "only-delimited", Short: 's', Long: "only-delimited", Help: "do not print lines not containing delimiters"},
		&Flag{Name: "output-delimiter", Long: "output-delimiter", HasArg: true, ArgName: "STRING", Help: "use STRING as the output delimiter"},
	)
	if !parseOrDie("cut", f, args) {
		return 2
	}
	if handleHelpVersion("cut", "[OPTION]... [FILE]...", f) {
		return 0
	}
	fieldsSpec := f.Str("fields")
	charsSpec := f.Str("characters")
	if fieldsSpec == "" && charsSpec == "" {
		warn("cut", "you must specify a list of bytes, characters, or fields")
		return 1
	}
	if fieldsSpec != "" && charsSpec != "" {
		warn("cut", "only one type of list may be specified")
		return 1
	}
	var ranges []RangeSpec
	var err error
	if fieldsSpec != "" {
		ranges, err = parseListSpec(fieldsSpec)
	} else {
		ranges, err = parseListSpec(charsSpec)
	}
	if err != nil {
		warn("cut", "%v", err)
		return 1
	}
	delim := f.Str("delimiter")
	if delim == "" {
		delim = "\t"
	}
	if d := strings.ToLower(delim); d == `\t` {
		delim = "\t"
	}
	outDelim := f.Str("output-delimiter")
	if outDelim == "" {
		outDelim = delim
	}
	if f.Has("only-delimited") && fieldsSpec == "" {
		warn("cut", "suppressing non-delimited lines makes sense only when operating on fields")
		return 1
	}
	files := f.Positional
	if len(files) == 0 {
		files = []string{"-"}
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	exit := 0
	for _, name := range files {
		r, err := openInput(name)
		if err != nil {
			warn("cut", "%s: No such file or directory", name)
			exit = 1
			continue
		}
		sc := newScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if fieldsSpec != "" {
				if !strings.Contains(line, delim) {
					if f.Has("only-delimited") {
						continue
					}
					if selected(ranges, 1) {
						fmt.Fprintln(w, line)
					}
					continue
				}
				parts := strings.Split(line, delim)
				var sel []string
				for i, p := range parts {
					if selected(ranges, i+1) {
						sel = append(sel, p)
					}
				}
				fmt.Fprintln(w, strings.Join(sel, outDelim))
				continue
			}
			runes := []rune(line)
			var b strings.Builder
			for i, ru := range runes {
				if selected(ranges, i+1) {
					b.WriteRune(ru)
				}
			}
			fmt.Fprintln(w, b.String())
		}
		r.Close()
	}
	return exit
}

func selected(ranges []RangeSpec, idx int) bool {
	for _, r := range ranges {
		if r.contains(idx) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ tr -----

func cmdTr(args []string) int {
	f := NewFlags(
		&Flag{Name: "complement", Short: 'c', Long: "complement", Help: "use the complement of SET1"},
		&Flag{Name: "complement", Short: 'C', Help: "same as -c"},
		&Flag{Name: "delete", Short: 'd', Long: "delete", Help: "delete characters in SET1"},
		&Flag{Name: "squeeze-repeats", Short: 's', Long: "squeeze-repeats", Help: "replace each repeated character with a single occurrence"},
		&Flag{Name: "truncate-set1", Short: 't', Long: "truncate-set1", Help: "first truncate SET1 to the length of SET2"},
	)
	if !parseOrDie("tr", f, args) {
		return 2
	}
	if handleHelpVersion("tr", "[OPTION]... SET1 [SET2]", f) {
		return 0
	}
	op := f.Positional
	if len(op) == 0 {
		warn("tr", "missing operand")
		return 1
	}
	set1 := expandTrSet(op[0])
	var set2 []rune
	if len(op) > 1 {
		set2 = expandTrSet(op[1])
	}
	if f.Has("truncate-set1") && len(set2) < len(set1) {
		set1 = set1[:len(set2)]
	}
	if !f.Has("delete") && !f.Has("squeeze-repeats") && len(set2) == 0 {
		warn("tr", "missing operand after '%s'", op[0])
		return 1
	}
	if len(op) > 2 {
		warn("tr", "extra operand '%s'", op[2])
		return 1
	}
	inSet1 := map[rune]bool{}
	for _, r := range set1 {
		inSet1[r] = true
	}
	translate := map[rune]rune{}
	if !f.Has("delete") && len(set2) > 0 {
		for i, r := range set1 {
			if i < len(set2) {
				translate[r] = set2[i]
			} else {
				translate[r] = set2[len(set2)-1]
			}
		}
	}
	squeezeSet := inSet1
	if len(set2) > 0 && !f.Has("delete") {
		squeezeSet = map[rune]bool{}
		for _, r := range set2 {
			squeezeSet[r] = true
		}
	}
	br := bufio.NewReaderSize(os.Stdin, 64*1024)
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	comp := f.Has("complement")
	prev := rune(-1)
	hasPrev := false
	for {
		ru, _, err := br.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			warn("tr", "%v", err)
			return 1
		}
		member := inSet1[ru]
		if comp {
			member = !member
		}
		out := ru
		if f.Has("delete") {
			if member {
				continue
			}
		} else if comp && len(set2) > 0 {
			member = true
			out = set2[len(set2)-1]
		} else if t, ok := translate[ru]; ok {
			out = t
		} else {
			member = false
		}
		if f.Has("squeeze-repeats") && hasPrev && out == prev && squeezeSet[out] {
			continue
		}
		if _, err := bw.WriteRune(out); err != nil {
			warn("tr", "%v", err)
			return 1
		}
		prev, hasPrev = out, true
	}
	return 0
}
