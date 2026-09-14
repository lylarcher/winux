//go:build windows

package core

// svchost is the built-in Windows service host: it runs a *.service unit's
// ExecStart as a child process, reports SCM status, and captures the child's
// stdout/stderr into <unit>.wrapper.log (the journalctl source). It is what
// the "wrapped" backend of `winux systemctl` points the service at:
//
//	winux.exe svchost -unit "C:\ProgramData\systemctl\units\demo.service"
//
// This is the Go port of the systemctl-win dsh-svchost.exe helper; because
// winux.exe embeds it, no separate helper binary is needed.

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func init() {
	addCmd("svchost", "built-in Windows service host for systemctl units (run by SCM)", cmdSvchost)
}

type svcUnitCfg struct {
	execStart   string
	workDir     string
	env         []string
	killTimeout time.Duration
	outputEnc   string
}

func svcLoadUnit(path string) (*svcUnitCfg, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ini := sysParseIni(data)
	sec := ini["service"]
	if sec == nil {
		return nil, fmt.Errorf("missing [Service] section")
	}
	execStart := sysIniFirst(sec, "execstart")
	if execStart == "" {
		return nil, fmt.Errorf("[Service] missing ExecStart")
	}
	cfg := &svcUnitCfg{execStart: execStart, killTimeout: 15 * time.Second}
	if v := sysIniFirst(sec, "workingdirectory"); v != "" {
		cfg.workDir = v
	}
	if v := sysIniFirst(sec, "killtimeoutsec"); v != "" {
		if d, e := time.ParseDuration(v + "s"); e == nil && d > 0 {
			cfg.killTimeout = d
		}
	}
	for _, e := range sec["environment"] {
		for _, pair := range sysSplitWords(e) {
			if strings.Contains(pair, "=") {
				cfg.env = append(cfg.env, pair)
			}
		}
	}
	cfg.outputEnc = strings.ToLower(sysIniFirst(sec, "execoutputencoding"))
	return cfg, nil
}

type svcRunner struct {
	cfg           *svcUnitCfg
	cmd           *exec.Cmd
	mu            sync.Mutex
	exited        chan struct{}
	code          int
	captureOutput bool
}

func (r *svcRunner) start() error {
	parts := sysSplitWords(r.cfg.execStart)
	if len(parts) == 0 {
		return fmt.Errorf("ExecStart is empty")
	}
	exe := parts[0]
	if !strings.ContainsAny(exe, `\/:`) {
		if p, err := exec.LookPath(exe); err == nil {
			exe = p
		}
	}
	cmd := exec.Command(exe, parts[1:]...)
	if r.cfg.workDir != "" {
		cmd.Dir = r.cfg.workDir
	} else {
		cmd.Dir = filepath.Dir(exe)
	}
	cmd.Env = append(os.Environ(), r.cfg.env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	var outR, errR io.Reader
	if r.captureOutput {
		if p, e := cmd.StdoutPipe(); e == nil {
			outR = p
		}
		if p, e := cmd.StderrPipe(); e == nil {
			errR = p
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if outR != nil {
		go svcCopyLines(outR, "", r.cfg.outputEnc)
	}
	if errR != nil {
		go svcCopyLines(errR, "[stderr] ", r.cfg.outputEnc)
	}
	r.mu.Lock()
	r.cmd = cmd
	r.exited = make(chan struct{})
	r.mu.Unlock()
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		r.mu.Lock()
		r.code = code
		close(r.exited)
		r.mu.Unlock()
		log.Printf("child exited, pid=%d code=%d", cmd.Process.Pid, code)
	}()
	log.Printf("child started, pid=%d, exe=%s", cmd.Process.Pid, exe)
	return nil
}

func (r *svcRunner) stop() {
	r.mu.Lock()
	cmd := r.cmd
	exited := r.exited
	r.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-exited:
		return
	default:
	}
	pid := cmd.Process.Pid
	log.Printf("stopping child tree, pid=%d, timeout=%s", pid, r.cfg.killTimeout)
	exec.Command("taskkill", "/pid", fmt.Sprint(pid), "/t").Run()
	select {
	case <-exited:
		return
	case <-time.After(r.cfg.killTimeout):
		log.Printf("timeout, force-killing child tree")
		exec.Command("taskkill", "/pid", fmt.Sprint(pid), "/t", "/f").Run()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
	}
}

func (r *svcRunner) exitCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

func svcCopyLines(r io.Reader, tag, enc string) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			log.Printf("%s%s", tag, svcDecodeOutput([]byte(line), enc))
		}
		if err != nil {
			return
		}
	}
}

func svcDecodeOutput(raw []byte, enc string) string {
	if len(raw) == 0 {
		return ""
	}
	cp := uint32(0)
	switch strings.ToLower(enc) {
	case "gbk", "gb2312", "gb18030", "cp936":
		cp = 936
	case "oem", "ansi":
		cp = 1
	default:
		return strings.TrimRight(string(raw), "\r\n")
	}
	wlen, err := windows.MultiByteToWideChar(cp, 0, &raw[0], int32(len(raw)), nil, 0)
	if err != nil || wlen <= 0 {
		return strings.TrimRight(string(raw), "\r\n")
	}
	buf := make([]uint16, wlen)
	if _, err := windows.MultiByteToWideChar(cp, 0, &raw[0], int32(len(raw)), &buf[0], wlen); err != nil {
		return strings.TrimRight(string(raw), "\r\n")
	}
	return strings.TrimRight(windows.UTF16ToString(buf), "\r\n")
}

func cmdSvchost(args []string) int {
	var unitPath string
	debug := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-unit", "--unit":
			if i+1 < len(args) {
				i++
				unitPath = args[i]
			}
		case "-debug", "--debug":
			debug = true
		case "-h", "--help", "-help":
			fmt.Println("Usage: winux svchost -unit <unit.service> [-debug]")
			fmt.Println("  built-in service host for systemctl units (run by SCM; -debug runs in the foreground)")
			return 0
		case "-v", "--version", "version":
			fmt.Printf("winux svchost %s\n", Version)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "svchost: unknown option %s\n", args[i])
			return 2
		}
	}
	if unitPath == "" {
		fmt.Fprintln(os.Stderr, "usage: winux svchost -unit <unit.service> [-debug]")
		return 2
	}
	cfg, err := svcLoadUnit(unitPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "svchost: parsing unit file failed: %v\n", err)
		return 1
	}
	logPath := strings.TrimSuffix(unitPath, filepath.Ext(unitPath)) + ".wrapper.log"
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(lf)
		defer lf.Close()
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("winux svchost started (version %s)", Version)

	rn := &svcRunner{cfg: cfg}
	if debug {
		if err := rn.start(); err != nil {
			fmt.Fprintf(os.Stderr, "svchost: %v\n", err)
			return 1
		}
		<-rn.exited
		return rn.exitCode()
	}
	name := strings.TrimSuffix(filepath.Base(unitPath), filepath.Ext(unitPath))
	if err := svc.Run(name, &svcHandler{rn: rn}); err != nil {
		log.Printf("svc.Run failed: %v", err)
		return 1
	}
	return 0
}

type svcHandler struct{ rn *svcRunner }

func (h *svcHandler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const cmds = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	rn := h.rn
	rn.captureOutput = true
	if err := rn.start(); err != nil {
		log.Printf("starting child failed: %v", err)
		changes <- svc.Status{State: svc.StopPending}
		return false, 1
	}
	changes <- svc.Status{State: svc.Running, Accepts: cmds}
	log.Printf("service entered Running state")

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				log.Printf("received %v, stopping", c.Cmd)
				changes <- svc.Status{State: svc.StopPending}
				rn.stop()
				return false, 0
			case svc.Interrogate:
				changes <- c.CurrentStatus
			}
		case <-rn.exited:
			code := rn.exitCode()
			log.Printf("child exited by itself (code=%d), service stops with it", code)
			return false, 0
		}
	}
}
