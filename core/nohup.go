package core

// nohup runs a command immune to hangups: the child is started in the
// background, detached from this console, with stdin on the null device and its
// output in a file, so it keeps running after the window is closed, the SSH
// session drops or the parent shell exits.
//
// On Linux the "hangup" is SIGHUP, sent to the foreground process group when the
// terminal goes away. Windows has no SIGHUP: closing a console delivers
// CTRL_CLOSE_EVENT to every process attached to it and then terminates them, so
// the equivalent of `nohup cmd &` is "start it without a console" (the platform
// layer, nohup_windows.go, does that with DETACHED_PROCESS + breakaway from the
// supervising job object).
//
// Options follow GNU nohup where GNU has them (the output file, the
// "ignoring input and appending output to" message, exit status 125/126/127),
// plus the knobs that a Windows session actually needs because there is no shell
// `&`, no `$!` and no redirection syntax to lean on: --output, --pid-file,
// --print-pid, --dir, --env and --wait.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	addCmd("nohup", "run a command immune to hangups, with output to a non-tty", cmdNohup)
}

// Exit statuses, matching GNU nohup: 125 for a bad invocation, 126 when the
// command exists but cannot be executed, 127 when it cannot be found.
const (
	nohupUsageExit = 125
	nohupExecExit  = 126
	nohupFoundExit = 127
)

func cmdNohup(args []string) int {
	f := NewFlags(
		&Flag{Name: "output", Short: 'o', Long: "output", HasArg: true, ArgName: "FILE",
			Help: "write stdout/stderr to FILE (default nohup.out when they are a console; '-' inherits them)"},
		&Flag{Name: "append", Short: 'a', Long: "append",
			Help: "append to FILE (the default, as in GNU nohup)"},
		&Flag{Name: "truncate", Short: 't', Long: "truncate",
			Help: "truncate FILE before writing"},
		&Flag{Name: "pid-file", Short: 'p', Long: "pid-file", HasArg: true, ArgName: "FILE",
			Help: "write the child's process id to FILE"},
		&Flag{Name: "print-pid", Short: 'P', Long: "print-pid",
			Help: "print the child's process id on stdout"},
		&Flag{Name: "dir", Short: 'd', Long: "dir", HasArg: true, ArgName: "DIR",
			Help: "run the command in DIR"},
		&Flag{Name: "env", Short: 'e', Long: "env", HasArg: true, ArgName: "NAME=VALUE",
			Help: "set an environment variable for the command (repeatable)"},
		&Flag{Name: "wait", Short: 'w', Long: "wait",
			Help: "wait for the command and exit with its status (do not detach)"},
		&Flag{Name: "ignore-stdin", Short: 'i', Long: "ignore-stdin",
			Help: "connect stdin to the null device even with --wait"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet",
			Help: "do not print the informational messages"},
	)
	// The first non-option word starts the command line: `nohup tail -f app.log`
	// must not treat -f as a nohup option.
	if !parseOrDieUntilCommand("nohup", f, args) {
		return 2
	}
	if handleHelpVersion("nohup", "COMMAND [ARG]...", f) {
		return 0
	}
	if len(f.Positional) == 0 {
		warn("nohup", "missing operand")
		fmt.Fprintln(os.Stderr, "Try 'nohup --help' for more information.")
		return nohupUsageExit
	}
	if f.Has("append") && f.Has("truncate") {
		warn("nohup", "cannot combine --append and --truncate")
		return nohupUsageExit
	}

	dir := f.Str("dir")
	if dir != "" {
		if !isDir(dir) {
			warn("nohup", "cannot change to '%s': No such directory", dir)
			return nohupUsageExit
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			warn("nohup", "cannot change to '%s': %v", dir, err)
			return nohupUsageExit
		}
		dir = abs
	}
	env, err := nohupEnv(f.Strs("env"))
	if err != nil {
		warn("nohup", "%v", err)
		return nohupUsageExit
	}

	name, cargs := f.Positional[0], f.Positional[1:]
	target, err := nohupResolve(name, cargs)
	if err != nil {
		warn("nohup", "failed to run command '%s': %v", name, err)
		if errors.Is(err, os.ErrNotExist) {
			return nohupFoundExit
		}
		return nohupExecExit
	}

	out, outName, err := nohupOpenOutput(f)
	if err != nil {
		warn("nohup", "cannot open '%s': %v", outName, err)
		return nohupExecExit
	}
	if out != nil {
		defer out.Close()
	}

	detach := !f.Has("wait")
	ignoreStdin := detach || f.Has("ignore-stdin")

	// newCmd builds a fresh command for every start attempt: a Cmd that failed
	// to start cannot be retried, and the platform layer may need a second try.
	newCmd := func() (*exec.Cmd, error) {
		c := exec.Command(target.exe, target.argv...)
		if target.cmdLine != "" {
			nohupCommandLine(c, target.cmdLine)
		}
		c.Dir = dir
		c.Env = env
		if out != nil {
			c.Stdout = out
			c.Stderr = out
		}
		if !ignoreStdin {
			c.Stdin = os.Stdin
		}
		return c, nil
	}
	cmd, err := nohupSpawn(newCmd, detach)
	if err != nil {
		warn("nohup", "failed to run command '%s': %v", name, err)
		return nohupExecExit
	}

	if !f.Has("quiet") {
		if outName != "" {
			verb := "appending output to"
			if f.Has("truncate") {
				verb = "redirecting output to"
			}
			if ignoreStdin {
				fmt.Fprintf(os.Stderr, "nohup: ignoring input and %s '%s'\n", verb, outName)
			} else {
				fmt.Fprintf(os.Stderr, "nohup: %s '%s'\n", verb, outName)
			}
		} else if ignoreStdin {
			fmt.Fprintln(os.Stderr, "nohup: ignoring input")
		}
		if detach {
			fmt.Fprintf(os.Stderr, "nohup: started '%s' (pid %d); it keeps running after this window closes\n",
				strings.Join(append([]string{name}, cargs...), " "), cmd.Process.Pid)
		}
	}
	if pf := f.Str("pid-file"); pf != "" {
		if err := os.WriteFile(pf, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
			warn("nohup", "cannot write pid file '%s': %v", pf, err)
		}
	}
	if f.Has("print-pid") {
		fmt.Println(cmd.Process.Pid)
	}

	if detach {
		// Hand the child to the OS and return; the handle itself is released by
		// Release so no finalizer waits on it.
		_ = cmd.Process.Release()
		return 0
	}
	err = cmd.Wait()
	return nohupExitStatus(cmd, err)
}

// nohupExitStatus turns the result of Wait into the child's exit status.
func nohupExitStatus(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
	}
	warn("nohup", "failed to run command '%s': %v", cmd.Path, err)
	return nohupExecExit
}

// nohupMissing reports a command that is not on PATH in GNU's wording while
// still answering errors.Is(err, os.ErrNotExist), which selects exit status 127.
// nohupTarget is a resolved command: exe+argv normally, or cmdLine when the
// platform has to be handed an exact command line (a batch file's quoting rules
// are cmd.exe's, not CommandLineToArgvW's, so letting Go quote the arguments
// would produce a line cmd.exe misreads).
type nohupTarget struct {
	exe     string
	argv    []string
	cmdLine string
}

type nohupMissing struct{ name string }

func (e *nohupMissing) Error() string { return "No such file or directory" }
func (e *nohupMissing) Unwrap() error { return os.ErrNotExist }

// nohupResolve maps COMMAND to something Start can run.
//
//   - a registered winux command is re-entered through this executable, so
//     `nohup tail -f app.log` and `nohup sh -c '...'` behave like the rest of the
//     toolbox instead of depending on PATH shims;
//   - anything else is looked up on PATH with PATHEXT (same order as `which`);
//   - batch files and PowerShell scripts cannot be started by CreateProcess, so
//     they are handed to cmd.exe / powershell.exe.
func nohupResolve(name string, args []string) (nohupTarget, error) {
	if name == "" {
		return nohupTarget{}, fmt.Errorf("empty command name")
	}
	// an explicit path (or a bare file in the working directory via .\x)
	if strings.ContainsAny(name, `/\`) {
		if !fileExists(name) || isDir(name) {
			return nohupTarget{}, &nohupMissing{name: name}
		}
		return nohupWrapScript(name, args)
	}
	// winux's own command names first: they are what this toolbox is for, and
	// the PATH shims around them are .cmd files we would otherwise have to
	// round-trip through cmd.exe.
	if _, ok := cmdTable[name]; ok {
		self, err := os.Executable()
		if err != nil {
			return nohupTarget{}, err
		}
		return nohupTarget{exe: self, argv: append([]string{name}, args...)}, nil
	}
	matches := lookPathAll(name)
	if len(matches) == 0 {
		return nohupTarget{}, &nohupMissing{name: name}
	}
	return nohupWrapScript(matches[0], args)
}

// nohupWrapScript picks the interpreter for a file CreateProcess cannot start
// itself. Everything else (.exe and friends, and any path with no known script
// extension) is passed through unchanged.
func nohupWrapScript(path string, args []string) (nohupTarget, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		// cmd /d /s /c <line>: with /s, cmd strips exactly one outer quote pair,
		// so the line is wrapped once more and survives intact.
		words := make([]string, 0, len(args)+1)
		// the script path is always quoted: it is a path, so quoting cannot
		// change its meaning, and it keeps a directory containing cmd
		// metacharacters (&, ^, parentheses) from splitting the line
		words = append(words, `"`+strings.ReplaceAll(path, `"`, `""`)+`"`)
		for _, a := range args {
			words = append(words, nohupCmdQuote(a))
		}
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = systemExe("cmd.exe")
		}
		line := `"` + comspec + `" /d /s /c "` + strings.Join(words, " ") + `"`
		return nohupTarget{exe: comspec, cmdLine: line}, nil
	case ".ps1":
		ps := systemExe("WindowsPowerShell\\v1.0\\powershell.exe")
		return nohupTarget{exe: ps, argv: append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}, args...)}, nil
	}
	return nohupTarget{exe: path, argv: args}, nil
}

// nohupCmdQuote quotes one word for a `cmd.exe /c` command line. cmd does not
// use the CommandLineToArgvW rules: a word only needs the outer quotes, and an
// embedded quote is written as "" (cmd's own convention).
func nohupCmdQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"&|<>^()%,;=") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// nohupOpenOutput decides where stdout/stderr go.
//
// With -o FILE the answer is explicit (append by default, --truncate to start
// over). Without it GNU nohup only redirects when stdout is a terminal, and
// Windows needs the same rule plus stderr: a child that survives the console has
// no console handles left, so output written to the old stderr would vanish.
// When nohup.out cannot be created in the working directory the file goes to the
// user's home, as GNU does.
func nohupOpenOutput(f *Flags) (*os.File, string, error) {
	want := f.Str("output")
	explicit := want != ""
	switch {
	case want == "-":
		return nil, "", nil // keep the inherited handles
	case want == "":
		if !isTerminal(os.Stdout) && !isTerminal(os.Stderr) {
			return nil, "", nil
		}
		want = "nohup.out"
	}
	flags := os.O_CREATE | os.O_WRONLY
	if f.Has("truncate") {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	fd, err := os.OpenFile(want, flags, 0o644)
	if err == nil {
		return fd, want, nil
	}
	if explicit {
		return nil, want, err
	}
	home, herr := os.UserHomeDir()
	if herr != nil || home == "" {
		return nil, want, err
	}
	alt := filepath.Join(home, "nohup.out")
	fd2, err2 := os.OpenFile(alt, flags, 0o644)
	if err2 != nil {
		return nil, want, err
	}
	return fd2, alt, nil
}

// nohupEnv merges NAME=VALUE overrides into the current environment, replacing
// an existing name case-insensitively (Windows environment names are).
func nohupEnv(pairs []string) ([]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	env := os.Environ()
	for _, p := range pairs {
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("invalid environment assignment '%s' (want NAME=VALUE)", p)
		}
		name, val := p[:eq], p[eq+1:]
		replaced := false
		for i, kv := range env {
			if j := strings.IndexByte(kv, '='); j > 0 && strings.EqualFold(kv[:j], name) {
				env[i] = name + "=" + val
				replaced = true
			}
		}
		if !replaced {
			env = append(env, name+"="+val)
		}
	}
	return env, nil
}
