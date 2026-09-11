@echo off
rem Deactivate the winux commands in this cmd.exe session (the opposite of winux-activate.cmd).
rem   call C:\path\to\winux\bin\cmd\winux-deactivate.cmd
rem An empty doskey definition removes the macro bound by winux-activate.cmd.
doskey cd=
doskey mkdir=
doskey echo=
doskey date=
if defined WINUX_OLD_PATH (
    set "PATH=%WINUX_OLD_PATH%"
) else if defined WINUX_SHIMS (
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
