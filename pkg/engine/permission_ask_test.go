package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// Test-only content checkers for checkContentPermissions tests.
func init() {
	permission.RegisterContentChecker("Bash", func(input json.RawMessage, contentRules []permission.Rule) permission.RuleAction {
		action, _, _ := permission.CheckBashPermission(permission.ExtractBashCommand(input), contentRules)
		return action
	})
	permission.RegisterContentChecker("Write", func(input json.RawMessage, contentRules []permission.Rule) permission.RuleAction {
		action, _, _ := permission.CheckFilePermission(permission.ExtractFilePath(input), contentRules)
		return action
	})
	permission.RegisterContentChecker("Edit", func(input json.RawMessage, contentRules []permission.Rule) permission.RuleAction {
		action, _, _ := permission.CheckFilePermission(permission.ExtractFilePath(input), contentRules)
		return action
	})
}

// ---------------------------------------------------------------------------
// askUser() tests
// ---------------------------------------------------------------------------

func TestAskUser_SubEngineDeniesImmediately(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)
	executor.SetSubEngine(true)

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{}`),
		Status: StatusQueued,
	}

	decision := executor.askUser(tt, permission.Decision{
		Action:  permission.ActionAsk,
		Message: "test ask",
	}, "")

	if decision != types.UserDecisionDeny {
		t.Errorf("sub-engine should deny immediately, got %v", decision)
	}
}

func TestAskUser_SessionAllowedCacheHit(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)
	executor.sessionAllowed = map[string]bool{
		"TestTool": true,
	}

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{}`),
		Status: StatusQueued,
	}

	decision := executor.askUser(tt, permission.Decision{
		Action:  permission.ActionAsk,
		Message: "test ask",
	}, "")

	if decision != types.UserDecisionAllow {
		t.Errorf("cached allow should return UserDecisionAllow, got %v", decision)
	}
}

func TestAskUser_SessionAllowedCacheHitWithContent(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)
	executor.sessionAllowed = map[string]bool{
		"TestTool:rm -rf *": true,
	}

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{"command":"rm -rf *"}`),
		Status: StatusQueued,
	}

	decision := executor.askUser(tt, permission.Decision{
		Action:  permission.ActionAsk,
		Message: "test ask",
	}, "rm -rf *")

	if decision != types.UserDecisionAllow {
		t.Errorf("cached allow with content should return UserDecisionAllow, got %v", decision)
	}
}

func TestAskUser_NormalFlowAllows(t *testing.T) {
	eventCh := make(chan types.QueryEvent, 1)
	emitEvent := func(evt types.QueryEvent) {
		eventCh <- evt
	}

	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		emitEvent,
		context.Background(),
	)

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{"arg":"value"}`),
		Status: StatusQueued,
	}

	resultCh := make(chan types.PermissionUserDecision, 1)
	go func() {
		decision := executor.askUser(tt, permission.Decision{
			Action:  permission.ActionAsk,
			Message: "please allow",
			Rule: &permission.Rule{
				Value: permission.RuleValue{
					ToolName:    "TestTool",
					RuleContent: nil,
				},
				Action: permission.ActionAsk,
				Source: "user",
			},
		}, "")
		resultCh <- decision
	}()

	// Wait for EventPermissionAsk
	var capturedEvent types.QueryEvent
	select {
	case capturedEvent = <-eventCh:
		if capturedEvent.PermissionAsk == nil {
			t.Fatal("PermissionAsk field is nil")
		}
		if capturedEvent.PermissionAsk.ToolName != "TestTool" {
			t.Errorf("ToolName: got %q, want 'TestTool'", capturedEvent.PermissionAsk.ToolName)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for EventPermissionAsk")
	}

	// Send decision
	capturedEvent.PermissionAsk.ResponseCh <- types.UserDecisionAllow

	// Wait for result
	select {
	case decision := <-resultCh:
		if decision != types.UserDecisionAllow {
			t.Errorf("expected UserDecisionAllow, got %v", decision)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for askUser result")
	}
}

func TestAskUser_RootCtxCancellationDenies(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())

	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		rootCtx,
	)

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{}`),
		Status: StatusQueued,
	}

	resultCh := make(chan types.PermissionUserDecision, 1)
	go func() {
		decision := executor.askUser(tt, permission.Decision{
			Action:  permission.ActionAsk,
			Message: "test",
		}, "")
		resultCh <- decision
	}()

	cancel()

	decision := <-resultCh
	if decision != types.UserDecisionDeny {
		t.Errorf("cancelled rootCtx should deny, got %v", decision)
	}
}

func TestAskUser_SiblingCtxCancellationDenies(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	siblingCtx, siblingCancel := context.WithCancelCause(rootCtx)

	executor := &StreamingToolExecutor{
		tools:         make([]*TrackedTool, 0),
		toolMap:       map[string]tool.Tool{},
		emitEvent:     func(evt types.QueryEvent) {},
		tctx:          nil,
		rootCtx:       rootCtx,
		siblingCtx:    siblingCtx,
		siblingCancel: siblingCancel,
	}

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{}`),
		Status: StatusQueued,
	}

	resultCh := make(chan types.PermissionUserDecision, 1)
	go func() {
		decision := executor.askUser(tt, permission.Decision{
			Action:  permission.ActionAsk,
			Message: "test",
		}, "")
		resultCh <- decision
	}()

	siblingCancel(context.Canceled)

	decision := <-resultCh
	if decision != types.UserDecisionDeny {
		t.Errorf("cancelled siblingCtx should deny, got %v", decision)
	}
	rootCancel()
}

func TestAskUser_ChannelClosedDenies(t *testing.T) {
	emitEvent := func(evt types.QueryEvent) {
		if evt.PermissionAsk != nil && evt.PermissionAsk.ResponseCh != nil {
			close(evt.PermissionAsk.ResponseCh)
		}
	}

	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		emitEvent,
		context.Background(),
	)

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{}`),
		Status: StatusQueued,
	}

	decision := executor.askUser(tt, permission.Decision{
		Action:  permission.ActionAsk,
		Message: "test",
	}, "")

	if decision != types.UserDecisionDeny {
		t.Errorf("closed channel should deny, got %v", decision)
	}
}

func TestAskUser_RuleDetailWithRuleContent(t *testing.T) {
	eventCh := make(chan types.QueryEvent, 1)
	emitEvent := func(evt types.QueryEvent) {
		eventCh <- evt
	}

	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		emitEvent,
		context.Background(),
	)

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "Bash",
		Input:  json.RawMessage(`{"command":"rm -rf /"}`),
		Status: StatusQueued,
	}

	resultCh := make(chan types.PermissionUserDecision, 1)
	go func() {
		decision := executor.askUser(tt, permission.Decision{
			Action:  permission.ActionAsk,
			Message: "dangerous",
			Rule: &permission.Rule{
				Value: permission.RuleValue{
					ToolName:    "Bash",
					RuleContent: new("rm -rf *"),
				},
				Action: permission.ActionAsk,
				Source: "project",
			},
		}, "")
		resultCh <- decision
	}()

	var capturedEvent types.QueryEvent
	select {
	case capturedEvent = <-eventCh:
		if capturedEvent.PermissionAsk == nil {
			t.Fatal("PermissionAsk is nil")
		}
		want := "Bash(rm -rf *) from project settings"
		if capturedEvent.PermissionAsk.RuleDetail != want {
			t.Errorf("RuleDetail: got %q, want %q", capturedEvent.PermissionAsk.RuleDetail, want)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for EventPermissionAsk")
	}

	capturedEvent.PermissionAsk.ResponseCh <- types.UserDecisionAllow
	<-resultCh
}

func TestAskUser_RootCtxDeniesWithIndependentSibling(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	siblingCtx, siblingCancel := context.WithCancelCause(context.Background())

	executor := &StreamingToolExecutor{
		tools:         make([]*TrackedTool, 0),
		toolMap:       map[string]tool.Tool{},
		emitEvent:     func(evt types.QueryEvent) {},
		tctx:          nil,
		rootCtx:       rootCtx,
		siblingCtx:    siblingCtx,
		siblingCancel: siblingCancel,
	}

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  json.RawMessage(`{}`),
		Status: StatusQueued,
	}

	resultCh := make(chan types.PermissionUserDecision, 1)
	go func() {
		decision := executor.askUser(tt, permission.Decision{
			Action:  permission.ActionAsk,
			Message: "test",
		}, "")
		resultCh <- decision
	}()

	rootCancel()

	decision := <-resultCh
	if decision != types.UserDecisionDeny {
		t.Errorf("cancelled rootCtx should deny, got %v", decision)
	}
	siblingCancel(context.Canceled)
}

// ---------------------------------------------------------------------------
// checkContentPermissions() tests
// ---------------------------------------------------------------------------

func TestCheckContentPermissions_DenyRule(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	contentRules := []permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("rm -rf *"),
			},
			Action: permission.ActionDeny,
			Source: "user",
		},
	}

	input := json.RawMessage(`{"command":"rm -rf /important"}`)
	action, _ := executor.checkContentPermissions("Bash", input, contentRules)

	if action != permission.ActionDeny {
		t.Errorf("expected ActionDeny, got %v", action)
	}
}

func TestCheckContentPermissions_AskRule(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	contentRules := []permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("rm *"),
			},
			Action: permission.ActionAsk,
			Source: "project",
		},
	}

	input := json.RawMessage(`{"command":"rm /tmp/test"}`)
	action, pattern := executor.checkContentPermissions("Bash", input, contentRules)

	if action != permission.ActionAsk {
		t.Errorf("expected ActionAsk, got %v", action)
	}
	if pattern == "" {
		t.Fatal("ask rule should return non-empty pattern")
	}
	if pattern != "Bash(rm *)" {
		t.Errorf("pattern should be 'Bash(rm *)', got %q", pattern)
	}
}

func TestCheckContentPermissions_NoMatch(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	contentRules := []permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("rm -rf *"),
			},
			Action: permission.ActionDeny,
			Source: "user",
		},
	}

	input := json.RawMessage(`{"command":"echo hello"}`)
	action, pattern := executor.checkContentPermissions("Bash", input, contentRules)

	if action != permission.ActionAllow {
		t.Errorf("expected ActionAllow when no match, got %v", action)
	}
	if pattern != "" {
		t.Errorf("no match should return empty pattern, got %q", pattern)
	}
}

func TestCheckContentPermissions_MultipleRules(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	contentRules := []permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("rm *"),
			},
			Action: permission.ActionAsk,
			Source: "project",
		},
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("sudo rm *"),
			},
			Action: permission.ActionDeny,
			Source: "user",
		},
	}

	input := json.RawMessage(`{"command":"sudo rm -rf /important"}`)
	action, _ := executor.checkContentPermissions("Bash", input, contentRules)

	if action != permission.ActionDeny {
		t.Errorf("deny rule should take priority, got %v", action)
	}
}

// ---------------------------------------------------------------------------
// SetSubEngine() tests
// ---------------------------------------------------------------------------

func TestSetSubEngine_MarksExecutorAsSubEngine(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	if executor.isSubEngine {
		t.Error("new executor should not be sub-engine by default")
	}

	executor.SetSubEngine(true)

	if !executor.isSubEngine {
		t.Error("SetSubEngine(true) should set isSubEngine flag")
	}

	executor.SetSubEngine(false)

	if executor.isSubEngine {
		t.Error("SetSubEngine(false) should clear isSubEngine flag")
	}
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestAskUser_Integration(t *testing.T) {
	permChecker := permission.NewChecker([]permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "AskTool",
				RuleContent: nil,
			},
			Action: permission.ActionAsk,
			Source: "user",
		},
	})

	askCh := make(chan *types.PermissionAskEvent, 1)
	emitEvent := func(evt types.QueryEvent) {
		if evt.Type == types.EventPermissionAsk && evt.PermissionAsk != nil {
			askCh <- evt.PermissionAsk
		}
	}

	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		emitEvent,
		context.Background(),
	)
	executor.SetPermissionChecker(permChecker)

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "AskTool",
		Input:  []byte(`{}`),
		Status: StatusQueued,
	}

	resultCh := make(chan types.PermissionUserDecision, 1)

	go func() {
		decision := executor.askUser(tt, permission.Decision{
			Action:  permission.ActionAsk,
			Message: "AskTool requires permission",
			Rule: &permission.Rule{
				Value: permission.RuleValue{
					ToolName: "AskTool",
				},
				Action: permission.ActionAsk,
				Source: "user",
			},
		}, "")
		resultCh <- decision
	}()

	var capturedAsk *types.PermissionAskEvent
	select {
	case capturedAsk = <-askCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected EventPermissionAsk to be emitted within 2s")
	}

	if capturedAsk.ToolName != "AskTool" {
		t.Errorf("ToolName: got %q, want 'AskTool'", capturedAsk.ToolName)
	}

	capturedAsk.ResponseCh <- types.UserDecisionAllow

	select {
	case decision := <-resultCh:
		if decision != types.UserDecisionAllow {
			t.Errorf("expected UserDecisionAllow, got %v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for decision")
	}
}

func TestCheckContentPermissions_Integration(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	denyRules := []permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("rm -rf *"),
			},
			Action: permission.ActionDeny,
			Source: "user",
		},
	}

	input := []byte(`{"command":"rm -rf /tmp"}`)
	action, pattern := executor.checkContentPermissions("Bash", input, denyRules)

	if action != permission.ActionDeny {
		t.Errorf("deny rule: expected ActionDeny, got %v", action)
	}
	if pattern != "" {
		t.Errorf("deny rule: expected empty pattern, got %q", pattern)
	}

	askRules := []permission.Rule{
		{
			Value: permission.RuleValue{
				ToolName:    "Bash",
				RuleContent: new("rm *"),
			},
			Action: permission.ActionAsk,
			Source: "project",
		},
	}

	input = []byte(`{"command":"rm /tmp/test"}`)
	action, pattern = executor.checkContentPermissions("Bash", input, askRules)

	if action != permission.ActionAsk {
		t.Errorf("ask rule: expected ActionAsk, got %v", action)
	}
	if pattern == "" {
		t.Error("ask rule: expected non-empty pattern")
	}

	input = []byte(`{"command":"echo hello"}`)
	action, pattern = executor.checkContentPermissions("Bash", input, denyRules)

	if action != permission.ActionAllow {
		t.Errorf("no match: expected ActionAllow, got %v", action)
	}
	if pattern != "" {
		t.Errorf("no match: expected empty pattern, got %q", pattern)
	}
}

func TestSetSubEngine_Integration(t *testing.T) {
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)

	if executor.isSubEngine {
		t.Error("new executor should not be sub-engine by default")
	}

	executor.SetSubEngine(true)
	if !executor.isSubEngine {
		t.Error("SetSubEngine(true) should set isSubEngine flag")
	}

	tt := &TrackedTool{
		ID:     "tu_1",
		Name:   "TestTool",
		Input:  []byte(`{}`),
		Status: StatusQueued,
	}

	decision := executor.askUser(tt, permission.Decision{
		Action:  permission.ActionAsk,
		Message: "test",
	}, "")

	if decision != types.UserDecisionDeny {
		t.Errorf("sub-engine should deny immediately, got %v", decision)
	}

	executor.SetSubEngine(false)
	if executor.isSubEngine {
		t.Error("SetSubEngine(false) should clear isSubEngine flag")
	}
}

// ---------------------------------------------------------------------------
// Regression: json.RawMessage with literal \n breaks Marshal
// ---------------------------------------------------------------------------

func TestPermissionDeny_ResultBlock_MarshalsWithNewline(t *testing.T) {
	// RED: executeTool deny path produces result blocks that must marshal.
	// Bug: []byte(errMsg) with literal \n fails json.Marshal.
	permChecker := permission.NewChecker([]permission.Rule{{
		Value:  permission.RuleValue{ToolName: "TestTool"},
		Action: permission.ActionDeny,
		Source: "user",
	}})
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{"TestTool": &denyTestTool{}},
		nil,
		func(evt types.QueryEvent) {},
		context.Background(),
	)
	executor.SetPermissionChecker(permChecker)

	result := executor.ExecuteAll([]types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "TestTool", Input: json.RawMessage(`{}`)},
	})
	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result.ToolResultBlocks))
	}
	_, err := json.Marshal(result.ToolResultBlocks)
	if err != nil {
		t.Errorf("result block with newline message failed to marshal: %v", err)
		// Verify rule-based deny message contains TS-aligned text
		var msg string
		if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &msg); err != nil {
			t.Fatalf("unmarshal content: %v", err)
		}
		if !strings.Contains(msg, "Permission to use TestTool has been denied") {
			t.Errorf("rule deny message should mention tool name, got: %q", msg)
		}
		if !strings.Contains(msg, "IMPORTANT") {
			t.Errorf("rule deny message should contain workaround guidance, got: %q", msg)
		}
	}
}


func TestPermissionDeny_UserRejectMessage(t *testing.T) {
	// Ask rule -> user denies -> message should be user-reject, not rule-deny.
	permChecker := permission.NewChecker([]permission.Rule{{
		Value:  permission.RuleValue{ToolName: "AskTool"},
		Action: permission.ActionAsk,
		Source: "user",
	}})

	askCh := make(chan *types.PermissionAskEvent, 1)
	executor := NewStreamingToolExecutor(
		map[string]tool.Tool{"AskTool": &denyTestTool{}},
		nil,
		func(evt types.QueryEvent) {
			if evt.Type == types.EventPermissionAsk && evt.PermissionAsk != nil {
				askCh <- evt.PermissionAsk
			}
		},
		context.Background(),
	)
	executor.SetPermissionChecker(permChecker)

	resultCh := make(chan *ExecuteAllResult, 1)
	go func() {
		resultCh <- executor.ExecuteAll([]types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "AskTool", Input: json.RawMessage(`{}`)},
		})
	}()

	// Wait for ask event, then deny
	select {
	case ask := <-askCh:
		ask.ResponseCh <- types.UserDecisionDeny
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission ask")
	}

	var result *ExecuteAllResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ExecuteAll result")
	}

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 result block, got %d", len(result.ToolResultBlocks))
	}

	var content string
	if err := json.Unmarshal(result.ToolResultBlocks[0].Content, &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if !strings.Contains(content, "The user doesn't want to proceed") {
		t.Errorf("user reject should contain user-facing message, got: %q", content)
	}
	if strings.Contains(content, "IMPORTANT") {
		t.Errorf("user reject should NOT contain workaround guidance, got: %q", content)
	}
}

type denyTestTool struct{}

func (d *denyTestTool) Name() string                        { return "TestTool" }
func (d *denyTestTool) Aliases() []string                   { return nil }
func (d *denyTestTool) Description(json.RawMessage) (string, error) { return "", nil }
func (d *denyTestTool) InputSchema() json.RawMessage        { return json.RawMessage(`{}`) }
func (d *denyTestTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return &tool.ToolResult{Data: "ok"}, nil
}
func (d *denyTestTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (d *denyTestTool) IsReadOnly(json.RawMessage) bool           { return false }
func (d *denyTestTool) IsDestructive(json.RawMessage) bool        { return true }
func (d *denyTestTool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (d *denyTestTool) IsEnabled() bool                           { return true }
func (d *denyTestTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (d *denyTestTool) Prompt() string                            { return "" }
func (d *denyTestTool) RenderResult(any) string                     { return "" }
func (d *denyTestTool) MaxResultSize() int                         { return 50000 }
