package llm

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestMaterialize_FileConverted(t *testing.T) {
	t.Parallel()
	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x01, 0x02}
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, imgBytes, 0o644); err != nil {
		t.Fatalf("write temp image: %v", err)
	}
	req := &Request{
		Messages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewFileImageBlock("image/png", path),
				},
			},
		},
	}
	if err := MaterializeFileImages(req); err != nil {
		t.Fatalf("MaterializeFileImages: %v", err)
	}
	cb := req.Messages[0].Content[0]
	if cb.Source == nil {
		t.Fatal("Source = nil after materialize")
	}
	if cb.Source.Type != "base64" {
		t.Errorf("Source.Type = %q, want %q", cb.Source.Type, "base64")
	}
	if cb.Source.Path != "" {
		t.Errorf("Source.Path = %q, want empty", cb.Source.Path)
	}
	if cb.Source.MediaType != "image/png" {
		t.Errorf("Source.MediaType = %q, want %q (must be preserved)", cb.Source.MediaType, "image/png")
	}
	decoded, err := base64.StdEncoding.DecodeString(cb.Source.Data)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(imgBytes) {
		t.Errorf("decoded data = %v, want %v", decoded, imgBytes)
	}
}

func TestMaterialize_Base64Untouched(t *testing.T) {
	t.Parallel()
	req := &Request{
		Messages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}},
				},
			},
		},
	}
	if err := MaterializeFileImages(req); err != nil {
		t.Fatalf("MaterializeFileImages: %v", err)
	}
	cb := req.Messages[0].Content[0]
	if cb.Source.Type != "base64" {
		t.Errorf("Type = %q, want base64 (untouched)", cb.Source.Type)
	}
	if cb.Source.Data != "iVBORw0KGgo=" {
		t.Errorf("Data = %q, want original (untouched)", cb.Source.Data)
	}
}

func TestMaterialize_NonImageUntouched(t *testing.T) {
	t.Parallel()
	req := &Request{
		Messages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewTextBlock("hello"),
					types.NewToolUseBlock("id-1", "Bash", nil),
				},
			},
		},
	}
	if err := MaterializeFileImages(req); err != nil {
		t.Fatalf("MaterializeFileImages: %v", err)
	}
	if req.Messages[0].Content[0].Text != "hello" {
		t.Errorf("text block altered: %q", req.Messages[0].Content[0].Text)
	}
	if req.Messages[0].Content[1].Name != "Bash" {
		t.Errorf("tool_use block altered: %q", req.Messages[0].Content[1].Name)
	}
}

func TestMaterialize_MissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist.png")
	req := &Request{
		Messages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewFileImageBlock("image/png", missing),
				},
			},
		},
	}
	err := MaterializeFileImages(req)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %q, want it to contain the path %q", err.Error(), missing)
	}
}

func TestMaterialize_MultipleBlocksMixed(t *testing.T) {
	t.Parallel()
	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, imgBytes, 0o644); err != nil {
		t.Fatalf("write temp image: %v", err)
	}
	req := &Request{
		Messages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.NewFileImageBlock("image/png", path), // file → converted
					{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "/9j/4AAQ"}}, // base64 → untouched
					types.NewTextBlock("caption"), // text → untouched
				},
			},
		},
	}
	if err := MaterializeFileImages(req); err != nil {
		t.Fatalf("MaterializeFileImages: %v", err)
	}
	c0 := req.Messages[0].Content[0]
	if c0.Source.Type != "base64" {
		t.Errorf("block 0 Type = %q, want base64 (file converted)", c0.Source.Type)
	}
	if c0.Source.Path != "" {
		t.Errorf("block 0 Path = %q, want empty", c0.Source.Path)
	}
	c1 := req.Messages[0].Content[1]
	if c1.Source.Type != "base64" || c1.Source.Data != "/9j/4AAQ" {
		t.Errorf("block 1 altered: Type=%q Data=%q (base64 must be untouched)", c1.Source.Type, c1.Source.Data)
	}
	c2 := req.Messages[0].Content[2]
	if c2.Text != "caption" {
		t.Errorf("block 2 Text = %q, want 'caption'", c2.Text)
	}
}

func TestMaterialize_ImageBlockNilSourceUntouched(t *testing.T) {
	t.Parallel()
	// An image block with nil Source must not panic — it is skipped.
	req := &Request{
		Messages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeImage, Source: nil},
				},
			},
		},
	}
	if err := MaterializeFileImages(req); err != nil {
		t.Fatalf("MaterializeFileImages: %v", err)
	}
	if req.Messages[0].Content[0].Source != nil {
		t.Error("nil Source should remain nil")
	}
}

func TestMaterialize_NoMessages(t *testing.T) {
	t.Parallel()
	req := &Request{Messages: nil}
	if err := MaterializeFileImages(req); err != nil {
		t.Fatalf("MaterializeFileImages on nil messages: %v", err)
	}
	// Assert messages stayed nil — no mutation.
	if req.Messages != nil {
		t.Errorf("Messages = %v, want nil (untouched)", req.Messages)
	}
}
