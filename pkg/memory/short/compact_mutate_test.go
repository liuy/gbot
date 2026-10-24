package short

// compact_mutate_test.go pins the behaviour of compact.go's pure functions at
// the exact boundaries their code claims: comparison thresholds, branch
// taken/not-taken, per-field content of generated messages, and the walk
// guards in the preserved-segment relink.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// mcBlocks marshals blocks into the stored content-block-array form.
func mcBlocks(blocks ...types.ContentBlock) string {
	b, err := json.Marshal(blocks)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// mcUsageContent builds an assistant-shaped content JSON with non-zero usage
// tokens, so zeroing is observable.
func mcUsageContent(inTokens string) string {
	return `{"message":{"usage":{"input_tokens":` + inTokens + `,"output_tokens":12,"cache_creation_input_tokens":3,"cache_read_input_tokens":9}}}`
}

// mcAssistantWithUsage builds an assistant message whose content JSON carries
// non-zero usage tokens, so zeroing is observable.
func mcAssistantWithUsage(uuid, parent, inTokens string) *TranscriptMessage {
	return &TranscriptMessage{
		UUID:       uuid,
		ParentUUID: parent,
		Type:       "assistant",
		Content:    mcUsageContent(inTokens),
	}
}

func mcUsageOf(t *testing.T, msg *TranscriptMessage) map[string]any {
	t.Helper()
	var parsed struct {
		Message struct {
			Usage map[string]any `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(msg.Content), &parsed); err != nil {
		t.Fatalf("unmarshal assistant content %q: %v", msg.Content, err)
	}
	return parsed.Message.Usage
}

// --- CreateCompactBoundaryMessage: inner content map fields (46, 51-57) ---

func TestMCBoundaryContentMapFields(t *testing.T) {
	msg := CreateCompactBoundaryMessage("auto", 4321, "")

	cm, err := parseBoundaryContentMap(msg.Content)
	if err != nil {
		t.Fatalf("parseBoundaryContentMap: %v", err)
	}
	for _, tc := range []struct {
		key  string
		want any
	}{
		{"type", "system"},
		{"subtype", "compact_boundary"},
		{"content", "Conversation compacted"},
		{"isMeta", false},
		{"level", "info"},
		{"uuid", msg.UUID},
		{"timestamp", msg.CreatedAt.Format("2006-01-02T15:04:05Z07:00")},
	} {
		got, ok := cm[tc.key]
		if !ok {
			t.Errorf("content map is missing key %q: %v", tc.key, cm)
			continue
		}
		if got != tc.want {
			t.Errorf("content map %q = %#v, want %#v", tc.key, got, tc.want)
		}
	}

	meta, ok := cm["compactMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("compactMetadata = %#v, want object", cm["compactMetadata"])
	}
	if got, ok := meta["messagesSummarized"]; !ok {
		t.Error("compactMetadata.messagesSummarized missing")
	} else if got != float64(0) {
		t.Errorf("messagesSummarized = %#v, want 0 (RecordCompact fills it later)", got)
	}
	if got, ok := meta["preTokens"]; !ok {
		t.Error("compactMetadata.preTokens missing")
	} else if got != float64(4321) {
		t.Errorf("preTokens = %#v, want 4321", got)
	}
	if got, ok := meta["trigger"]; !ok {
		t.Error("compactMetadata.trigger missing")
	} else if got != "auto" {
		t.Errorf("trigger = %#v, want auto", got)
	}
}

// --- PartialCompact (299, 310-314) ---

func TestMCPartialCompactSingleKeptMessage(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "m1", "", `[{"type":"text","text":"first"}]`),
		testMessage(0, "assistant", "m2", "m1", `[{"type":"text","text":"second"}]`),
		testMessage(0, "user", "m3", "m2", `[{"type":"text","text":"tail"}]`),
	}

	res, err := PartialCompact(msgs, 2, "auto")
	if err != nil {
		t.Fatalf("PartialCompact: %v", err)
	}

	if len(res.MessagesToKeep) != 1 || res.MessagesToKeep[0].UUID != "m3" {
		t.Fatalf("MessagesToKeep = %v, want only m3", chainUUIDs(res.MessagesToKeep))
	}

	// A one-message tail still gets the preserved-segment annotation: the
	// guard is len(messagesToKeep) > 0, not > 1.
	meta, err := extractCompactMetadata(res.BoundaryMarker)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	if meta.PreservedSegment == nil {
		t.Fatal("single-message tail lost its preservedSegment")
	}
	if meta.PreservedSegment.HeadUUID != "m3" || meta.PreservedSegment.TailUUID != "m3" {
		t.Errorf("segment = %+v, want head and tail both m3", meta.PreservedSegment)
	}
	if meta.PreservedSegment.AnchorUUID != res.BoundaryMarker.UUID {
		t.Errorf("anchor = %q, want boundary uuid %q", meta.PreservedSegment.AnchorUUID, res.BoundaryMarker.UUID)
	}

	wantPre := utils.EstimateTokens(msgs[0].Content) + utils.EstimateTokens(msgs[1].Content)
	if res.PreCompactTokens != wantPre {
		t.Errorf("PreCompactTokens = %d, want %d", res.PreCompactTokens, wantPre)
	}
	if res.PostCompactTokens != 0 {
		t.Errorf("PostCompactTokens = %d, want 0 before summary generation", res.PostCompactTokens)
	}
	if res.SummaryMessages == nil || len(res.SummaryMessages) != 0 {
		t.Errorf("SummaryMessages = %#v, want non-nil empty slice", res.SummaryMessages)
	}
	if res.Attachments == nil || len(res.Attachments) != 0 {
		t.Errorf("Attachments = %#v, want non-nil empty slice", res.Attachments)
	}
}

// --- StripImagesFromMessages (328-374) ---

func TestMCStripImagesNonUserUntouched(t *testing.T) {
	assistant := &TranscriptMessage{
		UUID:    "a-1",
		Type:    "assistant",
		Content: mcBlocks(types.ContentBlock{Type: "image", Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAA"}}),
	}
	got := StripImagesFromMessages([]*TranscriptMessage{assistant})
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0] != assistant {
		t.Errorf("assistant message was rebuilt: content %q, want the original %q", got[0].Content, assistant.Content)
	}
	if got[0].Content != assistant.Content {
		t.Errorf("image stripped from a non-user message: %q, want %q", got[0].Content, assistant.Content)
	}
}

func TestMCStripImagesKeepsScanningAfterSkippedMessages(t *testing.T) {
	assistant := &TranscriptMessage{UUID: "a-1", Type: "assistant", Content: mcBlocks(types.ContentBlock{Type: "text", Text: "hi"})}
	emptyUser := &TranscriptMessage{UUID: "u-1", Type: "user", Content: ""}
	textUser := &TranscriptMessage{UUID: "u-2", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "text", Text: "no media"})}
	imageUser := &TranscriptMessage{
		UUID:    "u-3",
		Type:    "user",
		Content: mcBlocks(types.ContentBlock{Type: "image", Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAA"}}),
	}

	got := StripImagesFromMessages([]*TranscriptMessage{assistant, emptyUser, textUser, imageUser})
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	if got[3].Content != `[{"type":"text","text":"[image]"}]` {
		t.Errorf("fourth message content = %q, want the image replaced by [image]", got[3].Content)
	}
}

func TestMCStripImagesUnmodifiedMessageKeepsExactContent(t *testing.T) {
	// Unknown JSON members are dropped by the re-marshal, so a byte-identical
	// result proves the message went through the pass-through path instead of
	// being rebuilt.
	original := `[{"type":"text","text":"keep me","vendor_extra":1}]`
	msg := &TranscriptMessage{UUID: "u-1", Type: "user", Content: original}

	got := StripImagesFromMessages([]*TranscriptMessage{msg})
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Content != original {
		t.Errorf("content = %q, want %q unchanged", got[0].Content, original)
	}
	if got[0] != msg {
		t.Errorf("unmodified message was cloned: %p vs %p", got[0], msg)
	}
}

func TestMCStripImagesNonToolResultContentUntouched(t *testing.T) {
	// A non-tool_result block that happens to carry a JSON content payload must
	// not go through the nested-media stripper.
	msg := &TranscriptMessage{
		UUID: "u-1",
		Type: "user",
		Content: mcBlocks(types.ContentBlock{
			Type:    "text",
			Text:    "note",
			Content: json.RawMessage(`[{"type":"image"}]`),
		}),
	}
	got := StripImagesFromMessages([]*TranscriptMessage{msg})
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Content != msg.Content {
		t.Errorf("content = %q, want %q unchanged", got[0].Content, msg.Content)
	}
}

// --- stripNestedMediaShort (390-408) ---

func TestMCStripNestedMediaInvalidJSONReturnsRaw(t *testing.T) {
	raw := json.RawMessage(`not json`)
	out, hasMedia := stripNestedMediaShort(raw)
	if hasMedia {
		t.Error("hasMedia = true, want false for unparseable content")
	}
	if string(out) != "not json" {
		t.Errorf("raw = %q, want the input returned unchanged", string(out))
	}
}

func TestMCStripNestedMediaNoMediaReturnsRawBytes(t *testing.T) {
	// The no-media path must hand back the original bytes, not a re-marshal:
	// unknown members and key order are part of the stored payload.
	raw := json.RawMessage(`[{"vendor_extra":2,"type":"text","text":"hi"}]`)
	out, hasMedia := stripNestedMediaShort(raw)
	if hasMedia {
		t.Error("hasMedia = true, want false for a text-only tool_result")
	}
	if string(out) != string(raw) {
		t.Errorf("raw = %q, want %q unchanged", string(out), string(raw))
	}
}

func TestMCStripNestedMediaDocumentReplaced(t *testing.T) {
	raw := json.RawMessage(`[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAA"}},{"type":"text","text":"after"}]`)
	out, hasMedia := stripNestedMediaShort(raw)
	if !hasMedia {
		t.Fatal("hasMedia = false, want true for a nested document")
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal(out, &blocks); err != nil {
		t.Fatalf("unmarshal result %q: %v", string(out), err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %s", len(blocks), string(out))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "[document]" {
		t.Errorf("block[0] = %s %q, want text \"[document]\"", blocks[0].Type, blocks[0].Text)
	}
	if blocks[1].Type != types.ContentTypeText || blocks[1].Text != "after" {
		t.Errorf("block[1] = %s %q, want text \"after\"", blocks[1].Type, blocks[1].Text)
	}
}

// --- StripReinjectedAttachments (420, 423) ---

func TestMCStripReinjectedAttachmentsMatrix(t *testing.T) {
	msgs := []*TranscriptMessage{
		{UUID: "keep-att", Type: "attachment", Subtype: "file_reference"},
		{UUID: "keep-user", Type: "user", Subtype: "skill_discovery"},
		{UUID: "keep-system", Type: "system", Subtype: "skill_listing"},
		{UUID: "drop-disc", Type: "attachment", Subtype: "skill_discovery"},
		{UUID: "drop-list", Type: "attachment", Subtype: "skill_listing"},
	}
	got := StripReinjectedAttachments(msgs)
	want := []string{"keep-att", "keep-user", "keep-system"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", chainUUIDs(got), want)
	}
	if !slices.Equal(chainUUIDs(got), want) {
		t.Errorf("kept %v, want %v (order preserved)", chainUUIDs(got), want)
	}
}

// --- CreatePostCompactFileAttachments (447-455) ---

func TestMCCreatePostCompactFileAttachmentsFields(t *testing.T) {
	pre := []*TranscriptMessage{
		{UUID: "u-1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`"src/a.go"`)}), CreatedAt: testTimeBase},
		{UUID: "u-2", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`"src/b.go"`)}), CreatedAt: testTimeBase},
	}
	got := CreatePostCompactFileAttachments(pre)
	if len(got) != 2 {
		t.Fatalf("got %d attachments, want 2", len(got))
	}

	// CollectReadToolFilePaths returns map-ordered paths, so the attachment
	// order is not fixed: pin each payload by its filepath value.
	payloads := make([]map[string]any, len(got))
	for i, att := range got {
		if att.Type != "attachment" {
			t.Errorf("attachment[%d].Type = %q, want attachment", i, att.Type)
		}
		if att.Subtype != "file_reference" {
			t.Errorf("attachment[%d].Subtype = %q, want file_reference", i, att.Subtype)
		}
		var cm map[string]any
		if err := json.Unmarshal([]byte(att.Content), &cm); err != nil {
			t.Fatalf("attachment[%d] content %q: %v", i, att.Content, err)
		}
		payloads[i] = cm
	}
	slices.SortFunc(payloads, func(a, b map[string]any) int {
		return strings.Compare(a["filepath"].(string), b["filepath"].(string))
	})

	wantPaths := []string{"src/a.go", "src/b.go"}
	for i, cm := range payloads {
		for _, tc := range []struct {
			key  string
			want any
		}{
			{"type", "attachment"},
			{"subtype", "file_reference"},
			{"filepath", wantPaths[i]},
			{"content", "File: " + wantPaths[i]},
		} {
			if cm[tc.key] != tc.want {
				t.Errorf("attachment %d: %q = %#v, want %#v", i, tc.key, cm[tc.key], tc.want)
			}
		}
	}
}

// --- ShouldExcludeFromPostCompactRestore (470, 473) ---

func TestMCShouldExcludeFromPostCompactRestoreMatrix(t *testing.T) {
	tests := []struct {
		msgType string
		subtype string
		want    bool
	}{
		{"progress", "", true},
		{"progress", "informational", true},
		{"system", "informational", true},
		{"system", "transient", true},
		{"system", "can_use_tool", false},
		{"system", "compact_boundary", false},
		{"attachment", "informational", false},
		{"attachment", "transient", false},
		{"user", "informational", false},
		{"user", "transient", false},
	}
	for _, tc := range tests {
		got := ShouldExcludeFromPostCompactRestore(&TranscriptMessage{Type: tc.msgType, Subtype: tc.subtype})
		if got != tc.want {
			t.Errorf("ShouldExclude(%s/%s) = %v, want %v", tc.msgType, tc.subtype, got, tc.want)
		}
	}
}

// --- TruncateToTokens (487, 491, 496) ---

func TestMCTruncateToTokensExactBudgets(t *testing.T) {
	content := `[{"type":"text","text":"the quick brown fox jumps over the lazy dog"}]`
	per := utils.EstimateTokens(content)
	if per < 2 {
		t.Fatalf("fixture too small to bound: per-message tokens = %d", per)
	}
	msgs := []*TranscriptMessage{
		{UUID: "m1", Type: "user", Content: content},
		{UUID: "m2", Type: "user", Content: content},
		{UUID: "m3", Type: "user", Content: content},
	}

	tests := []struct {
		name      string
		maxTokens int
		want      []string
	}{
		{"exact fit keeps all", 3 * per, []string{"m1", "m2", "m3"}},
		{"one over drops oldest", 3*per - 1, []string{"m2", "m3"}},
		{"two fit exactly", 2 * per, []string{"m2", "m3"}},
		{"one under drops two", 2*per - 1, []string{"m3"}},
	}
	for _, tc := range tests {
		got := TruncateToTokens(msgs, tc.maxTokens)
		if !slices.Equal(chainUUIDs(got), tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, chainUUIDs(got), tc.want)
		}
	}
}

// --- ApplyPreservedSegmentRelinks (537-595) ---

func TestMCRelinksPrunesPreBoundaryAndKeepsSegment(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "t3"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	chain := []*TranscriptMessage{
		{UUID: "old-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"gone"}]`},
		{UUID: "h1", Type: "user", ParentUUID: "old-1", Content: `[{"type":"text","text":"head"}]`},
		{UUID: "m2", Type: "assistant", ParentUUID: "h1"},
		{UUID: "t3", Type: "assistant", ParentUUID: "m2"},
		boundary,
		{UUID: "after-1", Type: "user", ParentUUID: boundary.UUID, Content: `[{"type":"text","text":"after"}]`},
	}

	got := ApplyPreservedSegmentRelinks(boundary, chain)
	want := []string{"h1", "m2", "t3", boundary.UUID, "after-1"}
	if !slices.Equal(chainUUIDs(got), want) {
		t.Fatalf("kept %v, want %v", chainUUIDs(got), want)
	}
	if h1 := findByUUID(got, "h1"); h1.ParentUUID != boundary.UUID {
		t.Errorf("head.ParentUUID = %q, want the anchor %q", h1.ParentUUID, boundary.UUID)
	}
}

func TestMCRelinksZeroesUsageOfEveryPreservedAssistant(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "a9"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	head := &TranscriptMessage{UUID: "h1", Type: "user", ParentUUID: boundary.UUID, Content: mcUsageContent("190000")}
	// Several assistants: the zeroing loop ranges over a map, so a loop that
	// stops early surfaces whichever order the non-assistant turn lands in.
	assistants := []*TranscriptMessage{
		mcAssistantWithUsage("a1", "h1", "190000"),
		mcAssistantWithUsage("a2", "a1", "180000"),
		mcAssistantWithUsage("a3", "a2", "170000"),
		mcAssistantWithUsage("a4", "a3", "160000"),
		mcAssistantWithUsage("a5", "a4", "150000"),
		mcAssistantWithUsage("a6", "a5", "140000"),
		mcAssistantWithUsage("a7", "a6", "130000"),
		mcAssistantWithUsage("a8", "a7", "120000"),
		mcAssistantWithUsage("a9", "a8", "110000"),
	}

	chain := append([]*TranscriptMessage{boundary, head}, assistants...)
	got := ApplyPreservedSegmentRelinks(boundary, chain)
	if len(got) != 11 {
		t.Fatalf("kept %d messages, want 11: %v", len(got), chainUUIDs(got))
	}

	for _, a := range assistants {
		usage := mcUsageOf(t, findByUUID(got, a.UUID))
		for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			if usage[key] != float64(0) {
				t.Errorf("%s usage[%s] = %#v, want 0", a.UUID, key, usage[key])
			}
		}
	}
	// The preserved user turn keeps its payload: only assistant usage is stale.
	if head.Content != mcUsageContent("190000") {
		t.Errorf("preserved user message was rewritten: %q", head.Content)
	}
}

func TestMCRelinksBrokenWalkReturnsChainUnchanged(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	// head and tail both exist (segment is live) but the tail's parent is
	// absent, so the tail→head walk breaks.
	chain := []*TranscriptMessage{
		{UUID: "old-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"keep"}]`},
		{UUID: "t1", Type: "user", ParentUUID: "ghost", Content: `[{"type":"text","text":"tail"}]`},
		{UUID: "h1", Type: "user", ParentUUID: "old-1", Content: `[{"type":"text","text":"head"}]`},
		boundary,
	}

	got := ApplyPreservedSegmentRelinks(boundary, chain)
	if !slices.Equal(chainUUIDs(got), []string{"old-1", "t1", "h1", boundary.UUID}) {
		t.Errorf("kept %v, want the chain unchanged (walk broke at a missing parent)", chainUUIDs(got))
	}
	if t1 := findByUUID(got, "t1"); t1.ParentUUID != "ghost" {
		t.Errorf("t1.ParentUUID = %q, want the original \"ghost\"", t1.ParentUUID)
	}
	if h1 := findByUUID(got, "h1"); h1.ParentUUID != "old-1" {
		t.Errorf("h1.ParentUUID = %q, want the original \"old-1\" (no relink after a broken walk)", h1.ParentUUID)
	}
}

func TestMCRelinksWalkStopsAtChainRoot(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	// The tail walks up to a chain root (empty parent_uuid) that is not the
	// head: the walk must stop there instead of following a stale pointer.
	chain := []*TranscriptMessage{
		{UUID: "t1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"tail"}]`},
		{UUID: "h1", Type: "user", ParentUUID: "t1", Content: `[{"type":"text","text":"head"}]`},
		boundary,
	}

	got := ApplyPreservedSegmentRelinks(boundary, chain)
	if !slices.Equal(chainUUIDs(got), []string{"t1", "h1", boundary.UUID}) {
		t.Errorf("kept %v, want the chain unchanged", chainUUIDs(got))
	}
	if h1 := findByUUID(got, "h1"); h1.ParentUUID != "t1" {
		t.Errorf("h1.ParentUUID = %q, want the original \"t1\"", h1.ParentUUID)
	}
}

func TestMCRelinksWalkTerminatesOnParentCycle(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	// head exists, but the tail's ancestry cycles without reaching it.
	chain := []*TranscriptMessage{
		{UUID: "h1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"head"}]`},
		{UUID: "t1", Type: "user", ParentUUID: "c2", Content: `[{"type":"text","text":"tail"}]`},
		{UUID: "c2", Type: "user", ParentUUID: "t1", Content: `[{"type":"text","text":"cycle"}]`},
		boundary,
	}

	got := ApplyPreservedSegmentRelinks(boundary, chain)
	if !slices.Equal(chainUUIDs(got), []string{"h1", "t1", "c2", boundary.UUID}) {
		t.Errorf("kept %v, want the chain unchanged (cycle aborts the walk)", chainUUIDs(got))
	}
}

// --- applyPreservedSegmentRelinksOnLoad (611-638) ---

// mcSegBoundary builds a boundary carrying a preserved segment whose head is
// the single-message segment "h1", plus one pre-boundary message that the
// relink prunes — so "relink ran" is observable in the returned slice.
func mcSegBoundaryWithPrunable(boundary *TranscriptMessage) []*TranscriptMessage {
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "h1"); err != nil {
		panic(err)
	}
	return []*TranscriptMessage{
		{UUID: "old-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"pruned"}]`},
		boundary,
		{UUID: "h1", Type: "user", ParentUUID: "old-1", Content: `[{"type":"text","text":"head"}]`},
	}
}

func TestMCRelinksOnLoadIgnoresNonBoundaryMessages(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	msgs := mcSegBoundaryWithPrunable(boundary)
	// Messages that each satisfy exactly one half of the boundary test. They
	// sit after the real boundary, so treating any of them as a boundary would
	// move the "absolute last boundary" index past the segment and skip the
	// relink entirely.
	lookalikeUser := &TranscriptMessage{UUID: "look-user", Type: "user", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"user"}`)}
	lookalikeSystem := &TranscriptMessage{UUID: "look-system", Type: "system", Subtype: "informational", Content: wrapAsTextBlock(`{"type":"system"}`)}

	got := applyPreservedSegmentRelinksOnLoad([]*TranscriptMessage{msgs[0], msgs[1], msgs[2], lookalikeUser, lookalikeSystem})
	want := []string{boundary.UUID, "h1", lookalikeUser.UUID, lookalikeSystem.UUID}
	if !slices.Equal(chainUUIDs(got), want) {
		t.Errorf("kept %v, want %v — only system/compact_boundary rows count as boundaries", chainUUIDs(got), want)
	}
}

func TestMCRelinksOnLoadKeepsScanningAfterUnusableBoundaries(t *testing.T) {
	cases := []struct {
		name string
		bad  *TranscriptMessage
	}{
		{"unparseable content", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: ""}},
		{"no compactMetadata", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"system"}`)}},
		{"compactMetadata not an object", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"system","compactMetadata":"oops"}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			boundary := CreateCompactBoundaryMessage("auto", 5000, "")
			msgs := mcSegBoundaryWithPrunable(boundary)
			got := applyPreservedSegmentRelinksOnLoad([]*TranscriptMessage{tc.bad, msgs[0], msgs[1], msgs[2]})
			if !slices.Equal(chainUUIDs(got), []string{boundary.UUID, "h1"}) {
				t.Errorf("kept %v, want [boundary h1] — the scan must continue past an unusable boundary", chainUUIDs(got))
			}
		})
	}
}

func TestMCRelinksOnLoadStaleSegmentSkipsRelink(t *testing.T) {
	segBoundary := CreateCompactBoundaryMessage("auto", 5000, "")
	msgs := mcSegBoundaryWithPrunable(segBoundary)
	later := CreateCompactBoundaryMessage("auto", 100, "")

	got := applyPreservedSegmentRelinksOnLoad([]*TranscriptMessage{msgs[0], msgs[1], msgs[2], later})
	want := []string{"old-1", segBoundary.UUID, "h1", later.UUID}
	if !slices.Equal(chainUUIDs(got), want) {
		t.Errorf("kept %v, want %v (a later boundary without a segment makes the segment stale)", chainUUIDs(got), want)
	}
	if h1 := findByUUID(got, "h1"); h1.ParentUUID != "old-1" {
		t.Errorf("h1.ParentUUID = %q, want the original \"old-1\" (no relink)", h1.ParentUUID)
	}
}

// --- ApplySnipRemovals (649-715) ---

func mcSnipBoundary(uuid string, removed []any) *TranscriptMessage {
	meta := map[string]any{"removedUuids": removed}
	raw, _ := json.Marshal(map[string]any{"type": "system", "subtype": "compact_boundary", "snipMetadata": meta})
	return &TranscriptMessage{UUID: uuid, Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(string(raw))}
}

func TestMCSnipRemovalsIgnoresNonBoundaryMessages(t *testing.T) {
	// Each lookalike satisfies exactly one half of the boundary test while
	// carrying snip metadata: neither may drive deletions.
	snipJSON := `{"type":"user","snipMetadata":{"removedUuids":["del-1"]}}`
	lookalikeUser := &TranscriptMessage{UUID: "look-user", Type: "user", Subtype: "compact_boundary", Content: wrapAsTextBlock(snipJSON)}
	lookalikeSystem := &TranscriptMessage{UUID: "look-system", Type: "system", Subtype: "informational", Content: wrapAsTextBlock(snipJSON)}
	survivor := &TranscriptMessage{UUID: "keep-1", Type: "user", ParentUUID: "del-1", Content: `[{"type":"text","text":"keep"}]`}
	doomed := &TranscriptMessage{UUID: "del-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"doomed"}]`}

	got := ApplySnipRemovals([]*TranscriptMessage{lookalikeUser, lookalikeSystem, doomed, survivor})
	if len(got) != 4 {
		t.Fatalf("kept %d messages %v, want all 4", len(got), chainUUIDs(got))
	}
	if survivor.ParentUUID != "del-1" {
		t.Errorf("survivor.ParentUUID = %q, want \"del-1\" unchanged", survivor.ParentUUID)
	}
}

func TestMCSnipRemovalsKeepsScanningAfterUnusableBoundaries(t *testing.T) {
	cases := []struct {
		name string
		bad  *TranscriptMessage
	}{
		{"unparseable content", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: ""}},
		{"no snipMetadata", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"system"}`)}},
		{"snipMetadata not an object", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"system","snipMetadata":"oops"}`)}},
		{"no removedUuids", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"system","snipMetadata":{"other":1}}`)}},
		{"removedUuids not a list", &TranscriptMessage{UUID: "bad-1", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(`{"type":"system","snipMetadata":{"removedUuids":"del-1"}}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doomed := &TranscriptMessage{UUID: "del-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"doomed"}]`}
			keep := &TranscriptMessage{UUID: "keep-1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"keep"}]`}
			snip := mcSnipBoundary("snip-1", []any{"del-1"})

			got := ApplySnipRemovals([]*TranscriptMessage{tc.bad, doomed, snip, keep})
			if !slices.Equal(chainUUIDs(got), []string{tc.bad.UUID, snip.UUID, keep.UUID}) {
				t.Errorf("kept %v, want the snip applied after an unusable boundary", chainUUIDs(got))
			}
		})
	}
}

func TestMCSnipRemovalsResolveStopsAtFirstLiveAncestor(t *testing.T) {
	snip := mcSnipBoundary("snip-1", []any{"del-b", "del-c"})
	a := &TranscriptMessage{UUID: "a", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"alive"}]`}
	b := &TranscriptMessage{UUID: "del-b", Type: "user", ParentUUID: "a", Content: `[{"type":"text","text":"dead"}]`}
	c := &TranscriptMessage{UUID: "del-c", Type: "user", ParentUUID: "del-b", Content: `[{"type":"text","text":"dead"}]`}
	d := &TranscriptMessage{UUID: "d", Type: "assistant", ParentUUID: "del-c", Content: `[{"type":"text","text":"survivor"}]`}

	got := ApplySnipRemovals([]*TranscriptMessage{snip, a, b, c, d})
	if !slices.Equal(chainUUIDs(got), []string{"snip-1", "a", "d"}) {
		t.Fatalf("kept %v, want [snip-1 a d]", chainUUIDs(got))
	}
	if d.ParentUUID != "a" {
		t.Errorf("survivor parent = %q, want \"a\" (the first non-deleted ancestor)", d.ParentUUID)
	}
}

// --- MergeHookInstructions (778, 779) ---

func TestMCMergeHookInstructionsEmptyAndSingle(t *testing.T) {
	post := []*TranscriptMessage{{UUID: "p1", Type: "user"}, {UUID: "p2", Type: "assistant"}}

	got := MergeHookInstructions(post, nil)
	if !slices.Equal(chainUUIDs(got), []string{"p1", "p2"}) {
		t.Errorf("empty hooks: got %v, want [p1 p2]", chainUUIDs(got))
	}

	one := []*TranscriptMessage{{UUID: "hook-1", Type: "user"}}
	got = MergeHookInstructions(post, one)
	if !slices.Equal(chainUUIDs(got), []string{"p1", "p2", "hook-1"}) {
		t.Errorf("single hook: got %v, want [p1 p2 hook-1]", chainUUIDs(got))
	}
}

// --- AddErrorNotificationIfNeeded (837-844) ---

func TestMCAddErrorNotificationFields(t *testing.T) {
	post := []*TranscriptMessage{{UUID: "p1", Type: "user"}}
	got := AddErrorNotificationIfNeeded(post, errors.New("boom"))
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	last := got[1]
	if last.Type != "system" {
		t.Errorf("Type = %q, want system", last.Type)
	}
	if last.Subtype != "error_notification" {
		t.Errorf("Subtype = %q, want error_notification", last.Subtype)
	}
	var cm map[string]any
	if err := json.Unmarshal([]byte(last.Content), &cm); err != nil {
		t.Fatalf("content %q: %v", last.Content, err)
	}
	for _, tc := range []struct {
		key  string
		want any
	}{
		{"type", "system"},
		{"subtype", "error_notification"},
		{"content", "Compact error: boom"},
		{"level", "error"},
	} {
		if cm[tc.key] != tc.want {
			t.Errorf("content %q = %#v, want %#v", tc.key, cm[tc.key], tc.want)
		}
	}
}

// --- CollectReadToolFilePaths (862-884) ---

func TestMCCollectReadToolFilePathsBranches(t *testing.T) {
	tests := []struct {
		name string
		msgs []*TranscriptMessage
		want []string
	}{
		{
			name: "json string content",
			msgs: []*TranscriptMessage{{UUID: "u1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`"src/main.go"`)})}},
			want: []string{"src/main.go"},
		},
		{
			name: "empty content falls back to text",
			msgs: []*TranscriptMessage{{UUID: "u1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Text: "docs/readme.md"})}},
			want: []string{"docs/readme.md"},
		},
		{
			name: "one-byte content is not a string",
			msgs: []*TranscriptMessage{{UUID: "u1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Text: "docs/a.md", Content: json.RawMessage(`0`)})}},
			want: nil,
		},
		{
			name: "non-string content uses raw bytes",
			msgs: []*TranscriptMessage{{UUID: "u1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`[{"p":"a/b"}]`)})}},
			want: []string{`[{"p":"a/b"}]`},
		},
		{
			name: "plain prose is not a path",
			msgs: []*TranscriptMessage{{UUID: "u1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`"hello world"`)})}},
			want: nil,
		},
		{
			name: "non-user messages are skipped, scanning continues",
			msgs: []*TranscriptMessage{
				{UUID: "a1", Type: "assistant", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`"skip/me.go"`)}), CreatedAt: testTimeBase},
				{UUID: "u1", Type: "user", Content: mcBlocks(types.ContentBlock{Type: "tool_result", Content: json.RawMessage(`"keep/me.go"`)}), CreatedAt: testTimeBase},
			},
			want: []string{"keep/me.go"},
		},
	}

	for _, tc := range tests {
		got := CollectReadToolFilePaths(tc.msgs)
		slices.Sort(got)
		want := tc.want
		if want == nil {
			want = []string{}
		}
		if got == nil {
			got = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, want)
		}
	}
}

// --- TruncateHeadForPTLRetry (901, 902) ---

func TestMCTruncateHeadForPTLRetryBudgets(t *testing.T) {
	content := `[{"type":"text","text":"the quick brown fox jumps over the lazy dog"}]`
	per := utils.EstimateTokens(content)
	msgs := []*TranscriptMessage{
		{UUID: "m1", Type: "user", Content: content},
		{UUID: "m2", Type: "user", Content: content},
	}

	got := TruncateHeadForPTLRetry(msgs, 1)
	if !slices.Equal(chainUUIDs(got), []string{"m2"}) {
		t.Errorf("budget 1: got %v, want [m2] (the last message always survives)", chainUUIDs(got))
	}

	if got := TruncateHeadForPTLRetry(msgs, per); !slices.Equal(chainUUIDs(got), []string{"m2"}) {
		t.Errorf("budget %d (one message): got %v, want [m2]", per, chainUUIDs(got))
	}
	if got := TruncateHeadForPTLRetry(msgs, 2*per); !slices.Equal(chainUUIDs(got), []string{"m1", "m2"}) {
		t.Errorf("budget %d (both messages): got %v, want [m1 m2]", 2*per, chainUUIDs(got))
	}

	empty := TruncateHeadForPTLRetry(msgs, 0)
	if empty == nil {
		t.Error("budget 0: got nil slice, want an empty non-nil slice")
	} else if len(empty) != 0 {
		t.Errorf("budget 0: got %v, want empty", chainUUIDs(empty))
	}
}

// --- annotateBoundaryWithPreservedSegment (936) ---

func TestMCAnnotateKeepsTextBlockShape(t *testing.T) {
	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", "anchor-1", "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	blocks := ParseContentBlocks(boundary.Content)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %s", len(blocks), boundary.Content)
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Errorf("block type = %q, want text", blocks[0].Type)
	}
	if blocks[0].Text == "" {
		t.Error("block text is empty — the boundary payload was dropped")
	}
}

// --- parseBoundaryContentMap (966, 967) ---

func TestMCParseBoundaryContentMapInvalidInnerJSON(t *testing.T) {
	cm, err := parseBoundaryContentMap(wrapAsTextBlock(`{"type":`))
	if err == nil {
		t.Fatalf("got map %#v, want an error for unparseable inner JSON", cm)
	}
	if cm != nil {
		t.Errorf("map = %#v, want nil alongside the error", cm)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the JSON cause", err)
	}
	if !strings.HasPrefix(err.Error(), "parse boundary inner JSON: ") {
		t.Errorf("error = %q, want the parse-boundary context prefix", err)
	}
}

// --- log trails for the best-effort failure paths (556, 919, 920, 1075, 1076) ---

// mcLogCapture records slog messages for the duration of a test. The compact
// paths that swallow failures are only debuggable through their warnings, so
// the warning itself is the observable behaviour.
type mcLogCapture struct{ msgs []string }

func (c *mcLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *mcLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.msgs = append(c.msgs, r.Message)
	return nil
}
func (c *mcLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *mcLogCapture) WithGroup(string) slog.Handler      { return c }

func (c *mcLogCapture) countWith(sub string) int {
	n := 0
	for _, m := range c.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func mcCaptureLogs(t *testing.T) *mcLogCapture {
	t.Helper()
	capture := &mcLogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return capture
}

func TestMCRelinksBrokenWalkLeavesWarning(t *testing.T) {
	logs := mcCaptureLogs(t)

	boundary := CreateCompactBoundaryMessage("auto", 5000, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", boundary.UUID, "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	chain := []*TranscriptMessage{
		{UUID: "t1", Type: "user", ParentUUID: "ghost", Content: `[{"type":"text","text":"tail"}]`},
		{UUID: "h1", Type: "user", ParentUUID: "", Content: `[{"type":"text","text":"head"}]`},
		boundary,
	}
	ApplyPreservedSegmentRelinks(boundary, chain)

	if got := logs.countWith("walk broken"); got != 1 {
		t.Errorf("broken-walk warnings = %d, want 1 (the skipped relink must leave a trail): %v", got, logs.msgs)
	}
}

func TestMCAnnotateUnparseableMetadataLeavesWarning(t *testing.T) {
	logs := mcCaptureLogs(t)

	boundary := &TranscriptMessage{
		UUID:    "b-1",
		Type:    "system",
		Subtype: "compact_boundary",
		Content: wrapAsTextBlock(`{"type":"system","compactMetadata":"not-an-object"}`),
	}
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", "anchor-1", "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if got := logs.countWith("failed to parse compactMetadata"); got != 1 {
		t.Errorf("parse-failure warnings = %d, want 1: %v", got, logs.msgs)
	}

	// The segment still survives the unparseable payload.
	meta, err := extractCompactMetadata(boundary)
	if err != nil {
		t.Fatalf("extractCompactMetadata: %v", err)
	}
	if meta.PreservedSegment == nil || meta.PreservedSegment.AnchorUUID != "anchor-1" {
		t.Errorf("segment = %+v, want the new anchor kept despite the bad payload", meta.PreservedSegment)
	}
}

func TestMCAnnotateCleanMetadataStaysSilent(t *testing.T) {
	logs := mcCaptureLogs(t)

	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	if err := annotateBoundaryWithPreservedSegment(boundary, "h1", "anchor-1", "t1"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if got, want := logs.countWith("failed to parse compactMetadata"), 0; got != want {
		t.Errorf("parse-failure warnings on a clean payload = %d, want %d: %v", got, want, logs.msgs)
	}
}

// --- looksLikeFilePath (1092-1096) ---

func TestMCLooksLikeFilePathBoundaries(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"a/b", true},
		{"a.b", true},
		{"ab", false},
		{strings.Repeat("a", 197) + "/b", true},
		{strings.Repeat("a", 198) + "/b", true},
		{strings.Repeat("a", 199) + "/b", false},
		{strings.Repeat("a", 200) + "/b", false},
		{strings.Repeat("a", 300), false},
	}
	for _, tc := range tests {
		if got := looksLikeFilePath(tc.in); got != tc.want {
			t.Errorf("looksLikeFilePath(len=%d) = %v, want %v", len(tc.in), got, tc.want)
		}
	}
}
