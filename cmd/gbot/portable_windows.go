//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func setupPortablePaths() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	injectPortablePaths(filepath.Dir(exe))
}
