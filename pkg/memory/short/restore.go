package short

import (
	"encoding/json"
	"log/slog"
)

// RestoreSkillStateFromMessages restores skill invocation state from messages.
// Looks for 'invoked_skills' attachment messages and extracts the skill list.
// TS: conversationRecovery.ts:382-403
func RestoreSkillStateFromMessages(messages []*Message) *SkillState {
	state := &SkillState{
		InvokedSkills: make(map[string]bool),
		CronTasks:     nil,
	}

	for _, msg := range messages {
		if msg.Type != "attachment" {
			continue
		}

		var attachment map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &attachment); err != nil {
			continue
		}

		attachmentType, _ := attachment["type"].(string)

		// Extract invoked skills
		if attachmentType == "invoked_skills" {
			skills, _ := attachment["skills"].([]interface{})
			for _, s := range skills {
				skill, ok := s.(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := skill["name"].(string)
				path, _ := skill["path"].(string)
				content, _ := skill["content"].(string)
				// Only add if all required fields are present
				if name != "" && path != "" && content != "" {
					state.InvokedSkills[name] = true
				}
			}
		}

		// Extract cron tasks
		if attachmentType == "cron_task" {
			skillName, _ := attachment["skill_name"].(string)
			cronExpr, _ := attachment["cron_expr"].(string)
			durable, _ := attachment["durable"].(bool)
			if skillName != "" && cronExpr != "" {
				state.CronTasks = append(state.CronTasks, CronTask{
					SkillName: skillName,
					CronExpr:  cronExpr,
					Durable:   durable,
				})
			}
		}
	}

	if len(state.InvokedSkills) == 0 && len(state.CronTasks) == 0 {
		return nil
	}
	return state
}

// RestoreAgentFromSession restores agent state from session messages.
// Looks for 'agent-setting' attachment messages to recover agent configuration.
// TS: sessionRestore.ts:200-242
func RestoreAgentFromSession(messages []*Message) *AgentState {
	for _, msg := range messages {
		if msg.Type != "attachment" {
			continue
		}

		var attachment map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &attachment); err != nil {
			continue
		}

		if attachment["type"] != "agent-setting" {
			continue
		}

		// Extract agent configuration
		agentType, _ := attachment["agent_type"].(string)
		model, _ := attachment["model"].(string)
		settings := make(map[string]string)

		if settingsMap, ok := attachment["settings"].(map[string]interface{}); ok {
			for k, v := range settingsMap {
				if str, ok := v.(string); ok {
					settings[k] = str
				}
			}
		}

		// Extract tool_use_id -> agent_id mappings
		toolUseIDs := make(map[string]string)
		if mappings, ok := attachment["tool_use_ids"].(map[string]interface{}); ok {
			for toolUseID, agentID := range mappings {
				if agentStr, ok := agentID.(string); ok {
					toolUseIDs[toolUseID] = agentStr
				}
			}
		}

		return &AgentState{
			AgentType:  agentType,
			Model:      model,
			Setting:    settings,
			ToolUseIDs: toolUseIDs,
		}
	}

	return nil
}

// ExtractTodosFromTranscript extracts TODO items from the transcript.
// Looks for the most recent TodoWrite tool_use and parses its todos list.
// TS: sessionRestore.ts:77-93
func ExtractTodosFromTranscript(messages []*Message) []*TodoItem {
	// Search backwards from the end to find the latest TodoWrite
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Type != "assistant" {
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		var todoWriteBlock *ContentBlock
		for _, block := range blocks {
			if block.Type == "tool_use" && block.Name == "TodoWrite" {
				todoWriteBlock = &block
				break
			}
		}

		if todoWriteBlock == nil {
			continue
		}

		// Parse the input to extract todos
		var input map[string]interface{}
		if err := json.Unmarshal(todoWriteBlock.Input, &input); err != nil {
			continue
		}

		todosRaw, ok := input["todos"]
		if !ok {
			continue
		}

		todosList, ok := todosRaw.([]interface{})
		if !ok {
			continue
		}

		var todos []*TodoItem
		for _, t := range todosList {
			todoMap, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := todoMap["id"].(string)
			subject, _ := todoMap["subject"].(string)
			status, _ := todoMap["status"].(string)
			description, _ := todoMap["description"].(string)

			todos = append(todos, &TodoItem{
				ID:          id,
				Subject:     subject,
				Status:      status,
				Description: description,
			})
		}

		return todos
	}

	return nil
}

// ComputeRestoredAttributionState computes attribution state from messages.
// Looks for 'attribution-snapshot' messages to determine if this is a sub-agent session.
// TS: sessionRestore.ts:157-168
func ComputeRestoredAttributionState(messages []*Message) *AttributionState {
	for _, msg := range messages {
		if msg.Type != "attribution-snapshot" {
			continue
		}

		var snapshot map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &snapshot); err != nil {
			continue
		}

		return &AttributionState{
			IsSubAgent:    true,
			ParentAgentID: valueAsString(snapshot["parent_agent_id"]),
			ToolUseID:     valueAsString(snapshot["tool_use_id"]),
		}
	}

	// Default: not a sub-agent
	return &AttributionState{
		IsSubAgent:    false,
		ParentAgentID: "",
		ToolUseID:     "",
	}
}

// ComputeStandaloneAgentContext computes standalone agent context from messages.
// Extracts agent name and color from the session metadata.
// TS: sessionRestore.ts:175-188
func ComputeStandaloneAgentContext(messages []*Message) *AgentContext {
	var agentName, agentColor string

	for _, msg := range messages {
		if msg.Type != "attachment" {
			continue
		}

		var attachment map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &attachment); err != nil {
			continue
		}

		if attachment["type"] == "agent-name" {
			agentName, _ = attachment["name"].(string)
		}
		if attachment["type"] == "agent-color" {
			agentColor, _ = attachment["color"].(string)
		}
	}

	if agentName == "" && agentColor == "" {
		return nil
	}

	// For context, we need session_id from the first message
	var sessionID string
	if len(messages) > 0 {
		sessionID = messages[0].SessionID
	}

	return &AgentContext{
		AgentType:    "standalone", // Generic type for standalone agents
		SessionID:    sessionID,
		Model:        "",
		SystemPrompt: "",
	}
}

// RestoreWorktreeForResume restores worktree state for session resume.
// In Go, this must be called from the CLI/engine layer which has access to
// process working directory and worktree session state.
// TS: sessionRestore.ts:332-366 — does process.chdir, saveWorktreeState, clearMemoryFileCaches.
func RestoreWorktreeForResume(sessionID string) error {
	// CLI layer responsibility: os.Chdir to worktree path, update cwd state.
	// The store layer does not manage process state.
	return nil
}

// ExitRestoredWorktree exits a restored worktree session.
// Must be called from the CLI/engine layer to undo the cwd change.
// TS: sessionRestore.ts:380-401 — restores original cwd, clears worktree state.
func ExitRestoredWorktree(sessionID string) error {
	// CLI layer responsibility: os.Chdir back to original directory.
	return nil
}

// RefreshAgentDefinitionsForModeSwitch refreshes agent definitions after a mode switch.
// Must be called from the CLI/engine layer which owns the agent loader.
// TS: sessionRestore.ts:251-271 — re-derives agent defs via getAgentDefinitionsWithOverrides.
func RefreshAgentDefinitionsForModeSwitch(sessionID string) error {
	// CLI layer responsibility: reload agent definitions from disk, update cache.
	return nil
}

// ProcessResumedConversation orchestrates the resume process by calling all restore functions.
// Returns a consolidated ResumedState with all restored information.
// TS: sessionRestore.ts:409-534 (partial)
func (s *Store) ProcessResumedConversation(sessionID string, messages []*Message) (*ResumedState, error) {
	if len(messages) == 0 {
		return &ResumedState{}, nil
	}

	// Check consistency first
	CheckResumeConsistency(messages)

	// Restore agent state
	agentState := RestoreAgentFromSession(messages)

	// Restore skill state
	skillState := RestoreSkillStateFromMessages(messages)

	// Extract todos
	todos := ExtractTodosFromTranscript(messages)

	// Compute attribution state
	attribution := ComputeRestoredAttributionState(messages)

	return &ResumedState{
		AgentState:  agentState,
		SkillState:  skillState,
		Todos:       todos,
		Attribution: attribution,
	}, nil
}

// CheckResumeConsistency validates resume chain consistency.
// Looks for 'turn_duration' checkpoints and verifies messageCount matches actual position.
// Logs warnings via slog for any inconsistencies (fail-open).
// TS: sessionStorage.ts:2224-2243
func CheckResumeConsistency(chain []*Message) {
	for i := len(chain) - 1; i >= 0; i-- {
		msg := chain[i]
		if msg.Type != "system" || msg.Subtype != "turn_duration" {
			continue
		}

		// Extract messageCount from the checkpoint
		var checkpoint map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &checkpoint); err != nil {
			continue
		}

		expectedRaw, ok := checkpoint["messageCount"]
		if !ok {
			return
		}

		var expected int
		switch v := expectedRaw.(type) {
		case float64:
			expected = int(v)
		default:
			return
		}

		actual := i
		if actual != expected {
			slog.Warn("resume consistency check failed",
				"expected", expected,
				"actual", actual,
				"delta", actual-expected,
				"chain_length", len(chain),
				"checkpoint_age_entries", len(chain)-1-i,
			)
		}
		return
	}
}

// GroupMessagesByApiRound groups messages by API round boundaries.
// A new group starts when an assistant message has a different "message_id" from the previous assistant.
// For Go messages without message_id, we group by UUID sequence as a heuristic.
// TS: grouping.ts:22-63
func GroupMessagesByApiRound(messages []*Message) [][]*Message {
	if len(messages) == 0 {
		return nil
	}

	var groups [][]*Message
	var current []*Message

	// Track the last assistant's message_id (extracted from content if available)
	var lastMessageID string

	for _, msg := range messages {
		var currentMessageID string

		// Try to extract message_id from assistant messages
		if msg.Type == "assistant" {
			currentMessageID = extractMessageID(msg.Content)
			// If no message_id in content, use UUID as fallback
			if currentMessageID == "" {
				currentMessageID = msg.UUID
			}
		}

		// Start new group when assistant has different message_id
		if msg.Type == "assistant" &&
			currentMessageID != lastMessageID &&
			len(current) > 0 {
			groups = append(groups, current)
			current = []*Message{msg}
		} else {
			current = append(current, msg)
		}

		if msg.Type == "assistant" {
			lastMessageID = currentMessageID
		}
	}

	if len(current) > 0 {
		groups = append(groups, current)
	}

	return groups
}

// extractMessageID extracts the message_id from an assistant message's content JSON.
// This is the API response identifier shared by streaming chunks from the same response.
// Returns empty string if not found.
func extractMessageID(content string) string {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return ""
	}

	messageID, _ := result["message_id"].(string)
	return messageID
}

// valueAsString is a helper to extract string values from interface{}
func valueAsString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
