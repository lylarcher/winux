# winux — Linux 风格命令行工具（原生 Windows 适配）

在 PowerShell 和 cmd.exe 里直接使用 `cp`、`rm`、`mv`、`mkdir`、`touch`、`grep`、
`ls`、`cat`、`more`、`less`、`tail`、`vi/vim`、`sudo`、`su`、`ps`、`netstat`、
`ss`、`ip`、`nmcli` 等常见 Linux 命令，保持 Linux 的命令习惯与参数选项
（`cp -r`、`rm -rf`、`ls -l`、`grep -rin`、`sort -nr`……）。内置 **50 个命令**
（51 个命令名，`vim` 是 `vi` 的别名）。

- **纯 Go 原生实现**，编译为单个自包含的 `winux.exe`（约 3.7 MB），零外部依赖、
  零运行时（不需要 Python/WSL/MSYS2/Cygwin）。
- 同一个二进制内置全部命令（busybox 风格多合一），同时支持
  `winux <命令> ...` 和按 argv[0] 派发（把 exe 改名/硬链接成 `cp.exe` 即可当 `cp` 用）。
- 两种 shell 都可用，且都支持通配符展开（PowerShell/cmd 给原生程序传参时**不**展开
  通配符，因此通配符在工具内部实现）、管道与重定向。

## 快速开始

```powershell
# 1) 构建（winux.exe 内嵌全部 .cmd 垫片与激活脚本，构建后自动解压到 bin\cmd\）
powershell -ExecutionPolicy Bypass -File .\build.ps1

# 2) 让 `winux` 全局可用：把 winux.exe 所在目录加入 PATH（推荐用 install.ps1，
#    它会写用户 PATH 并在 PowerShell 配置档里加激活行；也可手动添加 bin\ 目录）。
#    只需加 winux.exe 的目录——cmd\ 里的 .cmd 垫片不需要进 PATH。
powershell -ExecutionPolicy Bypass -File .\install.ps1

# 3) 激活当前会话（任意目录执行；winux 会识别父 shell 并打印对应的一行）：
winux activate | iex                                  # PowerShell（激活后也可直接输入 winux-activate）
winux-activate                                        # cmd.exe（winux 目录已在 PATH，直接输入即可）
for /f "delims=" %l in ('winux activate') do @%l      # cmd.exe（不用上面那个名字时的通用写法）
```

退出激活（撤销 PATH/别名/宏/环境标记，恢复到激活前状态）：

```powershell
winux deactivate | iex                                # PowerShell
winux-deactivate                                      # 两个 shell 都行：PowerShell 里是会话函数，cmd 里是批处理
for /f "delims=" %l in ('winux deactivate') do @%l    # cmd.exe
# 或直接（不依赖 PATH）：
. .\bin\winux-deactivate.ps1        # PowerShell
call .\bin\cmd\winux-deactivate.cmd # cmd.exe
```

> PowerShell 会话里的 `winux-activate`/`winux-deactivate` 是激活脚本安装的
> **会话函数**（与 `cd` 同理：外部进程改不了父 shell 的函数/别名/提示符）。
> 若裸名落到 cmd\ 的批处理垫片上，只会在子 cmd.exe 里还原 cmd 侧状态，表现
> 为「打印 deactivated 但 `(winux)` 前缀还在」——反激活时函数随会话一起移除。

> **布局与命名**：`winux.exe` 与 `cmd\` 目录并排。全部 `.cmd` 垫片与激活辅助
> 脚本（`winux-activate.ps1`/`winux-deactivate.ps1`、`winux-activate.cmd`/
> `winux-deactivate.cmd`、`cmd\winux-*.cmd`）都**内嵌在 winux.exe 里**，由
> `winux activate`（或 `winux shims`）按 **md5 增量**解压到 exe 旁：文件不存在
> 则创建，存在且 md5 相同则跳过（不重写、不动时间戳），md5 不同才覆盖。
> 因此把 winux.exe 单独拷到任何目录都能就地重建 `cmd\`。
> `winux-activate.cmd`/`winux-deactivate.cmd` 会解压到**两处**（内容相同、
> 自定位）：`cmd\` 里供 `winux activate` 打印的 call 行引用，以及 **winux.exe
> 旁边**——因为 winux.exe 目录就在 PATH 上，cmd 里直接输入 `winux-activate`
> 一个词即可激活当前会话，无需 for /f、无需复制粘贴。PowerShell 里
> `winux-activate`/`winux-deactivate` 两个词同样是一个命令搞定：激活脚本会
> 安装同名**会话函数**（见下）。
> 只有 `winux.exe` 的目录需要进 PATH（`winux` 到处可用）；垫片不进 PATH、
> 不激活时不会遮蔽任何原生命令。辅助脚本一律 `winux-` 前缀，所以把 `bin\`
> 加进系统 PATH 后不会引入裸 `activate`/`deactivate` 名字，不会与
> conda/Python venv 等其它工具的激活脚本冲突。cmd.exe 激活会顺带安装
> `mkdir/echo/date` 的 doskey 宏（见下文「cmd.exe 内建命令」一节）。

## 激活原理（venv 式会话机制）

`activate` 的工作方式与 **Python venv 的激活**同模型：只对**当前终端会话**
生效，改的是**当前 shell 进程的环境变量**，不碰磁盘上任何原生文件；关掉该
窗口或执行 `deactivate` 即完全还原。它**不会**改系统/用户 PATH 注册表
（不动 `setx`、不写注册表、不需要管理员），`bin\` 里的文件也始终留在项目
目录，不复制、不移动、不删除。

与 venv 的对应关系：

| venv | winux |
|---|---|
| `Scripts\activate.ps1`：加 `Scripts` 到 PATH + 设 `VIRTUAL_ENV` | `winux-activate.ps1`：加 `bin\cmd`（垫片目录）到 PATH + 设 `WINUX_ACTIVE`，并删除 PS 同名别名 |
| 只对当前终端进程生效，别的窗口不受影响 | 完全一样，每个窗口独立 |
| 关窗口/新开窗口即失效 | 一样（无任何持久状态） |
| `deactivate` 恢复 PATH | `deactivate` 恢复 PATH + **精确还原被删的别名**（快照）+ 清 cmd 的 doskey 宏 |
| **提示符前缀 `(.venv)`** | **提示符前缀 `(winux)`**（cmd 改 `PROMPT`、PowerShell 装 `prompt` 函数；deactivate 精确还原原提示符） |
| 不激活也能用 `venv\Scripts\python.exe` | 不激活也能用 `winux cp ...` 或直接调 `bin\cmd\cp.cmd` |

`winux activate` / `winux deactivate` 是内建命令：先按 **md5 增量**把内嵌的
垫片与辅助脚本解压到 exe 旁（见上「布局与命名」），再**识别父 shell**
（PowerShell 还是 cmd.exe，`--shell` 可覆盖）后打印一行让 shell 自己执行的
代码——PowerShell 打 `. '…\winux-activate.ps1'`（配
`| iex` 执行），cmd 打 `call "…\cmd\winux-activate.cmd"`（配
`for /f "delims=" %l in ('winux activate') do @%l` 执行）。父 shell 识别会
**穿透垫片**：`winux` 在 PowerShell 里解析到 `cmd\winux.cmd`，winux.exe 的
直接父进程是临时 cmd.exe，检测会再向上看一层——祖父是 PowerShell 就仍按
PowerShell 处理，不会误打 cmd 的 call 行。cmd 里还有更省事的
一条路：`winux-activate.cmd` 同时解压在 winux.exe 旁，而 winux.exe 目录就在
PATH 上，所以**直接输入 `winux-activate` 一个词**就能激活（`winux-deactivate`
同理），连 for /f 都不用。这样只要有 `winux` 就能在任意位置激活，不再需要
install.ps1（install 只是把「全局 winux + 每窗口自动激活」自动化）。

`activate` 具体做四件事（都是会话级）：

1. **前置 PATH**：把 `bin\cmd`（垫片目录）插到当前会话 `$env:Path` 最前，让
   垫片 `xxx.cmd` 在名字解析时先于 System32 里的原生 exe 被找到；原 PATH 存入
   `WINUX_OLD_PATH` 供 deactivate 精确还原。`winux.exe` 本身假定已在 PATH
   （这正是 cmd\ 布局的意义：系统 PATH 只放 winux.exe，垫片不进 PATH）。
2. **删除 PowerShell 同名别名/函数**：PS 里 `cp/ls/cat/ps/echo` 等本是**别名**
   （指向 `Copy-Item`/`Get-ChildItem`…），别名解析优先于 PATH，不删掉就轮不到
   垫片。删的是别名，`Copy-Item` 等 cmdlet 本身一直都在；被删的别名会快照到
   每会话临时文件，deactivate 按原样还原。
3. **安装 cmd 的 doskey 宏**（`cmd\winux-activate.cmd`）：`mkdir/echo/date` 是 cmd
   **内建命令**，内建优先级高于 PATH，垫片抢不到，所以用宏把输入行改写；
   宏只作用于该交互窗口的行编辑器，窗口一关即失效，连子 cmd 进程都不继承。
4. **提示符加 `(winux)` 前缀**（venv 风格）：cmd 把 `(winux) ` 前置到 `PROMPT`
   变量（原值存入 `WINUX_OLD_PROMPT`），PowerShell 安装一个 `prompt` 函数
   （原函数体保存为 `$global:winuxSavedPrompt`）——激活的窗口一眼可辨，
   deactivate 把两者都精确还原。

## 与 Python venv / conda 并存

可以，**任意顺序激活都互不冲突**（已用真实 venv 在 cmd 与 PowerShell 里实测）。
双方都是同一套「保存原值 + 前置 + 后进先出还原」的会话机制，而各自的标记与
名字**完全不相交**：

- **环境变量标记**：winux 用 `WINUX_*`，venv 用 `VIRTUAL_ENV`/`_OLD_VIRTUAL_*`，
  互不覆盖。
- **命令名**：venv 的 `activate`/`deactivate` 是裸名；winux 的辅助脚本全部
  `winux-` 前缀（这正是当初设计的原因），所以 bin\ 进 PATH 永远不会跟 venv
  抢 `deactivate` 这个名字。
- **PATH**：各自只前置自己的目录（winux 前置 `bin\cmd`+`bin`，venv 前置
  `venv\Scripts`），`python`/`pip` 仍解析到 venv，`cp`/`rm` 等仍解析到 winux。
- **提示符**：两个前缀叠在一起，谁后激活谁在前——cmd 显示
  `(winux) (.venv) ...` 或 `(.venv) (winux) ...`；PowerShell 显示
  `(.venv) (winux) PS ...>`（venv 的绿色前缀是写控制台的前缀，winux 的是
  返回字符串的前缀，两段拼成一行）。

**唯一的规则：反激活按相反顺序**（后激活的先反激活）。只要按
`winux-deactivate` 后再 `deactivate`（或反过来）的 LIFO 顺序，PATH、提示符、
标记全部精确还原。乱序反激活（比如 venv 还开着就把 winux 关了）会留下一个
**陈旧的提示符前缀**——这是「保存+前置」机制的固有行为，叠两个 venv 也一样，
不是 winux 特有的问题。

## 名字解析优先级（这就是「遮蔽」而非「替换」的原理）

- PowerShell：`别名/函数 → cmdlet → PATH 外部命令`
- cmd.exe：`内建命令 → PATH 外部命令`

所以 winux 只是让 `cp` 这个名字在**这个会话里**先解析到 `bin\cmd\cp.cmd →
winux.exe cp`；`C:\Windows\System32\sort.exe`、cmd 的 `mkdir` 内建等原生
文件原地未动，只是暂时排不到。两个值得知道的细节：

- **子进程会继承**：激活窗口里启动的子程序（含嵌套的 `powershell.exe`）继承
  PATH 与 `WINUX_ACTIVE`，也能用 winux 命令；但 `cd` 函数和「已删除的别名」
  是进程内状态，不会传给子 shell。
- **`cd -` 有一个跨会话的小残留**：上一目录存在 `%LOCALAPPDATA%\winux\oldpwd`
  小文件（故意设计，让 PS 钩子/cmd 宏/sh 内建三入口共享），不随窗口关闭
  消失，无副作用，deactivate 也不删它。

`install.ps1` 是「永久」路线：把 `bin\`（winux.exe 目录）写入**用户** PATH
并在 PowerShell `$PROFILE` 末尾追加一行激活，从此新开窗口自动生效——那是
venv 之外另一种显式选择；但即便不 install，只要 `winux` 可用，
`winux activate` 也能随时按需激活。

## 已实现命令

| 命令 | 说明与常用选项 |
|---|---|
| `cp` | `-r -R -a -f -i -n -u -v -p/--preserve -d -P -L -l -s -t --exclude --include`，含 `cp -r src dst`、`cp *.txt dir/` |
| `rm` | `-r -R -d -f -i -I -v --preserve-root --no-preserve-root`；默认拒绝删除 `/`、盘符根、`.`、`..`；只读文件也能删（自动清只读位） |
| `mv` | `-f -i -n -u -v -t`；跨盘自动退化为「递归复制+删除」 |
| `mkdir` | `-p -m -v`；`mkdir -p a/b/c` |
| `touch` | `-c -a -m -d -r -t`；`touch -t 202401020304.05 f`、`touch -d "2024-01-02 03:04:05" f` |
| `ln` | `-s -f -v -t -i -n -b[=METHOD] -r -T -L -P`；`-d` 拒绝（Windows 不支持目录硬链接）；符号链接需要开发者模式或管理员权限（否则给出提示） |
| `ls` | `-a -A -d -l -h -s -i -t -S -u -c -r -R -1 -C -x -m -f -F -p -Q -G -g -o -n -U -L --color[=WHEN] --time-style=STYLE --group-directories-first`；隐藏=点开头或 NTFS 隐藏属性，`-i` 显示真实 NTFS 文件 ID，彩色目录/可执行文件（见「ls 说明」） |
| `grep` | `-E -F -G -i -v -w -x -c -l -L -n -H -h -o -q -s -a -r -R -m -A -B -C -e -f --include --exclude --exclude-dir --color[=WHEN]`；退出码 0/1/2 同 GNU |
| `cat` | `-n -b -s -E -T -v -A -e -t` |
| `cd` | 跨盘切换目录（见下方「cd 与跨盘切换」）；支持 `cd -`、`cd /`、`cd ~`、正反斜杠混用 |
| `head` | `-n -c -q -v`；支持 `head -5`、`head -n -2`（去掉最后两行）、`head -n +3` |
| `tail` | `-n -c -f/--follow[=name] -F -q -v -s --pid`；支持 `tail -20`、`tail -n +3` |
| `wc` | `-l -w -c -m -L`（`-L` 对 CRLF 文件正确）；多文件输出 total 行 |
| `sort` | `-b -c -f -k -n -r -t -u -s`；`sort -k2 -n`、`sort -nr` |
| `uniq` | `-c -d -D -i -f -u`；与 coreutils 一样只比较相邻行 |
| `cut` | `-c -d -f -s --output-delimiter`；`cut -d, -f2`、`cut -c1-3` |
| `tr` | `-c -C -d -s -t`；`tr a-z A-Z`、`tr -d '\r'` |
| `more` | `+NUM`、`+/PATTERN`、`-NUM -d -s -u`；`--More--(N%)` 提示与 vi 式按键 |
| `less` | `-N -i -I -s -F`；`/` `?` `n` `N` 搜索、`g` `G` `j` `k` 移动、`:q` 等 |
| `vi`/`vim` | 完整 vi 子集：`h j k l w b e 0 $ ^ gg G`、`i I a A o O`、`x dd dw yy p P u r ~`、`/` `?` 搜索、`:w :q :q! :wq :x :set nu :%s/pat/rep/g`、`u` 撤销 |
| `echo` | `-n -e -E` 与转义 `\n \t \\ \xHH \0NNN` 等 |
| `pwd` | `-P -L` |
| `clear` | 清屏（VT 转义） |
| `date` | `-u -d -r -R -I -s +FORMAT`；`date +%F`、`date -d "yesterday"`；离线可用（内置时区数据） |
| `df` | `-a -B SIZE -h -H -i -k -l -P -T -t TYPE -x TYPE --total`；默认 POSIX 布局 `Filesystem 1K-blocks Used Available Use% Mounted on`，盘符即文件系统，见「df 说明」 |
| `du` | `-a -s -h -k -m -b -B SIZE -c -d N -x -L -H -P -0 -S -l -t SIZE --apparent-size --exclude PATTERN`；默认按磁盘占用（NTFS 实际分配大小）打印、1024 字节块，见「du 说明」 |
| `uname` | `-a -s -n -r -v -m -p -o`；如 `uname -a` → `Windows <主机名> 10.0.22631 x86_64 winux` |
| `hostname` | `-s -d -f -i`；设置主机名会拒绝（需要管理员，且 winux 不做） |
| `which` | `-a`；按 PATH+PATHEXT 查找，找不到退出码 1（同 GNU） |
| `sh` | `-c SCRIPT` 或脚本文件；内置 POSIX 风格小 shell：heredoc（`<<EOF … EOF`）、管道 `\|`、重定向 `> >> < 2> 2>> 2>&1`、`&& \|\|`、单双引号、`$VAR`/`${VAR}`/`$?`、`NAME=值` 变量、`cd`/`exit`/`:`/`true`/`false` 内建（见「sh 说明」） |
| `id` | `-u -g -G -n`；输出 POSIX 形态身份；uid/gid 取账户 SID 的 RID（首个本地账户为 1000，见限制）；支持 `id USER`，未知账户报 `no such user` 退出 1 |
| `whoami` | 当前用户 |
| `sudo` | `-i -s -u -E -n`；在 Windows 上以 UAC 提权重跑命令/开管理员 shell（见下） |
| `su` | `-c -l -s`；root/administrator → UAC 管理员 shell；其它用户 → `runas.exe` |
| `useradd` | `-m -d -c -p -g -G -s -u -r -e --dry-run`；映射为 `net user` + 可选的 `net localgroup`（见下「账户与权限」） |
| `userdel` | `-r -f --dry-run`；映射为 `net user NAME /delete` |
| `passwd` | `-S`；无参数改当前用户密码（旧密码校验，无需提权），`passwd USER` 改本地账户密码（UAC）；**微软账号/域账号拒绝** |
| `usermod` | `-l -d -a -g -G -L -U -c -p -s -u --dry-run`；改名用 `Rename-LocalUser`，锁定/解锁用 `net user /active:no\|yes` |
| `groupadd` | `-g -r --dry-run`；映射为 `net localgroup NAME /add` |
| `groupdel` | `-f --dry-run`；映射为 `net localgroup NAME /delete` |
| `chmod` | `-R -v -c -f --reference`；八进制（`755`）与符号（`u=rwx,go=r`）模式都支持；映射到 NTFS 只读属性（见下） |
| `chown` | `-R -v -c -f -h --reference --dry-run`；映射为 `icacls` 设置属主；`--dry-run` 先打印将要执行的命令 |
| `chgrp` | 校验组名后明确告知 Windows 无「组属主」概念并失败（诚实失败，不假装成功） |
| `cal` | `-m -y -3 -j -A -B`；`cal 3 2025`、`cal -y`、`cal -A 1` |
| `ps` | `aux`/`-ef`/`-e`/`-p`/`-u`/`-C`/`-o`/`--sort`/`-t` 等；数据来自 WMI（`Get-CimInstance Win32_Process`），无外部依赖（见下「进程」） |
| `netstat` | `-a -t -u -l -n -o -p tcp\|udp -e -i -r -s`；Windows 布局（`Proto Local Foreign State`） |
| `ss` | `-a -t -u -l -n -p -e -o -m -s -r -i -4 -6`；Linux（iproute2）布局；与 netstat 读同一份内核套接字表 |
| `ifconfig` | 默认直通系统 `ipconfig.exe`（别名 `ipconfig`）；`--help`/`--version` 为 winux 自带；`/release /renew /flushdns` 等改网操作被拒绝（见下「网络只读」） |
| `ip` | `addr/link/route/neigh` 的 `show`（含 `-4 -6 -br -o`）；iproute2 输出风格；一切改网子命令（`addr add`、`link set`、`route add`…）拒绝并提示原生替代 |
| `nmcli` | `device status/show`、`connection show`、`general status/permissions/hostname`；改网子命令拒绝（Windows 无 NetworkManager，列表由适配器推导） |
| `nmtui` | 执行即调出 Windows「网络连接」面板（`ncpa.cpl`，对应 控制面板\网络和 Internet\网络连接）；`nmtui hostname` 打开「重命名这台电脑」设置页；`edit/connect` 同面板（见「网络命令（只读策略）」末尾） |
| `tcpdump` | `-i -c -w -r -n -N -s -e -v -A -X -D -p -q`；**抓包**映射到 Windows 原生 `pktmon`（自动 UAC 提权，`-r` 纯 Go 离线解析 pcap/pcapng，见「tcpdump 说明」） |
| `mysql` | 纯 Go MySQL 客户端（**无需安装客户端**，仅 TCP）：`-h -P -u -p -D -e -B -N -t -r -E -v --connect-timeout -f`；交互 `mysql>`/`    ->` 提示、`;`/`\g`/`\G` 结束、`\q` 退出；支持 `mysql_native_password` 与 `caching_sha2_password`（含 RSA 公钥全认证） |
| `redis-cli` | 纯 Go Redis 客户端（**无需安装客户端**）：`-h -p -a -n -r -i -d -x --raw -e --no-auth-warning --timeout`；交互 `host:port>` 提示，RESP2 回复格式化（`1) "val"`、`(integer)`、`(nil)`） |
| `nc`/`netcat` | 纯 Go TCP/UDP 客户端与监听器：`-l -u -p -s -w -q -n -v -z -k -4 -6 -i`；stdin↔socket 双向转发、`-z` 端口扫描、`-k` 持续监听；`-e/-c`（连上后执行命令）因安全原因在 Windows 上拒绝（见「nc 说明」） |
| `clickhouse` | 纯 Go ClickHouse 客户端（**无需安装 clickhouse-cli**，走 HTTP 接口）：`-h --port -u --password -d -q/-e -f -m --secure --timeout`；`clickhouse client` 风格、`-q`/stdin 批量、交互 REPL（见「clickhouse 说明」） |
| `uptime` | 系统运行时长（`GetTickCount64` + 终端服务会话数）：默认 GNU 格式 `up 3 days, 4:05, 1 user, load average: N/A`；`-s` 启动时间、`-p` 友好格式；load average 为 Windows 无此概念，诚实显示 N/A |
| `mongodb` | 纯 Go MongoDB 客户端（**无需安装 mongosh/mongo**，直接 wire 协议）：`-h --port -u --password --authenticationDatabase -d --eval --tls --timeout`；手写 BSON + OP_MSG + SCRAM-SHA-256；`use/show dbs/show collections` 与 `db.<col>.<method>({...})` 简式（find/findOne/insertOne/insertMany/updateOne/updateMany/deleteOne/deleteMany/aggregate/countDocuments/distinct/…，支持 `ObjectId()`、无引号键），也接受原样 JSON 命令（见「mongodb 说明」） |
| `psql` | 纯 Go PostgreSQL 客户端（**无需安装 psql/PostgreSQL**，wire v3 协议）：`-h --port -U -d -W -w -c -f -t -A -q --ssl --timeout`；认证支持明文/MD5/SCRAM-SHA-256（PGPASSWORD 或 `-W` 隐藏输入）；psql 风格对齐表格，`-t` 只元组、`-A` 无对齐；REPL 支持 `\q \l \dt \d NAME \c DB \?`（见「psql 说明」） |
| `sqlite` | SQLite 客户端（**无需安装 sqlite3 CLI**，内嵌纯 Go 驱动 modernc.org/sqlite）：`sqlite [OPTIONS] [DBFILE] [SQL]`，缺省 DBFILE 用 `:memory:`；`-r/--readonly`、`-json/-line/-list/--column` 输出、`--separator --nullvalue --cmd -v`；REPL 点命令 `.tables .schema .databases .indexes .dump .mode .headers .read`（见「sqlite 说明」） |
| `systemctl` | systemd 风格服务管理（纯 Go，不装 OpenSSH 之外任何组件）：`start/stop/restart/enable/disable/status/is-active/is-enabled/list-units/list-unit-files/cat/journalctl/mask/unmask/daemon-reload`；后端为 Windows 服务（svc 或内置 svchost）与计划任务（schtasks），单元文件 *.service 放 `%ProgramData%\systemctl\units`（见「systemctl 说明」） |
| `svchost` | 内置 Windows 服务宿主（替代外部 wrapper）：`svchost -unit <unit.service> [-debug]`；由 systemctl 的 wrapped 后端自动调用，托管 ExecStart 子进程并转发输出到 `<unit>.wrapper.log` |
| `sshd` | 纯 Go SSH 服务端（**无需安装 Windows OpenSSH**）：默认监听 **1022** 端口；`--auth local` 用本地 Windows 账号密码校验（LogonUserW），`--auth any` 仅测试用；连入后是 winux sh 环境（exec 请求跑 `winux sh -c`、shell 请求挂 ConPTY 伪控制台）；`sshd --install` 注册并启动 **winux-sshd** 服务（systemctl 可管理），host key 自动生成于 `%ProgramData%\winux\sshd_host_ed25519`（见「sshd / wssh 说明」） |
| `wssh` | 纯 Go SSH 客户端（**无需安装 OpenSSH**）：`wssh [user@]host[:port] [command]`，默认端口 **1022**；密码认证（`-p` 或交互提示），可连 winux sshd 或任意 OpenSSH 服务器；交互模式请求远端 pty（ConPTY）并转发本机终端（见「sshd / wssh 说明」） |

运行 `winux --help` 查看全部命令，`winux <命令> --help` 查看单个命令用法。

## 通配符与引号

- `cp *.txt dir\`、`rm -rf *.log`、`grep foo *.go` 里的通配符由命令本身展开
  （cmd/PowerShell 不会替原生程序展开）。
- `~` 会按用户目录展开：`cp a.txt ~/b.txt`。
- 引号按 shell 的规则传递：`grep "hello world" f.txt` 中 `hello world` 作为一个
  整体到达 grep。
- 参数分隔仍以 Linux 习惯为准：`cp -r src dst` 等价于 `cp src dst -r` 等。

## 路径写法（斜杠与反斜杠）

Windows 同时接受 `\` 和 `/`，winux 的所有命令都遵循这一习惯，**不需要统一成某一种**：

- **文件操作数**：`cat src/a.txt` 与 `cat src\a.txt` 等价；`grep -r todo src/`、
  `head -n1 src/sub/b.txt`、`less src\a.txt`、`wc -l mix\one/two.txt` 均可。
- **目录操作数**：`cp a.txt dst/`、`cp -t dst\ a.txt`、`mv b.txt moved/`、
  `mkdir -p a\b/c`（混合分隔符也能建出完整目录树）。
- **通配符**：`cp src\*.go dst`、`rm -rf build\cache\*`、`grep --include=src\*.go`
  都按 Windows 分隔符规则展开（`*` 不跨目录层级）。
- **引用类选项**：`touch -r src\ref.txt dst/stamp.txt`、`date -r file.txt +%F` 同理。
- **输出统一为反斜杠**：命令打印的路径（`pwd`、`grep -r` 的文件名、`winux cd`
  的解析结果）都是 Windows 原生反斜杠格式。
- **末尾分隔符**：`cp a.txt b\`、`mv a.txt b\` 中 `b\` 表示「明确要放进目录 b」，
  若 b 不存在则报错（与 GNU 一致），不会悄悄把它当成文件名。
- **安全校验同样覆盖两种写法**：`rm -rf C:/` 与 `rm -rf C:\` 一样被拒绝；
  `cd /` 与 `cd \` 都表示当前盘根。

## PowerShell 说明

- `winux-activate.ps1` 会**删除与我们有替代品的同名别名/函数**（按 `bin\cmd\*.cmd` 动态计算），
  所以激活后 `ls`、`cp`、`rm`、`mv`、`cat`、`sort`、`which` 等一律解析到 winux
  垫片；PowerShell 自己的 `dir/cls/md/type/copy/move/rd/gc` 等因没有同名垫片而
  原样保留。
- `cp`、`rm`、`mv`、`ls`、`cat`、`pwd`、`clear`、`echo`、`mkdir`、`sort`、`more`、
  `which`、`ps` 等会解析到 `bin` 里的垫片。
- 输出编码为 UTF-8，控制台自动开启 VT（真彩/ANSI）支持，中文路径与内容正常。

## cmd.exe 说明

- 把 `bin` 加入 PATH 后，除少数内建命令外都会命中垫片（`cp`、`rm`、`grep`、
  `cat`、`less`、`tail` 等都不是 cmd 内建，直接可用）。
- cmd 内建命令（`mkdir`、`echo`、`date`、`more`、`sort` 等）优先于 PATH 可执行文件，
  所以 `winux-activate.cmd` 会为 `mkdir`、`echo`、`date` 安装 **doskey 宏**
  （`doskey mkdir=winux.exe mkdir $*`），在交互式 cmd 窗口里让 `mkdir -p a\b`、
  `echo -e ...`、`date +%F` 走 winux。**注意**：doskey 宏只在交互式会话生效，
  批处理文件里不会展开。
- **批处理文件里的垫片必须用 `call` 调用**（cmd.exe 的固有行为，所有 .cmd 垫片
  工具都如此）：`call "%WINUX_BIN%\cp.cmd" a b`，或直接 `"%WINUX_BIN%\winux.exe" cp a b`。

## cd 与跨盘切换

Windows 上有个根本约束：**外部进程无法改变父 shell 的工作目录**，所以 `cd` 采用
「解析 + 切换」两步架构，两种 shell 行为统一：

- **`winux cd [DIR]`**（解析器）：把目标解析为绝对路径（支持 `~`、`-`、`/`、
  `D:` 裸盘符、正反斜杠混用如 `D:/a\b`），校验存在且是目录，打印出**反斜杠格式**
  的绝对路径，成功退出 0、失败退出 1。单独运行它只打印路径，不会改你的 shell 目录。
- **PowerShell**（`winux-activate.ps1` 定义的 `cd` 函数）：先让 winux 解析校验，再用
  `Set-Location` 真正切换。因此从 C: 可以一步 `cd D:\some\dir`，并且支持
  `cd -`（回到上一目录）、`cd /`（当前盘根）、`cd ~`、`cd "带 空格 的目录"`。
  单独 `cd` 打印当前目录。
- **cmd.exe**（`winux-activate.cmd` 安装的 doskey 宏）：`cd` 宏经 winux 解析后交给
  cmd 自己的 `cd /d` 执行，因此 `cd -`、`cd ~`、`cd /`、跨盘与正反斜杠在
  cmd 里同样可用（宏只作用于交互式窗口，批处理文件请直接写
  `cd /d D:\some\dir`）。
  ```
  doskey cd=for /f "delims=" %d in ('winux.exe cd $*') do cd /d %d
  doskey mkdir=winux.exe mkdir $*
  doskey echo=winux.exe echo $*
  doskey date=winux.exe date $*
  ```

路径书写注意（反斜杠）：

- 输入 `cd D:\some\dir`、`cd D:/some/dir`、`cd ..\..\x`、`cd ~/Documents` 均可，
  一律解析成反斜杠绝对路径。
- `cd /` = 当前盘根（如 `C:\`）；`cd D:` = D 盘根（外部进程无法得知某盘的
  「当前目录」，映射到盘根是明确且可预期的行为）。
- `cd -` 回上一目录：winux 把最近一次成功 `cd` 前的位置记入
  `%LOCALAPPDATA%\winux\oldpwd` 状态文件（PowerShell 钩子、cmd 宏、`winux sh`
  内建共用同一来源），因此三个入口都支持，且可连续 `cd -` 来回切换
  （bash 的 $OLDPWD 语义）。可用 `WINUX_OLDPWD_FILE` 覆盖文件位置。

## 与 Linux 的差异（Windows 特性）

- **权限模型**：Windows 没有 POSIX 权限位/属主组。winux 按 Windows 语义做
  **诚实映射**（详见「账户与权限」一节）：`chmod` 映射到 NTFS 只读属性、
  `chown` 映射到 `icacls /setowner`、`chgrp` 明确说明无组属主概念后失败；
  文件只读属性会被 `cp/rm/mv` 自动处理（覆盖/删除前清除只读位）。
- **sudo/su 与 root**：Windows 没有 root 账户，也没有 POSIX `setuid`。winux 的
  `sudo` 通过 **UAC 提权**（`Start-Process -Verb RunAs`）用管理员令牌重跑命令
  （`sudo rm -rf C:\...` 会弹 UAC 确认框），`sudo -i`/`sudo -s` 打开管理员
  cmd shell；用户取消 UAC 时报错退出。`sudo -u` 与 `-n` 明确不支持并说明原因，
  绝不假装已提权。`su` 中 root/administrator 同样走 UAC；其它用户走系统
  `runas.exe`（交互输入密码）。
- **符号链接**：`ln -s` 需要开启「开发者模式」或以管理员运行，否则给出可操作的
  错误提示；硬链接 `ln` 无此限制。
- **编码**：与 GNU grep 一致，含 NUL 字节的文件（如 PowerShell `>` 重定向默认
  产生的 UTF-16 文件）按二进制处理，`grep` 报告 `Binary file ... matches`；
  需要按文本搜索时用 `grep -a` 或先 `Get-Content | winux grep`。
- **单用户**：Windows 没有 uid/gid，`id` 用账户 SID 的 **RID**（末段子权威）当
  uid 与 gid——本地账户从 1000 起编号，所以首个账户就是惯例的 1000，输出形如
  `uid=1000(lyl) gid=1000(lyl) groups=1000(lyl)`；`id USER` 会解析真实账户
  （`id Administrator` → 500），查无此人时报 `id: 'xxx': no such user`（同 GNU）。
- **交互工具**（`less`、`more`、`vi`）在 stdout 不是控制台时（管道/重定向/CI）
  自动退化为直接输出文件内容（`vi` 则报错提示需要控制台），不会挂死。

## 账户与权限命令

Windows 没有 Linux 的 UID/GID 与 POSIX 权限位，`useradd` 等命令按 Windows
账户模型做**诚实映射**，绝不假装成功：

- `useradd/userdel/usermod/groupadd/groupdel` 落地为 `net user` /
  `net localgroup` 原生命令。写操作默认以 **UAC 提权**重跑（普通权限的会话会弹
  一次 UAC 确认框）；已是管理员则以当前令牌直接执行。
- 所有写命令都支持 `--dry-run`：只打印将要执行的命令、**不执行**，方便预览和测试。
  （smoke 测试只验证 `--dry-run`，不会动真实账户。）
- 部分选项在 Windows 上无对应物，会明确说明后拒绝：如 `useradd -u`（没有 uid）、
  `-s`（没有 POSIX shell 字段）、`usermod -u`、`chgrp`（没有组属主）。
- `chmod` 支持八进制（`chmod 755`）与符号（`chmod u=rw,go=r`）写法（含
  `-R` 递归、`-v/-c/-f`、`--reference`），映射到 NTFS 只读属性：
  `chmod 444` → 只读，`chmod 644` 及更高 → 可写。执行位没有对应物，不做伪造。
- `chown [属主[:组]]` 映射到 `icacls /setowner`；属主/组名会先用
  `net user` / `net localgroup` 校验，不存在则报错。
- `passwd` 修改**本地用户**密码（只支持本地账户）：
  - 不带参数：改当前用户自己的密码。先校验旧密码（`NetUserChangePassword`
    原生 API，**无需提权**），新密码提示两次、输入不回显；符合本地密码策略
    才算成功。
  - `passwd USER`：改指定本地账户的密码，走 **UAC 提权**执行 `net user`。
  - `passwd -S [USER]`：GNU 形态状态行（`lyl P 10/29/2025 0 99999 7 -1`，
    L=禁用/P=可用/NP=无密码）+ 账户来源。
  - **微软账号（Microsoft account）与域账号一律拒绝**：`passwd 97953` 会提示
    该账号密码在线上管理（account.microsoft.com），不会去动本地 SAM 里的
    占位项；域账号提示「不是本地账户」。判断依据是 `Get-LocalUser` 的
    `PrincipalSource`。

## 进程查询（ps）

`ps` 通过只读 WMI 查询（`Get-CimInstance Win32_Process`）快照进程，单次调用即得
全部字段（PID/PPID/命令行/所有者/时间/内存），无需外部工具。支持：

- BSD 风格 `ps aux` 与 SysV 风格 `ps -ef`，以及 `PID TTY TIME CMD` 默认格式；
- `-e/-A`（全部）、`-u 用户`、`-p PID列表`、`-C 命令`、`-o 列名`（自选列与列宽）、
  `--sort=KEY`（`pid/comm/user/%cpu/%mem/time`，`-` 前缀降序）、`-N` 取反；
- 默认只显示当前用户的进程（与 Linux 一致），`ps aux`/`ps -ef` 显示全部；
- `%CPU` 按进程 CPU 时间与系统物理内存计算，与 Windows 任务管理器的近似一致。

## ls 说明

`ls` 忠实实现了 GNU ls 的常用参数（`-a -A -l -h -s -i -t -S -r -R -d -f -1 -C -x -m
-F -p -Q -u -c -n -o -g -G -U -L --color --time-style --group-directories-first`），
并按照本项目「诚实映射」的原则落地 Windows 语义：

- **隐藏判定**：点开头（`.git`）**或** NTFS 隐藏属性都算隐藏，`-a` 全部显示
  （含 `.`/`..`），`-A` 显示隐藏但不含 `.`/`..`。
- **长格式权限列**：目录 `drwxr-xr-x`，普通文件 `-rw-r--r--`，可执行扩展名
  （`.exe/.cmd/.bat/.com/.msi`）显示 `-rwxr-xr-x`；勾选了「只读」属性则清掉
  `w` 位（`-r--r--r--`）。Windows 没有属主组，属主列显示当前用户短名，
  组列显示 `None`，`-n` 显示合成的 `1000 1000`。
- **`-i` 显示真实 NTFS 文件 ID**（`GetFileInformationByHandle` 的卷内文件索引，
  即 Windows 的 inode），读不到时退化为路径哈希。
- **时间**：默认按 GNU 规则（半年内显示 `Jan 2 03:04`，更早显示年份）；
  `-u/-c` 显示/按访问时间、创建时间排序；`--time-style=long-iso|full-iso|iso|+FORMAT`。
- **彩色**：`--color=auto`（默认，仅终端着色）目录加粗蓝色、可执行文件绿色、
  符号链接青色；`--color=always|never` 强制/关闭。
- **布局**：输出到管道时每行一个（与 POSIX 一致），终端时自动多列竖排，
  `-1` 强制单列，`-C` 强制多列，`-x` 横排，`-m` 逗号分隔。
- 排序默认按名称（大小写不敏感），`-t` 时间新→旧、`-S` 大小大→小、`-r` 反转、
  `-f`/`-U` 不排序按目录顺序；`-R` 递归并打印 `目录名:` 标题。
- 默认输出 `total N` 汇总行（512 字节块）；操作数不存在时报错并退出 2（同 GNU）。

## du 说明

`du` 统计磁盘占用，忠实实现 GNU 常用参数
（`-a -s -h -k -m -b -B -c -d -x -L -H -P -0 -S -l -t --apparent-size
--exclude`），Windows 语义诚实映射如下：

- **默认统计磁盘实际占用**：文件用 `GetFileInformationByHandleEx`（FileStandardInfo）
  读取 NTFS 真实分配字节数（目录同样有分配量），而不是逻辑大小；读取失败时回退
  逻辑大小。`-b`/`--apparent-size` 切换为逻辑字节。
- **单位**：默认 1024 字节块（与 GNU coreutils 一致），`-k`/`-m`/`-B SIZE` 换单位，
  `-h` 人类可读（1K/234M/2G）。`-t SIZE` 按同一套 SIZE 解析（如 `-t 1M`）。
- **汇总与深度**：`-s`=只输出每个操作数总计；`-d N` 限制打印深度（仍全量累加）；
  `-a` 连同文件一起列出；`-c` 追加 grand total；`-S` 目录只算自身、不含子目录。
- **去重与软链**：默认硬链接文件**只计一次**（按 NTFS 文件 ID 去重），`-l` 全部计；
  默认不跟随符号链接，`-L` 全部跟随（带环路保护）、`-H` 只跟随命令行参数，
  `-P` 不跟随。
- **范围**：`--exclude PATTERN` 按名字通配排除；`-x` 不跨卷（按盘符/UNC 判断）；
  `-0` 用 NUL 分隔输出行。
- 布局为 GNU 风格 `块数\t路径`，目录总计时每行输出、最后是根目录自身；操作数
  不存在时报错并退出 1。

## df 说明

`df` 报告磁盘空间使用，忠实实现 GNU 常用参数
（`-a -B -h -H -i -k -l -P -T -t -x --total`）。Windows 语义诚实映射如下：

- **文件系统 = 盘符**：列出 `GetLogicalDriveStrings` 返回的每个盘符（含 subst 与
  映射网络盘），`C:` 即文件系统标识、`C:\` 即挂载点。默认输出稳定 POSIX 布局
  （`-P` 形式）：
  ```
  Filesystem 1K-blocks      Used Available Use% Mounted on
  C:         209715196 173534372  36180824  82% C:\
  ```
- **单位**：默认 1024 字节块；`-h` 人类可读（1024 幂，GNU 格式 1K/234M/2G）、
  `-H` 用 1000 幂；`-B SIZE` 按 GNU SIZE 解析（`512`、`1M`、`KB`、`2GB`……）。
- **列与过滤**：`-T` 在 Filesystem 后加 Type 列（NTFS/FAT32/CDFS…，来自
  GetVolumeInformation）；`-t TYPE` 只列指定类型、`-x TYPE` 排除（可重复，
  大小写不敏感）；`-l` 只列本地盘（跳过网络盘）。
- **范围与总数**：带参数时只列参数所在盘（盘符 `C:`/`C:\` 须为真实盘、路径须
  存在，否则报错退出 1，同 GNU）；`--total` 追加合计行；`-a` 连不可查询的盘
  （如无光盘的 CD-ROM）也列出（尺寸列显示 `-`）。
- **`-i/--inodes` 不支持**：Windows 没有 inode 概念，Win32 不提供卷级 inode 数，
  按项目诚实原则明确报错退出 1（而非编造数字）。

## sh 说明

`sh` 是 winux 内置的轻量 POSIX 风格 shell，解决两个宿主 shell 的短板：
**PowerShell 和 cmd.exe 都不支持 heredoc（`<<EOF … EOF`）语法**，有了 `sh`
之后两种 shell 都能写真正的多行输入脚本：

```
PS> winux sh -c "cat <<EOF`nhello 世界`nEOF"
C> winux sh demo.sh（脚本文件里写）
```

支持的子集（与 bash 习惯一致）：

- **heredoc**：`<<EOF`（正文里的 `$VAR` 会展开）、`<<'EOF'`（正文原样，不展开）；
  正文从命令行的下一行开始，直到单独一行等于定界符。
- **管道与条件**：`cmd1 | cmd2 | cmd3`（多级管道各阶段以 winux.exe 子进程串联）、
  `;`、`&&`、`||`（退出码驱动，语义同 bash）。
- **重定向**：`> file`（覆盖）、`>> file`（追加）、`< file`、`2> file`、`2>> file`、
  `2>&1`（stderr 并入 stdout）。
- **引号**：单引号原样、双引号内 `$VAR` 展开、`\` 只转义 shell 元字符
  （`C:\Users\...`、`\\server\share` 里的反斜杠**原样保留**，Windows 路径友好）。
- **变量**：`$VAR`、`${VAR}`、`$?`（上一条命令退出码）；`NAME=值 命令` 只对
  该命令生效，单独 `NAME=值` 则持久（bash 同款语义）。
- **内建**：`cd`、`exit [n]`、`:`、`true`、`false`；其余命令全部走 winux 命令表。
- **三种用法**：`sh -c "脚本"`、`sh 脚本文件`、无参数（交互式 REPL，提示符 `sh>`；
  非终端 stdin 时直接整段执行）。

不支持（会按字面处理或报错）：`$(...)` 命令替换、`$((...))` 算术、布尔测试
`[ ... ]`、后台 `&`（会警告并同步执行）。需要时用脚本文件 + 各命令的组合代替。

## 网络命令（只读策略）

网络命令分两类：**只读查询**（`netstat`、`ss`、`ip a/link/route/neigh show`、
`nmcli device/connection show`、`ifconfig`）直接读取内核套接字/路由/适配器表
（iphlpapi 原生 API），**绝不改动网络配置**；**改动网络的子命令**（`ip addr add`、
`ip link set`、`ip route add`、`nmcli device connect`、`ifconfig /release` 等）
一律**拒绝**并给出原生替代（`netsh …`），防止误操作弄断网络：

```
$ winux ip addr add 9.9.9.9/32 dev WLAN
ip addr add changes the network state and is refused (winux keeps networking read-only).
Use the native equivalent when you really need it: netsh interface ipv4 add address "NAME" ADDR MASK
```

- `netstat` 采用 Windows 布局（`Proto  Local  Foreign  State`），`ss` 采用
  Linux（iproute2）布局（`State  Recv-Q  Send-Q  Local:Port  Peer:Port`），两者
  读同一份表。
- `ifconfig` 默认直通系统 `ipconfig.exe`（`ifconfig /all` 可用；`ipconfig`
  是它的别名），只有 `/release /renew /flushdns` 等改网参数被拒绝。
- `nmtui` 不做任何查询/修改：它直接调出 Windows 原生的**网络连接**面板
  （`control.exe ncpa.cpl`，即 控制面板\网络和 Internet\网络连接），把
  NetworkManager 文本界面的「连接管理」职责交给 Windows 图形界面（用户自己
  在面板里操作）；`nmtui hostname` 打开设置里的「重命名这台电脑」页。
- **网络相关测试必须显式开启**：单元测试默认跳过网络用例，设环境变量
  `WINUX_NET_TESTS=1` 才运行（都是只读查询与「拒绝」用例）；
  冒烟测试的网络段落需 `smoke.ps1 -Network`。请先确认自己明白这些是只读操作再开。

## tcpdump 说明

`tcpdump` 在 Windows 上诚实映射到**原生 pktmon**（Windows Packet Monitor，Win10
2004+ 自带，无需 WinPcap/Npcap）：

- **抓包**（无 `-r`）：解析接口与过滤器后自动 **UAC 提权**，内部执行
  `pktmon start --capture` → `pktmon stop` → `etl2pcap` 把 ETL 转成 pcapng，
  再用纯 Go 解析输出 tcpdump 风格行（时间戳、IP、端口、TCP flags、载荷）。
  非提权启动会弹 UAC（管理员 shell 可用时直接捕获，`-D`/`-i` 用 `pktmon list`）。
- **离线解析**（`-r FILE`）：纯 Go 直接读 **pcap/pcapng**（无需 pktmon、无需
  提权），Ethernet / 回环 / RAW 链路，IPv4/IPv6、TCP/UDP/ICMP/ICMPv6/ARP 解码；
  `-e` 显示链路头，`-v` 显示 tos/ttl/id/flags 详情，`-A`/`-X` 打印载荷。
- **过滤表达式**：`host`、`port`、`tcp`/`udp`/`icmp`/`arp`、`ip`/`ip6`、
  `and`/`or`/`not` 与括号。pktmon 不支持的原语（`portrange`、`greater`、
  `less` 等）**诚实报错**而非假装支持。
- `-w FILE` 写 pcap（捕获时写 pcapng，`-w` 转成经典 pcap）；`-D` 列出接口；
  `-n`/`-N` 恒为「不解析名字」（Windows 名字解析慢且不可靠，与 pktmon 一致）。
- 提权后捕获会把临时 ETL/pcapng 放在 `%TEMP%` 并自动清理；Ctrl+C 可提前停止。

## mysql / redis-cli 说明

两者都是 **winux 内置的纯 Go 客户端**，只连 TCP，**不需要安装 MySQL/Redis
客户端**，也不依赖系统库：

- `mysql`：完整握手与认证（`mysql_native_password` 与 `caching_sha2_password`，
  SHA-2 全认证走 RSA 公钥加密），`mysql>` / `    ->` 交互提示（`;`/`\g` 表格、
  `\G` 竖直、`\c` 取消当前语句、`\q`/`exit`/`quit` 退出），`-e "SQL"` 非交互执行、
  `-B -N -t -r -E` 控制输出形状，`-D` 选库，`-p`/`-pSECRET`/`--password=SECRET`
  传密码（不带密码则交互隐藏输入）。无 `--ssl`：本机/内网默认明文 TCP，与
  Windows 上不装客户端的最简用法一致。
- `redis-cli`：RESP2 协议，`host:port>` 交互提示，`-a` 认证（NOAUTH 时警告）、
  `-n` 选库、`-r N -i SEC` 重复执行、`-x` 把 stdin 作为最后一个参数、`-d` 分隔符、
  `--raw` 原始输出。命令不做白名单——任何服务器支持的命令原样直发。

## nc 说明

`nc`/`netcat` 是 winux 内置的纯 Go TCP/UDP 工具（Windows 没有原生 netcat），
stdin 与对端双向转发：

- **连接模式** `nc HOST PORT`：`echo hello | nc 127.0.0.1 8080`；stdin 结束后
  对连接做半关闭（FIN），对端关闭连接即退出（GNU 语义，丢弃未发出的 stdin
  余量）。`-u` 切 UDP（stdin 结束即退出，OpenBSD 语义）；`-s ADDR`/`-p PORT`
  指定本地源地址/端口；端口可写服务名（`nc host http`）。
- **监听模式** `nc -l [-p PORT] [HOST] PORT`：接受一个连接并转发；`-k` 持续
  接受多个；`-u -l` 收 UDP 报文打印到 stdout，stdin 行发回最近来源。
- **`-z` 端口扫描**：只建连不发数据，成功 0 / 失败 1；配合 `-v` 在 stderr
  打印 `Connection to HOST PORT port [tcp/N] succeeded!`。
- **`-w SECS`**：连接超时 + 无数据空闲超时；**`-q SECS`**：stdin 结束后延迟
  关闭；**`-i SECS`**：发出的行之间间隔；**`-n`**：host 必须是数字地址；
  **`-4`/`-6`**：限定地址族。Ctrl+C 中止并退出 130。
- **`-e CMD`/`-c CMD`（连上后执行命令）在 Windows 上拒绝**（经典 RCE 入口，
  且没有把 socket 绑到子进程 stdio 的原生可靠办法）——提示改用
  `winux sh -c` + 重定向。

## clickhouse 说明

`clickhouse` 是 winux 内置的纯 Go ClickHouse 客户端（**无需安装
clickhouse-cli**），走 **HTTP 接口**（默认 `localhost:8123`），SQL 以
`X-ClickHouse-User`/`X-ClickHouse-Key` 头认证，`database`/`default_format`
参数选库与决定输出格式：

- **连接选项**：`-h/--host`（默认 localhost）、`--port`（默认 8123；
  `--secure` 切 HTTPS 且默认端口变 8443）、`-u/--user`（默认 default）、
  `--password`（可 `--password=SECRET` 或 `--password SECRET`；裸写时交互
  隐藏输入）、`-d/--database`（默认 default）、`--timeout SECS`（默认 30）。
- **执行方式**：`-q/--query "SQL"`（可重复，多条自动按语句切分逐条执行）、
  `-e/--execute` 同义；或 `echo "SELECT 1" | clickhouse`（stdin 管道）；
  或 `clickhouse client`（clickhouse-cli 风格）进入**交互 REPL**
  （`host:8123>` 提示，默认一行一条查询，`-m/--multiline` 改为累积到 `;`；
  `\q`/`exit` 退出）。
- **输出格式**：`-f/--format` 显式指定（Pretty、TSV、CSV、JSON…）；未指定时
  终端用 `PrettyCompact`、管道/重定向用 `TabSeparated`；SQL 里自带
  `FORMAT X` 时以 SQL 为准（服务端行为）。

## mongodb 说明

`mongodb` 是 winux 内置的纯 Go MongoDB 客户端（**无需安装
mongosh/mongo**），直接讲 wire 协议：手写 BSON 编解码、OP_MSG 消息、
SCRAM-SHA-256 认证（含服务端签名校验），全部标准库。

- **连接选项**：`-h/--host`（默认 localhost）、`--port`（默认 27017）、
  `-u/--username`（给定时触发 SCRAM-SHA-256 认证）、`--password`
  （`=SECRET`/` SECRET`/裸写交互隐藏输入）、`--authenticationDatabase`
  （默认 admin）、`-d/--database`（默认 test）、`--tls`/`--tlsInsecure`、
  `--timeout SECS`。
- **执行方式**：`--eval '<命令>'` 一次执行；或 stdin 管道逐行；或进入
  **交互 REPL**（`dbname>` 提示，`use <db>`、`show dbs`、
  `show collections`、`\q`/`exit` 退出）。
- **简式语法** `db.<集合>.<方法>(<json>)`：find/findOne/insertOne/
  insertMany/updateOne/updateMany/replaceOne/deleteOne/deleteMany/
  aggregate/countDocuments/distinct/drop/create/createIndexes；支持
  `ObjectId("hex")` 与无引号键（mongosh 习惯），如
  `db.users.findOne({_id: ObjectId("507f1f77bcf86cd799439011")})`。
  也接受**原样 JSON 命令文档**（`{"ping":1}`、`{"find":"users","filter":…}`）
  ——不做 JavaScript 解释，因此没有 mongosh 的 JS 语法。
- **输出**：回复文档逐行 JSON（`-json` 是默认格式，`--format pretty`
  缩进）；find/aggregate 自动只输出 `cursor.firstBatch`。

## psql 说明

`psql` 是 winux 内置的纯 Go PostgreSQL 客户端（**无需安装 psql**），走
v3 wire 协议：启动握手、认证（明文 / MD5 / SCRAM-SHA-256）、简单查询协议
（Q → RowDescription/DataRow/CommandComplete），全部标准库。

- **连接选项**：`-h/--host`（默认 localhost）、`--port`（默认 5432）、
  `-U/--username`（默认 postgres）、`-d/--dbname`（默认同用户名；也接受
  位置参数 `psql mydb`）、`-W/--password`（交互隐藏输入）、`-w/--no-password`、
  `--ssl`（TLS，不校验服务器证书）、`--timeout SECS`。密码来源：
  `PGPASSWORD` 环境变量 > `-W` 提示 > 非交互时明确报错。
- **执行方式**：`-c/--command "SQL"`（可重复）、`-f/--file`、stdin 管道
  批量、或 `psql -U postgres mydb` 进入**交互 REPL**（`db=> ` 提示，
  `\q` 退出、`\l` 数据库列表、`\dt` 表列表、`\d NAME` 表列、
  `\c DB` 切换连接、`\?` 帮助）。
- **输出**：psql 风格对齐表格；`-t/--tuples-only` 只输出数据行、
  `-A/--no-align` 制表符分隔；一条 SQL 的多个结果集依次输出。

## sqlite 说明

`sqlite` 是 winux 内置的 SQLite 客户端，驱动为**纯 Go 移植
modernc.org/sqlite**（无 CGO、跨平台），所以不需要安装 sqlite3 CLI。

- **用法**：`sqlite [OPTIONS] [DBFILE] [SQL]`。缺省 DBFILE 用
  `:memory:`；给 SQL 则执行后退出（可多条，`;` 分隔），否则进入
  **交互 REPL**（`sqlite> ` 提示，SQL 以 `;` 结束，多行累积）。
- **选项**：`-r/--readonly`（只读打开）、`--json`（结果数组 JSON）、
  `--line`（`字段 = 值`）、`--list` + `--separator SEP`（默认 `|`）、
  `--column`（默认对齐表）、`--nullvalue VALUE`、`-H/--header` 与
  `-N/--noheader`、`--cmd SQL`（进入 REPL 前先执行，可重复）、
  `-v/--verbose`（stderr 回显每条 SQL）。
- **REPL 点命令**：`.tables [模式]`、`.schema [表]`、`.databases`、
  `.indexes [表]`、`.dump [表]`、`.mode column|list|json|line`、
  `.headers on|off`、`.separator`、`.nullvalue`、`.read FILE`、`.quit`。
- **注意**：这是 winux 唯一的第三方 Go 模块（见「构建与测试」的依赖
  说明）；INSERT/UPDATE/DELETE/REPLACE 显示
  `Query OK, N rows affected`，DDL 静默执行。

## systemctl 说明

`systemctl` 用 Windows 原生机制实现 systemd 风格的单元管理，**不引入任何
外部组件**（不需要安装 Cygwin/systemd）。单元文件是 INI 格式的
`*.service`，放在 `%ProgramData%\systemctl\units`（可用环境变量
`SYSTEMCTL_UNIT_PATH` 追加目录）。

- **单元解析**：`[Unit] Description=`、`[Service] Type=simple|wrapped|native`
  （默认 wrapped）、`ExecStart=`（引号感知拆分，首个 token 为程序）、
  `WorkingDirectory=`、`Restart=on-failure|always|no`、`KillTimeoutSec=`、
  `Environment=A=1 B="two words"`、`[Install] WantedBy=`。
- **后端映射**：`wrapped` = 内置 `svchost` 服务宿主（托管 ExecStart 子进程，
  输出转发到 `<unit>.wrapper.log`，`journalctl -u` 可读）；`native` = 服务
  直接以 ExecStart 的命令行注册；`Type=simple` 且 ExecStart 为空 = 计划任务
  （schtasks，支持 login/loginsvc 触发）；纯 `[Install]` 单元 = 计划任务
  （login 登录触发）。状态保存在 `%ProgramData%\systemctl\state`。
- **常用命令**：`systemctl status NAME`（运行/已安装/启用状态+详情）、
  `start/stop/restart NAME`、`enable/disable NAME`（开机启动）、
  `is-active/is-enabled NAME`（供脚本判断，退出码 3/1）、`cat NAME`（打印
  单元）、`list-units`/`list-unit-files`、`mask/unmask NAME`、
  `journalctl -u NAME [-n N] [-f] [-r] [--since TIME]`、`daemon-reload`。
  变更类命令需要管理员权限（会先检查令牌并明确提示）。
- **内置单元 `sshd`**：winux 的 SSH 服务端是内置单元，无需手写
  .service 文件——`systemctl status sshd` / `start sshd` / `cat sshd`
  直接可用（内部映射到服务名 **winux-sshd**，首次使用自动物化单元文件
  到 `%ProgramData%\systemctl\units\winux-sshd.service`；只读命令在非
  管理员下也能解析，变更命令用 `winux sudo systemctl ...`）。
- **示例**：把 winux sshd 装成开机自启服务
  `winux sudo systemctl enable sshd` + `start sshd`
  （或等价的 `winux sshd --install`，两者管理同一个 winux-sshd 服务）。

## sshd / wssh 说明

winux 的 SSH 服务端与客户端都是**纯 Go**（golang.org/x/crypto/ssh），
Windows 上**不需要安装 OpenSSH 组件**。

- **服务端 `sshd`**：默认监听 `0.0.0.0:1022`（避开 22 与 OpenSSH 冲突）。
  认证默认 `--auth local`——用 Windows 本地账号密码校验（LogonUserW，
  仅限双方机器上的本地用户）；`--auth any` 接受任意密码（仅测试/内网）。
  连入后就是 winux 环境：SSH exec 请求跑 `winux sh -c <cmd>`（管道、重定向、
  `&&` 全可用），shell 请求挂一个 ConPTY 伪控制台跑 `winux sh`（交互 REPL，
  支持终端尺寸变更）。host key（ed25519）首次运行自动生成在
  `%ProgramData%\winux\sshd_host_ed25519`（不可写时回退
  `%LOCALAPPDATA%\winux`）。
- **服务安装**：`winux sshd --install` 以管理员注册并启动 **winux-sshd**
  服务（`--uninstall` 移除），也可直接用
  `winux sudo systemctl start sshd` / `status sshd` / `journalctl -u sshd`
  管理同一个服务。前台跑 `winux sshd` 即可（`--port N`、`--listen HOST`、
  `--log FILE`）。
- **客户端 `wssh`**：`winux wssh [-p 密码] [-P 端口] [-l 用户] user@host[:port] [command]`
  ——不给命令进交互 shell（请求远端 pty，本机进入 raw 模式，窗口变化自动
  推送 `window-change`）；给命令则执行并透传退出码。默认端口 1022；
  连 OpenSSH 服务器用 `-P 22`。认证支持 password 与 keyboard-interactive
  （连那些只开键盘交互的服务器也能用）；`--timeout SECS`（默认 15）覆盖
  **整个连接+握手+认证过程**——服务器握手阶段无响应时不会无限挂起。
  `-v` 打印连接/认证阶段日志便于排障。两台的 winux 互相连接：本机
  `winux sshd`（或装成服务），另一台 `winux wssh 对方IP`（激活环境后直接
  `wssh`）。

## 构建与测试

```powershell
# 单元测试（flags、glob/tilde 展开、cp/rm/mv/mkdir/touch/ls、grep 上下文、
#            head/tail/wc/sort/uniq/cut/tr、pager/vi 内部逻辑、date 解析、
#            chmod/cal、ps、nc 回环、clickhouse 假 HTTP 端、mongodb/psql
#            假 wire 端、真 sqlite 内存/临时库；netstat/ss、ip/nmcli/ifconfig
#            等网络用例默认跳过，见「网络命令（只读策略）」）
go test ./...

# 一键构建 + 垫片 + 激活脚本（-Version x.y.z 正式版 / -Version dev 用 git commit id）
powershell -ExecutionPolicy Bypass -File .\build.ps1

# 端到端冒烟测试（PowerShell 与 cmd.exe 各一节，224 项断言）
powershell -ExecutionPolicy Bypass -File .\smoke.ps1

# 冒烟测试另跑只读网络段落（netstat/ss/ip/nmcli/ifconfig，在常规项之上增加）
# ——需要你确认只读后使用
powershell -ExecutionPolicy Bypass -File .\smoke.ps1 -Network
```

也可以 `go build -o bin\winux.exe .` 手动构建。`build.ps1 -RealExes` 可改为生成
每个命令的真实 exe（硬链接，省空间），`-CopyInsteadOfHardLinks` 用复制。

**依赖说明**：winux 的第三方 Go 模块共三个：`modernc.org/sqlite`（`sqlite`
命令用，纯 Go 无 CGO）；它要求 `go 1.25` 以上，因此 `go.mod` 的 `go`
指令已从 1.21 升到 1.25。其余全部命令（含 mongodb/psql/clickhouse/mysql/
redis-cli/nc/tcpdump 等协议客户端）都只依赖 Go 标准库。

**版本号**由 `build.ps1` 构建时经 `-ldflags -X` 注入。**默认（不带
`-Version`）就是开发版**：取本地 git 仓库 `HEAD` 的后 8 位 commit id
（`git rev-parse --short=8 HEAD`）作为版本号，同时注入构建时间
（`yyyy-MM-ddTHH:mm:ss`）；机器上没有 git 或不在 git 仓库内则回退为
`1.0.0`。`-Version x.y.z` 注入正式发布版本（仅该模式为 release）；
`-Version dev/development` 与默认相同。因此：

```
$ winux --version
winux 769020d4 (built 2026-09-14T09:08:54)
$ cp --version
cp (winux) 769020d4 (built 2026-09-14T09:08:54)
```

`winux version`、`winux -V`、全局 `--help` 首行与各命令的 `--version`
一致显示；`uname -v` 输出 `winux <commit>`。直接 `go build`（不经
build.ps1）不注入，`Version` 保持默认 `1.0.0`、不显示 build time。

## 目录结构

```
winux/
├─ main.go           薄壳：只 import winux/core 并调用 core.Main()
├─ core/             核心包 package core（全部命令实现）
│  ├─ main.go        命令注册表、--help/--version、全局调度
│  ├─ export.go      面向嵌入/测试的导出 API（Execute、CommandNames、纯函数助手）
│  ├─ flags.go       GNU getopt 风格参数解析（-rf、-n5、--opt=val、--）
│  ├─ util.go        通配符/~/日期/列表/转义等公共工具
│  ├─ fs.go          cp rm mv mkdir touch ln
│  ├─ ls.go          ls（+ ls_windows.go：隐藏/只读属性、文件 ID、访问/创建时间）
│  ├─ du.go          du（+ du_windows.go：NTFS 实际分配大小、卷判断）
│  ├─ text.go        cat head tail wc sort uniq cut tr
│  ├─ grep.go        grep（递归/上下文/颜色/退出码）
│  ├─ page.go        more / less 分页器（VT 终端，非控制台自动降级）
│  ├─ vi.go          vi/vim 编辑器
│  ├─ sys.go         echo pwd clear date uname hostname which id whoami sudo su
│  ├─ account.go     useradd userdel usermod groupadd groupdel chmod chown chgrp passwd
│  ├─ cal.go         cal 日历
│  ├─ proc.go        ps（+ proc_windows.go 的 WMI 快照与属主解析）
│  ├─ netstat.go     netstat / ss（+ net_windows.go 的 iphlpapi 表读取）
│  ├─ ip.go          ifconfig / ip / nmcli / nmtui（+ ip_windows.go 适配器列表）
│  ├─ activate.go      winux activate/deactivate（父进程识别含垫片穿透，winux- 前缀）
│  ├─ shimgen.go       内嵌垫片解压（md5 增量）+ winux shims（+ shimdata/ 模板）
│  ├─ term_windows.go  控制台原始模式、按键解码、终端尺寸（Windows）
│  ├─ tcpdump.go       tcpdump（+ tcpdump_windows.go：pktmon 抓包；pcap/pcapng 解析）
│  ├─ mysql.go         mysql 纯 Go 客户端（握手/RSA 认证/交互 REPL）
│  ├─ redis.go         redis-cli 纯 Go 客户端（RESP2/交互 REPL）
│  ├─ nc.go            nc / netcat（TCP/UDP 连接与监听，stdin 双向转发）
│  ├─ clickhouse.go    clickhouse 纯 Go 客户端（HTTP 接口/交互 REPL）
│  ├─ mongo.go         mongodb 纯 Go 客户端（BSON/OP_MSG/SCRAM/交互 REPL）
│  ├─ psql.go          psql 纯 Go 客户端（wire v3/MD5·SCRAM/交互 REPL）
│  ├─ sqlite.go        sqlite 客户端（内嵌 modernc.org/sqlite，交互 REPL）
│  ├─ uptime.go        uptime（+ uptime_windows.go：GetTickCount64/WTS 会话数）
│  └─ *_other.go    非 Windows 空实现（便于交叉编译/CI）
├─ test/             package core_test 的单元测试（harness + 各命令）
├─ .gitignore       忽略构建产物（bin/、winux.exe 等）
├─ README.md        使用文档（快速开始/命令表/各命令说明）
├─ CHANGELOG.md     更新记录（历次功能与修复，倒序）
├─ .gitattributes   文本统一 LF、二进制不规范化
├─ build.ps1        构建（版本/构建时间经 -ldflags 注入；垫片内嵌于 exe，构建后经 `winux shims` 解压）
├─ install.ps1      持久化安装（用户 PATH + PowerShell 配置档）
├─ smoke.ps1        双 shell 端到端冒烟测试（-Network 额外跑只读网络段）
└─ bin/             构建产物：winux.exe、winux-activate.ps1/winux-deactivate.ps1
   └─ cmd/          *.cmd 垫片（cp.cmd/rm.cmd/…）+ winux-activate.cmd/winux-deactivate.cmd
```

## 更新记录

历次功能与修复按时间倒序记录在 [CHANGELOG.md](CHANGELOG.md)，这里不再重复。

## 许可

MIT。
