package tui

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// storeBlockToEngine converts a short.ContentBlock to a types.ContentBlock.
func storeBlockToEngine(sb short.ContentBlock) types.ContentBlock {
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

// engineBlockToStore converts a types.ContentBlock to a short.ContentBlock.
func engineBlockToStore(eb types.ContentBlock) short.ContentBlock {
	return short.ContentBlock{
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

// StoreMessagesToEngine converts short-term store messages to engine messages.
// Used when resuming a session from the store.
func StoreMessagesToEngine(storeMsgs []short.TranscriptMessage) ([]types.Message, error) {
	if len(storeMsgs) == 0 {
		return nil, nil
	}

	result := make([]types.Message, 0, len(storeMsgs))
	for _, sm := range storeMsgs {
		role := types.Role(sm.Type)
		switch role {
		case types.RoleUser, types.RoleAssistant, types.RoleSystem:
			// valid
		default:
			return nil, fmt.Errorf("unknown message role %q in store message seq=%d", sm.Type, sm.Seq)
		}

		storeBlocks := short.ParseContentBlocks(sm.Content)
		engineBlocks := make([]types.ContentBlock, 0, len(storeBlocks))
		for _, sb := range storeBlocks {
			engineBlocks = append(engineBlocks, storeBlockToEngine(sb))
		}

		result = append(result, types.Message{
			Role:      role,
			Content:   engineBlocks,
			Timestamp: sm.CreatedAt,
		})
	}

	return result, nil
}

// EngineMessagesToStore converts engine messages to short-term store messages.
// Used when persisting engine state to the store.
func EngineMessagesToStore(engineMsgs []types.Message) ([]short.TranscriptMessage, error) {
	if len(engineMsgs) == 0 {
		return nil, nil
	}

	result := make([]short.TranscriptMessage, 0, len(engineMsgs))
	for _, em := range engineMsgs {
		storeBlocks := make([]short.ContentBlock, 0, len(em.Content))
		for _, eb := range em.Content {
			storeBlocks = append(storeBlocks, engineBlockToStore(eb))
		}

		contentBytes, err := json.Marshal(storeBlocks)
		if err != nil {
			return nil, fmt.Errorf("marshal content blocks: %w", err)
		}

		result = append(result, short.TranscriptMessage{
			UUID:      uuid.New().String(),
			Type:      string(em.Role),
			Content:   string(contentBytes),
			CreatedAt: em.Timestamp,
		})
	}

	return result, nil
}
