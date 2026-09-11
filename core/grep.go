package core

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func init() {
	addCmd("grep", "print lines matching a pattern (supports -r, -E, -o, context)", cmdGrep)
}

type grepOpts struct {
	Re         *regexp.Regexp
	invert     bool
	count      bool
	withMatch  bool // -l
	without    bool // -L
	lineNumber bool
	onlyMatch  bool // -o
	quiet      bool
	noMessages bool // -s
	text       bool // -a: treat binary files as text
	maxCount   int
	before     int
	after      int
	color      bool
	recursive  bool
	include    []string
	exclude    []string
	excludeDir []string
	printLines bool
}

func cmdGrep(args []string) int {
	f := NewFlags(
		&Flag{Name: "extended-regexp", Short: 'E', Long: "extended-regexp", Help: "PATTERNS are extended regular expressions (default)"},
		&Flag{Name: "fixed-strings", Short: 'F', Long: "fixed-strings", Help: "PATTERNS are strings, not regular expressions"},
		&Flag{Name: "basic-regexp", Short: 'G', Long: "basic-regexp", Help: "PATTERNS are basic regular expressions (accepted, treated as -E)"},
		&Flag{Name: "ignore-case", Short: 'i', Long: "ignore-case", Help: "ignore case distinctions"},
		&Flag{Name: "invert-match", Short: 'v', Long: "invert-match", Help: "select non-matching lines"},
		&Flag{Name: "word-regexp", Short: 'w', Long: "word-regexp", Help: "match only whole words"},
		&Flag{Name: "line-regexp", Short: 'x', Long: "line-regexp", Help: "match only whole lines"},
		&Flag{Name: "count", Short: 'c', Long: "count", Help: "print only a count of matching lines per file"},
		&Flag{Name: "files-with-matches", Short: 'l', Long: "files-with-matches", Help: "print only names of files with matches"},
		&Flag{Name: "files-without-match", Short: 'L', Long: "files-without-match", Help: "print only names of files without matches"},
		&Flag{Name: "line-number", Short: 'n', Long: "line-number", Help: "print line number with output lines"},
		&Flag{Name: "with-filename", Short: 'H', Long: "with-filename", Help: "print the file name for each match"},
		&Flag{Name: "no-filename", Short: 'h', Long: "no-filename", Help: "suppress the file name prefix"},
		&Flag{Name: "only-matching", Short: 'o', Long: "only-matching", Help: "print only the matched parts of a line"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "suppress all normal output; exit on first match"},
		&Flag{Name: "no-messages", Short: 's', Long: "no-messages", Help: "suppress error messages"},
		&Flag{Name: "text", Short: 'a', Long: "text", Help: "process a binary file as if it were text (useful for UTF-16 files)"},
		&Flag{Name: "recursive", Short: 'r', Long: "recursive", Help: "read all files under each directory"},
		&Flag{Name: "recursive", Short: 'R', Long: "dereference-recursive", Help: "same as -r"},
		&Flag{Name: "max-count", Short: 'm', Long: "max-count", HasArg: true, ArgName: "NUM", Help: "stop after NUM matching lines per file"},
		&Flag{Name: "after-context", Short: 'A', Long: "after-context", HasArg: true, ArgName: "NUM", Help: "print NUM lines of trailing context"},
		&Flag{Name: "before-context", Short: 'B', Long: "before-context", HasArg: true, ArgName: "NUM", Help: "print NUM lines of leading context"},
		&Flag{Name: "context", Short: 'C', Long: "context", HasArg: true, ArgName: "NUM", Help: "print NUM lines of output context"},
		&Flag{Name: "regexp", Short: 'e', Long: "regexp", HasArg: true, ArgName: "PATTERN", Help: "use PATTERN for matching (repeatable)"},
		&Flag{Name: "file", Short: 'f', Long: "file", HasArg: true, ArgName: "FILE", Help: "take patterns from FILE, one per line"},
		&Flag{Name: "include", Long: "include", HasArg: true, ArgName: "GLOB", Help: "search only files matching GLOB (repeatable)"},
		&Flag{Name: "exclude", Long: "exclude", HasArg: true, ArgName: "GLOB", Help: "skip files matching GLOB (repeatable)"},
		&Flag{Name: "exclude-dir", Long: "exclude-dir", HasArg: true, ArgName: "GLOB", Help: "skip directories matching GLOB (repeatable)"},
		&Flag{Name: "color", Long: "color", OptArg: true, ArgName: "[=WHEN]", Help: "highlight matches: WHEN is always, never or auto"},
	)
	if !parseOrDie("grep", f, args) {
		return 2
	}
	if handleHelpVersion("grep", "[OPTION]... PATTERNS [FILE]...", f) {
		return 0
	}

	var patterns []string
	patterns = append(patterns, f.Strs("regexp")...)
	for _, pf := range f.Strs("file") {
		ls, err := readAllLines(pf)
		if err != nil {
			warn("grep", "%s: No such file or directory", pf)
			return 2
		}
		patterns = append(patterns, ls...)
	}
	op := f.Positional
	if len(patterns) == 0 {
		if len(op) == 0 {
			patterns = []string{""}
		} else {
			patterns = []string{op[0]}
			op = op[1:]
		}
	}

	o := &grepOpts{
		invert:     f.Has("invert-match"),
		count:      f.Has("count"),
		withMatch:  f.Has("files-with-matches"),
		without:    f.Has("files-without-match"),
		lineNumber: f.Has("line-number"),
		onlyMatch:  f.Has("only-matching"),
		quiet:      f.Has("quiet"),
		noMessages: f.Has("no-messages"),
		text:       f.Has("text"),
		recursive:  f.Has("recursive"),
		include:    f.Strs("include"),
		exclude:    f.Strs("exclude"),
		excludeDir: f.Strs("exclude-dir"),
	}
	o.maxCount, _ = f.Int("max-count", 0)
	o.before, _ = f.Int("before-context", 0)
	o.after, _ = f.Int("after-context", 0)
	if c, err := f.Int("context", 0); err == nil && c > 0 {
		if o.before == 0 {
			o.before = c
		}
		if o.after == 0 {
			o.after = c
		}
	}
	switch when := strings.ToLower(f.Str("color")); {
	case when == "always" || when == "yes" || when == "force":
		o.color = true
	case when == "never" || when == "no" || when == "none":
		o.color = false
	default:
		o.color = f.Has("color") && isTerminal(os.Stdout)
	}
	o.printLines = !o.count && !o.withMatch && !o.without && !o.quiet

	re, err := compilePatterns(patterns, f.Has("fixed-strings"), f.Has("ignore-case"), f.Has("word-regexp"), f.Has("line-regexp"))
	if err != nil {
		warn("grep", "%v", err)
		return 2
	}
	o.Re = re

	// figure out the file operands
	var targets []string
	if len(op) == 0 {
		if o.recursive {
			targets = []string{"."}
		} else {
			targets = nil // read standard input
		}
	} else {
		targets = expandGlobs(op)
	}
	showFilename := f.Has("with-filename")
	if !showFilename && !f.Has("no-filename") {
		showFilename = len(targets) > 1
		if o.recursive {
			showFilename = true
		}
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	anyMatch := false
	anyError := false
	stop := false // set by -q as soon as a match is found
	report := func(path string, reader io.Reader, name string) {
		matched, count, err := grepStream(reader, o, showFilename, out, name)
		if err != nil {
			if !o.noMessages {
				warn("grep", "%s: %v", path, err)
			}
			anyError = true
		}
		if matched {
			anyMatch = true
		}
		switch {
		case o.withMatch:
			if matched {
				fmt.Fprintln(out, name)
			}
		case o.without:
			if !matched {
				fmt.Fprintln(out, name)
			}
		case o.count:
			if showFilename {
				fmt.Fprintf(out, "%s:%d\n", name, count)
			} else {
				fmt.Fprintf(out, "%d\n", count)
			}
		}
		if o.quiet && matched {
			stop = true
		}
	}

	if targets == nil {
		report("(standard input)", os.Stdin, "(standard input)")
	} else {
		for _, t := range targets {
			if stop {
				break
			}
			switch {
			case isDir(t):
				if !o.recursive {
					if !o.noMessages {
						warn("grep", "%s: Is a directory", t)
					}
					anyError = true
					continue
				}
				err := filepath.WalkDir(t, func(p string, d fs.DirEntry, err error) error {
					if stop {
						return fs.SkipAll
					}
					if err != nil {
						if !o.noMessages {
							warn("grep", "%s: %v", p, err)
						}
						anyError = true
						return nil
					}
					if d.IsDir() {
						if p != t && globListMatch(o.excludeDir, filepath.Base(p)) {
							return fs.SkipDir
						}
						return nil
					}
					if !grepFileSelected(p, o) {
						return nil
					}
					fh, err := os.Open(p)
					if err != nil {
						if !o.noMessages {
							warn("grep", "%s: %v", p, err)
						}
						anyError = true
						return nil
					}
					report(p, fh, p)
					fh.Close()
					return nil
				})
				if err != nil {
					anyError = true
				}
			default:
				fh, err := os.Open(t)
				if err != nil {
					if !o.noMessages {
						warn("grep", "%s: No such file or directory", t)
					}
					anyError = true
					continue
				}
				report(t, fh, t)
				fh.Close()
			}
		}
	}
	out.Flush()

	if o.quiet {
		switch {
		case anyMatch:
			return 0
		case anyError:
			return 2
		default:
			return 1
		}
	}
	if anyError {
		return 2
	}
	if anyMatch {
		return 0
	}
	return 1
}

// compilePatterns builds a single RE2 expression from the pattern list, the
// way GNU grep combines -e patterns.
func compilePatterns(patterns []string, fixed, ignoreCase, word, wholeLine bool) (*regexp.Regexp, error) {
	if len(patterns) == 0 {
		patterns = []string{""}
	}
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if fixed {
			p = regexp.QuoteMeta(p)
		}
		if word {
			p = `\b(?:` + p + `)\b`
		}
		if wholeLine {
			p = `^(?:` + p + `)$`
		}
		parts = append(parts, "(?:"+p+")")
	}
	src := strings.Join(parts, "|")
	if ignoreCase {
		src = "(?i)" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression: %v", err)
	}
	return re, nil
}

func globListMatch(patterns []string, path string) bool {
	if len(patterns) == 0 {
		return false
	}
	base := filepath.Base(path)
	for _, pat := range patterns {
		if strings.ContainsAny(pat, `/\`) {
			if wildcardMatch(pat, path) {
				return true
			}
			continue
		}
		if wildcardMatch(pat, base) {
			return true
		}
	}
	return false
}

func grepFileSelected(path string, o *grepOpts) bool {
	if len(o.include) > 0 && !globListMatch(o.include, path) {
		return false
	}
	if globListMatch(o.exclude, path) {
		return false
	}
	return true
}

type gline struct {
	num  int
	text string
}

// grepStream searches one input, printing selected lines in GNU grep's format.
func grepStream(r io.Reader, o *grepOpts, showFilename bool, out *bufio.Writer, name string) (bool, int, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	binary := false
	if !o.text {
		if head, _ := br.Peek(8000); len(head) > 0 {
			if bytes.IndexByte(head, 0) >= 0 {
				binary = true
			}
		}
	}
	var tail []gline
	lineNo := 0
	lastPrinted := -1
	printedAny := false
	afterLeft := 0
	matches := 0
	context := o.before > 0 || o.after > 0

	emit := func(l gline, isMatch bool) {
		if !o.printLines || binary {
			return
		}
		printedAny = true
		lastPrinted = l.num
		sep := ":"
		if context && !isMatch {
			sep = "-"
		}
		if showFilename {
			out.WriteString(colorText(name, ansiMagenta, o.color))
			out.WriteString(sep)
		}
		if o.lineNumber {
			out.WriteString(colorText(strconv.Itoa(l.num), ansiGreen, o.color))
			out.WriteString(sep)
		}
		if isMatch && o.color {
			out.WriteString(highlight(l.text, o.Re))
		} else {
			out.WriteString(l.text)
		}
		out.WriteByte('\n')
	}

	sc := newScanner(br)
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		idx := o.Re.FindAllStringIndex(line, -1)
		selected := len(idx) > 0
		if o.invert {
			selected = !selected
		}
		if selected {
			matches++
			if o.onlyMatch && !o.invert {
				if o.printLines && !binary {
					for _, m := range idx {
						if showFilename {
							out.WriteString(colorText(name, ansiMagenta, o.color))
							out.WriteByte(':')
						}
						if o.lineNumber {
							out.WriteString(colorText(strconv.Itoa(lineNo), ansiGreen, o.color))
							out.WriteByte(':')
						}
						text := line[m[0]:m[1]]
						if o.color {
							out.WriteString(ansiRed + ansiBold + text + ansiReset)
						} else {
							out.WriteString(text)
						}
						out.WriteByte('\n')
					}
					printedAny = true
					lastPrinted = lineNo
				}
			} else {
				first := lineNo
				for _, cl := range tail {
					if cl.num > lastPrinted {
						first = cl.num
						break
					}
				}
				if context && printedAny && first > lastPrinted+1 {
					fmt.Fprintln(out, "--")
				}
				for _, cl := range tail {
					if cl.num > lastPrinted {
						emit(cl, false)
					}
				}
				emit(gline{lineNo, line}, true)
				afterLeft = o.after
			}
			if o.maxCount > 0 && matches >= o.maxCount {
				break
			}
			continue
		}
		if afterLeft > 0 {
			emit(gline{lineNo, line}, false)
			afterLeft--
			continue
		}
		if o.before > 0 {
			tail = append(tail, gline{lineNo, line})
			if len(tail) > o.before {
				tail = tail[1:]
			}
		}
	}
	err := sc.Err()
	if binary && matches > 0 && o.printLines {
		fmt.Fprintf(out, "Binary file %s matches\n", name)
	}
	return matches > 0, matches, err
}

func colorText(s, code string, enabled bool) string {
	if !enabled {
		return s
	}
	return code + s + ansiReset
}

// highlight wraps every match in the line with the grep colour.
func highlight(line string, re *regexp.Regexp) string {
	idx := re.FindAllStringIndex(line, -1)
	if len(idx) == 0 {
		return line
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		if m[1] <= last {
			continue
		}
		b.WriteString(line[last:m[0]])
		b.WriteString(ansiRed + ansiBold)
		b.WriteString(line[m[0]:m[1]])
		b.WriteString(ansiReset)
		last = m[1]
	}
	b.WriteString(line[last:])
	return b.String()
}
