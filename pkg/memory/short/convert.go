package short

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"uuid"

	"github.com/liuy/gbot/pkg/types"
)

// EngineMessagesToStore converts engine messages to store TranscriptMessages.
// Preserves UUID from em.ID when available, metadata via MetadataToJSON.
//
// A message whose blocks fail to marshal (e.g. tool args truncated mid-stream
// by a provider cut-off) is degraded to a single text block recording the
// block types and the error — never failing the whole batch. A batch-level
// error would wedge the caller's persist cursor forever and silently drop
// every later message of the session (2026-09-22 incident: 3 hours, zero
// rows). Messages holding the other half of a degraded message's
// tool_use/tool_result pair degrade too, because a surviving orphan half
// makes replayed history invalid for the API.
func EngineMessagesToStore(engineMsgs []types.Message) ([]*TranscriptMessage, error) {
	if len(engineMsgs) == 0 {
		return nil, nil
	}

	contents := make([][]byte, len(engineMsgs))
	degraded := make(map[int]string)
	// Root marshal error per tool-call id so pair-degraded messages get a
	// reason that still names the underlying failure.
	rootErrByID := make(map[string]string)
	for i, em := range engineMsgs {
		b, err := types.MarshalContentBlocksForStorage(em.Content)
		if err != nil {
			reason := fmt.Sprintf("marshal content blocks for storage: %v", err)
			degraded[i] = reason
			for _, id := range toolCallIDs(em) {
				if _, ok := rootErrByID[id]; !ok {
					rootErrByID[id] = reason
				}
			}
			continue
		}
		contents[i] = b
	}

	// Degrading a message orphans its tool-call pairs, and those orphans'
	// own pairs in turn, so re-scan until no new message gets degraded.
	for {
		changed := false
		for i, em := range engineMsgs {
			if _, ok := degraded[i]; ok {
				continue
			}
			for _, id := range toolCallIDs(em) {
				root, hit := rootErrByID[id]
				if !hit {
					continue
				}
				degraded[i] = fmt.Sprintf("paired tool call %q failed to serialize: %s", id, root)
				for _, id2 := range toolCallIDs(em) {
					if _, ok := rootErrByID[id2]; !ok {
						rootErrByID[id2] = root
					}
				}
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}

	result := make([]*TranscriptMessage, 0, len(engineMsgs))
	for i, em := range engineMsgs {
		msgUUID := em.ID
		if msgUUID == "" {
			msgUUID = uuid.New().String()
		}
		contentBytes := contents[i]
		if reason, ok := degraded[i]; ok {
			slog.Error("EngineMessagesToStore: degraded unserializable message to text dump",
				"message_uuid", msgUUID,
				"role", string(em.Role),
				"reason", reason,
			)
			// A lone text block always marshals; best-effort guard mirrors
			// MetadataToJSON's handling of an impossible marshal error.
			if dump, err := types.MarshalContentBlocksForStorage([]types.ContentBlock{degradedTextDump(em, reason)}); err == nil {
				contentBytes = dump
			}
		}
		result = append(result, &TranscriptMessage{
			UUID:      msgUUID,
			Type:      string(em.Role),
			Content:   string(contentBytes),
			Metadata:  em.MetadataToJSON(),
			CreatedAt: em.Timestamp,
		})
	}

	return result, nil
}

// toolCallIDs returns the tool-call ids a message participates in — tool_use
// ids and tool_result targets alike, since either half of a pair orphans the
// other when its message is degraded.
func toolCallIDs(em types.Message) []string {
	var ids []string
	for _, b := range em.Content {
		switch b.Type {
		case types.ContentTypeToolUse:
			ids = append(ids, b.ID)
		case types.ContentTypeToolResult:
			ids = append(ids, b.ToolUseID)
		}
	}
	return ids
}

// degradedTextDump renders a message that failed to serialize as one text
// block: the failure reason plus each original block's type and identity, so
// the stored transcript still records what the session actually contained
// without carrying the bytes that broke serialization.
func degradedTextDump(em types.Message, reason string) types.ContentBlock {
	var sb strings.Builder
	sb.WriteString("[storage fallback] ")
	sb.WriteString(reason)
	for _, b := range em.Content {
		switch b.Type {
		case types.ContentTypeToolUse:
			fmt.Fprintf(&sb, " [block type=%s id=%s name=%s]", b.Type, b.ID, b.Name)
		case types.ContentTypeToolResult:
			fmt.Fprintf(&sb, " [block type=%s tool_use_id=%s]", b.Type, b.ToolUseID)
		default:
			fmt.Fprintf(&sb, " [block type=%s]", b.Type)
		}
	}
	return types.NewTextBlock(sb.String())
}

// StoreMessageToEngine converts a single TranscriptMessage to a types.Message.
// Handles non-JSON content gracefully (falls back to text block).
func StoreMessageToEngine(m *TranscriptMessage) types.Message {
	if m == nil {
		return types.Message{}
	}

	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(m.Content), &blocks); err != nil {
		msg := types.Message{
			ID:        m.UUID,
			Role:      types.Role(m.Type),
			Content:   []types.ContentBlock{types.NewTextBlock(m.Content)},
			Timestamp: m.CreatedAt,
		}
		msg.SetMetadataFromJSON(m.Metadata)
		return msg
	}

	msg := types.Message{
		ID:        m.UUID,
		Role:      types.Role(m.Type),
		Content:   blocks,
		Timestamp: m.CreatedAt,
	}
	msg.SetMetadataFromJSON(m.Metadata)
	return msg
}

// StoreMessagesToEngine converts store TranscriptMessages to engine messages.
func StoreMessagesToEngine(storeMsgs []*TranscriptMessage) ([]types.Message, error) {
	if len(storeMsgs) == 0 {
		return nil, nil
	}

	result := make([]types.Message, 0, len(storeMsgs))
	for _, sm := range storeMsgs {
		role := types.Role(sm.Type)
		switch role {
		case types.RoleUser, types.RoleAssistant, types.RoleSystem:
		default:
			return nil, fmt.Errorf("unknown message role %q in store message seq=%d", sm.Type, sm.Seq)
		}

		msg := StoreMessageToEngine(sm)
		result = append(result, msg)
	}

	return result, nil
}
