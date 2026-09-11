package core

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func init() {
	addCmd("cd", "print or resolve a directory (shell function performs the change)", cmdCd)
	addCmd("clear", "clear the terminal screen", cmdClear)
	addCmd("date", "print the system date and time", cmdDate)
	addCmd("echo", "display a line of text", cmdEcho)
	addCmd("hostname", "show the system host name", cmdHostname)
	addCmd("id", "print user identity (Windows approximation)", cmdID)
	addCmd("pwd", "print the current working directory", cmdPwd)
	addCmd("su", "run a command as another user, or get an elevated shell", cmdSu)
	addCmd("sudo", "run a command with administrator rights (UAC)", cmdSudo)
	addCmd("uname", "print system information", cmdUname)
	addCmd("which", "locate a command", cmdWhich)
	addCmd("whoami", "print the effective user name", cmdWhoami)
}

// --------------------------------------------------------------- cd ---------

// A separate process cannot change the working directory of the shell that
// launched it, so `winux cd` is the resolver half of the `cd` command:
//   - with no arguments it prints the current working directory;
//   - with an argument it resolves the target (cross-drive, `~`, `-`, `/`,
//     forward or back slashes), validates that it exists and is a directory,
//     prints the absolute path with back slashes and exits 0; otherwise it
//     prints an error to stderr and exits 1.
// The shell layer (the PowerShell `cd` function in activate.ps1 and the cmd.exe
// doskey macro in activate.cmd) then performs the actual location change, which
// is the only way an external `cd` can work on Windows.
//
// `cd -` (previous directory) works everywhere because winux keeps the last
// visited directory in its own state file (see readOldPwd/writeOldPwd): the
// direct exe, the cmd.exe macro and the sh builtin all read it, so the feature
// no longer depends on a shell-specific environment variable.
func cmdCd(args []string) int {
	f := NewFlags()
	if !parseOrDie("cd", f, args) {
		return 2
	}
	if handleHelpVersion("cd", "[DIR]", f) {
		return 0
	}
	op := f.Positional
	if len(op) == 0 {
		dir, err := os.Getwd()
		if err != nil {
			warn("cd", "%v", err)
			return 1
		}
		fmt.Println(dir)
		return 0
	}
	target := op[0]
	resolved, err := ResolveCdTarget(target)
	if err != nil {
		warn("cd", "%v", err)
		return 1
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.IsDir() {
		warn("cd", "%s: No such file or directory", target)
		return 1
	}
	// Every successful change records where we are now as the previous
	// directory, so the next `cd -` (from any entry point) can come back.
	if cur, err := os.Getwd(); err == nil {
		writeOldPwd(cur)
	}
	fmt.Println(resolved)
	// A direct `winux.exe cd DIR` cannot change the parent shell's directory
	// (no child process can). When this session is not activated, the printed
	// path looks like a successful cd, so say plainly what happened and how to
	// get the real thing. The stderr hint never pollutes the stdout captured by
	// the activate.ps1 cd function (which runs with WINUX_ACTIVE set).
	if os.Getenv("WINUX_ACTIVE") == "" {
		fmt.Fprintln(os.Stderr, "提示: 子进程无法改变所在 shell 的工作目录, 这里只打印了目标路径。")
		fmt.Fprintln(os.Stderr, "请先激活 winux 再直接使用 cd: PowerShell 执行 winux activate | iex, cmd 执行 for /f \"delims=\" %l in ('winux activate') do @%l,")
		fmt.Fprintln(os.Stderr, "之后 cd 命令的跨盘切换、cd -、cd ~、cd / 才会真正生效。")
	}
	return 0
}

// ----------------------------------------------------------- previous dir ---

// oldPwdPath returns the per-user state file that records the last directory
// visited by any winux cd (the bash $OLDPWD equivalent). It is a plain file so
// the PowerShell hook, the cmd.exe macro and the sh builtin share one source
// of truth; WINUX_OLDPWD_FILE overrides it (used by tests and smoke).
func oldPwdPath() string {
	if p := os.Getenv("WINUX_OLDPWD_FILE"); p != "" {
		return p
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		if c, err := os.UserConfigDir(); err == nil {
			base = c
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "winux", "oldpwd")
}

func readOldPwd() string {
	b, err := os.ReadFile(oldPwdPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeOldPwd(p string) {
	if p == "" {
		return
	}
	f := oldPwdPath()
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(p), 0o600)
}

// shRunCd is the `cd` builtin of winux sh: unlike the resolver it really
// changes the sh process directory (bash semantics: silent on success, prints
// the target for `cd -`, and `cd` alone goes to $HOME). It writes the same
// persistent OLDPWD file, so `cd -` inside a script toggles correctly.
func shRunCd(c shCmd, ctx *shCtx) int {
	target := "~"
	if len(c.args) > 0 {
		target = c.args[0]
	}
	resolved, err := ResolveCdTarget(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cd: %v\n", err)
		return 1
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "cd: %s: No such file or directory\n", target)
		return 1
	}
	if cur, err := os.Getwd(); err == nil {
		writeOldPwd(cur)
	}
	if err := os.Chdir(resolved); err != nil {
		fmt.Fprintf(os.Stderr, "cd: %v\n", err)
		return 1
	}
	if target == "-" {
		fmt.Println(resolved)
	}
	return 0
}

// ResolveCdTarget turns a cd argument into an absolute Windows path (back
// slashes). It understands `~`, `~/...`, `-` (the previous directory: the
// winux state file first, then the WINUX_PREVDIR environment variable set by
// the PowerShell hook), `/` (the root of the current drive) and a bare drive
// letter such as `D:` (mapped to the drive root, since an external process
// cannot know the drive's current directory).
func ResolveCdTarget(target string) (string, error) {
	switch {
	case target == "~":
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot locate the home directory: %v", err)
		}
		return h, nil
	case target == "-":
		if prev := readOldPwd(); prev != "" {
			return prev, nil
		}
		prev := os.Getenv("WINUX_PREVDIR")
		if prev == "" {
			return "", fmt.Errorf("OLDPWD not set")
		}
		return prev, nil
	case strings.HasPrefix(target, "~/"):
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot locate the home directory: %v", err)
		}
		return filepath.Join(h, target[2:]), nil
	case strings.HasPrefix(target, `~\`):
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot locate the home directory: %v", err)
		}
		return filepath.Join(h, target[2:]), nil
	case target == "/" || target == `\`:
		// cmd.exe convention: a bare separator means the root of the current
		// drive, exactly like `/` in a POSIX shell.
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.VolumeName(wd) + `\`, nil
	}
	if len(target) == 2 && target[1] == ':' && isAlpha(target[0]) {
		// a bare drive letter: switch to its root.
		return strings.ToUpper(target[:1]) + `:\`, nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// --------------------------------------------------------------- clear -----

func cmdClear(args []string) int {
	f := NewFlags()
	if !parseOrDie("clear", f, args) {
		return 2
	}
	if handleHelpVersion("clear", "", f) {
		return 0
	}
	if !isTerminal(os.Stdout) {
		return 0
	}
	fmt.Print("\x1b[2J\x1b[3J\x1b[H")
	return 0
}

// ---------------------------------------------------------------- echo -----

func cmdEcho(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "--help":
			fmt.Println("Usage: echo [OPTION]... [STRING]...")
			fmt.Println("Options:")
			fmt.Println("  -n          do not output the trailing newline")
			fmt.Println("  -e          enable interpretation of backslash escapes")
			fmt.Println("  -E          disable interpretation of backslash escapes (default)")
			fmt.Println()
			fmt.Println("Escapes: \\\\ \\a \\b \\c \\e \\f \\n \\r \\t \\v \\0NNN \\xHH")
			return 0
		case "--version":
			printVersion("echo")
			return 0
		}
	}
	noNewline := false
	escapes := false
	i := 0
scan:
	for ; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			break
		}
		for _, c := range a[1:] {
			switch c {
			case 'n':
				noNewline = true
			case 'e':
				escapes = true
			case 'E':
				escapes = false
			default:
				break scan
			}
		}
	}
	text := strings.Join(args[i:], " ")
	out := text
	if escapes {
		var stop bool
		out, stop = interpretEscapes(text)
		if stop {
			noNewline = true
		}
	}
	fmt.Print(out)
	if !noNewline {
		fmt.Print("\n")
	}
	return 0
}

// interpretEscapes implements the backslash escapes of `echo -e`.
func interpretEscapes(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'c':
			return b.String(), true
		case 'e', 'E':
			b.WriteByte(27)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case '\\':
			b.WriteByte('\\')
		case '0':
			val := 0
			n := 0
			for n < 3 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
				i++
				val = val*8 + int(s[i]-'0')
				n++
			}
			b.WriteByte(byte(val))
		case 'x':
			val := 0
			n := 0
			for n < 2 && i+1 < len(s) && isHexDigit(s[i+1]) {
				i++
				val = val*16 + hexVal(s[i])
				n++
			}
			if n == 0 {
				b.WriteString("\\x")
			} else {
				b.WriteByte(byte(val))
			}
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String(), false
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

// ----------------------------------------------------------------- pwd -----

func cmdPwd(args []string) int {
	f := NewFlags(
		&Flag{Name: "physical", Short: 'P', Long: "physical", Help: "avoid all symbolic links"},
		&Flag{Name: "logical", Short: 'L', Long: "logical", Help: "use PWD from the environment (default)"},
	)
	if !parseOrDie("pwd", f, args) {
		return 2
	}
	if handleHelpVersion("pwd", "", f) {
		return 0
	}
	dir, err := os.Getwd()
	if err != nil {
		warn("pwd", "%v", err)
		return 1
	}
	if f.Has("physical") {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
	}
	fmt.Println(dir)
	return 0
}

// ------------------------------------------------------------- whoami/id ---

func currentUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, key := range []string{"USERNAME", "USER"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return "unknown"
}

func shortUserName(name string) string {
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		return name[i+1:]
	}
	return name
}

func cmdWhoami(args []string) int {
	f := NewFlags()
	if !parseOrDie("whoami", f, args) {
		return 2
	}
	if handleHelpVersion("whoami", "", f) {
		return 0
	}
	fmt.Println(shortUserName(currentUserName()))
	return 0
}

// cmdID reports a POSIX-shaped identity. Windows has no uid/gid, so winux maps
// the current account onto the conventional 1000/1000 pair.
func cmdID(args []string) int {
	f := NewFlags(
		&Flag{Name: "user", Short: 'u', Long: "user", Help: "print only the effective user ID"},
		&Flag{Name: "group", Short: 'g', Long: "group", Help: "print only the effective group ID"},
		&Flag{Name: "groups", Short: 'G', Long: "groups", Help: "print all group IDs"},
		&Flag{Name: "name", Short: 'n', Long: "name", Help: "print a name instead of a number"},
	)
	if !parseOrDie("id", f, args) {
		return 2
	}
	if handleHelpVersion("id", "[OPTION]... [USER]", f) {
		return 0
	}
	name := shortUserName(currentUserName())
	const uid, gid = "1000", "1000"
	switch {
	case f.Has("user"):
		if f.Has("name") {
			fmt.Println(name)
		} else {
			fmt.Println(uid)
		}
	case f.Has("group"):
		if f.Has("name") {
			fmt.Println(name)
		} else {
			fmt.Println(gid)
		}
	case f.Has("groups"):
		if f.Has("name") {
			fmt.Println(name)
		} else {
			fmt.Println(gid)
		}
	default:
		fmt.Printf("uid=%s(%s) gid=%s(%s) groups=%s(%s)\n", uid, name, gid, name, gid, name)
	}
	return 0
}

// --------------------------------------------------------------- uname -----

func unameArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "i686"
	case "arm64":
		return "aarch64"
	case "arm":
		return "armv7l"
	}
	return runtime.GOARCH
}

func cmdUname(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "print all information"},
		&Flag{Name: "kernel-name", Short: 's', Long: "kernel-name", Help: "print the kernel name"},
		&Flag{Name: "nodename", Short: 'n', Long: "nodename", Help: "print the network node hostname"},
		&Flag{Name: "kernel-release", Short: 'r', Long: "kernel-release", Help: "print the kernel release"},
		&Flag{Name: "kernel-version", Short: 'v', Long: "kernel-version", Help: "print the kernel version"},
		&Flag{Name: "machine", Short: 'm', Long: "machine", Help: "print the machine hardware name"},
		&Flag{Name: "processor", Short: 'p', Long: "processor", Help: "print the processor type"},
		&Flag{Name: "operating-system", Short: 'o', Long: "operating-system", Help: "print the operating system"},
	)
	if !parseOrDie("uname", f, args) {
		return 2
	}
	if handleHelpVersion("uname", "[OPTION]...", f) {
		return 0
	}
	hostname, _ := os.Hostname()
	var parts []string
	any := f.Has("all") || f.Has("kernel-name") || f.Has("nodename") || f.Has("kernel-release") ||
		f.Has("kernel-version") || f.Has("machine") || f.Has("processor") || f.Has("operating-system")
	switch {
	case f.Has("all"):
		parts = []string{"Windows", hostname, osVersion(), unameArch(), "winux"}
	default:
		if !any || f.Has("kernel-name") {
			parts = append(parts, "Windows")
		}
		if f.Has("nodename") {
			parts = append(parts, hostname)
		}
		if f.Has("kernel-release") {
			parts = append(parts, osVersion())
		}
		if f.Has("kernel-version") {
			parts = append(parts, "winux "+Version)
		}
		if f.Has("machine") {
			parts = append(parts, unameArch())
		}
		if f.Has("processor") {
			parts = append(parts, unameArch())
		}
		if f.Has("operating-system") {
			parts = append(parts, "winux")
		}
	}
	fmt.Println(strings.Join(parts, " "))
	return 0
}

// ------------------------------------------------------------ hostname -----

func cmdHostname(args []string) int {
	f := NewFlags(
		&Flag{Name: "short", Short: 's', Long: "short", Help: "print the short host name"},
		&Flag{Name: "domain", Short: 'd', Long: "domain", Help: "print the DNS domain name"},
		&Flag{Name: "fqdn", Short: 'f', Long: "fqdn", Help: "print the fully qualified domain name"},
		&Flag{Name: "ip-address", Short: 'i', Long: "ip-address", Help: "print the IP address of the host"},
	)
	if !parseOrDie("hostname", f, args) {
		return 2
	}
	if handleHelpVersion("hostname", "[NAME]", f) {
		return 0
	}
	host, err := os.Hostname()
	if err != nil {
		warn("hostname", "%v", err)
		return 1
	}
	if len(f.Positional) > 0 {
		if strings.EqualFold(f.Positional[0], host) {
			return 0
		}
		warn("hostname", "setting the host name requires elevated rights and is not supported by winux")
		return 1
	}
	if f.Has("short") {
		if i := strings.IndexByte(host, '.'); i > 0 {
			host = host[:i]
		}
	}
	if f.Has("domain") || f.Has("fqdn") {
		if i := strings.IndexByte(host, '.'); i >= 0 {
			fmt.Println(host[i+1:])
		} else {
			fmt.Println("(none)")
		}
		return 0
	}
	fmt.Println(host)
	return 0
}

// ---------------------------------------------------------------- date -----

func cmdDate(args []string) int {
	f := NewFlags(
		&Flag{Name: "date", Short: 'd', Long: "date", HasArg: true, ArgName: "STRING", Help: "display the given date instead of now"},
		&Flag{Name: "reference", Short: 'r', Long: "reference", HasArg: true, ArgName: "FILE", Help: "display the last modification time of FILE"},
		&Flag{Name: "utc", Short: 'u', Long: "utc", Help: "print Coordinated Universal Time"},
		&Flag{Name: "rfc-email", Short: 'R', Long: "rfc-email", Help: "output RFC 5322 date format"},
		&Flag{Name: "iso-8601", Short: 'I', Long: "iso-8601", OptArg: true, ArgName: "[=TIMESPEC]", Help: "output ISO 8601 format"},
	)
	if !parseOrDie("date", f, args) {
		return 2
	}
	if handleHelpVersion("date", "[OPTION]... [+FORMAT]", f) {
		return 0
	}
	now := time.Now()
	if f.Has("utc") {
		now = now.UTC()
	}
	if d := f.Str("date"); d != "" {
		t, err := parseDate(d)
		if err != nil {
			warn("date", "%v", err)
			return 1
		}
		now = t
	}
	if r := f.Str("reference"); r != "" {
		st, err := os.Stat(r)
		if err != nil {
			warn("date", "%s: No such file or directory", r)
			return 1
		}
		now = st.ModTime()
	}
	format := ""
	for _, a := range f.Positional {
		if strings.HasPrefix(a, "+") {
			format = a[1:]
		}
	}
	switch {
	case f.Has("rfc-email"):
		fmt.Println(now.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
		return 0
	case f.Has("iso-8601"):
		fmt.Println(now.Format("2006-01-02T15:04:05-07:00"))
		return 0
	case format == "":
		format = "%a %b %e %H:%M:%S %Z %Y"
	}
	zone := now.Format("MST")
	if zone == "" || zone == "UTC" && !f.Has("utc") && now.Location() == time.Local {
		zone = now.Format("-0700")
	}
	out := strftime(now, format)
	out = strings.ReplaceAll(out, "MST", zone)
	fmt.Println(out)
	return 0
}

// ---------------------------------------------------------------- sudo -----

func cmdSudo(args []string) int {
	f := NewFlags(
		&Flag{Name: "login", Short: 'i', Long: "login", Help: "run an elevated interactive shell"},
		&Flag{Name: "shell", Short: 's', Long: "shell", Help: "run an elevated shell"},
		&Flag{Name: "user", Short: 'u', Long: "user", HasArg: true, ArgName: "USER", Help: "not supported: elevation always uses the Administrator token"},
		&Flag{Name: "preserve-env", Short: 'E', Long: "preserve-env", Help: "preserve the environment (always on)"},
		&Flag{Name: "non-interactive", Short: 'n', Long: "non-interactive", Help: "fail instead of asking for UAC consent"},
	)
	if !parseOrDie("sudo", f, args) {
		return 2
	}
	if handleHelpVersion("sudo", "[OPTION]... COMMAND [ARG]...", f) {
		return 0
	}
	if u := f.Str("user"); u != "" {
		warn("sudo", "-u is not supported on Windows: an elevated process always runs with the Administrator token")
		return 1
	}
	if f.Has("non-interactive") {
		warn("sudo", "-n is not supported: Windows always shows a UAC consent dialog")
		return 1
	}
	if len(f.Positional) == 0 || f.Has("login") || f.Has("shell") {
		fmt.Fprintln(os.Stderr, "sudo: opening an elevated shell (UAC consent required)")
		return elevateAndWait(nil, true)
	}
	return elevateAndWait(f.Positional, false)
}

// elevateAndWait re-runs the given winux command line (or cmd.exe) through
// PowerShell's Start-Process -Verb RunAs, which triggers the UAC prompt, and
// waits for the elevated process to finish.
func elevateAndWait(args []string, shell bool) int {
	file := os.Getenv("COMSPEC")
	if file == "" {
		file = "cmd.exe"
	}
	var argList []string
	if !shell {
		self, err := os.Executable()
		if err != nil {
			warn("sudo", "cannot locate the running executable: %v", err)
			return 1
		}
		file, argList = self, args
	}
	ps := buildElevateScript(file, argList)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", ps)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		warn("sudo", "elevation failed: %v (was the UAC prompt declined?)", err)
		return 1
	}
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code != 0 {
			return code
		}
	}
	return 0
}

func buildElevateScript(file string, args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "''")+"'")
	}
	list := strings.Join(quoted, ",")
	if list == "" {
		list = "@()"
	}
	return fmt.Sprintf(
		"$ErrorActionPreference='Stop'; try { $p = Start-Process -FilePath '%s' -ArgumentList %s -Verb RunAs -Wait -PassThru; exit $p.ExitCode } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }",
		strings.ReplaceAll(file, "'", "''"), list)
}

// ------------------------------------------------------------------ su -----

func cmdSu(args []string) int {
	f := NewFlags(
		&Flag{Name: "command", Short: 'c', Long: "command", HasArg: true, ArgName: "COMMAND", Help: "pass COMMAND to the shell"},
		&Flag{Name: "login", Short: 'l', Long: "login", Help: "start a login shell (elevated Administrator shell on Windows)"},
		&Flag{Name: "shell", Short: 's', Long: "shell", HasArg: true, ArgName: "SHELL", Help: "accepted and ignored: cmd.exe is used"},
	)
	if !parseOrDie("su", f, args) {
		return 2
	}
	if handleHelpVersion("su", "[-] [USER [ARGUMENT]...]", f) {
		return 0
	}
	op := f.Positional
	login := f.Has("login")
	if len(op) > 0 && op[0] == "-" {
		login = true
		op = op[1:]
	}
	if cmdStr := f.Str("command"); cmdStr != "" {
		if len(op) > 0 && !strings.EqualFold(op[0], "root") && !strings.EqualFold(op[0], "administrator") {
			return suRunAs(op[0], cmdStr)
		}
		return runViaCmd(cmdStr)
	}
	if len(op) == 0 || login || strings.EqualFold(op[0], "root") || strings.EqualFold(op[0], "administrator") {
		fmt.Fprintln(os.Stderr, "su: Windows has no root account; opening an elevated Administrator shell (UAC consent required)")
		return elevateAndWait(nil, true)
	}
	username := op[0]
	rest := op[1:]
	if len(rest) == 0 {
		return suRunAs(username, "")
	}
	return suRunAs(username, strings.Join(rest, " "))
}

// suRunAs uses the built-in runas.exe, which prompts for the target account's
// password on the console.
func suRunAs(username, cmdline string) int {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	if cmdline == "" {
		cmdline = shell
	}
	cmd := exec.Command("runas.exe", "/user:"+username, cmdline)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		warn("su", "runas /user:%s failed: %v", username, err)
		return 1
	}
	return 0
}

func runViaCmd(command string) int {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell, "/d", "/s", "/c", command)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		warn("su", "%v", err)
		return 1
	}
	return 0
}

// --------------------------------------------------------------- which -----

func cmdWhich(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "print all matching path names, not just the first"},
	)
	if !parseOrDie("which", f, args) {
		return 2
	}
	if handleHelpVersion("which", "[OPTION]... COMMAND...", f) {
		return 0
	}
	if len(f.Positional) == 0 {
		warn("which", "missing command name")
		return 1
	}
	exit := 0
	for _, name := range f.Positional {
		matches := lookPathAll(name)
		if len(matches) == 0 {
			exit = 1
			continue
		}
		if f.Has("all") {
			for _, m := range matches {
				fmt.Println(m)
			}
			continue
		}
		fmt.Println(matches[0])
	}
	return exit
}

func lookPathAll(name string) []string {
	var out []string
	exts := []string{""}
	if filepath.Ext(name) == "" {
		exts = []string{""}
		for _, e := range filepath.SplitList(os.Getenv("PATHEXT")) {
			if e != "" {
				exts = append(exts, strings.ToLower(e))
			}
		}
		for _, e := range []string{".ps1"} {
			exts = append(exts, e)
		}
	}
	if strings.ContainsAny(name, `/\`) {
		if fileExists(name) && !isDir(name) {
			return []string{name}
		}
		return nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			cand := filepath.Join(dir, name+ext)
			if fileExists(cand) && !isDir(cand) {
				out = append(out, cand)
				break
			}
		}
	}
	return out
}
