<#
.SYNOPSIS
    Installs winux: adds bin\ to the persistent user PATH and optionally hooks
    the PowerShell profile.

.DESCRIPTION
    By default it only records the PATH and adds the PowerShell profile hook so
    every new PowerShell window has the commands available. cmd.exe users either
    run `call bin\cmd\winux-activate.cmd` or use `winux activate` from any
    shell (once bin\ is on PATH, `winux` itself is callable everywhere).

    -Uninstall removes the recorded entries (it never deletes files).

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\install.ps1
    powershell -ExecutionPolicy Bypass -File .\install.ps1 -NoProfile
    powershell -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [switch]$NoProfile,   # do not touch the PowerShell profile
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$bin = Join-Path $root 'bin'
if (-not (Test-Path (Join-Path $bin 'winux.exe'))) {
    throw "winux.exe not found in $bin - run build.ps1 first"
}
# The .cmd shims / .ps1 helpers are embedded in winux.exe; materialize them so
# the profile hook and direct use work even if bin\ was copied elsewhere.
& (Join-Path $bin 'winux.exe') shims | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'winux shims failed' }

$profileLine = ". '$bin\winux-activate.ps1'"
$pathEntry = $bin

if ($Uninstall) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $newPath = (($userPath -split ';' | Where-Object { $_ -and -not [string]::Equals($_, $pathEntry, 'OrdinalIgnoreCase') }) -join ';')
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')

    if (-not $NoProfile -and $PROFILE -and (Test-Path $PROFILE)) {
        $content = Get-Content -Raw $PROFILE
        if ($content) {
            $content = ($content -split "`r?`n" | Where-Object {
                $_ -and $_.Trim() -ne $profileLine
            }) -join "`r`n"
            Set-Content -Path $PROFILE -Value $content -Encoding utf8
        }
    }
    Write-Host 'winux uninstalled from PATH and profile (files left in place).' -ForegroundColor Green
    exit 0
}

# --- PATH ------------------------------------------------------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $pathEntry) {
    $newPath = $pathEntry + ';' + $userPath
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Host ("Added {0} to your user PATH." -f $pathEntry) -ForegroundColor Green
} else {
    Write-Host ("{0} is already on your user PATH." -f $pathEntry) -ForegroundColor Yellow
}
# Make it visible in this session too.
$env:Path = "$pathEntry;$env:Path"

# --- PowerShell profile ------------------------------------------------------
if (-not $NoProfile) {
    if (-not $PROFILE) {
        Write-Warning 'No PowerShell profile path is available; skipping the profile hook.'
    } else {
        if (-not (Test-Path $PROFILE)) {
            $null = New-Item -ItemType File -Force -Path $PROFILE
        }
        $content = Get-Content -Raw $PROFILE
        if ($content -and $content.Trim() -ne '') {
            $lines = $content -split "`r?`n"
        } else {
            $lines = @()
        }
        if ($lines | Where-Object { $_.Trim() -eq $profileLine }) {
            Write-Host 'The PowerShell profile hook is already present.' -ForegroundColor Yellow
        } else {
            $lines += ''
            $lines += '# winux: Linux-style commands for Windows'
            $lines += $profileLine
            Set-Content -Path $PROFILE -Value ($lines -join "`r`n") -Encoding utf8
            Write-Host ("Added the activation hook to your PowerShell profile: {0}" -f $PROFILE) -ForegroundColor Green
        }
    }
}

Write-Host ''
Write-Host 'Done. Open a NEW PowerShell or cmd.exe window:' -ForegroundColor Yellow
Write-Host '  PowerShell:  cp -r src dst;  grep -rin todo .;  vi file.txt'
Write-Host '  cmd.exe    :  mkdir -p a\b  (via doskey macros);  or call bin\cmd\winux-activate.cmd'
Write-Host ''
Write-Host 'Activation is also available on demand (winux.exe on PATH):' -ForegroundColor Yellow
Write-Host ('  winux activate | iex                    (PowerShell)')
Write-Host ('  for /f "delims=" %l in (''winux activate'') do @%l   (cmd.exe)')
Write-Host ("  . {0}\winux-activate.ps1                     (PowerShell, direct)" -f $bin)
Write-Host ("  call {0}\cmd\winux-activate.cmd               (cmd.exe, direct)" -f $bin)
