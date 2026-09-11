package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func init() {
	addCmd("cp", "copy files and directories", cmdCp)
	addCmd("rm", "remove files or directories", cmdRm)
	addCmd("mv", "move (rename) files and directories", cmdMv)
	addCmd("mkdir", "make directories", cmdMkdir)
	addCmd("touch", "create empty files / change file timestamps", cmdTouch)
	addCmd("ln", "make hard and symbolic links", cmdLn)
}

// ------------------------------------------------------------------ cp -----

type cpOpts struct {
	recursive   bool
	force       bool
	interactive bool
	noClobber   bool
	preserve    bool
	update      bool
	verbose     bool
	hardlink    bool
	symlink     bool
	noDeref     bool
}

func cmdCp(args []string) int {
	f := NewFlags(
		&Flag{Name: "archive", Short: 'a', Long: "archive", Help: "same as -rp (archive mode)"},
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "remove destination file if it cannot be opened"},
		&Flag{Name: "interactive", Short: 'i', Long: "interactive", Help: "prompt before overwrite"},
		&Flag{Name: "no-clobber", Short: 'n', Long: "no-clobber", Help: "do not overwrite an existing file"},
		&Flag{Name: "preserve", Short: 'p', Long: "preserve", OptArg: true, ArgName: "ATTR_LIST", Help: "preserve timestamps (--preserve=all accepted)"},
		&Flag{Name: "recursive", Short: 'r', Long: "recursive", Help: "copy directories recursively"},
		&Flag{Name: "recursive", Short: 'R', Help: "same as -r"},
		&Flag{Name: "no-dereference", Short: 'd', Help: "copy symbolic links as symbolic links"},
		&Flag{Name: "no-dereference", Short: 'P', Help: "never follow symbolic links in SOURCE"},
		&Flag{Name: "dereference", Short: 'L', Long: "dereference", Help: "always follow symbolic links (default)"},
		&Flag{Name: "link", Short: 'l', Long: "link", Help: "hard link files instead of copying"},
		&Flag{Name: "symbolic-link", Short: 's', Long: "symbolic-link", Help: "make symbolic links instead of copying"},
		&Flag{Name: "update", Short: 'u', Long: "update", Help: "copy only when SOURCE is newer than the destination"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "explain what is being done"},
		&Flag{Name: "target-directory", Short: 't', Long: "target-directory", HasArg: true, ArgName: "DIR", Help: "copy all SOURCE arguments into DIR"},
	)
	if !parseOrDie("cp", f, args) {
		return 2
	}
	if handleHelpVersion("cp", "[OPTION]... SOURCE... DEST", f) {
		return 0
	}

	o := &cpOpts{
		recursive:   f.Has("recursive") || f.Has("archive"),
		force:       f.Has("force"),
		interactive: f.Has("interactive"),
		noClobber:   f.Has("no-clobber"),
		preserve:    f.Has("preserve") || f.Has("archive"),
		update:      f.Has("update"),
		verbose:     f.Has("verbose"),
		hardlink:    f.Has("link"),
		symlink:     f.Has("symbolic-link"),
		noDeref:     f.Has("no-dereference"),
	}
	op := expandGlobs(f.Positional)
	sources, dest, ok := resolveSources("cp", f, op)
	if !ok {
		return 1
	}
	if len(sources) > 1 && !isDir(dest) {
		warn("cp", "target '%s' is not a directory", dest)
		return 1
	}
	exit := 0
	for _, src := range sources {
		if err := cpEntry(src, dest, o); err != nil {
			warn("cp", "%v", err)
			exit = 1
		}
	}
	return exit
}

func cpEntry(src, dest string, o *cpOpts) error {
	src = trimSep(src)
	if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, `\`) {
		if !isDir(dest) {
			return fmt.Errorf("cannot create directory '%s': No such file or directory", dest)
		}
	}
	st, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("cannot stat '%s': %v", src, err)
	}
	target := dest
	if isDir(dest) {
		target = filepath.Join(dest, filepath.Base(src))
	}
	if samePath(src, target) {
		return fmt.Errorf("'%s' and '%s' are the same file", src, target)
	}
	if st.IsDir() {
		if !o.recursive {
			return fmt.Errorf("-r not specified; omitting directory '%s'", src)
		}
		if isInside(src, target) {
			return fmt.Errorf("cannot copy a directory, '%s', into itself, '%s'", src, target)
		}
	} else if isDir(target) {
		// cp file dir/ handled by the branch above; nothing else to do here
		_ = target
	}
	if o.interactive && fileExists(target) && !isDir(target) {
		if !askYesNo(fmt.Sprintf("cp: overwrite '%s'? ", target)) {
			return nil
		}
	}
	if o.noClobber && fileExists(target) {
		return nil
	}
	if o.update && fileExists(target) {
		if dst, err := os.Stat(target); err == nil && dst.ModTime().After(st.ModTime()) {
			return nil
		}
	}
	makeWritable(target)
	return copyTree(src, target, o)
}

func copyTree(src, dst string, o *cpOpts) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.IsDir() {
		if err := os.MkdirAll(dst, 0o777); err != nil {
			return fmt.Errorf("cannot create directory '%s': %v", dst, err)
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return fmt.Errorf("cannot read directory '%s': %v", src, err)
		}
		for _, e := range ents {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), o); err != nil {
				return err
			}
		}
		if o.preserve {
			os.Chtimes(dst, st.ModTime(), st.ModTime())
		}
		return nil
	}
	return copyLeaf(src, dst, st, o)
}

func copyLeaf(src, dst string, st os.FileInfo, o *cpOpts) error {
	if st.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return fmt.Errorf("cannot read symbolic link '%s': %v", src, err)
		}
		if o.noDeref {
			os.Remove(dst)
			if err := os.Symlink(link, dst); err != nil {
				return symlinkError(dst, err)
			}
			if o.verbose {
				fmt.Printf("'%s' -> '%s'\n", src, dst)
			}
			return nil
		}
		real, err := filepath.EvalSymlinks(src)
		if err != nil {
			return fmt.Errorf("cannot resolve '%s': %v", src, err)
		}
		src = real
		if st, err = os.Stat(src); err != nil {
			return err
		}
	}
	if o.hardlink {
		os.Remove(dst)
		if err := os.Link(src, dst); err != nil {
			return fmt.Errorf("cannot create hard link '%s' => '%s': %v", dst, src, err)
		}
		if o.verbose {
			fmt.Printf("'%s' -> '%s'\n", src, dst)
		}
		return nil
	}
	if o.symlink {
		abs, err := filepath.Abs(src)
		if err != nil {
			abs = src
		}
		os.Remove(dst)
		if err := os.Symlink(abs, dst); err != nil {
			return symlinkError(dst, err)
		}
		if o.verbose {
			fmt.Printf("'%s' -> '%s'\n", src, dst)
		}
		return nil
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("cannot copy special file '%s'", src)
	}
	makeWritable(dst)
	if err := copyFileContents(src, dst); err != nil {
		return fmt.Errorf("cannot copy '%s' to '%s': %v", src, dst, err)
	}
	if o.preserve {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	if o.verbose {
		fmt.Printf("'%s' -> '%s'\n", src, dst)
	}
	return nil
}

func symlinkError(path string, err error) error {
	return fmt.Errorf("cannot create symbolic link '%s': %v "+
		"(Windows needs Developer Mode or an elevated shell for symlinks; use -l for a hard link)", path, err)
}

func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ------------------------------------------------------------------ rm -----

func cmdRm(args []string) int {
	f := NewFlags(
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "ignore nonexistent files, never prompt"},
		&Flag{Name: "interactive", Short: 'i', Help: "prompt before every removal"},
		&Flag{Name: "interactive-once", Short: 'I', Help: "prompt once before removing more than three files"},
		&Flag{Name: "recursive", Short: 'r', Long: "recursive", Help: "remove directories and their contents recursively"},
		&Flag{Name: "recursive", Short: 'R', Help: "same as -r"},
		&Flag{Name: "dir", Short: 'd', Long: "dir", Help: "remove empty directories"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "explain what is being done"},
		&Flag{Name: "preserve-root", Long: "preserve-root", Help: "do not remove drive roots (default)"},
		&Flag{Name: "no-preserve-root", Long: "no-preserve-root", Help: "allow removing drive roots (dangerous)"},
	)
	if !parseOrDie("rm", f, args) {
		return 2
	}
	if handleHelpVersion("rm", "[OPTION]... FILE...", f) {
		return 0
	}
	op := expandGlobs(f.Positional)
	force := f.Has("force")
	if len(op) == 0 {
		if force {
			return 0
		}
		warn("rm", "missing operand")
		return 1
	}
	if f.Has("interactive-once") && len(op) > 3 {
		if !askYesNo(fmt.Sprintf("rm: remove %d arguments? ", len(op))) {
			return 0
		}
	}
	exit := 0
	for _, p := range op {
		if err := checkSafeRemoval(p, f.Has("no-preserve-root")); err != nil {
			warn("rm", "%v", err)
			exit = 1
			continue
		}
		st, err := os.Lstat(p)
		if err != nil {
			if force {
				continue
			}
			warn("rm", "cannot remove '%s': No such file or directory", p)
			exit = 1
			continue
		}
		if f.Has("interactive") {
			kind := "regular file"
			if st.IsDir() {
				kind = "directory"
			}
			if !askYesNo(fmt.Sprintf("rm: remove %s '%s'? ", kind, p)) {
				continue
			}
		}
		switch {
		case st.IsDir():
			if f.Has("recursive") {
				if err := removeTree(p); err != nil {
					warn("rm", "cannot remove '%s': %v", p, err)
					exit = 1
					continue
				}
			} else if f.Has("dir") {
				makeWritable(p)
				if err := os.Remove(p); err != nil {
					warn("rm", "cannot remove '%s': %v", p, err)
					exit = 1
					continue
				}
			} else {
				warn("rm", "cannot remove '%s': Is a directory", p)
				exit = 1
				continue
			}
		default:
			makeWritable(p)
			if err := os.Remove(p); err != nil {
				warn("rm", "cannot remove '%s': %v", p, err)
				exit = 1
				continue
			}
		}
		if f.Has("verbose") {
			fmt.Printf("removed '%s'\n", p)
		}
	}
	return exit
}

// removeTree deletes a directory tree bottom-up, clearing read-only attributes
// on the way (which NTFS otherwise refuses to delete).
func removeTree(root string) error {
	st, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		makeWritable(root)
		return os.Remove(root)
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		makeWritable(root)
		if e := os.Remove(root); e == nil {
			return nil
		}
		return err
	}
	for _, e := range ents {
		child := filepath.Join(root, e.Name())
		cst, err := os.Lstat(child)
		if err != nil {
			continue
		}
		if cst.IsDir() {
			if err := removeTree(child); err != nil {
				return err
			}
			continue
		}
		makeWritable(child)
		if err := os.Remove(child); err != nil {
			return err
		}
	}
	makeWritable(root)
	return os.Remove(root)
}

// checkSafeRemoval refuses the destructive invocations GNU rm also protects
// against ("/" and drive roots, plus "." and "..").
func checkSafeRemoval(p string, noPreserveRoot bool) error {
	cp := filepath.Clean(p)
	if cp == "." || cp == ".." {
		return fmt.Errorf("refusing to remove '.' or '..' directory: skipping '%s'", p)
	}
	vol := filepath.VolumeName(cp)
	rest := strings.TrimPrefix(cp, vol)
	isRoot := (vol != "" && (rest == "" || rest == `\` || rest == "/")) ||
		(vol == "" && (rest == "/" || rest == `\`))
	if isRoot && !noPreserveRoot {
		return fmt.Errorf("it is dangerous to operate recursively on '%s' (use --no-preserve-root to override)", p)
	}
	return nil
}

// ------------------------------------------------------------------ mv -----

type mvOpts struct {
	force       bool
	interactive bool
	noClobber   bool
	update      bool
	verbose     bool
}

func cmdMv(args []string) int {
	f := NewFlags(
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "do not prompt before overwriting"},
		&Flag{Name: "interactive", Short: 'i', Long: "interactive", Help: "prompt before overwrite"},
		&Flag{Name: "no-clobber", Short: 'n', Long: "no-clobber", Help: "do not overwrite an existing file"},
		&Flag{Name: "update", Short: 'u', Long: "update", Help: "move only when SOURCE is newer than the destination"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "explain what is being done"},
		&Flag{Name: "target-directory", Short: 't', Long: "target-directory", HasArg: true, ArgName: "DIR", Help: "move all SOURCE arguments into DIR"},
	)
	if !parseOrDie("mv", f, args) {
		return 2
	}
	if handleHelpVersion("mv", "[OPTION]... SOURCE... DEST", f) {
		return 0
	}
	o := &mvOpts{
		force:       f.Has("force"),
		interactive: f.Has("interactive"),
		noClobber:   f.Has("no-clobber"),
		update:      f.Has("update"),
		verbose:     f.Has("verbose"),
	}
	op := expandGlobs(f.Positional)
	sources, dest, ok := resolveSources("mv", f, op)
	if !ok {
		return 1
	}
	if len(sources) > 1 && !isDir(dest) {
		warn("mv", "target '%s' is not a directory", dest)
		return 1
	}
	exit := 0
	for _, src := range sources {
		if err := mvEntry(src, dest, o); err != nil {
			warn("mv", "%v", err)
			exit = 1
		}
	}
	return exit
}

func mvEntry(src, dest string, o *mvOpts) error {
	src = trimSep(src)
	// A trailing separator marks an explicit directory target: GNU refuses it
	// when no such directory exists, instead of silently renaming onto it.
	if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, `\`) {
		if !isDir(dest) {
			return fmt.Errorf("cannot move to '%s': No such file or directory", dest)
		}
	}
	st, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("cannot stat '%s': No such file or directory", src)
	}
	target := dest
	if isDir(dest) {
		target = filepath.Join(dest, filepath.Base(src))
	}
	if samePath(src, target) {
		return fmt.Errorf("'%s' and '%s' are the same file", src, target)
	}
	if tst, err := os.Lstat(target); err == nil {
		if o.noClobber {
			return nil
		}
		if o.interactive && !askYesNo(fmt.Sprintf("mv: overwrite '%s'? ", target)) {
			return nil
		}
		if o.update && tst.ModTime().After(st.ModTime()) {
			return nil
		}
		if tst.IsDir() {
			if !st.IsDir() {
				return fmt.Errorf("cannot overwrite directory '%s' with non-directory", target)
			}
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("cannot move '%s' to '%s': Directory not empty", src, target)
			}
		} else {
			if st.IsDir() {
				return fmt.Errorf("cannot overwrite non-directory '%s' with directory '%s'", target, src)
			}
			makeWritable(target)
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("cannot remove '%s': %v", target, err)
			}
		}
	}
	if err := os.Rename(src, target); err == nil {
		if o.verbose {
			fmt.Printf("renamed '%s' -> '%s'\n", src, target)
		}
		return nil
	}
	// Cross-volume moves (and a few exotic cases) need copy + delete.
	if err := copyTree(src, target, &cpOpts{recursive: true, preserve: true, noDeref: true}); err != nil {
		return fmt.Errorf("cannot move '%s' to '%s': %v", src, target, err)
	}
	if st.IsDir() {
		if err := removeTree(src); err != nil {
			return fmt.Errorf("cannot remove '%s': %v", src, err)
		}
	} else {
		makeWritable(src)
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("cannot remove '%s': %v", src, err)
		}
	}
	if o.verbose {
		fmt.Printf("renamed '%s' -> '%s'\n", src, target)
	}
	return nil
}

// --------------------------------------------------------------- mkdir -----

func cmdMkdir(args []string) int {
	f := NewFlags(
		&Flag{Name: "parents", Short: 'p', Long: "parents", Help: "no error if existing, make parent directories as needed"},
		&Flag{Name: "mode", Short: 'm', Long: "mode", HasArg: true, ArgName: "MODE", Help: "set permission bits (read-only is what NTFS maps)"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "print a message for each created directory"},
	)
	if !parseOrDie("mkdir", f, args) {
		return 2
	}
	if handleHelpVersion("mkdir", "[OPTION]... DIRECTORY...", f) {
		return 0
	}
	op := f.Positional
	if len(op) == 0 {
		warn("mkdir", "missing operand")
		return 1
	}
	exit := 0
	for _, d := range op {
		existed := isDir(d)
		var err error
		if f.Has("parents") {
			err = os.MkdirAll(d, 0o777)
		} else {
			err = os.Mkdir(d, 0o777)
		}
		if err != nil {
			warn("mkdir", "cannot create directory '%s': %v", d, err)
			exit = 1
			continue
		}
		if mode := f.Str("mode"); mode != "" {
			if err := applyUnixMode(d, mode); err != nil {
				warn("mkdir", "%v", err)
				exit = 1
			}
		}
		if f.Has("verbose") && !existed {
			fmt.Printf("mkdir: created directory '%s'\n", d)
		}
	}
	return exit
}

// --------------------------------------------------------------- touch -----

func cmdTouch(args []string) int {
	f := NewFlags(
		&Flag{Name: "no-create", Short: 'c', Long: "no-create", Help: "do not create any files"},
		&Flag{Name: "access", Short: 'a', Help: "change only the access time"},
		&Flag{Name: "modify", Short: 'm', Help: "change only the modification time"},
		&Flag{Name: "date", Short: 'd', Long: "date", HasArg: true, ArgName: "STRING", Help: "use this date instead of now"},
		&Flag{Name: "reference", Short: 'r', Long: "reference", HasArg: true, ArgName: "FILE", Help: "use this file's times instead of now"},
		&Flag{Name: "stamp", Short: 't', HasArg: true, ArgName: "STAMP", Help: "use [[CC]YY]MMDDhhmm[.ss] instead of now"},
	)
	if !parseOrDie("touch", f, args) {
		return 2
	}
	if handleHelpVersion("touch", "[OPTION]... FILE...", f) {
		return 0
	}
	op := expandGlobs(f.Positional)
	if len(op) == 0 {
		warn("touch", "missing file operand")
		return 1
	}
	now := time.Now()
	atime, mtime := now, now
	if d := f.Str("date"); d != "" {
		t, err := parseDate(d)
		if err != nil {
			warn("touch", "invalid date format '%s'", d)
			return 1
		}
		atime, mtime = t, t
	}
	if s := f.Str("stamp"); s != "" {
		t, err := parseTouchStamp(s, now)
		if err != nil {
			warn("touch", "invalid date format '%s'", s)
			return 1
		}
		atime, mtime = t, t
	}
	if r := f.Str("reference"); r != "" {
		st, err := os.Stat(r)
		if err != nil {
			warn("touch", "failed to get attributes of '%s': No such file or directory", r)
			return 1
		}
		atime, mtime = st.ModTime(), st.ModTime()
	}
	onlyAccess := f.Has("access") && !f.Has("modify")
	onlyModify := f.Has("modify") && !f.Has("access")
	exit := 0
	for _, p := range op {
		st, err := os.Lstat(p)
		if err != nil {
			if f.Has("no-create") {
				continue
			}
			fh, cerr := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o666)
			if cerr != nil {
				warn("touch", "cannot touch '%s': %v", p, cerr)
				exit = 1
				continue
			}
			fh.Close()
			st, err = os.Lstat(p)
			if err != nil {
				warn("touch", "cannot touch '%s': %v", p, err)
				exit = 1
				continue
			}
		}
		fa, fm := atime, mtime
		if onlyAccess {
			fm = st.ModTime()
		}
		if onlyModify {
			fa = st.ModTime()
		}
		if err := os.Chtimes(p, fa, fm); err != nil {
			warn("touch", "setting times of '%s': %v", p, err)
			exit = 1
		}
	}
	return exit
}

// ------------------------------------------------------------------ ln -----

func cmdLn(args []string) int {
	f := NewFlags(
		&Flag{Name: "symbolic", Short: 's', Long: "symbolic", Help: "make symbolic links instead of hard links"},
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "remove existing destination files"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "print the name of each linked file"},
	)
	if !parseOrDie("ln", f, args) {
		return 2
	}
	if handleHelpVersion("ln", "[OPTION]... TARGET... [LINK_NAME|DIRECTORY]", f) {
		return 0
	}
	op := expandGlobs(f.Positional)
	if len(op) < 2 {
		warn("ln", "missing file operand")
		return 1
	}
	target := op[0]
	links := op[1:]
	if len(links) > 1 && !isDir(links[len(links)-1]) {
		warn("ln", "target '%s' is not a directory", links[len(links)-1])
		return 1
	}
	exit := 0
	for _, link := range links {
		linkPath := link
		if isDir(linkPath) {
			linkPath = filepath.Join(linkPath, filepath.Base(target))
		}
		if fileExists(linkPath) {
			if f.Has("force") {
				if err := os.Remove(linkPath); err != nil {
					warn("ln", "cannot remove '%s': %v", linkPath, err)
					exit = 1
					continue
				}
			} else {
				warn("ln", "failed to create link '%s': File exists", linkPath)
				exit = 1
				continue
			}
		}
		var err error
		if f.Has("symbolic") {
			t := target
			if !filepath.IsAbs(t) {
				if abs, aerr := filepath.Abs(t); aerr == nil {
					t = abs
				}
			}
			err = os.Symlink(t, linkPath)
			if err != nil {
				warn("ln", "%v", symlinkError(linkPath, err))
				exit = 1
				continue
			}
		} else {
			if err = os.Link(target, linkPath); err != nil {
				warn("ln", "failed to create hard link '%s' => '%s': %v", linkPath, target, err)
				exit = 1
				continue
			}
		}
		if f.Has("verbose") {
			fmt.Printf("'%s' -> '%s'\n", linkPath, target)
		}
	}
	return exit
}

// -------------------------------------------------------------- helpers ----

// resolveSources splits the positional arguments into sources and destination
// the way cp/mv/ln do, honouring -t/--target-directory.
func resolveSources(cmd string, f *Flags, op []string) ([]string, string, bool) {
	if td := f.Str("target-directory"); td != "" {
		if !isDir(td) {
			warn(cmd, "target '%s' is not a directory", td)
			return nil, "", false
		}
		if len(op) == 0 {
			warn(cmd, "missing file operand")
			return nil, "", false
		}
		return op, td, true
	}
	switch len(op) {
	case 0:
		warn(cmd, "missing file operand")
		return nil, "", false
	case 1:
		warn(cmd, "missing destination file operand after '%s'", op[0])
		return nil, "", false
	}
	return op[:len(op)-1], op[len(op)-1], true
}
