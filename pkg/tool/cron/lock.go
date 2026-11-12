// Scheduler lease lock for the durable tasks file.
// Source: utils/cronTasksLock.ts — O_EXCL atomic create, PID liveness probe,
// stale-lock recovery, identity-checked release. "sessionId" in the lock body
// is a stable owner key; gbot uses a per-Manager random UUID (EngineID is not
// unique across processes, so CC's same-sessionId branch would let two
// daemons both own the lock).
package cron

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const lockFileName = "scheduled_tasks.lock"

type lockBody struct {
	SessionID  string `json:"sessionId"`
	PID        int    `json:"pid"`
	AcquiredAt int64  `json:"acquiredAt"`
}

// Lock guards one directory's scheduler ownership.
type Lock struct {
	dir      string
	identity string
}

// NewLock creates a lock handle for dir; identity is the stable owner key
// written into the lock file (per-Manager UUID).
func NewLock(dir, identity string) *Lock {
	return &Lock{dir: dir, identity: identity}
}

func (l *Lock) path() string { return filepath.Join(l.dir, lockFileName) }

// readLock parses the lock file; nil when missing or malformed (CC: zod
// safeParse failure → undefined → stale treatment).
func (l *Lock) readLock() *lockBody {
	raw, err := os.ReadFile(l.path())
	if err != nil {
		return nil
	}
	var body struct {
		SessionID  *string  `json:"sessionId"`
		PID        *float64 `json:"pid"`
		AcquiredAt *float64 `json:"acquiredAt"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	if body.SessionID == nil || body.PID == nil || body.AcquiredAt == nil {
		return nil
	}
	return &lockBody{
		SessionID:  *body.SessionID,
		PID:        int(*body.PID),
		AcquiredAt: int64(*body.AcquiredAt),
	}
}

// tryCreateExclusive atomically creates the lock file ('wx' / O_EXCL).
// A missing parent dir is created and the create retried once (steady state
// always has the dir — scheduled_tasks.json lives there too).
func (l *Lock) tryCreateExclusive(body lockBody) bool {
	raw, err := json.Marshal(body)
	if err != nil {
		return false
	}
	f, err := os.OpenFile(l.path(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err := os.MkdirAll(l.dir, 0o755); err != nil {
			return false
		}
		f, err = os.OpenFile(l.path(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return false
		}
	}
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		_ = os.Remove(l.path())
		return false
	}
	return true
}

// TryAcquire returns true when this identity owns the lock after the call.
// O_EXCL for the atomic test-and-set; if the file exists: already ours → true
// (idempotent, refreshing the pid); another live PID → false; stale (PID dead
// / corrupt) → unlink and retry the exclusive create once. If two managers
// race to recover a stale lock, only one create succeeds.
func (l *Lock) TryAcquire() bool {
	body := lockBody{
		SessionID:  l.identity,
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UnixMilli(),
	}
	if l.tryCreateExclusive(body) {
		return true
	}
	existing := l.readLock()

	// Already ours (idempotent). Same identity but a new pid (engine reload)
	// rewrites the lock so other managers see a live PID.
	if existing != nil && existing.SessionID == l.identity {
		if existing.PID != os.Getpid() {
			if raw, err := json.Marshal(body); err == nil {
				_ = os.WriteFile(l.path(), raw, 0o644)
			}
		}
		return true
	}

	// Another live manager — blocked.
	if existing != nil && processAlive(existing.PID) {
		return false
	}

	// Stale or corrupt — unlink and retry the exclusive create once. The
	// loser of the recovery race returns false.
	_ = os.Remove(l.path())
	return l.tryCreateExclusive(body)
}

// Release removes the lock file only when this identity owns it.
func (l *Lock) Release() {
	if existing := l.readLock(); existing == nil || existing.SessionID != l.identity {
		return
	}
	_ = os.Remove(l.path())
}
