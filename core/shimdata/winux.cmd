@echo off
setlocal
"%~dp0..\winux.exe" %*
exit /b %ERRORLEVEL%
