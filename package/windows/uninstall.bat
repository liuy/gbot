@echo off
reg delete "HKCU\Software\Classes\Directory\Background\shell\GBot" /f >nul 2>&1
reg delete "HKCU\Software\Classes\Directory\shell\GBot" /f >nul 2>&1
echo GBot context menu removed.
pause
