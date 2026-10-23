package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// bundled.go — the embedded entries feed the same pipeline as on-disk files,
// so path and baseDir must survive verbatim (they become Filename and BaseDir
// on the resolved definition).
// ---------------------------------------------------------------------------

func TestLoadBundledAgentFiles_EntriesCarryPathAndBaseDir(t *testing.T) {
	t.Parallel()
	entries := loadBundledAgentFiles()
	if len(entries) == 0 {
		t.Fatal("no bundled agent entries loaded")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := filepath.Base(e.filePath)
		if !strings.HasSuffix(name, ".md") {
			t.Errorf("filePath = %q, want a bundled markdown path", e.filePath)
			continue
		}
		if e.filePath != filepath.Join("bundled", name) {
			t.Errorf("filePath = %q, want %q", e.filePath, filepath.Join("bundled", name))
		}
		if e.baseDir != "bundled" {
			t.Errorf("%s: baseDir = %q, want %q", name, e.baseDir, "bundled")
		}
		if e.source != types.AgentSourceBuiltIn {
			t.Errorf("%s: source = %q, want %q", name, e.source, types.AgentSourceBuiltIn)
		}
		if e.frontmatter["name"] == nil {
			t.Errorf("%s: frontmatter has no name: %v", name, e.frontmatter)
		}
		if e.content == "" {
			t.Errorf("%s: body content is empty", name)
		}
		names = append(names, strings.TrimSuffix(name, ".md"))
	}
	slices.Sort(names)
	want := []string{"critic", "executor", "planner", "reviewer"}
	if !slices.Equal(names, want) {
		t.Errorf("bundled agents = %v, want %v", names, want)
	}
}

// ---------------------------------------------------------------------------
// tools.go — a globally disallowed tool must not end the scan. Its position in
// the map is Go's choice, so the survivors are asserted as a complete set, and
// the whole filter runs several times because each range picks a fresh start.
// ---------------------------------------------------------------------------

func TestFilterToolsForAgent_DisallowedToolDoesNotEndScan(t *testing.T) {
	orig := AllAgentDisallowedTools
	AllAgentDisallowedTools = map[string]bool{"Bash": true}
	t.Cleanup(func() { AllAgentDisallowedTools = orig })

	names := []string{"Read", "Write", "Edit", "Grep", "Glob", "Bash", "Agent", "Web", "Task", "Lsp", "Skill", "Job"}
	want := []string{"Agent", "Edit", "Glob", "Grep", "Job", "Lsp", "Read", "Skill", "Task", "Web", "Write"}

	for range 5 {
		filtered := FilterToolsForAgent(makeTestTools(names...), &types.AgentDefinition{})

		got := make([]string, 0, len(filtered))
		for name := range filtered {
			got = append(got, name)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("filtered = %v, want %v — only Bash may be dropped", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// AgentRegistry — Get hands out an independent result, and every
// map access is serialised by the registry mutex.
// ---------------------------------------------------------------------------

func TestRegistry_GetResultCopyIsIndependent(t *testing.T) {
	t.Parallel()
	reg := NewAgentRegistry()
	state, err := reg.Spawn(context.Background(),
		func(context.Context, string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "original", TotalTokens: 5}, nil
		}, nil, "get copy", "call_1", "agent")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, ok := reg.Wait(state.ID); !ok {
		t.Fatal("Wait returned false for a known ID")
	}

	first, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false for a known ID")
	}
	if first.Result == nil || first.Result.Content != "original" {
		t.Fatalf("Get result = %+v, want the completed result", first.Result)
	}
	first.Result.Content = "tampered"

	second, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false after the first Get")
	}
	if second.Result == nil || second.Result.Content != "original" {
		t.Errorf("registry result = %+v, want it unaffected by the caller's edit to the first snapshot", second.Result)
	}
}

func TestIsInForkChild_MarkerOnANonTextBlockIsNotAMarker(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "id1", Text: "<fork-boilerplate>quoted marker</fork-boilerplate>"},
		}},
	}
	if IsInForkChild(messages) {
		t.Error("IsInForkChild = true, want false — only text blocks carry the fork marker")
	}
}

// Get, List and CleanupCompleted walk the registry map; if their unlock runs
// before the walk instead of after it, a concurrent Spawn trips the runtime's
// concurrent-map check. Correct code never gets there. The readers run until the
// spawners are done, so the overlap covers the whole Spawn phase instead of
// whatever slice of it a fixed iteration count happens to land on.
func TestRegistry_ConcurrentAccessIsSerialized(t *testing.T) {
	reg := NewAgentRegistry()

	var spawns sync.WaitGroup
	for range 4 {
		spawns.Go(func() {
			for range 800 {
				if _, err := reg.Spawn(context.Background(),
					func(context.Context, string) (*types.SubQueryResult, error) {
						return &types.SubQueryResult{Content: "done"}, nil
					}, nil, "hammer", "", "agent"); err != nil {
					t.Errorf("Spawn: %v", err)
					return
				}
			}
		})
	}

	stop := make(chan struct{})
	var reads sync.WaitGroup
	for range 4 {
		reads.Go(func() {
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				reg.Get("agent-" + strconv.Itoa(j))
				if j%16 == 0 {
					reg.List()
					reg.CleanupCompleted()
				}
			}
		})
	}
	spawns.Wait()
	close(stop)
	reads.Wait()

	for _, st := range reg.List() {
		reg.Wait(st.ID)
	}
	reg.CleanupCompleted()
	if remaining := len(reg.agents); remaining != 0 {
		t.Errorf("registry holds %d agents after cleanup, want 0 — every spawned agent reached a terminal state", remaining)
	}
}
