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
winux activate | iex                                  # PowerShell
for /f "delims=" %l in ('winux activate') do @%l      # cmd.exe
```

退出激活（撤销 PATH/别名/宏/环境标记，恢复到激活前状态）：

```powershell
winux deactivate | iex                                  # PowerShell
for /f "delims=" %l in ('winux deactivate') do @%l      # cmd.exe
# 或直接（不依赖 PATH）：
. .\bin\winux-deactivate.ps1        # PowerShell
call .\bin\cmd\winux-deactivate.cmd # cmd.exe
```

> **布局与命名**：`winux.exe` 与 `cmd\` 目录并排。全部 `.cmd` 垫片与激活辅助
> 脚本（`winux-activate.ps1`/`winux-deactivate.ps1`、`cmd\winux-*.cmd`）都
> **内嵌在 winux.exe 里**，由 `winux activate`（或 `winux shims`）按 **md5 增量**
> 解压到 exe 旁：文件不存在则创建，存在且 md5 相同则跳过（不重写、不动时间戳），
> md5 不同才覆盖。因此把 winux.exe 单独拷到任何目录都能就地重建 `cmd\`。
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
| 不激活也能用 `venv\Scripts\python.exe` | 不激活也能用 `winux cp ...` 或直接调 `bin\cmd\cp.cmd` |

`winux activate` / `winux deactivate` 是内建命令：先按 **md5 增量**把内嵌的
垫片与辅助脚本解压到 exe 旁（见上「布局与命名」），再**识别父 shell**
（PowerShell 还是 cmd.exe，`--shell` 可覆盖）后打印一行让 shell 自己执行的
代码——PowerShell 打 `. '…\winux-activate.ps1'`（配
`| iex` 执行），cmd 打 `call "…\cmd\winux-activate.cmd"`（配
`for /f "delims=" %l in ('winux activate') do @%l` 执行）。这样只要有
`winux` 就能在任意位置激活，不再需要 install.ps1（install 只是把「全局
winux + 每窗口自动激活」自动化）。

`activate` 具体做三件事（都是会话级）：

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

名字解析优先级（这就是「遮蔽」而非「替换」的原理）：

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
| `id` | `-u -g -G -n`；输出 POSIX 形态身份（uid/gid 恒为 1000，见限制） |
| `whoami` | 当前用户 |
| `sudo` | `-i -s -u -E -n`；在 Windows 上以 UAC 提权重跑命令/开管理员 shell（见下） |
| `su` | `-c -l -s`；root/administrator → UAC 管理员 shell；其它用户 → `runas.exe` |
| `useradd` | `-m -d -c -p -g -G -s -u -r -e --dry-run`；映射为 `net user` + 可选的 `net localgroup`（见下「账户与权限」） |
| `userdel` | `-r -f --dry-run`；映射为 `net user NAME /delete` |
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
| `ipconfig` | 默认直通系统 `ipconfig.exe`；`--help`/`--version` 为 winux 自带；`/release /renew /flushdns` 等改网操作被拒绝（见下「网络只读」） |
| `ip` | `addr/link/route/neigh` 的 `show`（含 `-4 -6 -br -o`）；iproute2 输出风格；一切改网子命令（`addr add`、`link set`、`route add`…）拒绝并提示原生替代 |
| `nmcli` | `device status/show`、`connection show`、`general status/permissions/hostname`；改网子命令拒绝（Windows 无 NetworkManager，列表由适配器推导） |
| `nmtui` | 执行即调出 Windows「网络连接」面板（`ncpa.cpl`，对应 控制面板\网络和 Internet\网络连接）；`nmtui hostname` 打开「重命名这台电脑」设置页；`edit/connect` 同面板（见「网络命令（只读策略）」末尾） |

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
- **单用户**：`id`/`whoami` 输出当前账户的合成 uid/gid（1000），仅作兼容显示。
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
`nmcli device/connection show`、`ipconfig`）直接读取内核套接字/路由/适配器表
（iphlpapi 原生 API），**绝不改动网络配置**；**改动网络的子命令**（`ip addr add`、
`ip link set`、`ip route add`、`nmcli device connect`、`ipconfig /release` 等）
一律**拒绝**并给出原生替代（`netsh …`），防止误操作弄断网络：

```
$ winux ip addr add 9.9.9.9/32 dev WLAN
ip addr add changes the network state and is refused (winux keeps networking read-only).
Use the native equivalent when you really need it: netsh interface ipv4 add address "NAME" ADDR MASK
```

- `netstat` 采用 Windows 布局（`Proto  Local  Foreign  State`），`ss` 采用
  Linux（iproute2）布局（`State  Recv-Q  Send-Q  Local:Port  Peer:Port`），两者
  读同一份表。
- `ipconfig` 默认直通系统 `ipconfig.exe`（`ipconfig /all` 可用），只有
  `/release /renew /flushdns` 等改网参数被拒绝。
- `nmtui` 不做任何查询/修改：它直接调出 Windows 原生的**网络连接**面板
  （`control.exe ncpa.cpl`，即 控制面板\网络和 Internet\网络连接），把
  NetworkManager 文本界面的「连接管理」职责交给 Windows 图形界面（用户自己
  在面板里操作）；`nmtui hostname` 打开设置里的「重命名这台电脑」页。
- **网络相关测试必须显式开启**：单元测试默认跳过网络用例，设环境变量
  `WINUX_NET_TESTS=1` 才运行（都是只读查询与「拒绝」用例）；
  冒烟测试的网络段落需 `smoke.ps1 -Network`。请先确认自己明白这些是只读操作再开。

## 构建与测试

```powershell
# 单元测试（flags、glob/tilde 展开、cp/rm/mv/mkdir/touch/ls、grep 上下文、
#            head/tail/wc/sort/uniq/cut/tr、pager/vi 内部逻辑、date 解析、
#            chmod/cal、ps；netstat/ss、ip/nmcli/ipconfig 等网络用例默认跳过，
#            见「网络命令（只读策略）」）
go test ./...

# 一键构建 + 垫片 + 激活脚本
powershell -ExecutionPolicy Bypass -File .\build.ps1

# 端到端冒烟测试（PowerShell 与 cmd.exe 各一节，141 项断言）
powershell -ExecutionPolicy Bypass -File .\smoke.ps1

# 冒烟测试另跑只读网络段落（netstat/ss/ip/nmcli/ipconfig，共 154 项断言）
# ——需要你确认只读后使用
powershell -ExecutionPolicy Bypass -File .\smoke.ps1 -Network
```

也可以 `go build -o bin\winux.exe .` 手动构建。`build.ps1 -RealExes` 可改为生成
每个命令的真实 exe（硬链接，省空间），`-CopyInsteadOfHardLinks` 用复制。

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
│  ├─ account.go     useradd userdel usermod groupadd groupdel chmod chown chgrp
│  ├─ cal.go         cal 日历
│  ├─ proc.go        ps（+ proc_windows.go 的 WMI 快照与属主解析）
│  ├─ netstat.go     netstat / ss（+ net_windows.go 的 iphlpapi 表读取）
│  ├─ ip.go          ipconfig / ip / nmcli / nmtui（+ ip_windows.go 适配器列表）
│  ├─ activate.go      winux activate/deactivate（父进程识别，winux- 前缀）
│  ├─ shimgen.go       内嵌垫片解压（md5 增量）+ winux shims（+ shimdata/ 模板）
│  ├─ term_windows.go  控制台原始模式、按键解码、终端尺寸（Windows）
│  └─ *_other.go    非 Windows 空实现（便于交叉编译/CI）
├─ test/             package core_test 的单元测试（harness + 各命令）
├─ .gitignore       忽略构建产物（bin/、winux.exe 等）
├─ README.md        使用文档（快速开始/命令表/各命令说明）
├─ CHANGELOG.md     更新记录（历次功能与修复，倒序）
├─ .gitattributes   文本统一 LF、二进制不规范化
├─ build.ps1        构建（垫片/激活脚本内嵌于 exe，构建后经 `winux shims` 解压）
├─ install.ps1      持久化安装（用户 PATH + PowerShell 配置档）
├─ smoke.ps1        双 shell 端到端冒烟测试（-Network 额外跑只读网络段）
└─ bin/             构建产物：winux.exe、winux-activate.ps1/winux-deactivate.ps1
   └─ cmd/          *.cmd 垫片（cp.cmd/rm.cmd/…）+ winux-activate.cmd/winux-deactivate.cmd
```

## 更新记录

历次功能与修复按时间倒序记录在 [CHANGELOG.md](CHANGELOG.md)，这里不再重复。

## 许可

MIT。
