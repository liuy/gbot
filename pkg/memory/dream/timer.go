package dream

import (
	"context"
	"log/slog"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// MessageLister provides time-filtered message queries for the gate check.
// Only the count matters — messages are not injected into the dream agent.
// *short.Store implements this.
type MessageLister interface {
	MessagesSince(since time.Time) ([]*short.TranscriptMessage, error)
}

// DreamEngine is the subset of *engine.Engine the timer needs.
type DreamEngine interface {
	IsBusy() bool
	Query(ctx context.Context, prompt string) error
}

// IdleQuerier returns the timestamp of the last main-thread assistant message.
// *short.Store implements this.
type IdleQuerier interface {
	LastAssistantTime() (time.Time, error)
}

// TimerParams configures a dream timer goroutine.
type TimerParams struct {
	Engine        DreamEngine
	Store         MessageLister
	IdleQuerier   IdleQuerier
	MemoryDir     string
	IdleThreshold time.Duration
	Cooldown      time.Duration
	TickInterval  time.Duration
	Logger        *slog.Logger
}

// RunDreamTimer ticks every TickInterval and runs the gate sequence + Query
// on each tick. Blocks until ctx is cancelled.
func RunDreamTimer(ctx context.Context, p TimerParams) {
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	ticker := time.NewTicker(p.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

// tick runs the 5-gate sequence. Any gate failure is a silent skip with a
// debug log. All gates passing triggers a single self-driven Query — the
// agent uses its own tools (ls, Read, Recall, Write/Edit) to consolidate.
func (p TimerParams) tick(ctx context.Context) {
	// Gate 1: dream engine busy — user is interacting with it
	if p.Engine.IsBusy() {
		p.Logger.Debug("dream: skip — engine busy")
		return
	}

	// Gate 2: user must be idle past IdleThreshold
	lastAssistant, err := p.IdleQuerier.LastAssistantTime()
	if err != nil {
		p.Logger.Warn("dream: LastAssistantTime failed", "error", err)
		return
	}
	if lastAssistant.IsZero() {
		p.Logger.Debug("dream: skip — no assistant message yet")
		return
	}
	idleDuration := time.Since(lastAssistant)
	if idleDuration < p.IdleThreshold {
		p.Logger.Debug("dream: skip — user not idle",
			"idle", idleDuration.Round(time.Second),
			"need", p.IdleThreshold)
		return
	}

	// Gate 3: cooldown since last consolidation
	lastDream, err := ReadWatermark(p.MemoryDir)
	if err != nil {
		p.Logger.Warn("dream: ReadWatermark failed", "error", err)
		return
	}
	if !lastDream.IsZero() && time.Since(lastDream) < p.Cooldown {
		p.Logger.Debug("dream: skip — cooldown",
			"remaining", (p.Cooldown - time.Since(lastDream)).Round(time.Second))
		return
	}

	// Gate 4: new messages since last consolidation.
	// Only the count matters — the agent gathers its own context via Recall.
	msgs, err := p.Store.MessagesSince(lastDream)
	if err != nil {
		p.Logger.Warn("dream: MessagesSince failed", "error", err)
		return
	}
	if len(msgs) == 0 {
		p.Logger.Debug("dream: skip — no new messages")
		return
	}

	// Gate 5: context already cancelled
	if ctx.Err() != nil {
		return
	}

	p.Logger.Info("dream: firing",
		"idle", idleDuration.Round(time.Second),
		"messages", len(msgs))

	prompt := TriggerMessage(p.MemoryDir, lastDream)

	if err := p.Engine.Query(ctx, prompt); err != nil {
		p.Logger.Warn("dream: query failed", "error", err)
		return
	}

	if err := WriteWatermark(p.MemoryDir); err != nil {
		p.Logger.Warn("dream: WriteWatermark failed", "error", err)
	}

	p.Logger.Info("dream: completed")
}
