package engine

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func TestQuerySyncWithContent_TextAndImage(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "I see the image."), nil)

	eng := New(&Params{
		Provider:        mp,
		Model:           "test-model",
		Logger:          slog.Default(),
		InputModalities: []string{"text", "image"},
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	content := []types.ContentBlock{
		types.NewTextBlock("What is in this image?"),
		types.NewFileImageBlock("image/png", "/tmp/cache/images/abc.png"),
	}
	result := eng.QuerySyncWithContent(ctx, content, "")
	if result.Error != nil {
		t.Fatalf("QuerySyncWithContent error: %v", result.Error)
	}

	// The provider must have received both blocks: a text block and an image
	// block carrying the file source. (Materialization runs in the provider,
	// not the engine, so the engine-side request still has Type=="file".)
	msgs := mp.lastRequestMessages()
	if len(msgs) == 0 {
		t.Fatal("provider received no messages")
	}
	userMsg := msgs[0]
	if userMsg.Role != types.RoleUser {
		t.Errorf("first message role = %q, want user", userMsg.Role)
	}
	if len(userMsg.Content) != 2 {
		t.Fatalf("user message content length = %d, want 2", len(userMsg.Content))
	}
	if userMsg.Content[0].Type != types.ContentTypeText {
		t.Errorf("content[0].Type = %q, want text", userMsg.Content[0].Type)
	}
	// The engine prepends a timestamp to user text (existing behavior); assert
	// the original question survives as a substring rather than exact-matching.
	if !strings.Contains(userMsg.Content[0].Text, "What is in this image?") {
		t.Errorf("content[0].Text = %q, want it to contain the question", userMsg.Content[0].Text)
	}
	cb1 := userMsg.Content[1]
	if cb1.Type != types.ContentTypeImage {
		t.Fatalf("content[1].Type = %q, want image", cb1.Type)
	}
	if cb1.Source == nil {
		t.Fatal("content[1].Source = nil, want non-nil")
	}
	if cb1.Source.Type != "file" {
		t.Errorf("content[1].Source.Type = %q, want file", cb1.Source.Type)
	}
	if cb1.Source.MediaType != "image/png" {
		t.Errorf("content[1].Source.MediaType = %q, want image/png", cb1.Source.MediaType)
	}
	if cb1.Source.Path != "/tmp/cache/images/abc.png" {
		t.Errorf("content[1].Source.Path = %q, want /tmp/cache/images/abc.png", cb1.Source.Path)
	}

	// result.Reply proves the turn loop ran and produced assistant text.
	if result.Reply == "" {
		t.Error("result.Reply is empty, want the assistant's text response")
	}
}

func TestQuerySyncWithContent_AppearsInResultMessages(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "ok"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	content := []types.ContentBlock{
		types.NewTextBlock("hello"),
		types.NewFileImageBlock("image/jpeg", "/x.jpg"),
	}
	result := eng.QuerySyncWithContent(ctx, content, "")
	if result.Error != nil {
		t.Fatalf("QuerySyncWithContent error: %v", result.Error)
	}

	// The user message in result.Messages must carry BOTH the text and image
	// block — proving queryLoopWithContent assembles the full content slice.
	var userMsg *types.Message
	for i := range result.Messages {
		if result.Messages[i].Role == types.RoleUser {
			userMsg = &result.Messages[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatal("no user message in result.Messages")
	}
	if len(userMsg.Content) != 2 {
		t.Fatalf("user message content length = %d, want 2", len(userMsg.Content))
	}
	hasText, hasImage := false, false
	for _, cb := range userMsg.Content {
		switch cb.Type {
		case types.ContentTypeText:
			if cb.Text == "hello" {
				hasText = true
			}
		case types.ContentTypeImage:
			if cb.Source != nil && cb.Source.MediaType == "image/jpeg" {
				hasImage = true
			}
		}
	}
	if !hasText {
		t.Error("user message missing the text block")
	}
	if !hasImage {
		t.Error("user message missing the image block")
	}
}

func TestQueryWithContent_EmitsQueryStartWithBlocks(t *testing.T) {
	// NOT parallel: reads from the shared event collector channel and waits
	// for the async query to complete.
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "reply"), nil)

	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	content := []types.ContentBlock{
		types.NewTextBlock("look at this"),
		types.NewFileImageBlock("image/png", "/img.png"),
	}
	eng.QueryWithContent(ctx, content, "")

	result := ec.WaitForResult()
	if result.Error != nil {
		t.Fatalf("query error: %v", result.Error)
	}

	starts := ec.FindEvents(types.EventQueryStart)
	if len(starts) != 1 {
		t.Fatalf("EventQueryStart count = %d, want 1", len(starts))
	}
	msg := starts[0].Message
	if msg == nil {
		t.Fatal("EventQueryStart carried no Message")
	}
	if msg.Role != types.RoleUser {
		t.Errorf("EventQueryStart message role = %q, want user", msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("EventQueryStart message content length = %d, want 2 (text + image)", len(msg.Content))
	}
	// Verify the image block specifically propagated through the event.
	foundImage := false
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeImage && cb.Source != nil && cb.Source.MediaType == "image/png" {
			foundImage = true
		}
	}
	if !foundImage {
		t.Error("EventQueryStart message missing the image block")
	}
}

// TestEngineImageCapableModelRetainsImage is the positive counterpart to
// TestEngineDefaultModalitiesStripsImage and the engine-layer mirror of the
// WeChat engine regression (cmd/gbot/main.go startWeChatConnector). An engine
// explicitly initialized with InputModalities=["text","image"] must NOT strip
// image blocks, even after the root-cause fix removes SupportsModality's
// empty-value branch. If a future change makes New() drop InputModalities back
// to empty (or flips callLLM's strip condition), SupportsModality("image")
// becomes false and this test goes red before the WeChat path regresses.
func TestEngineImageCapableModelRetainsImage(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "ok"), nil)

	eng := New(&Params{
		Provider:        mp,
		Model:           "test-model",
		Logger:          slog.Default(),
		InputModalities: []string{"text", "image"},
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	content := []types.ContentBlock{
		types.NewTextBlock("q"),
		types.NewFileImageBlock("image/png", "/x.png"),
	}
	result := eng.QuerySyncWithContent(ctx, content, "")
	if result.Error != nil {
		t.Fatalf("QuerySyncWithContent error: %v", result.Error)
	}

	msgs := mp.lastRequestMessages()
	if len(msgs) == 0 {
		t.Fatal("provider received no messages")
	}
	var userMsg *types.Message
	for i := range msgs {
		if msgs[i].Role == types.RoleUser {
			userMsg = &msgs[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatal("no user message in provider request")
	}

	hasImageBlock := false
	hasImagePlaceholder := false
	for _, cb := range userMsg.Content {
		switch {
		case cb.Type == types.ContentTypeImage:
			hasImageBlock = true
		case cb.Type == types.ContentTypeText && cb.Text == "[image]":
			hasImagePlaceholder = true
		}
	}
	if !hasImageBlock {
		t.Error("image-capable model: user message missing the ContentTypeImage block; modalities=[\"text\",\"image\"] must retain it")
	}
	if hasImagePlaceholder {
		t.Error("image-capable model: user message wrongly carries the \"[image]\" placeholder; stripping must not run when image is supported")
	}
}
