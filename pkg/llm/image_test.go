package llm

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestValidateImagesForAPI_ValidImage(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("look at this"),
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}),
		},
	}}
	if err := ValidateImagesForAPI(msgs); err != nil {
		t.Fatalf("ValidateImagesForAPI returned error for valid image: %v", err)
	}
}

func TestValidateImagesForAPI_Oversized(t *testing.T) {
	t.Parallel()

	// Build a base64 string strictly larger than API_IMAGE_MAX_BASE64_SIZE.
	oversized := strings.Repeat("A", API_IMAGE_MAX_BASE64_SIZE+1)
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: oversized}),
		},
	}}
	err := ValidateImagesForAPI(msgs)
	if err == nil {
		t.Fatal("ValidateImagesForAPI returned nil for oversized image")
	}
	isErr, ok := err.(*ImageSizeError)
	if !ok {
		t.Fatalf("error type = %T, want *ImageSizeError", err)
	}
	if len(isErr.OversizedImages) != 1 {
		t.Fatalf("OversizedImages len = %d, want 1", len(isErr.OversizedImages))
	}
	if isErr.OversizedImages[0].Index != 1 {
		t.Errorf("OversizedImages[0].Index = %d, want 1", isErr.OversizedImages[0].Index)
	}
	if isErr.OversizedImages[0].Size != len(oversized) {
		t.Errorf("OversizedImages[0].Size = %d, want %d", isErr.OversizedImages[0].Size, len(oversized))
	}
	if isErr.MaxSize != API_IMAGE_MAX_BASE64_SIZE {
		t.Errorf("MaxSize = %d, want %d", isErr.MaxSize, API_IMAGE_MAX_BASE64_SIZE)
	}
	if !strings.Contains(err.Error(), "exceeds API limit") {
		t.Errorf("error message = %q, want it to contain %q", err.Error(), "exceeds API limit")
	}
}

func TestValidateImagesForAPI_SkipsAssistantAndNonBase64(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("A", API_IMAGE_MAX_BASE64_SIZE+1)
	msgs := []types.Message{
		// Assistant images are ignored.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: oversized}),
		}},
		// Non-base64 source ignored.
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "url", MediaType: "image/png", Data: oversized}),
		}},
		// Nil source ignored.
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeImage}}},
	}
	if err := ValidateImagesForAPI(msgs); err != nil {
		t.Fatalf("ValidateImagesForAPI returned error for skipped cases: %v", err)
	}
}

func TestValidateImagesForAPI_MultipleOversized(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("A", API_IMAGE_MAX_BASE64_SIZE+1)
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: oversized}),
			types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: oversized}),
		},
	}}
	err := ValidateImagesForAPI(msgs)
	if err == nil {
		t.Fatal("expected error for multiple oversized images")
	}
	isErr := err.(*ImageSizeError)
	if len(isErr.OversizedImages) != 2 {
		t.Fatalf("OversizedImages len = %d, want 2", len(isErr.OversizedImages))
	}
	if isErr.OversizedImages[0].Index != 1 || isErr.OversizedImages[1].Index != 2 {
		t.Errorf("indices = %d, %d, want 1, 2", isErr.OversizedImages[0].Index, isErr.OversizedImages[1].Index)
	}
	if !strings.Contains(err.Error(), "2 images exceed") {
		t.Errorf("error message = %q, want it to contain %q", err.Error(), "2 images exceed")
	}
}

func TestFormatFileSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   int
		want string
	}{
		{512, "512 bytes"},
		{1536, "1.5KB"},
		{1024, "1KB"}, // 1.0KB -> strip .0 -> 1KB
		{5 * 1024 * 1024, "5MB"},
		{6 * 1024 * 1024, "6MB"},
	}
	for _, c := range cases {
		got := formatFileSize(c.in)
		if got != c.want {
			t.Errorf("formatFileSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
