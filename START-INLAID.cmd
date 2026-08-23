@echo off
setlocal

rem This is the file to double-click. PowerShell performs the Windows Terminal
rem launch with structured arguments so spaces in the project path stay intact.
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

start "" /b pwsh.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%" %*
if errorlevel 1 (
    echo PowerShell 7 could not be started.
    pause
    exit /b 1
)

exit /b 0
