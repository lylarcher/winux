//go:build !windows

package core

import "os"

// The real targets are cmd.exe and PowerShell on Windows; these stubs keep the
// package buildable elsewhere (useful for running the unit tests on Linux CI).

func initConsole() {}

func isTerminal(f *os.File) bool { return false }

func consoleInput() (*os.File, func()) { return os.Stdin, func() {} }

func enterRawMode(in *os.File) func() { return func() {} }

func terminalSize() (rows, cols int) { return 24, 80 }

func osVersion() string { return "unknown" }

func processAlive(pid int) bool { return true }

func waitInput(f *os.File, ms int) bool { return false }

// readPassword cannot hide input without a console; callers refuse.
func readPassword(prompt string) (string, bool) { return "", false }
