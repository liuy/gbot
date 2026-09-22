// upgradehelper is a minimal tableflip daemon used by
// TestUpgradeE2E_FDInheritanceZeroDowntimeAndRollback. The test process
// itself must never construct a tableflip upgrader (one per process), so
// the upgrade dance runs in this helper binary.
//
// Env:
//
//	UPGRADE_TEST_DIR  — directory for signal files (ready-*, pid-*, upgrade, upgrade-failed, upgraded)
//	UPGRADE_TEST_ADDR — listen address
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudflare/tableflip"
)

func signalFile(dir, name string) string { return filepath.Join(dir, name) }

func touch(dir, name string) {
	if err := os.WriteFile(signalFile(dir, name), []byte("1"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "upgradehelper: touch %s: %v\n", name, err)
		os.Exit(1)
	}
}

func main() {
	dir := os.Getenv("UPGRADE_TEST_DIR")
	addr := os.Getenv("UPGRADE_TEST_ADDR")
	if dir == "" || addr == "" {
		fmt.Fprintln(os.Stderr, "upgradehelper: UPGRADE_TEST_DIR and UPGRADE_TEST_ADDR required")
		os.Exit(1)
	}

	upg, err := tableflip.New(tableflip.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgradehelper: tableflip.New: %v\n", err)
		os.Exit(1)
	}
	who := "root"
	if upg.HasParent() {
		who = "upgraded"
	}

	ln, err := upg.Fds.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgradehelper: listen %s: %v\n", addr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, who)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "upgradehelper: serve: %v\n", err)
			os.Exit(1)
		}
	}()

	if err := os.WriteFile(signalFile(dir, "pid-"+who), []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "upgradehelper: write pid: %v\n", err)
		os.Exit(1)
	}

	if err := upg.Ready(); err != nil {
		fmt.Fprintf(os.Stderr, "upgradehelper: ready: %v\n", err)
		os.Exit(1)
	}
	touch(dir, "ready-"+who)

	if who == "upgraded" {
		// The upgraded child has nothing left to coordinate — the test's
		// cleanup kills it via the pid-upgraded file.
		select {}
	}

	// Consume-and-keep-polling is load-bearing (two-phase state machine):
	// a one-shot poll would never observe the phase-2 touch because the
	// file already exists, and the success phase would hang forever.
	// Phase 1 = rollback verification (upgrade-failed, keep serving); phase
	// 2 = normal upgrade (upgraded, drain, exit).
	for {
		if _, err := os.Stat(signalFile(dir, "upgrade")); err == nil {
			os.Remove(signalFile(dir, "upgrade")) // consume: next touch is a fresh edge
			if err := upg.Upgrade(); err != nil {
				touch(dir, "upgrade-failed") // rollback: keep serving, keep polling
				continue
			}
			touch(dir, "upgraded")
			<-upg.Exit()
			// Same drain contract as the daemon: graceful, then hard-cut —
			// a raw Close() can cut an in-flight probe response mid-stream
			// and flake the zero-failure assertion.
			dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := srv.Shutdown(dctx); err != nil {
				srv.Close()
			}
			dcancel()
			os.Exit(0)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
