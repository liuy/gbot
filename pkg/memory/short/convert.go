package short

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/types"
)

// EngineBlockToStore converts a types.ContentBlock to a ContentBlock.
func EngineBlockToStore(eb types.ContentBlock) ContentBlock {
	return ContentBlock{
		Type:      string(eb.Type),
		Text:      eb.Text,
		ID:        eb.ID,
		Name:      eb.Name,
		Input:     eb.Input,
		ToolUseID: eb.ToolUseID,
		Content:   eb.Content,
		IsError:   eb.IsError,
		Data:      eb.Data,
	}
}

// StoreBlockToEngine converts a ContentBlock to a types.ContentBlock.
func StoreBlockToEngine(sb ContentBlock) types.ContentBlock {
	return types.ContentBlock{
		Type:      types.ContentType(sb.Type),
		Text:      sb.Text,
		ID:        sb.ID,
		Name:      sb.Name,
		Input:     sb.Input,
		ToolUseID: sb.ToolUseID,
		Content:   sb.Content,
		IsError:   sb.IsError,
		Data:      sb.Data,
	}
}

// EngineMessagesToStore converts engine messages to store TranscriptMessages.
// Preserves UUID from em.ID when available, metadata via MetadataToJSON.
func EngineMessagesToStore(engineMsgs []types.Message) ([]*TranscriptMessage, error) {
	if len(engineMsgs) == 0 {
		return nil, nil
	}

	result := make([]*TranscriptMessage, 0, len(engineMsgs))
	for _, em := range engineMsgs {
		storeBlocks := make([]ContentBlock, 0, len(em.Content))
		for _, eb := range em.Content {
			storeBlocks = append(storeBlocks, EngineBlockToStore(eb))
		}

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

	var blocks []ContentBlock
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

	engineBlocks := make([]types.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		engineBlocks = append(engineBlocks, StoreBlockToEngine(b))
	}

	msg := types.Message{
		ID:        m.UUID,
		Role:      types.Role(m.Type),
		Content:   engineBlocks,
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
