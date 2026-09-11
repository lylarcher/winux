//go:build windows

package core

// df reports file system disk space usage (GNU-style), mapped honestly to
// Windows: a "file system" is a drive letter (C:, D:, ...; subst and mapped
// network drives included, since GetLogicalDriveStrings lists them). The
// resolver uses the kernel32 volume APIs so no external tools are needed.
//
// By default the output is the stable POSIX layout (like `df -P`):
//
//	Filesystem 1K-blocks  Used Available Use% Mounted on
//	C:         204799992  153160  204646832   1% C:\
//
// -h/--human-readable uses powers of 1024 (1K 234M 2G, GNU formatting);
// -H/--si uses powers of 1000. -B SIZE rescales the numeric columns.
// -T/--print-type inserts a Type column (NTFS, FAT32, ...).
// -i/--inodes is unsupported and fails with a clear explanation: Windows has
// no inode concept and provides no per-volume file-count limit via Win32.
// Drives that cannot be queried (e.g. an empty CD/DVD) are skipped unless -a.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func init() {
	addCmd("df", "report file system disk space usage", cmdDf)
}

var (
	procGetLogicalDriveStringsW = kernel32.NewProc("GetLogicalDriveStringsW")
	procGetDiskFreeSpaceExW     = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetVolumeInformationW   = kernel32.NewProc("GetVolumeInformationW")
	procGetDriveTypeW           = kernel32.NewProc("GetDriveTypeW")
)

const (
	drvUnknown   = 0
	drvNoRoot    = 1
	drvRemovable = 2
	drvFixed     = 3
	drvRemote    = 4
	drvCDROM     = 5
	drvRamdisk   = 6
)

// listDriveRoots returns the drive roots such as "C:\", "D:\" in the order
// GetLogicalDriveStringsW reports them (the list is double-NUL terminated).
func listDriveRoots() []string {
	buf := make([]uint16, 260)
	r, _, _ := procGetLogicalDriveStringsW.Call(
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r == 0 {
		return nil
	}
	var roots []string
	start := 0
	for i, c := range buf {
		if c == 0 {
			if i == start {
				break
			}
			roots = append(roots, syscall.UTF16ToString(buf[start:i]))
			start = i + 1
		}
	}
	return roots
}

// driveSpace queries the free/used space of a drive root ("C:\").
func driveSpace(root string) (avail, total, free uint64) {
	p, _ := syscall.UTF16PtrFromString(root)
	r, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&free)),
	)
	if r == 0 {
		return 0, 0, 0
	}
	return avail, total, free
}

// driveFsName returns the file system name ("NTFS", "FAT32", "CDFS", ...) or
// "" when it cannot be read (e.g. a drive without media).
func driveFsName(root string) string {
	p, _ := syscall.UTF16PtrFromString(root)
	name := make([]uint16, 64)
	r, _, _ := procGetVolumeInformationW.Call(
		uintptr(unsafe.Pointer(p)),
		0, 0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&name[0])),
		uintptr(len(name)),
	)
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(name)
}

// driveType returns the Win32 DRIVE_* constant for a drive root.
func driveType(root string) int {
	p, _ := syscall.UTF16PtrFromString(root)
	r, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
	return int(r)
}

type dfDrive struct {
	root     string // "C:\"
	fs       string // "C:" (the file system identifier)
	typ      string // "NTFS" or ""
	isRemote bool
	avail    uint64
	total    uint64
	free     uint64
	ok       bool // false: the drive exists but cannot be queried
}

func cmdDf(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "include pseudo, duplicate and inaccessible file systems"},
		&Flag{Name: "block-size", Short: 'B', Long: "block-size", HasArg: true, ArgName: "SIZE", Help: "scale sizes by SIZE before printing (e.g., -B M or -B 512)"},
		&Flag{Name: "human", Short: 'h', Long: "human-readable", Help: "print sizes in human readable format (e.g., 1K 234M 2G)"},
		&Flag{Name: "si", Short: 'H', Long: "si", Help: "like -h, but use powers of 1000 not 1024"},
		&Flag{Name: "inodes", Short: 'i', Long: "inodes", Help: "list inode information instead of block usage (unsupported: no inodes on Windows)"},
		&Flag{Name: "kib", Short: 'k', Help: "like --block-size=1K"},
		&Flag{Name: "local", Short: 'l', Long: "local", Help: "limit listing to local file systems (skip network drives)"},
		&Flag{Name: "portability", Short: 'P', Long: "portability", Help: "use the POSIX output format (the default)"},
		&Flag{Name: "print-type", Short: 'T', Long: "print-type", Help: "print file system type"},
		&Flag{Name: "type", Short: 't', Long: "type", HasArg: true, ArgName: "TYPE", Help: "limit listing to file systems of type TYPE (repeatable)"},
		&Flag{Name: "exclude-type", Short: 'x', Long: "exclude-type", HasArg: true, ArgName: "TYPE", Help: "limit listing to file systems not of type TYPE (repeatable)"},
		&Flag{Name: "total", Long: "total", Help: "produce a grand total"},
		&Flag{Name: "sysv", Short: 'v', Help: "ignored (for System V compatibility)"},
	)
	if !parseOrDie("df", f, args) {
		return 2
	}
	if handleHelpVersion("df", "[OPTION]... [FILE]...", f) {
		return 0
	}

	o := dfOpts{
		all:      f.Has("all"),
		human:    f.Has("human"),
		si:       f.Has("si"),
		local:    f.Has("local"),
		printType: f.Has("print-type"),
		total:    f.Has("total"),
		types:    f.Strs("type"),
		exclude:  f.Strs("exclude-type"),
	}

	if f.Has("inodes") {
		warn("df", "-i/--inodes: Windows 没有 inode 概念（NTFS 用文件 ID, 但 Win32 不提供卷级 inode 数量）")
		return 1
	}

	// the human/block-size resolution, GNU order: -h > -H > -B > -k > 1K
	switch {
	case o.human:
		o.header = "Size"
	case o.si:
		o.header = "Size"
	case f.Has("block-size"):
		bs, label, err := parseBlockSize(f.Str("block-size"))
		if err != nil {
			warn("df", "invalid block size '%s'", f.Str("block-size"))
			return 1
		}
		o.blockSize, o.blockLabel = bs, label
		o.header = o.blockLabel + "-blocks"
	default:
		o.blockSize, o.blockLabel = 1024, "1K"
		o.header = "1K-blocks"
	}
	if o.human || o.si {
		o.blockSize = 1
	}

	// operands restrict the listing to their drives (GNU semantics: a bare
	// drive must exist, any other path must exist on disk)
	wantRoots := map[string]bool{}
	for _, a := range f.Positional {
		root, err := driveRootOf(a)
		if err != nil {
			if errors.Is(err, errNoDrive) {
				warn("df", "%s: No such file or directory", a)
			} else {
				warn("df", "%s: %v", a, err)
			}
			return 1
		}
		if isBareDriveRef(a) {
			if !driveExists(root) {
				warn("df", "%s: No such file or directory", a)
				return 1
			}
		} else {
			abs, _ := filepath.Abs(a)
			if _, err := os.Stat(abs); err != nil {
				warn("df", "%s: No such file or directory", a)
				return 1
			}
		}
		wantRoots[strings.ToUpper(root)] = true
	}

	drives := collectDrives(o, wantRoots)

	var rows []dfRow
	var tUsed, tAvail, tTotal uint64
	for _, d := range drives {
		if len(o.types) > 0 && !strSliceEqualFold(o.types, d.typ) {
			continue
		}
		if strSliceEqualFold(o.exclude, d.typ) {
			continue
		}
		row := dfRow{fs: d.fs, typ: d.typ, mounted: d.root}
		if !d.ok {
			row.blocks, row.used, row.avail, row.usePct = "-", "-", "-", "-"
		} else {
			used := d.total - d.free
			row.blocks = o.size(d.total)
			row.used = o.size(used)
			row.avail = o.size(d.avail)
			row.usePct = dfPct(used, d.total)
			tUsed += used
			tAvail += d.avail
			tTotal += d.total
		}
		rows = append(rows, row)
	}

	if o.total && len(rows) > 0 {
		row := dfRow{fs: "total", typ: "-", mounted: ""}
		if tTotal == 0 {
			row.blocks, row.used, row.avail, row.usePct = "-", "-", "-", "-"
		} else {
			row.blocks = o.size(tTotal)
			row.used = o.size(tUsed)
			row.avail = o.size(tAvail)
			row.usePct = dfPct(tUsed, tTotal)
		}
		rows = append(rows, row)
	}

	printDfTable(o, rows)
	return 0
}

type dfOpts struct {
	all, human, si  bool
	local, printType bool
	total           bool
	blockSize       int64
	blockLabel      string
	header          string
	types, exclude  []string
}

func (o *dfOpts) size(n uint64) string {
	switch {
	case o.human:
		return humanSize(int64(n))
	case o.si:
		return siHumanSize(int64(n))
	default:
		return strconv.FormatUint(n/uint64(o.blockSize), 10)
	}
}

// collectDrives lists the drives, filtered by -l and by the operand set.
func collectDrives(o dfOpts, want map[string]bool) []dfDrive {
	var out []dfDrive
	for _, root := range listDriveRoots() {
		up := strings.ToUpper(root)
		if len(want) > 0 && !want[up] {
			continue
		}
		d := dfDrive{root: root, fs: strings.TrimSuffix(up, `\`)}
		d.typ = driveFsName(root)
		d.isRemote = driveType(root) == drvRemote
		if o.local && d.isRemote {
			continue
		}
		d.avail, d.total, d.free = driveSpace(root)
		d.ok = d.total > 0
		if !d.ok && !o.all {
			continue // e.g. an empty CD/DVD drive is skipped unless -a
		}
		out = append(out, d)
	}
	return out
}

var errNoDrive = errors.New("no such drive")

func isBareDriveRef(a string) bool {
	return (len(a) == 2 && a[1] == ':' && isAlpha(a[0])) ||
		(len(a) == 3 && a[1] == ':' && a[2] == '\\' && isAlpha(a[0]))
}

func driveExists(root string) bool {
	root = strings.ToUpper(root)
	for _, r := range listDriveRoots() {
		if strings.ToUpper(r) == root {
			return true
		}
	}
	return false
}

func driveRootOf(arg string) (string, error) {
	if len(arg) == 2 && arg[1] == ':' && isAlpha(arg[0]) {
		return strings.ToUpper(arg[:2]) + `\`, nil
	}
	if len(arg) == 3 && arg[1] == ':' && arg[2] == '\\' && isAlpha(arg[0]) {
		return strings.ToUpper(arg[:2]) + `\`, nil
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	vol := filepath.VolumeName(abs)
	if vol == "" {
		return "", errNoDrive
	}
	if strings.HasPrefix(vol, `\\`) {
		return "", fmt.Errorf("UNC paths are not supported by df (list drives by letter)")
	}
	return strings.ToUpper(vol[:2]) + `\`, nil
}

func strSliceEqualFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func dfPct(used, total uint64) string {
	if total == 0 {
		return "-"
	}
	return strconv.FormatUint(used*100/total, 10) + "%"
}

func siHumanSize(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	f := float64(n)
	for _, u := range []string{"K", "M", "G", "T", "P", "E"} {
		f /= 1000
		if f < 1000 {
			if f < 10 {
				return fmt.Sprintf("%.1f%s", f, u)
			}
			return fmt.Sprintf("%.0f%s", f, u)
		}
	}
	return fmt.Sprintf("%.0fE", f)
}

type dfRow struct {
	fs, typ                     string
	blocks, used, avail, usePct string
	mounted                     string
}

func printDfTable(o dfOpts, rows []dfRow) {
	type col struct {
		header   string
		width    int
		right    bool // numeric columns align right
		get      func(dfRow) string
	}
	cols := []col{
		{header: "Filesystem", get: func(r dfRow) string { return r.fs }},
	}
	if o.printType {
		cols = append(cols, col{header: "Type", get: func(r dfRow) string { return r.typ }})
	}
	cols = append(cols,
		col{header: o.header, right: true, get: func(r dfRow) string { return r.blocks }},
		col{header: "Used", right: true, get: func(r dfRow) string { return r.used }},
		col{header: "Available", right: true, get: func(r dfRow) string { return r.avail }},
		col{header: "Use%", right: true, get: func(r dfRow) string { return r.usePct }},
		col{header: "Mounted on", get: func(r dfRow) string { return r.mounted }},
	)
	for i := range cols {
		w := len(cols[i].header)
		for _, r := range rows {
			if l := len(cols[i].get(r)); l > w {
				w = l
			}
		}
		cols[i].width = w
	}
	// header line
	for i, c := range cols {
		if i > 0 {
			fmt.Print(" ")
		}
		fmt.Print(padCell(c.header, c.width, c.right))
	}
	fmt.Println()
	// rows
	for _, r := range rows {
		for i, c := range cols {
			if i > 0 {
				fmt.Print(" ")
			}
			fmt.Print(padCell(c.get(r), c.width, c.right))
		}
		fmt.Println()
	}
}

func padCell(s string, w int, right bool) string {
	if right {
		return strings.Repeat(" ", w-len(s)) + s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// parseBlockSize parses a GNU SIZE argument ("512", "1M", "KB", "2GB", ...)
// into bytes and the display label ("512", "1M", "1KB", "2GB").
func parseBlockSize(s string) (int64, string, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	num := s[:i]
	unit := strings.ToUpper(s[i:])
	n := int64(1)
	if num != "" {
		v, err := strconv.ParseInt(num, 10, 64)
		if err != nil || v <= 0 {
			return 0, "", fmt.Errorf("invalid size")
		}
		n = v
	}
	var mult int64
	switch unit {
	case "", "B":
		mult = 1
	case "K", "KIB":
		mult = 1 << 10
	case "KB":
		mult = 1000
	case "M", "MIB":
		mult = 1 << 20
	case "MB":
		mult = 1000 * 1000
	case "G", "GIB":
		mult = 1 << 30
	case "GB":
		mult = 1000 * 1000 * 1000
	case "T", "TIB":
		mult = 1 << 40
	case "TB":
		mult = 1000 * 1000 * 1000 * 1000
	default:
		return 0, "", fmt.Errorf("invalid suffix")
	}
	label := s
	if num == "" {
		label = "1" + unit
	}
	return n * mult, label, nil
}
