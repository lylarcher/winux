# 更新记录

本文件记录 winux 各版本/各次改动。README 只保留使用文档，这里按时间倒序
（新条目在上）记录每次变更。

- **2026-09-12 · 修复 PowerShell 会话反激活不彻底**：`winux-deactivate` /
  `winux-activate` 在 PowerShell 里原本解析到 cmd\ 的批处理垫片，只在子
  cmd.exe 里还原 cmd 侧状态，导致「提示 deactivated 但 `(winux)` 提示符前缀
  还在」。两处修复：
  - **父 shell 识别穿透垫片**（activate.go）：`winux` 在 PS 里解析到
    `cmd\winux.cmd`，winux.exe 的直接父进程是临时 cmd.exe——检测现在向上
    再看一层，祖父是 PowerShell/pwsh 就仍按 PowerShell 处理，`winux deactivate`
    会正确打印 `. '…\winux-deactivate.ps1'` 一行而非 cmd 的 call 行。
  - **PowerShell 会话函数**（winux-activate.ps1）：像 `cd` 一样安装
    `winux-deactivate`/`winux-activate` 两个全局函数（函数优先于 PATH 上的
    .cmd），dot-source 真正的 .ps1 脚本完成全量还原（别名快照、PATH、
    prompt 前缀、WINUX_* 标记）；winux-deactivate.ps1 退出时移除这两个函数。
  smoke 新增 6 项断言（裸 `winux activate/deactivate` 经垫片仍打印 PS 一行、
  两个名字解析为函数、deactivate 移除函数），冒烟 204 项全绿；README
  用法/原理/目录结构同步更新。

- **2026-09-12 · 新增 `nc`/`netcat` 命令**：Windows 没有原生 netcat，winux 内置
  纯 Go 实现（零依赖）。连接模式 `nc HOST PORT`（stdin↔socket 双向转发，
  stdin 结束后 TCP 半关闭、对端关闭即退出，同 GNU；`-u` UDP 模式 stdin 结束
  即退出，同 OpenBSD）；监听模式 `nc -l [-p PORT] [HOST] PORT`（`-k` 持续
  accept，`-u -l` 收 UDP 报文打印、stdin 行发回最近来源）；`-z` 只连不发
  （端口扫描，成功 0/失败 1，`-v` 打印 GNU 风格 `Connection to … succeeded!`）；
  `-w` 连接+空闲超时、`-q` EOF 后延迟关闭、`-i` 行间延迟、`-s`/`-p` 本地源
  地址/端口（服务名端口如 `nc host http` 也支持）、`-n` 数字地址、`-4`/`-6`
  地址族、Ctrl+C 退出 130。**`-e`/`-c`（连上后执行命令）在 Windows 上拒绝**
  （RCE 入口 + 无可靠的原生绑定办法），提示改用 `winux sh -c` + 重定向。
  stdin 入口同样剥离 PS 5.1 注入的 BOM（与其它命令一致）。单测
  `test/nc_test.go` 用进程内 TCP/UDP 对端覆盖回显、listen、-z 扫描、-w 超时、
  UDP 收发与各拒绝路径（全绿）；build.ps1 命令表 55→57，smoke 新增 6 项断言
  （缺参数、-e 拒绝、-z 关闭端口、端到端回显），冒烟 198 项全绿；README
  命令表、nc 说明节、目录结构同步更新。

- **2026-09-12 · 新增 `tcpdump`、`mysql`、`redis-cli` 三个命令**：
  - `tcpdump`：Windows 原生抓包，诚实映射到内置 **pktmon**（无 WinPcap/Npcap
    依赖）。非提权时自动 **UAC 提权**跑 `pktmon start/stop + etl2pcap`，再纯 Go
    解析 pcapng 输出 tcpdump 风格行（时间戳、IP:端口、TCP flags、`-v` 详情、
    `-e` 链路头、`-A/-X` 载荷）；`-r FILE` **纯 Go 离线解析 pcap/pcapng**（无需
    pktmon/提权），支持 Ethernet/回环/RAW、IPv4/IPv6/TCP/UDP/ICMP/ICMPv6/ARP；
    过滤表达式 OR-of-AND 组（host/port/tcp/udp/icmp/arp/not/括号），pktmon 不
    支持的原语（portrange/greater/less）诚实报错；`-D` 列接口、`-w` 写 pcap、
    `-c` 计数停止。跨平台编译（linux/darwin 为存根，`-r` 仍可用）。
  - `mysql`：**纯 Go MySQL 客户端**（无需安装客户端），完整握手：
    `mysql_native_password` 与 `caching_sha2_password`（无 TLS 走 RSA 公钥全
    认证）；`mysql>`/`    ->` 交互 REPL（`;`/`\g` 表格、`\G` 竖直、`\c` 取消、
    `\q` 退出）、`-e` 非交互、`-B -N -t -r -E` 输出形状、`-D` 选库、`-p`/`-pSECRET`
    密码、`-v` 详情、`-f` 出错继续。单测用进程内 mock MySQL 服务器验证
    native/caching 认证、结果集、NULL/错误包、竖直/批量输出（全绿）。
  - `redis-cli`：**纯 Go Redis 客户端**（无需安装客户端），RESP2 协议；交互
    `host:port>` 提示、`-a` 认证、`-n` 选库、`-r/-i` 重复、`-x` stdin 作参数、
    `-d` 分隔符、`--raw`；命令直发不设白名单。单测用 mock RESP 服务器验证
    AUTH/PING/GET/SET/INCR/LRUNGE/MGET/错误包与交互（全绿）。
  - **修复 PowerShell 5.1 管道 BOM 注入**（本次冒烟暴露的隐藏 bug）：winux 把
    控制台切到 CP 65001 后，PS 5.1 会向原生进程 stdin 注入 **UTF-8 BOM**（首行
    前多 `EF BB BF`）并用 CRLF 换行，导致 `sort -n` 首行数字解析失败、`uniq`
    首行去重失效。现在共享行读取器（`newScanner`）与 `sh`/`mysql`/`redis-cli`
    的 stdin 读取统一剥离首个 UTF-8 BOM，行为稳定且不误伤流中间的 BOM 数据。
    新增 bom_test.go 回归测试（sort/uniq/grep/head/sh/仅首行剥离）。
  - build.ps1 命令表 + smoke 新增断言（mysql/redis-cli 连不上服务器的错误路径、
    `tcpdump -r` 合成 pcap 端到端解码、`-r` 缺文件失败），冒烟 192 项全绿；
    README 命令表、tcpdump/mysql/redis-cli 说明节、目录结构同步更新。

- **2026-09-11 · 新增 `passwd` 命令（仅本地用户）**：无参数 = 改当前用户密码
  （`NetUserChangePassword` API，旧密码校验 + 新密码两次隐藏输入，无需提权）；
  `passwd USER` = 改指定本地账户密码（UAC 提权跑 `net user`，输出经临时文件
  回传）；`-S` = GNU 形态状态行（L/P/NP + 最近修改/过期天数近似）+ 账户来源。
  **微软账号与域账号一律拒绝**（按 `Get-LocalUser` 的 `PrincipalSource`
  判定）：`passwd 97953` 提示密码在线上管理，不碰本地 SAM 占位项。密码输入
  用 SetConsoleMode 关回显（readPassword），非控制台时诚实拒绝。smoke 新增
  4 项断言（含 MSA 条件拒绝），README 命令表与「账户与权限」同步。
- **2026-09-11 · 修复 `id USER` 忽略用户名参数**：此前 `id test`/`id hello` 等
  一律打印当前用户身份（[USER] 位置参数被完全无视）。现在 `id USER` 用
  `LookupAccountNameW` 把账号名解析成 SID 并取末段 **RID** 当 uid/gid（本地
  账户从 1000 起，`id Administrator` → 500，首个账户即惯例的 1000），同时经
  SID→名字反查输出规范化账号名（`id FTPUSER` → `uid=1004(ftpuser)`）；账户
  不存在时报 `id: 'xxx': no such user` 退出 1，多余操作数报 `extra operand`
  （均同 GNU）。smoke 新增 4 项断言，README「限制」与命令表同步更新。
- **2026-09-11 · 验证并固化「与 Python venv 并存」**：用真实 venv 在 cmd 与
  PowerShell 里实测 winux 与 Python venv 任意顺序激活都互不冲突——双方都是
  「保存原值+前置+后进先出还原」的会话机制，标记变量（WINUX_* vs
  VIRTUAL_ENV/_OLD_VIRTUAL_*）与命令名（winux- 前缀 vs 裸 activate/deactivate）
  完全不相交；提示符前缀叠加显示 `(winux) (.venv) ...`，LIFO 反激活精确还原。
  唯一规则：反激活按相反顺序（乱序会残留陈旧前缀，与叠两层 venv 相同，属
  venv 式机制的固有行为）。smoke 新增「venv 并存」段（python 可用时自动建
  临时 venv 验证 cmd + PS 各一条，176 项全绿），README 增「与 Python venv /
  conda 并存」一节。
- **2026-09-11 · venv 式提示符前缀 `(winux)`**：激活后每个提示行前面出现
  `(winux) `（像 Python venv 的 `(.venv)`），一眼看出当前窗口已激活。cmd 把
  `(winux) ` 前置到 `PROMPT` 变量（原值存 `WINUX_OLD_PROMPT`，含未设置时回退
  cmd 默认 `$P$G` 的情况），PowerShell 安装 `prompt` 函数（保存原函数体
  `$global:winuxSavedPrompt`，调用原函数后加前缀）；deactivate 都精确还原。
  重复 activate 不叠加前缀（幂等）。smoke 新增 PS 与 cmd 双 shell 的提示符
  断言（174 项全绿）。
- **2026-09-11 · cmd 激活一键化**：`winux-activate.cmd`/`winux-deactivate.cmd`
  改为**自定位**（根据自身位置推算垫片目录：在 `cmd\` 里则 exe 上一级，在
  exe 旁则垫片在 `cmd\` 子目录），同一份内嵌模板解压到**两处**——`cmd\`
  供 `winux activate` 的 call 行引用，以及 **winux.exe 旁**（winux.exe 目录
  在 PATH 上，cmd 里直接输入 `winux-activate` / `winux-deactivate` 一个词即
  可激活/反激活当前会话，无需 for /f、无需复制粘贴）。`winux activate` 的
  cmd 提示也缩短为「复制上一行执行，或直接输入 winux-xxx」。smoke 新增
  一键激活断言（172 项全绿）。
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
