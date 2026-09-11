package core

// ps reports a snapshot of running processes. The option surface follows
// procps (Linux): the BSD `ps aux` and SysV `ps -ef` listings plus the common
// selectors (-e -a -u -U -p -C -t), custom columns (-o) and --sort. Windows
// has no tty or POSIX process states, so TTY is always "?" and STAT is a
// coarse "R" (running); every other column is real data.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("ps", "report a snapshot of the current processes", cmdPs)
}

// procInfo is one snapshot row, filled by the platform backend.
type procInfo struct {
	pid, ppid int
	name      string // image name, e.g. chrome.exe
	cmdline   string // full command line; falls back to the image name
	user      string // owner account name, "?" when unreadable
	create    time.Time
	userTime, kernTime time.Duration
	virtKB, rssKB      uint64
	threads, prio      int
}

func cmdPs(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'e', Long: "all", Help: "select all processes"},
		&Flag{Name: "all-A", Short: 'A'},
		&Flag{Name: "all-a", Short: 'a', Help: "select all processes (Windows has no tty filtering)"},
		&Flag{Name: "full", Short: 'f', Long: "full", Help: "full-format listing (SysV style)"},
		&Flag{Name: "user", Short: 'u', Long: "user", HasArg: true, ArgName: "USER", Help: "select processes owned by USER; bare -u (as in `ps aux`) selects all"},
		&Flag{Name: "User", Short: 'U', Long: "User", HasArg: true, ArgName: "USER", Help: "select processes whose real user is USER"},
		&Flag{Name: "pid", Short: 'p', Long: "pid", HasArg: true, ArgName: "PIDLIST", Help: "select by process id, comma separated"},
		&Flag{Name: "comm", Short: 'C', Long: "comm", HasArg: true, ArgName: "CMD", Help: "select by image name (e.g. chrome)"},
		&Flag{Name: "tty", Short: 't', Long: "tty", HasArg: true, ArgName: "TTY", Help: "select by controlling terminal (only '?' matches on Windows)"},
		&Flag{Name: "output", Short: 'o', Long: "output", HasArg: true, ArgName: "LIST", Help: "comma separated columns: pid,ppid,user,comm,args,cmd,%cpu,%mem,vsz,rss,stat,time,etime,start,tty"},
		&Flag{Name: "sort", Long: "sort", HasArg: true, ArgName: "KEY", Help: "sort by pid,comm,user,%cpu,%mem or time (prefix - for descending)"},
		&Flag{Name: "no-headers", Short: 'h', Long: "no-headers", Help: "do not print the header line"},
		&Flag{Name: "negate", Short: 'N', Long: "negate", Help: "negate the selection"},
	)
	if !parseOrDie("ps", f, args) {
		return 2
	}
	if handleHelpVersion("ps", "[OPTION]...", f) {
		return 0
	}

	// `ps aux` is the classic BSD spelling (no dash, a+u+x combined); the
	// flags parser leaves "aux" as a positional, so detect it here.
	bsdStyle := false
	if len(f.Positional) == 1 && f.Positional[0] == "aux" && !f.Has("output") && !f.Has("full") {
		bsdStyle = true
	}

	procs, err := snapshotProcesses()
	if err != nil {
		warn("ps", "%v", err)
		return 1
	}

	// ---- selection ----
	hasSelector := f.Str("user") != "" || f.Str("User") != "" || f.Str("pid") != "" ||
		f.Str("comm") != "" || f.Str("tty") != ""
	all := f.Has("all") || f.Has("all-A") || f.Has("all-a") || bsdStyle || f.Has("user") || hasSelector
	sel := procs
	if !all {
		me := shortUserName(currentUserName())
		sel = nil
		for _, p := range procs {
			if p.user != "?" && (me == "unknown" || strings.EqualFold(shortUserName(p.user), me)) {
				sel = append(sel, p)
			}
		}
	}
	var pick []procInfo
	negate := f.Has("negate")
	match := func(p procInfo) bool {
		if u := f.Str("user"); u != "" && !strings.EqualFold(shortUserName(p.user), u) && !strings.EqualFold(shortUserName(p.user), strings.TrimSuffix(u, ".exe")) {
			return false
		}
		if u := f.Str("User"); u != "" && !strings.EqualFold(shortUserName(p.user), u) {
			return false
		}
		if pl := f.Str("pid"); pl != "" {
			found := false
			for _, ps := range strings.Split(pl, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(ps)); err == nil && n == p.pid {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		if c := f.Str("comm"); c != "" {
			base := strings.ToLower(strings.TrimSuffix(p.name, ".exe"))
			if base != strings.ToLower(strings.TrimSuffix(c, ".exe")) {
				return false
			}
		}
		if t := f.Str("tty"); t != "" && t != "?" {
			return false
		}
		return true
	}
	for _, p := range sel {
		ok := match(p)
		if negate {
			ok = !ok
		}
		if ok {
			pick = append(pick, p)
		}
	}

	// ---- sorting ----
	sortKey, descending := "pid", false
	if s := f.Str("sort"); s != "" {
		sortKey = strings.TrimPrefix(s, "-")
		descending = strings.HasPrefix(s, "-")
		switch sortKey {
		case "pid", "comm", "user", "%cpu", "%mem", "time":
		default:
			warn("ps", "invalid --sort key '%s'", s)
			return 1
		}
	}
	sort.SliceStable(pick, func(i, j int) bool {
		a, b := pick[i], pick[j]
		var less bool
		switch sortKey {
		case "pid":
			less = a.pid < b.pid
		case "comm":
			less = a.name < b.name
		case "user":
			less = a.user < b.user
		case "%cpu":
			less = cpuPct(a) < cpuPct(b)
		case "%mem":
			less = memPct(a) < memPct(b)
		case "time":
			less = a.userTime+a.kernTime < b.userTime+b.kernTime
		}
		if descending {
			return !less
		}
		return less
	})

	// ---- output ----
	noHeader := f.Has("no-headers")
	phys := physMemoryKB()
	if o := f.Str("output"); o != "" {
		return psCustomOutput(pick, o, phys, noHeader)
	}
	switch {
	case bsdStyle:
		if !noHeader {
			fmt.Println("USER       PID  %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND")
		}
		for _, p := range pick {
			fmt.Printf("%-8s %5d %5.1f %4.1f %8d %6d %-8s %-4s %5s %5s %s\n",
				shortUserName(p.user), p.pid, cpuPct(p), memPct(p),
				p.virtKB, p.rssKB, "?", "R", startStr(p.create), timeStr(p),
				commandOf(p))
		}
	case f.Has("full"):
		if !noHeader {
			fmt.Println("UID        PID  PPID  C STIME TTY          TIME     CMD")
		}
		for _, p := range pick {
			fmt.Printf("%-8s %5d %5d %2d %5s %-10s %8s %s\n",
				shortUserName(p.user), p.pid, p.ppid, int(cpuPct(p)),
				startStr(p.create), "?", timeStrLong(p), commandOf(p))
		}
	default:
		if !noHeader {
			fmt.Println("  PID TTY          TIME CMD")
		}
		for _, p := range pick {
			fmt.Printf("%5d %-10s %8s %s\n", p.pid, "?", timeStrLong(p), p.name)
		}
	}
	return 0
}

func cpuPct(p procInfo) float64 {
	age := time.Since(p.create).Seconds()
	if age <= 0 {
		return 0
	}
	v := (p.userTime + p.kernTime).Seconds() / age * 100
	if v > 999.9 {
		v = 999.9
	}
	return v
}

func memPct(p procInfo) float64 {
	if phys := physMemoryKB(); phys > 0 {
		return float64(p.rssKB) / float64(phys) * 100
	}
	return 0
}

// timeStr renders MM:SS (or H:MM:SS past an hour) like the BSD TIME column.
func timeStr(p procInfo) string {
	return fmtDuration((p.userTime + p.kernTime).Seconds(), false)
}

func timeStrLong(p procInfo) string {
	return fmtDuration((p.userTime + p.kernTime).Seconds(), true)
}

func fmtDuration(secs float64, pad bool) string {
	total := int(secs)
	if total < 3600 {
		if pad {
			return fmt.Sprintf("%02d:%02d:%02d", total/3600, total/60%60, total%60)
		}
		return fmt.Sprintf("%02d:%02d", total/60, total%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", total/3600, total/60%60, total%60)
}

// startStr renders START/STIME like procps: HH:MM for today, MonDD otherwise.
func startStr(t time.Time) string {
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan02")
}

func commandOf(p procInfo) string {
	if p.cmdline != "" {
		return p.cmdline
	}
	return p.name
}

// etimeStr renders GNU's ELAPSED format [[DD-]hh:]mm:ss.
func etimeStr(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	if h >= 100 {
		days := h / 24
		return fmt.Sprintf("%d-%02d:%02d:%02d", days, h%24, int(d.Minutes())%60, int(d.Seconds())%60)
	}
	if h >= 1 {
		return fmt.Sprintf("%02d:%02d:%02d", h, int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%02d:%02d", int(d.Minutes())%60, int(d.Seconds())%60)
}

// psCustomOutput renders the -o column list.
func psCustomOutput(pick []procInfo, spec string, phys uint64, noHeader bool) int {
	var cols []string
	for _, c := range strings.Split(spec, ",") {
		if c = strings.TrimSpace(c); c != "" {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		warn("ps", "empty -o specification")
		return 1
	}
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = psColumnHeader(c)
	}
	if !noHeader {
		fmt.Println(strings.Join(header, " "))
	}
	for _, p := range pick {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = psColumn(p, c, phys)
		}
		fmt.Println(strings.Join(row, " "))
	}
	return 0
}

func psColumnHeader(c string) string {
	switch c {
	case "pid":
		return "PID"
	case "ppid":
		return "PPID"
	case "user", "uid":
		return "USER"
	case "comm", "command", "cmd", "args":
		return "COMMAND"
	case "%cpu":
		return "%CPU"
	case "%mem":
		return "%MEM"
	case "vsz":
		return "VSZ"
	case "rss":
		return "RSS"
	case "stat":
		return "STAT"
	case "time":
		return "TIME"
	case "etime":
		return "ELAPSED"
	case "start":
		return "STARTED"
	case "tty":
		return "TT"
	}
	return strings.ToUpper(c)
}

func psColumn(p procInfo, c string, phys uint64) string {
	switch c {
	case "pid":
		return strconv.Itoa(p.pid)
	case "ppid":
		return strconv.Itoa(p.ppid)
	case "user", "uid":
		return shortUserName(p.user)
	case "comm":
		return p.name
	case "command", "cmd", "args":
		return commandOf(p)
	case "%cpu":
		return fmt.Sprintf("%.1f", cpuPct(p))
	case "%mem":
		if phys > 0 {
			return fmt.Sprintf("%.1f", float64(p.rssKB)/float64(phys)*100)
		}
		return "0.0"
	case "vsz":
		return strconv.FormatUint(p.virtKB, 10)
	case "rss":
		return strconv.FormatUint(p.rssKB, 10)
	case "stat":
		return "R"
	case "time":
		return timeStr(p)
	case "etime":
		return etimeStr(p.create)
	case "start":
		return startStr(p.create)
	case "tty":
		return "?"
	}
	return ""
}
