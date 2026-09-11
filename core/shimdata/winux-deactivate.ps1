# Deactivate the winux commands in this PowerShell session (the opposite of
# winux-activate.ps1): restore PATH and any shadowed aliases, drop the cd
# function and clear the markers. Dot-source it:
#   . C:\path\to\winux\bin\winux-deactivate.ps1

$state = $env:WINUX_STATE
if ($state -and (Test-Path -LiteralPath $state)) {
    . $state               # restores exactly the aliases winux-activate.ps1 removed
    Remove-Item -LiteralPath $state -Force -ErrorAction SilentlyContinue
}
Remove-Item Env:WINUX_STATE -ErrorAction SilentlyContinue

Remove-Item Function:cd -ErrorAction SilentlyContinue

if ($env:WINUX_OLD_PATH) {
    $env:Path = $env:WINUX_OLD_PATH
    Remove-Item Env:WINUX_OLD_PATH -ErrorAction SilentlyContinue
} else {
    # fallback: strip every PATH entry that contains winux.exe, and the
    # shims directory (bin\cmd) whose parent contains winux.exe
    $env:Path = (($env:Path -split ';' | Where-Object {
        $_ -and -not (Test-Path (Join-Path $_ 'winux.exe')) -and
        -not (Test-Path (Join-Path $_ '..\winux.exe'))
    }) -join ';')
}

Remove-Item Env:WINUX_ACTIVE, Env:WINUX_PREVDIR -ErrorAction SilentlyContinue
Remove-Variable -Scope Global -Name winuxExe, winuxPrevDir -ErrorAction SilentlyContinue

Write-Host 'winux deactivated: the native PowerShell commands (ls/cat/cd/ps/...) are restored.' -ForegroundColor Yellow
