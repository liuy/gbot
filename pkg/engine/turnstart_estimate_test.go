package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// turnSnapshot records what the engine held when it emitted a turn_start. The
// derived numbers are computed on the emission goroutine: a later response
// re-fits the ratios and learns the context overhead, so post-query math would
// describe a different request than the one being estimated.
type turnSnapshot struct {
	agent    *types.AgentMeta
	estimate int
	// fullWan is the unanchored cost of exactly these messages: calibrated
	// content estimate plus the request-shape overhead.
	fullWan int
	// tailWan and calibOut cost the messages that landed after the usage
	// anchor (the tool result), and the tool output alone.
	tailWan  int
	calibOut int
	plain    int
	msgs     []types.Message
}

// snapshotDispatcher captures one turnSnapshot per turn_start event.
type snapshotDispatcher struct {
	eng *Engine
	// quotedOutput is the tool result as the transcript stores it: the tool's
	// data marshaled to JSON, which is what the estimator costs.
	quotedOutput string
	mu           sync.Mutex
	snaps        []turnSnapshot
}

func (d *snapshotDispatcher) Dispatch(evt types.QueryEvent) {
	if evt.Type != types.EventTurnStart {
		return
	}
	msgs := d.eng.Messages()
	est := d.eng.calibratedEstimator()
	snap := turnSnapshot{
		agent:    evt.Agent,
		estimate: evt.InputEstimate,
		fullWan:  estimateMessagesTokensWith(msgs, defaultMessageEnvelopeTokens, est) + d.eng.ContextOverheadTokens(),
		plain:    EstimateMessagesTokens(msgs) + d.eng.ContextOverheadTokens(),
		msgs:     msgs,
	}
	if len(msgs) > 2 {
		snap.tailWan = estimateMessagesTokensWith(msgs[2:], defaultMessageEnvelopeTokens, est)
		snap.calibOut = est(d.quotedOutput) + defaultMessageEnvelopeTokens
	}
	d.mu.Lock()
	d.snaps = append(d.snaps, snap)
	d.mu.Unlock()
}

func TestTurnStart_CarriesCalibratedInputEstimate(t *testing.T) {
	t.Parallel()

	const (
		userText    = "请分析这段中文文本的 token 估算口径"
		toolOutput  = "工具返回的中文结果，用于验证逐 turn 的估算口径"
		billedInput = 1000
		billedOut   = 20
	)

	mp := &mockProvider{}
	mp.addResponse([]llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test-model", Usage: types.Usage{InputTokens: billedInput}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "probe"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: billedOut}},
		{Type: "message_stop"},
	}, nil)
	mp.addResponse(textStreamEvents("test-model", "完成"), nil)

	mt := &mockTool{name: "probe", enabled: true, callFn: func(
		_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext,
	) (*tool.ToolResult, error) {
		return &tool.ToolResult{Data: toolOutput}, nil
	}}

	// The transcript stores a string tool result as a text block carrying its
	// JSON form, so that quoted form — not the bare text — is what the
	// estimator costs.
	quoted, qerr := json.Marshal(toolOutput)
	if qerr != nil {
		t.Fatalf("marshal tool output: %v", qerr)
	}

	disp := &snapshotDispatcher{quotedOutput: string(quoted)}
	eng := New(&Params{
		Provider:      mp,
		Dispatcher:    disp,
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{mt.Name(): mt} },
		Model:         "test-model",
		Logger:        slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	disp.eng = eng

	// Fit the model with a CJK ratio the seed table does not carry, so the
	// wire estimate is only reproducible from the calibrated closure. The
	// seeded observations are collinear on purpose: the 6×6 normal equations
	// stay singular, so a later refit keeps these ratios instead of inventing
	// a solution mid-query.
	eng.calibMu.Lock()
	eng.calibrations[calibrationKey(mp, "test-model")] = &tokenCalibration{
		obs:    []calibObservation{{f: utils.TokenFeatures{CJK: 10}, real: 7}, {f: utils.TokenFeatures{CJK: 10}, real: 7}, {f: utils.TokenFeatures{CJK: 10}, real: 7}},
		R:      [6]float64{1.4, 1.38, 1.34, -0.04, 0.48, 0.11},
		fitted: true,
	}
	eng.calibMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if res := eng.QuerySync(ctx, userText, ""); res.Error != nil {
		t.Fatalf("query failed: %v", res.Error)
	}

	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.snaps) != 2 {
		t.Fatalf("turn_start count = %d, want 2 (tool turn + final turn)", len(disp.snaps))
	}
	for i, s := range disp.snaps {
		if s.agent != nil {
			t.Errorf("turn_start[%d].Agent = %+v, want nil for the main engine", i, s.agent)
		}
	}

	// Turn 1: history is the prompt alone and no message carries usage yet, so
	// the estimate is the calibrated cost of exactly those messages.
	first := disp.snaps[0]
	if got := len(first.msgs); got != 1 {
		t.Fatalf("turn 1 history length = %d, want 1 (the user prompt only)", got)
	}
	if first.estimate != first.fullWan {
		t.Errorf("turn 1 estimate = %d, want %d (calibrated estimate of the messages being sent)", first.estimate, first.fullWan)
	}
	if first.estimate <= 0 {
		t.Fatalf("turn 1 estimate = %d, want > 0", first.estimate)
	}
	// CJK text is where the calibrated and plain-ratio estimators part company:
	// equality would mean the wire stopped riding the calibrated source.
	if first.plain == first.fullWan {
		t.Fatalf("calibrated and uncalibrated estimates agree (%d) — CJK fixture no longer separates them", first.plain)
	}
	if first.estimate == first.plain {
		t.Errorf("turn 1 estimate = %d, want the calibrated %d rather than the uncalibrated %d", first.estimate, first.fullWan, first.plain)
	}

	// Turn 2: the anchor is turn 1's reply, so the estimate must be its billed
	// total plus the calibrated cost of the tool result that landed after it —
	// an estimate of the previous turn's messages could not match.
	second := disp.snaps[1]
	if got := len(second.msgs); got != 3 {
		t.Fatalf("turn 2 history length = %d, want 3 (prompt, reply, tool result)", got)
	}
	wantSecond := billedInput + billedOut + second.tailWan
	if second.estimate != wantSecond {
		t.Errorf("turn 2 estimate = %d, want %d (billed %d + tool-result tail %d)",
			second.estimate, wantSecond, billedInput+billedOut, second.tailWan)
	}
	if second.tailWan != second.calibOut {
		t.Errorf("tool-result tail estimate = %d, want %d (calibrated output + envelope)", second.tailWan, second.calibOut)
	}
	if second.estimate <= first.estimate {
		t.Errorf("turn 2 estimate = %d, want > turn 1 %d (the tool result grew the request)", second.estimate, first.estimate)
	}
}
