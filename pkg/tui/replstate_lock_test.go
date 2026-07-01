package tui

import (
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// TestReplState_NoDeadlock_NestedCalls verifies that concurrent reads +
// writes to a ReplState do not deadlock and do not race. The internal call
// graph (PendingTool* → lastMsg/updateToolBlock via *Locked helpers) acquires
// the mutex once per public method, so nested calls must not re-lock.
//
// Run with -race to catch data races; the timeout catches deadlocks.
func TestReplState_NoDeadlock_NestedCalls(t *testing.T) {
	t.Parallel()
	s := NewReplState()

	// Seed an assistant message so PendingTool* has somewhere to write.
	s.StartQuery()
	s.AppendTextItem()

	const duration = 200 * time.Millisecond
	var wg sync.WaitGroup

	// Writer goroutine: hammer public mutators.
	wg.Go(func() {
		deadline := time.Now().Add(duration) // REAL-TIME: deadline loop, not asserted
		i := 0
		for time.Now().Before(deadline) { // REAL-TIME: loop guard, not asserted
			id := "tool-" + string(rune('a'+(i%26)))
			s.PendingToolStarted(id, "Bash", "running", "{\"cmd\":\"ls\"}", tool.SearchReadKind{})
			s.PendingToolDelta(id, "{\"a\":1}", "ls", tool.SearchReadKind{IsSearch: true})
			s.PendingToolOutput(id, "line1\n")
			s.PendingToolDone(id, "done", false, tool.SearchReadKind{IsRead: true})
			s.AppendChunk("delta")
			s.PendingThinkingStarted()
			s.PendingThinkingDelta("thought")
			s.PendingThinkingDone(time.Millisecond)
			s.SetAgentContextWindow(id, 200000)
			s.UpdateToolBlock(id, &ToolCallView{ID: id, Name: "Bash"})
			s.TrimBlocks(&ToolCallView{Blocks: make([]ContentBlock, 60)})
			i++
		}
	})

	// Reader goroutine: hammer public readers (the paths the render +
	// background-drain adapter use).
	wg.Go(func() {
		deadline := time.Now().Add(duration) // REAL-TIME: deadline loop, not asserted
		for time.Now().Before(deadline) {    // REAL-TIME: loop guard, not asserted
			_ = s.IsStreaming()
			_ = s.Messages()
			_ = s.MessagesSnapshot()
			_ = s.LastMsg()
			_ = s.FindToolView("tool-a")
			_ = s.CurrentToolName()
			_ = s.ToolCount()
			_, _ = s.PendingToolStart("tool-a")
		}
	})

	// If a deadlock exists, wg.Wait never returns and the test eventually
	// hits the testing goroutine's deadline (panic from go test).
	wg.Wait()
}
