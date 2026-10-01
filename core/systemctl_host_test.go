//go:build windows

package core

// The per-unit service host is what keeps an installed service independent of
// the winux installation: SCM is registered against
// %ProgramData%\systemctl\hosts\<unit>.exe, a self-contained file (hard link
// when the volume allows it, otherwise a copy) that owns its data. These tests
// cover the materialization rules without needing administrator rights.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHostFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertHostContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s content mismatch: %d bytes, want %d", path, len(got), len(want))
	}
}

func TestHostDirDefaultsUnderProgramData(t *testing.T) {
	t.Setenv("SYSTEMCTL_HOST_DIR", "")
	want := filepath.Join(os.Getenv("ProgramData"), "systemctl", "hosts")
	if got := sysHostDir(); !strings.EqualFold(got, want) {
		t.Errorf("sysHostDir() = %q, want %q", got, want)
	}
	if got := sysHostPath("demo"); !strings.EqualFold(got, filepath.Join(want, "demo.exe")) {
		t.Errorf("sysHostPath(demo) = %q, want %q", got, filepath.Join(want, "demo.exe"))
	}
}

func TestMaterializeHostIsSelfContained(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	src := filepath.Join(t.TempDir(), "winux.exe")
	payload := []byte("winux-service-host-payload")
	writeHostFixture(t, src, payload)

	dest, err := sysMaterializeHostFrom(src, "demo")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if want := filepath.Join(hostDir, "demo.exe"); !strings.EqualFold(dest, want) {
		t.Fatalf("host path = %q, want %q", dest, want)
	}
	assertHostContent(t, dest, payload)

	// moving the winux binary away (upgrade/uninstall) must not break the host
	moved := src + ".moved"
	if err := os.Rename(src, moved); err != nil {
		t.Fatal(err)
	}
	assertHostContent(t, dest, payload)

	// re-materializing with the source gone keeps the existing host instead of
	// failing — the link or copy owns the data
	again, err := sysMaterializeHostFrom(src, "demo")
	if err != nil {
		t.Fatalf("materialize with the source gone: %v", err)
	}
	if !strings.EqualFold(again, dest) {
		t.Errorf("host path changed to %q", again)
	}
	assertHostContent(t, dest, payload)
}

func TestMaterializeHostReusesAndRefreshes(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	src := filepath.Join(t.TempDir(), "winux.exe")
	v1 := bytes.Repeat([]byte("a"), 4096)
	writeHostFixture(t, src, v1)

	dest, err := sysMaterializeHostFrom(src, "demo2")
	if err != nil {
		t.Fatal(err)
	}
	assertHostContent(t, dest, v1)

	// an unchanged source must reuse the very same file (no pointless rewrite)
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	again, err := sysMaterializeHostFrom(src, "demo2")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(again)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the host was replaced although the source did not change")
	}

	// same size, different content: only a content comparison can notice
	v2 := bytes.Repeat([]byte("b"), 4096)
	writeHostFixture(t, src, v2)
	if _, err := sysMaterializeHostFrom(src, "demo2"); err != nil {
		t.Fatal(err)
	}
	assertHostContent(t, dest, v2)

	// a size change is enough to notice too
	v3 := append(bytes.Repeat([]byte("c"), 100), []byte("tail")...)
	writeHostFixture(t, src, v3)
	if _, err := sysMaterializeHostFrom(src, "demo2"); err != nil {
		t.Fatal(err)
	}
	assertHostContent(t, dest, v3)
}

func TestMaterializeHostPrefersHardLink(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	dir := t.TempDir()
	src := filepath.Join(dir, "winux.exe")
	writeHostFixture(t, src, []byte("payload"))
	probe := filepath.Join(dir, "probe.link")
	if err := os.Link(src, probe); err != nil {
		t.Skipf("this volume does not support hard links: %v", err)
	}
	os.Remove(probe)

	dest, err := sysMaterializeHostFrom(src, "demo3")
	if err != nil {
		t.Fatal(err)
	}
	si, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(si, di) {
		t.Errorf("%s should be hard-linked to %s on a volume that supports links", dest, src)
	}
}

func TestMaterializeHostFromItsOwnPath(t *testing.T) {
	// when winux itself runs from the host file (e.g. `<unit>.exe systemctl
	// start <unit>`), materializing must be a no-op rather than a link onto
	// itself
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	self := filepath.Join(hostDir, "demo4.exe")
	writeHostFixture(t, self, []byte("self"))
	got, err := sysMaterializeHostFrom(self, "demo4")
	if err != nil {
		t.Fatalf("materialize onto itself: %v", err)
	}
	if !strings.EqualFold(got, self) {
		t.Errorf("got %q, want %q", got, self)
	}
	assertHostContent(t, self, []byte("self"))
}

func TestRemoveHost(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	p := sysHostPath("demo5")
	writeHostFixture(t, p, []byte("x"))
	sysRemoveHost("demo5")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("host still present after sysRemoveHost: %v", err)
	}
	// removing a host that is not there is not an error either
	sysRemoveHost("demo5")
}

func TestSamePathComparison(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.exe")
	if !sysSamePath(a, filepath.Join(dir, ".", "a.exe")) {
		t.Error("equivalent paths should compare equal")
	}
	if sysSamePath(a, filepath.Join(dir, "b.exe")) {
		t.Error("different paths should not compare equal")
	}
}

func TestHostModeSelection(t *testing.T) {
	cases := []struct{ env, want string }{
		{"", "link"},
		{"link", "link"},
		{"LINK", "link"},
		{" link ", "link"},
		{"copy", "copy"},
		{"COPY", "copy"},
		{"copy ", "copy"},
		{"bogus", "link"}, // unrecognized: warn once, keep the default
	}
	for _, c := range cases {
		t.Setenv("SYSTEMCTL_HOST_MODE", c.env)
		if got := sysHostMode(); got != c.want {
			t.Errorf("SYSTEMCTL_HOST_MODE=%q -> %q, want %q", c.env, got, c.want)
		}
	}
}

func TestMaterializeHostCopyModeDecouples(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	t.Setenv("SYSTEMCTL_HOST_MODE", "copy")
	src := filepath.Join(t.TempDir(), "winux.exe")
	payload := bytes.Repeat([]byte("z"), 2048)
	writeHostFixture(t, src, payload)

	dest, err := sysMaterializeHostFrom(src, "copymode")
	if err != nil {
		t.Fatal(err)
	}
	assertHostContent(t, dest, payload)
	si, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(si, di) {
		t.Fatal("copy mode must not hard-link the host to the installation")
	}

	// the point of copy mode: rewriting the source in place (copy /y over
	// winux.exe) must leave the host untouched
	writeHostFixture(t, src, []byte("overwritten-in-place"))
	assertHostContent(t, dest, payload)

	// and an identical copy is still reused instead of being rewritten
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sysMaterializeHostFrom(src, "copymode"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an up-to-date copy was replaced")
	}
}

func TestMaterializeHostModeSwitchBreaksLink(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("SYSTEMCTL_HOST_DIR", hostDir)
	dir := t.TempDir()
	src := filepath.Join(dir, "winux.exe")
	payload := bytes.Repeat([]byte("q"), 4096)
	writeHostFixture(t, src, payload)
	probe := filepath.Join(dir, "probe.link")
	if err := os.Link(src, probe); err != nil {
		t.Skipf("this volume does not support hard links: %v", err)
	}
	os.Remove(probe)

	t.Setenv("SYSTEMCTL_HOST_MODE", "link")
	dest, err := sysMaterializeHostFrom(src, "switch")
	if err != nil {
		t.Fatal(err)
	}
	si, _ := os.Stat(src)
	di, _ := os.Stat(dest)
	if !os.SameFile(si, di) {
		t.Fatalf("link mode did not hard-link %s to %s", dest, src)
	}

	// switching to copy mode must break the existing link, otherwise the host
	// would still share its data with the installation
	t.Setenv("SYSTEMCTL_HOST_MODE", "copy")
	dest2, err := sysMaterializeHostFrom(src, "switch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(dest2, dest) {
		t.Errorf("host path changed: %q -> %q", dest, dest2)
	}
	si2, _ := os.Stat(src)
	di2, _ := os.Stat(dest2)
	if os.SameFile(si2, di2) {
		t.Error("switching to copy mode left the hard link in place")
	}
	assertHostContent(t, dest2, payload)
}

// The generated built-in unit is winux-owned: a stale copy (baked-in winux
// path from another installation, or a path a test binary left behind) must
// never win, and a current copy must not be rewritten on every query.
func TestBuiltinUnitIsRegeneratedWhenStale(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SYSTEMCTL_ROOT", root)
	t.Setenv("SYSTEMCTL_UNIT_PATH", "")
	dir := filepath.Join(root, "units")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "winux-sshd.service")
	stale := "[Unit]\nDescription=winux SSH server (sshd)\n\n[Service]\nType=simple\n" +
		"ExecStart=\"C:\\somewhere\\else\\winux.exe\" sshd\n\n[Install]\nWantedBy=multi-user.target\n"
	writeHostFixture(t, p, []byte(stale))

	u, _, _, err := sysResolveTarget("winux-sshd")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := sysHostPath("winux-sshd"); !strings.Contains(u.ExecStart, want) {
		t.Errorf("ExecStart = %q, want it to point at the per-unit host %q", u.ExecStart, want)
	}
	if b := sysBuiltinUnit("winux-sshd"); !sysBuiltinUnitCurrent(p, b) {
		t.Errorf("the generated unit file was not refreshed: %s", p)
	}

	// already current: no rewrite (the mtime must stay put)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sysResolveTarget("winux-sshd"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("a current generated unit file was rewritten (%v -> %v)", before.ModTime(), after.ModTime())
	}
}

func TestBuiltinUnitUserCopyIsKept(t *testing.T) {
	// a user-written winux-sshd.service in a SYSTEMCTL_UNIT_PATH directory is
	// a deliberate override and must be used as is
	t.Setenv("SYSTEMCTL_ROOT", t.TempDir())
	user := t.TempDir()
	t.Setenv("SYSTEMCTL_UNIT_PATH", user)
	custom := "[Unit]\nDescription=custom sshd\n[Service]\nType=simple\n" +
		"ExecStart=C:\\tools\\mine.exe --serve\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(user, "winux-sshd.service"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _, _, err := sysResolveTarget("winux-sshd")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(u.ExecStart, "mine.exe") {
		t.Errorf("ExecStart = %q, want the user's own definition", u.ExecStart)
	}
}

func TestBuiltinUnitNeedsNoFileYet(t *testing.T) {
	// without a unit file the built-in is materialized so a wrapped service
	// gets a real path for `svchost -unit`
	root := t.TempDir()
	t.Setenv("SYSTEMCTL_ROOT", root)
	t.Setenv("SYSTEMCTL_UNIT_PATH", "")
	if got := sysCanonicalName("sshd"); got != "winux-sshd" {
		t.Errorf("sysCanonicalName(sshd) = %q, want winux-sshd", got)
	}
	u, _, _, err := sysResolveTarget("winux-sshd")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Name != "winux-sshd" {
		t.Errorf("unit name = %q, want winux-sshd", u.Name)
	}
	if _, err := os.Stat(filepath.Join(root, "units", "winux-sshd.service")); err != nil {
		t.Errorf("the built-in unit was not materialized: %v", err)
	}
}
