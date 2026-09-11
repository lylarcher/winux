<#
.SYNOPSIS
    Builds winux.exe and generates the per-command shims used by cmd.exe and PowerShell.

.DESCRIPTION
    Produces:
      bin\winux.exe        the single native binary that implements every command
      bin\<command>.cmd    one shim per command so that `cp -r a b` works in both shells
      bin\activate.cmd     cmd.exe session setup (PATH + doskey macros for cmd's built-ins)
      bin\activate.ps1     PowerShell session setup (PATH + removal of the shadowing aliases)

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\build.ps1
    powershell -ExecutionPolicy Bypass -File .\build.ps1 -CopyInsteadOfHardLinks
#>
[CmdletBinding()]
param(
    # Create real cp.exe/rm.exe/... copies (or hard links) instead of .cmd shims.
    [switch]$RealExes,
    [switch]$CopyInsteadOfHardLinks,
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$bin = Join-Path $root 'bin'
$null = New-Item -ItemType Directory -Force -Path $bin

$commands = @(
    'cal', 'cat', 'cd', 'chgrp', 'chmod', 'chown', 'clear', 'cp', 'cut', 'date', 'du', 'echo',
    'grep', 'groupadd', 'groupdel', 'head', 'hostname', 'id', 'ip', 'ipconfig', 'less', 'ln',
    'ls', 'mkdir', 'more', 'mv', 'netstat', 'nmcli', 'nmtui', 'ps', 'pwd', 'rm', 'sort', 'ss',
    'su', 'sudo', 'tail', 'touch', 'tr', 'uname', 'uniq', 'useradd', 'userdel', 'usermod',
    'vi', 'vim', 'wc', 'which', 'whoami'
)

# cmd.exe internal commands cannot be shadowed by an executable on PATH, so the
# session setup installs doskey macros for them instead.
$cmdBuiltins = @('date', 'echo', 'mkdir')

$env:GOTELEMETRY = 'off'

if (-not $SkipTests) {
    Write-Host 'Running unit tests ...' -ForegroundColor Cyan
    Push-Location $root
    try {
        & go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
    } finally { Pop-Location }
}

Write-Host 'Building bin\winux.exe ...' -ForegroundColor Cyan
Push-Location $root
try {
    & go build -trimpath -ldflags '-s -w' -o (Join-Path $bin 'winux.exe') .
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally { Pop-Location }

$shimTemplate = @'
@echo off
setlocal
"%~dp0winux.exe" __COMMAND__ %*
exit /b %ERRORLEVEL%
'@

$wrapperTemplate = @'
@echo off
setlocal
"%~dp0winux.exe" %*
exit /b %ERRORLEVEL%
'@

Write-Host 'Generating command shims ...' -ForegroundColor Cyan
foreach ($c in $commands) {
    $shim = Join-Path $bin "$c.cmd"
    $body = $shimTemplate.Replace('__COMMAND__', $c).Replace("`r`n", "`n")
    Set-Content -Path $shim -Value $body -Encoding ascii -NoNewline
    Add-Content -Path $shim -Value "`n" -Encoding ascii -NoNewline
}
Set-Content -Path (Join-Path $bin 'winux.cmd') -Value $wrapperTemplate -Encoding ascii

if ($RealExes) {
    Write-Host 'Creating per-command executables ...' -ForegroundColor Cyan
    $exe = Join-Path $bin 'winux.exe'
    foreach ($c in $commands) {
        $target = Join-Path $bin "$c.exe"
        if (Test-Path $target) { Remove-Item $target -Force }
        try {
            if ($CopyInsteadOfHardLinks) {
                Copy-Item $exe $target -Force
            } else {
                $null = New-Item -ItemType HardLink -Path $target -Target $exe
            }
        } catch {
            Write-Warning "could not create $c.exe ($($_.Exception.Message)); the .cmd shim still works"
        }
    }
}

# ---------------------------------------------------------------- activate --

$activateCmd = @'
@echo off
rem Activate the winux Linux-style commands in this cmd.exe session.
rem   call C:\path\to\winux\bin\activate.cmd
set "WINUX_BIN=%~dp0"
set "PATH=%WINUX_BIN%;%PATH%"
rem cmd.exe resolves its own internal commands before PATH, so bind those names
rem to winux with doskey macros (interactive sessions only, batch files unaffected).
rem `cd /d` is the one cmd built-in that can cross drives, so `cd D:\some\dir`
rem works directly from C:.
doskey cd=cd /d $*
doskey mkdir=winux.exe mkdir $*
doskey echo=winux.exe echo $*
doskey date=winux.exe date $*
echo winux activated: cp rm mv mkdir touch grep cat cd less vi and more are now available.
echo Type "winux --help" for the full command list.
'@
Set-Content -Path (Join-Path $bin 'activate.cmd') -Value $activateCmd -Encoding ascii

$activatePs1 = @'
# Activate the winux Linux-style commands in this PowerShell session:
#   . C:\path\to\winux\bin\activate.ps1
# Add the same line to $PROFILE to make it permanent.

$winuxBin = $PSScriptRoot
if (-not (Test-Path (Join-Path $winuxBin 'winux.exe'))) {
    throw "winux.exe not found in $winuxBin - run build.ps1 first"
}

# Only names with a shim in our bin\ directory are shadowed, so PowerShell's
# own ls/dir/cls/md/type/copy/... keep working untouched.
$shimNames = @(Get-ChildItem -Path $winuxBin -Filter '*.cmd' | ForEach-Object { $_.BaseName })
foreach ($name in $shimNames) {
    Remove-Item "Alias:$name" -Force -ErrorAction SilentlyContinue
    Remove-Item "Function:$name" -Force -ErrorAction SilentlyContinue
}

if (($env:Path -split ';') -notcontains $winuxBin) {
    $env:Path = "$winuxBin;$env:Path"
}

# An external process cannot change the parent shell's working directory, so
# `cd` is a function: winux resolves/validates the target (cross-drive, ~, -,
# /, both slash styles) and Set-Location performs the actual change.
$global:winuxExe = Join-Path $winuxBin 'winux.exe'
function cd {
    if ($args.Count -eq 0) {
        Get-Location
        return
    }
    if ($args[0] -eq '-') {
        if (-not $global:winuxPrevDir) {
            Write-Host 'cd: OLDPWD not set' -ForegroundColor Red
            return
        }
        $old = (Get-Location).Path
        Set-Location -LiteralPath $global:winuxPrevDir -ErrorAction Stop
        $global:winuxPrevDir = $old
        return
    }
    $out = (& $global:winuxExe cd $args | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { return }   # winux already wrote the error to stderr
    if (-not $out) { return }
    $target = @($out -split "`r?`n" | Where-Object { $_ }) | Select-Object -Last 1
    if (-not $target) { return }
    # winux cd prints one absolute path on success; anything else (e.g. --help)
    # is shown without trying to change the location.
    if ($target -notmatch '^(?:[A-Za-z]:\\|\\)') {
        Write-Host $target
        return
    }
    $old = (Get-Location).Path
    try {
        Set-Location -LiteralPath $target -ErrorAction Stop
    } catch {
        Write-Host $_.Exception.Message -ForegroundColor Red
        return
    }
    $global:winuxPrevDir = $old
    $env:WINUX_PREVDIR = $old
}

Write-Host "winux activated from $winuxBin" -ForegroundColor Green
Write-Host 'cp rm mv mkdir touch grep cat cd less vi and more now resolve to winux.' -ForegroundColor Green
Write-Host 'Run "winux --help" for the whole command list.' -ForegroundColor Green
'@
Set-Content -Path (Join-Path $bin 'activate.ps1') -Value $activatePs1 -Encoding utf8

$exeSize = (Get-Item (Join-Path $bin 'winux.exe')).Length
Write-Host ''
Write-Host ("Build complete: {0} commands, winux.exe {1:N0} bytes" -f $commands.Count, $exeSize) -ForegroundColor Green
Write-Host ''
Write-Host 'Enable the commands in a shell:' -ForegroundColor Yellow
Write-Host ("  cmd.exe     :  call `"{0}\activate.cmd`"" -f $bin)
Write-Host ("  PowerShell  :  . `"{0}\activate.ps1`"" -f $bin)
Write-Host ''
Write-Host 'They can also be used directly without activation:' -ForegroundColor Yellow
Write-Host ("  {0}\cp.cmd -r src dst" -f $bin)
Write-Host ("  winux cp -r src dst          (after adding {0} to PATH)" -f $bin)
