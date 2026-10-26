//go:build windows

package core

// Every wrapped unit gets its own service host executable under
// %ProgramData%\systemctl\hosts\<unit>.exe.
//
// Why a host is needed at all: the SCM requires the registered program to
// speak the service protocol itself (StartServiceCtrlDispatcher + a control
// handler) and otherwise fails the start with error 1053 ("did not respond in
// a timely fashion"). A plain console program does not, so a console ExecStart
// can only ever run *under* a host process that does — that is what `svchost`
// is. Registering the host straight out of the winux installation directory
// would make every installed service depend on winux.exe staying exactly where
// it is: moving, replacing or uninstalling winux would break services that are
// already installed.
//
// The host is therefore materialized next to the unit data, as a self-contained
// file that is the very same binary as winux.exe:
//
//	%ProgramData%\systemctl\hosts\<unit>.exe
//
// A hard link is preferred: it costs no extra disk space and, because the link
// owns the file data, the host keeps working after winux.exe is deleted, moved
// or replaced. Cross-volume setups (a link is impossible) fall back to a real
// copy. The host is re-materialized when the source binary's contents changed,
// which is how a winux upgrade propagates (`systemctl start` / `enable` /
// `daemon-reload`). If the source is gone, an existing host is kept as is —
// that is exactly the "winux was uninstalled, the service still runs" case.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// sysHostDir returns the directory holding the per-unit service hosts.
// SYSTEMCTL_HOST_DIR overrides it (tests).
func sysHostDir() string {
	if p := os.Getenv("SYSTEMCTL_HOST_DIR"); p != "" {
		return p
	}
	return filepath.Join(sysRootDir(), "hosts")
}

// sysHostPath returns the host executable path of a unit.
func sysHostPath(name string) string {
	return filepath.Join(sysHostDir(), name+".exe")
}

// sysMaterializeHostMode makes sure the unit owns a self-contained service host
// and returns its path. The unit's own `HostMode=` in [Service] wins, then
// SYSTEMCTL_HOST_MODE, then the "link" default (pass "" for a unit with no
// override). The source binary is the running winux.exe, or WINUX_EXE when set
// (the same override the remote-exec children use, which is also how tests
// point at a freshly built binary); a WINUX_EXE naming the installation
// directory is accepted too — see sysWinuxBinary.
func sysMaterializeHostMode(name, unitMode string) (string, error) {
	src := sysWinuxBinary()
	if src == "" {
		return "", fmt.Errorf("cannot locate the winux executable")
	}
	return sysMaterializeHostFrom(src, name, sysHostModeFor(unitMode))
}

func sysMaterializeHostFrom(src, name, mode string) (string, error) {
	if mode == "" {
		// no unit-level override: follow the environment (default "link")
		mode = sysHostMode()
	}
	dest := sysHostPath(name)
	if sysSamePath(src, dest) {
		// already running from our own host file
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", fmt.Errorf("cannot create %s: %v", filepath.Dir(dest), err)
	}
	if _, err := os.Stat(src); err != nil {
		// The winux binary is gone (uninstalled/moved) but a host exists: keep
		// it — the link or copy owns the data, so the service keeps working.
		if fi, derr := os.Stat(dest); derr == nil && !fi.IsDir() && fi.Size() > 0 {
			return dest, nil
		}
		return "", fmt.Errorf("cannot create the service host %s: %v", dest, err)
	}
	if sysHostUpToDate(src, dest, mode) {
		return dest, nil
	}
	// refresh: drop the stale host first so Link/Copy always succeeds
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		if _, serr := os.Stat(dest); serr == nil {
			// locked (another process holds it open without sharing delete):
			// keep the old host and let a later start refresh it
			warn("systemctl", "cannot refresh the service host %s: %v", dest, err)
			return dest, nil
		}
		return "", err
	}
	if mode == "link" {
		if err := os.Link(src, dest); err == nil {
			return dest, nil
		}
		// no hard links on this volume: fall through to a copy, which is the
		// only remaining way to give the unit a self-contained host
	}
	if cerr := copyFileContents(src, dest); cerr != nil {
		return "", fmt.Errorf("cannot create the service host %s: %v", dest, cerr)
	}
	return dest, nil
}

// sysHostMode returns how a per-unit host is materialized:
//
//	link (default) - hard link to the winux binary when the volume allows it,
//	                 otherwise a copy. Costs no extra disk space, but the host
//	                 shares its data with the installation, so overwriting
//	                 winux.exe in place (copy /y) is refused while a service
//	                 runs from it — stop the service first, or replace the file
//	                 (move /y).
//	copy           - always a real copy. The host then owns its data outright:
//	                 winux.exe can even be overwritten in place while services
//	                 run, and each service picks the new binary up at its next
//	                 start. Costs one full binary per unit (~13 MiB).
//
// SYSTEMCTL_HOST_MODE selects the mode (link|copy); an empty or unrecognized
// value means "link". A unit can override it per service with `HostMode=` in
// its [Service] section (see sysHostModeFor). The mode only matters when a host
// is materialized, i.e. at `systemctl start`/`enable`/`daemon-reload` or
// `sshd --install` — never at service start-up, so the services themselves need
// no environment.
func sysHostMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SYSTEMCTL_HOST_MODE"))) {
	case "copy", "clone":
		return "copy"
	case "", "link", "hardlink", "hard":
		return "link"
	default:
		hostModeWarn.Do(func() {
			warn("systemctl", "ignoring SYSTEMCTL_HOST_MODE=%q (use link or copy)",
				os.Getenv("SYSTEMCTL_HOST_MODE"))
		})
		return "link"
	}
}

// sysHostModeFor resolves the effective mode for one unit: the unit's own
// `HostMode=` wins over SYSTEMCTL_HOST_MODE, which wins over the "link"
// default. A value the unit level does not recognize falls through to the
// environment (sysLoadUnit already warns about it; this keeps direct callers
// and hand-written values safe too).
func sysHostModeFor(unitMode string) string {
	if unitMode != "" {
		if m, ok := sysParseHostMode(unitMode); ok {
			return m
		}
	}
	return sysHostMode()
}

// sysParseHostMode normalizes a mode spelling from a unit file. ok is false for
// an unknown value (the caller warns and falls back to the environment).
func sysParseHostMode(v string) (mode string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "copy", "clone":
		return "copy", true
	case "link", "hardlink", "hard":
		return "link", true
	}
	return "", false
}

// hostModeWarn keeps the "unrecognized mode" warning to one line per run.
var hostModeWarn sync.Once

// sysHostUpToDate reports whether dest already serves as the host for src in
// the given mode: the very same file (hard link), or a copy with identical
// contents.
func sysHostUpToDate(src, dest, mode string) bool {
	si, err := os.Stat(src)
	if err != nil {
		return false
	}
	di, err := os.Stat(dest)
	if err != nil || di.IsDir() || di.Size() == 0 {
		return false
	}
	if os.SameFile(si, di) {
		// a hard link is only right in link mode: copy mode must own its data,
		// so an existing link is broken (re-copied) even when the contents
		// match. The other direction needs no action — an existing copy is
		// good enough for link mode and turns into a link at the next refresh
		// (i.e. when the winux binary changes).
		return mode != "copy"
	}
	if si.Size() != di.Size() {
		return false
	}
	return sysFileDigest(src) == sysFileDigest(dest)
}

// sysRemoveHost drops a unit's host file (best effort: a running service maps
// it, in which case the file stays and the next start reuses it).
func sysRemoveHost(name string) {
	p := sysHostPath(name)
	if _, err := os.Stat(p); err != nil {
		return
	}
	if err := os.Remove(p); err != nil {
		warn("systemctl", "cannot remove the service host %s: %v", p, err)
	}
}

// sysSamePath compares two paths without touching the file system.
func sysSamePath(a, b string) bool {
	ca, errA := filepath.Abs(a)
	cb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
}

// sysFileDigest returns the SHA-256 of a file's contents ("" on error).
func sysFileDigest(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
