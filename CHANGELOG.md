# 更新记录

本文件记录 winux 各版本/各次改动。README 只保留使用文档，这里按时间倒序
（新条目在上）记录每次变更。

- **2026-09-11 · 修复非 Windows 交叉编译**：`sys.go`/`ip.go` 之前引用只在
  `*_windows.go` 里定义的类型/常量，Linux 构建报错。新增共享文件
  `core/nettypes.go`（无 build tag），迁入 `sockRow`/`routeRow`/`ifRow`/
  `adapterInfo`/`addrInfo`/`netNeighbor`/`protoStats` 类型、MIB TCP 状态常量、
  接口类型常量、`linkTypeName()` 与 `serviceName()`；`currentUserName` 的
  `proc_other.go` 存根签名与 `sys.go` 不一致（返回 `(string,error)` vs
  `string`）导致重定义，删除该存根（`sys.go` 的实现本就跨平台）。现在
  `GOOS=linux` 与 `GOOS=darwin` 均可 `go build ./core/...` 通过，Windows 侧
  行为不变（smoke 170 全绿）。
- **2026-09-11 · 垫片内嵌进 winux.exe，激活时按 md5 增量解压**：`build.ps1`
  不再生成任何 .cmd/.ps1 文件——全部命令垫片（由命令表动态合成）与 5 个辅助
  脚本（`winux.cmd`、`cmd\winux-activate.cmd`/`winux-deactivate.cmd`、
  `winux-activate.ps1`/`winux-deactivate.ps1`，存于 `core/shimdata/` 并用
  `//go:embed` 打进二进制）都由 winux.exe 自带。`winux activate`/`deactivate`
  在打印激活指令前先把这些文件解压到 exe 旁（目录不存在则创建；文件已存在
  且 md5 相同则跳过、不重写，md5 不同才覆盖），新增 `winux shims` 命令显式
  解压并清理 cmd\ 里过期的垫片（build.ps1 构建后调用，install.ps1 也先调用
  一次）。winux.exe 单独拷到任何目录都能就地重建 cmd\。
- **2026-09-11 · 更新记录独立成 `CHANGELOG.md`**：从 README 拆出，README 的
  「更新记录」一节只留指向本文件的链接，避免文档臃肿。
- **2026-09-11 · 激活重构为「winux.exe 进 PATH + `winux activate`」模型**：所有
  `.cmd` 垫片移到 `winux.exe` 旁的 `cmd\` 子目录，系统 PATH 只需加 winux.exe
  所在目录（垫片不进 PATH）；新增内建命令 `winux activate`/`winux deactivate`，
  自动识别父 shell 并打印一行激活指令（PS 配 `| iex`，cmd 配
  `for /f "delims=" %l in ('winux activate') do @%l`），任意目录即可激活/退出，
  无需 install.ps1（install 保留为「永久」路线）。
- **2026-09-11 · README 新增「激活原理（venv 式会话机制）」一节**：讲清
  activate/deactivate 的会话级模型——只改当前终端进程的 PATH/别名/宏，不改
  系统 PATH 注册表、不替换不删除任何原生文件（优先级遮蔽而非覆盖）、关窗口
  即还原；含与 Python venv 的对照表、名字解析优先级、子进程继承与 `cd -`
  状态文件残留等细节。
- **2026-09-11 · 新增 `df`**：报告磁盘空间使用，默认稳定 POSIX 布局
  `Filesystem 1K-blocks Used Available Use% Mounted on`；文件系统=盘符（含 subst
  与映射网络盘，用 kernel32 GetLogicalDriveStrings/GetDiskFreeSpaceEx/
  GetVolumeInformation/GetDriveType，零外部依赖）。支持 `-a -B -h -H -i -k -l
  -P -T -t TYPE -x TYPE --total`；`-i` 因 Windows 无 inode 概念诚实报错退出 1；
  带操作数时限制到所在盘（盘符/路径须存在，同 GNU）；`-a` 连空光驱也列出。
  新增单元测试（块大小解析/百分比/SI 人类可读/盘根映射/表格排版）与 smoke
  df 段 8 项，共 166 项全绿。修复过程中发现并修正 `driveRootOf` 丢失盘符冒号
  的 bug（`C:\` 曾被解析成 `C\`，导致按操作数过滤永远为空）。
- **2026-09-11 · `cd -` 全面可用**：上一目录不再依赖 PS 环境变量，winux 把
  最近一次成功 `cd` 前的位置持久化到 `%LOCALAPPDATA%\winux\oldpwd`（可用
  `WINUX_OLDPWD_FILE` 覆盖），PowerShell 钩子、cmd doskey 宏（改为经 winux
  解析后交 `cd /d` 执行，`cd -/~/`/跨盘在 cmd 里同样生效）、`winux sh` 内建
  （修复：此前 sh 的 `cd` 只打印不真正切换目录，现真 chdir 且记录 oldpwd）
  三个入口统一支持，连续 `cd -` 可来回切换。smoke 新增直连 `cd -`、toggle、
  cmd 宏机制（for 循环等价批处理）等 4 项，共 158 项全绿。
- **2026-09-11 · 激活辅助脚本全部改 `winux-` 前缀**：`winux-activate.ps1` /
  `winux-deactivate.ps1` / `cmd\winux-activate.cmd` / `cmd\winux-deactivate.cmd`。
  `bin\` 进系统 PATH 后不再暴露裸 `activate`/`deactivate` 名字（`.ps1` 本就不在
  PATHEXT、不会被裸名执行，`.cmd` 不进系统 PATH），彻底消除与 conda/Python
  venv 等其它工具激活脚本的命名冲突；`winux activate`/`winux deactivate` 命令
  行为不变。
- **2026-09-11 · 新增 `winux-deactivate.ps1` / `winux-deactivate.cmd`**：反向撤销激活——
  PowerShell 侧还原快照中被遮蔽的别名（PS 默认 `cd/ls/pwd/cat/ps` 等及用户
  自定义别名，激活时写入每会话快照文件）、移除 `cd` 函数、恢复 `WINUX_OLD_PATH`
  快照的 PATH、清除 `WINUX_ACTIVE/WINUX_STATE/WINUX_PREVDIR` 标记；cmd 侧清除
  doskey 宏、恢复 PATH 与清除环境变量。smoke 新增 PS deactivate 段 4 项 +
  cmd 批处理 deactivate 2 项。
- **2026-09-11 · 新增 `sh` 小 shell（heredoc 支持）**：PowerShell/cmd 都没有
  heredoc 语法，现提供 `winux sh` 内置 POSIX 风格解释器：`<<EOF`/`<<'EOF'`
  heredoc、管道 `|`、重定向 `> >> < 2> 2>> 2>&1`、`&& || ;`、单双引号与
  `\` 转义（Windows 路径反斜杠原样保留）、`$VAR/${VAR}/$?` 展开、
  `NAME=值` 变量、`cd exit : true false` 内建；用法 `sh -c "脚本"`、
  `sh 脚本文件`、交互 REPL（`sh>`）。单命令在进程内跑命令表，多级管道各
  阶段以 winux.exe 子进程串联；退出码语义同 bash（`$?`、`&&`/`||`）。
  新增强制清单 `core/sh_test.go`（tokenize/parse/heredoc/展开/重定向端到端）。
- **2026-09-11 · 修复 `vi/vim` 含中文（宽字符）行光标无法到行尾**：此前横向
  绘制/滚动/截断都把 rune 索引当屏幕列（宽字符占 2 列），中文行内光标会
  「卡」在文本中部、长行还会溢出换行；现新增 runewidth/cellWidth/truncToCells
  按终端单元计算光标列、窗口滚动与行截断（宽字符不拆半），附核心包内单测。
- **2026-09-11 · 修复 `vi/vim` insert 模式光标不可见**：`newScreen()` 会先发送
  `\x1b[?25l` 隐藏控制台光标，但 vi 从不在绘制时恢复；现在 vi 每帧重绘时显式
  `\x1b[?25h` 显示光标，并按模式切换形状（normal 方块 `\x1b[2 q`、insert 竖线
  `\x1b[6 q`），退出时复位。
- **2026-09-11 · `nmtui` 改为调出 Windows 网络连接面板**：执行 `nmtui`
  打开 `ncpa.cpl`（控制面板\网络和 Internet\网络连接），`edit/connect` 同一面板，
  `hostname` 打开「重命名这台电脑」设置页。
- **2026-09-11 · 新增 `du` 与完整 `ln`**：`du` 支持 `-a -s -h -k -m -b -B -c -d -x
  -L -H -l -S -t -0 --apparent-size --exclude`，NTFS 实际分配大小统计；`ln` 补齐
  `-t -i -n -b[=METHOD] -r -T -L -P`（备份/相对链接/目标目录/交互提示），`-d`
  明确拒绝。
- **2026-09-11 · 新增 `ls`**：GNU 风格列表（`-a -A -l -h -s -i -t -S -u -c -r -R -F
  -m --color --time-style` 等，详见「ls 说明」）；`-i` 显示真实 NTFS 文件 ID，
  隐藏判定=点开头或 NTFS 隐藏属性；权限列按「诚实映射」落地（目录/可执行/只读）。
- **2026-09-11 · 重构为 core 包 + 新命令**：全部命令迁入 `winux/core`（可被
  import/嵌入，`Execute`/`CommandNames` 导出），`main.go` 变为薄壳；单元测试迁至
  `winux/test`。新增账户与权限命令（`useradd userdel usermod groupadd groupdel
  chmod chown chgrp`，写操作 UAC 提权 + `--dry-run`）、`cal`、`ps`（WMI 快照）、
  `netstat/ss`（iphlpapi 内核表）、`ipconfig/ip/nmcli/nmtui`（网络只读策略，
  改网子命令一律拒绝）。
- **v1.0.0 · 初始版本**：`cp rm mv mkdir touch ln grep cat head tail wc sort uniq
  cut tr vi/vim more less echo pwd clear date uname hostname which id whoami sudo
  su`，含 cd 跨盘切换、正反斜杠兼容、通配符展开、双 shell 激活脚本。
