package dream

import (
	"context"
	"log/slog"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// MessageLister provides time-filtered message queries.
// *short.Store implements this.
type MessageLister interface {
	MessagesSince(since time.Time) ([]*short.TranscriptMessage, error)
}

// DreamEngine is the subset of *engine.Engine the timer needs. Decoupling via
// an interface makes the timer testable without a real engine.
type DreamEngine interface {
	IsBusy() bool
	RunChunk(ctx context.Context, userMessage string) error
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
	ContextWindow int
	IdleThreshold time.Duration
	Cooldown      time.Duration
	TickInterval  time.Duration
	Logger        *slog.Logger
}

// RunDreamTimer ticks every TickInterval and runs the gate sequence + consolidate
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
// debug log. All gates passing triggers consolidate.
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

	// Gate 4: new messages since last consolidation
	// When lastDream is zero (cold start), MessagesSince(epoch) returns all.
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
	p.consolidate(ctx, msgs)
}

// consolidate chunks the messages and injects each chunk into the dream engine
// sequentially via RunChunk. The watermark is advanced only when ALL chunks
// succeed — partial/total failure leaves it untouched so the next tick
// reprocesses (recall + UNIQUE dedup prevents duplicate facts).
func (p TimerParams) consolidate(ctx context.Context, messages []*short.TranscriptMessage) {
	chunks := chunkByTokens(messages, p.ContextWindow)
	if len(chunks) == 0 {
		return
	}

	successCount := 0
	for i, chunk := range chunks {
		userMsg := "[system] Dream time.\n\n" + formatMessages(chunk, i+1, len(chunks))
		chunkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := p.Engine.RunChunk(chunkCtx, userMsg)
		cancel()
		if err != nil {
			p.Logger.Warn("dream: chunk failed", "chunk", i+1, "of", len(chunks), "error", err)
			continue
		}
		successCount++
	}

	if successCount == len(chunks) {
		if err := WriteWatermark(p.MemoryDir); err != nil {
			p.Logger.Warn("dream: WriteWatermark failed", "error", err)
		}
	} else {
		p.Logger.Warn("dream: partial failure, watermark not advanced",
			"success", successCount, "total", len(chunks))
	}
}
