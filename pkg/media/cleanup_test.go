package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestStartCleanupLoop_LogsOnlyWhenItRemoves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureMediaLogs(t)
		root := t.TempDir()
		store, err := NewAt(root)
		if err != nil {
			t.Fatalf("NewAt: %v", err)
		}
		stop := store.StartCleanup(context.Background(), time.Millisecond, time.Hour)
		time.Sleep(5 * time.Millisecond)
		if got := logs.find("media: cleanup removed stale files"); len(got) != 0 {
			t.Errorf("cleanup logged %d removals with nothing stale, want 0", len(got))
		}
		stale := filepath.Join(root, string(CategoryImage), "stale.png")
		if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		old := time.Now().Add(-24 * time.Hour)
		if err := os.Chtimes(stale, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
		recs := logs.find("media: cleanup removed stale files")
		if len(recs) != 1 {
			t.Fatalf("cleanup logged %d removals, want 1", len(recs))
		}
		if n, _ := recs[0].attrs["count"].(int64); n != 1 {
			t.Errorf("count attr = %v, want int64(1)", recs[0].attrs["count"])
		}
		stop()
	})
}
