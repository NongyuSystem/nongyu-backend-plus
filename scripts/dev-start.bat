@echo off
REM ===========================================================================
REM  dev-start.bat - thin wrapper that forwards to dev-start.ps1
REM
REM  Why so thin: cmd.exe has hard limits that bit us repeatedly - it parses
REM  .bat with the active code page (so non-ASCII comments break before chcp
REM  can run), `for /f` mis-reads LF-only files like our .env, and `%%X`
REM  cannot be followed by a digit. All real logic lives in dev-start.ps1.
REM
REM  This file is intentionally ASCII-only.
REM
REM  Usage:  double-click, or from a terminal:
REM    scripts\dev-start.bat                start + probe
REM    scripts\dev-start.bat -ProbeOnly     probe only
REM    scripts\dev-start.bat -Stop          stop
REM    scripts\dev-start.bat -Foreground    run in foreground
REM    scripts\dev-start.bat -Build         build .exe then run
REM ===========================================================================
setlocal
set "SCRIPT=%~dp0dev-start.ps1"

if not exist "%SCRIPT%" (
    echo [error] dev-start.ps1 not found next to this .bat: %SCRIPT%
    exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%" %*
exit /b %ERRORLEVEL%
