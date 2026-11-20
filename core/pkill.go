package core

// pkill and pgrep select processes by name or command line and then signal them
// (pkill) or list them (pgrep). They share one selector, so `pgrep PATTERN` is
// exactly the dry run for `pkill PATTERN`.
//
// The pattern is a regular expression, as in GNU's tools. Without -f it matches
// the image name (comm, .exe stripped); with -f it matches the full command line.
// Selection follows procps: -x (whole string), -i (case), -u USER, -P PPID,
// -n (newest) and -o (oldest). pkill accepts the same signal spellings as kill
// (-TERM, -9, -s SIGNAL), plus two additions Windows scripts want: --count (how
// many were signalled) and --dry-run (show what would be signalled).

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func init() {
	addCmd("pkill", "signal processes by name or command line pattern", cmdPkill)
	addCmd("pgrep", "list processes by name or command line pattern", cmdPgrep)
}

// procSelector is the shared matcher of pkill and pgrep.
type procSelector struct {
	pattern      string
	re           *regexp.Regexp
	full         bool   // -f: match the command line instead of the image name
	exact        bool   // -x: the pattern must match the whole subject
	ignoreCase   bool   // -i
	user         string // -u: owner
	parent       int    // -P: parent pid, -1 when unset
	newest       bool   // -n
	oldest       bool   // -o
	count        bool   // -c
	listName     bool   // -l: print pid and name
	listFull     bool   // -a: print pid and full command line
	inverse      bool   // -v: select the processes that do not match
	dryRun       bool   // --dry-run (pkill)
	gracefulOnly bool   // --graceful-only: do not escalate to a forced kill
	quiet        bool   // -q (pkill): check only, signal nothing
}

// procOpts is the parsed command line of pkill/pgrep.
type procOpts struct {
	sel      *procSelector
	sig      signalDef
	operands []string
	help     bool
	version  bool
	ok       bool
}

// param is one option that needs a value.
type param struct {
	s  *procSelector
	ok bool
}

// parseProcOptions reads the shared option surface. It stops at the first word
// that is not an option: that word and everything after it are returned as the
// operands, so a pattern starting with '-' needs `--` (GNU getopt's rule for
// these tools).
//
// withSignal (pkill) additionally accepts the kill-style spellings -9/-KILL/
// -s SIGNAL/--signal=.
func parseProcOptions(cmd string, args []string, withSignal bool) procOpts {
	s := &procSelector{parent: -1}
	res := procOpts{sel: s, sig: defaultSignal(), ok: true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		// A word that cannot be an option starts the operand list. A lone "-"
		// is a word too (a pattern may begin with a dash only after `--`).
		if a == "--" {
			res.operands = args[i+1:]
			return res
		}
		if a != "" && a[0] != '-' {
			res.operands = args[i:]
			return res
		}
		switch {
		case a == "-h" || a == "--help":
			res.help = true
			res.ok = false
			return res
		case a == "-V" || a == "--version":
			res.version = true
			res.ok = false
			return res
		case a == "-f" || a == "--full":
			s.full = true
		case a == "-x" || a == "--exact":
			s.exact = true
		case a == "-i" || a == "--ignore-case":
			s.ignoreCase = true
		case a == "-n" || a == "--newest":
			s.newest = true
		case a == "-o" || a == "--oldest":
			s.oldest = true
		case a == "-c" || a == "--count":
			s.count = true
		case a == "-l" || a == "--list-name":
			s.listName = true
		case a == "-a" || a == "--full-list":
			s.listFull = true
		case a == "--dry-run":
			s.dryRun = true
		case a == "--graceful-only":
			s.gracefulOnly = true
		case a == "-q" || a == "--quiet":
			s.quiet = true
		case a == "-v" || a == "--inverse":
			s.inverse = true
		case a == "-u" || a == "--euid" || a == "--user" || a == "-U" || a == "--uid":
			if i+1 >= len(args) {
				warn(cmd, "option '%s' requires an argument", a)
				res.ok = false
				return res
			}
			i++
			s.user = args[i]
		case strings.HasPrefix(a, "--euid="), strings.HasPrefix(a, "--user="), strings.HasPrefix(a, "--uid="):
			s.user = a[strings.IndexByte(a, '=')+1:]
		case a == "-P" || a == "--parent":
			if i+1 >= len(args) {
				warn(cmd, "option '%s' requires an argument", a)
				res.ok = false
				return res
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				warn(cmd, "%s: invalid parent pid", args[i])
				res.ok = false
				return res
			}
			s.parent = n
		case strings.HasPrefix(a, "--parent="):
			n, err := strconv.Atoi(a[len("--parent="):])
			if err != nil {
				warn(cmd, "%s: invalid parent pid", a[len("--parent="):])
				res.ok = false
				return res
			}
			s.parent = n
		case withSignal && (a == "-s" || a == "--signal"):
			if i+1 >= len(args) {
				warn(cmd, "option '%s' requires an argument", a)
				res.ok = false
				return res
			}
			i++
			d, ok := lookupSignal(args[i])
			if !ok {
				warn(cmd, "%s: invalid signal specification", args[i])
				res.ok = false
				return res
			}
			res.sig = d
		case withSignal && strings.HasPrefix(a, "--signal="):
			d, ok := lookupSignal(a[len("--signal="):])
			if !ok {
				warn(cmd, "%s: invalid signal specification", a[len("--signal="):])
				res.ok = false
				return res
			}
			res.sig = d
		case withSignal && len(a) > 1:
			// -9 / -KILL / -SIGTERM
			if d, ok := lookupSignal(a); ok {
				res.sig = d
				break
			}
			warn(cmd, "unknown option '%s'", a)
			fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", cmd)
			res.ok = false
			return res
		default:
			warn(cmd, "unknown option '%s'", a)
			fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", cmd)
			res.ok = false
			return res
		}
	}
	return res
}

// compilePattern turns the pattern into the regexp used for matching.
func (s *procSelector) compilePattern(cmd string) bool {
	if s.pattern == "" {
		return true
	}
	// Matching is case-insensitive by default: Windows reports image names in
	// the case stored on disk (PING.EXE, CHROME.EXE, ...), so a case-sensitive
	// default would make `pgrep ping` miss ping.exe. -i stays accepted for
	// compatibility with scripts written for GNU pgrep.
	expr := "(?i)" + s.pattern
	if s.exact {
		expr = "^(?:" + expr + ")$"
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		warn(cmd, "%s: invalid pattern: %v", s.pattern, err)
		return false
	}
	s.re = re
	return true
}

// subject is the string a pattern is matched against for one process.
func (s *procSelector) subject(p procInfo) string {
	if s.full {
		if c := strings.TrimSpace(p.cmdline); c != "" {
			return c
		}
	}
	return procComm(p.name)
}

// procComm is the image name without the .exe suffix, the Windows answer to
// Linux's comm (which GNU pgrep matches by default).
func procComm(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name[:len(name)-len(".exe")]
	}
	return name
}

func (s *procSelector) matches(p procInfo) bool {
	// parent is only a filter when set: 0 is the zero value of an unset selector
	if s.parent > 0 && p.ppid != s.parent {
		return false
	}
	if s.user != "" && !sameUser(p.user, s.user) {
		return false
	}
	hit := true
	if s.re != nil {
		hit = s.re.MatchString(s.subject(p))
	}
	if s.inverse {
		return !hit
	}
	return hit
}

// sameUser compares an owner name from the process snapshot with -u USER.
// Windows account names arrive as "DOMAIN\user" (or plain "user" for built-in
// accounts), so the bare name is accepted too.
func sameUser(owner, want string) bool {
	if owner == "" || owner == "?" {
		return false
	}
	if strings.EqualFold(owner, want) {
		return true
	}
	short := owner
	if i := strings.LastIndexAny(owner, `\/`); i >= 0 {
		short = owner[i+1:]
	}
	if strings.EqualFold(short, want) {
		return true
	}
	// a domain-only spelling: DOMAIN\name as written
	if i := strings.LastIndexAny(want, `\/`); i >= 0 {
		return strings.EqualFold(owner, want)
	}
	return false
}

// selectProcs applies the selector to a snapshot and returns the chosen rows.
// The caller's own process and its direct children (the snapshot helper) are
// never included, so `pkill -f powershell` cannot kill the enumeration it just
// spawned, and pkill never signals itself.
func (s *procSelector) selectProcs(procs []procInfo) []procInfo {
	self := os.Getpid()
	var out []procInfo
	for _, p := range procs {
		if p.pid == self || p.ppid == self {
			continue
		}
		if s.matches(p) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	if (s.newest || s.oldest) && len(out) > 1 {
		best := out[0]
		for _, p := range out[1:] {
			if s.newest && p.create.After(best.create) {
				best = p
			}
			if s.oldest && !p.create.IsZero() && (best.create.IsZero() || p.create.Before(best.create)) {
				best = p
			}
		}
		out = []procInfo{best}
	}
	return out
}

// pgrepFlags is the option block shared by pgrep/pkill --help.
func pgrepFlags(pkill bool) *Flags {
	specs := []*Flag{
		&Flag{Name: "full", Short: 'f', Long: "full", Help: "match the whole command line, not just the image name"},
		&Flag{Name: "exact", Short: 'x', Long: "exact", Help: "the pattern must match the whole string"},
		&Flag{Name: "ignore-case", Short: 'i', Long: "ignore-case", Help: "case-insensitive matching (already the default on Windows)"},
		&Flag{Name: "euid", Short: 'u', Long: "euid", HasArg: true, ArgName: "USER", Help: "only processes owned by USER"},
		&Flag{Name: "parent", Short: 'P', Long: "parent", HasArg: true, ArgName: "PPID", Help: "only children of PPID"},
		&Flag{Name: "newest", Short: 'n', Long: "newest", Help: "select only the newest matching process"},
		&Flag{Name: "oldest", Short: 'o', Long: "oldest", Help: "select only the oldest matching process"},
		&Flag{Name: "count", Short: 'c', Long: "count", Help: "print how many processes were selected"},
		&Flag{Name: "inverse", Short: 'v', Long: "inverse", Help: "select the processes that do NOT match"},
	}
	if pkill {
		specs = append(specs,
			&Flag{Name: "dry-run", Long: "dry-run", Help: "print what would be signalled, change nothing"},
			&Flag{Name: "graceful-only", Long: "graceful-only", Help: "fail instead of forcing when a process cannot be asked to exit"},
			&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "signal nothing; only set the exit status"},
		)
	} else {
		specs = append(specs,
			&Flag{Name: "list-name", Short: 'l', Long: "list-name", Help: "print the process name next to the pid"},
			&Flag{Name: "full-list", Short: 'a', Long: "full-list", Help: "print the command line next to the pid"},
		)
	}
	return NewFlags(specs...)
}

func pgrepHelp(cmd string, pkill bool) {
	if pkill {
		fmt.Println("Usage: pkill [-SIGNAL] [-fivx] [-u USER] [-P PPID] [-n|-o] PATTERN")

	} else {
		fmt.Println("Usage: pgrep [-flavx] [-u USER] [-P PPID] [-n|-o] PATTERN")
	}
	printFlagList(pgrepFlags(pkill))
	if pkill {
		fmt.Println()
		fmt.Println("Additions for scripts: --count prints the number signalled, --dry-run only")
		fmt.Println("prints what would be signalled, -q signals nothing and only sets the status.")
		fmt.Println("The default signal is SIGTERM (see `kill -L` for the Windows mapping).")
	}
	fmt.Println()
	fmt.Println("The pattern is a regular expression matched against the image name (or, with -f,")
	fmt.Println("the command line). Matching ignores case by default, because Windows reports")
	fmt.Println("image names as stored on disk (PING.EXE). A pattern that begins with '-' needs --.")
	fmt.Println("With -P or -u a pattern may be omitted (select everything they name).")
	fmt.Println("Exit status: 0 when something matched, 1 when nothing did.")
	fmt.Println("Examples:")
	fmt.Println("  pgrep -l chrome            list chrome processes with their names")
	fmt.Println("  pkill -x ping              terminate processes named exactly ping")
	fmt.Println("  pkill -f 'node .*server'   termate node running a server script")
	fmt.Println("  pkill -9 -u lyl spam       force-kill 'spam' owned by lyl")
}

func cmdPgrep(args []string) int {
	opts := parseProcOptions("pgrep", args, false)
	if opts.help {
		pgrepHelp("pgrep", false)
		return 0
	}
	if opts.version {
		printVersion("pgrep")
		return 0
	}
	if !opts.ok {
		return 2
	}
	sel, rest := opts.sel, opts.operands
	if len(rest) == 0 && sel.parent <= 0 && sel.user == "" {
		warn("pgrep", "missing pattern operand")
		fmt.Fprintln(os.Stderr, "Try 'pgrep --help' for more information.")
		return 2
	}
	if len(rest) > 0 {
		sel.pattern = rest[0]
	}
	if !sel.compilePattern("pgrep") {
		return 2
	}
	procs, err := snapshotProcesses()
	if err != nil {
		warn("pgrep", "%v", err)
		return 1
	}
	matches := sel.selectProcs(procs)
	if sel.count {
		fmt.Println(len(matches))
	} else {
		pgrepPrint(sel, matches)
	}
	if len(matches) == 0 {
		return 1
	}
	return 0
}

func pgrepPrint(sel *procSelector, matches []procInfo) {
	for _, p := range matches {
		switch {
		case sel.listFull:
			line := strings.TrimSpace(p.cmdline)
			if line == "" {
				line = p.name
			}
			fmt.Printf("%d %s\n", p.pid, line)
		case sel.listName:
			fmt.Printf("%d %s\n", p.pid, procComm(p.name))
		default:
			fmt.Printf("%d\n", p.pid)
		}
	}
}

func cmdPkill(args []string) int {
	opts := parseProcOptions("pkill", args, true)
	if opts.help {
		pgrepHelp("pkill", true)
		return 0
	}
	if opts.version {
		printVersion("pkill")
		return 0
	}
	if !opts.ok {
		return 2
	}
	sel, sig, rest := opts.sel, opts.sig, opts.operands
	if len(rest) == 0 && sel.parent <= 0 && sel.user == "" {
		warn("pkill", "missing pattern operand")
		fmt.Fprintln(os.Stderr, "Try 'pkill --help' for more information.")
		return 2
	}
	if len(rest) > 0 {
		sel.pattern = rest[0]
	}
	if sig.action == actUnsupported {
		warn("pkill", "%s is not supported on Windows: %s", signalLabel(sig),
			"use TERM/INT/KILL/STOP/CONT or 0")
		return 1
	}
	if !sel.compilePattern("pkill") {
		return 2
	}
	procs, err := snapshotProcesses()
	if err != nil {
		warn("pkill", "%v", err)
		return 1
	}
	matches := sel.selectProcs(procs)
	if len(matches) == 0 {
		return 1
	}
	status := 0
	done := 0
	for _, p := range matches {
		if sel.dryRun {
			fmt.Printf("would signal %d %s\n", p.pid, procComm(p.name))
			done++
			continue
		}
		if sel.quiet || sig.action == actCheck {
			done++
			continue
		}
		if err := killSend(p.pid, sig.action, false, !sel.gracefulOnly); err != nil {
			warn("pkill", "%d (%s): %v", p.pid, procComm(p.name), err)
			status = 1
			continue
		}
		done++
	}
	if sel.count {
		fmt.Println(done)
	}
	return status
}
