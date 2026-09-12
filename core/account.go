package core

// Account, group and permission commands. Windows has no /etc/passwd, no UID/
// GID and no POSIX permission bits, so these commands parse the GNU option
// sets faithfully and translate them to the native mechanisms (net.exe for
// users and groups, the NTFS read-only attribute for chmod, icacls for chown).
// Anything that has no Windows equivalent is reported honestly with the native
// alternative, never silently faked. Mutating operations re-run themselves
// through UAC when the current shell is not elevated.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("useradd", "create a new local user (maps to net user / net localgroup)", cmdUseradd)
	addCmd("userdel", "delete a local user account", cmdUserdel)
	addCmd("usermod", "modify a local user account", cmdUsermod)
	addCmd("groupadd", "create a new local group", cmdGroupadd)
	addCmd("groupdel", "delete a local group", cmdGroupdel)
	addCmd("chmod", "change mode bits (r/w mapped to the NTFS read-only attribute)", cmdChmod)
	addCmd("chown", "change file owner (via icacls)", cmdChown)
	addCmd("chgrp", "change file group (unsupported on Windows; explains the alternative)", cmdChgrp)
	addCmd("passwd", "change a local user's password", cmdPasswd)
}

// unsupportedError is returned by commands whose Windows back end does not
// exist (e.g. pktmon off Windows).
type unsupportedError struct{ tool string }

func (e *unsupportedError) Error() string { return e.tool + " is not available on this platform" }

// accountElevate re-runs this winux command elevated unless we already are.
// Returns -1 when the re-run was launched (the caller must stop) or 0 when the
// caller can proceed (we are already elevated).
func accountElevate(name string, args []string) int {
	if isElevated() {
		return 0
	}
	return elevateAndWait(name, append([]string{name}, args...), false)
}

// netExpiry converts a user-supplied expiry date (YYYY-MM-DD, MM/DD/YYYY or
// "never") into the format `net user /expires:` expects.
func netExpiry(s string) (string, bool) {
	if strings.EqualFold(s, "never") {
		return "never", true
	}
	for _, layout := range []string{"2006-01-02", "01/02/2006"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format("01/02/2006"), true
		}
	}
	return "", false
}

func splitGroups(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// printDryRun prints each native command that would be executed, quoting
// arguments that contain whitespace.
func printDryRun(cmds [][]string) {
	for _, c := range cmds {
		parts := make([]string, len(c))
		for i, a := range c {
			if strings.ContainsAny(a, " \t") {
				parts[i] = `"` + a + `"`
			} else {
				parts[i] = a
			}
		}
		fmt.Println(strings.Join(parts, " "))
	}
}

func runAll(cmds [][]string, name string) int {
	for _, c := range cmds {
		if code := runNative(c...); code != 0 {
			warn(name, "command failed with exit code %d: %s", code, strings.Join(c, " "))
			return 1
		}
	}
	return 0
}

// ----------------------------------------------------------------- useradd --

func cmdUseradd(args []string) int {
	f := NewFlags(
		&Flag{Name: "create-home", Short: 'm', Long: "create-home", Help: "create the home directory C:\\Users\\LOGIN"},
		&Flag{Name: "home-dir", Short: 'd', Long: "home-dir", HasArg: true, ArgName: "DIR", Help: "home directory (accepted; Windows keeps profile paths in the registry)"},
		&Flag{Name: "comment", Short: 'c', Long: "comment", HasArg: true, ArgName: "COMMENT", Help: "full name of the new account"},
		&Flag{Name: "password", Short: 'p', Long: "password", HasArg: true, ArgName: "PASS", Help: "initial password; without it the account is created disabled (locked)"},
		&Flag{Name: "gid", Short: 'g', Long: "gid", HasArg: true, ArgName: "GROUP", Help: "primary group (mapped to localgroup membership; Windows has no primary group)"},
		&Flag{Name: "groups", Short: 'G', Long: "groups", HasArg: true, ArgName: "GROUPS", Help: "supplementary groups, comma separated"},
		&Flag{Name: "shell", Short: 's', Long: "shell", HasArg: true, ArgName: "SHELL", Help: "accepted and ignored: cmd.exe and powershell.exe are the shells"},
		&Flag{Name: "uid", Short: 'u', Long: "uid", HasArg: true, ArgName: "UID", Help: "accepted and ignored: Windows has no UID"},
		&Flag{Name: "system", Short: 'r', Long: "system", Help: "accepted and ignored: Windows has no system-account flag"},
		&Flag{Name: "expiredate", Short: 'e', Long: "expiredate", HasArg: true, ArgName: "DATE", Help: "account expiry date (YYYY-MM-DD or MM/DD/YYYY, or 'never')"},
		&Flag{Name: "dry-run", Long: "dry-run", Help: "print the native commands without running them"},
	)
	if !parseOrDie("useradd", f, args) {
		return 2
	}
	if handleHelpVersion("useradd", "[OPTION]... LOGIN", f) {
		return 0
	}
	op := f.Positional
	if len(op) != 1 {
		warn("useradd", "exactly one LOGIN is required")
		return 1
	}
	login := op[0]
	net := systemExe("net.exe")

	cmd := []string{net, "user", login}
	if p := f.Str("password"); p != "" {
		cmd = append(cmd, p)
	}
	cmd = append(cmd, "/add")
	if c := f.Str("comment"); c != "" {
		cmd = append(cmd, "/fullname:"+c)
	}
	if e := f.Str("expiredate"); e != "" {
		ex, ok := netExpiry(e)
		if !ok {
			warn("useradd", "invalid expiration date '%s' (use YYYY-MM-DD, MM/DD/YYYY or 'never')", e)
			return 1
		}
		cmd = append(cmd, "/expires:"+ex)
	}
	if f.Str("password") == "" {
		cmd = append(cmd, "/active:no") // disabled account until usermod -U -p
	}
	var cmds [][]string
	cmds = append(cmds, cmd)
	for _, g := range splitGroups(f.Str("groups")) {
		cmds = append(cmds, []string{net, "localgroup", g, login, "/add"})
	}
	if g := f.Str("gid"); g != "" {
		cmds = append(cmds, []string{net, "localgroup", g, login, "/add"})
	}
	if f.Has("create-home") {
		home := `C:\Users\` + login
		if d := f.Str("home-dir"); d != "" {
			home = d
		}
		cmds = append(cmds, []string{"mkdir", "-p", home})
	}
	if f.Has("dry-run") {
		printDryRun(cmds)
		return 0
	}
	if code := accountElevate("useradd", args); code != 0 {
		return code
	}
	for _, c := range cmds {
		if len(c) > 0 && c[0] == "mkdir" {
			if err := os.MkdirAll(c[2], 0o777); err != nil {
				warn("useradd", "cannot create home directory '%s': %v", c[2], err)
				return 1
			}
			continue
		}
		if code := runNative(c...); code != 0 {
			warn("useradd", "command failed with exit code %d: %s", code, strings.Join(c, " "))
			return 1
		}
	}
	if f.Str("password") == "" {
		fmt.Fprintln(os.Stderr, "useradd: no -p given: the account was created disabled; enable with `usermod -p PASS -U LOGIN`")
	}
	return 0
}

// --------------------------------------------------------------- userdel ----

func cmdUserdel(args []string) int {
	f := NewFlags(
		&Flag{Name: "remove", Short: 'r', Long: "remove", Help: "remove the home directory C:\\Users\\LOGIN too"},
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "best effort: ignore a missing account and proceed with home removal"},
		&Flag{Name: "dry-run", Long: "dry-run", Help: "print the native commands without running them"},
	)
	if !parseOrDie("userdel", f, args) {
		return 2
	}
	if handleHelpVersion("userdel", "[OPTION]... LOGIN", f) {
		return 0
	}
	op := f.Positional
	if len(op) != 1 {
		warn("userdel", "exactly one LOGIN is required")
		return 1
	}
	login := op[0]
	var cmds [][]string
	cmds = append(cmds, []string{systemExe("net.exe"), "user", login, "/delete"})
	if f.Has("remove") {
		cmds = append(cmds, []string{"rm", "-rf", `C:\Users\` + login})
	}
	if f.Has("dry-run") {
		printDryRun(cmds)
		return 0
	}
	if code := accountElevate("userdel", args); code != 0 {
		return code
	}
	for _, c := range cmds {
		if c[0] == "rm" {
			if err := removeTree(c[2]); err != nil {
				if !f.Has("force") {
					warn("userdel", "cannot remove home directory '%s': %v", c[2], err)
					return 1
				}
			}
			continue
		}
		if code := runNative(c...); code != 0 {
			if f.Has("force") {
				warn("userdel", "account deletion failed (the user may be signed in); continuing")
				continue
			}
			warn("userdel", "account deletion failed: %s (the user may be signed in; use -f to force)", strings.Join(c, " "))
			return 1
		}
	}
	return 0
}

// --------------------------------------------------------------- usermod ----

func cmdUsermod(args []string) int {
	f := NewFlags(
		&Flag{Name: "login", Short: 'l', Long: "login", HasArg: true, ArgName: "NEWLOGIN", Help: "rename the account"},
		&Flag{Name: "home-dir", Short: 'd', Long: "home-dir", HasArg: true, ArgName: "DIR", Help: "accepted and ignored: profile paths are managed by Windows"},
		&Flag{Name: "append", Short: 'a', Long: "append", Help: "with -G: add to the groups (Windows groups are always appended)"},
		&Flag{Name: "gid", Short: 'g', Long: "gid", HasArg: true, ArgName: "GROUP", Help: "add the account to GROUP"},
		&Flag{Name: "groups", Short: 'G', Long: "groups", HasArg: true, ArgName: "GROUPS", Help: "supplementary groups, comma separated"},
		&Flag{Name: "lock", Short: 'L', Long: "lock", Help: "disable (lock) the account"},
		&Flag{Name: "unlock", Short: 'U', Long: "unlock", Help: "enable (unlock) the account"},
		&Flag{Name: "comment", Short: 'c', Long: "comment", HasArg: true, ArgName: "COMMENT", Help: "set the full name"},
		&Flag{Name: "password", Short: 'p', Long: "password", HasArg: true, ArgName: "PASS", Help: "set the password"},
		&Flag{Name: "shell", Short: 's', Long: "shell", HasArg: true, ArgName: "SHELL", Help: "accepted and ignored"},
		&Flag{Name: "uid", Short: 'u', Long: "uid", HasArg: true, ArgName: "UID", Help: "accepted and ignored"},
		&Flag{Name: "dry-run", Long: "dry-run", Help: "print the native commands without running them"},
	)
	if !parseOrDie("usermod", f, args) {
		return 2
	}
	if handleHelpVersion("usermod", "[OPTION]... LOGIN", f) {
		return 0
	}
	op := f.Positional
	if len(op) != 1 {
		warn("usermod", "exactly one LOGIN is required")
		return 1
	}
	login := op[0]
	net := systemExe("net.exe")

	var cmds [][]string
	if newName := f.Str("login"); newName != "" {
		cmds = append(cmds, []string{"rename-user", login, newName})
	}
	if f.Has("lock") {
		cmds = append(cmds, []string{net, "user", login, "/active:no"})
	}
	if f.Has("unlock") {
		cmds = append(cmds, []string{net, "user", login, "/active:yes"})
	}
	if p := f.Str("password"); p != "" {
		cmds = append(cmds, []string{net, "user", login, p})
	}
	if c := f.Str("comment"); c != "" {
		cmds = append(cmds, []string{net, "user", login, "/fullname:" + c})
	}
	for _, g := range splitGroups(f.Str("groups")) {
		cmds = append(cmds, []string{net, "localgroup", g, login, "/add"})
	}
	if g := f.Str("gid"); g != "" {
		cmds = append(cmds, []string{net, "localgroup", g, login, "/add"})
	}
	if len(cmds) == 0 {
		warn("usermod", "no modification requested (try -l, -c, -p, -L, -U, -G)")
		return 1
	}
	if f.Has("dry-run") {
		for _, c := range cmds {
			if c[0] == "rename-user" {
				fmt.Printf("Rename-LocalUser -Name '%s' -NewName '%s'\n", c[1], c[2])
			} else {
				fmt.Println(strings.Join(c, " "))
			}
		}
		return 0
	}
	if code := accountElevate("usermod", args); code != 0 {
		return code
	}
	for _, c := range cmds {
		if c[0] == "rename-user" {
			if err := renameLocalUser(c[1], c[2]); err != nil {
				warn("usermod", "cannot rename '%s' to '%s': %v", c[1], c[2], err)
				return 1
			}
			continue
		}
		if code := runNative(c...); code != 0 {
			warn("usermod", "command failed with exit code %d: %s", code, strings.Join(c, " "))
			return 1
		}
	}
	return 0
}

// renameLocalUser renames an account via PowerShell's Rename-LocalUser, falling
// back to wmic on systems where the module is missing.
func renameLocalUser(oldName, newName string) error {
	ps := systemExe("WindowsPowerShell\\v1.0\\powershell.exe")
	script := fmt.Sprintf("Rename-LocalUser -Name '%s' -NewName '%s'", strings.ReplaceAll(oldName, "'", "''"), strings.ReplaceAll(newName, "'", "''"))
	if code := runNative(ps, "-NoProfile", "-NonInteractive", "-Command", script); code == 0 {
		return nil
	}
	wmic := systemExe("wbem\\WMIC.exe")
	if code := runNative(wmic, "useraccount", "where", fmt.Sprintf("name='%s'", oldName), "call", "rename", newName); code == 0 {
		return nil
	}
	return fmt.Errorf("Rename-LocalUser failed (the LocalAccounts module or wmic is unavailable); rename manually in Computer Management")
}

// ------------------------------------------------------------- groupadd -----

func cmdGroupadd(args []string) int {
	f := NewFlags(
		&Flag{Name: "gid", Short: 'g', Long: "gid", HasArg: true, ArgName: "GID", Help: "accepted and ignored: Windows groups have no numeric id"},
		&Flag{Name: "system", Short: 'r', Long: "system", Help: "accepted and ignored"},
		&Flag{Name: "dry-run", Long: "dry-run", Help: "print the native commands without running them"},
	)
	if !parseOrDie("groupadd", f, args) {
		return 2
	}
	if handleHelpVersion("groupadd", "[OPTION]... GROUP", f) {
		return 0
	}
	op := f.Positional
	if len(op) != 1 {
		warn("groupadd", "exactly one GROUP is required")
		return 1
	}
	cmds := [][]string{{systemExe("net.exe"), "localgroup", op[0], "/add"}}
	if f.Has("dry-run") {
		printDryRun(cmds)
		return 0
	}
	if code := accountElevate("groupadd", args); code != 0 {
		return code
	}
	return runAll(cmds, "groupadd")
}

// ------------------------------------------------------------- groupdel -----

func cmdGroupdel(args []string) int {
	f := NewFlags(
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "best effort: report but ignore failures (members must be removed manually)"},
		&Flag{Name: "dry-run", Long: "dry-run", Help: "print the native commands without running them"},
	)
	if !parseOrDie("groupdel", f, args) {
		return 2
	}
	if handleHelpVersion("groupdel", "[OPTION]... GROUP", f) {
		return 0
	}
	op := f.Positional
	if len(op) != 1 {
		warn("groupdel", "exactly one GROUP is required")
		return 1
	}
	group := op[0]
	cmds := [][]string{{systemExe("net.exe"), "localgroup", group, "/delete"}}
	if f.Has("dry-run") {
		printDryRun(cmds)
		return 0
	}
	if code := accountElevate("groupdel", args); code != 0 {
		return code
	}
	if code := runAll(cmds, "groupdel"); code != 0 {
		if f.Has("force") {
			return 0
		}
		fmt.Fprintf(os.Stderr, "groupdel: cannot delete '%s' (it may still have members); remove members first with `net localgroup %s USER /delete` or use -f\n", group, group)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- chmod -----

func cmdChmod(args []string) int {
	f := NewFlags(
		&Flag{Name: "recursive", Short: 'R', Long: "recursive", Help: "change files and directories recursively"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "output a diagnostic for every file processed"},
		&Flag{Name: "changes", Short: 'c', Long: "changes", Help: "like verbose but report only when a change is made"},
		&Flag{Name: "silent", Short: 'f', Long: "silent", Help: "suppress most error messages"},
		&Flag{Name: "reference", Long: "reference", HasArg: true, ArgName: "RFILE", Help: "use RFILE's writable/read-only state instead of MODE"},
	)
	if !parseOrDie("chmod", f, args) {
		return 2
	}
	if handleHelpVersion("chmod", "[OPTION]... MODE[,MODE]... FILE...", f) {
		return 0
	}
	op := f.Positional
	var ops []modeOp
	var fixedWritable *bool
	if ref := f.Str("reference"); ref != "" {
		if len(op) == 0 {
			warn("chmod", "missing operand after '--reference=%s'", ref)
			return 1
		}
		st, err := os.Stat(ref)
		if err != nil {
			warn("chmod", "cannot access '%s': No such file or directory", ref)
			return 1
		}
		w := st.Mode().Perm()&0o200 != 0
		fixedWritable = &w
	} else {
		if len(op) < 2 {
			if len(op) == 0 {
				warn("chmod", "missing operand")
			} else {
				warn("chmod", "missing operand after '%s'", op[0])
			}
			return 1
		}
		var err error
		ops, err = parseModeSpec(op[0])
		if err != nil {
			warn("chmod", "%v", err)
			return 1
		}
		op = op[1:]
	}
	return chmodApply(op, func(wasW bool) bool {
		if fixedWritable != nil {
			return *fixedWritable
		}
		return modeWritable(ops, wasW)
	}, f)
}

// modeOp is one symbolic clause such as "u+w" or "go=r".
type modeOp struct {
	who   byte // 'u', 'g', 'o', 'a' (or 0 for "a")
	op    byte // '+', '-', '='
	perms string
}

// parseModeSpec interprets an octal mode (644, 0755) or a symbolic mode
// ([ugoa]*[+-=][rwxXst]..., comma separated). Only the owner-write bit has an
// NTFS effect (the read-only attribute); the other bits are accepted for
// compatibility.
func parseModeSpec(spec string) ([]modeOp, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("invalid mode: ''")
	}
	if isAllDigits(spec) && len(spec) <= 4 {
		m, err := strconv.ParseUint(spec, 8, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
		// an octal mode fixes the owner-write bit: encode it as an '=' clause
		return []modeOp{{who: 'a', op: '=', perms: writablePerms(m&0o200 != 0)}}, nil
	}
	var ops []modeOp
	for _, clause := range strings.Split(spec, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
		op := modeOp{who: 'a'}
		i := 0
		for i < len(clause) && strings.ContainsRune("ugoa", rune(clause[i])) {
			op.who = clause[i]
			i++
		}
		if i >= len(clause) || !strings.ContainsRune("+-=", rune(clause[i])) {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
		op.op = clause[i]
		i++
		op.perms = clause[i:]
		if strings.ContainsAny(op.perms, "ugoa+-=") {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
		for _, p := range op.perms {
			if !strings.ContainsRune("rwxXst", p) {
				return nil, fmt.Errorf("invalid mode: '%s'", spec)
			}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func writablePerms(w bool) string {
	if w {
		return "w"
	}
	return ""
}

// modeWritable computes the effective owner-write bit by applying the symbolic
// operations on top of the file's current state.
func modeWritable(ops []modeOp, current bool) bool {
	w := current
	for _, op := range ops {
		affectsOwner := op.who == 'a' || op.who == 'u'
		hasW := strings.Contains(op.perms, "w")
		switch op.op {
		case '+':
			if affectsOwner && hasW {
				w = true
			}
		case '-':
			if affectsOwner && hasW {
				w = false
			}
		case '=':
			if op.who == 'a' {
				w = hasW
			} else if op.who == 'u' {
				w = hasW
			}
			// g=/o= never touch the owner write bit
		}
	}
	return w
}

// chmodApply walks the targets (recursively with -R) and flips the NTFS
// read-only attribute to reflect the effective owner-write bit.
func chmodApply(targets []string, want func(wasW bool) bool, f *Flags) int {
	verbose := f.Has("verbose") || f.Has("changes")
	changes := f.Has("changes")
	exit := 0
	var walk func(p string)
	walk = func(p string) {
		st, err := os.Stat(p)
		if err != nil {
			if !f.Has("silent") {
				warn("chmod", "cannot access '%s': %v", p, err)
			}
			exit = 1
			return
		}
		wasW := st.Mode().Perm()&0o200 != 0
		newW := want(wasW)
		if wasW != newW {
			if err := os.Chmod(p, map[bool]os.FileMode{true: 0o666, false: 0o444}[newW]); err != nil {
				if !f.Has("silent") {
					warn("chmod", "cannot change mode of '%s': %v", p, err)
				}
				exit = 1
				return
			}
			if verbose {
				fmt.Printf("chmod: mode of '%s' changed from %s to %s\n", p, modeString(wasW), modeString(newW))
			}
		} else if verbose && !changes {
			fmt.Printf("chmod: mode of '%s' retained as %s\n", p, modeString(newW))
		}
	}
	for _, t := range targets {
		if f.Has("recursive") {
			err := filepath.WalkDir(t, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				walk(p)
				return nil
			})
			if err != nil && !f.Has("silent") {
				warn("chmod", "cannot access '%s': %v", t, err)
				exit = 1
			}
		} else {
			walk(t)
		}
	}
	return exit
}

// modeString renders the effective mapped permission string (only the owner
// write bit differs, since NTFS exposes nothing else).
func modeString(writable bool) string {
	if writable {
		return "rw-r--r--"
	}
	return "r--r--r--"
}

// ---------------------------------------------------------------- chown -----

func cmdChown(args []string) int {
	f := NewFlags(
		&Flag{Name: "recursive", Short: 'R', Long: "recursive", Help: "operate on files and directories recursively"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "output a diagnostic for every file processed"},
		&Flag{Name: "changes", Short: 'c', Long: "changes", Help: "like verbose but report only when a change is made"},
		&Flag{Name: "silent", Short: 'f', Long: "silent", Help: "suppress most error messages"},
		&Flag{Name: "reference", Long: "reference", HasArg: true, ArgName: "RFILE", Help: "use RFILE's owner instead of OWNER"},
		&Flag{Name: "no-dereference", Short: 'h', Long: "no-dereference", Help: "accepted (symlink owners are not manageable on Windows)"},
		&Flag{Name: "dry-run", Long: "dry-run", Help: "print the native commands without running them"},
	)
	if !parseOrDie("chown", f, args) {
		return 2
	}
	if handleHelpVersion("chown", "[OPTION]... [OWNER][:[GROUP]] FILE...", f) {
		return 0
	}
	op := f.Positional
	owner := ""
	group := ""
	if ref := f.Str("reference"); ref != "" {
		o, err := fileOwner(ref)
		if err != nil {
			warn("chown", "cannot read the owner of '%s': %v", ref, err)
			return 1
		}
		owner = o
	} else {
		if len(op) < 2 {
			warn("chown", "missing operand (need OWNER and at least one FILE)")
			return 1
		}
		spec := op[0]
		op = op[1:]
		if i := strings.IndexByte(spec, ':'); i >= 0 {
			owner, group = spec[:i], spec[i+1:]
		} else {
			owner = spec
		}
		if owner == "" {
			warn("chown", "missing owner")
			return 1
		}
		if _, err := queryNative(systemExe("net.exe"), "user", owner); err != nil {
			warn("chown", "invalid user: '%s'", owner)
			return 1
		}
		if group != "" {
			if _, err := queryNative(systemExe("net.exe"), "localgroup", group); err != nil {
				warn("chown", "invalid group: '%s'", group)
				return 1
			}
			fmt.Fprintf(os.Stderr, "chown: Windows has no group ownership; the GROUP part is accepted for compatibility only\n")
		}
	}
	var cmds [][]string
	for _, p := range op {
		c := []string{systemExe("icacls.exe"), p, "/setowner", owner}
		if f.Has("recursive") {
			c = append(c, "/T")
		}
		cmds = append(cmds, c)
	}
	if f.Has("dry-run") {
		printDryRun(cmds)
		return 0
	}
	if code := accountElevate("chown", args); code != 0 {
		return code
	}
	for _, c := range cmds {
		if code := runNative(c...); code != 0 {
			if !f.Has("silent") {
				warn("chown", "cannot change owner of '%s' (elevation may be required): %s", c[1], strings.Join(c, " "))
			}
			return 1
		}
		if f.Has("verbose") || f.Has("changes") {
			fmt.Printf("chown: changed owner of '%s' to %s\n", c[1], owner)
		}
	}
	return 0
}

// ---------------------------------------------------------------- chgrp -----

func cmdChgrp(args []string) int {
	f := NewFlags(
		&Flag{Name: "recursive", Short: 'R', Long: "recursive", Help: "accepted"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "accepted"},
		&Flag{Name: "changes", Short: 'c', Long: "changes", Help: "accepted"},
		&Flag{Name: "silent", Short: 'f', Long: "silent", Help: "accepted"},
		&Flag{Name: "reference", Long: "reference", HasArg: true, ArgName: "RFILE", Help: "accepted"},
		&Flag{Name: "no-dereference", Short: 'h', Long: "no-dereference", Help: "accepted"},
	)
	if !parseOrDie("chgrp", f, args) {
		return 2
	}
	if handleHelpVersion("chgrp", "[OPTION]... GROUP FILE...", f) {
		return 0
	}
	op := f.Positional
	if len(op) < 2 {
		warn("chgrp", "missing operand (need GROUP and at least one FILE)")
		return 1
	}
	group := op[0]
	if _, err := queryNative(systemExe("net.exe"), "localgroup", group); err != nil {
		warn("chgrp", "invalid group: '%s'", group)
		return 1
	}
	fmt.Fprintf(os.Stderr, "chgrp: Windows files have no group owner (ACLs grant groups access instead);\n")
	fmt.Fprintf(os.Stderr, "       grant group rights with: icacls FILE /grant '%s:(OI)(CI)R'\n", group)
	return 1
}

// --------------------------------------------------------------- passwd -----

// localUserStatus carries the native account facts `passwd -S` reports.
// localUserInfo (account_windows.go / account_other.go) fills it.
type localUserStatus struct {
	Source           string
	Enabled          bool
	PasswordRequired bool
	PasswordLastSet  string // MM/DD/YYYY or "never"
	PasswordExpires  string // MM/DD/YYYY or "never"
}

// errUserNotFound is returned by localUserInfo when the account does not exist,
// as opposed to a database/query failure.
var errUserNotFound = errors.New("no such user")

// cmdPasswd changes a local user's password. With no operand it changes the
// current user's password (self-service: the old password is checked through
// the NetUserChangePassword API, no elevation needed). With a USER operand it
// changes that account's password elevated, like `net user USER PASS`. Only
// local accounts are supported: Microsoft accounts (PrincipalSource
// MicrosoftAccount) and domain accounts are refused — their passwords are
// managed online/elsewhere. -S prints a GNU-shaped status line.
func cmdPasswd(args []string) int {
	f := NewFlags(
		&Flag{Name: "status", Short: 'S', Long: "status", Help: "report password status for USER (default: the current user)"},
	)
	if !parseOrDie("passwd", f, args) {
		return 2
	}
	if handleHelpVersion("passwd", "[OPTION]... [USER]", f) {
		return 0
	}
	op := f.Positional
	if len(op) > 1 {
		warn("passwd", "extra operand '%s'", op[1])
		return 1
	}
	target := shortUserName(currentUserName())
	if len(op) == 1 {
		target = op[0]
	}
	if f.Has("status") {
		return passwdStatus(target)
	}
	if len(op) == 0 {
		return passwdSelf()
	}
	return passwdOther(target)
}

// passwdSelf changes the current user's password, prompting for the old and
// the new password with console echo disabled.
func passwdSelf() int {
	name := shortUserName(currentUserName())
	if info, err := localUserInfo(name); err == nil && strings.EqualFold(info.Source, "MicrosoftAccount") {
		fmt.Fprintf(os.Stderr, "passwd: '%s' is a Microsoft account; its password is managed online and cannot be changed locally (use account.microsoft.com)\n", name)
		return 1
	}
	oldPass, hidden := readPassword("Current password: ")
	if !hidden {
		return passwdNoConsole()
	}
	newPass, hidden := readPassword("New password: ")
	if !hidden {
		return passwdNoConsole()
	}
	confirm, hidden := readPassword("Retype new password: ")
	if !hidden {
		return passwdNoConsole()
	}
	if newPass != confirm {
		fmt.Fprintln(os.Stderr, "passwd: passwords do not match")
		return 1
	}
	if newPass == "" {
		fmt.Fprintln(os.Stderr, "passwd: the password may not be empty")
		return 1
	}
	if err := passwdChangeSelf(oldPass, newPass); err != nil {
		fmt.Fprintf(os.Stderr, "passwd: %v\n", err)
		return 1
	}
	fmt.Printf("passwd: password for '%s' changed successfully.\n", name)
	return 0
}

// passwdOther changes a named account's password. The account must be a local
// account (Microsoft/domain accounts are refused); the new password is read
// hidden and applied elevated via `net user USER PASS`.
func passwdOther(target string) int {
	info, err := localUserInfo(target)
	if err != nil {
		if errors.Is(err, errUserNotFound) {
			fmt.Fprintf(os.Stderr, "passwd: user '%s' does not exist\n", target)
		} else {
			fmt.Fprintf(os.Stderr, "passwd: %v\n", err)
		}
		return 1
	}
	switch strings.ToLower(info.Source) {
	case "microsoftaccount":
		fmt.Fprintf(os.Stderr, "passwd: '%s' is a Microsoft account; its password is managed online and cannot be changed locally (use account.microsoft.com)\n", target)
		return 1
	case "active directory", "activedirectory", "azuread", "azure ad":
		fmt.Fprintf(os.Stderr, "passwd: '%s' is not a local account (source: %s); only local user passwords can be changed\n", target, info.Source)
		return 1
	}
	newPass, hidden := readPassword("New password: ")
	if !hidden {
		return passwdNoConsole()
	}
	confirm, hidden := readPassword("Retype new password: ")
	if !hidden {
		return passwdNoConsole()
	}
	if newPass != confirm {
		fmt.Fprintln(os.Stderr, "passwd: passwords do not match")
		return 1
	}
	if newPass == "" {
		fmt.Fprintln(os.Stderr, "passwd: the password may not be empty")
		return 1
	}
	// Apply elevated: net user writes its (localized) output to a temp file the
	// elevated cmd can reach; we surface it when the change fails.
	tmp, err := os.CreateTemp("", "winux-passwd-*.txt")
	if err != nil {
		warn("passwd", "cannot create a temp file for the result: %v", err)
		return 1
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	cmdLine := fmt.Sprintf("net user %s %s > \"%s\" 2>&1", quoteCmdArg(target), quoteCmdArg(newPass), path)
	if code := elevateAndWait("passwd", []string{"/c", cmdLine}, true); code != 0 {
		if b, rerr := os.ReadFile(path); rerr == nil && len(b) > 0 {
			os.Stderr.Write(b)
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "passwd: could not change the password for '%s' (exit code %d)\n", target, code)
		return 1
	}
	fmt.Printf("passwd: password for '%s' changed successfully.\n", target)
	return 0
}

// passwdStatus prints a GNU-shaped status line for USER plus a short native
// summary. The days columns are approximated: min=0, max = days until expiry
// (99999 when the password never expires), warn=7, inactive=-1.
func passwdStatus(target string) int {
	info, err := localUserInfo(target)
	if err != nil {
		if errors.Is(err, errUserNotFound) {
			fmt.Fprintf(os.Stderr, "passwd: user '%s' does not exist\n", target)
		} else {
			fmt.Fprintf(os.Stderr, "passwd: %v\n", err)
		}
		return 1
	}
	state := "P"
	switch {
	case !info.Enabled:
		state = "L"
	case info.PasswordLastSet == "never":
		state = "NP"
	}
	maxDays := "99999"
	if ls, err1 := time.Parse("01/02/2006", info.PasswordLastSet); err1 == nil {
		if ex, err2 := time.Parse("01/02/2006", info.PasswordExpires); err2 == nil && ex.After(ls) {
			maxDays = strconv.Itoa(int(ex.Sub(ls).Hours() / 24))
		}
	}
	fmt.Printf("%s %s %s 0 %s 7 -1\n", target, state, info.PasswordLastSet, maxDays)
	fmt.Printf("password required: %v; account enabled: %v; source: %s\n",
		info.PasswordRequired, info.Enabled, info.Source)
	return 0
}

// passwdNoConsole reports the honest failure when stdin is not a console and
// the password cannot be typed hidden.
func passwdNoConsole() int {
	fmt.Fprintln(os.Stderr, "passwd: cannot read a password securely (stdin is not a console); run passwd from a console")
	return 1
}

// quoteCmdArg wraps one argument for a cmd.exe command line.
func quoteCmdArg(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
