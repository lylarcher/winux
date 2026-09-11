package core_test

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func writable(t *testing.T, p string) bool {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()&0o200 != 0
}

func TestChmodOctalAndReference(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "f.txt", "x")
	if !writable(t, "f.txt") {
		t.Fatal("a fresh file should be writable")
	}
	if code, _, errOut := run(t, "", "chmod", "444", "f.txt"); code != 0 {
		t.Fatalf("chmod 444 failed: %s", errOut)
	}
	if writable(t, "f.txt") {
		t.Fatal("chmod 444 should clear the write bit")
	}
	if code, _, errOut := run(t, "", "chmod", "644", "f.txt"); code != 0 {
		t.Fatalf("chmod 644 failed: %s", errOut)
	}
	if !writable(t, "f.txt") {
		t.Fatal("chmod 644 should set the write bit")
	}
	// a four digit octal works too
	if code, _, _ := run(t, "", "chmod", "0444", "f.txt"); code != 0 || writable(t, "f.txt") {
		t.Fatal("chmod 0444 should clear the write bit")
	}
	// --reference copies the reference's state
	writeFile(t, "ref.txt", "r")
	if code, _, errOut := run(t, "", "chmod", "444", "ref.txt"); code != 0 {
		t.Fatalf("chmod 444 ref failed: %s", errOut)
	}
	if code, _, errOut := run(t, "", "chmod", "--reference=ref.txt", "f.txt"); code != 0 {
		t.Fatalf("chmod --reference failed: %s", errOut)
	}
	if writable(t, "f.txt") {
		t.Fatal("chmod --reference of a read-only file should clear the write bit")
	}
	// an invalid mode is an error
	if code, _, _ := run(t, "", "chmod", "999", "f.txt"); code != 1 {
		t.Fatal("chmod 999 should fail (9 is not an octal digit)")
	}
	if code, _, _ := run(t, "", "chmod", "u+q", "f.txt"); code != 1 {
		t.Fatal("chmod u+q should fail (unknown permission letter)")
	}
	// missing operands
	if code, _, _ := run(t, "", "chmod"); code != 1 {
		t.Fatal("bare chmod should fail")
	}
	if code, _, _ := run(t, "", "chmod", "644"); code != 1 {
		t.Fatal("chmod with only a mode should fail")
	}
}

func TestChmodSymbolic(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "f.txt", "x")
	if code, _, errOut := run(t, "", "chmod", "u-w", "f.txt"); code != 0 {
		t.Fatalf("chmod u-w failed: %s", errOut)
	}
	if writable(t, "f.txt") {
		t.Fatal("u-w should clear the write bit")
	}
	if code, _, _ := run(t, "", "chmod", "u+w", "f.txt"); code != 0 || !writable(t, "f.txt") {
		t.Fatal("u+w should restore the write bit")
	}
	if code, _, _ := run(t, "", "chmod", "a=r", "f.txt"); code != 0 || writable(t, "f.txt") {
		t.Fatal("a=r should clear the write bit")
	}
	if code, _, _ := run(t, "", "chmod", "u=rw,go=r", "f.txt"); code != 0 || !writable(t, "f.txt") {
		t.Fatal("u=rw,go=r should restore the write bit")
	}
	// g+w must not change the owner write bit
	if code, _, _ := run(t, "", "chmod", "444", "f.txt"); code != 0 {
		t.Fatal("chmod 444")
	}
	if code, _, errOut := run(t, "", "chmod", "g+w", "f.txt"); code != 0 {
		t.Fatalf("chmod g+w failed: %s", errOut)
	}
	if writable(t, "f.txt") {
		t.Fatal("g+w must not make the file writable (owner write untouched)")
	}
	// a-w clears it again
	if code, _, _ := run(t, "", "chmod", "a-w", "f.txt"); code != 0 || writable(t, "f.txt") {
		t.Fatal("a-w should clear the write bit")
	}
}

func TestChmodRecursiveAndVerbose(t *testing.T) {
	chdirTemp(t)
	writeFile(t, filepath.Join("tree", "sub", "a.txt"), "a")
	writeFile(t, filepath.Join("tree", "b.txt"), "b")
	if code, _, errOut := run(t, "", "chmod", "-R", "444", "tree"); code != 0 {
		t.Fatalf("chmod -R 444 failed: %s", errOut)
	}
	for _, p := range []string{"tree", filepath.Join("tree", "sub"), filepath.Join("tree", "sub", "a.txt"), filepath.Join("tree", "b.txt")} {
		if writable(t, p) {
			t.Fatalf("chmod -R left %s writable", p)
		}
	}
	// -c reports only changes; applying the same mode again reports none
	code, out, _ := run(t, "", "chmod", "-c", "444", "tree/b.txt")
	if code != 0 || strings.Contains(out, "changed") {
		t.Fatalf("chmod -c should be silent when nothing changes: %q", out)
	}
	code, out, _ = run(t, "", "chmod", "-c", "644", "tree/b.txt")
	if code != 0 || !strings.Contains(out, "changed") {
		t.Fatalf("chmod -c should report the change: %q", out)
	}
	// -v reports every file, including unchanged ones
	code, out, _ = run(t, "", "chmod", "-v", "644", "tree/b.txt")
	if code != 0 || !strings.Contains(out, "retained") {
		t.Fatalf("chmod -v should report the retained mode: %q", out)
	}
}

func TestChownDryRunAndValidation(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "f.txt", "x")
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	me := u.Username
	if i := strings.LastIndexByte(me, '\\'); i >= 0 {
		me = me[i+1:]
	}
	code, out, errOut := run(t, "", "chown", "--dry-run", me, "f.txt")
	if code != 0 {
		t.Fatalf("chown --dry-run failed: %s", errOut)
	}
	if !strings.Contains(out, "icacls") || !strings.Contains(out, "f.txt") || !strings.Contains(out, me) {
		t.Fatalf("chown --dry-run output = %q", out)
	}
	// an unknown user is rejected before any command runs
	if code, _, errOut := run(t, "", "chown", "--dry-run", "no-such-user-xyz", "f.txt"); code != 1 || !strings.Contains(errOut, "invalid user") {
		t.Fatalf("chown of an unknown user: code=%d err=%q", code, errOut)
	}
	// owner:group validates the group too and warns about the missing feature
	if code, _, errOut := run(t, "", "chown", "--dry-run", me+":no-such-group-xyz", "f.txt"); code != 1 || !strings.Contains(errOut, "invalid group") {
		t.Fatalf("chown with an unknown group: code=%d err=%q", code, errOut)
	}
	// missing operands
	if code, _, _ := run(t, "", "chown"); code != 1 {
		t.Fatal("bare chown should fail")
	}
	if code, _, _ := run(t, "", "chown", me); code != 1 {
		t.Fatal("chown with only an owner should fail")
	}
}

func TestChgrpHonestFailure(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "f.txt", "x")
	// an existing built-in group validates, then the command explains and fails
	code, _, errOut := run(t, "", "chgrp", "Users", "f.txt")
	if code != 1 || !strings.Contains(errOut, "no group owner") {
		t.Fatalf("chgrp on a valid group: code=%d err=%q", code, errOut)
	}
	// an unknown group is rejected during validation
	code, _, errOut = run(t, "", "chgrp", "no-such-group-xyz", "f.txt")
	if code != 1 || !strings.Contains(errOut, "invalid group") {
		t.Fatalf("chgrp with an unknown group: code=%d err=%q", code, errOut)
	}
}

func TestAccountDryRuns(t *testing.T) {
	chdirTemp(t)
	// useradd --dry-run prints the native commands, never touching the system
	code, out, errOut := run(t, "", "useradd", "--dry-run", "-m", "-c", "Test User", "-e", "2025-12-31", "-p", "s3cret", "-G", "Users", "bob")
	if code != 0 {
		t.Fatalf("useradd --dry-run failed: %s", errOut)
	}
	for _, want := range []string{"user bob s3cret /add", "/fullname:Test User", "/expires:12/31/2025", "localgroup Users bob /add", "mkdir -p C:\\Users\\bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("useradd --dry-run missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/active:no") {
		t.Errorf("useradd -p should not disable the account:\n%s", out)
	}
	// without -p the account is created disabled (locked)
	code, out, _ = run(t, "", "useradd", "--dry-run", "-m", "-G", "Users", "locked")
	if code != 0 || !strings.Contains(out, "/active:no") {
		t.Fatalf("useradd without -p should disable the account:\n%s", out)
	}
	// a bad expiry is rejected
	if code, _, _ := run(t, "", "useradd", "--dry-run", "-e", "not-a-date", "bob"); code != 1 {
		t.Fatal("useradd with a bad expiry should fail")
	}
	// userdel
	code, out, _ = run(t, "", "userdel", "--dry-run", "-r", "bob")
	if code != 0 || !strings.Contains(out, "user bob /delete") || !strings.Contains(out, "rm -rf C:\\Users\\bob") {
		t.Fatalf("userdel --dry-run:\n%s", out)
	}
	// usermod: lock, groups and rename
	code, out, _ = run(t, "", "usermod", "--dry-run", "-L", "-c", "New Name", "-aG", "Users", "bob")
	if code != 0 || !strings.Contains(out, "/active:no") || !strings.Contains(out, "/fullname:New Name") || !strings.Contains(out, "localgroup Users bob /add") {
		t.Fatalf("usermod --dry-run:\n%s", out)
	}
	code, out, _ = run(t, "", "usermod", "--dry-run", "-l", "bobby", "bob")
	if code != 0 || !strings.Contains(out, "Rename-LocalUser -Name 'bob' -NewName 'bobby'") {
		t.Fatalf("usermod -l --dry-run:\n%s", out)
	}
	if code, _, _ := run(t, "", "usermod", "--dry-run", "bob"); code != 1 {
		t.Fatal("usermod with no modification should fail")
	}
	// groupadd / groupdel
	code, out, _ = run(t, "", "groupadd", "--dry-run", "devs")
	if code != 0 || !strings.Contains(out, "localgroup devs /add") {
		t.Fatalf("groupadd --dry-run:\n%s", out)
	}
	code, out, _ = run(t, "", "groupdel", "--dry-run", "devs")
	if code != 0 || !strings.Contains(out, "localgroup devs /delete") {
		t.Fatalf("groupdel --dry-run:\n%s", out)
	}
	// missing logins
	if code, _, _ := run(t, "", "useradd", "--dry-run"); code != 1 {
		t.Fatal("useradd without a login should fail")
	}
	if code, _, _ := run(t, "", "groupadd", "--dry-run"); code != 1 {
		t.Fatal("groupadd without a group should fail")
	}
}
