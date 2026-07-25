//go:build android && !cgo

package main

// Compile-only stub for host-side cross-compile verification (CGO_ENABLED=0).
// Production Android builds use CGO_ENABLED=1 via main_android.go, which
// imports the Wails application package to register main() with the runtime.
// That import pulls in cgo glue that has no CGO=0 stubs in Wails v3 alpha
// (mobile_features_android.go references undefined androidBridge* symbols).
// This file lets `go build -tags android ./cmd/wails/` succeed under CGO=0
// for syntax/type checking without producing a runnable binary.
