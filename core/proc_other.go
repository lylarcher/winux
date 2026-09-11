//go:build !windows

package core

import "time"

func snapshotProcesses() ([]procInfo, error) {
	return nil, &unsupportedError{"process snapshot"}
}

// currentUserName is implemented portably in sys.go (os/user + env fallback),
// so no stub is needed here.

func physMemoryKB() uint64 { return 0 }

func processOwner(pid int) string { return "?" }

func parseCimDate(s string) (time.Time, error) { return time.Time{}, &unsupportedError{"CIM date"} }
