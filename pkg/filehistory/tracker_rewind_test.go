package filehistory

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// setupMultiEditSameFile creates a tracker and a file edited at turns 1..N.
// The file starts at "v0" and is edited to "v1", "v2", ..., "vN".
// Each edit records the pre-edit content as a backup.
// Returns the tracker, file path, and a map of expected content at each turn boundary.
func setupMultiEditSameFile(t *testing.T, numEdits int) (*Tracker, string, map[int]string) {
	t.Helper()
	dir := t.TempDir()
	tr := NewTracker(dir)
	targetFile := filepath.Join(t.TempDir(), "multi.go")

	// v0 = initial content
	mustWriteFile(t, targetFile, []byte("v0"))

	expected := map[int]string{}
	// After rewind to turn K, file should be "v{K-1}" (pre-edit-of-turn-K)
	// After rewind to turn 0, file should be "v0" (original)
	expected[0] = "v0"

	for i := 1; i <= numEdits; i++ {
		preContent := []byte("v" + itoa(i-1))
		if err := tr.RecordBackup(targetFile, preContent, i); err != nil {
			t.Fatalf("RecordBackup turn %d: %v", i, err)
		}
		mustWriteFile(t, targetFile, []byte("v"+itoa(i)))
		// After rewind to turn i+1, file = "v{i}" (pre-edit of turn i+1)
		// But we handle this in expected map: rewind to turn K = "v{K-1}"
		expected[i] = "v" + itoa(i-1)
	}

	// Current file should be "v{numEdits}"
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "v"+itoa(numEdits) {
		t.Fatalf("setup: file = %q, want %q", string(data), "v"+itoa(numEdits))
	}

	return tr, targetFile, expected
}

// TestRewind_SameFile_MultipleEdits_RewindToEachTurn verifies that rewinding
// to any intermediate turn correctly restores the file to its pre-edit state.
//
// Setup: file goes v0 → v1 → v2 → v3 → v4 (edits at turns 1..4)
// Rewind to turn 4 → file = "v3" (pre-edit-of-turn-4)
// Rewind to turn 3 → file = "v2"
// Rewind to turn 2 → file = "v1"
// Rewind to turn 1 → file = "v0"
// Rewind to turn 0 → no records to match, file unchanged (edge case)
func TestRewind_SameFile_MultipleEdits_RewindToEachTurn(t *testing.T) {
	for _, targetTurn := range []int{4, 3, 2, 1} {
		t.Run("rewind_to_turn_"+itoa(targetTurn), func(t *testing.T) {
			tr, file, expected := setupMultiEditSameFile(t, 4)

			restored, err := tr.RestoreToIndex(targetTurn)
			if err != nil {
				t.Fatalf("RestoreToIndex(%d): %v", targetTurn, err)
			}
			if len(restored) != 1 {
				t.Fatalf("expected 1 restored file, got %d: %v", len(restored), restored)
			}
			if restored[0] != file {
				t.Errorf("restored[0] = %q, want %q", restored[0], file)
			}

			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read file: %v", err)
			}
			want := expected[targetTurn]
			if string(data) != want {
				t.Errorf("file after rewind to turn %d = %q, want %q", targetTurn, string(data), want)
			}

			// Verify records were truncated
			records := tr.Records()
			for _, r := range records {
				if r.TurnIndex >= targetTurn {
					t.Errorf("record with TurnIndex %d should have been truncated (target=%d)", r.TurnIndex, targetTurn)
				}
			}
		})
	}
}

// TestRewind_SameFile_MultipleEdits_RewindPastAllEdits verifies rewind to turn 0
// when all edits happened at turns 1..N. The earliest record is turn 1,
// so rewind to 0 finds record at turn 1 and restores "v0".
func TestRewind_SameFile_MultipleEdits_RewindPastAllEdits(t *testing.T) {
	tr, file, _ := setupMultiEditSameFile(t, 4)

	restored, err := tr.RestoreToIndex(0)
	if err != nil {
		t.Fatalf("RestoreToIndex(0): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "v0" {
		t.Errorf("file = %q, want %q", string(data), "v0")
	}
	// All records truncated
	if len(tr.Records()) != 0 {
		t.Errorf("expected 0 records after full rewind, got %d", len(tr.Records()))
	}
}

// TestRewind_MultipleFiles_IntermediateRewind verifies rewinding to an
// intermediate turn where some files are affected and some are not.
//
// Setup:
//   - File A edited at turn 1 (a0→a1), turn 3 (a1→a3), turn 5 (a3→a5)
//   - File B edited at turn 2 (b0→b2), turn 4 (b2→b4)
//   - File C edited at turn 3 only (c0→c3)
//
// Rewind to turn 3 removes turns [3..end]:
//   - A: earliest record >= 3 is turn 3, pre-edit = "a1" → restored to "a1"
//   - B: earliest record >= 3 is turn 4, pre-edit = "b2" → restored to "b2"
//   - C: earliest record >= 3 is turn 3, pre-edit = "c0" → restored to "c0"
//
// Rewind to turn 2 removes turns [2..end]:
//   - A: earliest record >= 2 is turn 3, pre-edit = "a1" → restored to "a1"
//     (turn 1 edit is kept because 1 < 2)
//   - B: earliest record >= 2 is turn 2, pre-edit = "b0" → restored to "b0"
//   - C: earliest record >= 2 is turn 3, pre-edit = "c0" → restored to "c0"
func TestRewind_MultipleFiles_IntermediateRewind(t *testing.T) {
	tmpDir := t.TempDir()
	fileA := filepath.Join(tmpDir, "a.go")
	fileB := filepath.Join(tmpDir, "b.go")
	fileC := filepath.Join(tmpDir, "c.go")

	// Helper to set up a fresh scenario and rewind to a specific turn
	runRewind := func(t *testing.T, rewindTo int, expectA, expectB, expectC string) {
		t.Helper()
		dir := t.TempDir()
		tr := NewTracker(dir)

		// Initial state
		mustWriteFile(t, fileA, []byte("a0"))
		mustWriteFile(t, fileB, []byte("b0"))
		mustWriteFile(t, fileC, []byte("c0"))

		// Turn 1: edit A (a0→a1)
		if err := tr.RecordBackup(fileA, []byte("a0"), 1); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("a1"))

		// Turn 2: edit B (b0→b2)
		if err := tr.RecordBackup(fileB, []byte("b0"), 2); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileB, []byte("b2"))

		// Turn 3: edit A (a1→a3), edit C (c0→c3)
		if err := tr.RecordBackup(fileA, []byte("a1"), 3); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("a3"))
		if err := tr.RecordBackup(fileC, []byte("c0"), 3); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileC, []byte("c3"))

		// Turn 4: edit B (b2→b4)
		if err := tr.RecordBackup(fileB, []byte("b2"), 4); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileB, []byte("b4"))

		// Turn 5: edit A (a3→a5)
		if err := tr.RecordBackup(fileA, []byte("a3"), 5); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("a5"))

		// Act: rewind
		restored, err := tr.RestoreToIndex(rewindTo)
		if err != nil {
			t.Fatalf("RestoreToIndex(%d): %v", rewindTo, err)
		}

		// Verify all 3 files are in restored list
		sort.Strings(restored)
		if len(restored) != 3 {
			t.Fatalf("expected 3 restored, got %d: %v", len(restored), restored)
		}
		if !slices.Contains(restored, fileA) {
			t.Errorf("fileA missing from restored list: %v", restored)
		}
		if !slices.Contains(restored, fileB) {
			t.Errorf("fileB missing from restored list: %v", restored)
		}
		if !slices.Contains(restored, fileC) {
			t.Errorf("fileC missing from restored list: %v", restored)
		}

		// Verify file contents
		assertFileContent(t, fileA, expectA)
		assertFileContent(t, fileB, expectB)
		assertFileContent(t, fileC, expectC)
	}

	t.Run("rewind_to_3", func(t *testing.T) {
		// Rewind removes turns [3..end]
		// A: earliest >= 3 is turn 3 (pre="a1") → "a1"
		// B: earliest >= 3 is turn 4 (pre="b2") → "b2"
		// C: earliest >= 3 is turn 3 (pre="c0") → "c0"
		runRewind(t, 3, "a1", "b2", "c0")
	})

	t.Run("rewind_to_2", func(t *testing.T) {
		// Rewind removes turns [2..end]
		// A: earliest >= 2 is turn 3 (pre="a1") → "a1" (turn 1 edit kept)
		// B: earliest >= 2 is turn 2 (pre="b0") → "b0"
		// C: earliest >= 2 is turn 3 (pre="c0") → "c0"
		runRewind(t, 2, "a1", "b0", "c0")
	})

	t.Run("rewind_to_1", func(t *testing.T) {
		// Rewind removes turns [1..end]
		// A: earliest >= 1 is turn 1 (pre="a0") → "a0"
		// B: earliest >= 1 is turn 2 (pre="b0") → "b0"
		// C: earliest >= 1 is turn 3 (pre="c0") → "c0"
		runRewind(t, 1, "a0", "b0", "c0")
	})
}

// TestRewind_MixedOperations_CreateEditDelete tests file creation, editing,
// and deletion across multiple turns with rewind to verify each operation
// type is correctly reversed.
//
// Setup:
//   - Turn 1: create file A (nil backup = didn't exist before)
//   - Turn 2: edit file A (backup "created content")
//   - Turn 3: create file B (nil backup), edit file A again
//   - Turn 4: edit file B
//
// Rewind to turn 3: B deleted (created at turn 3, nil backup → delete)
//   - A: earliest >= 3 is turn 3 (pre-edit) → restored to "edited_A_v2"
//   - B: earliest >= 3 is turn 3 (nil backup) → DELETED
//
// Rewind to turn 2: B deleted (created at turn 3), A restored to post-turn-1
//   - A: earliest >= 2 is turn 2 (pre-edit = "created content") → restored
//   - B: earliest >= 2 is turn 3 (nil backup) → DELETED
//
// Rewind to turn 1: A deleted (created at turn 1), B deleted (created at turn 3)
//   - A: earliest >= 1 is turn 1 (nil backup) → DELETED
//   - B: earliest >= 1 is turn 3 (nil backup) → DELETED
func TestRewind_MixedOperations_CreateEditDelete(t *testing.T) {
	t.Run("rewind_to_3_deletes_B_restores_A", func(t *testing.T) {
		tmpDir := t.TempDir()
		fileA := filepath.Join(tmpDir, "a.go")
		fileB := filepath.Join(tmpDir, "b.go")
		dir := t.TempDir()
		tr := NewTracker(dir)

		// Turn 1: create file A
		mustWriteFile(t, fileA, []byte("created_A"))
		if err := tr.RecordBackup(fileA, nil, 1); err != nil {
			t.Fatal(err)
		}

		// Turn 2: edit file A
		if err := tr.RecordBackup(fileA, []byte("created_A"), 2); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("edited_A_v2"))

		// Turn 3: create file B, edit file A
		mustWriteFile(t, fileB, []byte("created_B"))
		if err := tr.RecordBackup(fileB, nil, 3); err != nil {
			t.Fatal(err)
		}
		if err := tr.RecordBackup(fileA, []byte("edited_A_v2"), 3); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("edited_A_v3"))

		// Turn 4: edit file B
		if err := tr.RecordBackup(fileB, []byte("created_B"), 4); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileB, []byte("edited_B_v4"))

		// Rewind to turn 3: removes turns [3..end]
		// B was created at turn 3 (nil backup), so rewind to 3 DELETES B.
		// A: earliest >= 3 is turn 3 (pre="edited_A_v2") → restored.
		restored, err := tr.RestoreToIndex(3)
		if err != nil {
			t.Fatalf("RestoreToIndex(3): %v", err)
		}
		sort.Strings(restored)
		if len(restored) != 2 {
			t.Fatalf("expected 2 restored, got %d: %v", len(restored), restored)
		}

		// A: restored to pre-edit-of-turn-3 = "edited_A_v2"
		assertFileContent(t, fileA, "edited_A_v2")
		// B: created at turn 3 (nil backup) → DELETED by rewind
		if _, err := os.Stat(fileB); !os.IsNotExist(err) {
			t.Errorf("fileB should have been deleted (created in rewound turns), err=%v", err)
		}
	})

	t.Run("rewind_to_2_deletes_B_restores_A", func(t *testing.T) {
		tmpDir := t.TempDir()
		fileA := filepath.Join(tmpDir, "a.go")
		fileB := filepath.Join(tmpDir, "b.go")
		dir := t.TempDir()
		tr := NewTracker(dir)

		// Same setup as above
		mustWriteFile(t, fileA, []byte("created_A"))
		if err := tr.RecordBackup(fileA, nil, 1); err != nil {
			t.Fatal(err)
		}
		if err := tr.RecordBackup(fileA, []byte("created_A"), 2); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("edited_A_v2"))
		mustWriteFile(t, fileB, []byte("created_B"))
		if err := tr.RecordBackup(fileB, nil, 3); err != nil {
			t.Fatal(err)
		}
		if err := tr.RecordBackup(fileA, []byte("edited_A_v2"), 3); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("edited_A_v3"))
		if err := tr.RecordBackup(fileB, []byte("created_B"), 4); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileB, []byte("edited_B_v4"))

		// Rewind to turn 2: removes turns [2..end]
		restored, err := tr.RestoreToIndex(2)
		if err != nil {
			t.Fatalf("RestoreToIndex(2): %v", err)
		}
		sort.Strings(restored)
		if len(restored) != 2 {
			t.Fatalf("expected 2 restored, got %d: %v", len(restored), restored)
		}

		// A: earliest >= 2 is turn 2 (pre="created_A") → "created_A"
		assertFileContent(t, fileA, "created_A")

		// B: earliest >= 2 is turn 3 (nil backup) → DELETED
		if _, err := os.Stat(fileB); !os.IsNotExist(err) {
			t.Errorf("fileB should have been deleted, err=%v", err)
		}
	})

	t.Run("rewind_to_1_deletes_both", func(t *testing.T) {
		tmpDir := t.TempDir()
		fileA := filepath.Join(tmpDir, "a.go")
		fileB := filepath.Join(tmpDir, "b.go")
		dir := t.TempDir()
		tr := NewTracker(dir)

		// Same setup
		mustWriteFile(t, fileA, []byte("created_A"))
		if err := tr.RecordBackup(fileA, nil, 1); err != nil {
			t.Fatal(err)
		}
		if err := tr.RecordBackup(fileA, []byte("created_A"), 2); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("edited_A_v2"))
		mustWriteFile(t, fileB, []byte("created_B"))
		if err := tr.RecordBackup(fileB, nil, 3); err != nil {
			t.Fatal(err)
		}
		if err := tr.RecordBackup(fileA, []byte("edited_A_v2"), 3); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileA, []byte("edited_A_v3"))
		if err := tr.RecordBackup(fileB, []byte("created_B"), 4); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, fileB, []byte("edited_B_v4"))

		// Rewind to turn 1: removes turns [1..end]
		restored, err := tr.RestoreToIndex(1)
		if err != nil {
			t.Fatalf("RestoreToIndex(1): %v", err)
		}
		if len(restored) != 2 {
			t.Fatalf("expected 2 restored (both deleted), got %d: %v", len(restored), restored)
		}

		// Both files should be deleted
		if _, err := os.Stat(fileA); !os.IsNotExist(err) {
			t.Errorf("fileA should have been deleted, err=%v", err)
		}
		if _, err := os.Stat(fileB); !os.IsNotExist(err) {
			t.Errorf("fileB should have been deleted, err=%v", err)
		}
	})
}

// TestRewind_SameFile_SequentialRewindsFromEnd verifies that after a partial rewind,
// the truncated records allow a second rewind to an even earlier state.
// This simulates user doing /rewind twice: first to turn 3, then to turn 1.
func TestRewind_SameFile_SequentialRewindsFromEnd(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	targetFile := filepath.Join(t.TempDir(), "seq.go")

	// v0 → v1 → v2 → v3 → v4
	mustWriteFile(t, targetFile, []byte("v0"))
	if err := tr.RecordBackup(targetFile, []byte("v0"), 1); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v1"))
	if err := tr.RecordBackup(targetFile, []byte("v1"), 2); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v2"))
	if err := tr.RecordBackup(targetFile, []byte("v2"), 3); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v3"))
	if err := tr.RecordBackup(targetFile, []byte("v3"), 4); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v4"))

	// First rewind: to turn 3 → file becomes "v2" (pre-edit-of-turn-3)
	restored, err := tr.RestoreToIndex(3)
	if err != nil {
		t.Fatalf("RestoreToIndex(3): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	assertFileContent(t, targetFile, "v2")

	// Records should now only have turns < 3 (turns 1 and 2)
	records := tr.Records()
	if len(records) != 2 {
		t.Fatalf("expected 2 records after first rewind, got %d", len(records))
	}

	// Second rewind: to turn 1 → file becomes "v0" (pre-edit-of-turn-1)
	restored, err = tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex(1): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored on second rewind, got %d", len(restored))
	}
	assertFileContent(t, targetFile, "v0")

	// All records gone
	if len(tr.Records()) != 0 {
		t.Errorf("expected 0 records after full rewind, got %d", len(tr.Records()))
	}
}

// TestRewind_MultipleFiles_OnlyAffectedFilesRestored verifies that files
// NOT edited during the rewound turns are left untouched.
func TestRewind_MultipleFiles_OnlyAffectedFilesRestored(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	tmpDir := t.TempDir()

	fileA := filepath.Join(tmpDir, "a.go")
	fileB := filepath.Join(tmpDir, "b.go")

	mustWriteFile(t, fileA, []byte("a0"))
	mustWriteFile(t, fileB, []byte("b0"))

	// Turn 1: edit only file A
	if err := tr.RecordBackup(fileA, []byte("a0"), 1); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, fileA, []byte("a1"))

	// Turn 3: edit only file B (gap at turn 2)
	if err := tr.RecordBackup(fileB, []byte("b0"), 3); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, fileB, []byte("b3"))

	// Rewind to turn 2: removes turns [2..end]
	// A: earliest >= 2 is... none! A's only record is at turn 1 (1 < 2). Not affected.
	// B: earliest >= 2 is turn 3 (pre="b0") → restored to "b0"
	restored, err := tr.RestoreToIndex(2)
	if err != nil {
		t.Fatalf("RestoreToIndex(2): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored (only B), got %d: %v", len(restored), restored)
	}
	if restored[0] != fileB {
		t.Errorf("restored[0] = %q, want %q", restored[0], fileB)
	}

	// A should be untouched (still "a1")
	assertFileContent(t, fileA, "a1")
	// B should be restored to "b0"
	assertFileContent(t, fileB, "b0")
}

// --- helpers ---

func itoa(i int) string {
	return string(rune('0' + i))
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s content = %q, want %q", filepath.Base(path), string(data), want)
	}
}

// --- RestoreFilesOnly tests ---

// TestRestoreFilesOnly_RestoresFilesWithoutTruncatingRecords verifies that
// RestoreFilesOnly restores file content but keeps all backup records intact.
// This is used by "Restore code only" option in /rewind.
func TestRestoreFilesOnly_RestoresFilesWithoutTruncatingRecords(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	targetFile := filepath.Join(t.TempDir(), "test.go")

	// v0 → v1 → v2
	mustWriteFile(t, targetFile, []byte("v0"))
	if err := tr.RecordBackup(targetFile, []byte("v0"), 1); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v1"))
	if err := tr.RecordBackup(targetFile, []byte("v1"), 2); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v2"))

	// RestoreFilesOnly to turn 1: file becomes "v0", records stay intact
	restored, err := tr.RestoreFilesOnly(1)
	if err != nil {
		t.Fatalf("RestoreFilesOnly(1): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	assertFileContent(t, targetFile, "v0")

	// Records should NOT be truncated — both records still present
	records := tr.Records()
	if len(records) != 2 {
		t.Fatalf("expected 2 records (not truncated), got %d", len(records))
	}
}

// TestRestoreFilesOnly_DeletesCreatedFiles verifies that files created during
// the rewound turns are deleted even with files-only restore.
func TestRestoreFilesOnly_DeletesCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	targetFile := filepath.Join(t.TempDir(), "new.go")

	mustWriteFile(t, targetFile, []byte("new content"))
	if err := tr.RecordBackup(targetFile, nil, 2); err != nil {
		t.Fatal(err)
	}

	restored, err := tr.RestoreFilesOnly(2)
	if err != nil {
		t.Fatalf("RestoreFilesOnly(2): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	if _, err := os.Stat(targetFile); !os.IsNotExist(err) {
		t.Error("file should have been deleted (created during rewound turns)")
	}
	// Records still present
	if len(tr.Records()) != 1 {
		t.Errorf("expected 1 record (not truncated), got %d", len(tr.Records()))
	}
}

// TestRestoreFilesOnly_NoRecordsForTarget does nothing when no records match.
func TestRestoreFilesOnly_NoRecordsForTarget(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	restored, err := tr.RestoreFilesOnly(5)
	if err != nil {
		t.Fatalf("RestoreFilesOnly(5): %v", err)
	}
	if len(restored) != 0 {
		t.Errorf("expected 0 restored, got %d", len(restored))
	}
}

// TestRestoreFilesOnly_SubsequentRestoreToIndexStillWorks verifies that after
// RestoreFilesOnly, a subsequent RestoreToIndex can still use the preserved records.
func TestRestoreFilesOnly_SubsequentRestoreToIndexStillWorks(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	targetFile := filepath.Join(t.TempDir(), "test.go")

	mustWriteFile(t, targetFile, []byte("v0"))
	if err := tr.RecordBackup(targetFile, []byte("v0"), 1); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v1"))
	if err := tr.RecordBackup(targetFile, []byte("v1"), 2); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, targetFile, []byte("v2"))

	// First: RestoreFilesOnly to turn 1 — file = "v0", records intact
	_, err := tr.RestoreFilesOnly(1)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, targetFile, "v0")

	// Re-edit the file to simulate user continuing to edit
	mustWriteFile(t, targetFile, []byte("v0_reedited"))

	// Second: RestoreToIndex to turn 1 — should still work using preserved records
	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex(1): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	assertFileContent(t, targetFile, "v0")
	// Now records should be truncated
	if len(tr.Records()) != 0 {
		t.Errorf("expected 0 records after RestoreToIndex, got %d", len(tr.Records()))
	}
}

// --- HasRecordsAtOrAfter tests ---

// TestHasRecordsAtOrAfter is used by TUI to decide whether to show the
// "Restore code" option in the rewind confirmation dialog.
func TestHasRecordsAtOrAfter(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if tr.HasRecordsAtOrAfter(1) {
		t.Error("expected false with no records")
	}

	if err := tr.RecordBackup("/tmp/a.go", []byte("a"), 2); err != nil {
		t.Fatal(err)
	}
	if err := tr.RecordBackup("/tmp/b.go", []byte("b"), 4); err != nil {
		t.Fatal(err)
	}

	if !tr.HasRecordsAtOrAfter(1) {
		t.Error("turn 1 should match (record at turn 2 >= 1)")
	}
	if !tr.HasRecordsAtOrAfter(2) {
		t.Error("turn 2 should match (record at turn 2)")
	}
	if !tr.HasRecordsAtOrAfter(3) {
		t.Error("turn 3 should match (record at turn 4 >= 3)")
	}
	if !tr.HasRecordsAtOrAfter(4) {
		t.Error("turn 4 should match (record at turn 4)")
	}
	if tr.HasRecordsAtOrAfter(5) {
		t.Error("turn 5 should not match (no record >= 5)")
	}
}

// TestRestoreToIndex_SameFileMultipleEditsSameTurn verifies that when the same
// file is edited multiple times within a single turn (same turnIndex), rewind
// restores the file to the state BEFORE the first edit (original pre-edit content),
// not an intermediate version.
//
// Uses LoadRecords with reversed version order to simulate sort.Slice instability
// (sort.Slice is not stable — for equal keys, element order is undefined).
//
// Issue: restoreFilesLocked sorts only by turnIndex, not by (turnIndex, version).
// When records have the same turnIndex but are in reverse version order,
// the "first" record picked may be the wrong one (highest version instead of lowest).
func TestRestoreToIndex_SameFileMultipleEditsSameTurn(t *testing.T) {
	tmp := t.TempDir()
	backupDir := filepath.Join(tmp, "backups")
	tr := NewTracker(backupDir)
	file := filepath.Join(tmp, "file.go")

	// Content versions
	v1 := []byte("package main // v1\n")
	v2 := []byte("package main // v2\n")
	v3 := []byte("package main // v3\n")
	v4 := []byte("package main // v4\n")

	// Create backup files on disk manually
	hash := fileHash(file)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(backupDir, hash+"@v1"), v1)
	mustWriteFile(t, filepath.Join(backupDir, hash+"@v2"), v2)
	mustWriteFile(t, filepath.Join(backupDir, hash+"@v3"), v3)

	// Load records in REVERSE version order (simulates sort.Slice instability)
	// This is the worst case: the first record for turnIndex=2 has version=3 (v3 content).
	// If restore picks this, it restores v3 instead of v1 — wrong!
	tr.LoadRecords([]BackupRecord{
		{FilePath: file, BackupName: hash + "@v3", Version: 3, TurnIndex: 2},
		{FilePath: file, BackupName: hash + "@v2", Version: 2, TurnIndex: 2},
		{FilePath: file, BackupName: hash + "@v1", Version: 1, TurnIndex: 2},
	})

	// File is currently at v4 (post-all-edits)
	mustWriteFile(t, file, v4)

	// --- Execute: rewind to turn 2 ---
	restored, err := tr.RestoreToIndex(2)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}

	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d: %v", len(restored), restored)
	}

	// --- Verify: must restore v1 (pre-first-edit), NOT v3 (pre-last-edit) ---
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read file after rewind: %v", err)
	}
	if string(data) != string(v1) {
		t.Errorf("ISSUE: file after rewind = %q, want %q (original pre-edit content, not intermediate)", string(data), string(v1))
	}
}

// TestRestoreToIndex_MultipleFilesMultipleEditsSameTurn verifies that when
// multiple files are each edited multiple times within the same turn,
// rewind restores ALL files to their pre-first-edit states.
//
// Scenario: turn 2 edits fileA (v1→v2→v3) and fileB (x→y→z).
// Rewind to turn 2 should restore fileA=v1, fileB=x.
func TestRestoreToIndex_MultipleFilesMultipleEditsSameTurn(t *testing.T) {
	tmp := t.TempDir()
	backupDir := filepath.Join(tmp, "backups")
	tr := NewTracker(backupDir)

	fileA := filepath.Join(tmp, "a.go")
	fileB := filepath.Join(tmp, "b.go")

	// Content versions for fileA
	aV1 := []byte("package a // v1\n")
	aV2 := []byte("package a // v2\n")
	aV3 := []byte("package a // v3\n")

	// Content versions for fileB
	bV1 := []byte("package b // v1\n")
	bV2 := []byte("package b // v2\n")
	bV3 := []byte("package b // v3\n")

	// Create backup files
	hashA := fileHash(fileA)
	hashB := fileHash(fileB)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(backupDir, hashA+"@v1"), aV1)
	mustWriteFile(t, filepath.Join(backupDir, hashA+"@v2"), aV2)
	mustWriteFile(t, filepath.Join(backupDir, hashB+"@v1"), bV1)
	mustWriteFile(t, filepath.Join(backupDir, hashB+"@v2"), bV2)

	// Load records in reverse version order for both files (worst case)
	tr.LoadRecords([]BackupRecord{
		{FilePath: fileA, BackupName: hashA + "@v2", Version: 2, TurnIndex: 2},
		{FilePath: fileA, BackupName: hashA + "@v1", Version: 1, TurnIndex: 2},
		{FilePath: fileB, BackupName: hashB + "@v2", Version: 2, TurnIndex: 2},
		{FilePath: fileB, BackupName: hashB + "@v1", Version: 1, TurnIndex: 2},
	})

	// Current state: both at v3
	mustWriteFile(t, fileA, aV3)
	mustWriteFile(t, fileB, bV3)

	// --- Execute: rewind to turn 2 ---
	restored, err := tr.RestoreToIndex(2)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}

	if len(restored) != 2 {
		t.Fatalf("expected 2 restored files, got %d: %v", len(restored), restored)
	}

	// --- Verify: both files at pre-first-edit state ---
	dataA, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatalf("read fileA: %v", err)
	}
	if string(dataA) != string(aV1) {
		t.Errorf("fileA after rewind = %q, want %q", string(dataA), string(aV1))
	}

	dataB, err := os.ReadFile(fileB)
	if err != nil {
		t.Fatalf("read fileB: %v", err)
	}
	if string(dataB) != string(bV1) {
		t.Errorf("fileB after rewind = %q, want %q", string(dataB), string(bV1))
	}
}
