@echo off
REM Double-click friendly launcher (quotes paths with spaces).
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%USERPROFILE%\.green\start-chat.ps1"
if errorlevel 1 pause
