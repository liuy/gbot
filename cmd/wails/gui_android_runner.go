//go:build android

package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/liuy/gbot/pkg/app"
)

// runGUI is a no-op on Android: the WebView is created and managed by Java
// (MainActivity). app.Start (called from main()) already mounted the WUI HTTP
// server on :8765. Block forever so the Go runtime stays alive until the Java
// side calls nativeShutdown.
func runGUI(inst *app.Instance, wsPort string) {
	slog.Info("android: wui ready", "url", "http://localhost:"+wsPort+"/")
	select {}
}

// applyAndroidEnv configures HOME, GBOT_BASH_PATH, and PATH so the rest of
// the Go code can find the bash bootstrap and rg binary that the Java side
// extracts into the app's files directory. Empty path is a no-op.
//
// NOT idempotent: calling twice with the same path prepends "usr/bin:" to
// PATH a second time. Idempotency is enforced one layer up, in
// gui_android.go's nativeSetDataPath JNI entry, via sync.Once — that's the
// single caller in production (MainActivity.onCreate → bridge.setDataPath),
// so an idempotency guard at the JNI boundary is sufficient and avoids
// paying for a sync.Once on every call here. Tests that exercise this
// function directly either reset env between calls or invoke it once.
func applyAndroidEnv(path string) {
	if path == "" {
		return
	}
	os.Setenv("HOME", path)
	usrBin := filepath.Join(path, "usr", "bin")
	os.Setenv("GBOT_BASH_PATH", filepath.Join(usrBin, "bash"))
	os.Setenv("PATH", usrBin+":"+os.Getenv("PATH"))
}
