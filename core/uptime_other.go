//go:build !windows

package core

// Stubs for cross-compilation (linux/darwin): winux is a Windows toolbox, so
// uptime degrades to a clear "not supported" on other platforms.

import "time"

func systemUptime() time.Duration { return 0 }

func activeSessionCount() int { return 0 }
