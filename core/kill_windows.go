//go:build windows

package core

// The Windows implementation of kill's actions. Each verb maps onto the closest
// real Windows mechanism; what cannot be expressed is reported as an error
// instead of being faked.
//
//   - actCheck: open the process with PROCESS_QUERY_LIMITED_INFORMATION.
//   - actForce: TerminateProcess (or `taskkill /F` for a tree, because a tree
//     walk is taskkill's job and it uses the same API).
//   - actGraceful: `taskkill /pid N` posts WM_CLOSE to a GUI program, which is
//     the only politeness Windows offers to a process of another console. It
//     refuses console processes ("can only be terminated forcefully"), so for a
//     process sharing OUR console the equivalent of Ctrl+C is used instead:
//     GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, pid) — that only works when the
//     pid is a console process group leader, which is exactly how a shell or
//     nohup would have started it. If the process is still there afterwards the
//     caller reports that -9 is needed, the same way GNU kill reports a program
//     that ignored SIGTERM.
//   - actSuspend / actResume: NtSuspendProcess / NtResumeProcess from ntdll.

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const processSuspendResume = 0x0800

var (
	ntdllSuspendProcess = ntdll.NewProc("NtSuspendProcess")
	ntdllResumeProcess  = ntdll.NewProc("NtResumeProcess")
)

// killErr turns a Win32 error into GNU's wording so scripts see the familiar
// messages: "No such process", "Operation not permitted".
func killErr(pid int, err error) error {
	switch {
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER), errors.Is(err, windows.ERROR_NOT_FOUND):
		return fmt.Errorf("No such process")
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return fmt.Errorf("Operation not permitted")
	}
	return err
}

func killSend(pid int, act killAction, tree, escalate bool) error {
	switch act {
	case actCheck:
		return killExists(pid)
	case actForce:
		return killForce(pid, tree)
	case actGraceful:
		return killGraceful(pid, tree, escalate)
	case actSuspend:
		return killSuspend(pid)
	case actResume:
		return killResume(pid)
	}
	return fmt.Errorf("unsupported action")
}

// killExists implements signal 0.
func killExists(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil // it exists; we just may not inspect it
		}
		return killErr(pid, err)
	}
	windows.CloseHandle(h)
	return nil
}

func killForce(pid int, tree bool) error {
	if tree {
		// a tree needs taskkill: it walks the parent/child links
		if _, err := runTaskkill(pid, "/T", "/F"); err != nil {
			if errors.Is(err, errTaskkillNotFound) || errors.Is(err, errTaskkillDenied) {
				return err
			}
			return fmt.Errorf("could not terminate pid %d and its descendants: %v", pid, err)
		}
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return killErr(pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return killErr(pid, err)
	}
	return nil
}

// killGraceful asks the process to exit.
//
// The only polite request Windows offers to a process outside your own code is
// WM_CLOSE, and taskkill sends exactly that (without /F). Console processes
// cannot be asked at all: taskkill answers "can only be terminated forcefully".
//
// GenerateConsoleCtrlEvent would look like the answer for console processes, and
// it is not: it addresses a process GROUP of the caller's console, so it also
// reaches the caller and its parent — an attempt to "politely" stop one child
// killed the whole shell during testing, and the target survived. It is therefore
// not used at all.
//
// When the process is still running after the request (or could never be asked),
// escalate to a forced termination unless the caller asked for strict POSIX
// behaviour (--graceful-only), in which case an error is returned and the caller
// can suggest -9.
func killGraceful(pid int, tree, escalate bool) error {
	if err := killExists(pid); err != nil {
		return err
	}
	_, err := runTaskkill(pid, treeArgs(tree)...)
	switch {
	case err == nil:
		if waitGone(pid, 3*time.Second) {
			return nil
		}
	case errors.Is(err, errTaskkillForceOnly):
		// console process: there is no polite request for it
	case errors.Is(err, errTaskkillNotFound), errors.Is(err, errTaskkillDenied):
		return err
	default:
		return fmt.Errorf("could not signal pid %d: %v", pid, err)
	}
	if !escalate {
		return fmt.Errorf("process %d cannot be asked to exit on Windows (no polite signal for console processes); use -9 or drop --graceful-only", pid)
	}
	return killForce(pid, tree)
}

func killSuspend(pid int) error {
	h, err := windows.OpenProcess(processSuspendResume, false, uint32(pid))
	if err != nil {
		return killErr(pid, err)
	}
	defer windows.CloseHandle(h)
	if r, _, e := ntdllSuspendProcess.Call(uintptr(h)); r != 0 {
		return fmt.Errorf("cannot suspend pid %d: %v", pid, e)
	}
	return nil
}

func killResume(pid int) error {
	h, err := windows.OpenProcess(processSuspendResume, false, uint32(pid))
	if err != nil {
		return killErr(pid, err)
	}
	defer windows.CloseHandle(h)
	if r, _, e := ntdllResumeProcess.Call(uintptr(h)); r != 0 {
		return fmt.Errorf("cannot resume pid %d: %v", pid, e)
	}
	return nil
}

func treeArgs(tree bool) []string {
	if tree {
		return []string{"/T"}
	}
	return nil
}

func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if killExists(pid) != nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// taskkill's exit statuses and messages, which is how the three outcomes below
// are recognised (taskkill has no machine-readable output).
var (
	errTaskkillNotFound  = errors.New("No such process")
	errTaskkillDenied    = errors.New("Operation not permitted")
	errTaskkillForceOnly = errors.New("taskkill can only force this process")
)

// runTaskkill runs taskkill and classifies its failure.
func runTaskkill(pid int, args ...string) (string, error) {
	cmd := exec.Command(systemExe("taskkill.exe"), append([]string{"/PID", strconv.Itoa(pid)}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err == nil {
		return text, nil
	}
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "not found"), strings.Contains(low, "没有找到"):
		return text, errTaskkillNotFound
	case strings.Contains(low, "access is denied"), strings.Contains(low, "拒绝访问"):
		return text, errTaskkillDenied
	case strings.Contains(low, "forcefully"), strings.Contains(low, "强行"):
		return text, errTaskkillForceOnly
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return text, fmt.Errorf("taskkill exited %d: %s", ee.ExitCode(), text)
	}
	return text, err
}
