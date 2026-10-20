@echo off
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "CloverClassroomAgent" /f >nul 2>&1
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "LabHelper" /f >nul 2>&1
schtasks /delete /tn "LabHelper" /f >nul 2>&1
taskkill /im agent.exe /f >nul 2>&1
rmdir /s /q "C:\LabHelper"
echo Uninstallation complete.
pause
