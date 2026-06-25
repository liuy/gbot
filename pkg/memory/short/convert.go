package short

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/types"
)

// EngineMessagesToStore converts engine messages to store TranscriptMessages.
// Preserves UUID from em.ID when available, metadata via MetadataToJSON.
func EngineMessagesToStore(engineMsgs []types.Message) ([]*TranscriptMessage, error) {
	if len(engineMsgs) == 0 {
		return nil, nil
	}

	result := make([]*TranscriptMessage, 0, len(engineMsgs))
	for _, em := range engineMsgs {
		storeBlocks := make([]types.ContentBlock, 0, len(em.Content))
		storeBlocks = append(storeBlocks, em.Content...)

		contentBytes, err := json.Marshal(storeBlocks)
		if err != nil {
			return nil, fmt.Errorf("marshal content blocks: %w", err)
		}

		msgUUID := em.ID
		if msgUUID == "" {
			msgUUID = uuid.New().String()
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
