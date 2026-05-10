package filehistory

import (
	"crypto/sha256"
	"os"
	"time"
)

// FileChange represents a file that changed between two snapshots.
type FileChange struct {
	Path          string // absolute file path
	BeforeContent []byte // content before the change (nil = file was new)
	AfterContent  []byte // content after the change (nil = file was deleted)
}

// fileSnapshot captures file metadata + content hash for change detection.
// Only a 32-byte sha256 hash is stored (not full content), avoiding OOM on large repos
// while still allowing accurate content comparison to filter false-positive mtime changes.
type fileSnapshot struct {
	modTime    time.Time
	size       int64
	contentHash [sha256.Size]byte
}

// TakeSnapshot walks root and captures file metadata + content hash.
// Skips known large directories (.git, node_modules, vendor, etc.).
// Full content is NOT stored — only a sha256 hash for change verification.
func TakeSnapshot(root string) (map[string]*fileSnapshot, error) {
	snapshot := make(map[string]*fileSnapshot)
	err := WalkDir(root, func(path string, _ os.DirEntry) error {
		info, err := os.Stat(path)
		if err != nil {
			return nil // skip inaccessible files
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}
		snapshot[path] = &fileSnapshot{
			modTime:    info.ModTime(),
			size:       info.Size(),
			contentHash: sha256.Sum256(content),
		}
		return nil
	})
	return snapshot, err
}

// DetectChanges compares the current filesystem state against a prior snapshot.
// Returns a list of FileChange entries for files that were modified, created, or deleted.
// Only files with actual content changes (not just mtime changes) are included.
//
// For modified files, content is read lazily: only when mtime or size changed.
// The stored content hash filters false-positive mtime-only changes without storing full content.
func DetectChanges(root string, before map[string]*fileSnapshot) ([]FileChange, error) {
	var changes []FileChange

	// Build a set of paths seen in current walk, to detect deletions later
	seen := make(map[string]bool)

	// Check current files against snapshot
	err := WalkDir(root, func(path string, _ os.DirEntry) error {
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil {
			return nil // skip inaccessible
		}

		snap, existed := before[path]
		if !existed {
			// New file created during Bash execution
			changes = append(changes, FileChange{
				Path:          path,
				BeforeContent: nil, // file didn't exist before
				AfterContent:  nil, // we don't need after-content for new files
			})
			return nil
		}

		// File existed — check if mtime or size changed
		if info.ModTime().Equal(snap.modTime) && info.Size() == snap.size {
			return nil // no change detected
		}

		// mtime or size changed — verify content actually differs via hash comparison
		currentContent, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable
		}
		currentHash := sha256.Sum256(currentContent)
		if currentHash == snap.contentHash {
			return nil // content identical (false positive from mtime change)
		}

		changes = append(changes, FileChange{
			Path:          path,
			BeforeContent: nil, // not available (only hash was stored)
			AfterContent:  currentContent,
		})
		return nil
	})

	// Check for deleted files (in snapshot but not in current walk)
	for path := range before {
		if !seen[path] {
			changes = append(changes, FileChange{
				Path:          path,
				BeforeContent: nil, // not available (only hash was stored)
				AfterContent:  nil, // file was deleted
			})
		}
	}

	return changes, err
}
