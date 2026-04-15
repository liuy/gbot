package engine_test

import (
	"log/slog"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

func TestNewBudgetTracker(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(1000, slog.Default())
	if bt == nil {
		t.Fatal("expected non-nil tracker")
	}
	if bt.Remaining() != 1000 {
		t.Errorf("expected 1000 remaining, got %d", bt.Remaining())
	}
}

func TestNewBudgetTracker_NilLogger(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(1000, nil)
	if bt == nil {
		t.Fatal("expected non-nil tracker with nil logger")
	}
}

func TestBudgetTracker_Consume(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(1000, slog.Default())

	bt.Consume(types.Usage{InputTokens: 300, OutputTokens: 100})
	if bt.Remaining() != 600 {
		t.Errorf("expected 600 remaining, got %d", bt.Remaining())
	}

	bt.Consume(types.Usage{InputTokens: 200, OutputTokens: 100})
	if bt.Remaining() != 300 {
		t.Errorf("expected 300 remaining, got %d", bt.Remaining())
	}
}

func TestBudgetTracker_Exhausted(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(100, slog.Default())
	if bt.Exhausted() {
		t.Error("should not be exhausted initially")
	}

	bt.Consume(types.Usage{InputTokens: 50, OutputTokens: 50})
	if !bt.Exhausted() {
		t.Error("should be exhausted after consuming budget")
	}
}

func TestBudgetTracker_NotExhaustedZeroBudget(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(0, slog.Default())
	if bt.Exhausted() {
		t.Error("zero budget should never be exhausted (unlimited)")
	}
	bt.Consume(types.Usage{InputTokens: 99999})
	if bt.Exhausted() {
		t.Error("zero budget should never be exhausted")
	}
}

func TestBudgetTracker_CheckAndWarn(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(100, slog.Default())
	if bt.CheckAndWarn() {
		t.Error("should not warn when not exhausted")
	}

	bt.Consume(types.Usage{InputTokens: 100})
	if !bt.CheckAndWarn() {
		t.Error("should warn when exhausted")
	}
}

func TestBudgetTracker_Usage(t *testing.T) {
	t.Parallel()
	bt := engine.NewBudgetTracker(1000, slog.Default())
	bt.Consume(types.Usage{InputTokens: 300, OutputTokens: 100})
	bt.Consume(types.Usage{InputTokens: 200, OutputTokens: 50})

	usage := bt.Usage()
	// Consume sums InputTokens + OutputTokens into used; Usage() returns used as InputTokens.
	if usage.InputTokens != 650 {
		t.Errorf("expected 650 total used tokens, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("expected OutputTokens to be 0, got %d", usage.OutputTokens)
	}
	if bt.Remaining() != 350 {
		t.Errorf("expected 350 remaining, got %d", bt.Remaining())
	}
}

func TestTrimMessages_NoTrim(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("system")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	}
	result := engine.TrimMessages(msgs, 10)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestTrimMessages_TrimToMax(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("2")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("3")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("4")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("5")}},
	}
	result := engine.TrimMessages(msgs, 3)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	// Verify exact trimming: oldest 2 dropped, last 3 kept.
	if result[0].Content[0].Text != "3" {
		t.Errorf("expected first trimmed message to be '3', got %q", result[0].Content[0].Text)
	}
	if result[1].Content[0].Text != "4" {
		t.Errorf("expected second trimmed message to be '4', got %q", result[1].Content[0].Text)
	}
	if result[2].Content[0].Text != "5" {
		t.Errorf("expected third trimmed message to be '5', got %q", result[2].Content[0].Text)
	}
}

func TestTrimMessages_PreserveSystem(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("system")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("2")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("3")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("4")}},
	}
	result := engine.TrimMessages(msgs, 2)
	if len(result) != 3 { // system + 2 most recent
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Role != types.RoleSystem {
		t.Errorf("expected first message to be system, got %s", result[0].Role)
	}
	if result[0].Content[0].Text != "system" {
		t.Errorf("expected system text 'system', got %q", result[0].Content[0].Text)
	}
	// After system, the 2 most recent messages (3, 4) should be kept.
	if result[1].Content[0].Text != "3" {
		t.Errorf("expected second message text '3', got %q", result[1].Content[0].Text)
	}
	if result[2].Content[0].Text != "4" {
		t.Errorf("expected third message text '4', got %q", result[2].Content[0].Text)
	}
}

func TestTrimMessages_ZeroMax(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("1")}},
	}
	result := engine.TrimMessages(msgs, 0)
	if len(result) != 1 {
		t.Errorf("expected no trimming with max=0, got %d messages", len(result))
	}
}

func TestTrimMessages_Empty(t *testing.T) {
	t.Parallel()
	result := engine.TrimMessages(nil, 5)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}
