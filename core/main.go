package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	_ "time/tzdata" // embed the IANA database so `date` and `touch -d` work offline
)

// Version and BuildTime are plain package variables so the build can stamp
// them with `go build -ldflags "-X winux/core.Version=... -X winux/core.BuildTime=..."`.
// build.ps1 injects the 8-char git commit id (falling back to 1.0.0 when there
// is no local git repository) and the build timestamp; a plain `go build`
// leaves the defaults.
var (
	Version   = "1.0.0"
	BuildTime = ""
	progName  = "winux"
)

type command struct {
	name    string
	summary string
	run     func([]string) int
}

var cmdTable = map[string]*command{}

// addCmd registers a command (plus optional aliases such as vim for vi).
func addCmd(name, summary string, run func([]string) int, aliases ...string) {
	c := &command{name: name, summary: summary, run: run}
	cmdTable[name] = c
	for _, a := range aliases {
		cmdTable[a] = c
	}
}

func Main() {
	initConsole()

	args := os.Args[1:]
	prog := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")

	// busybox style dispatch: a copy (or hard link) named cp.exe acts as `cp`,
	// which is what the generated shims and renamed binaries rely on.
	if c, ok := cmdTable[prog]; ok && prog != progName {
		os.Exit(c.run(args))
	}

	if len(args) == 0 {
		printGlobalHelp()
		os.Exit(0)
	}
	switch args[0] {
	case "--help", "-h", "help", "-?":
		if len(args) > 1 {
			if c, ok := cmdTable[args[1]]; ok {
				os.Exit(c.run([]string{"--help"}))
			}
			fmt.Fprintf(os.Stderr, "%s: unknown command '%s'\n", progName, args[1])
			os.Exit(1)
		}
		printGlobalHelp()
		os.Exit(0)
	case "--version", "-V", "version":
		if BuildTime != "" {
			fmt.Printf("%s %s (built %s)\n", progName, Version, BuildTime)
		} else {
			fmt.Printf("%s %s\n", progName, Version)
		}
		fmt.Printf("from: %s\n", exePath())
		os.Exit(0)
	}

	c, ok := cmdTable[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown command '%s'\n", progName, args[0])
		fmt.Fprintf(os.Stderr, "Try '%s --help' for the command list.\n", progName)
		os.Exit(1)
	}
	os.Exit(c.run(args[1:]))
}

func printGlobalHelp() {
	if BuildTime != "" {
		fmt.Printf("%s %s (built %s) - Linux-style commands for Windows (cmd.exe and PowerShell)\n", progName, Version, BuildTime)
	} else {
		fmt.Printf("%s %s - Linux-style commands for Windows (cmd.exe and PowerShell)\n", progName, Version)
	}
	fmt.Println("Native Go implementation: no WSL, no MSYS2/Cygwin, no Python runtime needed.")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Printf("  %s <command> [OPTION]... [ARGUMENT]...\n", progName)
	fmt.Println("  <command> [OPTION]... [ARGUMENT]...        via the generated .cmd shims or renamed winux.exe")
	fmt.Println()
	fmt.Println("Commands:")
	var list []*command
	seen := map[*command]bool{}
	for _, c := range cmdTable {
		if seen[c] {
			continue
		}
		seen[c] = true
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	for _, c := range list {
		fmt.Printf("  %-9s %s\n", c.name, c.summary)
	}
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println(`  cp -r src\pkg dst\pkg          rm -rf build\          touch -d "2024-01-02 10:00" a.txt`)
	fmt.Println(`  grep -rin todo --include=*.go .     tail -f -n 50 app.log     less README.md     vi main.go`)
	fmt.Println()
	fmt.Printf("Run '%s <command> --help' for the options of a single command.\n", progName)
}

// printUsage renders a GNU-ish help screen from the registered flag specs.
func printUsage(name, argSpec string, f *Flags) {
	fmt.Printf("Usage: %s [OPTION]... %s\n", name, argSpec)
	fmt.Println("Options:")
	for _, s := range f.specs {
		var left string
		switch {
		case s.Short != 0 && s.Long != "":
			left = fmt.Sprintf("  -%c, --%s", s.Short, s.Long)
		case s.Short != 0:
			left = fmt.Sprintf("  -%c", s.Short)
		default:
			left = fmt.Sprintf("      --%s", s.Long)
		}
		for _, a := range s.aliases {
			left += fmt.Sprintf(", -%c", a)
		}
		if s.HasArg {
			an := s.ArgName
			if an == "" {
				an = "ARG"
			}
			left += " " + an
		}
		fmt.Printf("%-28s %s\n", left, s.Help)
	}
}

func printVersion(name string) {
	if BuildTime != "" {
		fmt.Printf("%s (%s) %s (built %s)\n", name, progName, Version, BuildTime)
	} else {
		fmt.Printf("%s (%s) %s\n", name, progName, Version)
	}
	fmt.Printf("from: %s\n", exePath())
}

// exePath returns the full path of the running winux.exe binary.
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return "<unknown>"
	}
	return p
}

// parseOrDie parses args and reports option errors the way GNU tools do.
func parseOrDie(name string, f *Flags, args []string) bool {
	if err := f.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", name, err)
		fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", name)
		return false
	}
	return true
}

// handleHelpVersion returns true when the invocation was answered by
// --help/--version and the command should stop.
func handleHelpVersion(name, argSpec string, f *Flags) bool {
	if f.Has("help") {
		printUsage(name, argSpec, f)
		return true
	}
	if f.Has("version") {
		printVersion(name)
		return true
	}
	return false
}
