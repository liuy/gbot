package lsptool

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestStatus_ExactLiveServer(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	res, err := status(testCtx(), reg)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := "Language servers:\n" +
		fmt.Sprintf("  • fakels — ready (root: %s)\n", filepath.Join(dir)) +
		"\n" +
		"'not started' = binary found on PATH, will spawn on first request.\n" +
		"'ready' = server process is live for this cwd.\n" +
		"'root' = the project directory that server is indexing.\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("status =\n%q\nwant\n%q", got, want)
	}
}

func TestStatus_NotStarted(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	res, err := status(testCtx(), reg)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := "Language servers:\n" +
		"  • gopls — not started\n" +
		"\n" +
		"'not started' = binary found on PATH, will spawn on first request.\n" +
		"'ready' = server process is live for this cwd.\n" +
		"'root' = the project directory that server is indexing.\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("status =\n%q\nwant\n%q", got, want)
	}
}

func TestStatus_NoServers(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	res, err := status(testCtx(), reg)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No language servers configured for this project" {
		t.Errorf("status = %q", got)
	}
}

// TestStatus_NotStartedThenReady: the not-started branch must continue to
// later specs, not stop listing.
func TestStatus_NotStartedThenReady(t *testing.T) {
	dir := t.TempDir()
	reg := lsp.NewRegistry(dir)
	// Scanned (never spawned) spec comes first in the list.
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	clientConn, serverConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() { serveFake(t, serverConn, nil, dir) })
	c := lsp.NewTestClient("fakels", clientConn)
	reg.InjectClient("fakels", lsp.ServerSpec{
		Name: "fakels", Language: "Fake", FileExts: []string{".go"}, Command: "fake-lsp",
	}, c)
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close(); wg.Wait() })

	res, err := status(testCtx(), reg)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	if !strings.Contains(got, "  • gopls — not started\n") {
		t.Errorf("missing not-started line:\n%s", got[:100])
	}
	if !strings.Contains(got, fmt.Sprintf("  • fakels — ready (root: %s)\n", dir)) {
		t.Errorf("missing ready line after not-started spec:\n%s", got)
	}
}
