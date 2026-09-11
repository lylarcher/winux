package core

// ls lists directory contents GNU-style. Windows-specific facts (hidden
// attribute, read-only attribute, access/creation times, NTFS file id) come
// from ls_windows.go; the non-Windows build degrades gracefully.

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func init() {
	addCmd("ls", "list directory contents", cmdLs)
}

const (
	lsDirColor  = "\x1b[1;34m" // bold blue, as GNU colours directories
	lsExeColor  = ansiGreen
	lsLinkColor = ansiCyan
)

type lsOpts struct {
	all, almostAll  bool
	dir, long       bool
	human, blocks   bool
	inode           bool
	sortKey         byte // 0 = name, 't' mtime, 'S' size, 'u' atime, 'c' creation
	showKey         byte // long-format time to display: 'm' mtime, 'u' atime, 'c' creation
	reverse         bool
	recursive       bool
	one, cols       bool
	across, comma   bool
	classify, slash bool
	quote           bool
	noGroup, noOwner, numeric bool
	groupDirsFirst  bool
	timeStyle       string
	alwaysColor     bool
	neverColor      bool
	noSort          bool
	deref           bool
}

type lsEntry struct {
	name   string // display name (no indicator yet)
	path   string // filesystem path for further stat/recursion
	fi     os.FileInfo
	hidden bool
	ind    byte // classify indicator: '/', '*', '@'
}

func cmdLs(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "do not ignore entries starting with . or with the hidden attribute"},
		&Flag{Name: "almost-all", Short: 'A', Long: "almost-all", Help: "like -a, but do not list . and .."},
		&Flag{Name: "directory", Short: 'd', Long: "directory", Help: "list directories themselves, not their contents"},
		&Flag{Name: "long", Short: 'l', Long: "long", Help: "use a long listing format"},
		&Flag{Name: "human", Short: 'h', Long: "human-readable", Help: "with -l and -s, print sizes like 1K 234M 2G"},
		&Flag{Name: "blocks", Short: 's', Help: "print the allocated size of each file, in 512-byte blocks"},
		&Flag{Name: "inode", Short: 'i', Long: "inode", Help: "print the index number (NTFS file id) of each file"},
		&Flag{Name: "sort-time", Short: 't', Help: "sort by modification time, newest first"},
		&Flag{Name: "sort-size", Short: 'S', Help: "sort by file size, largest first"},
		&Flag{Name: "atime", Short: 'u', Help: "with -l: show the access time; with -lt: sort by it"},
		&Flag{Name: "ctime", Short: 'c', Help: "with -l: show the creation time; with -lt: sort by it (Windows analog of ctime)"},
		&Flag{Name: "reverse", Short: 'r', Long: "reverse", Help: "reverse order while sorting"},
		&Flag{Name: "recursive", Short: 'R', Long: "recursive", Help: "list subdirectories recursively"},
		&Flag{Name: "one", Short: '1', Help: "list one file per line"},
		&Flag{Name: "columns", Short: 'C', Long: "columns", Help: "list entries by columns"},
		&Flag{Name: "across", Short: 'x', Long: "across", Help: "list entries by lines instead of by columns"},
		&Flag{Name: "comma", Short: 'm', Long: "comma", Help: "fill width with a comma separated list of entries"},
		&Flag{Name: "classify", Short: 'F', Long: "classify", Help: "append indicator (one of */) to entries"},
		&Flag{Name: "slash", Short: 'p', Long: "indicator-style", OptArg: true, ArgName: "[=STYLE]", Help: "append / indicator to directories; STYLE: none, slash, file-type, classify"},
		&Flag{Name: "quote", Short: 'Q', Long: "quote-name", Help: "enclose entry names in double quotes"},
		&Flag{Name: "no-group", Short: 'G', Long: "no-group", Help: "in a long listing, don't print group names"},
		&Flag{Name: "no-group", Short: 'o', Help: "like -l, but do not list group information"},
		&Flag{Name: "no-owner", Short: 'g', Help: "like -l, but do not list owner information"},
		&Flag{Name: "numeric", Short: 'n', Long: "numeric-uid-gid", Help: "like -l, but list numeric uid and gid"},
		&Flag{Name: "group-dirs-first", Long: "group-directories-first", Help: "group directories before files"},
		&Flag{Name: "time-style", Long: "time-style", HasArg: true, ArgName: "STYLE", Help: "time/date format: full-iso, long-iso, iso, locale, +FORMAT"},
		&Flag{Name: "color", Long: "color", OptArg: true, ArgName: "[=WHEN]", Help: "colorize output: WHEN is always, never or auto"},
		&Flag{Name: "no-sort", Short: 'U', Help: "do not sort; list entries in directory order"},
		&Flag{Name: "force", Short: 'f', Help: "same as -a -U (no sort, list all)"},
		&Flag{Name: "dereference", Short: 'L', Long: "dereference", Help: "follow symbolic links"},
	)
	if !parseOrDie("ls", f, args) {
		return 2
	}
	if handleHelpVersion("ls", "[OPTION]... [FILE]...", f) {
		return 0
	}
	o := lsOpts{
		all:             f.Has("all"),
		almostAll:       f.Has("almost-all"),
		dir:             f.Has("directory"),
		long:            f.Has("long"),
		human:           f.Has("human"),
		blocks:          f.Has("blocks"),
		inode:           f.Has("inode"),
		reverse:         f.Has("reverse"),
		recursive:       f.Has("recursive"),
		one:             f.Has("one"),
		cols:            f.Has("columns"),
		across:          f.Has("across"),
		comma:           f.Has("comma"),
		quote:           f.Has("quote"),
		noGroup:         f.Has("no-group"),
		noOwner:         f.Has("no-owner"),
		numeric:         f.Has("numeric"),
		groupDirsFirst:  f.Has("group-dirs-first"),
		timeStyle:       f.Str("time-style"),
		neverColor:      strings.EqualFold(f.Str("color"), "never"),
		alwaysColor:     strings.EqualFold(f.Str("color"), "always"),
		noSort:          f.Has("no-sort"),
		deref:           f.Has("dereference"),
	}
	// sort key (GNU precedence): -S always sorts by size; with -t the time base
	// is chosen by -u/-c; -u/-c without -t sort by that time unless -l is given
	switch {
	case f.Has("sort-size"):
		o.sortKey = 'S'
	case f.Has("ctime") && (f.Has("sort-time") || !o.long):
		o.sortKey = 'c'
	case f.Has("atime") && (f.Has("sort-time") || !o.long):
		o.sortKey = 'u'
	case f.Has("sort-time"):
		o.sortKey = 't'
	}
	if o.long {
		switch {
		case f.Has("ctime"):
			o.showKey = 'c'
		case f.Has("atime"):
			o.showKey = 'u'
		default:
			o.showKey = 'm'
		}
	}
	if f.Has("force") {
		o.all, o.noSort = true, true
	}
	// -F implies the slash indicator plus the executable star
	if f.Has("classify") {
		o.classify, o.slash = true, true
	}
	if f.Has("slash") {
		o.slash = true
	}
	if st := f.Str("slash"); st != "" { // --indicator-style=STYLE
		switch st {
		case "none":
			o.slash, o.classify = false, false
		case "slash":
			o.slash = true
		case "file-type":
			o.slash, o.classify = true, false
		case "classify":
			o.slash, o.classify = true, true
		default:
			warn("ls", "unrecognized indicator style '%s' (use none, slash, file-type or classify)", st)
			return 2
		}
	}

	ops := expandGlobs(f.Positional)
	if len(ops) == 0 {
		ops = []string{"."}
	}
	var fileItems, dirItems []lsEntry
	var hadErr bool
	for _, op := range ops {
		var fi os.FileInfo
		var err error
		if o.deref {
			fi, err = os.Stat(op)
		} else {
			fi, err = os.Lstat(op)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ls: cannot access '%s': %v\n", op, err)
			hadErr = true
			continue
		}
		e := lsEntry{name: op, path: op, fi: fi}
		if !fi.IsDir() || o.dir {
			fileItems = append(fileItems, e)
			continue
		}
		dirItems = append(dirItems, e)
	}
	code := 0
	if hadErr {
		code = 2
	}
	if len(fileItems) > 0 {
		printEntries(fileItems, o)
	}
	for _, d := range dirItems {
		if len(dirItems) > 1 || len(fileItems) > 0 {
			fmt.Printf("\n%s:\n", d.name)
		}
		listDirTree(d.path, d.name, o, true)
	}
	return code
}

// listDirTree lists one directory and, with -R, its subdirectories.
// topLevel controls whether "." and ".." may appear (-a only, never with -R).
func listDirTree(dirPath, dirText string, o lsOpts, topLevel bool) {
	items := readDirItems(dirPath, o, topLevel)
	sortEntries(items, o)
	printEntries(items, o)
	if !o.recursive {
		return
	}
	for _, it := range items {
		if !it.fi.IsDir() || it.name == "." || it.name == ".." {
			continue
		}
		fmt.Printf("\n%s:\n", filepath.Join(dirText, it.name))
		listDirTree(it.path, filepath.Join(dirText, it.name), o, false)
	}
}

// readDirItems loads the children of dirPath (directory order when -U/-f).
func readDirItems(dirPath string, o lsOpts, topLevel bool) []lsEntry {
	ep := os.Lstat  // stat function used for children
	if o.deref {
		ep = os.Stat
	}
	dh, err := os.Open(dirPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ls: cannot open directory '%s': %v\n", dirPath, err)
		return nil
	}
	defer dh.Close()
	var names []string
	if o.noSort {
		names, err = dh.Readdirnames(-1)
	} else {
		var fis []os.FileInfo
		fis, err = dh.Readdir(-1)
		for _, fi := range fis {
			names = append(names, fi.Name())
		}
	}
	if err != nil && err.Error() != "EOF" {
		fmt.Fprintf(os.Stderr, "ls: reading directory '%s': %v\n", dirPath, err)
	}
	var out []lsEntry
	if o.all && topLevel && !o.dir {
		if fi, err := ep(dirPath); err == nil {
			out = append(out, lsEntry{name: ".", path: dirPath, fi: fi})
			out = append(out, lsEntry{name: "..", path: filepath.Dir(dirPath), fi: fi})
		}
	}
	for _, n := range names {
		p := filepath.Join(dirPath, n)
		fi, err := ep(p)
		if err != nil {
			// entry disappeared between readdir and stat; list it anyway with a stub
			fi = syntheticFileInfo(n)
		}
		hidden := isHiddenName(n) || winHidden(p)
		if hidden && !o.all && !o.almostAll {
			continue
		}
		if o.all && !o.almostAll && (n == "." || n == "..") {
			continue // those were added above (topLevel only); never in recursion
		}
		out = append(out, lsEntry{name: n, path: p, fi: fi, hidden: hidden})
	}
	return out
}

// syntheticFileInfo is used when an entry vanished between readdir and stat.
func syntheticFileInfo(name string) os.FileInfo { return fakeInfo{name: name} }

type fakeInfo struct{ name string }

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() interface{}   { return nil }

func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

func sortEntries(items []lsEntry, o lsOpts) {
	if o.noSort {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if o.groupDirsFirst && a.fi.IsDir() != b.fi.IsDir() {
			return a.fi.IsDir()
		}
		c := keyCmp(a, b, o)
		if c == 0 {
			// tie-break by folded name (GNU breaks ties by name)
			c = nameCmp(a.name, b.name)
		}
		if o.reverse {
			c = -c
		}
		return c < 0
	})
}

// keyCmp compares two entries by the active sort key (newest/largest first).
func keyCmp(a, b lsEntry, o lsOpts) int {
	switch o.sortKey {
	case 't':
		return cmpTime(a.fi.ModTime(), b.fi.ModTime())
	case 'u':
		at, _, _ := fileTimes(a.fi)
		bt, _, _ := fileTimes(b.fi)
		return cmpTime(at, bt)
	case 'c':
		_, ac, _ := fileTimes(a.fi)
		_, bc, _ := fileTimes(b.fi)
		return cmpTime(ac, bc)
	case 'S':
		switch {
		case a.fi.Size() > b.fi.Size():
			return -1
		case a.fi.Size() < b.fi.Size():
			return 1
		}
		return 0
	default:
		return nameCmp(a.name, b.name)
	}
}

func cmpTime(a, b time.Time) int {
	switch {
	case a.After(b):
		return -1
	case a.Before(b):
		return 1
	}
	return 0
}

func nameCmp(a, b string) int {
	fa, fb := strings.ToLower(a), strings.ToLower(b)
	switch {
	case fa < fb:
		return -1
	case fa > fb:
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// printEntries renders one listing (names, comma mode or long format).
func printEntries(items []lsEntry, o lsOpts) {
	if len(items) == 0 {
		return
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = displayName(it, o)
		if (o.blocks || o.inode) && !o.long {
			var pre []string
			if o.blocks {
				pre = append(pre, sizeStr(it, o))
			}
			if o.inode {
				pre = append(pre, strconv.FormatUint(fileID(it.path), 10))
			}
			names[i] = strings.Join(pre, " ") + " " + names[i]
		}
	}
	useColor := !o.neverColor && (o.alwaysColor || (!o.long && isTerminal(os.Stdout)))
	// colour only per-name in the short format; long format colours the name too
	if useColor && !o.long {
		for i, it := range items {
			if c := entryColor(it, o); c != "" {
				names[i] = colorName(names[i], c)
			}
		}
	}
	if o.long {
		printLong(items, names, o)
		return
	}
	if o.comma {
		printComma(names)
		return
	}
	_, termCols := terminalSize()
	if termCols <= 0 {
		termCols = 80
	}
	if o.one || !isTerminal(os.Stdout) {
		for _, n := range names {
			fmt.Println(n)
		}
		return
	}
	for _, line := range fmtColumns(names, termCols, o.across) {
		fmt.Println(line)
	}
}

// colorName applies the entry colour to the name part of a cell (numeric
// prefixes such as the inode stay uncoloured).
func colorName(cell, color string) string {
	idx := strings.LastIndex(cell, " ")
	if idx < 0 {
		return color + cell + ansiReset
	}
	return cell[:idx+1] + color + cell[idx+1:] + ansiReset
}

func entryColor(it lsEntry, o lsOpts) string {
	if it.fi.IsDir() {
		return lsDirColor
	}
	if isSymlinkMode(it.fi.Mode()) {
		return lsLinkColor
	}
	if exeLike(it.name) {
		return lsExeColor
	}
	return ""
}

func exeLike(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".cmd", ".bat", ".com", ".msi", ".ps1":
		return true
	}
	return false
}

func isSymlinkMode(m os.FileMode) bool { return m&os.ModeSymlink != 0 }

// displayName applies quoting and the classify indicator.
func displayName(it lsEntry, o lsOpts) string {
	name := it.name
	if o.quote {
		name = strconv.Quote(name)
	}
	ind := byte(0)
	switch {
	case it.fi.IsDir():
		if o.slash {
			ind = '/'
		}
	case isSymlinkMode(it.fi.Mode()):
		if o.classify {
			ind = '@'
		}
	case exeLike(it.name):
		if o.classify {
			ind = '*'
		}
	}
	if ind != 0 {
		name += string(ind)
	}
	return name
}

// fmtColumns lays out names into a grid: default fills columns top-to-bottom,
// across fills rows left-to-right. Returns the display lines without trailing
// spaces.
func fmtColumns(names []string, termWidth int, across bool) []string {
	if len(names) == 0 {
		return nil
	}
	maxw := 0
	for _, n := range names {
		if w := utf8.RuneCountInString(n); w > maxw {
			maxw = w
		}
	}
	cell := maxw + 2
	cols := termWidth / cell
	if cols < 1 {
		cols = 1
	}
	if cols > len(names) {
		cols = len(names)
	}
	rows := (len(names) + cols - 1) / cols
	lines := make([]string, 0, rows)
	for r := 0; r < rows; r++ {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			idx := r*cols + c
			if !across {
				idx = c*rows + r
			}
			if idx >= len(names) {
				if across {
					break // row-major order ends the row here
				}
				continue // column-major order may leave gaps in the last row
			}
			b.WriteString(names[idx])
			hasNext := false
			if across {
				hasNext = c+1 < cols && idx+1 < len(names)
			} else {
				hasNext = (c+1)*rows+r < len(names)
			}
			if hasNext {
				pad := cell - utf8.RuneCountInString(names[idx])
				if pad > 0 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

// printComma joins names with ", ", wrapping at the terminal width.
func printComma(names []string) {
	_, termCols := terminalSize()
	if termCols <= 0 {
		termCols = 80
	}
	var b strings.Builder
	col := 0
	for i, n := range names {
		sep := ""
		if i > 0 {
			sep = ", "
		}
		w := utf8.RuneCountInString(sep) + utf8.RuneCountInString(n)
		if i > 0 && col+w > termCols {
			fmt.Println(strings.TrimRight(b.String(), " "))
			b.Reset()
			col = 0
			sep = ""
		}
		b.WriteString(sep)
		b.WriteString(n)
		col += w
	}
	fmt.Println(strings.TrimRight(b.String(), " "))
}

// ------------------------------------------------------------------- long ----

func printLong(items []lsEntry, names []string, o lsOpts) {
	owner, group := "", ""
	if !o.noOwner {
		owner = shortUserName(currentUserName())
		if o.numeric {
			owner = "1000"
		}
	}
	if !o.noGroup {
		group = "None"
		if o.numeric {
			group = "1000"
		}
	}
	iW, sW, oW, gW, dW := 0, 0, 0, 0, 0
	for _, it := range items {
		if o.inode {
			if w := len(strconv.FormatUint(fileID(it.path), 10)); w > iW {
				iW = w
			}
		}
		if o.blocks {
			if w := utf8.RuneCountInString(sizeStr(it, o)); w > sW {
				sW = w
			}
		}
		if !o.noOwner {
			if w := utf8.RuneCountInString(owner); w > oW {
				oW = w
			}
		}
		if !o.noGroup {
			if w := utf8.RuneCountInString(group); w > gW {
				gW = w
			}
		}
		if w := utf8.RuneCountInString(lsTimeStr(it, o)); w > dW {
			dW = w
		}
	}
	// GNU prints a "total" line: the 512-byte blocks of the listed entries
	total := int64(0)
	for _, it := range items {
		total += (it.fi.Size() + 511) / 512
	}
	fmt.Printf("total %d\n", total)
	color := !o.neverColor && (o.alwaysColor || isTerminal(os.Stdout))
	for i, it := range items {
		var b strings.Builder
		if o.inode {
			b.WriteString(rightAlign(strconv.FormatUint(fileID(it.path), 10), iW))
			b.WriteByte(' ')
		}
		if o.blocks {
			b.WriteString(rightAlign(sizeStr(it, o), sW))
			b.WriteByte(' ')
		}
		b.WriteString(modeStr(it))
		b.WriteByte(' ')
		b.WriteString(rightAlign("1", 2))
		b.WriteByte(' ')
		if !o.noOwner {
			b.WriteString(pad(owner, oW))
			b.WriteByte(' ')
		}
		if !o.noGroup {
			b.WriteString(pad(group, gW))
			b.WriteByte(' ')
		}
		b.WriteString(rightAlign(sizeStr(it, o), sW))
		b.WriteByte(' ')
		b.WriteString(pad(lsTimeStr(it, o), dW))
		b.WriteByte(' ')
		name := names[i]
		if color {
			if c := entryColor(it, o); c != "" {
				name = c + name + ansiReset
			}
		}
		b.WriteString(name)
		fmt.Println(b.String())
	}
}

func pad(s string, w int) string {
	if d := w - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func rightAlign(s string, w int) string {
	if d := w - utf8.RuneCountInString(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// modeStr renders the 10-character permission column, mapping Windows facts
// honestly: directories are drwxr-xr-x, executables get x bits (as Git does),
// the read-only attribute clears the w bits, symlinks are lrwxrwxrwx.
func modeStr(it lsEntry) string {
	fi := it.fi
	ro := winReadonly(it.path)
	exe := exeLike(it.name)
	var b [10]byte
	switch {
	case fi.IsDir():
		b[0] = 'd'
	case isSymlinkMode(fi.Mode()):
		b[0] = 'l'
	default:
		b[0] = '-'
	}
	for i := 1; i < 10; i++ {
		switch i % 3 {
		case 1:
			b[i] = 'r'
		case 2:
			if i == 2 && !ro { // w bit only for the owner (644-style)
				b[i] = 'w'
			} else {
				b[i] = '-'
			}
		case 0:
			if fi.IsDir() || exe {
				b[i] = 'x'
			} else {
				b[i] = '-'
			}
		}
	}
	if b[0] == 'd' {
		// directories ignore the read-only attribute, as Windows does
		copy(b[1:], "rwxr-xr-x")
	}
	if b[0] == 'l' {
		copy(b[1:], "rwxrwxrwx")
	}
	return string(b[:])
}

func sizeStr(it lsEntry, o lsOpts) string {
	size := it.fi.Size()
	if o.blocks {
		blocks := (size + 511) / 512
		if o.human {
			return humanSize(blocks * 512)
		}
		return strconv.FormatInt(blocks, 10)
	}
	if o.human {
		return humanSize(size)
	}
	return strconv.FormatInt(size, 10)
}

func humanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10)
	}
	f := float64(n)
	for _, u := range []string{"K", "M", "G", "T", "P", "E"} {
		f /= 1024
		if f < 1024 {
			if f < 10 {
				return fmt.Sprintf("%.1f%s", f, u)
			}
			return fmt.Sprintf("%.0f%s", f, u)
		}
	}
	return fmt.Sprintf("%.0fE", f)
}

// lsTimeStr picks the displayed time: mtime, or -u/-c with -l show access/creation.
func lsTimeStr(it lsEntry, o lsOpts) string {
	t := it.fi.ModTime()
	switch o.showKey {
	case 'u':
		if at, _, ok := fileTimes(it.fi); ok {
			t = at
		}
	case 'c':
		if _, ct, ok := fileTimes(it.fi); ok {
			t = ct
		}
	}
	return formatLsTime(t, o.timeStyle)
}

// syntheticID is the cross-platform fallback for -i when the real file id
// cannot be read: a stable 64-bit hash of the absolute path.
func syntheticID(path string) uint64 {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	h := fnv.New64a()
	h.Write([]byte(abs))
	return h.Sum64()
}

func formatLsTime(t time.Time, style string) string {
	switch {
	case style == "full-iso":
		return t.Format("2006-01-02 15:04:05.000000000 -0700")
	case style == "long-iso":
		return t.Format("2006-01-02 15:04")
	case style == "iso":
		if recent := t.After(time.Now().AddDate(0, -6, 0)); recent {
			return t.Format("01-02 15:04")
		}
		return t.Format("01-02  2006")
	case style == "" || style == "locale":
		// GNU default: within six months, show the time; otherwise the year
		now := time.Now()
		if t.Before(now.AddDate(0, -6, 0)) || t.After(now.Add(time.Hour)) {
			return fmt.Sprintf("%3s %2d  %4d", t.Month().String()[:3], t.Day(), t.Year())
		}
		return fmt.Sprintf("%3s %2d %02d:%02d", t.Month().String()[:3], t.Day(), t.Hour(), t.Minute())
	case strings.HasPrefix(style, "+"):
		return strftime(t, style[1:])
	default:
		return t.Format("2006-01-02 15:04")
	}
}