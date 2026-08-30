@echo off
setlocal

rem Source-development convenience only. PowerShell runs in this terminal and
rem receives the checkout path through START-INLAID.ps1.
set "ROOT=%~dp0"
set "SCRIPT=%ROOT%START-INLAID.ps1"

if not exist "%SCRIPT%" (
    echo Inlaid could not find START-INLAID.ps1 beside this file.
    pause
    exit /b 1
)

where pwsh.exe >nul 2>nul
if errorlevel 1 (
    echo PowerShell 7 ^(pwsh.exe^) was not found.
    echo Install PowerShell 7, then double-click this file again.
    pause
    exit /b 1
)

pwsh.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%" %*
if errorlevel 1 (
    echo The Inlaid source launcher failed.
    pause
    exit /b 1
)

exit /b 0
