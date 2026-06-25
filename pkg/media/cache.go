// Package media stores downloaded WeChat media (images, documents) on disk,
// content-hash deduplicated, with a 30-day LRU eviction.
//
// The store owns its cleanup goroutine: New() launches it against a background
// context (so it outlives any single connector), and Store.Close() stops it.
// Callers (the WeChat connector) only call Save/Get and remain unaware that
// cleanup exists; main.go captures MediaCache() and calls Close() at shutdown.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Category classifies a cached file by storage subdirectory under RootDir.
type Category string

const (
	CategoryImage    Category = "images"
	CategoryDocument Category = "documents"
)

// DefaultCleanupInterval is how often the background cleanup loop sweeps the
// cache directories. 6h bounds disk growth between sessions while keeping the
// ReadDir + stat loop cheap (typically <1000 files).
const DefaultCleanupInterval = 6 * time.Hour

// DefaultMaxAge is the LRU eviction threshold — files whose mtime is older than
// this are removed on the next sweep. 30 days matches the openclaw retention.
const DefaultMaxAge = 30 * 24 * time.Hour

// Store is a content-addressed media cache rooted at RootDir. A Store created
// via New() runs a background cleanup goroutine; call Close() at shutdown to
// stop it. NewAt() does not start cleanup (use StartCleanup or New+Close).
type Store struct {
	RootDir    string
	cancelStop context.CancelFunc // nil when cleanup was never started (NewAt path) or already closed
	stopDone   chan struct{}      // closed when the cleanup goroutine has exited
}

// New returns a Store rooted at ~/.gbot/cache, ensures the {images,documents}
// subdirs exist, AND launches the background cleanup goroutine (30-day
// eviction, DefaultCleanupInterval). The goroutine runs against a background
// context so it survives connector cancellation. Call Close() to stop it.
func New() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("media: resolve home dir: %w", err)
	}
	root := filepath.Join(home, ".gbot", "cache")
	s, err := newStoreRoot(root)
	if err != nil {
		return nil, err
	}
	s.cancelStop, s.stopDone = s.startCleanupLoop(context.Background(), DefaultCleanupInterval, DefaultMaxAge)
	return s, nil
}

// NewAt returns a Store at an explicit root (for tests). It does NOT start the
// cleanup goroutine — tests that want to exercise cleanup call StartCleanup
// explicitly, or use New()+Close() with a HOME override.
func NewAt(rootDir string) (*Store, error) {
	return newStoreRoot(rootDir)
}

// newStoreRoot creates the Store and ensures both category subdirs exist.
func newStoreRoot(rootDir string) (*Store, error) {
	for _, cat := range []Category{CategoryImage, CategoryDocument} {
		if err := os.MkdirAll(filepath.Join(rootDir, string(cat)), 0o755); err != nil {
			return nil, fmt.Errorf("media: create %s dir: %w", cat, err)
		}
	}
	return &Store{RootDir: rootDir}, nil
}

// Close stops the cleanup goroutine started by New(). Safe to call multiple
// times; no-op if cleanup was never started (NewAt path) or already closed.
func (s *Store) Close() {
	if s.cancelStop != nil {
		s.cancelStop()
		<-s.stopDone
		s.cancelStop = nil
		s.stopDone = nil
	}
}

// Path returns the would-be cache path for data with the given extension,
// without writing the file. Useful for testing dedup and for callers that want
// to predict the destination. ext must include the leading dot (".png"); an
// empty ext falls back to ".bin".
func (s *Store) Path(cat Category, data []byte, ext string) string {
	if ext == "" {
		ext = ".bin"
	}
	sum := sha256.Sum256(data)
	// sum[:8] = first 8 bytes → 16 hex chars, matching the goal's "sha256[:16]"
	// notation read as 16 hex characters (not 16 raw bytes).
	name := hex.EncodeToString(sum[:8]) + ext
	return filepath.Join(s.RootDir, string(cat), name)
}

// Save writes data to {root}/{category}/{sha256[:16]}.{ext} and returns the
// absolute path. If the file already exists (same content hash), it is NOT
// rewritten — the existing path is returned, preserving the original mtime
// (dedup). ext must include the leading dot (".png"); empty falls back to ".bin".
// Writes are atomic: data lands in a temp file then os.Rename into place, so a
// crash never leaves a partial cache file.
func (s *Store) Save(cat Category, data []byte, ext string) (string, error) {
	if ext == "" {
		ext = ".bin"
	}
	dst := s.Path(cat, data, ext)
	// Dedup: same content hash already on disk → return without rewriting so
	// the original mtime (used by LRU eviction) is preserved.
	if info, err := os.Stat(dst); err == nil && !info.IsDir() {
		return dst, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("media: stat %s: %w", dst, err)
	}
	tmp := dst + ".tmp." + fmt.Sprintf("%d", os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("media: write temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("media: rename %s: %w", dst, err)
	}
	return dst, nil
}

// Cleanup removes files in {root}/{category}/ whose mtime is older than maxAge.
// Returns the count of files removed. Errors on individual files are logged
// (slog.Warn) and skipped so one unreadable file does not abort the sweep.
func (s *Store) Cleanup(cat Category, maxAge time.Duration) int {
	dir := filepath.Join(s.RootDir, string(cat))
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Missing dir is not fatal — nothing to clean.
		if os.IsNotExist(err) {
			return 0
		}
		slog.Warn("media: cleanup readdir failed", "dir", dir, "error", err)
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			slog.Warn("media: cleanup stat failed", "file", entry.Name(), "error", err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				slog.Warn("media: cleanup remove failed", "file", path, "error", err)
				continue
			}
			removed++
		}
	}
	return removed
}

// CleanupAll runs Cleanup across both images and documents. Returns the total
// count of files removed.
func (s *Store) CleanupAll(maxAge time.Duration) int {
	return s.Cleanup(CategoryImage, maxAge) + s.Cleanup(CategoryDocument, maxAge)
}
