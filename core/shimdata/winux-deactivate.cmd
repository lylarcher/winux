@echo off
rem winux session teardown for cmd.exe (the opposite of winux-activate.cmd).
rem Materialized in the same two places as winux-activate.cmd and locates the
rem shims dir the same way, so deactivating via either copy restores the PATH
rem entries that the matching activation prepended.
rem
rem KEEP THIS FILE FLAT: no multi-line ( ... ) blocks, one statement per line or
rem behind a label. cmd.exe's batch parser only tracks line offsets correctly
rem with CRLF line endings; for an LF-only file a multi-line block is read a few
rem bytes off, so fragments of the script run as commands ("rem" -> 'm',
rem "doskey" -> 'skey', "if defined" -> 'defined', ") else ..." -> 'else').
rem shimgen.go now writes CRLF, and this script stays line-oriented so a wrong
rem line ending can never desynchronise a block.
doskey cd=
doskey mkdir=
doskey echo=
doskey date=
rem drop the (winux) prompt prefix, restoring the exact previous PROMPT
if defined WINUX_OLD_PROMPT set "PROMPT=%WINUX_OLD_PROMPT%"
if defined WINUX_OLD_PATH goto :restore_path
if not defined WINUX_SHIMS goto :clean_env
rem fallback: no saved PATH (activated by an older winux, or the variable was
rem lost), so rebuild PATH entry by entry, dropping the shims/install dirs.
set "WINUX_NP="
for %%p in ("%PATH:;=" "%") do call :keep_entry "%%~p"
set "PATH=%WINUX_NP%"
set "WINUX_NP="
goto :clean_env

:keep_entry
rem %~1 is one PATH entry, already unquoted. Entries and WINUX_SHIMS may carry a
rem trailing backslash, so both sides are trimmed before comparing.
set "WINUX_E=%~1"
if not defined WINUX_E goto :eof
if "%WINUX_E:~-1%"=="\" set "WINUX_E=%WINUX_E:~0,-1%"
set "WINUX_NS=%WINUX_SHIMS%"
if defined WINUX_NS if "%WINUX_NS:~-1%"=="\" set "WINUX_NS=%WINUX_NS:~0,-1%"
if /i "%WINUX_E%"=="%WINUX_NS%" goto :eof
if /i "%WINUX_E%"=="%WINUX_HOME%" goto :eof
if /i "%WINUX_E%"=="%WINUX_EXE%" goto :eof
if not defined WINUX_NP goto :keep_first
set "WINUX_NP=%WINUX_NP%;%WINUX_E%"
goto :eof
:keep_first
set "WINUX_NP=%WINUX_E%"
goto :eof

:restore_path
set "PATH=%WINUX_OLD_PATH%"

:clean_env
set "WINUX_ACTIVE="
set "WINUX_SHIMS="
set "WINUX_HOME="
set "WINUX_EXE="
set "WINUX_OLD_PATH="
set "WINUX_OLD_PROMPT="
echo winux deactivated: mkdir echo date cd are cmd.exe built-ins again.
goto :eof
