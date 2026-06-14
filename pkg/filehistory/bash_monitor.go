package filehistory

import (
	"bytes"
	"os"
	"time"
)

// FileChange represents a file that changed between two snapshots.
type FileChange struct {
	Path          string // absolute file path
	BeforeContent []byte // content before the change (nil = file was new)
	AfterContent  []byte // content after the change (nil = file was deleted)
}

// FileSnapshot captures file metadata + content for change detection.
// Content is stored so that DetectChanges can return BeforeContent for modified files,
// which is required by TrackEdit/MakeSnapshot to save pre-edit state for rewind restoration.
type FileSnapshot struct {
	modTime time.Time
	size    int64
	content []byte
}

// TakeSnapshot walks root and captures file metadata + content.
// Skips known large directories (.git, node_modules, vendor, etc.).
func TakeSnapshot(root string) (map[string]*FileSnapshot, error) {
	snapshot := make(map[string]*FileSnapshot)
	err := WalkDir(root, func(path string, _ os.DirEntry) error {
		info, err := os.Stat(path)
		if err != nil {
			return nil // skip inaccessible files
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}
		snapshot[path] = &FileSnapshot{
			modTime: info.ModTime(),
			size:    info.Size(),
			content: content,
		}
		return nil
	})
	return snapshot, err
}

// DetectChanges compares the current filesystem state against a prior snapshot.
// Returns a list of FileChange entries for files that were modified, created, or deleted.
// Only files with actual content changes (not just mtime changes) are included.
func DetectChanges(root string, before map[string]*FileSnapshot) ([]FileChange, error) {
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

		// mtime or size changed — verify content actually differs
		currentContent, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable
		}
		if bytes.Equal(currentContent, snap.content) {
			return nil // content identical (false positive from mtime change)
		}

		changes = append(changes, FileChange{
			Path:          path,
			BeforeContent: snap.content,
			AfterContent:  currentContent,
		})
		return nil
	})

	// Check for deleted files (in snapshot but not in current walk)
	for path, snap := range before {
		if !seen[path] {
			changes = append(changes, FileChange{
				Path:          path,
				BeforeContent: snap.content,
				AfterContent:  nil, // file was deleted
			})
		}
	}

	return changes, err
}
