<#
.SYNOPSIS
    Builds winux.exe. The per-command shims and the activation helpers are
    EMBEDDED in the binary and materialized on demand (md5-compare, so only
    changed files are rewritten).

.DESCRIPTION
    Produces:
      bin\winux.exe        the single native binary that implements every command
                           and carries the .cmd/.ps1 helpers inside itself
      bin\cmd\<command>.cmd   materialized by `winux shims` (also done
      bin\winux-activate.ps1  automatically by `winux activate`) — one shim
                           per command so that `cp -r a b` works in both shells

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\build.ps1
    powershell -ExecutionPolicy Bypass -File .\build.ps1 -CopyInsteadOfHardLinks
#>
[CmdletBinding()]
param(
    # Version to stamp into core.Version. Default (omitted) = 1.0.0.
    #   -Version 2.1.0   a formal release version (x.y.z)
    #   -Version dev     development/test build: the 8-char git commit id
    #                    (falls back to 1.0.0 without a local git repository)
    [string]$Version,
    # Create real cp.exe/rm.exe/... copies (or hard links) instead of .cmd shims.
    [switch]$RealExes,
    [switch]$CopyInsteadOfHardLinks,
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$bin = Join-Path $root 'bin'
$null = New-Item -ItemType Directory -Force -Path $bin

# Used only for the optional -RealExes per-command executables; the .cmd shims
# themselves are derived from the binary's command table by `winux shims`.
$commands = @(
    'cal', 'cat', 'cd', 'chgrp', 'chmod', 'chown', 'clear', 'cp', 'cut', 'date', 'df', 'du', 'echo',
    'grep', 'groupadd', 'groupdel', 'head', 'hostname', 'id', 'ip', 'ifconfig', 'ipconfig', 'less', 'ln',
    'ls', 'mkdir', 'more', 'mv', 'netstat', 'nmcli', 'nmtui', 'ps', 'pwd', 'rm', 'sh', 'sort', 'ss',
    'su', 'sudo', 'tail', 'touch', 'tr', 'uname', 'uniq', 'uptime', 'useradd', 'userdel', 'usermod',
    'passwd', 'vi', 'vim', 'wc', 'which', 'whoami',
    'mysql', 'redis-cli', 'tcpdump', 'nc', 'netcat', 'clickhouse',
    'mongodb', 'psql', 'sqlite'
)

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

# Stamp the version with the 8-char git commit id (fallback 1.0.0 when there is
# no local git repository) and the build timestamp into core.Version/BuildTime.
# Resolve the version to stamp: a formal release version when -Version is
# given (x.y.z), 'dev' stamps the 8-char git commit id, and the default
# (no -Version) is the stable 1.0.0. The commit id is only ever a development
# build marker.
$commit = $null
if (Get-Command git -ErrorAction SilentlyContinue) {
    $rev = & git rev-parse --short=8 HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $rev) { $commit = $rev.Trim() }
}
$stampVersion = '1.0.0'
if ($Version) {
    if ($Version -ieq 'dev' -or $Version -ieq 'development') {
        $stampVersion = if ($commit) { $commit } else { '1.0.0' }
    } elseif ($Version -match '^\d+\.\d+(\.\d+)*$') {
        $stampVersion = $Version
    } else {
        throw "invalid -Version '$Version' (use a version like 1.0.0, or 'dev' for a git-commit build)"
    }
}
# ISO timestamp with a 'T' separator: no spaces, so the linker's -X value
# needs no quoting (PowerShell 5.1 mangles quoted -ldflags arguments).
$buildTime = Get-Date -Format 'yyyy-MM-ddTHH:mm:ss'
$ldflags = "-s -w -X winux/core.Version=$stampVersion -X winux/core.BuildTime=$buildTime"

Push-Location $root
try {
    & go build -trimpath -ldflags $ldflags -o (Join-Path $bin 'winux.exe') .
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally { Pop-Location }

$shimDir = Join-Path $bin 'cmd'

# Drop stale artifacts from previous layouts (old shims used to sit directly
# in bin\, next to winux.exe; old helpers were bare activate.ps1/deactivate.ps1
# and are now winux- prefixed). Keep winux.exe itself.
Get-ChildItem $bin -Filter '*.cmd' -ErrorAction SilentlyContinue | Remove-Item -Force
Get-ChildItem $bin -Filter '*.exe' -ErrorAction SilentlyContinue | Where-Object { $_.Name -ne 'winux.exe' } | Remove-Item -Force
Get-ChildItem $bin -Filter '*.ps1' -ErrorAction SilentlyContinue | Where-Object { $_.Name -notlike 'winux-*' } | Remove-Item -Force

# Materialize the embedded .cmd shims + activation helpers next to winux.exe
# (md5-compare: only files whose content changed are rewritten). `winux activate`
# does the same automatically; this call just makes bin\ usable right after build.
Write-Host 'Materializing embedded shims ...' -ForegroundColor Cyan
& (Join-Path $bin 'winux.exe') shims
if ($LASTEXITCODE -ne 0) { throw "winux shims failed with exit code $LASTEXITCODE" }

if ($RealExes) {
    Write-Host 'Creating per-command executables ...' -ForegroundColor Cyan
    $exe = Join-Path $bin 'winux.exe'
    foreach ($c in $commands) {
        $target = Join-Path $shimDir "$c.exe"
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


$exeSize = (Get-Item (Join-Path $bin 'winux.exe')).Length
$mode = if ($Version -and $Version -ne $stampVersion) { 'dev (git commit)' } else { 'release' }
Write-Host ''
Write-Host ("Build complete: {0} commands, winux.exe {1:N0} bytes (version {2} [{3}], built {4})" -f $commands.Count, $exeSize, $stampVersion, $mode, $buildTime) -ForegroundColor Green
Write-Host ''
Write-Host 'Enable the commands in a shell (winux.exe on PATH):' -ForegroundColor Yellow
Write-Host ("  cmd.exe     :  for /f `"delims=`" %l in ('winux activate') do @%l   (exit: ... 'winux deactivate' ...)" )
Write-Host ("  PowerShell  :  winux activate | iex   (exit: winux deactivate | iex)")
Write-Host ("  or directly:  . `"{0}\winux-activate.ps1`"   /   call `"{0}\cmd\winux-activate.cmd`"" -f $bin)
Write-Host ''
Write-Host 'They can also be used directly without activation:' -ForegroundColor Yellow
Write-Host ("  {0}\cmd\cp.cmd -r src dst" -f $bin)
Write-Host ("  winux cp -r src dst          (after adding {0} to PATH)" -f $bin)
