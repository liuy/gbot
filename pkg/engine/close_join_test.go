package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// Regression for the Close() lie: Close returned while query goroutines were
// still running, and the endQuery tail could kick attachment processing with a
// cancel nobody owned (observed in production paths: session cleanup and
// reload close engines mid-process; surfaced in tests as cross-test DATA RACE
// on time.Local under -shuffle).

// funcProvider adapts closures to llm.Provider so the linger sleep can live
// lexically inside the synctest bubble (the scanner's bubble exemption is
// lexical; a package-level method would sit outside it).
type funcProvider struct {
	name     string
	stream   func(context.Context, *llm.Request) (<-chan llm.StreamEvent, error)
	complete func(context.Context, *llm.Request) (*llm.Response, error)
}

func (p *funcProvider) Name() string { return p.name }

func (p *funcProvider) Stream(ctx context.Context, r *llm.Request) (<-chan llm.StreamEvent, error) {
	return p.stream(ctx, r)
}

func (p *funcProvider) Complete(ctx context.Context, r *llm.Request) (*llm.Response, error) {
	return p.complete(ctx, r)
}

// The provider stays inside Stream after ctx cancellation, so a Close that
// does not join deterministically returns while the goroutine is alive:
// inside a synctest bubble the linger is virtual — the goroutine only
// resumes when every other goroutine blocks, i.e. exactly when a joining
// Close waits.
func TestClose_JoinsQueryGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		eventCh := make(chan types.QueryEvent, 100)
		started := make(chan struct{})
		mp := &funcProvider{
			name: "slow",
			stream: func(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
				ch := make(chan llm.StreamEvent, 1)
				go func() {
					defer close(ch)
					close(started)
					<-ctx.Done()
					time.Sleep(150 * time.Millisecond)
				}()
				return ch, nil
			},
			complete: func(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		eng := New(&Params{
			Provider:   mp,
			Model:      "test",
			Dispatcher: &chanDispatcher{ch: eventCh},
			WorkingDir: t.TempDir(),
		})
		eng.Query(context.Background(), "hi", "")
		<-started
		eng.Close()
		// Close returned — the cancelled goroutine (still inside Stream's
		// linger at Close time) must have been joined, not abandoned.
		if eng.queryActive.Load() != 0 {
			t.Error("queryActive != 0 after Close returned — query goroutine outlived Close")
		}
	})
}

func TestClose_RefusesWorkAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		eventCh := make(chan types.QueryEvent, 100)
		mp := &mockProvider{}
		mp.addResponse(textStreamEvents("test-model", "late"), nil)
		eng := New(&Params{
			Provider:   mp,
			Model:      "test",
			Dispatcher: &chanDispatcher{ch: eventCh},
			WorkingDir: t.TempDir(),
		})
		eng.Close()
		eng.Query(context.Background(), "hi", "")
		eng.QueryWithContent(context.Background(), []types.ContentBlock{types.NewTextBlock("hi")}, "")
		eng.RunSkill(context.Background(), "nope", "", "")
		// Non-empty queue: if goRun's closing guard were removed, the spawn
		// would drain this attachment and hit the provider.
		eng.EnqueueAttachment(types.QueuedItem{Value: "x", Mode: types.ItemModePrompt, Timestamp: time.Now()})
		eng.ProcessAttachments(context.Background(), "")
		// Let every goroutine the rejected calls could have spawned run to
		// their blocking points; a pre-fix spawn would have hit the provider.
		synctest.Wait()
		if eng.queryActive.Load() != 0 {
			t.Error("closed engine accepted a query (queryActive != 0)")
		}
		if res := eng.QuerySync(context.Background(), "hi", ""); res.Error == nil {
			t.Error("QuerySync on a closed engine must return an error, got a result")
		}
		if res := eng.QuerySyncWithContent(context.Background(), []types.ContentBlock{types.NewTextBlock("hi")}, ""); res.Error == nil {
			t.Error("QuerySyncWithContent on a closed engine must return an error, got a result")
		}
		if res := eng.RunForkedQuery(context.Background(), []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}}, ""); res.Error == nil {
			t.Error("RunForkedQuery on a closed engine must return an error, got a result")
		}
		if mp.callCount() != 0 {
			t.Errorf("closed engine called the provider %d times, want 0", mp.callCount())
		}
		// The async rejections must be observable: a QueryEnd carrying the
		// error, so a WS/REPL client waiting for completion is not left
		// hanging (Query, QueryWithContent, RunSkill — one each).
		for i := range 3 {
			select {
			case evt := <-eventCh:
				if evt.Type != types.EventQueryEnd || evt.Error == nil || !strings.Contains(evt.Error.Error(), "closed engine") {
					t.Errorf("closed engine emitted %v (err=%v), want QueryEnd with a closed-engine error", evt.Type, evt.Error)
				}
			default:
				t.Errorf("closed engine gave rejection %d/3 no observable rejection event", i+1)
			}
		}
	})
}

// TestClose_JoinTimeout covers the bounded-join branch: a goroutine that
// ignores cancellation must not hang Close; it returns at the cap with the
// work still outstanding.
func TestClose_JoinTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		eventCh := make(chan types.QueryEvent, 100)
		started := make(chan struct{})
		release := make(chan struct{})
		mp := &funcProvider{
			name: "stuck",
			stream: func(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
				ch := make(chan llm.StreamEvent, 1)
				go func() {
					defer close(ch)
					close(started)
					<-release // ignores ctx entirely
				}()
				return ch, nil
			},
			complete: func(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		var logs bytes.Buffer
		eng := New(&Params{
			Provider:   mp,
			Model:      "test",
			Dispatcher: &chanDispatcher{ch: eventCh},
			WorkingDir: t.TempDir(),
			Logger:     slog.New(slog.NewTextHandler(&logs, nil)),
		})
		eng.Query(context.Background(), "hi", "")
		<-started
		t0 := time.Now()
		eng.Close() // virtual clock advances to the cap; Close must return
		// Pinned against the cap constant's value (hardcoded on purpose: a
		// mutated closeJoinTimeout — either direction — turns this red).
		if elapsed := time.Since(t0); elapsed != 5*time.Second {
			t.Errorf("Close returned after %v, want exactly the 5s join cap", elapsed)
		}
		if eng.queryActive.Load() == 0 {
			t.Error("join timeout branch did not run: work was joined despite ignoring cancellation")
		}
		if !strings.Contains(logs.String(), "close_join_timeout") {
			t.Error("join timeout must log engine:close_join_timeout")
		}
		close(release) // let the stuck goroutine finish before the bubble ends
	})
}

func TestClose_RefusesAttachmentKick(t *testing.T) {
	eng := New(&Params{
		Provider:   &mockProvider{},
		Model:      "test",
		WorkingDir: t.TempDir(),
	})
	eng.Close()
	// The leak path: endQuery of a cancelled query calls
	// startProcessAttachmentsIfIdle → beginQueryIfIdle. After Close this must
	// refuse, or the kick runs a full turn with an unowned cancel.
	if _, ok := eng.beginQueryIfIdle(func() {}); ok {
		t.Error("beginQueryIfIdle accepted work after Close — post-close kick can spawn")
	}
}
