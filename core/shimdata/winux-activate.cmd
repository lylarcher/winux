@echo off
rem winux session setup for cmd.exe.
rem This file is materialized in TWO places from the same embedded template:
rem   <exe dir>\winux-activate.cmd        - typing `winux-activate` works when
rem                                         winux.exe's dir is on PATH
rem   <exe dir>\cmd\winux-activate.cmd    - referenced by the `winux activate`
rem                                         call line / for /f one-liner
rem It locates the shims dir from its own path, so the same content works in
rem both spots: if the parent directory holds winux.exe we are inside cmd\,
rem otherwise we sit next to winux.exe and the shims are in the cmd\ subdir.
if exist "%~dp0..\winux.exe" (
    set "WINUX_SHIMS=%~dp0"
    for %%E in ("%~dp0..") do set "WINUX_EXE=%%~fE"
) else (
    set "WINUX_SHIMS=%~dp0cmd\"
    for %%E in ("%~dp0.") do set "WINUX_EXE=%%~fE"
)
if not defined WINUX_OLD_PATH set "WINUX_OLD_PATH=%PATH%"
set "PATH=%WINUX_SHIMS%;%WINUX_EXE%;%PATH%"
set "WINUX_ACTIVE=1"
rem cmd.exe resolves its own internal commands before PATH, so bind those names
rem to winux with doskey macros (interactive sessions only, batch files unaffected).
rem `cd` is routed through the winux resolver so `cd -`, `cd ~`, `cd /` and
rem cross-drive changes work exactly like the PowerShell hook: the resolver
rem prints the absolute target and the for-loop hands it to cmd's own `cd /d`
rem (the only cmd built-in that can cross drives).
doskey cd=for /f "delims=" %%d in ('winux.exe cd $*') do cd /d %%d
doskey mkdir=winux.exe mkdir $*
doskey echo=winux.exe echo $*
doskey date=winux.exe date $*
echo winux activated: cp rm mv mkdir touch grep cat cd less vi and more are now available.
echo Type "winux --help" for the full command list.
