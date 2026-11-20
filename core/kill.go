package core

// kill delivers a "signal" to running processes. Windows has no POSIX signals,
// so this command maps the ones it can honour onto real Windows mechanisms and
// refuses the rest instead of pretending they worked:
//
//	TERM INT QUIT HUP  ask the process to exit (GUI: WM_CLOSE via taskkill,
//	                   console process in our own console: Ctrl+Break)
//	KILL               terminate it outright (TerminateProcess / taskkill /F)
//	STOP TSTP          suspend it (NtSuspendProcess)
//	CONT               resume it (NtResumeProcess)
//	0                  only check that it exists
//
// The syntax is GNU's: `kill -9 PID`, `kill -s TERM PID`, `kill -KILL PID`,
// `kill -l [SIG]`, `kill -L`, `--` before negative process ids. Two additions
// exist because Windows needs them: `-t/--tree` also terminates the process's
// descendants, and a negative pid (POSIX process group) means "the tree rooted
// at this pid" — Windows has no POSIX process groups to address.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func init() {
	addCmd("kill", "send a signal to processes (POSIX names mapped onto Windows mechanisms)", cmdKill)
}

// killAction is what a signal means on this platform.
type killAction int

const (
	actCheck killAction = iota // signal 0: existence only
	actGraceful
	actForce
	actSuspend
	actResume
	actUnsupported
)

// signalDef describes one POSIX signal and what winux does with it.
type signalDef struct {
	num    int
	name   string // canonical name without the SIG prefix
	action killAction
	note   string // how it is carried out on Windows
}

// signalTable covers the Linux x86 numbering, which is what every Linux user
// expects to type. Signals outside it are rejected with an explanation.
var signalTable = []signalDef{
	{1, "HUP", actGraceful, "ask the process to exit (same as TERM here)"},
	{2, "INT", actGraceful, "Ctrl+Break for a console process in this console, else ask to exit"},
	{3, "QUIT", actGraceful, "same as INT; no core dump on Windows"},
	{4, "ILL", actUnsupported, ""},
	{5, "TRAP", actUnsupported, ""},
	{6, "ABRT", actForce, "terminate outright (there is no abort-with-coredump)"},
	{7, "BUS", actUnsupported, ""},
	{8, "FPE", actUnsupported, ""},
	{9, "KILL", actForce, "TerminateProcess / taskkill /F"},
	{10, "USR1", actUnsupported, ""},
	{11, "SEGV", actUnsupported, ""},
	{12, "USR2", actUnsupported, ""},
	{13, "PIPE", actUnsupported, ""},
	{14, "ALRM", actUnsupported, ""},
	{15, "TERM", actGraceful, "ask the process to exit (GUI window: WM_CLOSE)"},
	{16, "STKFLT", actUnsupported, ""},
	{17, "CHLD", actUnsupported, ""},
	{18, "CONT", actResume, "resume a suspended process (NtResumeProcess)"},
	{19, "STOP", actSuspend, "suspend the process (NtSuspendProcess)"},
	{20, "TSTP", actSuspend, "same as STOP (no tty stop on Windows)"},
	{21, "TTIN", actUnsupported, ""},
	{22, "TTOU", actUnsupported, ""},
	{23, "URG", actUnsupported, ""},
	{24, "XCPU", actUnsupported, ""},
	{25, "XFSZ", actUnsupported, ""},
	{26, "VTALRM", actUnsupported, ""},
	{27, "PROF", actUnsupported, ""},
	{28, "WINCH", actUnsupported, ""},
	{29, "IO", actUnsupported, ""},
	{30, "PWR", actUnsupported, ""},
	{31, "SYS", actUnsupported, ""},
}

func defaultSignal() signalDef {
	for _, s := range signalTable {
		if s.name == "TERM" {
			return s
		}
	}
	return signalTable[0]
}

// lookupSignal accepts what GNU kill accepts: a number ("9"), a bare name
// ("KILL"), or the SIG-prefixed name ("SIGKILL"), case-insensitively.
func lookupSignal(spec string) (signalDef, bool) {
	s := strings.ToUpper(strings.TrimSpace(spec))
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "SIG")
	if n, err := strconv.Atoi(s); err == nil {
		if n == 0 {
			return signalDef{num: 0, name: "0", action: actCheck, note: "check existence only"}, true
		}
		for _, d := range signalTable {
			if d.num == n {
				return d, true
			}
		}
		return signalDef{num: n, action: actUnsupported}, true // unknown POSIX number
	}
	for _, d := range signalTable {
		if d.name == s {
			return d, true
		}
	}
	return signalDef{}, false
}

// signalLabel is the name used in messages: the canonical name when known.
func signalLabel(s signalDef) string {
	if s.name != "" {
		return "SIG" + s.name
	}
	return "signal " + strconv.Itoa(s.num)
}

func cmdKill(args []string) int {
	sig := defaultSignal()
	checkOnly, tree, gracefulOnly := false, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return killRun(sig, checkOnly, tree, gracefulOnly, args[i+1:])
		case a == "-h" || a == "--help":
			killHelp()
			return 0
		case a == "--version" || a == "-V":
			printVersion("kill")
			return 0
		case a == "-l" || a == "--list":
			// GNU: -l takes an OPTIONAL signal; a following non-option word is it
			arg := ""
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				arg = args[i+1]
				i++
			}
			return killList(arg)
		case a == "-L" || a == "--table":
			return killTable()
		case a == "-0":
			checkOnly = true
			sig = signalDef{num: 0, name: "0", action: actCheck, note: "check existence only"}
		case a == "-t" || a == "--tree":
			tree = true
		case a == "--graceful-only":
			gracefulOnly = true
		case a == "-s" || a == "--signal":
			if i+1 >= len(args) {
				warn("kill", "option '%s' requires an argument", a)
				return 2
			}
			i++
			d, ok := lookupSignal(args[i])
			if !ok {
				warn("kill", "%s: invalid signal specification", args[i])
				return 2
			}
			sig = d
		case strings.HasPrefix(a, "--signal="):
			d, ok := lookupSignal(a[len("--signal="):])
			if !ok {
				warn("kill", "%s: invalid signal specification", a[len("--signal="):])
				return 2
			}
			sig = d
		case len(a) > 1 && a[0] == '-':
			// the classic forms: -9, -KILL, -SIGKILL
			d, ok := lookupSignal(a)
			if !ok {
				warn("kill", "unknown option '%s'", a)
				fmt.Fprintln(os.Stderr, "Try 'kill --help' for more information.")
				return 2
			}
			sig = d
		default:
			// first operand: everything from here on is a pid
			return killRun(sig, checkOnly, tree, gracefulOnly, args[i:])
		}
	}
	warn("kill", "missing pid operand")
	fmt.Fprintln(os.Stderr, "Try 'kill --help' for more information.")
	return 2
}

// killRun applies the signal to every operand and reports each failure.
func killRun(sig signalDef, checkOnly, tree, gracefulOnly bool, pids []string) int {
	if len(pids) == 0 {
		warn("kill", "missing pid operand")
		fmt.Fprintln(os.Stderr, "Try 'kill --help' for more information.")
		return 2
	}
	if checkOnly {
		sig = signalDef{num: 0, name: "0", action: actCheck}
	}
	if sig.action == actUnsupported {
		warn("kill", "%s is not supported on Windows: %s", signalLabel(sig),
			"only TERM/INT/QUIT/HUP (exit), KILL (force), STOP/TSTP (suspend), CONT (resume) and 0 (check) have a meaning here")
		return 1
	}
	status := 0
	for _, raw := range pids {
		pid, group, err := parseKillPid(raw)
		if err != nil {
			warn("kill", "%s: arguments must be process ids", raw)
			status = 1
			continue
		}
		killTree := tree || group
		if err := killSend(pid, sig.action, killTree, !gracefulOnly); err != nil {
			warn("kill", "%s: %v", raw, err)
			status = 1
		}
	}
	return status
}

// parseKillPid converts an operand. A negative value is a POSIX process group;
// Windows has none, so it is taken as "the tree rooted at this pid".
func parseKillPid(raw string) (pid int, group bool, err error) {
	pid, err = strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || pid == 0 {
		return 0, false, fmt.Errorf("bad pid")
	}
	if pid < 0 {
		return -pid, true, nil
	}
	return pid, false, nil
}

func killList(arg string) int {
	if arg != "" {
		d, ok := lookupSignal(arg)
		if !ok {
			warn("kill", "%s: invalid signal specification", arg)
			return 1
		}
		if d.name == "" {
			fmt.Printf("%d\n", d.num)
			return 0
		}
		// GNU semantics: a name prints the number, a number prints the name
		if _, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(arg), "SIG"))); err == nil {
			fmt.Printf("%s\n", d.name)
			return 0
		}
		fmt.Printf("%d\n", d.num)
		return 0
	}
	const perLine = 4
	for i, d := range signalTable {
		fmt.Printf("%2d) SIG%-8s", d.num, d.name)
		if (i+1)%perLine == 0 || i == len(signalTable)-1 {
			fmt.Println()
		}
	}
	fmt.Println()
	fmt.Println("Windows note: only the signals with a mapping work; `kill -L` shows it.")
	return 0
}

func killTable() int {
	fmt.Printf("%-5s %-12s %s\n", "NUM", "SIGNAL", "WHAT WINUX DOES ON WINDOWS")
	for _, d := range signalTable {
		what := d.note
		if d.action == actUnsupported {
			what = "not supported (no equivalent mechanism)"
		}
		fmt.Printf("%-5d %-12s %s\n", d.num, "SIG"+d.name, what)
	}
	return 0
}

// killFlags is only used to render the options block of --help; kill parses the
// GNU signal spellings itself (-9, -KILL), which a generic flag parser cannot
// express.
func killFlags() *Flags {
	f := NewFlags(
		&Flag{Name: "signal", Short: 's', Long: "signal", HasArg: true, ArgName: "SIGNAL", Help: "signal to send: a name (TERM, KILL) or a number (15, 9); the default is TERM"},
		&Flag{Name: "list", Short: 'l', Long: "list", OptArg: true, ArgName: "[SIGNAL]", Help: "list the signal names, or print the name/number of SIGNAL"},
		&Flag{Name: "table", Short: 'L', Long: "table", Help: "print the signal table with its Windows meaning"},
		&Flag{Name: "tree", Short: 't', Long: "tree", Help: "also terminate the target's descendants (taskkill /T)"},
		&Flag{Name: "graceful-only", Long: "graceful-only", Help: "fail instead of forcing when the process cannot be asked to exit (strict POSIX TERM)"},
	)
	return f
}

func killHelp() {
	fmt.Println("Usage: kill [-SIGNAL] [-s SIGNAL] [-t] PID...")
	fmt.Println("       kill -l [SIGNAL]")
	fmt.Println("       kill -L")
	printFlagList(killFlags())
	fmt.Println()
	fmt.Println("Signals: -SIGNAL, -s SIGNAL, --signal SIGNAL (TERM by default); -0 only checks")
	fmt.Println("         whether the process exists. Negative pids are process groups in POSIX;")
	fmt.Println("         on Windows they mean 'the process tree rooted at that pid'.")
	fmt.Println("Examples:")
	fmt.Println("  kill 1234             ask 1234 to exit")
	fmt.Println("  kill -9 1234          terminate 1234 outright")
	fmt.Println("  kill -STOP 1234       suspend it          kill -CONT 1234   resume it")
	fmt.Println("  kill -0 1234          print nothing, exit 0 if 1234 exists, 1 if not")
	fmt.Println("  kill -l KILL          print the number of SIGKILL (9)")
	fmt.Println()
	fmt.Println("TERM asks a GUI window to close (WM_CLOSE). Windows cannot ask a console")
	fmt.Println("process to exit, so when it has not exited after 3 seconds winux terminates")
	fmt.Println("it forcibly; --graceful-only turns that into an error instead. KILL never asks.")
}
