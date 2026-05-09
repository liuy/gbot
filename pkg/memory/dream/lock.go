package dream

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	lockFileName  = ".consolidate-lock"
	holderStaleMS = 3600_000 // 1 hour — stale even if PID is live (matches TS)
)

// ReadLastConsolidatedAt returns lock file mtime as UnixMS, 0 if absent.
// Per-turn cost: one stat.
// TS source: consolidationLock.ts:29-36 — readLastConsolidatedAt.
func ReadLastConsolidatedAt(memoryDir string) (int64, error) {
	path := filepath.Join(memoryDir, lockFileName)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat lock file: %w", err)
	}
	return info.ModTime().UnixMilli(), nil
}

// readLockPid reads and parses the PID from the lock file body.
// Returns 0 on any error (file missing, parse failure).
func readLockPid(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0
	}
	return pid
}

// TryAcquireConsolidationLock writes PID, sets mtime=now.
// Returns pre-acquire mtime (for rollback), false if blocked/lost race.
// Race detection: re-read file after write, verify this process won.
// TS source: consolidationLock.ts:46-84 — tryAcquireConsolidationLock.
func TryAcquireConsolidationLock(memoryDir string) (priorMtime int64, acquired bool, err error) {
	path := filepath.Join(memoryDir, lockFileName)

	// Read existing lock state
	var mtimeMs int64
	info, statErr := os.Stat(path)
	if statErr == nil {
		mtimeMs = info.ModTime().UnixMilli()
	}
	holderPid := readLockPid(path) // returns 0 if file absent or unparseable

	// If lock exists and is not stale, check if holder is alive
	now := time.Now().UnixMilli()
	if statErr == nil && (now-mtimeMs) < int64(holderStaleMS) {
		if holderPid != 0 && isProcessRunning(holderPid) {
			slog.Debug("dream: lock held by live PID",
				"pid", holderPid,
				"age_seconds", (now-mtimeMs)/1000)
			return 0, false, nil
		}
		// Dead PID or unparseable body — reclaim.
	}

	// Ensure memory dir exists
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return 0, false, fmt.Errorf("mkdir memory dir: %w", err)
	}

	// Write our PID
	pid := os.Getpid()
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return 0, false, fmt.Errorf("write lock file: %w", err)
	}

	// Race detection: re-read to verify we won the write.
	// Handle file deletion between write and read (e.g., crash recovery cleanup).
	verify, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// File deleted mid-operation — reacquire
			if writeErr := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644); writeErr != nil {
				return 0, false, fmt.Errorf("reacquire write failed: %w", writeErr)
			}
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("verify read failed: %w", err)
	}
	verifyPid, err := strconv.Atoi(string(verify))
	if err != nil || verifyPid != pid {
		return 0, false, nil // lost race
	}

	// Return prior mtime (0 if no prior lock existed)
	if statErr != nil {
		return 0, true, nil
	}
	return mtimeMs, true, nil
}

// RollbackConsolidationLock rewinds mtime after failed fork.
// priorMtime 0 → unlink (restore no-file state).
// priorMtime N → write empty body, set mtime to priorMtime.
// Best-effort — logs warning on failure.
// TS source: consolidationLock.ts:91-108 — rollbackConsolidationLock.
func RollbackConsolidationLock(memoryDir string, priorMtime int64) {
	path := filepath.Join(memoryDir, lockFileName)
	if priorMtime == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("dream: rollback unlink failed", "error", err)
		}
		return
	}
	// Write empty body and rewind mtime
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		slog.Warn("dream: rollback write failed", "error", err)
		return
	}
	// Set mtime to priorMtime
	t := time.UnixMilli(priorMtime)
	if err := os.Chtimes(path, t, t); err != nil {
		slog.Warn("dream: rollback utimes failed", "error", err)
	}
}

// RecordConsolidation stamps lock file with current PID and mtime.
// Best-effort, no rollback.
// TS source: consolidationLock.ts:130-140 — recordConsolidation.
func RecordConsolidation(memoryDir string) {
	path := filepath.Join(memoryDir, lockFileName)
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		slog.Warn("dream: recordConsolidation mkdir failed", "error", err)
		return
	}
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(pid), 0o644); err != nil {
		slog.Warn("dream: recordConsolidation write failed", "error", err)
	}
}

// isProcessRunning checks if a process with the given PID is alive.
// Uses signal 0 (existence check without sending a signal).
// TS source: utils/genericProcessUtils.ts — isProcessRunning.
func isProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
