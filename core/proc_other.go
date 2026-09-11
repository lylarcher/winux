//go:build !windows

package core

import "time"

func snapshotProcesses() ([]procInfo, error) {
	return nil, &unsupportedError{"process snapshot"}
}

func currentUserName() (string, error) {
	return "", &unsupportedError{"current user lookup"}
}

func physMemoryKB() uint64 { return 0 }

func processOwner(pid int) string { return "?" }

func parseCimDate(s string) (time.Time, error) { return time.Time{}, &unsupportedError{"CIM date"} }
