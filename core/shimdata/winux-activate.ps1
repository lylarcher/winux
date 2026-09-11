# Activate the winux Linux-style commands in this PowerShell session:
#   . C:\path\to\winux\bin\winux-activate.ps1
#   or, with winux.exe on PATH:  winux activate | iex
# Add the same line to $PROFILE to make it permanent.

$winuxBin = $PSScriptRoot
if (-not (Test-Path (Join-Path $winuxBin 'winux.exe'))) {
    throw "winux.exe not found in $winuxBin - run build.ps1 first"
}
$winuxShims = Join-Path $winuxBin 'cmd'

# Only names with a shim in cmd\ are shadowed, so PowerShell's own
# ls/dir/cls/md/type/copy/... keep working untouched. The session helper
# scripts (winux-activate.cmd / winux-deactivate.cmd / winux.cmd) and the
# winux- prefixed helpers are not command shims.
$shimNames = @(Get-ChildItem -Path $winuxShims -Filter '*.cmd' |
    Where-Object { $_.BaseName -notmatch '^(activate|deactivate|winux)' } |
    ForEach-Object { $_.BaseName })

# Snapshot the aliases we are about to shadow (PowerShell defaults such as
# cd/ls/pwd plus any user-defined ones) into a per-session file, so
# deactivate.ps1 can restore exactly what was there. User FUNCTIONS with shim
# names are deliberately not restored (rare; they are not covered).
# NOTE: the helper scripts are winux- prefixed (winux-activate.ps1 etc.) so
# adding bin\ to PATH never introduces bare `activate`/`deactivate` names that
# could collide with other toolchains (conda, Python venv, ...).
$env:WINUX_STATE = Join-Path $env:TEMP ("winux-state-" + $PID + ".ps1")
$stateLines = @()
foreach ($name in $shimNames) {
    if (Test-Path "Alias:$name") {
        $def = (Get-Alias $name).Definition.Replace("'", "''")
        $stateLines += "Set-Alias -Name $name -Value '$def' -Force"
    }
}
if ($stateLines.Count -eq 0) {
    $stateLines = @('# no aliases were shadowed by winux activation')
}
Set-Content -LiteralPath $env:WINUX_STATE -Value $stateLines -Encoding utf8

foreach ($name in $shimNames) {
    Remove-Item "Alias:$name" -Force -ErrorAction SilentlyContinue
    Remove-Item "Function:$name" -Force -ErrorAction SilentlyContinue
}

# One-shot PATH history so deactivate.ps1 can restore the exact original PATH.
# Only the shims directory is prepended: winux.exe itself is assumed to be on
# PATH already (that is the whole point of the cmd\ layout).
if (-not $env:WINUX_OLD_PATH) { $env:WINUX_OLD_PATH = $env:Path }
if (($env:Path -split ';') -notcontains $winuxShims) {
    $env:Path = "$winuxShims;$env:Path"
}

# Marks this session as activated so `winux cd` knows the shell hook (the cd
# function below) is in place and skips the "activate first" hint.
$env:WINUX_ACTIVE = '1'

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
        if ($global:winuxPrevDir) {
            $old = (Get-Location).Path
            Set-Location -LiteralPath $global:winuxPrevDir -ErrorAction Stop
            $global:winuxPrevDir = $old
            return
        }
        # no in-session history yet: fall through so winux resolves `-` from
        # its persistent OLDPWD state file (shared with cmd and sh)
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

# venv-style prompt prefix: show `(winux) ` in front of every prompt line (like
# Python's (.venv) prefix) so an activated window is recognizable at a glance.
# The previous prompt function (default or user-customized) is saved and
# restored by winux-deactivate.ps1.
if (-not $global:winuxPromptInstalled) {
    $global:winuxSavedPrompt = if (Test-Path Function:prompt) { (Get-Item Function:prompt).ScriptBlock } else { $null }
    $global:winuxPromptInstalled = $true
}
function global:prompt {
    if ($global:winuxSavedPrompt) {
        $base = & $global:winuxSavedPrompt
        if ($base -like '(winux) *') { return $base }
        return "(winux) $base"
    }
    $loc = $executionContext.SessionState.Path.CurrentLocation
    "(winux) PS $loc$('>' * ($nestedPromptLevel + 1)) "
}

Write-Host "winux activated from $winuxBin" -ForegroundColor Green
Write-Host 'cp rm mv mkdir touch grep cat cd less vi and more now resolve to winux.' -ForegroundColor Green
Write-Host 'Run "winux --help" for the whole command list.' -ForegroundColor Green
