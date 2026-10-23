package core

// Well-known Windows file and directory paths, reachable by keyword.
//
// Remembering where `hosts` lives is the classic case: typing the keyword in
// command position prints a reminder with the path, its state and the commands
// that act on it, instead of failing with "unknown command":
//
//	winux hosts
//
// A keyword that appears as an argument of a real command is left completely
// alone, so `winux cat hosts` still means the file named `hosts` in the current
// directory and nothing is substituted or hinted (该怎样就怎样).
//
// The table is the built-in list below plus an optional user table at
// %USERPROFILE%\.winux\paths.tsv (overridable with WINUX_PATHS_FILE), so machine
// specific paths can be added without rebuilding. WINUX_PATH_HINTS=off disables
// the reminder entirely.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func init() {
	addCmd("paths", "print well-known Windows file/directory paths (a keyword alone shows one)", cmdPaths)
}

// pathEntry is one keyword (plus aliases) pointing at a well-known path.
type pathEntry struct {
	Keys  []string // canonical keyword first, then aliases
	Path  string   // %VAR% placeholders and a leading ~ are expanded on lookup
	Kind  string   // "file" or "dir"
	Desc  string   // one-line explanation
	Admin bool     // changing it normally needs administrator rights
	Usage []string // extra command lines to suggest (optional)
	User  bool     // came from the user table, not the built-in list
}

// builtinPathEntries is the curated table. Keep it small and genuinely useful:
// every entry costs the reader a line in `winux paths`. %VAR% is expanded from
// the environment, so an entry whose variable is missing on this machine simply
// does not apply and is skipped instead of printing a broken path.
var builtinPathEntries = []pathEntry{
	// ---- network configuration (the /etc of Windows) ----
	{Keys: []string{"hosts"}, Path: `%SystemRoot%\System32\drivers\etc\hosts`, Kind: "file", Admin: true,
		Desc: "本机主机名映射（DNS 覆盖），改完立即生效，无需重启"},
	{Keys: []string{"services"}, Path: `%SystemRoot%\System32\drivers\etc\services`, Kind: "file", Admin: true,
		Desc: "知名网络服务名与端口对照表（对应 /etc/services）"},
	{Keys: []string{"protocols", "protocol"}, Path: `%SystemRoot%\System32\drivers\etc\protocol`, Kind: "file", Admin: true,
		Desc: "IP 协议名与协议号对照表（对应 /etc/protocols）"},
	{Keys: []string{"networks"}, Path: `%SystemRoot%\System32\drivers\etc\networks`, Kind: "file", Admin: true,
		Desc: "网络名对照表（对应 /etc/networks）"},
	{Keys: []string{"lmhosts"}, Path: `%SystemRoot%\System32\drivers\etc\lmhosts.sam`, Kind: "file", Admin: true,
		Desc: "NetBIOS 名字解析样例文件（默认不启用，去掉 .sam 才生效）"},
	{Keys: []string{"etc"}, Path: `%SystemRoot%\System32\drivers\etc`, Kind: "dir", Admin: true,
		Desc: " hosts/services/protocols 等网络配置文件所在目录"},

	// ---- 遗留的系统级 ini ----
	{Keys: []string{"winini"}, Path: `%SystemRoot%\win.ini`, Kind: "file", Admin: true,
		Desc: "16 位时代遗留配置，少数老程序仍在读"},
	{Keys: []string{"systemini"}, Path: `%SystemRoot%\system.ini`, Kind: "file", Admin: true,
		Desc: "16 位时代遗留的驱动/子系统配置"},

	// ---- 系统目录 ----
	{Keys: []string{"windows"}, Path: `%SystemRoot%`, Kind: "dir",
		Desc: "Windows 安装目录"},
	{Keys: []string{"system32"}, Path: `%SystemRoot%\System32`, Kind: "dir",
		Desc: "64 位系统程序与命令行工具主目录"},
	{Keys: []string{"syswow64"}, Path: `%SystemRoot%\SysWOW64`, Kind: "dir",
		Desc: "32 位兼容层目录（32 位进程看到的 System32）"},
	{Keys: []string{"drivers"}, Path: `%SystemRoot%\System32\drivers`, Kind: "dir", Admin: true,
		Desc: "驱动与内核模块文件"},
	{Keys: []string{"fonts"}, Path: `%SystemRoot%\Fonts`, Kind: "dir", Admin: true,
		Desc: "字体目录，把 .ttf/.otf 拖进来即为安装"},
	{Keys: []string{"tasks"}, Path: `%SystemRoot%\System32\Tasks`, Kind: "dir", Admin: true,
		Desc: "计划任务的 XML 定义（schtasks 的数据目录）"},
	{Keys: []string{"eventlogs"}, Path: `%SystemRoot%\System32\winevt\Logs`, Kind: "dir", Admin: true,
		Desc: "Windows 事件日志（.evtx，事件查看器读的就是这里）"},

	// ---- 用户与全局数据目录 ----
	{Keys: []string{"userprofile", "home"}, Path: `%USERPROFILE%`, Kind: "dir",
		Desc: "当前用户的主目录（C:\\Users\\<你>）"},
	{Keys: []string{"appdata"}, Path: `%APPDATA%`, Kind: "dir",
		Desc: "当前用户的漫游应用数据（多数程序的配置在这里）"},
	{Keys: []string{"localappdata"}, Path: `%LOCALAPPDATA%`, Kind: "dir",
		Desc: "当前用户的本地应用数据（缓存、日志，不随域漫游）"},
	{Keys: []string{"programdata"}, Path: `%ProgramData%`, Kind: "dir",
		Desc: "所有用户共享的应用数据（隐藏目录，装服务常写这里）"},
	{Keys: []string{"programfiles"}, Path: `%ProgramFiles%`, Kind: "dir",
		Desc: "64 位程序的默认安装目录"},
	{Keys: []string{"programfiles86"}, Path: `%ProgramFiles(x86)%`, Kind: "dir",
		Desc: "32 位程序的默认安装目录"},
	{Keys: []string{"temp", "tmp"}, Path: `%TEMP%`, Kind: "dir",
		Desc: "当前用户的临时目录（%TMP%/%TEMP%）"},
	{Keys: []string{"desktop"}, Path: `%USERPROFILE%\Desktop`, Kind: "dir",
		Desc: "当前用户的桌面"},
	{Keys: []string{"downloads"}, Path: `%USERPROFILE%\Downloads`, Kind: "dir",
		Desc: "当前用户的下载目录"},
	{Keys: []string{"documents"}, Path: `%USERPROFILE%\Documents`, Kind: "dir",
		Desc: "当前用户的文档目录"},
	{Keys: []string{"inetpub"}, Path: `%SystemDrive%\inetpub`, Kind: "dir",
		Desc: "IIS 的默认站点根目录（装了 IIS 才有）"},

	// ---- 开机自启 ----
	{Keys: []string{"startup"}, Path: `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup`, Kind: "dir",
		Desc: "当前用户登录时自动运行的程序（放快捷方式即可）"},
	{Keys: []string{"commonstartup"}, Path: `%ProgramData%\Microsoft\Windows\Start Menu\Programs\StartUp`, Kind: "dir", Admin: true,
		Desc: "所有用户登录时自动运行的程序（放快捷方式即可）"},

	// ---- 开发工具的用户级配置 ----
	{Keys: []string{"wslconfig"}, Path: `%USERPROFILE%\.wslconfig`, Kind: "file",
		Desc: "WSL2 全局配置（内存、swap、网络、内核参数）"},
	{Keys: []string{"gitconfig"}, Path: `%USERPROFILE%\.gitconfig`, Kind: "file",
		Desc: "Git 全局配置（user.name、core.autocrlf 等）"},
	{Keys: []string{"sshdir"}, Path: `%USERPROFILE%\.ssh`, Kind: "dir",
		Desc: "SSH 密钥与客户端配置目录（OpenSSH/winux wssh 都认它）"},
	{Keys: []string{"sshconfig"}, Path: `%USERPROFILE%\.ssh\config`, Kind: "file",
		Desc: "OpenSSH 客户端配置（Host 别名、端口、私钥）"},
	{Keys: []string{"authorizedkeys"}, Path: `%USERPROFILE%\.ssh\authorized_keys`, Kind: "file",
		Desc: "SSH 服务端公钥白名单（OpenSSH 约定）"},
	{Keys: []string{"kubeconfig"}, Path: `%USERPROFILE%\.kube\config`, Kind: "file",
		Desc: "kubectl 的集群与凭据配置"},
	{Keys: []string{"dockerconfig"}, Path: `%USERPROFILE%\.docker\config.json`, Kind: "file",
		Desc: "Docker CLI 配置（登录凭据、代理）"},
	{Keys: []string{"psprofile"}, Path: `%USERPROFILE%\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`, Kind: "file",
		Desc: "Windows PowerShell 5.1 的个人配置文件（每次启动都会执行）"},
	{Keys: []string{"ps7profile"}, Path: `%USERPROFILE%\Documents\PowerShell\Microsoft.PowerShell_profile.ps1`, Kind: "file",
		Desc: "PowerShell 7+ 的个人配置文件"},
}

// ---------------------------------------------------------------- lookup ----

// pathHintDisabled reports whether WINUX_PATH_HINTS switches the reminder off.
func pathHintDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WINUX_PATH_HINTS"))) {
	case "0", "off", "false", "no", "disable", "disabled":
		return true
	}
	return false
}

// pathUserFile is the optional user table; WINUX_PATHS_FILE overrides the
// default location so tests (and portal installs) can point elsewhere.
func pathUserFile() string {
	if p := strings.TrimSpace(os.Getenv("WINUX_PATHS_FILE")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".winux", "paths.tsv")
}

// userPathCache keeps the parsed user table for the current process so a single
// command does not re-read (and re-warn about) the same file for every keyword
// lookup. The key includes size and mtime, so an edit made while winux runs is
// still picked up.
var (
	userPathMu    sync.Mutex
	userPathKey   string
	userPathValue []pathEntry
)

// loadUserPathEntries parses the user table: one entry per line, tab separated
//
//	keyword[|alias|alias]<TAB>path<TAB>file|dir<TAB>description[<TAB>admin]
//
// Blank lines and lines starting with # or ; are ignored; malformed lines are
// reported and skipped (a broken table must not break the command).
func loadUserPathEntries() []pathEntry {
	file := pathUserFile()
	if file == "" {
		return nil
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil
	}
	key := fmt.Sprintf("%s|%d|%d", file, st.Size(), st.ModTime().UnixNano())
	userPathMu.Lock()
	defer userPathMu.Unlock()
	if key == userPathKey {
		return userPathValue
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []pathEntry
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			warn("paths", "%s:%d: skipping malformed line (want keyword<TAB>path<TAB>file|dir<TAB>description)", file, i+1)
			continue
		}
		var keys []string
		for _, k := range strings.Split(fields[0], "|") {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
				keys = append(keys, k)
			}
		}
		kind := strings.ToLower(strings.TrimSpace(fields[2]))
		if kind != "file" && kind != "dir" {
			warn("paths", "%s:%d: skipping entry %q (kind must be file or dir)", file, i+1, fields[0])
			continue
		}
		if len(keys) == 0 || strings.TrimSpace(fields[1]) == "" {
			warn("paths", "%s:%d: skipping entry with no keyword or path", file, i+1)
			continue
		}
		e := pathEntry{Keys: keys, Path: strings.TrimSpace(fields[1]), Kind: kind}
		if len(fields) > 3 {
			e.Desc = strings.TrimSpace(fields[3])
		}
		for _, f := range fields[4:] {
			if strings.EqualFold(strings.TrimSpace(f), "admin") {
				e.Admin = true
			}
		}
		if e.Desc == "" {
			e.Desc = "用户自定义路径"
		}
		e.User = true
		out = append(out, e)
	}
	userPathKey, userPathValue = key, out
	return out
}

// allPathEntries merges the built-in table with the user table; a user entry
// that reuses a built-in keyword shadows that built-in entry.
func allPathEntries() []pathEntry {
	user := loadUserPathEntries()
	shadowed := map[string]bool{}
	for _, e := range user {
		for _, k := range e.Keys {
			shadowed[k] = true
		}
	}
	out := make([]pathEntry, 0, len(builtinPathEntries)+len(user))
	for _, e := range builtinPathEntries {
		dup := false
		for _, k := range e.Keys {
			if shadowed[k] {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, e)
		}
	}
	return append(out, user...)
}

// expandPathTemplate expands %VAR% placeholders and a leading ~. ok is false
// when a placeholder has no value, which means the entry does not apply to this
// machine (for example every %SystemRoot% entry outside Windows).
func expandPathTemplate(p string) (string, bool) {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		p = home + p[1:]
	}
	var b strings.Builder
	for i := 0; i < len(p); {
		if p[i] != '%' {
			b.WriteByte(p[i])
			i++
			continue
		}
		end := strings.IndexByte(p[i+1:], '%')
		if end < 0 {
			b.WriteByte(p[i])
			i++
			continue
		}
		name := p[i+1 : i+1+end]
		val := os.Getenv(name)
		if val == "" {
			return "", false
		}
		b.WriteString(val)
		i += end + 2
	}
	out := b.String()
	if out == "" {
		return "", false
	}
	return filepath.Clean(out), true
}

// lookupPath resolves one keyword (case-insensitive, aliases included) to its
// entry and expanded path. The user table wins over the built-in one.
func lookupPath(keyword string) (*pathEntry, string, bool) {
	k := strings.ToLower(strings.TrimSpace(keyword))
	if k == "" {
		return nil, "", false
	}
	for _, e := range allPathEntries() {
		for _, key := range e.Keys {
			if key != k {
				continue
			}
			if p, ok := expandPathTemplate(e.Path); ok {
				entry := e
				return &entry, p, true
			}
			return nil, "", false
		}
	}
	return nil, "", false
}

// ---------------------------------------------------------------- render ----

// renderPathReminder renders the reminder for one resolved entry.
func renderPathReminder(e *pathEntry, p string, extra []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s → %s\n", e.Keys[0], p)
	fmt.Fprintf(&b, "  %s\n", e.Desc)

	// state: kind, size, mtime; a missing path is a fact worth stating, not an
	// error (some entries only exist with the matching Windows feature)
	if st, err := os.Stat(p); err == nil {
		if st.IsDir() {
			fmt.Fprintf(&b, "  状态: 目录，修改于 %s\n", st.ModTime().Format("2006-01-02 15:04"))
		} else {
			fmt.Fprintf(&b, "  状态: 文件，%s，修改于 %s\n", humanSize(st.Size()), st.ModTime().Format("2006-01-02 15:04"))
		}
	} else {
		b.WriteString("  状态: 不存在（这个 Windows 版本/配置没有该路径）\n")
	}
	if e.Admin {
		b.WriteString("  权限: 编辑需要管理员权限（用 winux sudo）\n")
	}

	b.WriteString("  接着做:\n")
	quote := `"` + p + `"`
	if e.Kind == "dir" {
		fmt.Fprintf(&b, "    winux ls %s\n", quote)
		fmt.Fprintf(&b, "    winux open %s\n", quote)
	} else {
		fmt.Fprintf(&b, "    winux cat %s\n", quote)
		fmt.Fprintf(&b, "    winux open %s\n", quote)
		if e.Admin {
			fmt.Fprintf(&b, "    winux sudo vi %s\n", quote)
		}
		if dir := filepath.Dir(p); dir != "" && dir != p && e.Kind == "file" {
			fmt.Fprintf(&b, "    winux open \"%s\"   (打开所在目录)\n", dir)
		}
	}
	for _, u := range e.Usage {
		fmt.Fprintf(&b, "    %s\n", u)
	}
	if len(extra) > 0 {
		fmt.Fprintf(&b, "  注意: 额外的参数 %q 被忽略；提醒只在命令位置触发\n", strings.Join(extra, " "))
	}
	fmt.Fprintf(&b, "  更多: winux paths 列出全部关键词；winux paths -q %s 只输出路径\n", e.Keys[0])
	return b.String()
}

// pathHint renders the reminder for a keyword in command position. matched is
// false when the keyword is unknown or the reminder is switched off, in which
// case the caller keeps its normal "unknown command" behaviour.
func pathHint(keyword string, extra []string) (text string, matched bool) {
	if pathHintDisabled() {
		return "", false
	}
	e, p, ok := lookupPath(keyword)
	if !ok {
		return "", false
	}
	return renderPathReminder(e, p, extra), true
}

// pathSuggestion returns a one-line hint for a near-miss command name ("host"
// for "hosts"), or "" when nothing is close. It keeps the unknown-command exit
// code: a suggestion is still an error, an exact keyword is not.
func pathSuggestion(name string) string {
	if pathHintDisabled() {
		return ""
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if len(lower) < 3 {
		return ""
	}
	var hits []string
	for _, e := range allPathEntries() {
		for _, k := range e.Keys {
			if !strings.HasPrefix(k, lower) && !strings.HasPrefix(lower, k) {
				continue
			}
			if p, ok := expandPathTemplate(e.Path); ok {
				hits = append(hits, fmt.Sprintf("%s → %s", k, p))
			}
			break
		}
		if len(hits) >= 3 {
			break
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return "提示: 你是不是想找 " + strings.Join(hits, "；") + "（winux <关键词> 查看详情）\n"
}

// ---------------------------------------------------------------- command ---

func cmdPaths(args []string) int {
	f := NewFlags(
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "print paths only, one per line"},
		&Flag{Name: "json", Long: "json", Help: "print the table as JSON"},
		&Flag{Name: "existing", Short: 'e', Long: "existing", Help: "only entries that exist on this machine"},
		&Flag{Name: "search", Short: 's', Long: "search", HasArg: true, ArgName: "TEXT", Help: "substring match on keyword, path or description"},
	)
	if !parseOrDie("paths", f, args) {
		return 2
	}
	if handleHelpVersion("paths", "[KEYWORD]...", f) {
		return 0
	}
	quiet, asJSON := f.Has("quiet"), f.Has("json")
	onlyExisting := f.Has("existing")

	// `winux paths hosts` is the same reminder as `winux hosts`; -q/--json/-e
	// and the search form fall through to the table rendering below.
	if search := f.Str("search"); search == "" && len(f.Positional) > 0 && !quiet && !asJSON && !onlyExisting {
		status := 0
		for _, n := range f.Positional {
			e, p, ok := lookupPath(n)
			if !ok {
				warn("paths", "unknown keyword '%s'", n)
				status = 1
				continue
			}
			fmt.Print(renderPathReminder(e, p, nil))
		}
		if status != 0 {
			fmt.Fprintf(os.Stderr, "Run '%s paths' for the full list.\n", progName)
		}
		return status
	}

	type row struct {
		entry pathEntry
		path  string
	}
	var rows []row
	add := func(e pathEntry) {
		p, ok := expandPathTemplate(e.Path)
		if !ok {
			return // placeholder has no value here: the entry does not apply
		}
		if onlyExisting {
			if _, err := os.Stat(p); err != nil {
				return
			}
		}
		rows = append(rows, row{entry: e, path: p})
	}

	if search := f.Str("search"); search != "" || len(f.Positional) > 0 {
		needles := f.Positional
		if search != "" {
			needles = []string{search}
		}
		var missing []string
		seen := map[string]bool{}
		for _, n := range needles {
			// an exact keyword wins over substring matches
			if e, p, ok := lookupPath(n); ok {
				if !seen[strings.ToLower(e.Keys[0])] {
					seen[strings.ToLower(e.Keys[0])] = true
					if !onlyExisting || fileExists(p) {
						rows = append(rows, row{entry: *e, path: p})
					}
				}
				continue
			}
			low := strings.ToLower(n)
			matched := false
			for _, e := range allPathEntries() {
				p, ok := expandPathTemplate(e.Path)
				if !ok {
					continue
				}
				hay := strings.ToLower(strings.Join(e.Keys, " ") + " " + p + " " + e.Desc)
				if !strings.Contains(hay, low) {
					continue
				}
				matched = true
				if key := strings.ToLower(e.Keys[0]); !seen[key] {
					seen[key] = true
					if !onlyExisting || fileExists(p) {
						rows = append(rows, row{entry: e, path: p})
					}
				}
			}
			if !matched {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 && len(rows) == 0 {
			for _, m := range missing {
				warn("paths", "unknown keyword '%s'", m)
			}
			fmt.Fprintf(os.Stderr, "Run '%s paths' for the full list.\n", progName)
			return 1
		}
		for _, m := range missing {
			warn("paths", "no entry matches '%s'", m)
		}
	} else {
		for _, e := range allPathEntries() {
			add(e)
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].entry.Keys[0]) < strings.ToLower(rows[j].entry.Keys[0])
	})

	switch {
	case asJSON:
		type jsonRow struct {
			Keyword     string   `json:"keyword"`
			Aliases     []string `json:"aliases,omitempty"`
			Path        string   `json:"path"`
			Kind        string   `json:"kind"`
			Description string   `json:"description"`
			Admin       bool     `json:"admin"`
			Exists      bool     `json:"exists"`
			Source      string   `json:"source"`
		}
		out := make([]jsonRow, 0, len(rows))
		for _, r := range rows {
			src := "builtin"
			if r.entry.User {
				src = "user"
			}
			out = append(out, jsonRow{
				Keyword:     r.entry.Keys[0],
				Aliases:     r.entry.Keys[1:],
				Path:        r.path,
				Kind:        r.entry.Kind,
				Description: r.entry.Desc,
				Admin:       r.entry.Admin,
				Exists:      fileExists(r.path),
				Source:      src,
			})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			warn("paths", "%v", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	case quiet:
		for _, r := range rows {
			fmt.Println(r.path)
		}
		return 0
	}

	if len(rows) == 0 {
		fmt.Println("没有可用的关键词（%VAR% 占位符在本机取不到值）。")
		return 0
	}
	if len(f.Positional) == 0 && f.Str("search") == "" {
		file := pathUserFile()
		if _, err := os.Stat(file); err == nil {
			fmt.Printf("共 %d 个关键词（用户表: %s）\n", len(rows), file)
		} else {
			fmt.Printf("共 %d 个关键词（可在 %s 里补充自己的路径）\n", len(rows), file)
		}
	}
	for _, r := range rows {
		mark := ""
		if r.entry.Admin {
			mark = " [需管理员]"
		}
		src := ""
		if r.entry.User {
			src = " [用户]"
		}
		fmt.Printf("  %-22s %s%s%s\n", r.entry.Keys[0], r.path, mark, src)
		if r.entry.Desc != "" {
			fmt.Printf("  %-22s %s\n", "", r.entry.Desc)
		}
		if len(r.entry.Keys) > 1 {
			fmt.Printf("  %-22s 别名: %s\n", "", strings.Join(r.entry.Keys[1:], ", "))
		}
	}
	fmt.Println()
	fmt.Println("用法: winux <关键词> 看提醒（如 winux hosts）；winux paths -q <关键词> 只输出路径。")
	return 0
}
