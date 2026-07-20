//go:build windows

package bash

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows/registry"
)

// resolveShellCommand returns the bash executable path on Windows.
//
// Resolution order:
//  1. Test override: if shellCommand has been changed from "bash" (default),
//     honor it. This preserves the cross-platform test hook used by
//     bash_internal_test.go and pty_test.go to force error paths.
//  2. GBOT_BASH_PATH env var (user override).
//  3. GitForWindows registry key under HKLM\SOFTWARE\GitForWindow.
//  4. Standard Program Files install paths (64-bit, MSYS-style, 32-bit).
//  5. %LOCALAPPDATA%\Programs\Git\bin\bash.exe (non-admin install).
//  6. exec.LookPath("bash.exe") — last resort; may find WSL bash.
//  7. Literal "bash" — defer to go-pty's LookPath inside Cmd.Start to
//     surface a clear error.
//
// Result is cached via sync.OnceValue after first successful resolution.
func resolveShellCommand() string {
	if shellCommand != "bash" {
		return shellCommand
	}
	return resolvedShellCommandOnce()
}

var resolvedShellCommandOnce = sync.OnceValue(func() string {
	if p := os.Getenv("GBOT_BASH_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, ok := lookupGitForWindowsViaRegistry(); ok {
		return p
	}
	candidates := []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "Programs", "Git", "bin", "bash.exe"))
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath("bash.exe"); err == nil {
		return p
	}
	return "bash"
})

func lookupGitForWindowsViaRegistry() (string, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\GitForWindows`, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	installPath, _, err := k.GetStringValue("InstallPath")
	if err != nil || installPath == "" {
		return "", false
	}
	p := filepath.Join(installPath, "bin", "bash.exe")
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}
