package types

import (
	"encoding/json"
	"testing"
)

func TestItemModeConstants(t *testing.T) {
	if ItemModePrompt != "prompt" {
		t.Errorf("ItemModePrompt = %q, want %q", ItemModePrompt, "prompt")
	}
	if ItemModeJob != "job" {
		t.Errorf("ItemModeJob = %q, want %q", ItemModeJob, "job")
	}
}

func TestQueuePriorityConstants(t *testing.T) {
	if PriorityNow != "now" {
		t.Errorf("PriorityNow = %q, want %q", PriorityNow, "now")
	}
	if PriorityNext != "next" {
		t.Errorf("PriorityNext = %q, want %q", PriorityNext, "next")
	}
	if PriorityLater != "later" {
		t.Errorf("PriorityLater = %q, want %q", PriorityLater, "later")
	}
}

func TestMessageOriginSerialization(t *testing.T) {
	orig := &MessageOrigin{Kind: OriginJob}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal MessageOrigin: %v", err)
	}
	var got MessageOrigin
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal MessageOrigin: %v", err)
	}
	if got.Kind != OriginJob {
		t.Errorf("Kind = %q, want %q", got.Kind, OriginJob)
	}
}

func TestAttachmentSerialization(t *testing.T) {
	att := &Attachment{
		Type:   AttachmentTypeQueued,
		Prompt: "do something",
		Mode:   ItemModeJob,
		Origin: &MessageOrigin{Kind: OriginJob},
		IsMeta: true,
	}
	b, err := json.Marshal(att)
	if err != nil {
		t.Fatalf("marshal Attachment: %v", err)
	}
	var got Attachment
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Attachment: %v", err)
	}
	if got.Type != AttachmentTypeQueued {
		t.Errorf("Type = %q, want %q", got.Type, AttachmentTypeQueued)
	}
	if got.Prompt != "do something" {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "do something")
	}
	if got.Mode != ItemModeJob {
		t.Errorf("Mode = %q, want %q", got.Mode, ItemModeJob)
	}
	if got.Origin == nil || got.Origin.Kind != OriginJob {
		t.Errorf("Origin.Kind = %v, want %q", got.Origin, OriginJob)
	}
	if !got.IsMeta {
		t.Error("IsMeta = false, want true")
	}
}

func TestAttachmentSerialization_OmitsEmpty(t *testing.T) {
	att := Attachment{Type: AttachmentTypeQueued, IsMeta: false}
	b, err := json.Marshal(att)
	if err != nil {
		t.Fatalf("marshal Attachment: %v", err)
	}
	var got Attachment
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Attachment: %v", err)
	}
	if got.Prompt != "" {
		t.Errorf("Prompt = %q, want empty", got.Prompt)
	}
	if got.Origin != nil {
		t.Errorf("Origin = %v, want nil", got.Origin)
	}
}

func TestMessageTypeConstants(t *testing.T) {
	if MessageTypeUser != "user" {
		t.Errorf("MessageTypeUser = %q, want %q", MessageTypeUser, "user")
	}
	if MessageTypeAttachment != "attachment" {
		t.Errorf("MessageTypeAttachment = %q, want %q", MessageTypeAttachment, "attachment")
	}
}

func TestMessageMetadata_RoundTrip_Attachment(t *testing.T) {
	msg := Message{
		Role:        RoleUser,
		MessageType: MessageTypeAttachment,
		Attachment: &Attachment{
			Type:   AttachmentTypeQueued,
			Prompt: "job done",
			Mode:   ItemModeJob,
			Origin: &MessageOrigin{Kind: OriginJob},
			IsMeta: true,
		},
	}
	meta := msg.MetadataToJSON()
	if meta == "" {
		t.Fatal("MetadataToJSON returned empty string for attachment message")
	}

	var restored Message
	restored.SetMetadataFromJSON(meta)
	if restored.MessageType != MessageTypeAttachment {
		t.Errorf("MessageType = %q, want %q", restored.MessageType, MessageTypeAttachment)
	}
	if restored.Attachment == nil {
		t.Fatal("Attachment is nil after SetMetadataFromJSON")
	}
	if restored.Attachment.Prompt != "job done" {
		t.Errorf("Attachment.Prompt = %q, want %q", restored.Attachment.Prompt, "job done")
	}
	if restored.Attachment.Mode != ItemModeJob {
		t.Errorf("Attachment.Mode = %q, want %q", restored.Attachment.Mode, ItemModeJob)
	}
	if restored.Attachment.Origin == nil || restored.Attachment.Origin.Kind != OriginJob {
		t.Errorf("Attachment.Origin.Kind = %v, want %q", restored.Attachment.Origin, OriginJob)
	}
	if !restored.Attachment.IsMeta {
		t.Error("Attachment.IsMeta = false, want true")
	}
}

func TestMessageMetadata_EmptyValues_NoOutput(t *testing.T) {
	msg := Message{Role: RoleUser}
	meta := msg.MetadataToJSON()
	if meta != "" {
		t.Errorf("MetadataToJSON = %q, want empty for zero-value message", meta)
	}
}
