# 更新记录

本文件记录 winux 各版本/各次改动。README 只保留使用文档，这里按时间倒序
（新条目在上）记录每次变更。

- **2026-09-14 · 修复 wssh/ssh 登录后卡在 `sh>`（按键无效、无回显）**：
  - **根因在服务端 ConPTY 接线**：`CreatePseudoConsole` 的 `hInput`/`hOutput`
    被传成了**服务端自己用的那两头**（`inW`/`outR`），也就是官方文档与所有
    ConPTY 封装都反着用；同时子进程用 `STARTF_USESTDHANDLES` 把标准句柄
    换成了裸管道。结果是：输出能出来（走的是继承的 std 管道），但伪控制台
    **一个按键都收不到**——没有回显、没有行编辑、Enter 也无效，客户端却又
    进了 raw 模式，于是「提示符在、打字没反应、Ctrl+C 也退不出」。
    现在按文档接线：控制台读 `inR`（输入管道读端）、写 `outW`（输出管道
    写端），我们继续写 `inW`、读 `outR`；子进程 `STARTF_USESTDHANDLES`
    句柄留空、`bInheritHandles=false`、不再加 `CREATE_NEW_PROCESS_GROUP`，
    由伪控制台提供 CONIN$/CONOUT$。
  - **CR/LF**：控制台只认回车（CR）结束一行，客户端/脚本常发裸 `\n`，
    这种输入会永远躺在控制台输入缓冲区里。服务端新增
    `sshConsoleInput()` 把「前面不是 CR 的 LF」翻译成 CR（CRLF 原样保留），
    因此 `echo x\n` 这种脚本式喂入与真人按 Enter 都能执行。
  - **客户端 `wssh`**：`terminalSize()` 返回的是 `(rows, cols)`，交互模式却
    按 `(cols, rows)` 解构，导致 `RequestPty`/`WindowChange` 的行列颠倒
    （远端控制台尺寸错误、换行位置异常），已修正；远端 shell 结束后会等
    输出管道排空（最多 1s）再退出，不再吞掉最后几行输出。
  - **`wssh` 管道 stdin 崩溃**：stdin 是管道时（`echo ls | wssh host`、
    脚本喂命令），读线程到 EOF 与主流程各 `close(done)` 一次，正常跑完却以
    `panic: close of closed channel`（退出码 2）收场；该 channel 无人等待，
    现已删除，读线程遇 EOF/写失败直接返回。新增
    `TestWsshPipedStdinRunsRemoteShell`（管道喂 `echo …` + `exit`，断言退出
    码 0 且命令真的在远端执行）。
  - **`sh`**：REPL 读到的行去掉行尾 `\r`（控制台/CRLF 输入不再把 CR 带进
    最后一个参数）。
  - 测试：`core/ssh_conpty_test.go` 重写为**行为测试**——逐键输入必须被远端
    控制台回显、按 Enter（CR）必须执行该行、LF 结尾的脚本同样执行、最后
    `exit` 正常退出；新增 `TestWsshInteractiveInRealConsole`：后台起真
    `winux sshd`，再在**真控制台**（自建 ConPTY）里跑 `winux.exe wssh`，
    逐键输入 → 远端回显 → Enter 执行 → `exit` 退出，端到端复现用户场景。
    `test/ssh_test.go` 新增 `TestSSHShellPTYInteractive`（pty 会话逐键回显），
    `TestSSHShellPTY` 断言收紧为匹配整行输出（原先只看子串，回显即可蒙混）。

- **2026-09-14 · systemctl 生命周期对齐 systemd（restart 无需先 start、disable 不阻止 start）；新增 `lsof`；修复 IPv6 套接字表解析**：
  - **systemctl 生命周期语义**（此前是 SCM 语义，与 systemd 不一致）：
    - `restart` 不再无条件先 stop：只在单元**正在运行**时才停，对**已停止**
      的单元直接 start（此前对未运行的服务调 Control(Stop) 会被 SCM 拒绝，
      报 `restart failed: The service has not been started.`）。停止类调用
      把 `ERROR_SERVICE_NOT_ACTIVE`(1062) 视为成功，所以 `stop` 一个本来
      就没运行的单元不再算错误。
    - `disable` 只表示「不随开机自启」，**不再阻止手动 start**：启动前若服务
      启动类型是 Disabled，临时提升为 Manual 再启动，成功后**还原为
      Disabled**（`is-enabled` 依旧报告 disabled），正如
      `systemctl disable` 之后照样能 `systemctl start`。
    - `start` 现在会**等待服务真正进入 Running**（最多 10s）：服务刚起就退出
      时报 `the service exited immediately (check ExecStart and 'systemctl
      journalctl -u NAME')`，超时报 timed out，不再把「SCM 受理了 StartService」
      当成启动成功。
    - `enable`/`disable`/`restart` 在单元既无单元文件也无后端时给出明确
      提示而不是崩溃或空转。
  - **新增 `lsof`**（与 `netstat`/`ss` 同源，读 `GetExtendedTcpTable`/
    `GetExtendedUdpTable`）：lsof 的选择语法与列布局
    （`COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME`）；
    `-i [46][tcp|udp][@host][:port[-port]]`（`-i:1022`/`-i :1022`/`-i:80-90`/
    `-i:ssh`/`-iTCP@10.0.0.1:22`）、`-p PID[,PID]`、`-c NAME`、`-u USER`、
    `-s PROTO:STATE`（`-sTCP:LISTEN`、`-sTCP:^TIME_WAIT`）、`-a`（并集→交集）、
    `-4/-6`、`-t`（只打印 PID）、`-n`、`-P`，`^` 前缀为排除；退出码 0（有匹配）
    /1（无匹配）。Windows 无 `/proc`，故只列**进程映像（FD txt）与进程占有的
    套接字（TYPE IPv4/IPv6）**，内核持有的 pid 0 套接字（TIME_WAIT 等）与真实
    lsof 一样不列出；主机名反解并行执行（最多 16 并发、单个 500ms 上限），
    解析器不可达时不会拖慢命令。
  - **修复 IPv6 套接字解析**（`GetExtendedTcpTable` 的 v6 行）：此前按 v4 的
    字段次序读 `MIB_TCP6ROW_OWNER_PID`，导致 IPv6 行的地址、端口、状态整体
    错位（表现为 `ss -tln`/`netstat -an` 里 v6 监听显示成 `*:*->*:512` 之类）。
    现在按 `ucLocalAddr[16], dwLocalScopeId, dwLocalPort, ucRemoteAddr[16],
    dwRemoteScopeId, dwRemotePort, dwState, dwOwningPid` 正确读取，
    `ss`/`netstat`/`lsof` 的 IPv4/IPv6 行都正确。
  - 测试：新增 `core/lsof_parse_test.go`（选择器解析、状态名、列渲染、
    并集/交集/排除规则、服务名↔端口）、`test/lsof_test.go`（真开监听/UDP
    套接字再查、`-t`、`-p` 映像、退出码、错误选项）、
    `test/systemctl_test.go` 新增 `TestSystemctlLifecycleLinuxSemantics`
    （真实服务跑 stop→restart→disable→start，断言不再出现
    "has not been started"/"because it is disabled"；需管理员，非提升
    shell 自动跳过）；smoke 新增 lsof 断言（`--help` 循环 + 网络段落）。
  - README：命令表新增 `lsof` 行，新增「lsof 说明」章节，
    「systemctl 说明」补「生命周期语义按 systemd（不是按 SCM）」小节。
- **2026-09-14 · systemctl 内置单元自愈：winux.exe 换路径后自动重同步**：
  - 物化的 `winux-sshd.service` 里 ExecStart 是**绝对路径**（物化时的
    winux.exe）。把 winux.exe 拷到新位置后，旧文件仍指向旧路径（如
    `D:\AIWorkSpace\...\bin\winux.exe`），服务实际从旧路径启动。
  - 修复：`sysResolveTarget` 对内置单元每次尝试用**当前运行的 exe**
    重写物化文件并重新加载（管理员下自动；status 非管理员显示失配时
    提示 `sudo systemctl daemon-reload`）；`daemon-reload` 开头重写内置
    单元并顺带修正服务的 binPath（指向当前 winux.exe）。
  - 用法：换了 winux.exe 位置后执行 `sudo systemctl daemon-reload`
    + `sudo systemctl restart sshd` 即可。
- **2026-09-14 · sudo 提权输出回显（sudo systemctl 不再静默失败）**：
  - 根因：`Start-Process -Verb RunAs` 启动的提权进程运行在**独立控制台**，
    其 stdout/stderr 用户不可见；加上此前"内部命令失败静默返回退出码"，
    `sudo systemctl start winux-sshd` 失败时没有任何输出，用户无法知道
    发生了什么。
  - 修复：非交互提权（sudo <cmd>）改为通过隐藏的 `sudo-helper` 命令执行
    ——提权进程把 stdout/stderr 写入临时文件，父进程在 PowerShell 结束后
    读回并**回显到用户终端**；失败退出码照常透传，UAC 被拒/异常时打印
    "elevation failed" 原因。`sudo -i`（交互管理员 shell）保持原行为。
  - `sudo-helper` 不注册为命令（不进 --help/命令表）。
- **2026-09-14 · 修复 wssh 连接 Linux OpenSSH 卡住；systemctl 内置 sshd 单元；`--version` 显示 exe 路径**：
  - **wssh 握手超时**：`ClientConfig.Timeout` 只覆盖 TCP dial，握手/认证阶段
    无超时——服务器握手不响应（丢包、慢 DNS/PAM）会无限挂起。改为手动
    `net.DialTimeout` + `SetDeadline` 覆盖**整个连接+握手+认证**，
    `--timeout`（默认 15s）对全程生效；握手结束后清除 deadline。
  - **wssh keyboard-interactive**：`Auth` 增加 `KeyboardInteractive` 回调
    （首个问题即已收集的密码），可连只开键盘交互认证（关 password 方法）
    的 Linux OpenSSH 服务器。
  - **wssh -v**：打印 TCP 连接、握手 deadline、认证完成各阶段日志，便于
    定位连接/认证问题。
  - **systemctl 内置 sshd 单元**：`systemctl status/start/stop/restart/
    enable/disable/cat/is-active sshd` 直接可用——`sshd` 别名映射到内置
    **winux-sshd** 单元（wrapped，svchost 托管 `winux.exe sshd`），首次
    使用自动物化单元文件到 `%ProgramData%\systemctl\units\
    winux-sshd.service`，与 `sshd --install` 管理同一个服务；非管理员下
    只读命令仍能解析（回退内存定义），`cat` 打印内置内容。
  - **sudo 报错修正**：提权后内部命令失败时直接透传其退出码，不再误报
    "elevation failed (was the UAC prompt declined?)"。
  - **`--version` 显示路径**：`winux --version` 与各命令（wssh/sshd/
    systemctl/svchost）的 `--version`/`-v` 输出后新增 `from: <exe 路径>`
    一行（经 `printVersion` 的 flag 框架命令同步生效）。
  - 测试：新增 TestSystemctlSshdBuiltinAlias（status/cat/is-active 别名）、
    TestWsshHandshakeTimeout（静默服务器在 --timeout 内失败）；README
    同步；smoke 版本断言适配 from 行。
- **2026-09-14 · 新增 `systemctl`/`svchost`/`sshd`/`wssh`，`build.ps1` 默认 dev**：
  - **`systemctl`**：systemd 风格服务管理器（纯 Go，参考 systemctl-win 工程
    的语义）：单元解析（INI + 引号感知 ExecStart）、四类后端——内置
    `svchost` 服务宿主（wrapped）、原生命令行服务（native）、计划任务
    （schtasks，login/loginsvc/login 触发）、掩码；`start/stop/restart/
    enable/disable/status/is-active/is-enabled/cat/list-units/
    list-unit-files/journalctl/mask/unmask/daemon-reload`；状态存
    `%ProgramData%\systemctl\state`，`journalctl -u` 读 wrapper.log。
  - **`svchost`**：内置 Windows 服务宿主（`x/sys/windows/svc`），替代
    systemctl-win 的 dsh-svchost.exe；解析 *.service 单元、托管 ExecStart
    子进程、taskkill /t 停进程树、GBK/OEM→UTF8 日志解码。
  - **`sshd`**：纯 Go SSH 服务端（`golang.org/x/crypto/ssh`，Windows **无需
    安装 OpenSSH**）：默认 `0.0.0.0:1022`；`--auth local` 用 LogonUserW 校验
    本地账号密码，`--auth any` 测试用；exec 请求跑 `winux sh -c`、shell 请求
    挂 **ConPTY 伪控制台**跑 `winux sh`（syscall 手写 ConPTY：CreatePipe→
    CreatePseudoConsole→属性列表→CreateProcess；修复了 HRESULT 判断、COORD
    按值传递、pty-req/window-change 按 RFC 4254 解析等调试坑）；host key
    ed25519 自动生成；`sshd --install/--uninstall` 注册 **winux-sshd** 服务。
  - **`wssh`**：纯 Go SSH 客户端（无需 OpenSSH）：`wssh [user@]host[:port]
    [command]`，默认端口 1022，密码认证（`-p` 或交互提示），交互模式请求
    pty 并转发本机终端（raw 模式 + 窗口变化推送）。
  - **`sh -i`**：`sh` 新增 `-i/--interactive`（伪控制台里非 tty 也进 REPL）。
  - **`build.ps1` 默认 dev**：不带 `-Version` 即用 git commit id 作开发版
    （无仓库回退 1.0.0），`-Version x.y.z` 才注入正式版。
  - 依赖：新增 `golang.org/x/crypto v0.50.0`、`golang.org/x/sys v0.40.0`
    （均与 Go 1.25 兼容并 pin 住）；命令表 63→67；新增
    core/ssh_conpty_test.go（ConPTY 回环）、test/ssh_test.go（真实 sshd 进程
    + x/crypto 客户端 + wssh CLI 的 exec/管道/退出码/ConPTY shell/连接失败）、
    test/systemctl_test.go（解析、cat/list/status 只读路径 + 非管理员拒绝）；
    smoke 新增 6 项（help 循环 +4、systemctl status 未知单元、wssh 连接失败）；
    README 命令表/两个说明节/依赖/版本文档同步更新。
- **2026-09-14 · 新增 `mongodb`/`psql`/`sqlite` 客户端与 `build.ps1 -Version`**：
  - **`mongodb`**：纯 Go 客户端（**无需 mongosh/mongo**），直接 wire
    协议——手写 BSON 编解码、OP_MSG、SCRAM-SHA-256（含服务端签名校验）。
    选项 `-h --port -u --password --authenticationDatabase -d --eval
    --tls/--tlsInsecure --timeout`；REPL 支持 `use/show dbs/show
    collections`、`db.<col>.<method>({...})` 简式（CRUD/aggregate/
    countDocuments/distinct/…，支持 `ObjectId()` 与无引号键）与原样 JSON
    命令文档。单测用假 wire 服务器完整验证 SCRAM 双向握手（正确密码
    通过、错误 proof 拒绝）与 find/error/连接失败路径，全绿。
  - **`psql`**：纯 Go PostgreSQL 客户端（**无需 psql/PostgreSQL**），v3
    wire 协议：启动握手、明文/MD5/SCRAM-SHA-256 认证（复用 mongodb 的
    SCRAM 实现，MD5 用标准库）、简单查询协议多结果集。选项
    `-h --port -U -d -W -w -c -f -t -A -q --ssl --timeout`，密码取
    PGPASSWORD 或 `-W` 隐藏输入；psql 风格对齐表格；REPL 支持
    `\q \l \dt \d NAME \c DB \?`。单测假 wire 服务器覆盖三种认证、多
    结果集、错误路径，全绿。
  - **`sqlite`**：客户端内嵌**纯 Go 驱动 modernc.org/sqlite**（无 CGO），
    因此**无需安装 sqlite3 CLI**——这是 winux 首个（也是唯一）第三方 Go
    模块，`go.mod` 的 go 指令随驱动要求升到 1.25（构建机需 Go 1.25+，
    本地 1.25.5 已验证；二进制约 8.1MB → 17.8MB）。用法
    `sqlite [OPTIONS] [DBFILE] [SQL]`（缺省 `:memory:`），选项
    `-r/--readonly --json --line --list --separator --nullvalue --cmd -v`，
    REPL 点命令 `.tables/.schema/.databases/.indexes/.dump/.mode/
    .headers/.read`。单测用真实引擎覆盖建表/插入/查询、各输出模式、
    只读写失败、NULL 渲染、错误路径，全绿。
  - **`build.ps1 -Version`**：`-Version x.y.z` 注入正式发布版本，
    `-Version dev` 注入 git commit id（开发/测试版，无仓库回退 1.0.0），
    缺省为稳定 `1.0.0`——commit id 只作为开发版本号，不再默认嵌入。
    构建完成行显示 `version <ver> [release|dev (git commit)]`。
  - 命令表 60→63；smoke 新增 6 项（help 循环 +3、sqlite 真实执行、
    mongodb/psql 连接失败）；README 命令表/三个说明节/依赖说明/目录结构
    同步更新。

- **2026-09-14 · 构建注入 git commit id 与构建时间**：`Version` 由常量改为
  包变量，新增 `BuildTime`；`build.ps1` 构建时经
  `-ldflags "-X winux/core.Version=… -X winux/core.BuildTime=…"` 注入
  **HEAD 后 8 位 commit id**（`git rev-parse --short=8 HEAD`）与构建时间
  （无空格的 ISO `yyyy-MM-ddTHH:mm:ss`，避免 PowerShell 5.1 对含引号
  ldflags 参数的破坏）；机器上**没有 git 或不在 git 仓库**时版本回退
  `1.0.0`（先 `Get-Command git` 预检，避免 ErrorActionPreference=Stop 下
  直接终止）。`winux --version`/`version`/`-V`、各命令 `--version`、全局
  `--help` 首行均显示 `(built <时间>)`；`uname -v` 输出 `winux <commit>`；
  直接 `go build`（不经 build.ps1）不注入，保持默认 `1.0.0` 且不显示
  build time。smoke 新增版本格式断言（`winux <8位hex|1.0.0> (built …)`）。

- **2026-09-14 · 新增 `uptime`、`clickhouse`，`ipconfig` 改名 `ifconfig`**：
  - **`ipconfig` → `ifconfig`**（Linux 风格名，`ipconfig` 保留为别名，两个
    垫片都在）：命令行为不变（默认直通系统 `ipconfig.exe`，改网参数拒绝）。
  - **`uptime`**：GNU 格式 `HH:MM:SS up N days, H:MM, N user(s), load
    average: N/A`——时长来自 `GetTickCount64`，用户数来自终端服务会话枚举
    （WTS，活跃 RDP/控制台会话，排除 services 会话 0）；`-s` 打印启动时间、
    `-p` 友好格式。Windows 没有 1/5/15 分钟负载均值，诚实显示 N/A 并在
    stderr 说明，不伪造数字。`uptime_other.go` 保证 linux/darwin 交叉编译。
  - **`clickhouse`**：纯 Go 客户端走 **HTTP 接口**（默认 `localhost:8123`，
    `--secure` 切 HTTPS/8443），无需安装 clickhouse-cli。选项
    `-h/--host -u/--user --password -d/--database -q/--query -e/--execute
    -f/--format -m/--multiline --port --timeout --secure`；`X-ClickHouse-User/
    Key` 头认证 + `database`/`default_format` 参数；`-q` 可重复、多条按语句
    切分（引号感知，复用 mysql 的 splitExecStatements）；stdin 管道批量；
    `clickhouse client` 交互 REPL（默认一行一条查询，`-m` 累积到 `;`）。
    输出格式默认终端 PrettyCompact、管道 TabSeparated，`-f` 覆盖。单测
    `test/clickhouse_test.go` 用 httptest 假服务器覆盖请求形状（方法/头/
    参数/切分）、错误码 60 透传、连接失败与参数校验，全绿。
  - build.ps1 命令表 57→60，smoke 新增 uptime（3）、ifconfig 别名、
    clickhouse 连接失败、help 循环 +3 共 8 项断言；README 命令表、clickhouse
    说明节、目录结构同步更新。

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
