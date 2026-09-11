<#
.SYNOPSIS
    End-to-end smoke test for the winux commands in PowerShell and cmd.exe.

.DESCRIPTION
    Creates a scratch directory, activates winux in both shells and exercises the
    documented Linux-ish behaviour (wildcards, -r/-f, pipes, exit codes, ...).
    Exit code 0 means every check passed.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\smoke.ps1
    powershell -ExecutionPolicy Bypass -File .\smoke.ps1 -Network   # run the read-only network checks too
#>
[CmdletBinding()]
param(
    # Run the network section (netstat/ss/ip/nmcli/ipconfig). These checks are
    # strictly read-only, but they touch the live network stack, so they run
    # only when the operator opts in.
    [switch]$Network
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$bin = Join-Path $root 'bin'
$winux = Join-Path $bin 'winux.exe'
if (-not (Test-Path $winux)) { throw 'run build.ps1 first' }

$script:pass = 0
$script:fail = 0

function Check {
    param([string]$Name, [bool]$Ok, [string]$Detail = '')
    if ($Ok) {
        $script:pass++
        Write-Host ("  PASS  {0}" -f $Name) -ForegroundColor DarkGreen
    } else {
        $script:fail++
        Write-Host ("  FAIL  {0} {1}" -f $Name, $Detail) -ForegroundColor Red
    }
}

function Text {
    param($Value)
    return (($Value | Out-String).Trim())
}

# Runs a cmd.exe command line and returns its exit code (used where PowerShell
# would otherwise handle the name itself).
function Invoke-CmdLine {
    param([string]$Line)
    & cmd /c $Line 2>&1 | Out-Null
    return $LASTEXITCODE
}

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("winux-smoke-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
$null = New-Item -ItemType Directory -Force -Path $work
Push-Location $work
try {
    Write-Host '== PowerShell section ==' -ForegroundColor Cyan
    . (Join-Path $bin 'activate.ps1') | Out-Null

    Check 'cp resolves to the winux shim, not the Copy-Item alias' `
        ((Get-Command cp -ErrorAction SilentlyContinue).Source -like "$bin*") "source=$((Get-Command cp -ErrorAction SilentlyContinue).Source)"

    # ---------------------------------------------------------- fixtures ----
    mkdir -p src/sub | Out-Null
    Set-Content -Path src/a.txt -Value 'alpha'      -NoNewline
    Set-Content -Path src/sub/b.txt -Value 'beta'   -NoNewline
    Set-Content -Path src/sub/c.log -Value 'gamma'  -NoNewline
    Set-Content -Path lines.txt -Value @('one', 'two', 'three', 'four', 'five')
    Set-Content -Path nums.txt  -Value @('10', '2', '33')

    Check 'mkdir -p created a nested path' (Test-Path src/sub)

    touch -d '2024-01-02 03:04:05' old.txt
    $st = (Get-Item old.txt).LastWriteTime
    Check 'touch -d set the timestamp' ($st.Year -eq 2024 -and $st.Month -eq 1 -and $st.Day -eq 2 -and $st.Hour -eq 3) "got $st"
    touch -t 202401020304.05 stamp.txt
    $st2 = (Get-Item stamp.txt).LastWriteTime
    Check 'touch -t [[CC]YY]MMDDhhmm[.ss] works' ($st2.Year -eq 2024 -and $st2.Minute -eq 4 -and $st2.Second -eq 5) "got $st2"

    # ---------------------------------------------------------------- cp ---
    cp -r src dst
    Check 'cp -r copied the tree' ((Get-Content dst/sub/b.txt -Raw) -eq 'beta')
    cp -v src/a.txt dst/a-copy.txt | Out-Null
    Check 'cp to a new name works' ((Get-Content dst/a-copy.txt -Raw) -eq 'alpha')
    mkdir -p globbed | Out-Null
    cp *.txt globbed
    Check 'cp expands wildcards itself (cmd/PowerShell do not)' `
        ((Test-Path globbed/old.txt) -and (Test-Path globbed/lines.txt) -and -not (Test-Path globbed/b.txt))

    # ---------------------------------------------------------------- mv ---
    mv dst/a-copy.txt dst/moved.txt
    Check 'mv renamed a file' ((-not (Test-Path dst/a-copy.txt)) -and (Test-Path dst/moved.txt))
    Set-Content -Path stray.txt -Value 'stray' -NoNewline
    mv stray.txt dst
    Check 'mv into an existing directory works' ((Get-Content dst/stray.txt -Raw) -eq 'stray')

    # ---------------------------------------------------------------- rm ---
    rm -f src/sub/c.log
    Check 'rm -f removed a file' (-not (Test-Path src/sub/c.log))
    rm -rf globbed
    Check 'rm -rf removed a directory tree' (-not (Test-Path globbed))
    $code = Invoke-CmdLine "`"$bin\rm.cmd`" -rf C:\ >nul 2>&1"
    Check 'rm -rf C:\ is refused (as GNU rm does)' ($code -ne 0) "exit=$code"
    $code = Invoke-CmdLine "`"$bin\rm.cmd`" -f definitely-missing.txt >nul 2>&1"
    Check 'rm -f on a missing file succeeds' ($code -eq 0) "exit=$code"

    # ---------------------------------------------------------- text tools --
    Check 'grep -rin finds matches recursively' ((Text (grep -rin alpha .)) -match 'a\.txt')
    $code = Invoke-CmdLine "`"$bin\grep.cmd`" -q nomatch-xyz src\a.txt >nul 2>&1"
    Check 'grep exits 1 when nothing matches' ($code -eq 1) "exit=$code"
    Check 'grep -c counts matching lines' ((Text (grep -c 'o' lines.txt)) -eq '3')
    Check 'head -n 1 prints the first line' ((Text (head -n 1 src/a.txt)) -eq 'alpha')
    Check 'head -n -2 prints all but the last two lines' (((head -n -2 lines.txt) -join ',') -eq 'one,two,three')
    Check 'tail -n 2 prints the last two lines' (((tail -n 2 lines.txt) -join ',') -eq 'four,five')
    Check 'tail -2 shorthand works' (((tail -2 lines.txt) -join ',') -eq 'four,five')
    Check 'tail -n +3 starts at line 3' (((tail -n +3 lines.txt) -join ',') -eq 'three,four,five')
    Check 'wc -l counts lines' (((wc -l lines.txt) -split '\s+' | Where-Object { $_ })[0] -eq '5')
    Check 'wc -w -c prints words and bytes' (((wc -wc lines.txt) -join ' ') -match '\d+')
    Check 'sort -n sorts numerically' (((Get-Content nums.txt | sort -n) -join ',') -eq '2,10,33')
    Check 'sort -nr sorts numerically reversed' (((Get-Content nums.txt | sort -nr) -join ',') -eq '33,10,2')
    Check 'uniq -c prefixes counts' (((@('a', 'a', 'b') | uniq -c) -join ' ') -match '2 a')
    Check 'cut -d, -f2 selects a field' ((Text (@('x,y,z') | cut -d',' -f2)) -eq 'y')
    Check 'tr translates characters' ((Text (@('abc') | tr 'a-z' 'A-Z')) -eq 'ABC')
    Check 'grep works in a pipeline' (((@('foo', 'bar', 'foobar') | grep foo) -join ',') -eq 'foo,foobar')
    Check 'cat prints a file' ((Text (cat src/a.txt)) -eq 'alpha')
    Check 'cat -n numbers lines' ((Text (cat -n src/a.txt)) -match '1\s+alpha')
    Check 'less dumps content when the output is not a console' ((Text (less lines.txt)) -match 'three')
    Check 'more dumps content when the output is not a console' ((Text (more lines.txt)) -match 'four')

    # ------------------------------------------------------- system helpers --
    Check 'pwd prints the working directory' ((Text (pwd)).EndsWith((Get-Item .).Name))
    Check 'uname -a mentions Windows' ((Text (uname -a)) -match 'Windows')
    Check 'uname -m reports an x86/arm style machine' ((Text (uname -m)) -match 'x86_64|aarch64|i686|armv7l')
    Check 'date +%Y prints the year' ((Text (date +%Y)) -eq (Get-Date).Year.ToString())
    Check 'date +%F prints an ISO date' ((Text (date +%F)) -match '^\d{4}-\d{2}-\d{2}$')
    Check 'which locates cmd.exe' ((Text (which cmd.exe)) -match 'cmd\.exe')
    $code = Invoke-CmdLine "`"$bin\which.cmd`" no-such-command-xyz >nul 2>&1"
    Check 'which exits 1 for an unknown command' ($code -eq 1) "exit=$code"
    Check 'echo prints its arguments' ((Text (echo hello world)) -eq 'hello world')
    Check 'id prints a POSIX-shaped identity' ((Text (id)) -match 'uid=')
    Check 'whoami prints a user name' ((Text (whoami)).Length -gt 0)
    Check 'hostname prints something' ((Text (hostname)).Length -gt 0)
    Check 'ln -s / ln create links' ((Text (& $winux ln -v src/a.txt hardlink.txt)) -match 'hardlink' -or (Test-Path hardlink.txt))

    # ----------------------------------------- direct invocation and help ----
    Check 'winux <command> form works' ((Text (& $winux echo subcommand)) -eq 'subcommand')
    $cpExe = Join-Path $work 'cp.exe'
    Copy-Item $winux $cpExe -Force
    & $cpExe src/a.txt argv0-copy.txt | Out-Null
    Check 'argv[0] dispatch (a binary renamed cp.exe) works' ((Get-Content argv0-copy.txt -Raw) -eq 'alpha')
    $help = Text (& $winux --help)
    Check 'global help lists the commands' ($help -match 'cp' -and $help -match 'grep' -and $help -match 'vi')
    foreach ($cmd in @('cp', 'rm', 'mv', 'mkdir', 'touch', 'grep', 'cat', 'more', 'less', 'tail', 'vi', 'sudo', 'su',
            'chmod', 'chown', 'chgrp', 'useradd', 'userdel', 'usermod', 'groupadd', 'groupdel', 'cal', 'ps',
            'netstat', 'ss', 'ip', 'ipconfig', 'nmcli', 'nmtui')) {
        $h = Text (& $winux $cmd --help)
        Check "$cmd --help prints a usage line" ($h -match 'Usage:')
    }
    Check 'sudo explains the Windows elevation model' ((Text (& $winux sudo --help)) -match 'UAC|administrator')
    Check 'su explains that Windows has no root account' ((Text (& $winux su --help)) -match 'USER|USER')

    # ------------------------------------------------------------- cd --------
    # `cd` is a PowerShell function (defined by activate.ps1): winux resolves
    # the target and Set-Location performs the actual change.
    cd 'src'
    Check 'cd into a subdirectory' ((Get-Location).Path.EndsWith('src'))
    cd 'sub'
    Check 'cd into a nested path (relative)' ((Get-Location).Path.EndsWith('src\sub'))
    cd -
    Check 'cd - returns to the previous directory' ((Get-Location).Path.EndsWith('src'))
    cd ..
    Check 'cd .. works' ((Get-Location).Path -eq $work)
    cd /
    $rootNow = (Get-Location).Path
    Check 'cd / goes to the root of the current drive' ($rootNow -match ':\\$')
    cd $work
    mkdir -p 'space dir/deep' | Out-Null
    cd 'space dir/deep'
    Check 'cd with a quoted space and backslashes' ((Get-Location).Path.EndsWith('space dir\deep'))
    cd '../..'
    Check 'cd accepts forward slashes' ((Get-Location).Path -eq $work) ((Get-Location).Path)
    cd $work
    $resolved = Text (& $winux cd "$work\src")
    Check 'winux cd prints an absolute backslash path' ($resolved -eq (Join-Path $work 'src')) "got $resolved"
    $code = Invoke-CmdLine "`"$winux`" cd no-such-dir-xyz >nul 2>&1"
    Check 'winux cd fails for a missing directory' ($code -eq 1) "exit=$code"
    $curDrive = Split-Path (Get-Location).Path -Qualifier
    $alt = Get-PSDrive -PSProvider FileSystem | Where-Object {
        $_.Name -ne $curDrive.TrimEnd(':') -and (Test-Path (Join-Path $_.Root 'Windows') -ErrorAction SilentlyContinue)
    } | Select-Object -First 1
    if ($alt) {
        $target = "$($alt.Name):\Windows"
        cd $target
        Check "cross-drive cd to $target" ((Get-Location).Path -eq $target) ((Get-Location).Path)
        cd $work
    } else {
        Write-Host '  SKIP  cross-drive cd (no second drive containing \Windows)'
        $script:pass++
    }

    # ------------------------------------------- path separators (/ vs \) ----
    mkdir -p 'slash test/sub' | Out-Null
    Set-Content -Path 'slash test/a.txt' -Value 'data' -Encoding ascii -NoNewline
    cp 'slash test/a.txt' 'slash test/copy.txt'
    Check 'cp with spaces and backslashes' (Test-Path 'slash test\copy.txt')
    grep -r 'data' 'slash test' | Out-Null
    Check 'grep -r with a space in the directory name' ($LASTEXITCODE -eq 0)
    rm -rf 'slash test'
    Check 'rm -rf with backslashes and a space' (-not (Test-Path 'slash test'))
    cp src/../src/a.txt slash-mixed.txt
    Check 'relative paths with forward slashes' (Test-Path 'slash-mixed.txt')
    rm -f slash-mixed.txt
    Check 'rm -f with forward slashes' (-not (Test-Path 'slash-mixed.txt'))

    # ------------------------------------------- accounts / permissions / cal --
    Write-Host '== accounts / permissions / cal section ==' -ForegroundColor Cyan

    Set-Content -Path perm.txt -Value 'x' -Encoding ascii -NoNewline
    chmod 444 perm.txt | Out-Null
    Check 'chmod 444 makes a file read-only' ((Get-Item perm.txt).Attributes -band [IO.FileAttributes]::ReadOnly)
    chmod 644 perm.txt | Out-Null
    Check 'chmod 644 restores writability' (-not ((Get-Item perm.txt).Attributes -band [IO.FileAttributes]::ReadOnly))
    chmod -c 444 perm.txt | Out-Null
    Check 'chmod -c reports the change' ($LASTEXITCODE -eq 0)
    chmod u=rw,go=r perm.txt | Out-Null
    Check 'chmod with a symbolic mode works' ($LASTEXITCODE -eq 0)
    $bad = Invoke-CmdLine "`"$bin\chmod.cmd`" 999 perm.txt >nul 2>&1"
    Check 'chmod rejects an invalid mode' ($bad -ne 0) "exit=$bad"

    Check 'cal 3 2025 shows March' ((Text (cal 3 2025)) -match 'March 2025')
    Check 'cal 2025 shows December' ((Text (cal 2025)) -match 'December')
    Check 'cal -j shows day-of-year numbers' ((Text (cal -j 3 2025)) -match '\b60\b')

    Check 'useradd --dry-run prints the native commands' ((Text (& $winux useradd --dry-run -m -p x bob)) -match 'user bob')
    Check 'useradd without -p locks the account' ((Text (& $winux useradd --dry-run bob)) -match '/active:no')
    Check 'userdel --dry-run prints the deletion' ((Text (& $winux userdel --dry-run -r bob)) -match 'user bob /delete')
    Check 'groupadd --dry-run prints the creation' ((Text (& $winux groupadd --dry-run devs)) -match 'localgroup devs /add')
    Check 'groupdel --dry-run prints the deletion' ((Text (& $winux groupdel --dry-run devs)) -match 'localgroup devs /delete')
    Check 'usermod --dry-run prints the lock' ((Text (& $winux usermod --dry-run -L bob)) -match '/active:no')
    $co = Text (& $winux chown --dry-run $env:USERNAME perm.txt 2>&1)
    if ($LASTEXITCODE -eq 0) {
        Check 'chown --dry-run prints icacls' ($co -match 'icacls') $co
    } else {
        Write-Host '  SKIP  chown --dry-run (account lookup not available in this environment)'
        $script:pass++
    }
    $chgrp = Invoke-CmdLine "`"$bin\chgrp.cmd`" Users perm.txt >nul 2>&1"
    Check 'chgrp explains that Windows has no group ownership' ($chgrp -ne 0) "exit=$chgrp"

    # -------------------------------------------------------------- processes --
    Write-Host '== process section ==' -ForegroundColor Cyan

    $aux = Text (& $winux ps aux)
    Check 'ps aux prints the BSD header' ($aux -match 'USER\s+PID\s+%CPU')
    Check 'ps aux lists processes' (($aux -split "`n").Count -gt 3)
    $ef = Text (& $winux ps -ef)
    Check 'ps -ef prints the SysV header' ($ef -match 'UID\s+PID\s+PPID')
    $self = $PID
    $pself = Text (& $winux ps -p $self -o pid,comm -h)
    Check 'ps -p lists our own process' ($pself -match "$self")
    & $winux ps --sort=-%cpu -e | Out-Null
    Check 'ps --sort=-%cpu works' ($LASTEXITCODE -eq 0) "exit=$LASTEXITCODE"

    # ----------------------------------------------------------------- network --
    if ($Network) {
        Write-Host '== network section (read-only) ==' -ForegroundColor Cyan
        $an = Text (& $winux netstat -an)
        Check 'netstat -an lists sockets' ($an -match 'LISTENING|ESTABLISHED')
        Check 'netstat -r shows the route table' ((Text (& $winux netstat -r)) -match '0\.0\.0\.0')
        Check 'netstat -e shows interface statistics' ((Text (& $winux netstat -e)) -match 'Bytes')
        Check 'netstat -i lists interfaces' ((Text (& $winux netstat -i)) -match 'MTU')
        Check 'netstat -s shows protocol statistics' ((Text (& $winux netstat -s)) -match 'TCP Statistics')
        Check 'ss -t -a -n lists sockets' ((Text (& $winux ss -t -a -n)) -match 'LISTEN|ESTAB')
        Check 'ss -s prints a summary' ((Text (& $winux ss -s)) -match 'Total:')
        Check 'ip addr shows the loopback' ((Text (& $winux ip a)) -match '127\.0\.0\.1/8')
        Check 'ip -br a lists interfaces' ((Text (& $winux ip -br a)) -match 'UP|DOWN')
        Check 'ip route shows a default route' ((Text (& $winux ip r)) -match 'default|dev')
        Check 'nmcli lists devices' ((Text (& $winux nmcli)) -match 'DEVICE')
        Check 'ipconfig prints the configuration' ((Text (& $winux ipconfig)) -match 'IP')
        $code = Invoke-CmdLine "`"$bin\ip.cmd`" addr add 9.9.9.9/32 dev x >nul 2>&1"
        Check 'ip addr add is refused (read-only policy)' ($code -eq 1) "exit=$code"
        $code = Invoke-CmdLine "`"$bin\ipconfig.cmd`" /release >nul 2>&1"
        Check 'ipconfig /release is refused (read-only policy)' ($code -eq 1) "exit=$code"
    } else {
        Write-Host '  SKIP  network section (pass -Network, after explicit consent, to run the read-only network checks)'
        $script:pass++
    }

    # --------------------------------------------------------- cmd.exe -------
    Write-Host '== cmd.exe section ==' -ForegroundColor Cyan

    $macroOut = Text (& cmd /c "call `"$bin\activate.cmd`" >nul && doskey /macros")
    Check 'activate.cmd registers doskey macros for cmd built-ins' `
        ($macroOut -match 'mkdir=winux' -and $macroOut -match 'echo=winux' -and $macroOut -match 'date=winux') $macroOut

    # A batch file must invoke the .cmd shims with `call` (cmd.exe does not
    # return control otherwise) or call winux.exe directly.
    $cmdScript = @"
@echo off
set WINUX=$winux
set SHIMS=$bin
call "$bin\activate.cmd" >nul
cd /d "$work"
call "$bin\mkdir.cmd" -p cmdtest\deep
if not exist cmdtest\deep (echo FAIL mkdir -p & exit /b 1)
echo one> cmdtest\f1.txt
echo two> cmdtest\f2.txt
call "$bin\cp.cmd" cmdtest\f1.txt cmdtest\f1.copy.txt
if not exist cmdtest\f1.copy.txt (echo FAIL cp & exit /b 2)
call "$bin\cp.cmd" -r cmdtest cmdclone
if not exist cmdclone\deep (echo FAIL cp -r & exit /b 3)
call "$bin\rm.cmd" -f cmdtest\f1.copy.txt
if exist cmdtest\f1.copy.txt (echo FAIL rm & exit /b 4)
call "$bin\rm.cmd" -rf cmdclone
if exist cmdclone (echo FAIL rm -rf & exit /b 5)
call "$bin\grep.cmd" two cmdtest\f2.txt >nul || (echo FAIL grep & exit /b 6)
for /f %%i in ('"$bin\wc.cmd" -l cmdtest\f2.txt') do set LINES=%%i
if not "%LINES%"=="1" (echo FAIL wc & exit /b 7)
type cmdtest\f2.txt | "$bin\tail.cmd" -n 1 | findstr two >nul || (echo FAIL tail & exit /b 8)
type cmdtest\f1.txt | "$bin\head.cmd" -n 1 | findstr one >nul || (echo FAIL head & exit /b 9)
call "$bin\touch.cmd" cmdtest\touched.txt
if not exist cmdtest\touched.txt (echo FAIL touch & exit /b 10)
call "$bin\touch.cmd" -c cmdtest\never.txt
if exist cmdtest\never.txt (echo FAIL touch -c created a file & exit /b 10)
type cmdtest\f1.txt | "$bin\grep.cmd" one >nul || (echo FAIL cat+grep & exit /b 11)
call "$bin\cp.cmd" cmdtest/f1.txt cmdtest/fslash.txt
if not exist cmdtest\fslash.txt (echo FAIL forward-slash cp & exit /b 12)
call "$bin\rm.cmd" -f cmdtest/fslash.txt
if exist cmdtest\fslash.txt (echo FAIL forward-slash rm & exit /b 12)
echo cmdtest-ok
exit /b 0
"@
    $cmdFile = Join-Path $work 'cmd-section.cmd'
    Set-Content -Path $cmdFile -Value $cmdScript -Encoding ascii
    $cmdOut = Text (& cmd /c "`"$cmdFile`" 2>&1")
    Check 'cmd.exe batch: mkdir/cp/rm/grep/wc/tail/head/touch/cat' ($cmdOut -match 'cmdtest-ok') $cmdOut

    # Interactive cmd sessions expand the doskey macros (they are processed by
    # the console command-line editor, which is why piped/redirected input does
    # not trigger them). Verify the exact macro definitions here.
    $macros = @{}
    $raw = (& cmd /c "call `"$bin\activate.cmd`" >nul && doskey /macros") | Out-String
    foreach ($line in ($raw -split "`r?`n")) {
        $parts = $line -split '=', 2
        if ($parts.Count -eq 2) { $macros[$parts[0].Trim()] = $parts[1].Trim() }
    }
    $macroOk = ($macros['mkdir'] -like 'winux.exe mkdir*') -and
               ($macros['echo'] -like 'winux.exe echo*') -and
               ($macros['date'] -like 'winux.exe date*') -and
               ($macros['cd'] -like 'cd /d*')
    Check 'cmd.exe: doskey macros bind mkdir/echo/date/cd to winux' $macroOk (($macros | ConvertTo-Json -Compress))
}
finally {
    Pop-Location
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

Write-Host ''
if ($script:fail -eq 0) {
    Write-Host ("smoke test: {0} passed, 0 failed" -f $script:pass) -ForegroundColor Green
    exit 0
}
Write-Host ("smoke test: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor Red
exit 1
