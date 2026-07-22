!define APPNAME "GBot"
!define APPVERSION "0.0.0"
!define COMPANY "GBot"

Name "GBot"
OutFile "..\dist\gbot.exe"
InstallDir "$PROGRAMFILES64\GBot"
RequestExecutionLevel admin

!include "MUI2.nsh"
!include "LogicLib.nsh"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "Install"
    ; Main binary -> $INSTDIR\gbot.exe
    SetOutPath "$INSTDIR"
    File "staging\gbot.exe"

    ; PortableGit tree: each subdirectory must get its own SetOutPath
    ; so File /r copies contents into the right install subdirectory.
    SetOutPath "$INSTDIR\bin"
    File /r "staging\bin\*.*"

    SetOutPath "$INSTDIR\usr"
    File /r "staging\usr\*.*"

    SetOutPath "$INSTDIR\mingw64"
    File /r "staging\mingw64\*.*"

    SetOutPath "$INSTDIR\etc"
    File /r "staging\etc\*.*"

    ; PortableGit's cmd/ directory (git.cmd, etc.) contains Windows CMD
    ; wrappers; gbot invokes bash.exe directly via bin/, so cmd/ is omitted.

    ; Start Menu shortcut — runs gbot.exe -d (Wails GUI mode)
    CreateDirectory "$SMPROGRAMS\GBot"
    CreateShortCut "$SMPROGRAMS\GBot\GBot.lnk" "$INSTDIR\gbot.exe" "-d" "$INSTDIR\gbot.exe" 0

    ; Right-click context menu — detect wt.exe (Win10 2004+ / Win11)
    ; Fallback to direct launch if Windows Terminal absent.
    nsExec::ExecToStack "where wt.exe"
    Pop $0  ; exit code
    Pop $1  ; output (discard)

    ${If} $0 == 0
        StrCpy $0 'wt.exe -d "%V" "$INSTDIR\gbot.exe"'
        StrCpy $1 'wt.exe -d "%1" "$INSTDIR\gbot.exe"'
    ${Else}
        StrCpy $0 '"$INSTDIR\gbot.exe"'
        StrCpy $1 '"$INSTDIR\gbot.exe"'
    ${EndIf}

    ; Folder background right-click (empty space in explorer)
    WriteRegStr HKCU "Software\Classes\Directory\Background\shell\GBot" "" "Open GBot Here"
    WriteRegStr HKCU "Software\Classes\Directory\Background\shell\GBot" "Icon" "$INSTDIR\gbot.exe,0"
    WriteRegStr HKCU "Software\Classes\Directory\Background\shell\GBot\command" "" '$0'

    ; Folder right-click (clicking on a folder icon)
    WriteRegStr HKCU "Software\Classes\Directory\shell\GBot" "" "Open GBot Here"
    WriteRegStr HKCU "Software\Classes\Directory\shell\GBot" "Icon" "$INSTDIR\gbot.exe,0"
    WriteRegStr HKCU "Software\Classes\Directory\shell\GBot\command" "" '$1'

    ; Write PATH and GBOT_BASH_PATH to user environment (HKCU\Environment)
    EnVar::SetHKCU
    EnVar::AddValue "PATH" "$INSTDIR\bin"
    EnVar::AddValue "GBOT_BASH_PATH" "$INSTDIR\bin\bash.exe"
    SendMessage ${HWND_BROADCAST} ${WM_SETTINGCHANGE} 0 "STR:Environment" /TIMEOUT=5000

    ; Uninstaller
    WriteUninstaller "$INSTDIR\uninstall-gbot.exe"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GBot" "DisplayName" "GBot"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GBot" "UninstallString" "$INSTDIR\uninstall-gbot.exe"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GBot" "InstallLocation" "$INSTDIR"
SectionEnd

Section "Uninstall"
    ; Remove context menu entries
    DeleteRegKey HKCU "Software\Classes\Directory\Background\shell\GBot"
    DeleteRegKey HKCU "Software\Classes\Directory\shell\GBot"

    ; Remove PATH entries
    EnVar::SetHKCU
    EnVar::DeleteValue "PATH" "$INSTDIR\bin"
    DeleteRegValue HKCU "Environment" "GBOT_BASH_PATH"
    SendMessage ${HWND_BROADCAST} ${WM_SETTINGCHANGE} 0 "STR:Environment" /TIMEOUT=5000

    ; Remove files
    Delete "$INSTDIR\gbot.exe"
    RMDir /r "$INSTDIR\bin"
    RMDir /r "$INSTDIR\usr"
    RMDir /r "$INSTDIR\mingw64"
    RMDir /r "$INSTDIR\etc"

    ; Remove shortcuts
    Delete "$SMPROGRAMS\GBot\GBot.lnk"
    RMDir "$SMPROGRAMS\GBot"

    ; Remove uninstaller + registry
    Delete "$INSTDIR\uninstall-gbot.exe"
    DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GBot"
SectionEnd
