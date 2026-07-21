@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion

for /f "delims=" %%D in ("%~dp0") do cd /d "%%~fD"
set GBOT_EXE=%CD%\gbot.exe

if not exist "%GBOT_EXE%" (
    echo ERROR: gbot.exe not found next to install.bat
    echo Expected: %GBOT_EXE%
    echo.
    pause
    goto :eof
)

REM Detect Windows Terminal — use wt.exe if available, otherwise direct launch.
set USE_WT=0
where wt.exe >nul 2>&1 && set USE_WT=1

if "!USE_WT!"=="1" (
    set CMD_BG=wt.exe -d "%%V" "%GBOT_EXE%"
    set CMD_DIR=wt.exe -d "%%1" "%GBOT_EXE%"
    echo Windows Terminal detected.
) else (
    set CMD_BG="%GBOT_EXE%"
    set CMD_DIR="%GBOT_EXE%"
    echo Windows Terminal not found — using direct launch.
)

echo Installing GBot context menu...

REM Folder background (right-click in empty space)
reg add "HKCU\Software\Classes\Directory\Background\shell\GBot" /ve /d "Open GBot Here" /f >nul
reg add "HKCU\Software\Classes\Directory\Background\shell\GBot" /v Icon /d "%GBOT_EXE%,0" /f >nul
reg add "HKCU\Software\Classes\Directory\Background\shell\GBot\command" /ve /d "!CMD_BG!" /f >nul

REM Folder itself (right-click on a folder)
reg add "HKCU\Software\Classes\Directory\shell\GBot" /ve /d "Open GBot Here" /f >nul
reg add "HKCU\Software\Classes\Directory\shell\GBot" /v Icon /d "%GBOT_EXE%,0" /f >nul
reg add "HKCU\Software\Classes\Directory\shell\GBot\command" /ve /d "!CMD_DIR!" /f >nul

echo.
echo GBot context menu installed successfully.
echo Right-click any folder or folder background to "Open GBot Here".
echo Run uninstall.bat to remove.
echo.
pause
