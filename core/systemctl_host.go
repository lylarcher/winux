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

// sysMaterializeHost makes sure the unit owns a self-contained service host and
// returns its path. The source binary is the running winux.exe, or WINUX_EXE
// when set (the same override the remote-exec children use, which is also how
// tests point at a freshly built binary).
func sysMaterializeHost(name string) (string, error) {
	src := os.Getenv("WINUX_EXE")
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("cannot locate the winux executable: %v", err)
		}
		src = exe
	}
	return sysMaterializeHostFrom(src, name)
}

func sysMaterializeHostFrom(src, name string) (string, error) {
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
	if sysHostUpToDate(src, dest) {
		return dest, nil
	}
	// refresh: drop the stale host first so Link/Copy always succeeds
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		if _, serr := os.Stat(dest); serr == nil {
			// locked, almost certainly because the service is running and the
			// image is mapped: keep the old host and let a later start refresh
			warn("systemctl", "cannot refresh the service host %s: %v", dest, err)
			return dest, nil
		}
		return "", err
	}
	if err := os.Link(src, dest); err != nil {
		if cerr := copyFileContents(src, dest); cerr != nil {
			return "", fmt.Errorf("cannot create the service host %s: %v", dest, cerr)
		}
	}
	return dest, nil
}

// sysHostUpToDate reports whether dest already serves as the host for src: the
// very same file (hard link), or a copy with identical contents.
func sysHostUpToDate(src, dest string) bool {
	si, err := os.Stat(src)
	if err != nil {
		return false
	}
	di, err := os.Stat(dest)
	if err != nil || di.IsDir() || di.Size() == 0 {
		return false
	}
	if os.SameFile(si, di) {
		return true
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
