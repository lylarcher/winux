//go:build windows

package core

// systemctl is a Go port of the systemctl-win service manager: it manages
// systemd-style *.service unit files by mapping them onto Windows services
// (wrapped by the built-in svchost host, or native win32 services) and
// scheduled tasks (on-boot / on-login backends).
//
//	winux systemctl daemon-reload          # scan the unit dir, (re)install
//	winux systemctl start demo             # start a unit
//	winux systemctl status demo            # detailed status (systemd style)
//	winux systemctl list-units
//	winux systemctl journalctl -u demo -f  # follow the wrapper log
//
// Unit directory (like systemctl-win): %ProgramData%\systemctl\units, plus
// any directories in the SYSTEMCTL_UNIT_PATH env var. State lives in
// %ProgramData%\systemctl\state. Mutating commands need administrator
// rights.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func init() {
	addCmd("systemctl", "systemd-style service manager on Windows (units -> services/tasks)", cmdSystemctl)
}

const (
	sysTaskPath = `\systemctl\`
)

// sysRootDir is the unit/state/host root, %ProgramData%\systemctl.
// SYSTEMCTL_ROOT overrides it: tests use a temporary root so they can never
// touch the real unit files, state or service hosts of the machine.
func sysRootDir() string {
	if r := os.Getenv("SYSTEMCTL_ROOT"); r != "" {
		return r
	}
	return filepath.Join(os.Getenv("ProgramData"), "systemctl")
}

func sysStateDir() string       { return filepath.Join(sysRootDir(), "state") }
func sysDefaultUnitDir() string { return filepath.Join(sysRootDir(), "units") }

func sysUnitDirs() []string {
	dirs := []string{sysDefaultUnitDir()}
	if p := os.Getenv("SYSTEMCTL_UNIT_PATH"); p != "" {
		for _, d := range strings.Split(p, ";") {
			if strings.TrimSpace(d) != "" {
				dirs = append(dirs, strings.TrimSpace(d))
			}
		}
	}
	return dirs
}

// isAdmin reports whether the current process runs with an elevated token.
func isAdmin() bool {
	var tok windows.Token
	cur, err := windows.GetCurrentProcess()
	if err != nil {
		return false
	}
	if err := windows.OpenProcessToken(cur, windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()
	var elevated uint32
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &n); err != nil {
		return false
	}
	return elevated != 0
}

// ---------------------------------------------------------------------------
// Unit file model (mirrors systemctl-win)

type sysUnit struct {
	Name           string
	Path           string
	Description    string
	SvcType        string // "simple" (wrapped) or "native" (win32)
	Trigger        string // "on-boot" or "on-login"
	ExecStart      string
	WorkDir        string
	User           string
	Restart        string
	KillTimeoutSec string
	Env            []string
	HostMode       string // "" (use SYSTEMCTL_HOST_MODE), "link" or "copy"
	Hash           string
}

type sysUnitState struct {
	Name      string `json:"name"`
	Backend   string `json:"backend"`
	Trigger   string `json:"trigger"`
	UnitPath  string `json:"unitPath"`
	Hash      string `json:"hash"`
	UpdatedAt string `json:"updatedAt"`
}

func sysParseIni(data []byte) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line[1 : len(line)-1])
			if out[section] == nil {
				out[section] = map[string][]string{}
			}
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 || section == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		val := strings.TrimSpace(line[i+1:])
		out[section][key] = append(out[section][key], val)
	}
	return out
}

func sysIniFirst(sec map[string][]string, key string) string {
	if v := sec[key]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func sysValidUnitName(n string) bool {
	if n == "" || len(n) > 200 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func sysLoadUnit(path string) (*sysUnit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !sysValidUnitName(name) {
		return nil, fmt.Errorf("illegal unit name %q", name)
	}
	ini := sysParseIni(data)
	sec := ini["service"]
	if sec == nil {
		return nil, fmt.Errorf("[%s] missing [Service] section", path)
	}
	execStart := sysIniFirst(sec, "execstart")
	if execStart == "" {
		return nil, fmt.Errorf("[%s] missing [Service] ExecStart=", path)
	}
	svcType := "simple"
	if t := strings.ToLower(sysIniFirst(sec, "type")); t != "" {
		switch t {
		case "win32", "native", "own-process":
			svcType = "native"
		case "simple":
			svcType = "simple"
		default:
			return nil, fmt.Errorf("[%s] unsupported Type=%s", path, t)
		}
	}
	trigger := "on-boot"
	if t := strings.ToLower(sysIniFirst(sec, "startuptrigger")); t != "" {
		switch t {
		case "on-login", "login":
			trigger = "on-login"
		case "on-boot", "boot":
			trigger = "on-boot"
		default:
			return nil, fmt.Errorf("[%s] illegal StartupTrigger=%s", path, t)
		}
	}
	var env []string
	for _, e := range sec["environment"] {
		for _, pair := range sysSplitWords(e) {
			if strings.Contains(pair, "=") {
				env = append(env, pair)
			}
		}
	}
	restart := sysIniFirst(sec, "restart")
	if restart == "" {
		restart = "no"
	}
	// Per-unit host mode (winux extension): how this unit's service host is
	// materialized (hosts\<unit>.exe). "link" (hard link, default) or "copy"
	// (real copy, decoupled from the winux installation). Falls back to
	// SYSTEMCTL_HOST_MODE and then to "link".
	hostMode := ""
	if v := sysIniFirst(sec, "hostmode"); v != "" {
		m, ok := sysParseHostMode(v)
		if !ok {
			warn("systemctl", "%s: ignoring unknown HostMode=%s (use link or copy)", name, v)
		} else {
			hostMode = m
		}
	}
	u := &sysUnit{
		Name:           name,
		Path:           path,
		Description:    sysIniFirst(ini["unit"], "description"),
		SvcType:        svcType,
		Trigger:        trigger,
		ExecStart:      execStart,
		WorkDir:        sysIniFirst(sec, "workingdirectory"),
		User:           sysIniFirst(sec, "user"),
		Restart:        strings.ToLower(restart),
		KillTimeoutSec: sysIniFirst(sec, "killtimeoutsec"),
		Env:            env,
		HostMode:       hostMode,
	}
	// 16-char hash of the file content (used to detect changes)
	sum := sha256Hex(data)
	if len(sum) > 16 {
		sum = sum[:16]
	}
	u.Hash = sum
	return u, nil
}

// sysSplitWords splits a line into shell-style words honouring double quotes.
func sysSplitWords(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
		case (r == ' ' || r == '\t') && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// sysSplitExecStart splits ExecStart into exe + args preserving quotes.
func sysSplitExecStart(s string) (exe, args string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			return s[1 : 1+end], strings.TrimSpace(s[end+2:])
		}
		return strings.Trim(s, `"`), ""
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i:])
	}
	return s, ""
}

// ---------------------------------------------------------------------------
// State storage

func sysReadState(name string) *sysUnitState {
	p := filepath.Join(sysStateDir(), name+".json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var st sysUnitState
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	return &st
}

func sysWriteState(st *sysUnitState) {
	dir := sysStateDir()
	os.MkdirAll(dir, 0755)
	data, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(filepath.Join(dir, st.Name+".json"), data, 0644)
}

func sysRemoveState(name string) {
	os.Remove(filepath.Join(sysStateDir(), name+".json"))
}

func sysTestMasked(name string) bool {
	_, err := os.Stat(filepath.Join(sysStateDir(), name+".masked"))
	return err == nil
}

// ---------------------------------------------------------------------------
// Windows service backend (x/sys/windows/svc/mgr)

func sysOpenMgr() (*mgr.Mgr, error) {
	return mgr.Connect()
}

func sysServiceExists(m *mgr.Mgr, name string) (bool, error) {
	s, err := m.OpenService(name)
	if err != nil {
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			return false, nil
		}
		return false, err
	}
	s.Close()
	return true, nil
}

// sysServiceRuntime returns (running, enabled) or (false,false,false) when the
// service does not exist.
func sysServiceRuntime(name string) (exists, running, enabled bool) {
	m, err := sysOpenMgr()
	if err != nil {
		return false, false, false
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return false, false, false
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return true, false, false
	}
	cfg, err := s.Config()
	if err != nil {
		return true, st.State == svc.Running, false
	}
	en := cfg.StartType == mgr.StartAutomatic
	return true, st.State == svc.Running, en
}

// sysServiceStartType returns the service's StartType name ("Automatic"...).
func sysServiceStartType(name string) string {
	m, err := sysOpenMgr()
	if err != nil {
		return ""
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return ""
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return ""
	}
	switch cfg.StartType {
	case mgr.StartAutomatic:
		return "Automatic"
	case mgr.StartManual:
		return "Manual"
	case mgr.StartDisabled:
		return "Disabled"
	}
	return fmt.Sprintf("%d", cfg.StartType)
}

func sysServiceDetail(name string) (binPath, account string) {
	m, err := sysOpenMgr()
	if err != nil {
		return "", ""
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return "", ""
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return "", ""
	}
	return cfg.BinaryPathName, cfg.ServiceStartName
}

// ---------------------------------------------------------------------------
// Scheduled task backend (schtasks.exe; /xml output is locale-independent)

type sysTaskXML struct {
	State    int `xml:"State"`
	Settings struct {
		Enabled bool `xml:"Enabled"`
	} `xml:"Settings"`
}

func sysTaskExists(name string) (exists, running, enabled bool) {
	out, err := exec.Command("schtasks.exe", "/query", "/tn", sysTaskPath+name, "/xml").Output()
	if err != nil {
		return false, false, false
	}
	var t sysTaskXML
	if xml.Unmarshal(out, &t) != nil {
		return true, false, false
	}
	// State: 0 unknown, 1 disabled, 2 queued, 3 ready, 4 running
	return true, t.State == 4, t.Settings.Enabled
}

func sysLoginTaskName(name string) string { return "systemctl-login-" + name }

// sysRunTask runs schtasks.exe and returns a friendly error on failure.
func sysRunTask(args ...string) error {
	cmd := exec.Command("schtasks.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func sysInstallTask(name, cmdline, user string, onLogin bool) error {
	exe, args := sysSplitExecStart(cmdline)
	tr := filepath.ToSlash(exe)
	taskArgs := []string{"/create", "/f", "/tn", sysTaskPath + name, "/tr", fmt.Sprintf(`"%s" %s`, tr, args), "/sc", "onstart", "/ru", "SYSTEM"}
	if onLogin {
		taskArgs = []string{"/create", "/f", "/tn", sysTaskPath + sysLoginTaskName(name), "/tr", fmt.Sprintf(`"%s" %s`, tr, args), "/sc", "onlogon", "/ru", user, "/it"}
	}
	if err := sysRunTask(taskArgs...); err != nil {
		return err
	}
	// systemd semantics: freshly installed units are not enabled until `enable`.
	taskName := sysTaskPath + name
	if onLogin {
		taskName = sysTaskPath + sysLoginTaskName(name)
	}
	return sysRunTask("/change", "/tn", taskName, "/disable")
}

func sysRemoveTask(name string, login bool) error {
	t := name
	if login {
		t = sysLoginTaskName(name)
	}
	return sysRunTask("/delete", "/f", "/tn", sysTaskPath+t)
}

func sysTaskEnabled(name string, login, enabled bool) error {
	t := name
	if login {
		t = sysLoginTaskName(name)
	}
	flag := "/enable"
	if !enabled {
		flag = "/disable"
	}
	return sysRunTask("/change", "/tn", sysTaskPath+t, flag)
}

func sysStartStopTask(name string, login, start bool) error {
	t := name
	if login {
		t = sysLoginTaskName(name)
	}
	verb := "/run"
	if !start {
		verb = "/end"
	}
	return sysRunTask(verb, "/tn", sysTaskPath+t)
}

// ---------------------------------------------------------------------------
// Runtime model

type sysRuntime struct {
	Kind      string // service | task | login | loginsvc
	Running   bool
	Enabled   bool
	StartType string
	Trigger   string
	Detail    string
}

func sysGetRuntime(name string) *sysRuntime {
	e, r, en := sysTaskExists(sysLoginTaskName(name))
	if e {
		sExists, sRunning, _ := sysServiceRuntime(name)
		if sExists {
			// loginsvc: real service + login task
			return &sysRuntime{Kind: "loginsvc", Running: sRunning, Enabled: en, StartType: sysServiceStartType(name) + " + login-task", Trigger: "on-login", Detail: name + " + login task"}
		}
		return &sysRuntime{Kind: "login", Running: r, Enabled: en, StartType: func() string {
			if en {
				return "OnLogOn"
			}
			return "Disabled(logon)"
		}(), Trigger: "on-login", Detail: sysTaskPath + sysLoginTaskName(name)}
	}
	if sExists, sRunning, sEnabled := sysServiceRuntime(name); sExists {
		return &sysRuntime{Kind: "service", Running: sRunning, Enabled: sEnabled, StartType: sysServiceStartType(name), Trigger: "on-boot", Detail: name}
	}
	if e, r, en := sysTaskExists(name); e {
		enabled := en
		return &sysRuntime{Kind: "task", Running: r, Enabled: enabled, StartType: func() string {
			if enabled {
				return "AtStartup"
			}
			return "Disabled"
		}(), Trigger: "on-boot", Detail: sysTaskPath + name}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backend selection + install (mirrors systemctl-win)

func sysTargetBackend(u *sysUnit) string {
	if u.Trigger == "on-login" {
		if u.SvcType == "native" {
			return "loginsvc"
		}
		return "login"
	}
	if u.SvcType == "native" {
		return "native"
	}
	return "wrapped" // winux.exe always embeds the svchost host
}

// sysServiceImage returns the SCM command line of a unit's service backend. Both
// registration and repair go through it so a repaired service is registered with
// exactly what an install would have written.
func sysServiceImage(u *sysUnit, backend string) (string, error) {
	if backend == "wrapped" {
		// The service must not depend on the winux installation: each unit gets
		// its own self-contained copy of the host under ProgramData (see
		// sysMaterializeHost), and SCM is pointed at that file. The unit's own
		// HostMode= decides how it lands on disk (link or copy).
		host, err := sysMaterializeHostMode(u.Name, u.HostMode)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(`"%s" svchost -unit "%s"`, host, u.Path), nil
	}
	exe, args := sysSplitExecStart(u.ExecStart)
	if strings.ContainsAny(exe, " ") {
		exe = `"` + exe + `"`
	}
	if args != "" {
		return exe + " " + args, nil
	}
	return exe, nil
}

func sysInstallServiceBackend(u *sysUnit, backend string) (string, error) {
	name := u.Name
	bin, err := sysServiceImage(u, backend)
	if err != nil {
		return "", err
	}
	desc := u.Description
	if desc == "" {
		desc = name
	}
	m, err := sysOpenMgr()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	exists, err := sysServiceExists(m, name)
	if err != nil {
		return "", err
	}
	if !exists {
		s, err := m.CreateService(name, bin, mgr.Config{
			DisplayName: desc,
			StartType:   mgr.StartManual,
			Description: desc,
		})
		if err != nil {
			return "", err
		}
		s.Close()
		if acct := sysResolveAccount(u.User); acct != "" && acct != "LocalSystem" {
			s2, err := m.OpenService(name)
			if err == nil {
				cfg, _ := s2.Config()
				cfg.ServiceStartName = acct
				s2.UpdateConfig(cfg)
				s2.Close()
			}
		}
		return "installed", nil
	}
	s, err := m.OpenService(name)
	if err != nil {
		return "", err
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return "", err
	}
	cfg.BinaryPathName = bin
	cfg.DisplayName = desc
	if u.Description != "" {
		cfg.Description = u.Description
	}
	if err := s.UpdateConfig(cfg); err != nil {
		return "", err
	}
	return "updated", nil
}

// ---------------------------------------------------------------------------
// Start diagnostics and self-repair
//
// `systemctl start` used to report the SCM's bare error, e.g. "start failed:
// Access is denied." — which names neither the operation nor the file, and that
// is the message a broken registration produces (an image path pointing at a
// directory, a host that was removed, a name left over from an older install).
// Windows answers ERROR_ACCESS_DENIED when the image cannot be opened for
// execution, so the code alone cannot tell those apart: the facts can.

// sysImagePathOf strips the arguments from an SCM command line.
//
// It also understands a command line polluted by PowerShell-style escaping:
// `sc.exe create x binPath= "\"C:\a b\x.exe\" -foo"` stores the backslashes
// verbatim, so the SCM ends up with `\"C:\a b\x.exe\"` where it expects
// `"C:\a b\x.exe"`. Such a service cannot start (ERROR_ACCESS_DENIED), and a
// reader that merely strips arguments would report the file as missing instead
// of the quoting as broken.
func sysImagePathOf(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if strings.HasPrefix(cmd, `\"`) { // the escaped opening quote
		body := cmd[2:]
		if i := strings.Index(body, `\"`); i >= 0 {
			return body[:i]
		}
		if i := strings.IndexByte(body, ' '); i > 0 {
			return body[:i]
		}
		return body
	}
	if cmd[0] == '"' {
		if i := strings.IndexByte(cmd[1:], '"'); i >= 0 {
			return cmd[1 : 1+i]
		}
		return strings.TrimSpace(cmd[1:])
	}
	if i := strings.IndexByte(cmd, ' '); i > 0 {
		return cmd[:i]
	}
	return cmd
}

// sysImageIsEscaped reports whether a registered command line still carries the
// `\"` escaping — i.e. the backslashes were handed to the SCM as data.
func sysImageIsEscaped(cmd string) bool {
	return strings.Contains(strings.TrimSpace(cmd), `\"`)
}

// sysImageFact describes a registered image so a failure message can show what
// the SCM is actually pointing at.
func sysImageFact(cmd string) string {
	p := sysImagePathOf(cmd)
	if p == "" {
		return "（注册的命令行为空）"
	}
	fi, err := os.Stat(p)
	if sysImageIsEscaped(cmd) {
		// the file itself may be perfectly fine: what is broken is the quoting
		// the SCM was handed, and that alone stops the service from starting
		return fmt.Sprintf("%s（命令行里带着 \\\" 转义：SCM 解析不出有效映像，重新注册即可）", p)
	}
	switch {
	case err != nil:
		return p + "（不存在）"
	case fi.IsDir():
		return p + "（是目录，不是可执行文件）"
	case fi.Size() == 0:
		return p + "（0 字节：空文件，无法执行）"
	default:
		return fmt.Sprintf("%s（%d 字节）", p, fi.Size())
	}
}

// sysStartHint renders the actionable part of a start failure: Windows reuses
// ERROR_ACCESS_DENIED for several unrelated causes, so a code-only message sends
// people looking in the wrong place.
func sysStartHint(err error) string {
	var e syscall.Errno
	if !errors.As(err, &e) {
		return ""
	}
	switch uint32(e) {
	case 5: // ERROR_ACCESS_DENIED
		return "SCM 拒绝了这次启动。常见三种原因：① 注册的映像不可执行——路径是目录、" +
			"文件已被删除，或 ACL 不允读取（看上面的『服务映像』）；② 服务的安全描述符不" +
			"允许当前账号启动它；③ 服务账户的密码/权限已失效。可用 `winux systemctl " +
			"enable <unit>` 按单元文件重写注册后重试，或 `sc.exe qc <unit>` 对照 BINARY_PATH_NAME。"
	case 1053: // ERROR_SERVICE_REQUEST_TIMEOUT
		return "程序 30 秒内没有回应 SCM。控制台程序必须用 Type=simple（由 winux 的 svchost " +
			"宿主承载）；Type=native 只有程序自己实现了服务协议才行。"
	case 1058:
		return "服务被禁用。`winux systemctl enable <unit>` 后再启动。"
	case 1060:
		return "服务不存在（可能已被删除）。`winux systemctl daemon-reload` 重新安装一次。"
	case 1072:
		return "服务被标记为删除。重启一次，或 `sc.exe delete <unit>` 后重新安装。"
	}
	return ""
}

// sysImageRunnable reports whether a path can possibly be started as an image:
// a regular file with contents. A directory and a 0-byte file are the two shapes
// an interrupted materialization leaves behind, and both fail the same way as a
// missing file. A command line that still carries `\"` escaping is unrunnable no
// matter what the file looks like — the SCM was handed the backslashes as data.
func sysImageRunnable(cmd string) bool {
	if sysImageIsEscaped(cmd) {
		return false
	}
	path := sysImagePathOf(cmd)
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// sysImageNeedsRepair reports whether the registered command line cannot possibly
// run while the expected one can. Only that provable case is repaired: a working
// registration is never rewritten, so a deliberate edit is respected.
func sysImageNeedsRepair(current, expected string) bool {
	if expected == "" || sysImageRunnable(current) {
		return false
	}
	return sysImageRunnable(expected)
}

// sysRepairServiceImage rewrites a registration whose image cannot run and
// returns a short description of what changed.
func sysRepairServiceImage(name string, s *mgr.Service) (string, bool) {
	u, _, _, err := sysResolveTarget(name)
	if err != nil || u == nil {
		return "", false
	}
	want, err := sysServiceImage(u, sysTargetBackend(u))
	if err != nil {
		return "", false
	}
	cfg, err := s.Config()
	if err != nil {
		return "", false
	}
	if !sysImageNeedsRepair(cfg.BinaryPathName, want) {
		return "", false
	}
	was := cfg.BinaryPathName
	cfg.BinaryPathName = want
	if err := s.UpdateConfig(cfg); err != nil {
		warn("systemctl", "%s: cannot rewrite the service registration: %v", name, err)
		return "", false
	}
	return fmt.Sprintf("映像 %s → %s", sysImagePathOf(was), sysImagePathOf(want)), true
}

// sysStartFailure turns the SCM's error into something that can be acted on: the
// code, the registered image, what the unit expects, and the elevation state.
func sysStartFailure(name string, s *mgr.Service, cause error, repaired string) error {
	var cfgp *mgr.Config
	if cfg, err := s.Config(); err == nil {
		cfgp = &cfg
	}
	return errors.New(sysStartFailureText(name, cfgp, cause, repaired))
}

// sysStartFailureText builds the message sysStartFailure returns. Split out so
// the wording (which is the whole point of it) is testable without an SCM.
func sysStartFailureText(name string, cfg *mgr.Config, cause error, repaired string) string {
	var b strings.Builder
	b.WriteString(cause.Error())
	if repaired != "" {
		fmt.Fprintf(&b, "\n   已修正服务注册（%s），重试后仍然失败", repaired)
	}
	if cfg != nil {
		fmt.Fprintf(&b, "\n   服务映像: %s", sysImageFact(cfg.BinaryPathName))
	}
	if u, _, _, err := sysResolveTarget(name); err == nil && u != nil {
		if want, werr := sysServiceImage(u, sysTargetBackend(u)); werr == nil {
			fmt.Fprintf(&b, "\n   单元期望: %s", want)
		}
	}
	if isAdmin() {
		b.WriteString("\n   权限: 管理员（已具备启停服务的权限）")
	} else {
		b.WriteString("\n   权限: 非管理员 —— start 需要提权")
	}
	if hint := sysStartHint(cause); hint != "" {
		fmt.Fprintf(&b, "\n   提示: %s", hint)
	}
	return b.String()
}

// sysStartService starts an already-registered service, repairing a registration
// that provably cannot run before giving up.
func sysStartService(name string, s *mgr.Service) (repaired string, err error) {
	if err = s.Start(); err == nil {
		return "", nil
	}
	rep, ok := sysRepairServiceImage(name, s)
	if !ok {
		return "", err
	}
	if e2 := s.Start(); e2 != nil {
		return rep, e2
	}
	return rep, nil
}

func sysResolveAccount(user string) string {
	switch strings.ToLower(user) {
	case "localsystem", "system":
		return "LocalSystem"
	case "localservice":
		return "NT AUTHORITY\\LocalService"
	case "networkservice":
		return "NT AUTHORITY\\NetworkService"
	}
	return ""
}

func sysInstallUnit(u *sysUnit, adopt bool) (string, error) {
	name := u.Name
	target := sysTargetBackend(u)
	// A wrapped unit is re-read from disk by its host at run time, so the file
	// must match the definition we are installing: refresh a stale generated
	// built-in unit rather than registering a service that would start the
	// wrong thing (or nothing at all).
	if b := sysBuiltinUnit(name); b != nil && (u.Path == "" || !sysBuiltinUnitCurrent(u.Path, b)) {
		mp := sysMaterializeBuiltin(name)
		if mp == "" || !sysBuiltinUnitCurrent(mp, b) {
			return "", fmt.Errorf("%s: cannot write the generated unit file (needs administrator rights)", name)
		}
		u.Path = mp
	}
	st := sysReadState(name)
	exists, running, _ := sysServiceRuntime(name)
	_ = running
	_ = exists
	// collision check: a same-name service we do not own
	if st == nil {
		if e, _, _ := sysServiceRuntime(name); e {
			return "", fmt.Errorf("%s: a Windows service with this name already exists (not installed by winux systemctl); pick another unit name or use daemon-reload --adopt", name)
		}
	}
	if adopt && st == nil && exists {
		sysWriteState(&sysUnitState{Name: name, Backend: "wrapped", Trigger: "on-boot", UnitPath: u.Path, Hash: u.Hash, UpdatedAt: time.Now().Format(time.RFC3339)})
		return "adopted", nil
	}

	hadBackend := st != nil
	_ = hadBackend
	var oldKind string
	if st != nil {
		switch st.Backend {
		case "task", "login":
			oldKind = "task"
		default:
			oldKind = "service"
		}
	}
	newIsService := target == "wrapped" || target == "native" || target == "loginsvc"
	// cross-backend cleanup (task <-> service)
	if oldKind == "task" && newIsService {
		sysRemoveTask(name, false)
	} else if oldKind == "service" && !newIsService {
		sysRemoveService(name)
	}
	// also drop a stale login task when moving to an on-boot backend
	if target == "wrapped" || target == "native" || target == "task" {
		sysRemoveTask(name, true)
	}

	var result string
	switch target {
	case "login":
		if len(u.Env) > 0 {
			fmt.Fprintf(os.Stderr, "systemctl: warning: %s: login-task backend ignores Environment=\n", name)
		}
		if u.HostMode != "" {
			fmt.Fprintf(os.Stderr, "systemctl: warning: %s: login-task backend has no service host, ignored HostMode=%s\n", name, u.HostMode)
		}
		if err := sysInstallTask(name, u.ExecStart, sysCurrentUser(), true); err != nil {
			return "", err
		}
		if hadBackend {
			result = "updated"
		} else {
			result = "installed"
		}
	case "loginsvc":
		r, err := sysInstallServiceBackend(u, "native")
		if err != nil {
			return "", err
		}
		// the logon task helper runs from the unit's own host as well, so the
		// task keeps working after winux is moved or removed
		host, err := sysMaterializeHostMode(name, u.HostMode)
		if err != nil {
			return "", err
		}
		if err := sysInstallTask(name, fmt.Sprintf(`"%s" systemctl task-helper %s`, host, name), sysCurrentUser(), true); err != nil {
			return "", err
		}
		result = r
	case "task":
		if len(u.Env) > 0 {
			fmt.Fprintf(os.Stderr, "systemctl: warning: %s: task backend ignores Environment=\n", name)
		}
		if u.HostMode != "" {
			fmt.Fprintf(os.Stderr, "systemctl: warning: %s: task backend has no service host, ignored HostMode=%s\n", name, u.HostMode)
		}
		if err := sysInstallTask(name, u.ExecStart, "SYSTEM", false); err != nil {
			return "", err
		}
		if hadBackend {
			result = "updated"
		} else {
			result = "installed"
		}
	default: // wrapped / native
		if target == "native" && u.HostMode != "" {
			// a native service registers ExecStart itself: there is no winux host
			fmt.Fprintf(os.Stderr, "systemctl: warning: %s: native service has no winux host, ignored HostMode=%s\n", name, u.HostMode)
		}
		result, err := sysInstallServiceBackend(u, target)
		if err != nil {
			return "", err
		}
		if u.Restart != "" && u.Restart != "no" {
			sysSetFailureRecovery(name)
		}
		_ = result
	}
	if result == "" {
		result = "installed"
	}
	sysWriteState(&sysUnitState{Name: name, Backend: target, Trigger: u.Trigger, UnitPath: u.Path, Hash: u.Hash, UpdatedAt: time.Now().Format(time.RFC3339)})
	return result, nil
}

func sysSetFailureRecovery(name string) {
	// sc.exe failure ... restart — Windows ships sc.exe on every system
	exec.Command("sc.exe", "failure", name, "reset=", "86400", "actions=", "restart/5000/restart/10000/restart/30000").Run()
}

func sysRemoveService(name string) {
	m, err := sysOpenMgr()
	if err != nil {
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return
	}
	defer s.Close()
	st, err := s.Query()
	if err == nil && st.State != svc.Stopped {
		s.Control(svc.Stop)
		time.Sleep(500 * time.Millisecond)
	}
	s.Delete()
	// the unit no longer has a service, so drop its self-contained host too
	// (best effort: a still-running child keeps the file mapped for a moment)
	sysRemoveHost(name)
}

func sysCurrentUser() string {
	// "DOMAIN\User" of the interactive user
	u := os.Getenv("USERNAME")
	if d := os.Getenv("USERDOMAIN"); d != "" {
		return d + "\\" + u
	}
	return u
}

func sysFindUnitFile(name string) string {
	for _, dir := range sysUnitDirs() {
		p := filepath.Join(dir, name+".service")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// sysCanonicalName maps user-facing unit aliases to the real unit name. The
// winux sshd server is managed under the service name "winux-sshd" (the name
// `sshd --install` registers); `systemctl status sshd` / `start sshd` are
// convenient aliases for it, matching the well-known sshd.service name.
func sysCanonicalName(name string) string {
	switch name {
	case "sshd":
		return "winux-sshd"
	}
	return name
}

// sysBuiltinUnit returns the in-memory definition of a built-in unit that
// needs no user-written .service file. The unit file is materialized on
// demand (see sysMaterializeBuiltin) so wrapped svchost units get a real
// path and `cat` shows the definition.
func sysBuiltinUnit(name string) *sysUnit {
	switch name {
	case "winux-sshd":
		return &sysUnit{
			Name:        "winux-sshd",
			Description: "winux SSH server (sshd)",
			SvcType:     "wrapped",
			Trigger:     "on-boot",
			// ExecStart points at this unit's own host copy, not at the winux
			// installation: the sshd service then survives winux being moved,
			// upgraded or uninstalled (the copy is the same binary).
			ExecStart: fmt.Sprintf(`"%s" sshd`, sysHostPath("winux-sshd")),
			Restart:   "on-failure",
		}
	}
	return nil
}

func sysBuiltinUnitText(u *sysUnit) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + u.Description + "\n")
	b.WriteString("\n[Service]\n")
	// "wrapped" is an internal backend name; the on-disk Type is the
	// systemd-style "simple" (svchost wraps ExecStart either way).
	t := u.SvcType
	if t == "wrapped" {
		t = "simple"
	}
	b.WriteString("Type=" + t + "\n")
	b.WriteString("ExecStart=" + u.ExecStart + "\n")
	if u.HostMode != "" {
		b.WriteString("HostMode=" + u.HostMode + "\n")
	}
	if u.Restart != "" {
		b.WriteString("Restart=" + u.Restart + "\n")
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// sysMaterializeBuiltin writes the built-in unit definition to the default
// unit directory (needs admin; the caller falls back to the in-memory unit
// when the write fails). Returns the unit file path, or "".
func sysMaterializeBuiltin(name string) string {
	u := sysBuiltinUnit(name)
	if u == nil {
		return ""
	}
	dir := sysDefaultUnitDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	text := sysBuiltinUnitText(u)
	p := filepath.Join(dir, name+".service")
	if cur, err := os.ReadFile(p); err != nil || string(cur) != text {
		if err := os.WriteFile(p, []byte(text), 0644); err != nil {
			return ""
		}
	}
	return p
}

// sysBuiltinUnitCurrent reports whether the generated unit file still matches
// the definition this winux would write.
func sysBuiltinUnitCurrent(path string, b *sysUnit) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(data) == sysBuiltinUnitText(b)
}

// sysSameDir reports whether two paths name the same directory.
func sysSameDir(a, b string) bool {
	ca, errA := filepath.Abs(a)
	cb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
}

// ---------------------------------------------------------------------------
// Unit resolution

func sysResolveTarget(name string) (unit *sysUnit, rt *sysRuntime, st *sysUnitState, err error) {
	if !sysValidUnitName(name) {
		return nil, nil, nil, fmt.Errorf("illegal unit name %q", name)
	}
	builtin := sysBuiltinUnit(name)
	if p := sysFindUnitFile(name); p != "" {
		// winux owns the unit files it generates in %ProgramData%\systemctl\
		// units: a stale copy (an older format, a machine path baked in by
		// another winux or a test binary, an absolute winux.exe that moved)
		// must never win over the current definition. Regenerate when the text
		// differs — writable only when elevated, otherwise keep the in-memory
		// definition and say why. A user-written file of the same name in a
		// SYSTEMCTL_UNIT_PATH directory is left alone.
		if builtin != nil && sysSameDir(filepath.Dir(p), sysDefaultUnitDir()) && !sysBuiltinUnitCurrent(p, builtin) {
			mp := sysMaterializeBuiltin(name)
			if mp == "" {
				warn("systemctl", "%s: cannot refresh the generated unit file %s (needs administrator rights)", name, p)
				builtin.Path = p
				unit = builtin
			} else {
				p = mp
			}
		}
		if unit == nil {
			u, e := sysLoadUnit(p)
			if e == nil {
				unit = u
			} else if builtin != nil {
				// an unparsable generated file: rewrite it, else fall back to
				// the in-memory definition
				if mp := sysMaterializeBuiltin(name); mp != "" {
					if u2, e2 := sysLoadUnit(mp); e2 == nil {
						unit = u2
					} else {
						return nil, nil, nil, e2
					}
				} else {
					unit = builtin
				}
			} else {
				return nil, nil, nil, e
			}
		}
	} else if builtin != nil {
		// no file yet: materialize it so wrapped units get a real path for
		// `svchost -unit` (falls back to the in-memory definition)
		if mp := sysMaterializeBuiltin(name); mp != "" {
			if u2, e2 := sysLoadUnit(mp); e2 == nil {
				unit = u2
			} else {
				unit = builtin
			}
		} else {
			unit = builtin
		}
	}
	rt = sysGetRuntime(name)
	if unit == nil && rt == nil {
		return nil, nil, nil, fmt.Errorf("Unit %s.service not found (no unit file, no same-name service/task)", name)
	}
	st = sysReadState(name)
	return unit, rt, st, nil
}

// ---------------------------------------------------------------------------
// Commands

func cmdSystemctl(args []string) int {
	if len(args) == 0 {
		sysShowHelp()
		return 0
	}
	cmd := strings.ToLower(args[0])
	if cmd == "--version" || cmd == "-v" || cmd == "version" {
		fmt.Printf("winux systemctl %s (systemd-style service manager on Windows)\n", Version)
		fmt.Printf("from: %s\n", exePath())
		return 0
	}
	if cmd == "--help" || cmd == "-h" || cmd == "help" {
		sysShowHelp()
		return 0
	}
	rest := args[1:]
	full := sysHasFlag(rest, "-l") || sysHasFlag(rest, "--full") || sysHasFlag(rest, "--no-pager")
	dryRun := sysHasFlag(rest, "--dry-run")
	adopt := sysHasFlag(rest, "--adopt")
	names := sysFilterNames(rest)
	for i := range names {
		names[i] = sysCanonicalName(names[i])
	}

	needAdmin := map[string]bool{
		"daemon-reload": true, "daemon-reexec": true, "start": true, "stop": true,
		"restart": true, "enable": true, "disable": true, "mask": true, "unmask": true,
	}
	if needAdmin[cmd] && !isAdmin() {
		fmt.Fprintf(os.Stderr, "systemctl: %s needs administrator rights; run from an elevated shell (e.g. winux sudo systemctl %s ...)\n", cmd, cmd)
		return 1
	}

	code := 0
	req := func(cond bool, format string, a ...any) bool {
		if !cond {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
			code = 1
		}
		return cond
	}

	switch cmd {
	case "daemon-reload", "daemon-reexec":
		sysCmdDaemonReload(dryRun, adopt, &code)
	case "start":
		for _, n := range names {
			if sysTestMasked(n) {
				fmt.Fprintf(os.Stderr, "systemctl: %s.service is masked; unmask it first\n", n)
				code = 1
				continue
			}
			u, rt, _, err := sysResolveTarget(n)
			if !req(err == nil, "systemctl: %v", err) {
				continue
			}
			if u != nil {
				if sysReadState(n) == nil && sysGetRuntime(n) == nil {
					if _, e := sysInstallUnit(u, false); e != nil {
						fmt.Fprintf(os.Stderr, "systemctl: %v\n", e)
						code = 1
						continue
					}
				}
				rt = sysGetRuntime(n)
			}
			if rt == nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s: no runtime backend\n", n)
				code = 1
				continue
			}
			if rt.Running {
				fmt.Printf("%s already running\n", n)
				continue
			}
			if e := sysStartUnit(rt, n); e != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s start failed: %v\n", n, e)
				code = 1
			} else {
				fmt.Printf("%s started\n", n)
			}
		}
	case "stop":
		for _, n := range names {
			u, rt, _, err := sysResolveTarget(n)
			if !req(err == nil, "systemctl: %v", err) {
				continue
			}
			if u != nil && rt == nil {
				// not installed yet; nothing to stop
			}
			if rt == nil || !rt.Running {
				fmt.Printf("%s not running\n", n)
				continue
			}
			if e := sysStopUnit(rt, n); e != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s stop failed: %v\n", n, e)
				code = 1
			} else {
				fmt.Printf("%s stopped\n", n)
			}
		}
	case "restart":
		for _, n := range names {
			if sysTestMasked(n) {
				fmt.Fprintf(os.Stderr, "systemctl: %s.service is masked\n", n)
				code = 1
				continue
			}
			u, rt, _, err := sysResolveTarget(n)
			if !req(err == nil, "systemctl: %v", err) {
				continue
			}
			if u != nil && rt == nil {
				if _, e := sysInstallUnit(u, false); e != nil {
					fmt.Fprintf(os.Stderr, "systemctl: %v\n", e)
					code = 1
					continue
				}
				rt = sysGetRuntime(n)
			}
			if rt == nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s: no runtime backend\n", n)
				code = 1
				continue
			}
			if e := sysRestartUnit(rt, n); e != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s restart failed: %v\n", n, e)
				code = 1
			} else {
				fmt.Printf("%s restarted\n", n)
			}
		}
	case "enable":
		for _, n := range names {
			if sysTestMasked(n) {
				fmt.Fprintf(os.Stderr, "systemctl: %s.service is masked\n", n)
				code = 1
				continue
			}
			u, rt, _, err := sysResolveTarget(n)
			if !req(err == nil, "systemctl: %v", err) {
				continue
			}
			if u != nil && rt == nil {
				if _, e := sysInstallUnit(u, false); e != nil {
					fmt.Fprintf(os.Stderr, "systemctl: %v\n", e)
					code = 1
					continue
				}
				rt = sysGetRuntime(n)
			}
			if rt == nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s: no runtime backend\n", n)
				code = 1
				continue
			}
			if e := sysSetEnabled(rt, n, true); e != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s enable failed: %v\n", n, e)
				code = 1
			} else {
				fmt.Printf("%s enabled\n", n)
			}
		}
	case "disable":
		for _, n := range names {
			_, rt, _, err := sysResolveTarget(n)
			if !req(err == nil, "systemctl: %v", err) {
				continue
			}
			if rt == nil {
				// nothing installed: a unit that never runs at boot cannot
				// be "un-enabled", which is not an error in systemd either
				fmt.Printf("%s is not installed; nothing to disable\n", n)
				continue
			}
			if e := sysSetEnabled(rt, n, false); e != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %s disable failed: %v\n", n, e)
				code = 1
			} else {
				fmt.Printf("%s disabled\n", n)
			}
		}
	case "is-active":
		for _, n := range names {
			_, rt, _, err := sysResolveTarget(n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %v\n", err)
				code = 4
				continue
			}
			if rt != nil && rt.Running {
				fmt.Println("active")
			} else {
				fmt.Println("inactive")
				code = 3
			}
		}
	case "is-enabled":
		for _, n := range names {
			_, rt, _, err := sysResolveTarget(n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "systemctl: %v\n", err)
				code = 4
				continue
			}
			if rt != nil && rt.Enabled {
				fmt.Println("enabled")
			} else {
				fmt.Println("disabled")
				code = 1
			}
		}
	case "status":
		if len(names) == 0 {
			sysCmdListUnits()
		} else {
			for _, n := range names {
				sysCmdStatus(n, full, &code)
			}
		}
	case "list-units":
		sysCmdListUnits()
	case "list-unit-files":
		sysCmdListUnitFiles()
	case "cat":
		for _, n := range names {
			p := sysFindUnitFile(n)
			if p == "" {
				p = sysMaterializeBuiltin(n)
			}
			if p == "" {
				if u := sysBuiltinUnit(n); u != nil {
					fmt.Printf("# %s (built-in unit)\n", n+".service")
					fmt.Print(sysBuiltinUnitText(u))
					continue
				}
				fmt.Fprintf(os.Stderr, "systemctl: no files found for %s.service\n", n)
				code = 4
				continue
			}
			fmt.Printf("# %s\n", p)
			data, _ := os.ReadFile(p)
			fmt.Print(string(data))
		}
	case "journalctl":
		code = sysCmdJournalctl(rest)
	case "mask":
		for _, n := range names {
			os.MkdirAll(sysStateDir(), 0755)
			if rt := sysGetRuntime(n); rt != nil {
				sysRemoveTask(n, true)
				if rt.Kind == "task" || rt.Kind == "login" {
					sysRemoveTask(n, false)
				} else {
					sysRemoveService(n)
				}
			}
			os.WriteFile(filepath.Join(sysStateDir(), n+".masked"), []byte("masked at "+time.Now().Format(time.RFC3339)), 0644)
			fmt.Printf("%s.service masked\n", n)
		}
	case "unmask":
		for _, n := range names {
			p := filepath.Join(sysStateDir(), n+".masked")
			if err := os.Remove(p); err == nil {
				fmt.Printf("%s.service unmasked; run daemon-reload to reinstall\n", n)
			} else {
				fmt.Printf("%s.service is not masked\n", n)
			}
		}
	case "task-helper":
		// invoked by a login task: pull up the real service at logon
		if len(names) > 0 {
			m, err := sysOpenMgr()
			if err == nil {
				if s, e := m.OpenService(names[0]); e == nil {
					s.Start()
					s.Close()
				}
				m.Disconnect()
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "systemctl: unknown command: %s\n", cmd)
		sysShowHelp()
		return 1
	}
	return code
}

func sysHasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func sysFilterNames(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, strings.TrimSuffix(a, ".service"))
	}
	return out
}

// Windows SCM refusals that have no systemd equivalent. `systemctl disable`
// only means "do not start at boot" — it never blocks an explicit start — and
// stopping a unit that already stopped is a no-op, not an error. The SCM
// disagrees on both counts, so these two codes are translated in sysStartUnit
// and sysStopUnit.
const (
	errSvcDisabled  = syscall.Errno(1058) // ERROR_SERVICE_DISABLED
	errSvcNotActive = syscall.Errno(1062) // ERROR_SERVICE_NOT_ACTIVE
)

// sysStartTimeout bounds how long a start waits for the service to report
// running, so `systemctl start` fails loudly when the payload dies at once
// instead of claiming success.
const sysStartTimeout = 10 * time.Second

// withStartType copies cfg with a different start type.
func withStartType(cfg mgr.Config, st uint32) mgr.Config {
	cfg.StartType = st
	return cfg
}

// sysServiceHandle opens the SCM and the service; the caller must Close both.
func sysServiceHandle(name string) (*mgr.Mgr, *mgr.Service, error) {
	m, err := sysOpenMgr()
	if err != nil {
		return nil, nil, err
	}
	s, err := m.OpenService(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	return m, s, nil
}

// sysForceDemandStart flips a Disabled service to demand start so the SCM
// accepts a start request, returning the start type to restore (0 = untouched).
func sysForceDemandStart(s *mgr.Service) uint32 {
	cfg, err := s.Config()
	if err != nil || cfg.StartType != mgr.StartDisabled {
		return 0
	}
	if err := s.UpdateConfig(withStartType(cfg, mgr.StartManual)); err != nil {
		return 0
	}
	return mgr.StartDisabled
}

// sysRestoreStartType puts a temporarily changed start type back.
func sysRestoreStartType(s *mgr.Service, st uint32) {
	if st == 0 {
		return
	}
	if cfg, err := s.Config(); err == nil && cfg.StartType != st {
		s.UpdateConfig(withStartType(cfg, st))
	}
}

// sysWaitServiceRunning waits until the service reports running. A service
// that reaches stopped instead means the payload exited immediately (bad
// ExecStart, port already in use, missing binary) — report that rather than
// printing a cheerful "started".
func sysWaitServiceRunning(name string, timeout time.Duration) error {
	m, s, err := sysServiceHandle(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		switch st.State {
		case svc.Running:
			return nil
		case svc.Stopped:
			return fmt.Errorf("the service exited immediately (check ExecStart and 'systemctl journalctl -u %s')", name)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for the service to start")
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// sysWaitServiceStopped waits until the service reports stopped (restart must
// not race the previous instance's shutdown).
func sysWaitServiceStopped(name string, timeout time.Duration) error {
	m, s, err := sysServiceHandle(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for the service to stop")
		}
		time.Sleep(120 * time.Millisecond)
	}
}

func sysStartUnit(rt *sysRuntime, name string) error {
	if rt == nil {
		return fmt.Errorf("no runtime backend for %s", name)
	}
	switch rt.Kind {
	case "service", "loginsvc":
		m, s, err := sysServiceHandle(name)
		if err != nil {
			return err
		}
		defer m.Disconnect()
		defer s.Close()
		// Linux semantics: a disabled unit can still be started on demand.
		// Windows refuses (ERROR_SERVICE_DISABLED), so lift the start type for
		// the duration of the start and put it back afterwards — the unit
		// stays disabled across reboots, exactly like systemd.
		restore := sysForceDemandStart(s)
		repaired, err := sysStartService(name, s)
		if err != nil && restore == 0 && errors.Is(err, errSvcDisabled) {
			restore = sysForceDemandStart(s)
			if restore != 0 {
				repaired, err = sysStartService(name, s)
			}
		}
		if err == nil {
			err = sysWaitServiceRunning(name, sysStartTimeout)
		}
		sysRestoreStartType(s, restore)
		if repaired != "" && err == nil {
			warn("systemctl", "%s: 已修正服务注册（%s），服务已启动", name, repaired)
		}
		if err != nil {
			return sysStartFailure(name, s, err, repaired)
		}
		return nil
	case "task", "login":
		return sysStartStopTask(name, rt.Kind == "login", true)
	}
	return fmt.Errorf("unsupported backend %s", rt.Kind)
}

func sysStopUnit(rt *sysRuntime, name string) error {
	if rt == nil {
		return fmt.Errorf("no runtime backend for %s", name)
	}
	switch rt.Kind {
	case "service", "loginsvc":
		m, s, err := sysServiceHandle(name)
		if err != nil {
			return err
		}
		defer m.Disconnect()
		defer s.Close()
		if _, err := s.Control(svc.Stop); err != nil {
			// already stopped: stopping an inactive unit is not an error
			if errors.Is(err, errSvcNotActive) {
				return nil
			}
			return err
		}
		return sysWaitServiceStopped(name, sysStartTimeout)
	case "task", "login":
		return sysStartStopTask(name, rt.Kind == "login", false)
	}
	return fmt.Errorf("unsupported backend %s", rt.Kind)
}

func sysRestartUnit(rt *sysRuntime, name string) error {
	if rt == nil {
		return fmt.Errorf("no runtime backend for %s", name)
	}
	// Re-read the state: rt may be stale (e.g. the unit was stopped by hand).
	if fresh := sysGetRuntime(name); fresh != nil {
		rt = fresh
	}
	if rt.Kind == "service" || rt.Kind == "loginsvc" {
		// `restart` on a stopped unit is just a start — systemd never requires
		// a prior `start`.
		if rt.Running {
			if err := sysStopUnit(rt, name); err != nil {
				return err
			}
		}
		return sysStartUnit(rt, name)
	}
	if rt.Running {
		if err := sysStopUnit(rt, name); err != nil {
			return err
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return sysStartUnit(rt, name)
}

func sysSetEnabled(rt *sysRuntime, name string, enabled bool) error {
	if rt == nil {
		return fmt.Errorf("no runtime backend for %s", name)
	}
	switch rt.Kind {
	case "service", "loginsvc":
		m, err := sysOpenMgr()
		if err != nil {
			return err
		}
		defer m.Disconnect()
		s, err := m.OpenService(name)
		if err != nil {
			return err
		}
		defer s.Close()
		cfg, err := s.Config()
		if err != nil {
			return err
		}
		if enabled {
			cfg.StartType = mgr.StartAutomatic
		} else {
			cfg.StartType = mgr.StartDisabled
		}
		return s.UpdateConfig(cfg)
	case "task", "login":
		return sysTaskEnabled(name, rt.Kind == "login", enabled)
	}
	return fmt.Errorf("unsupported backend %s", rt.Kind)
}

func sysCmdDaemonReload(dryRun, adopt bool, code *int) {
	// Keep the built-in unit files (winux-sshd) in sync with the running
	// winux.exe before scanning, so a moved binary is picked up.
	sysMaterializeBuiltin("winux-sshd")
	units := map[string]*sysUnit{}
	for _, dir := range sysUnitDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".service") {
				continue
			}
			if u, err := sysLoadUnit(filepath.Join(dir, e.Name())); err == nil {
				units[u.Name] = u // later dirs override
			} else {
				fmt.Fprintf(os.Stderr, "systemctl: skipping invalid unit file %s: %v\n", e.Name(), err)
			}
		}
	}
	if !dryRun {
		os.MkdirAll(sysDefaultUnitDir(), 0755)
	}
	installed, updated, adopted, skipped := 0, 0, 0, 0
	for _, name := range sortedKeys(units) {
		u := units[name]
		if sysTestMasked(name) {
			fmt.Printf("  [skip] %s.service : masked\n", name)
			skipped++
			continue
		}
		if dryRun {
			fmt.Printf("  [plan] %s.service : %s backend / %s (ExecStart=%s)\n", name, sysTargetBackend(u), u.Trigger, u.ExecStart)
			continue
		}
		r, err := sysInstallUnit(u, adopt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s.service : %v\n", name, err)
			*code = 1
			continue
		}
		switch r {
		case "installed":
			fmt.Printf("  [install] %s.service\n", name)
			installed++
		case "updated":
			fmt.Printf("  [update] %s.service\n", name)
			updated++
		case "adopted":
			fmt.Printf("  [adopt] %s.service\n", name)
			adopted++
		}
	}
	// orphan cleanup: state exists but the unit file is gone
	entries, err := os.ReadDir(sysStateDir())
	if err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			orphan := strings.TrimSuffix(e.Name(), ".json")
			if _, ok := units[orphan]; ok {
				continue
			}
			if dryRun {
				fmt.Printf("  [plan] remove orphan backend: %s (unit file gone)\n", orphan)
				continue
			}
			st := sysReadState(orphan)
			sysRemoveTask(orphan, true)
			if st != nil && (st.Backend == "login" || st.Backend == "task") {
				sysRemoveTask(orphan, false)
			} else {
				sysRemoveService(orphan)
			}
			sysRemoveState(orphan)
			fmt.Printf("  [remove] orphan backend %s\n", orphan)
		}
	}
	if dryRun {
		fmt.Printf("dry run done (no changes). unit dirs: %s\n", strings.Join(sysUnitDirs(), " ; "))
	} else {
		fmt.Printf("daemon-reload done: new %d, updated %d, adopted %d, skipped %d, total %d units\n",
			installed, updated, adopted, skipped, len(units))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sysCmdStatus(name string, full bool, code *int) {
	u, rt, _, err := sysResolveTarget(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "systemctl: %v\n", err)
		*code = 4
		return
	}
	title := name + ".service"
	if u != nil && u.Description != "" {
		title += " - " + u.Description
	}
	fmt.Printf("● %s\n", title)
	if rt == nil {
		loaded := u.Path
		if loaded == "" && sysBuiltinUnit(name) != nil {
			loaded = "(built-in unit)"
		}
		fmt.Printf("   Loaded: loaded (%s; not installed)\n", loaded)
		fmt.Printf("   Active: inactive (not installed — run 'systemctl daemon-reload' or 'systemctl start %s')\n", name)
		*code = 3
		return
	}
	enabledStr := "disabled"
	if rt.Enabled {
		enabledStr = "enabled"
	}
	if u != nil {
		loaded := u.Path
		if loaded == "" && sysBuiltinUnit(name) != nil {
			loaded = "(built-in unit)"
		}
		fmt.Printf("   Loaded: loaded (%s; %s; backend: %s; trigger: %s)\n", loaded, enabledStr, backendName(name), rt.Trigger)
	} else {
		fmt.Printf("   Loaded: loaded (Windows service, no unit file; %s)\n", enabledStr)
	}
	if rt.Running {
		fmt.Printf("   Active: active (running)\n")
	} else {
		fmt.Printf("   Active: inactive (dead)\n")
	}
	switch rt.Kind {
	case "service", "loginsvc":
		fmt.Printf("  StartType: %s\n", rt.StartType)
		if u != nil {
			fmt.Printf("  ExecStart: %s\n", u.ExecStart)
		}
	case "login":
		if u != nil {
			fmt.Printf("  ExecStart: %s\n", u.ExecStart)
		}
		fmt.Printf("  LoginTask: %s (state: %s)\n", sysTaskPath+sysLoginTaskName(name), func() string {
			if rt.Enabled {
				return "Ready"
			}
			return "Disabled"
		}())
	case "task":
		if u != nil {
			fmt.Printf("  ExecStart: %s\n", u.ExecStart)
		}
	}
	if u != nil && sysBuiltinUnit(name) != nil && u.ExecStart != sysBuiltinUnit(name).ExecStart {
		fmt.Printf("   NOTE: the built-in unit's ExecStart points at a different winux.exe — run 'sudo systemctl daemon-reload' to resync it to the current binary\n")
	}
	if sysTestMasked(name) {
		fmt.Printf("    Masked: yes\n")
	}
	if !rt.Running {
		*code = 3
	}
	if full {
		if rt.Kind == "service" || rt.Kind == "loginsvc" {
			bin, acct := sysServiceDetail(name)
			if bin != "" {
				fmt.Printf("  BinPath:   %s\n", bin)
			}
			if acct != "" {
				fmt.Printf("  Account:   %s\n", acct)
			}
		}
		if u != nil {
			if u.WorkDir != "" {
				fmt.Printf("  WorkDir:   %s\n", u.WorkDir)
			}
			if u.User != "" {
				fmt.Printf("  User:      %s\n", u.User)
			}
			if u.Restart != "" && u.Restart != "no" {
				fmt.Printf("  Restart:   %s\n", u.Restart)
			}
			if u.KillTimeoutSec != "" {
				fmt.Printf("  KillTimeout:%ss\n", u.KillTimeoutSec)
			}
			if len(u.Env) > 0 {
				fmt.Printf("  Env:       %s\n", strings.Join(u.Env, "; "))
			}
		}
	}
}

func backendName(name string) string {
	if st := sysReadState(name); st != nil {
		return st.Backend
	}
	if rt := sysGetRuntime(name); rt != nil {
		if rt.Kind == "task" || rt.Kind == "login" {
			return "task"
		}
		return "foreign-service"
	}
	return "-"
}

func sysCmdListUnits() {
	seen := map[string]bool{}
	type row struct {
		unit, backend, active, enabled, desc string
	}
	var rows []row
	entries, err := os.ReadDir(sysStateDir())
	if err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			rt := sysGetRuntime(name)
			if rt == nil {
				continue
			}
			seen[name] = true
			desc := ""
			if p := sysFindUnitFile(name); p != "" {
				if u, e := sysLoadUnit(p); e == nil {
					desc = u.Description
				}
			}
			rows = append(rows, row{name + ".service", backendName(name), act(rt.Running), enab(rt.Enabled), desc})
		}
	}
	for _, dir := range sysUnitDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".service") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".service")
			if seen[name] {
				continue
			}
			seen[name] = true
			desc := ""
			if u, err := sysLoadUnit(filepath.Join(dir, e.Name())); err == nil {
				desc = u.Description
			}
			rows = append(rows, row{e.Name(), "-", "not-installed", "-", desc})
		}
	}
	if len(rows) == 0 {
		fmt.Println("no units installed. put *.service files in the unit dir and run: systemctl daemon-reload")
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].unit < rows[j].unit })
	fmt.Printf("%-32s %-14s %-14s %-9s %s\n", "UNIT", "BACKEND", "ACTIVE", "ENABLED", "DESCRIPTION")
	for _, r := range rows {
		fmt.Printf("%-32s %-14s %-14s %-9s %s\n", r.unit, r.backend, r.active, r.enabled, r.desc)
	}
}

func act(r bool) string {
	if r {
		return "active"
	}
	return "inactive"
}

func enab(r bool) string {
	if r {
		return "enabled"
	}
	return "disabled"
}

func sysCmdListUnitFiles() {
	seen := map[string]string{} // name -> path
	for _, dir := range sysUnitDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".service") {
				continue
			}
			seen[strings.TrimSuffix(e.Name(), ".service")] = filepath.Join(dir, e.Name())
		}
	}
	if len(seen) == 0 {
		fmt.Printf("no *.service files in the unit dirs: %s\n", strings.Join(sysUnitDirs(), " ; "))
		return
	}
	fmt.Printf("%-32s %-12s %s\n", "UNIT FILE", "STATE", "DESCRIPTION")
	for _, name := range sortedKeys(seen) {
		stateStr := "static"
		if sysTestMasked(name) {
			stateStr = "masked"
		} else if rt := sysGetRuntime(name); rt != nil {
			if rt.Enabled {
				stateStr = "enabled"
			} else {
				stateStr = "disabled"
			}
		}
		desc := ""
		if u, err := sysLoadUnit(seen[name]); err == nil {
			desc = u.Description
		}
		fmt.Printf("%-32s %-12s %s\n", name+".service", stateStr, desc)
	}
}

func sysShowHelp() {
	fmt.Print(`winux systemctl — systemd-style service manager on Windows

Usage: winux systemctl <command> [unit...] [options]

unit lifecycle:
  daemon-reload [--dry-run] [--adopt]  scan unit dirs, install/update/remove
  start <unit>    stop <unit>    restart <unit>
  enable <unit>   disable <unit>
  mask <unit>     unmask <unit>

  start/stop/restart follow systemd's rules, not the SCM's:
    * restart works on a stopped unit (it simply starts it);
    * a disabled unit can still be started and stopped — disable only means
      "do not start at boot" (the SCM refuses to start Disabled services, so
      winux lifts the start type for the start and restores it afterwards);
    * stopping an inactive unit, or starting a running one, is not an error.

query:
  status <unit>           detailed status; status (no args) lists units
  status <unit> -l        more detail (binPath/account/ExecStart/Env/...)
  is-active <unit>        active|inactive   is-enabled <unit> enabled|disabled
  cat <unit>              print the unit file
  list-units              list installed units
  list-unit-files         list unit files
  journalctl -u <unit> [-n N] [-f] [-r] [--since 'YYYY-MM-DD HH:mm:ss']

unit files are systemd-style *.service files; default dir:
  %ProgramData%\systemctl\units   (SYSTEMCTL_UNIT_PATH appends more dirs)

example unit (demo.service):
  [Unit]
  Description=My App
  [Service]
  Type=simple
  ExecStart=C:\myapp\app.exe --serve
  Restart=on-failure

[Service] keys: ExecStart, WorkingDirectory, Type (simple/win32),
StartupTrigger (on-boot/on-login), User (LocalSystem/LocalService/
NetworkService), Restart, Environment, KillTimeoutSec, SuccessExitStatus.
Mutating commands need administrator rights.
`)
}

func sysCmdJournalctl(args []string) int {
	name := ""
	lines := 50
	follow, reverse, end, linesAll := false, false, false, false
	since := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-u" || a == "--unit":
			if i+1 < len(args) {
				i++
				name = strings.TrimSuffix(args[i], ".service")
			}
		case strings.HasPrefix(a, "--unit="):
			name = strings.TrimSuffix(strings.TrimPrefix(a, "--unit="), ".service")
		case a == "-n" || a == "--lines":
			if i+1 < len(args) {
				i++
				lines = 50
				fmt.Sscanf(args[i], "%d", &lines)
				if strings.EqualFold(args[i], "all") || args[i] == "0" {
					linesAll = true
				}
			}
		case strings.HasPrefix(a, "--lines="):
			v := strings.TrimPrefix(a, "--lines=")
			linesAll = strings.EqualFold(v, "all") || v == "0"
			fmt.Sscanf(v, "%d", &lines)
		case a == "-f" || a == "--follow":
			follow = true
		case a == "-r" || a == "--reverse":
			reverse = true
		case a == "-e" || a == "--pager-end":
			end = true
		case a == "--no-pager":
		case strings.HasPrefix(a, "--since="):
			since = strings.TrimPrefix(a, "--since=")
		case a == "--since":
			if i+1 < len(args) {
				i++
				since = args[i]
			}
		default:
			fmt.Fprintf(os.Stderr, "journalctl: unrecognised argument: %s\n", a)
			return 1
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "journalctl needs -u <unit>. usage: systemctl journalctl -u <unit> [-n N] [-f] [-r] [--since 'YYYY-MM-DD HH:mm:ss']")
		return 1
	}
	logFile := sysWrapperLogPath(name)
	if logFile == "" {
		fmt.Fprintf(os.Stderr, "journalctl: %s: no wrapper log (wrapped services write %s; native services log to the Event Viewer)\n", name, sysUnitDirs()[0])
		return 4
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "journalctl: %v\n", err)
		return 4
	}
	raw := strings.Split(string(data), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	var sinceDt time.Time
	if since != "" {
		sinceDt, err = time.Parse("2006-01-02 15:04:05", since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "journalctl: invalid --since %q (use 'YYYY-MM-DD HH:mm:ss')\n", since)
			return 1
		}
	}
	all := raw
	if !sinceDt.IsZero() {
		var kept []string
		for _, l := range all {
			if m := sysLogTS(l); !m.IsZero() && m.Before(sinceDt) {
				continue
			}
			kept = append(kept, l)
		}
		all = kept
	}
	if end && linesAll {
		lines = 10
		linesAll = false
	}
	if reverse {
		if linesAll {
			all = sysLastN(all, 500)
			reverseSlice(all)
		} else {
			all = sysLastN(all, lines)
			reverseSlice(all)
		}
	} else if !linesAll && !follow {
		all = sysLastN(all, lines)
	}
	if len(all) > 0 {
		fmt.Printf("-- Logs begin at %s, end at %s. --\n", raw[0], sysLogTailTS(raw))
	}
	if follow {
		fmt.Println("-- Follow mode (Ctrl+C to exit). --")
		sysFollowLog(logFile, all)
	} else {
		for _, l := range all {
			fmt.Println(l)
		}
	}
	return 0
}

func sysWrapperLogPath(name string) string {
	for _, dir := range sysUnitDirs() {
		p := filepath.Join(dir, name+".wrapper.log")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if st := sysReadState(name); st != nil && st.UnitPath != "" {
		p := strings.TrimSuffix(st.UnitPath, filepath.Ext(st.UnitPath)) + ".wrapper.log"
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func sysLogTS(line string) time.Time {
	if len(line) >= 19 {
		if t, err := time.Parse("2006/01/02 15:04:05", line[:19]); err == nil {
			return t
		}
	}
	return time.Time{}
}

func sysLogTailTS(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if t := sysLogTS(lines[i]); !t.IsZero() {
			return t.Format("2006/01/02 15:04:05")
		}
	}
	return "?"
}

func sysLastN(lines []string, n int) []string {
	if n >= len(lines) {
		return lines
	}
	return lines[len(lines)-n:]
}

func reverseSlice(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func sysFollowLog(path string, initial []string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	// tail the initial content already printed; seek to end
	stat, _ := f.Stat()
	if stat != nil {
		f.Seek(stat.Size(), io.SeekStart)
	}
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			fmt.Print(string(buf[:n]))
		}
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			f.Seek(0, io.SeekCurrent)
		}
	}
}
