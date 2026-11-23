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

# restore the original prompt function (drop the (winux) prefix)
if ($global:winuxPromptInstalled) {
    if ($global:winuxSavedPrompt) {
        Set-Item -Path Function:prompt -Value $global:winuxSavedPrompt
    } else {
        Remove-Item Function:prompt -ErrorAction SilentlyContinue
    }
    Remove-Variable -Scope Global -Name winuxSavedPrompt, winuxPromptInstalled -ErrorAction SilentlyContinue
}

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
Remove-Variable -Scope Global -Name winuxExe, winuxPrevDir, winuxBin -ErrorAction SilentlyContinue

# drop the && / || line-editor hook installed by winux-activate.ps1 (PowerShell
# 5.1 only): put Enter back on AcceptLine and remove the helper
if ($global:winuxChainHook) {
    try { Set-PSReadLineKeyHandler -Chord Enter -Function AcceptLine -ErrorAction SilentlyContinue } catch { }
    Remove-Item Function:__winux-rewrite-chain -ErrorAction SilentlyContinue
    Remove-Variable -Scope Global -Name winuxChainHook, __winuxChainExe -ErrorAction SilentlyContinue
}

# drop the session functions that winux-activate.ps1 installed (they shadow
# the cmd\ batch shims so a PowerShell user can type winux-deactivate directly)
Remove-Item Function:winux-deactivate, Function:winux-activate -ErrorAction SilentlyContinue

Write-Host 'winux deactivated: the native PowerShell commands (ls/cat/cd/ps/...) are restored.' -ForegroundColor Yellow
