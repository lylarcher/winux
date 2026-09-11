package core

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxLineSize = 16 * 1024 * 1024

// ANSI escape sequences: initConsole enables VT processing on the console.
const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiReverse = "\x1b[7m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

func warn(cmd, format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", cmd, fmt.Sprintf(format, a...))
}

// ---------------------------------------------------------------- paths ----

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// makeWritable clears the read-only attribute (Windows refuses to delete or
// overwrite read-only files, while on Linux this is governed by directories).
func makeWritable(p string) {
	st, err := os.Lstat(p)
	if err != nil {
		return
	}
	if st.Mode().Perm()&0o200 == 0 {
		os.Chmod(p, st.Mode().Perm()|0o200)
	}
}

// trimSep removes a trailing separator but keeps "C:\" and "\" intact.
func trimSep(p string) string {
	if len(p) <= 1 {
		return p
	}
	t := strings.TrimRight(p, `/\`)
	if t == "" {
		return string(filepath.Separator)
	}
	if len(t) == 2 && t[1] == ':' {
		return t + string(filepath.Separator)
	}
	return t
}

func samePath(a, b string) bool {
	ra, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	rb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(ra); err == nil {
		ra = r
	}
	if r, err := filepath.EvalSymlinks(rb); err == nil {
		rb = r
	}
	return strings.EqualFold(filepath.Clean(ra), filepath.Clean(rb))
}

// isInside reports whether target is dir itself or lives below dir.
func isInside(dir, target string) bool {
	rd, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rt, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rd, rt = filepath.Clean(rd), filepath.Clean(rt)
	if strings.EqualFold(rd, rt) {
		return true
	}
	rel, err := filepath.Rel(rd, rt)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ---------------------------------------------------- shell-ish expansion ---

// expandGlobs performs the wildcard expansion the POSIX shell would have done
// before invoking a command: neither cmd.exe nor PowerShell expands wildcards
// for native executables, so `cp *.txt out\` has to work by itself.
func expandGlobs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = expandTilde(a)
		if a == "-" || !strings.ContainsAny(a, "*?[") {
			out = append(out, a)
			continue
		}
		matches, err := filepath.Glob(a)
		if err != nil || len(matches) == 0 {
			out = append(out, a)
			continue
		}
		out = append(out, matches...)
	}
	return out
}

func expandTilde(s string) string {
	if s != "~" && !strings.HasPrefix(s, "~/") && !strings.HasPrefix(s, `~\`) {
		return s
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	if s == "~" {
		return home
	}
	return filepath.Join(home, s[2:])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// preScanShortNumber rewrites the classic `head -5` / `tail -20` form into
// `-n 5`, which the ordinary parser then handles. Arguments that are the value
// of a preceding option (e.g. the "-2" in `head -n -2`) are left untouched.
func preScanShortNumber(args []string, valueOpts ...string) []string {
	out := make([]string, 0, len(args))
	expectValue := false
	for _, a := range args {
		if expectValue {
			expectValue = false
			out = append(out, a)
			continue
		}
		if len(a) > 1 && a[0] == '-' && isAllDigits(a[1:]) {
			out = append(out, "-n", a[1:])
			continue
		}
		for _, v := range valueOpts {
			if a == v {
				expectValue = true
				break
			}
		}
		out = append(out, a)
	}
	return out
}

// isOptionLike tells whether an argument should be treated as an option.
func isOptionLike(a string) bool {
	return len(a) > 1 && a[0] == '-'
}

// ---------------------------------------------------------- interaction ----

func askYesNo(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	line = strings.TrimSpace(line)
	return line != "" && (line[0] == 'y' || line[0] == 'Y')
}

// ----------------------------------------------------------------- I/O -----

func openInput(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	return sc
}

func readAllLines(path string) ([]string, error) {
	r, err := openInput(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readLinesFrom(r)
}

func readLinesFrom(r io.Reader) ([]string, error) {
	var lines []string
	sc := newScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}

// showNonPrinting renders control characters using the ^X notation used by
// `cat -v`.
func showNonPrinting(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteRune(r)
		case r < 32:
			b.WriteByte('^')
			b.WriteRune(r + 64)
		case r == 127:
			b.WriteString("^?")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ------------------------------------------------------------ wildcards ----

// wildcardMatch implements shell-style glob matching (*, ?, [a-z], [!x]) over
// runes, used for grep --include/--exclude and friends.
func wildcardMatch(pattern, name string) bool {
	p := []rune(strings.ReplaceAll(pattern, `\`, "/"))
	n := []rune(strings.ReplaceAll(name, `\`, "/"))
	return wildMatch(p, n)
}

func wildMatch(p, n []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 1 && p[1] == '*' {
				p = p[1:]
			}
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(n); i++ {
				if wildMatch(p[1:], n[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(n) == 0 {
				return false
			}
			p, n = p[1:], n[1:]
		case '[':
			rest, matched, ok := matchBracket(p, n)
			if !ok {
				if len(n) == 0 || n[0] != '[' {
					return false
				}
				p, n = p[1:], n[1:]
				continue
			}
			if len(n) == 0 || !matched {
				return false
			}
			p, n = rest, n[1:]
		default:
			if len(n) == 0 || n[0] != p[0] {
				return false
			}
			p, n = p[1:], n[1:]
		}
	}
	return len(n) == 0
}

func matchBracket(p, n []rune) (rest []rune, matched, ok bool) {
	i := 1
	negate := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negate = true
		i++
	}
	var set []rune
	first := true
	for i < len(p) {
		if p[i] == ']' && !first {
			break
		}
		first = false
		if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
			for c := p[i]; c <= p[i+2]; c++ {
				set = append(set, c)
			}
			i += 3
			continue
		}
		set = append(set, p[i])
		i++
	}
	if i >= len(p) {
		return nil, false, false
	}
	rest = p[i+1:]
	if len(n) == 0 {
		return rest, false, true
	}
	m := false
	for _, c := range set {
		if c == n[0] {
			m = true
			break
		}
	}
	if negate {
		m = !m
	}
	return rest, m, true
}

// ---------------------------------------------------------------- dates ----

var dateLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
	"01/02/2006 15:04:05",
	"01/02/2006",
	"15:04:05",
	"15:04",
}

// parseDate understands the common date/time spellings accepted by
// `touch -d` and `date -d`.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	now := time.Now()
	switch strings.ToLower(s) {
	case "now":
		return now, nil
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local), nil
	case "yesterday":
		y := now.AddDate(0, 0, -1)
		return time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, time.Local), nil
	case "tomorrow":
		t := now.AddDate(0, 0, 1)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), nil
	}
	if s != "" && (isAllDigits(s) || (s[0] == '-' && isAllDigits(s[1:]))) {
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(sec, 0), nil
		}
	}
	for _, layout := range dateLayouts {
		t, err := time.ParseInLocation(layout, s, time.Local)
		if err != nil {
			continue
		}
		if t.Year() == 0 { // time-only layouts
			t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date '%s'", s)
}

// parseTouchStamp implements the `touch -t [[CC]YY]MMDDhhmm[.ss]` format.
func parseTouchStamp(s string, now time.Time) (time.Time, error) {
	sec := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		frag := s[i+1:]
		s = s[:i]
		if len(frag) != 2 || !isAllDigits(frag) {
			return time.Time{}, fmt.Errorf("invalid date '%s'", s)
		}
		sec, _ = strconv.Atoi(frag)
	}
	if !isAllDigits(s) {
		return time.Time{}, fmt.Errorf("invalid date '%s'", s)
	}
	var year int
	var rest string
	switch len(s) {
	case 8:
		year = now.Year()
		rest = s
	case 10:
		y, _ := strconv.Atoi(s[:2])
		if y < 69 {
			year = 2000 + y
		} else {
			year = 1900 + y
		}
		rest = s[2:]
	case 12:
		year, _ = strconv.Atoi(s[:4])
		rest = s[4:]
	default:
		return time.Time{}, fmt.Errorf("invalid date '%s'", s)
	}
	mon, _ := strconv.Atoi(rest[0:2])
	day, _ := strconv.Atoi(rest[2:4])
	hour, _ := strconv.Atoi(rest[4:6])
	min, _ := strconv.Atoi(rest[6:8])
	if mon < 1 || mon > 12 || day < 1 || day > 31 || hour > 23 || min > 59 || sec > 59 {
		return time.Time{}, fmt.Errorf("invalid date '%s'", s)
	}
	return time.Date(year, time.Month(mon), day, hour, min, sec, 0, time.Local), nil
}

// strftime formats t with the POSIX conversion specifiers users type in
// `date +FORMAT`.
func strftime(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(format) {
			b.WriteByte('%')
			break
		}
		switch format[i] {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&b, "%2d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 'b', 'h':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'w':
			fmt.Fprintf(&b, "%d", int(t.Weekday()))
		case 'u':
			w := int(t.Weekday())
			if w == 0 {
				w = 7
			}
			fmt.Fprintf(&b, "%d", w)
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 's':
			fmt.Fprintf(&b, "%d", t.Unix())
		case 'F':
			b.WriteString(t.Format("2006-01-02"))
		case 'T':
			b.WriteString(t.Format("15:04:05"))
		case 'R':
			b.WriteString(t.Format("15:04"))
		case 'D':
			b.WriteString(t.Format("01/02/06"))
		case 'r':
			b.WriteString(t.Format("03:04:05 PM"))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// ------------------------------------------------------------ field specs ---

type RangeSpec struct{ start, end int } // 1-based; end == 0 means "to the end"

func (r RangeSpec) contains(i int) bool {
	return i >= r.start && (r.end == 0 || i <= r.end)
}

// parseListSpec parses the field/character lists used by `cut -f` / `cut -c`:
// "1,3", "2-5", "3-", "-4".
func parseListSpec(s string) ([]RangeSpec, error) {
	var out []RangeSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '-'); i >= 0 {
			lo, hi := part[:i], part[i+1:]
			r := RangeSpec{}
			if lo == "" {
				r.start = 1
			} else {
				n, err := strconv.Atoi(lo)
				if err != nil || n < 1 {
					return nil, fmt.Errorf("invalid field value '%s'", part)
				}
				r.start = n
			}
			if hi == "" {
				r.end = 0
			} else {
				n, err := strconv.Atoi(hi)
				if err != nil || n < 1 {
					return nil, fmt.Errorf("invalid field value '%s'", part)
				}
				r.end = n
			}
			if r.end != 0 && r.end < r.start {
				return nil, fmt.Errorf("invalid decreasing range '%s'", part)
			}
			out = append(out, r)
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid field value '%s'", part)
		}
		out = append(out, RangeSpec{n, n})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("invalid field list")
	}
	return out, nil
}

// expandTrSet expands the SET syntax used by `tr`: ranges (a-z) plus the usual
// backslash escapes.
func expandTrSet(s string) []rune {
	var out []rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			i++
			switch rs[i] {
			case 'n':
				out = append(out, '\n')
			case 't':
				out = append(out, '\t')
			case 'r':
				out = append(out, '\r')
			case 'a':
				out = append(out, 7)
			case 'b':
				out = append(out, 8)
			case 'f':
				out = append(out, 12)
			case 'v':
				out = append(out, 11)
			case '0':
				out = append(out, 0)
			case '\\':
				out = append(out, '\\')
			default:
				out = append(out, rs[i])
			}
			continue
		}
		if i+2 < len(rs) && rs[i+1] == '-' {
			for c := rs[i]; c <= rs[i+2]; c++ {
				out = append(out, c)
			}
			i += 2
			continue
		}
		out = append(out, rs[i])
	}
	return out
}

// applyUnixMode maps the POSIX permission bits a user types (mkdir -m 755) onto
// the only thing NTFS really exposes: the read-only attribute.
func applyUnixMode(path, mode string) error {
	m, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(mode, "0o"), "0O"), 8, 32)
	if err != nil {
		return fmt.Errorf("invalid mode '%s'", mode)
	}
	if m&0o200 != 0 {
		return os.Chmod(path, 0o666)
	}
	return os.Chmod(path, 0o444)
}
