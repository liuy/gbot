package types

import (
	"time"
)

// ---------------------------------------------------------------------------
// QueuedItem — source: types/textInputTypes.ts:299-349 QueuedCommand
// ---------------------------------------------------------------------------

// ItemMode identifies the type of queued item.
type ItemMode string

const (
	ItemModePrompt ItemMode = "prompt"
	ItemModeJob    ItemMode = "job"
)

// QueuePriority controls when a queued item is drained and injected into the LLM conversation:
//
//   - now:   Engine interrupts current tool call immediately. Not used by attachments.
//   - next:  Engine drains at turn boundary (after tool results, before next LLM call).
//     Default for both prompt and job modes. Attachments injected mid-query.
//   - later: Engine drains only at query end (no-tool-use terminal path) or when idle.
//     For items that should not interrupt the current query.
//
// Drain call sites in engine.go:
//   - Turn boundary (after tool results): DrainByPriority(PriorityNext) → drains now + next
//   - No-tool-use terminal path:          DrainAll()                  → drains all
//   - ProcessAttachments (TUI idle):      DrainAll()                  → drains all
type QueuePriority string

const (
	PriorityNow   QueuePriority = "now"
	PriorityNext  QueuePriority = "next"
	PriorityLater QueuePriority = "later"
)

// QueuedItem is a pending item in the attachment queue.
// TS source: types/textInputTypes.ts:299-349 — QueuedCommand
type QueuedItem struct {
	Value     string         // XML or plain text
	Mode      ItemMode       // prompt | job
	Priority  QueuePriority  // now | next | later
	UUID      string         // unique identifier
	IsMeta    bool           // true = system-generated, hidden from rewind/brief
	Origin    *MessageOrigin // source context
	Timestamp time.Time
}

// ---------------------------------------------------------------------------
// Attachment — source: utils/attachments.ts:440 Attachment type union
// ---------------------------------------------------------------------------

// AttachmentType discriminates between attachment sub-types.
type AttachmentType string

const (
	AttachmentTypeQueued AttachmentType = "queued_item"
)

// Attachment carries metadata for an attachment message.
type Attachment struct {
	Type       AttachmentType `json:"type"`
	Prompt     string         `json:"prompt,omitempty"`
	SourceUUID string         `json:"source_uuid,omitempty"`
	Mode       ItemMode       `json:"mode,omitempty"`
	Origin     *MessageOrigin `json:"origin,omitempty"`
	IsMeta     bool           `json:"is_meta"`
}

// ---------------------------------------------------------------------------
// MessageOrigin — source: types/message.ts MessageOrigin
// ---------------------------------------------------------------------------

// MessageOriginKind identifies where a message came from.
type MessageOriginKind string

const (
	OriginJob         MessageOriginKind = "job"
	OriginHuman       MessageOriginKind = "human"
	OriginCoordinator MessageOriginKind = "coordinator"
	OriginChannel     MessageOriginKind = "channel"
)

// MessageOrigin describes the source of a message.
type MessageOrigin struct {
	Kind MessageOriginKind `json:"kind"`
}

// ---------------------------------------------------------------------------
// MessageType — source: TS discriminated union UserMessage vs AttachmentMessage
// ---------------------------------------------------------------------------

// MessageType distinguishes user messages from attachment messages.
// Empty string is treated as "user" (backward compat).
type MessageType string

const (
	MessageTypeUser       MessageType = "user"
	MessageTypeAttachment MessageType = "attachment"
)
