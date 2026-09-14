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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

func sysRootDir() string        { return filepath.Join(os.Getenv("ProgramData"), "systemctl") }
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

func sysGetWrapperPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "winux.exe"
	}
	return exe
}

func sysInstallServiceBackend(u *sysUnit, backend string) (string, error) {
	name := u.Name
	var bin string
	if backend == "wrapped" {
		bin = fmt.Sprintf(`"%s" svchost -unit "%s"`, sysGetWrapperPath(), u.Path)
	} else {
		exe, args := sysSplitExecStart(u.ExecStart)
		if strings.ContainsAny(exe, " ") {
			exe = `"` + exe + `"`
		}
		if args != "" {
			bin = exe + " " + args
		} else {
			bin = exe
		}
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
		if err := sysInstallTask(name, fmt.Sprintf(`"%s" systemctl task-helper %s`, sysGetWrapperPath(), name), sysCurrentUser(), true); err != nil {
			return "", err
		}
		result = r
	case "task":
		if len(u.Env) > 0 {
			fmt.Fprintf(os.Stderr, "systemctl: warning: %s: task backend ignores Environment=\n", name)
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

func sysResolveTarget(name string) (unit *sysUnit, rt *sysRuntime, st *sysUnitState, err error) {
	if !sysValidUnitName(name) {
		return nil, nil, nil, fmt.Errorf("illegal unit name %q", name)
	}
	if p := sysFindUnitFile(name); p != "" {
		if u, e := sysLoadUnit(p); e == nil {
			unit = u
		} else {
			return nil, nil, nil, e
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

func sysStartUnit(rt *sysRuntime, name string) error {
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
		return s.Start()
	case "task", "login":
		return sysStartStopTask(name, rt.Kind == "login", true)
	}
	return fmt.Errorf("unsupported backend %s", rt.Kind)
}

func sysStopUnit(rt *sysRuntime, name string) error {
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
		_, err = s.Control(svc.Stop)
		return err
	case "task", "login":
		return sysStartStopTask(name, rt.Kind == "login", false)
	}
	return fmt.Errorf("unsupported backend %s", rt.Kind)
}

func sysRestartUnit(rt *sysRuntime, name string) error {
	if rt.Kind == "service" || rt.Kind == "loginsvc" {
		if err := sysStopUnit(rt, name); err != nil {
			return err
		}
		time.Sleep(1500 * time.Millisecond)
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
		fmt.Printf("   Loaded: loaded (%s; not installed)\n", u.Path)
		fmt.Printf("   Active: inactive (not installed — run 'systemctl daemon-reload' or 'systemctl start %s')\n", name)
		*code = 3
		return
	}
	enabledStr := "disabled"
	if rt.Enabled {
		enabledStr = "enabled"
	}
	if u != nil {
		fmt.Printf("   Loaded: loaded (%s; %s; backend: %s; trigger: %s)\n", u.Path, enabledStr, backendName(name), rt.Trigger)
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
