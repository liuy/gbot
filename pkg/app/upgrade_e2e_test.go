//go:build !windows

package app

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two phases, one continuous 25 ms probe:
//
//	Phase 1 (rollback): the helper binary is chmod 000 so the child exec
//	fails — the old process must keep serving and the probe must stay green.
//	Phase 2 (success): a fresh `upgrade` touch makes the helper hand the
//	listener to a working child; /who flips root→upgraded with zero failed
//	probes (FD inheritance + graceful drain).
func TestUpgradeE2E_FDInheritanceZeroDowntimeAndRollback(t *testing.T) {
	// Build the helper with the module's own toolchain state; testdata dirs
	// are excluded from `go build ./...`, so this is the only build.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	appDir := filepath.Dir(thisFile)
	helperBin := filepath.Join(t.TempDir(), "upgradehelper")
	build := exec.Command("go", "build", "-o", helperBin, "./testdata/upgradehelper")
	build.Dir = appDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build upgradehelper: %v\n%s", err, out)
	}

	sigDir := t.TempDir()
	addr := "127.0.0.1:" + freeTCPPort(t)
	t.Setenv("UPGRADE_TEST_DIR", sigDir)
	t.Setenv("UPGRADE_TEST_ADDR", addr)

	// A fresh helper must not believe it has a tableflip parent. When this
	// test runs INSIDE a hot-restarted daemon (the daemon is itself a
	// tableflip child and its env — TABLEFLIP_HAS_PARENT_*=yes — leaks
	// into every descendant shell), the helper's tableflip.New would try
	// to decode parent fds that don't exist here and die with EBADF.
	helperEnv := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TABLEFLIP_") {
			continue
		}
		helperEnv = append(helperEnv, kv)
	}
	root := exec.Command(helperBin)
	root.Env = helperEnv
	root.Stdout, root.Stderr = os.Stderr, os.Stderr
	if err := root.Start(); err != nil {
		t.Fatalf("start root helper: %v", err)
	}
	if !waitForFile(t, sigDir, "ready-root", 5*time.Second) {
		t.Fatal("root helper never reached ready-root within 5s")
	}

	// Continuous probe: every failure (dial error or non-200) is recorded.
	var probeFailures atomic.Int64
	var probeWG sync.WaitGroup
	probeStop := make(chan struct{})
	base := "http://" + addr
	probeWG.Go(func() {
		client := &http.Client{Timeout: 2 * time.Second}
		for {
			select {
			case <-probeStop:
				return
			default:
			}
			resp, err := client.Get(base + "/who")
			if err != nil {
				probeFailures.Add(1)
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
				if cerr := resp.Body.Close(); cerr != nil {
					t.Logf("close probe body: %v", cerr)
				}
				if resp.StatusCode != http.StatusOK {
					probeFailures.Add(1)
				}
			}
			time.Sleep(25 * time.Millisecond) // REAL-TIME — real TCP probe cadence
		}
	})

	// Kill both helper processes at teardown — including phase-2 leftovers
	// if an assertion aborts the test early. probeStop may already be
	// closed by the success path, so guard with a Once.
	var stopOnce sync.Once
	stopProbes := func() { stopOnce.Do(func() { close(probeStop) }) }
	t.Cleanup(func() {
		stopProbes()
		probeWG.Wait()
		for _, who := range []string{"root", "upgraded"} {
			data, err := os.ReadFile(filepath.Join(sigDir, "pid-"+who))
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				continue
			}
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
		_ = os.RemoveAll(sigDir)
	})

	// Phase 1 — rollback: break the binary, the upgrade must fail and the
	// old process must keep answering.
	if err := os.Chmod(helperBin, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sigDir, "upgrade"), []byte("1"), 0644); err != nil {
		t.Fatalf("touch upgrade: %v", err)
	}
	if !waitForFile(t, sigDir, "upgrade-failed", 10*time.Second) {
		t.Fatal("upgrade-failed never appeared within 10s; the corrupt-binary rollback was not detected")
	}
	if body := getWho(t, base); body != "root" {
		t.Fatalf("after failed upgrade /who = %q, want \"root\" (old process must keep serving)", body)
	}
	if _, err := os.Stat(filepath.Join(sigDir, "upgraded")); err == nil {
		t.Fatal("upgraded file exists in rollback phase — the child of a corrupt binary must not take over")
	}
	if n := probeFailures.Load(); n != 0 {
		t.Fatalf("probe failures during rollback phase = %d, want 0", n)
	}
	if err := os.Chmod(helperBin, 0o755); err != nil {
		t.Fatalf("chmod 755: %v", err)
	}

	// Phase 2 — normal upgrade. The touch is only observable because the
	// helper consumed (deleted) phase 1's file and kept polling.
	if err := os.WriteFile(filepath.Join(sigDir, "upgrade"), []byte("1"), 0644); err != nil {
		t.Fatalf("touch upgrade (phase 2): %v", err)
	}
	if !waitForFile(t, sigDir, "upgraded", 10*time.Second) {
		t.Fatal("upgraded never appeared within 10s — the working-binary handover did not complete")
	}
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for {
		if body := getWho(t, base); body == "upgraded" {
			break
		}
		if !time.Now().Before(deadline) { // REAL-TIME
			t.Fatalf("/who never flipped to \"upgraded\" within 5s (last: %q)", getWho(t, base))
		}
		time.Sleep(25 * time.Millisecond) // REAL-TIME
	}

	stopProbes()
	probeWG.Wait()
	if n := probeFailures.Load(); n != 0 {
		t.Fatalf("probe failures across the full handover = %d, want 0 (FD inheritance + graceful drain)", n)
	}
	if _, err := os.Stat(filepath.Join(sigDir, "pid-upgraded")); err != nil {
		t.Fatal("pid-upgraded file missing — the upgraded child never wrote its pid")
	}
}

// getWho fetches /who and returns the trimmed body, fataling on transport
// errors (the caller decides whether a non-"who" body is the failure).
func getWho(t *testing.T, base string) string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/who")
	if err != nil {
		t.Fatalf("GET /who: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /who body: %v", err)
	}
	return strings.TrimSpace(string(body))
}

// waitForFile polls for a signal file's existence.
func waitForFile(t *testing.T, dir, name string, timeout time.Duration) bool {
	t.Helper()
	path := filepath.Join(dir, name)
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(25 * time.Millisecond) // REAL-TIME
	}
	_, err := os.Stat(path)
	return err == nil
}
