package core

// du estimates file and directory disk usage (GNU-style). The printed size
// is, by default, the on-disk allocation (GetFileInformationByHandleEx on
// Windows, os.FileInfo.Size() elsewhere), rounded up to the nearest block
// unit (default 1024). --apparent-size switches to logical sizes.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	addCmd("du", "estimate file space usage", cmdDu)
}

type duOpts struct {
	all, summarize    bool
	human             bool
	unit              int64 // bytes per block (default 1024)
	apparent          bool
	total             bool
	maxDepth          int // -1 = unlimited
	oneFileSystem     bool
	countLinks        bool
	derefAll, derefArgs bool
	nullOut           bool
	separateDirs      bool
	threshold         int64 // -1 = none
	excludes          []string
}

type duWalk struct {
	o         *duOpts
	seenFiles map[string]bool // vol+fileID for hardlink dedupe
	seenDirs  map[string]bool // for -L cycle protection
	volRoot   string          // root volume of current operand when -x
	operand   string          // display text of the current operand
	total     int64
	hadErr    bool
}

func cmdDu(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "write counts for all files, not just directories"},
		&Flag{Name: "summarize", Short: 's', Long: "summarize", Help: "display only a total for each argument"},
		&Flag{Name: "human", Short: 'h', Long: "human-readable", Help: "print sizes in human readable format (e.g., 1K 234M 2G)"},
		&Flag{Name: "kib", Short: 'k', Help: "like --block-size=1K"},
		&Flag{Name: "mib", Short: 'm', Help: "like --block-size=1M"},
		&Flag{Name: "bytes", Short: 'b', Long: "bytes", Help: "equivalent to --apparent-size --block-size=1"},
		&Flag{Name: "block-size", Short: 'B', Long: "block-size", HasArg: true, ArgName: "SIZE", Help: "scale sizes by SIZE before printing (e.g., 1M, 512)"},
		&Flag{Name: "apparent", Long: "apparent-size", Help: "print apparent sizes, rather than disk usage"},
		&Flag{Name: "total", Short: 'c', Long: "total", Help: "produce a grand total"},
		&Flag{Name: "max-depth", Short: 'd', Long: "max-depth", HasArg: true, ArgName: "N", Help: "print only directories (and files with -a) at depth ≤ N; -d 0 == -s"},
		&Flag{Name: "one-file-system", Short: 'x', Long: "one-file-system", Help: "skip directories on different file systems (volume)"},
		&Flag{Name: "dereference", Short: 'L', Long: "dereference", Help: "dereference all symbolic links"},
		&Flag{Name: "dereference-args", Short: 'H', Help: "dereference symbolic links listed on the command line"},
		&Flag{Name: "dereference-default", Short: 'P', Long: "no-dereference", Help: "don't follow any symbolic links (default)"},
		&Flag{Name: "null", Short: '0', Long: "null", Help: "end each output line with NUL, not newline"},
		&Flag{Name: "separate-dirs", Short: 'S', Long: "separate-dirs", Help: "print the size of each directory separately, not including subdirectories"},
		&Flag{Name: "count-links", Short: 'l', Long: "count-links", Help: "count sizes many times if hard linked"},
		&Flag{Name: "threshold", Short: 't', Long: "threshold", HasArg: true, ArgName: "SIZE", Help: "exclude entries smaller than SIZE if positive, or entries greater than SIZE if negative"},
		&Flag{Name: "exclude", Long: "exclude", HasArg: true, ArgName: "PATTERN", Help: "exclude files and directories matching PATTERN"},
	)
	if !parseOrDie("du", f, args) {
		return 2
	}
	if handleHelpVersion("du", "[OPTION]... [FILE]...\n  or:  du [OPTION]... --files0-from=F", f) {
		return 0
	}
	o := duOpts{
		all:            f.Has("all"),
		summarize:      f.Has("summarize"),
		human:          f.Has("human"),
		unit:           1024,
		apparent:       f.Has("apparent"),
		total:          f.Has("total"),
		oneFileSystem:  f.Has("one-file-system"),
		countLinks:     f.Has("count-links"),
		derefAll:       f.Has("dereference"),
		derefArgs:      f.Has("dereference-args"),
		nullOut:        f.Has("null"),
		separateDirs:   f.Has("separate-dirs"),
		threshold:      -1,
	}
	switch {
	case f.Has("kib"):
		o.unit = 1024
	case f.Has("mib"):
		o.unit = 1 << 20
	}
	if bs := f.Str("block-size"); bs != "" {
		if u, err := parseDuUnit(bs); err == nil {
			o.unit = u
		} else {
			warn("du", "invalid block size '%s': %v", bs, err)
			return 1
		}
	}
	if f.Has("bytes") {
		o.unit = 1
		o.apparent = true
	}
	if th := f.Str("threshold"); th != "" {
		if v, err := parseDuUnit(th); err == nil {
			o.threshold = v
		} else {
			warn("du", "invalid threshold '%s': %v", th, err)
			return 1
		}
	}
	if f.Has("summarize") {
		o.maxDepth = 0
	} else {
		o.maxDepth = -1
	}
	if d, err := f.Int("max-depth", -1); err != nil {
		warn("du", "invalid depth '%s'", f.Str("max-depth"))
		return 1
	} else if d >= 0 {
		o.maxDepth = d
	}
	o.excludes = f.Strs("exclude")

	ops := f.Positional
	// expand glob but preserve --exclude's own wildcards
	for i, a := range ops {
		if a != "-" && strings.ContainsAny(a, "*?[") {
			matches, err := filepath.Glob(a)
			if err == nil && len(matches) > 0 {
				ops = append(ops[:i], append(matches, ops[i+1:]...)...)
			}
		}
	}
	if len(ops) == 0 {
		ops = []string{"."}
	}
	w := duWalk{o: &o}
	exit := 0
	for _, op := range ops {
		fi, err := duStat(op, o.derefAll || o.derefArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "du: cannot access '%s': %v\n", op, err)
			exit = 1
			continue
		}
		w.operand = op
		if o.oneFileSystem && fi.IsDir() {
			if v, err := duVolume(op); err == nil {
				w.volRoot = v
			}
		}
		if !fi.IsDir() {
			sz := duSize(op, fi, &o)
			if o.threshold < 0 || sz >= o.threshold {
				w.duPrint(sz, op)
			}
			w.total += sz
			continue
		}
		w.duWalk(op, &o, 0)
	}
	// For directories, duWalk already contributes total. For files above, we added locally.
	// The total above for files duplicates? Let me recheck:
	// In the for loop: file: w.duPrint(sz,op); w.total+=add ✓
	// dir: _, ok := w.duWalk(...); total added inside walk (to w.total)
	// OK. But I shouldn't add again after walk.
	if o.total {
		label := "total"
		w.duPrintLabel(w.total, label)
	}
	if w.hadErr {
		exit = 1
	}
	if exit != 0 || w.hadErr {
		return 1
	}
	return 0
}

func duStat(path string, deref bool) (os.FileInfo, error) {
	if deref {
		return os.Stat(path)
	}
	return os.Lstat(path)
}

func duSize(path string, fi os.FileInfo, o *duOpts) int64 {
	if o.apparent {
		return fi.Size()
	}
	return winAllocSize(path, fi)
}

func (w *duWalk) duWalk(path string, o *duOpts, depth int) (int64, bool) {
	fi, err := duStat(path, o.derefAll)
	if err != nil {
		fmt.Fprintf(os.Stderr, "du: cannot read '%s': %v\n", path, err)
		w.hadErr = true
		return 0, false
	}
	if !fi.IsDir() {
		return duSize(path, fi, o), false
	}
	// directory
	ownSize := duSize(path, fi, o)

	// -x: skip directories on a different volume
	if o.oneFileSystem && w.volRoot != "" && depth > 0 {
		if v, err := duVolume(path); err == nil && v != w.volRoot {
			// still display the directory at depth 0; skip deeper
			if depth <= o.maxDepth || o.maxDepth < 0 {
				w.duPrint(ownSize, path)
			}
			return ownSize, false
		}
	}
	// cycle guard for -L
	if o.derefAll {
		key := duFileKey(path, fi, o)
		if key == "" {
			key = path
		}
		if w.seenDirs == nil {
			w.seenDirs = map[string]bool{}
		}
		if w.seenDirs[key] {
			return 0, false
		}
		w.seenDirs[key] = true
	}

	total := int64(0)
	if o.separateDirs {
		total = ownSize
	}
	fh, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "du: cannot open directory '%s': %v\n", path, err)
		w.hadErr = true
		return ownSize, false
	}
	fis, err := fh.Readdir(-1)
	fh.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "du: reading directory '%s': %v\n", path, err)
		w.hadErr = true
	}
	for _, d := range fis {
		name := d.Name()
		if name == "." || name == ".." {
			continue
		}
		if excludedChild(name, o.excludes) {
			continue
		}
		childPath := filepath.Join(path, name)
		childFi := d
		add := int64(0)
		if o.derefAll {
			// follow symlinks when walking
			if s, err := os.Stat(childPath); err == nil {
				childFi = s
			}
		}
		if childFi.IsDir() {
			sub, _ := w.duWalk(childPath, o, depth+1)
			total += sub
		} else {
			add = duSize(childPath, childFi, o)
			if !o.countLinks && !isSymlinkMode(childFi.Mode()) {
				key := duFileKey(childPath, childFi, o)
				if key != "" {
					if w.seenFiles == nil {
						w.seenFiles = map[string]bool{}
					}
					if w.seenFiles[key] {
						add = 0 // hardlink duplicate
					} else {
						w.seenFiles[key] = true
					}
				}
			}
			total += add
			// print file line when -a and within depth
			if o.all && (depth+1 <= o.maxDepth || o.maxDepth < 0) {
				if o.threshold < 0 || add >= o.threshold {
					w.duPrint(add, childPath)
					w.total += add
				}
			}
		}
	}
	// print directory line
	if depth <= o.maxDepth || o.maxDepth < 0 {
		if o.threshold < 0 || total >= o.threshold {
			w.duPrint(total, path)
			w.total += total
		}
	} else {
		w.total += total
	}
	return total + ownSize, true
}

func excludedChild(name string, patterns []string) bool {
	for _, p := range patterns {
		if wildcardMatch(p, name) {
			return true
		}
	}
	return false
}

func (w *duWalk) duPrint(sizeBytes int64, disp string) {
	o := w.o
	if o.human {
		fmt.Print(humanSize(sizeBytes))
	} else {
		blocks := (sizeBytes + o.unit - 1) / o.unit
		fmt.Print(strconv.FormatInt(blocks, 10))
	}
	if o.nullOut {
		fmt.Print("\t", disp, "\x00")
	} else {
		fmt.Print("\t", disp)
		fmt.Println()
	}
}

func (w *duWalk) duPrintLabel(sizeBytes int64, label string) {
	o := w.o
	if o.human {
		fmt.Print(humanSize(sizeBytes))
	} else {
		blocks := (sizeBytes + o.unit - 1) / o.unit
		fmt.Print(strconv.FormatInt(blocks, 10))
	}
	if o.nullOut {
		fmt.Print("\t", label, "\x00")
	} else {
		fmt.Print("\t", label)
		fmt.Println()
	}
}

// parseDuUnit parses a GNU-style numeric size with optional suffix (1K, 2M, …).
func parseDuUnit(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	// clip a single SI/IEC suffix (case-insensitive)
	switch {
	case len(s) >= 3 && strings.EqualFold(s[len(s)-3:], "KIB"):
		mult, s = 1024, s[:len(s)-3]
	case len(s) >= 1 && (s[len(s)-1] == 'K' || s[len(s)-1] == 'k'):
		mult, s = 1024, s[:len(s)-1]
	case len(s) >= 3 && strings.EqualFold(s[len(s)-3:], "MIB"):
		mult, s = 1<<20, s[:len(s)-3]
	case len(s) >= 1 && (s[len(s)-1] == 'M' || s[len(s)-1] == 'm'):
		mult, s = 1<<20, s[:len(s)-1]
	case len(s) >= 3 && strings.EqualFold(s[len(s)-3:], "GIB"):
		mult, s = 1<<30, s[:len(s)-3]
	case len(s) >= 1 && (s[len(s)-1] == 'G' || s[len(s)-1] == 'g'):
		mult, s = 1<<30, s[:len(s)-1]
	case len(s) >= 3 && strings.EqualFold(s[len(s)-3:], "TIB"):
		mult, s = 1<<40, s[:len(s)-3]
	case len(s) >= 1 && (s[len(s)-1] == 'T' || s[len(s)-1] == 't'):
		mult, s = 1<<40, s[:len(s)-1]
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return v * mult, nil
}

// duVolume returns a short identifier of the volume containing path (first 2
// characters for drives, else path prefix for UNC). Used for -x.
func duVolume(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(abs, `\\`) {
		// UNC: return the server+share prefix
		parts := strings.SplitN(abs[2:], `\`, 3)
		if len(parts) >= 2 {
			return `\\` + parts[0] + `\` + parts[1], nil
		}
		return abs, nil
	}
	if len(abs) >= 2 && abs[1] == ':' {
		return abs[:2], nil
	}
	return abs, nil
}

// duFileKey builds a per-volume-file deduplication key. It reuses the NTFS
// file id from the ls implementation when available.
func duFileKey(path string, fi os.FileInfo, o *duOpts) string {
	if isSymlinkMode(fi.Mode()) {
		return "" // symlinks are not content duplicates
	}
	vol, err := duVolume(path)
	if err != nil {
		return ""
	}
	id := fileID(path)
	return vol + "|" + strconv.FormatUint(id, 10)
}