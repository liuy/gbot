@echo off
chcp 65001 >nul
setlocal

for /f "delims=" %%D in ("%~dp0") do cd /d "%%~fD"
set GBOT_EXE=%CD%\gbot.exe

if not exist "%GBOT_EXE%" (
    echo ERROR: gbot.exe not found next to install.bat
    echo Expected: %GBOT_EXE%
    echo.
    pause
    goto :eof
)

echo Installing GBot context menu...

reg add "HKCU\Software\Classes\Directory\Background\shell\GBot" /ve /d "Open GBot Here" /f >nul
reg add "HKCU\Software\Classes\Directory\Background\shell\GBot" /v Icon /d "%GBOT_EXE%,0" /f >nul
reg add "HKCU\Software\Classes\Directory\Background\shell\GBot\command" /ve /d "\"%GBOT_EXE%\"" /f >nul

reg add "HKCU\Software\Classes\Directory\shell\GBot" /ve /d "Open GBot Here" /f >nul
reg add "HKCU\Software\Classes\Directory\shell\GBot" /v Icon /d "%GBOT_EXE%,0" /f >nul
reg add "HKCU\Software\Classes\Directory\shell\GBot\command" /ve /d "\"%GBOT_EXE%\"" /f >nul

echo.
echo GBot context menu installed successfully.
echo Right-click any folder or folder background to "Open GBot Here".
echo Run uninstall.bat to remove.
echo.
pause
