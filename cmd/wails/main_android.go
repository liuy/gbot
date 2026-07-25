//go:build android && cgo

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// Register gbot's main() with the Wails runtime so that JNI nativeInit
// (called from WailsBridge.initialize on the Java side) can launch it in
// a goroutine. Without this registration the runtime logs an error and
// never starts the engine.
//
// Production builds use CGO_ENABLED=1 (required for c-shared buildmode
// against the NDK); the cgo build tag here ensures the import compiles.
// CGO=0 cross-compile builds use main_android_nocgo.go (no-op stub).
func init() {
	application.RegisterAndroidMain(main)
}
