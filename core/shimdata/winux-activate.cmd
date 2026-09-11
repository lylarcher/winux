@echo off
rem Activate the winux Linux-style commands in this cmd.exe session.
rem   call C:\path\to\winux\bin\cmd\winux-activate.cmd
rem   or, with winux.exe on PATH:  for /f "delims=" %l in ('winux activate') do @%l
set "WINUX_SHIMS=%~dp0"
for %%E in ("%~dp0..") do set "WINUX_EXE=%%~fE"
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
