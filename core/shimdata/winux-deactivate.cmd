@echo off
rem winux session teardown for cmd.exe (the opposite of winux-activate.cmd).
rem Materialized in the same two places as winux-activate.cmd and locates the
rem shims dir the same way, so deactivating via either copy restores the PATH
rem entries that the matching activation prepended.
doskey cd=
doskey mkdir=
doskey echo=
doskey date=
if defined WINUX_OLD_PATH (
    set "PATH=%WINUX_OLD_PATH%"
) else if defined WINUX_SHIMS (
    rem fallback: strip every PATH entry that is the shims dir or the exe dir
    setlocal EnableDelayedExpansion
    set "NP="
    for %%p in ("%PATH:;=" "%") do (
        if /i not "%%~p\"=="%WINUX_SHIMS%" if /i not "%%~p"=="%WINUX_EXE%" (
            if defined NP (set "NP=!NP!;%%~p") else (set "NP=%%~p")
        )
    )
    endlocal & set "PATH=%NP%"
)
set "WINUX_ACTIVE="
set "WINUX_SHIMS="
set "WINUX_EXE="
set "WINUX_OLD_PATH="
echo winux deactivated: mkdir echo date cd are cmd.exe built-ins again.
