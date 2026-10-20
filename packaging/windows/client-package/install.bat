@echo off
setlocal
rem Run as administrator while the classroom restore software is thawed.
set "DIR=C:\LabHelper"

if not exist "%~dp0agent.exe" (
  echo agent.exe is missing from this package.
  pause
  exit /b 1
)
if not exist "%~dp0client.json" (
  echo client.json is missing from this package.
  pause
  exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0verify-config.ps1" -Path "%~dp0client.json"
if errorlevel 1 (
  echo Configure a valid adminPub in client.json before installing.
  echo The management app can export a client package with its public key pinned.
  pause
  exit /b 1
)

if not exist "%DIR%" mkdir "%DIR%"
copy /y "%~dp0agent.exe" "%DIR%\agent.exe" >nul
if errorlevel 1 exit /b 1
copy /y "%~dp0client.json" "%DIR%\client.json" >nul
if errorlevel 1 exit /b 1

rem Remove the older SYSTEM task and start the agent in the logged-in session.
schtasks /delete /tn "LabHelper" /f >nul 2>&1
reg add "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "CloverClassroomAgent" /t REG_SZ /d "\"%DIR%\agent.exe\"" /f
if errorlevel 1 exit /b 1

"%DIR%\agent.exe" -id
if errorlevel 1 exit /b 1
icacls "%DIR%" /inheritance:r /grant:r "SYSTEM:F" "Administrators:F" /grant:r "Users:RX"
if errorlevel 1 exit /b 1

echo.
echo Installation complete. Sign in and confirm the client appears in the management app.
echo Then freeze the classroom restore software.
pause
